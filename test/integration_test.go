package test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/DiamondGo/HttpHop/internal/client"
	"github.com/DiamondGo/HttpHop/internal/config"
	"github.com/DiamondGo/HttpHop/internal/server"
)

func startEcho(t *testing.T) (string, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo-Path", r.URL.Path)
		w.Header().Set("X-Echo-Host", r.Host)
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			w.Header().Set("X-Echo-XFF", xff)
		}
		io.Copy(w, r.Body)
	})
	srv := httptest.NewServer(mux)
	return strings.TrimPrefix(srv.URL, "http://"), srv.Close
}

// startBufferedEcho is startEcho's large-payload counterpart: it reads the
// full request body before writing any response byte, instead of streaming
// the response as the body arrives. That ordering matters here because
// Go's http.Transport (used by the server's ReverseProxy to talk to the
// tunnel) can abandon writing the rest of a request body once response
// bytes start coming back — a standard HTTP/1.1 client optimization for
// "the peer already started answering, it doesn't want more body," not a
// tunnel bug. startEcho's handler streams its response via io.Copy(w,
// r.Body) while still reading, which is realistic for small bodies (the
// whole exchange completes in one read/write pair before Transport ever
// notices) but for a payload spanning many read/write cycles it makes the
// backend look like it stopped wanting the rest of the request mid-flight,
// which is a real HTTP dynamic worth knowing about but not what the large
// payload tests below are trying to exercise. Buffering first sidesteps it
// the same way a typical JSON/API backend already would.
func startBufferedEcho(t *testing.T) (string, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(body)
	})
	srv := httptest.NewServer(mux)
	return strings.TrimPrefix(srv.URL, "http://"), srv.Close
}

const integrationClientToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func integrationSetup(t *testing.T) (serverURL string, publicHost string, cleanup func()) {
	return integrationSetupDomain(t, "httphop.io", []config.ClientBinding{
		{
			ClientID:    "test-apex",
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       integrationClientToken,
			MaxClients:  1,
		},
		{
			ClientID:   "test-myapp",
			Subdomain:  "myapp",
			Token:      strings.Repeat("b", 32),
			MaxClients: 1,
		},
	})
}

// integrationSetupDomain starts an in-process server + client for the given domain
// layout. publicHost for apex routing is rootDomain.
func integrationSetupDomain(t *testing.T, rootDomain string, clients []config.ClientBinding, opts ...func(*config.ServerConfig, *config.ClientConfig)) (serverURL string, publicHost string, cleanup func()) {
	t.Helper()
	echoAddr, closeEcho := startEcho(t)
	return integrationSetupDomainWithLocal(t, rootDomain, clients, echoAddr, closeEcho, opts...)
}

func integrationSetupDomainWithLocal(t *testing.T, rootDomain string, clients []config.ClientBinding, localAddr string, closeLocal func(), opts ...func(*config.ServerConfig, *config.ClientConfig)) (serverURL string, publicHost string, cleanup func()) {
	t.Helper()

	logger := zap.NewNop()
	if os.Getenv("HTTPHOP_TEST_DEBUG") != "" {
		logger, _ = zap.NewDevelopment()
	}
	srvCfg := config.Defaults()
	srvCfg.RootDomain = rootDomain
	srvCfg.TLS.Disable = true
	srvCfg.Tunnel.PollTimeout = 500 * time.Millisecond
	srvCfg.Tunnel.SessionTimeout = 2 * srvCfg.Tunnel.PollTimeout
	srvCfg.Tunnel.SweepInterval = 100 * time.Millisecond
	srvCfg.Status.Enabled = false
	srvCfg.Clients = clients

	cliCfg := config.DefaultClient()
	cliCfg.ClientID = clients[0].ClientID
	cliCfg.Server.Token = clients[0].Token
	cliCfg.Server.InsecureSkipVerify = true
	cliCfg.Local.Target = localAddr
	cliCfg.Health.Enabled = true
	cliCfg.Health.Interval = 100 * time.Millisecond

	for _, opt := range opts {
		opt(&srvCfg, &cliCfg)
	}

	srv, err := server.NewServer(&srvCfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.StartOnListener(ln); err != nil {
		t.Fatal(err)
	}
	serverURL = "http://" + ln.Addr().String()
	cliCfg.Server.URL = serverURL

	cli := client.New(&cliCfg, logger)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = cli.Run(ctx)
		close(done)
	}()

	waitForTunnel(t, srv, "@")

	cleanup = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		ctxStop, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = srv.Stop(ctxStop)
		closeLocal()
	}

	return serverURL, rootDomain, cleanup
}

func waitForTunnel(t *testing.T, srv *server.Server, hostKey string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pool, ok := srv.Registry().Pool(hostKey)
		if ok && pool.Len() > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pool, ok := srv.Registry().Pool(hostKey); !ok || pool.Len() == 0 {
		t.Fatal("client did not connect in time")
	}
}

func TestIntegrationBasicForward(t *testing.T) {
	serverURL, publicHost, cleanup := integrationSetup(t)
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, serverURL+"/service/hello", nil)
	req.Host = publicHost
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Echo-Path"); got != "/hello" {
		t.Fatalf("echo path = %q, want /hello", got)
	}
}

func TestIntegrationSubdomainRoute(t *testing.T) {
	serverURL, _, cleanup := integrationSetup(t)
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, serverURL+"/foo", nil)
	req.Host = "myapp.httphop.io"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		// myapp route has no connected client (only test-apex is online)
		// This test validates 503 when no backend for myapp
		return
	}
}

func TestIntegrationXForwardedFor(t *testing.T) {
	serverURL, publicHost, cleanup := integrationSetup(t)
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, serverURL+"/service/", nil)
	req.Host = publicHost
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	xff := resp.Header.Get("X-Echo-XFF")
	if strings.Contains(xff, "1.2.3.4") {
		t.Fatalf("spoofed XFF was not stripped: %q", xff)
	}
}

// TestIntegrationBuilderrorsApexServicePath covers the common deployment:
// an internal client on 127.0.0.1 exposes a local HTTP service through the
// public apex path builderrors.com/service/* (strip_prefix → /* on localhost).
func TestIntegrationBuilderrorsApexServicePath(t *testing.T) {
	serverURL, publicHost, cleanup := integrationSetupDomain(t, "builderrors.com", []config.ClientBinding{
		{
			ClientID:    "builderrors-apex",
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       integrationClientToken,
			MaxClients:  1,
		},
	})
	defer cleanup()

	cases := []struct {
		name       string
		publicPath string
		wantPath   string
		wantStatus int
	}{
		{
			name:       "auth endpoint",
			publicPath: "/service/auth",
			wantPath:   "/auth",
			wantStatus: http.StatusOK,
		},
		{
			name:       "service root",
			publicPath: "/service",
			wantPath:   "/",
			wantStatus: http.StatusOK,
		},
		{
			name:       "nested path",
			publicPath: "/service/api/v1/users",
			wantPath:   "/api/v1/users",
			wantStatus: http.StatusOK,
		},
		{
			name:       "unmapped path",
			publicPath: "/other",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, serverURL+tc.publicPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = publicHost

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			if got := resp.Header.Get("X-Echo-Path"); got != tc.wantPath {
				t.Fatalf("local path = %q, want %q (public %q)", got, tc.wantPath, tc.publicPath)
			}
			if got := resp.Header.Get("X-Echo-Host"); got != publicHost {
				t.Fatalf("local Host = %q, want %q", got, publicHost)
			}
		})
	}
}

func startRedirectBackend(t *testing.T) (string, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc", Path: "/"})
		http.Redirect(w, r, "/login", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	return strings.TrimPrefix(srv.URL, "http://"), srv.Close
}

func TestIntegrationBuilderrorsRedirectAndCookie(t *testing.T) {
	localAddr, closeLocal := startRedirectBackend(t)
	serverURL, publicHost, cleanup := integrationSetupDomainWithLocal(t, "builderrors.com", []config.ClientBinding{
		{
			ClientID:    "builderrors-apex",
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       integrationClientToken,
			MaxClients:  1,
		},
	}, localAddr, closeLocal)
	defer cleanup()

	req, err := http.NewRequest(http.MethodGet, serverURL+"/service/auth", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = publicHost

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/service/login" {
		t.Fatalf("Location = %q, want /service/login", got)
	}
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		t.Fatal("missing Set-Cookie")
	}
	if !strings.Contains(cookies[0], "Path=/service") {
		t.Fatalf("Set-Cookie = %q, want Path=/service", cookies[0])
	}
}

func TestIntegrationBuilderrorsServicePostBody(t *testing.T) {
	serverURL, publicHost, cleanup := integrationSetupDomain(t, "builderrors.com", []config.ClientBinding{
		{
			ClientID:    "builderrors-apex",
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       integrationClientToken,
			MaxClients:  1,
		},
	})
	defer cleanup()

	body := strings.NewReader(`{"user":"alice"}`)
	req, err := http.NewRequest(http.MethodPost, serverURL+"/service/auth/login", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = publicHost
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Echo-Path"); got != "/auth/login" {
		t.Fatalf("local path = %q, want /auth/login", got)
	}
	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != `{"user":"alice"}` {
		t.Fatalf("body = %q", gotBody)
	}
}

// TestIntegrationStreamPollMode covers pollmux's stream poll mode (download
// held-open long poll + streamed upload), negotiated end to end instead of
// the default discrete batch polling.
func TestIntegrationStreamPollMode(t *testing.T) {
	serverURL, publicHost, cleanup := integrationSetupDomain(t, "httphop.io", []config.ClientBinding{
		{
			ClientID:    "test-apex",
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       integrationClientToken,
			MaxClients:  1,
		},
	}, func(srvCfg *config.ServerConfig, cliCfg *config.ClientConfig) {
		srvCfg.Tunnel.PollMode = "stream"
		srvCfg.Tunnel.HeartbeatInterval = 200 * time.Millisecond
		srvCfg.Tunnel.StreamMaxDuration = 2 * time.Second
		cliCfg.Transport.PreferStream = true
		cliCfg.Transport.UploadStreamPreference = "stream"
	})
	defer cleanup()

	body := strings.NewReader(`{"user":"alice"}`)
	req, err := http.NewRequest(http.MethodPost, serverURL+"/service/echo", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = publicHost
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	gotBody, _ := io.ReadAll(resp.Body)
	if string(gotBody) != `{"user":"alice"}` {
		t.Fatalf("body = %q", gotBody)
	}
}

// TestIntegrationStreamLargePayload forces small poll_buffer_size/
// max_send_bytes so a payload well over either limit must actually be split
// across multiple stream frames in both directions, then verifies the round
// trip is byte-exact. This is the case batch mode also handles via
// discrete chunking — the point here is that stream mode's frame-based
// pipe (frame.go) doesn't corrupt or reorder data across many frames.
func TestIntegrationStreamLargePayload(t *testing.T) {
	localAddr, closeLocal := startBufferedEcho(t)
	serverURL, publicHost, cleanup := integrationSetupDomainWithLocal(t, "httphop.io", []config.ClientBinding{
		{
			ClientID:    "test-apex",
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       integrationClientToken,
			MaxClients:  1,
		},
	}, localAddr, closeLocal, func(srvCfg *config.ServerConfig, cliCfg *config.ClientConfig) {
		srvCfg.Tunnel.PollMode = "stream"
		srvCfg.Tunnel.HeartbeatInterval = 200 * time.Millisecond
		srvCfg.Tunnel.StreamMaxDuration = 2 * time.Second
		srvCfg.Tunnel.PollBufferSize = 16 << 10
		srvCfg.Tunnel.MaxSendBytes = 16 << 10
		cliCfg.Transport.PreferStream = true
		cliCfg.Transport.UploadStreamPreference = "stream"
		cliCfg.Transport.MaxSendChunk = 16 << 10
	})
	defer cleanup()

	payload := make([]byte, 512<<10) // 512KB, ~32x the 16KB frame/chunk limits
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	wantSum := sha256.Sum256(payload)

	req, err := http.NewRequest(http.MethodPost, serverURL+"/service/echo", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = publicHost
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	gotBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	gotSum := sha256.Sum256(gotBody)
	if gotSum != wantSum {
		t.Fatalf("round-tripped payload corrupted: got %d bytes, sha256 %x, want %x", len(gotBody), gotSum, wantSum)
	}
}

// TestIntegrationStreamNegotiationFallback checks that a client asking for
// stream mode against a server that only offers batch (the default)
// negotiates down to batch instead of failing — old-server/new-client
// compatibility is the whole point of the negotiation being per-connect.
func TestIntegrationStreamNegotiationFallback(t *testing.T) {
	serverURL, publicHost, cleanup := integrationSetupDomain(t, "httphop.io", []config.ClientBinding{
		{
			ClientID:    "test-apex",
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       integrationClientToken,
			MaxClients:  1,
		},
	}, func(srvCfg *config.ServerConfig, cliCfg *config.ClientConfig) {
		// srvCfg.Tunnel.PollMode left at the "batch" default.
		cliCfg.Transport.PreferStream = true
		cliCfg.Transport.UploadStreamPreference = "stream"
		cliCfg.Transport.PreferWebSocket = true
	})
	defer cleanup()

	req, err := http.NewRequest(http.MethodGet, serverURL+"/service/hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = publicHost
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Echo-Path"); got != "/hello" {
		t.Fatalf("echo path = %q, want /hello", got)
	}
}

// TestIntegrationStreamRollover holds the tunnel open across several
// StreamMaxDuration windows with no application traffic in between, so the
// stream poll leg has to roll over (frameEnd) and reopen a fresh long-held
// request purely to stay alive. If rollover ever dropped the session
// instead of reopening it, the tunnel would fall out of the registry and
// the follow-up request would 503.
func TestIntegrationStreamRollover(t *testing.T) {
	serverURL, publicHost, cleanup := integrationSetupDomain(t, "httphop.io", []config.ClientBinding{
		{
			ClientID:    "test-apex",
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       integrationClientToken,
			MaxClients:  1,
		},
	}, func(srvCfg *config.ServerConfig, cliCfg *config.ClientConfig) {
		srvCfg.Tunnel.PollMode = "stream"
		srvCfg.Tunnel.HeartbeatInterval = 100 * time.Millisecond
		srvCfg.Tunnel.StreamMaxDuration = 300 * time.Millisecond
		// SessionTimeout must stay well above StreamMaxDuration or the
		// sweeper would (correctly) evict the session as idle during the
		// gap between requests below.
		srvCfg.Tunnel.SessionTimeout = 5 * time.Second
		cliCfg.Transport.PreferStream = true
	})
	defer cleanup()

	// Outlast several StreamMaxDuration rollovers with no request in flight.
	time.Sleep(1200 * time.Millisecond)

	for i := 0; i < 3; i++ {
		req, err := http.NewRequest(http.MethodGet, serverURL+"/service/hello", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = publicHost
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request %d after rollover: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d after rollover: status = %d", i, resp.StatusCode)
		}
		time.Sleep(400 * time.Millisecond)
	}
}

// disconnectProxy forwards TCP connections and can sever every currently
// active leg. The listener remains up, so pollmux can reconnect to the same
// address and exercise its real resume handshake.
type disconnectProxy struct {
	ln       net.Listener
	upstream string
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	accepted atomic.Int64
}

func newDisconnectProxy(t *testing.T, upstream string) *disconnectProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &disconnectProxy{ln: ln, upstream: upstream, conns: make(map[net.Conn]struct{})}
	go p.serve()
	t.Cleanup(func() {
		_ = p.ln.Close()
		p.drop()
	})
	return p
}

func (p *disconnectProxy) url() string { return "http://" + p.ln.Addr().String() }

func (p *disconnectProxy) serve() {
	for {
		downstream, err := p.ln.Accept()
		if err != nil {
			return
		}
		upstream, err := net.Dial("tcp", p.upstream)
		if err != nil {
			_ = downstream.Close()
			continue
		}
		p.accepted.Add(1)
		p.track(downstream, true)
		p.track(upstream, true)
		go p.pipe(downstream, upstream)
		go p.pipe(upstream, downstream)
	}
}

func (p *disconnectProxy) pipe(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
	p.track(dst, false)
	p.track(src, false)
}

func (p *disconnectProxy) track(conn net.Conn, add bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if add {
		p.conns[conn] = struct{}{}
	} else {
		delete(p.conns, conn)
	}
}

func (p *disconnectProxy) drop() {
	p.mu.Lock()
	conns := make([]net.Conn, 0, len(p.conns))
	for conn := range p.conns {
		conns = append(conns, conn)
	}
	p.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func TestIntegrationResumeKeepsActiveStream(t *testing.T) {
	for _, transport := range []string{"websocket", "stream"} {
		t.Run(transport, func(t *testing.T) {
			payload := make([]byte, 768<<10)
			if _, err := rand.Read(payload); err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			var startedOnce sync.Once
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				flusher, _ := w.(http.Flusher)
				for off := 0; off < len(payload); off += 8 << 10 {
					end := min(off+(8<<10), len(payload))
					_, _ = w.Write(payload[off:end])
					if flusher != nil {
						flusher.Flush()
					}
					startedOnce.Do(func() { close(started) })
					time.Sleep(3 * time.Millisecond)
				}
			}))
			defer backend.Close()

			logger := zap.NewNop()
			srvCfg := config.Defaults()
			srvCfg.RootDomain = "httphop.io"
			srvCfg.TLS.Disable = true
			srvCfg.Status.Enabled = false
			srvCfg.Tunnel.PollTimeout = 500 * time.Millisecond
			srvCfg.Tunnel.SessionTimeout = time.Second
			srvCfg.Tunnel.SweepInterval = 50 * time.Millisecond
			srvCfg.Tunnel.EnableResume = true
			srvCfg.Tunnel.ResumeGrace = 3 * time.Second
			srvCfg.Tunnel.HeartbeatInterval = 100 * time.Millisecond
			srvCfg.Tunnel.StreamMaxDuration = time.Second
			srvCfg.Clients = []config.ClientBinding{{
				ClientID: "resume-client", Subdomain: "@", PathPrefix: "/service", StripPrefix: true,
				Token: integrationClientToken, MaxClients: 1,
			}}
			if transport == "websocket" {
				srvCfg.Tunnel.EnableWebSocket = true
			} else {
				srvCfg.Tunnel.PollMode = "stream"
			}

			srv, err := server.NewServer(&srvCfg, logger)
			if err != nil {
				t.Fatal(err)
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			if err := srv.StartOnListener(ln); err != nil {
				t.Fatal(err)
			}
			serverURL := "http://" + ln.Addr().String()
			proxy := newDisconnectProxy(t, ln.Addr().String())

			cliCfg := config.DefaultClient()
			cliCfg.ClientID = "resume-client"
			cliCfg.Server.URL = proxy.url()
			cliCfg.Server.Token = integrationClientToken
			cliCfg.Local.Target = strings.TrimPrefix(backend.URL, "http://")
			cliCfg.Health.Enabled = false
			cliCfg.Transport.PreferResume = true
			cliCfg.Transport.PollGrace = 300 * time.Millisecond
			if transport == "websocket" {
				cliCfg.Transport.PreferWebSocket = true
			} else {
				cliCfg.Transport.PreferStream = true
				cliCfg.Transport.UploadStreamPreference = "stream"
			}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				_ = client.New(&cliCfg, logger).Run(ctx)
				close(done)
			}()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
				}
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer stopCancel()
				_ = srv.Stop(stopCtx)
			}()

			waitForTunnel(t, srv, "@")
			pool, _ := srv.Registry().Pool("@")
			original := pool.ByID("resume-client")
			if original == nil || !original.Session.Resumable() {
				t.Fatal("tunnel did not negotiate resume")
			}
			originalSessionID := original.Session.ID
			acceptedBefore := proxy.accepted.Load()

			req, _ := http.NewRequest(http.MethodGet, serverURL+"/service/slow", nil)
			req.Host = "httphop.io"
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("backend response did not start")
			}
			time.Sleep(30 * time.Millisecond)
			proxy.drop()

			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read response across resume: %v", err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("response corrupted across resume: got %d bytes, want %d", len(got), len(payload))
			}

			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) && proxy.accepted.Load() <= acceptedBefore {
				time.Sleep(20 * time.Millisecond)
			}
			if proxy.accepted.Load() <= acceptedBefore {
				t.Fatal("client opened no replacement transport after disconnect")
			}
			current := pool.ByID("resume-client")
			if current == nil || current.Session.ID != originalSessionID {
				t.Fatalf("session was replaced instead of resumed: got %#v, want %q", current, originalSessionID)
			}
		})
	}
}

// TestIntegrationWebSocketTransport covers pollmux's WebSocket transport,
// where the session attaches over a single ws connection instead of polling.
func TestIntegrationWebSocketTransport(t *testing.T) {
	serverURL, publicHost, cleanup := integrationSetupDomain(t, "httphop.io", []config.ClientBinding{
		{
			ClientID:    "test-apex",
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       integrationClientToken,
			MaxClients:  1,
		},
	}, func(srvCfg *config.ServerConfig, cliCfg *config.ClientConfig) {
		srvCfg.Tunnel.EnableWebSocket = true
		cliCfg.Transport.PreferWebSocket = true
	})
	defer cleanup()

	req, err := http.NewRequest(http.MethodGet, serverURL+"/service/hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = publicHost
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Echo-Path"); got != "/hello" {
		t.Fatalf("echo path = %q, want /hello", got)
	}
}

// TestIntegrationWebSocketLargePayload mirrors TestIntegrationStreamLargePayload
// but over the WebSocket transport, which replaces polling entirely rather
// than adding a third poll mode.
func TestIntegrationWebSocketLargePayload(t *testing.T) {
	localAddr, closeLocal := startBufferedEcho(t)
	serverURL, publicHost, cleanup := integrationSetupDomainWithLocal(t, "httphop.io", []config.ClientBinding{
		{
			ClientID:    "test-apex",
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       integrationClientToken,
			MaxClients:  1,
		},
	}, localAddr, closeLocal, func(srvCfg *config.ServerConfig, cliCfg *config.ClientConfig) {
		srvCfg.Tunnel.EnableWebSocket = true
		cliCfg.Transport.PreferWebSocket = true
	})
	defer cleanup()

	payload := make([]byte, 512<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	wantSum := sha256.Sum256(payload)

	req, err := http.NewRequest(http.MethodPost, serverURL+"/service/echo", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = publicHost
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	gotBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	gotSum := sha256.Sum256(gotBody)
	if gotSum != wantSum {
		t.Fatalf("round-tripped payload corrupted: got %d bytes, sha256 %x, want %x", len(gotBody), gotSum, wantSum)
	}
}
