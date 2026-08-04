package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DiamondGo/pollmux"
	"go.uber.org/zap"

	"github.com/DiamondGo/HttpHop/internal/config"
	"github.com/DiamondGo/HttpHop/internal/server"
)

func testServerConfig(token string) *config.ServerConfig {
	cfg := config.Defaults()
	cfg.RootDomain = "httphop.io"
	cfg.ControlHost = "tunnel.httphop.io"
	cfg.TLS.Disable = true
	cfg.Tunnel.PollTimeout = 200 * time.Millisecond
	cfg.Tunnel.SessionTimeout = 2 * cfg.Tunnel.PollTimeout
	cfg.Tunnel.SweepInterval = 50 * time.Millisecond
	cfg.Status.Enabled = true
	cfg.Tunnels = []config.TunnelBinding{{
		Subdomain:  "myapp",
		Token:      token,
		MaxClients: 1,
	}}
	return &cfg
}

func startTestServer(t *testing.T, cfg *config.ServerConfig) (*server.Server, string) {
	t.Helper()
	logger := zap.NewNop()
	srv, err := server.NewServer(cfg, logger)
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
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})
	return srv, "http://" + ln.Addr().String()
}

func TestControlConnectPollDelete(t *testing.T) {
	token := strings.Repeat("t", 32)
	cfg := testServerConfig(token)
	_, baseURL := startTestServer(t, cfg)

	body, _ := json.Marshal(pollmux.ConnectRequest{
		ProtocolVersion: pollmux.ProtocolVersion,
		Meta:            map[string]string{"client_id": "test-client"},
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/connect", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("connect status %d: %s", resp.StatusCode, b)
	}

	var cr pollmux.ConnectResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	if cr.SessionID == "" {
		t.Fatal("empty session id")
	}
	if cr.Meta["subdomain"] != "myapp" {
		t.Fatalf("meta subdomain = %q", cr.Meta["subdomain"])
	}
	if cr.Limits.PollTimeoutMS == 0 {
		t.Fatal("missing limits")
	}

	pollReq, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/"+cr.SessionID+"/poll", nil)
	pollReq.Header.Set("Authorization", "Bearer "+token)
	pollReq.Header.Set("X-Receive-Only", "true")
	pollResp, err := http.DefaultClient.Do(pollReq)
	if err != nil {
		t.Fatal(err)
	}
	pollResp.Body.Close()
	if pollResp.StatusCode != http.StatusNoContent {
		t.Fatalf("poll status %d", pollResp.StatusCode)
	}

	delReq, _ := http.NewRequest(http.MethodDelete, baseURL+"/tunnel/"+cr.SessionID, nil)
	delReq.Header.Set("Authorization", "Bearer "+token)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete status %d", delResp.StatusCode)
	}
}

func TestControlInvalidToken(t *testing.T) {
	token := strings.Repeat("t", 32)
	cfg := testServerConfig(token)
	_, baseURL := startTestServer(t, cfg)

	body, _ := json.Marshal(pollmux.ConnectRequest{
		ProtocolVersion: pollmux.ProtocolVersion,
		Meta:            map[string]string{"client_id": "x"},
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/connect", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestCloseSessionReturns410(t *testing.T) {
	token := strings.Repeat("t", 32)
	cfg := testServerConfig(token)
	srv, baseURL := startTestServer(t, cfg)

	body, _ := json.Marshal(pollmux.ConnectRequest{
		ProtocolVersion: pollmux.ProtocolVersion,
		Meta:            map[string]string{"client_id": "c1"},
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/connect", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	var cr pollmux.ConnectResponse
	json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()

	sess, ok := srv.SessionStore().Get(cr.SessionID)
	if !ok {
		t.Fatal("session not found")
	}
	_ = sess.Close()

	pollReq, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/"+cr.SessionID+"/poll", nil)
	pollReq.Header.Set("Authorization", "Bearer "+token)
	pollReq.Header.Set("X-Receive-Only", "true")
	pollResp, _ := http.DefaultClient.Do(pollReq)
	pollResp.Body.Close()
	if pollResp.StatusCode != http.StatusGone {
		t.Fatalf("expected 410, got %d", pollResp.StatusCode)
	}
}
