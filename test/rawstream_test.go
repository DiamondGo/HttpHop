package test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/DiamondGo/HttpHop/internal/client"
	"github.com/DiamondGo/HttpHop/internal/config"
	"github.com/DiamondGo/HttpHop/internal/server"
)

// startRawEcho starts a plain TCP echo server (no HTTP), to isolate whether
// data corruption comes from HttpHop's own tunnel/bridging plumbing or from
// Go's http.Transport/httputil.ReverseProxy layered on top of it.
func startRawEcho(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

// TestRawStreamBidirectionalLargePayload opens a yamux stream directly
// through the real server/client tunnel (OpenStream + StreamHandler.Handle)
// and echoes a large payload over raw TCP, with no HTTP framing and no
// httputil.ReverseProxy in the path. This isolates the tunnel's own
// bidirectional bridging (internal/client/handler.go's bridgeBidirectional,
// internal/server/bridge.go's OpenStream) from Go's net/http machinery.
func TestRawStreamBidirectionalLargePayload(t *testing.T) {
	rawAddr, closeRaw := startRawEcho(t)
	defer closeRaw()

	logger := zap.NewNop()
	srvCfg := config.Defaults()
	srvCfg.RootDomain = "httphop.io"
	srvCfg.TLS.Disable = true
	srvCfg.Tunnel.PollTimeout = 500 * time.Millisecond
	srvCfg.Tunnel.SessionTimeout = 2 * srvCfg.Tunnel.PollTimeout
	srvCfg.Tunnel.SweepInterval = 100 * time.Millisecond
	srvCfg.Status.Enabled = false
	srvCfg.Clients = []config.ClientBinding{
		{
			ClientID:   "test-apex",
			Subdomain:  "@",
			Token:      integrationClientToken,
			MaxClients: 1,
		},
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

	cliCfg := config.DefaultClient()
	cliCfg.ClientID = "test-apex"
	cliCfg.Server.Token = integrationClientToken
	cliCfg.Server.URL = "http://" + ln.Addr().String()
	cliCfg.Server.InsecureSkipVerify = true
	cliCfg.Local.Target = rawAddr
	cliCfg.Health.Enabled = false

	cli := client.New(&cliCfg, logger)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = cli.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		ctxStop, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = srv.Stop(ctxStop)
	}()

	waitForTunnel(t, srv, "@")

	pool, ok := srv.Registry().Pool("@")
	if !ok || pool.Len() == 0 {
		t.Fatal("no tunnel registered")
	}
	tun := pool.ByID("test-apex")
	if tun == nil {
		t.Fatal("tunnel not found")
	}

	stream, err := server.OpenStream(tun)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	payload := make([]byte, 512<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(payload)

	readDone := make(chan []byte, 1)
	go func() {
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(stream, got); err != nil {
			t.Logf("ReadFull error: %v", err)
			readDone <- nil
			return
		}
		readDone <- got
	}()

	if _, err := stream.Write(payload); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-readDone:
		if got == nil {
			t.Fatal("echo came back short/errored")
		}
		if sha256.Sum256(got) != want {
			t.Fatal("echoed bytes do not match what was sent")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("echo never came back")
	}
}
