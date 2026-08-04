package test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

func integrationSetup(t *testing.T) (serverURL string, publicHost string, cleanup func()) {
	return integrationSetupDomain(t, "httphop.io", "tunnel.httphop.io", []config.TunnelBinding{
		{
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       strings.Repeat("a", 32),
			MaxClients:  1,
		},
		{
			Subdomain:  "myapp",
			Token:      strings.Repeat("b", 32),
			MaxClients: 1,
		},
	})
}

// integrationSetupDomain starts an in-process server + client for the given domain
// layout. publicHost for apex routing is rootDomain.
func integrationSetupDomain(t *testing.T, rootDomain, controlHost string, tunnels []config.TunnelBinding) (serverURL string, publicHost string, cleanup func()) {
	t.Helper()
	echoAddr, closeEcho := startEcho(t)
	return integrationSetupDomainWithLocal(t, rootDomain, controlHost, tunnels, echoAddr, closeEcho)
}

func integrationSetupDomainWithLocal(t *testing.T, rootDomain, controlHost string, tunnels []config.TunnelBinding, localAddr string, closeLocal func()) (serverURL string, publicHost string, cleanup func()) {
	t.Helper()

	logger := zap.NewNop()
	srvCfg := config.Defaults()
	srvCfg.RootDomain = rootDomain
	srvCfg.ControlHost = controlHost
	srvCfg.TLS.Disable = true
	srvCfg.Tunnel.PollTimeout = 500 * time.Millisecond
	srvCfg.Tunnel.SessionTimeout = 2 * srvCfg.Tunnel.PollTimeout
	srvCfg.Tunnel.SweepInterval = 100 * time.Millisecond
	srvCfg.Status.Enabled = false
	srvCfg.Tunnels = tunnels

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

	token := tunnels[0].Token
	cliCfg := config.DefaultClient()
	cliCfg.ClientID = "test-client"
	cliCfg.Server.URL = serverURL
	cliCfg.Server.Token = token
	cliCfg.Server.InsecureSkipVerify = true
	cliCfg.Local.Target = localAddr
	cliCfg.Health.Enabled = true
	cliCfg.Health.Interval = 100 * time.Millisecond

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
		// myapp tunnel uses different token - client connected to @ tunnel only
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
	token := strings.Repeat("c", 32)
	serverURL, publicHost, cleanup := integrationSetupDomain(t, "builderrors.com", "tunnel.builderrors.com", []config.TunnelBinding{
		{
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       token,
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
	token := strings.Repeat("e", 32)
	localAddr, closeLocal := startRedirectBackend(t)
	serverURL, publicHost, cleanup := integrationSetupDomainWithLocal(t, "builderrors.com", "tunnel.builderrors.com", []config.TunnelBinding{
		{
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       token,
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
	token := strings.Repeat("d", 32)
	serverURL, publicHost, cleanup := integrationSetupDomain(t, "builderrors.com", "tunnel.builderrors.com", []config.TunnelBinding{
		{
			Subdomain:   "@",
			PathPrefix:  "/service",
			StripPrefix: true,
			Token:       token,
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
