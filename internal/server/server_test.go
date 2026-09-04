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

const testClientToken = "tttttttttttttttttttttttttttttttt"

func testServerConfig(clientID string) *config.ServerConfig {
	cfg := config.Defaults()
	cfg.RootDomain = "httphop.io"
	cfg.TLS.Disable = true
	cfg.Tunnel.PollTimeout = 200 * time.Millisecond
	cfg.Tunnel.SessionTimeout = 2 * cfg.Tunnel.PollTimeout
	cfg.Tunnel.SweepInterval = 50 * time.Millisecond
	cfg.Status.Enabled = true
	cfg.Clients = []config.ClientBinding{{
		ClientID:   clientID,
		Subdomain:  "myapp",
		Token:      testClientToken,
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
	const clientID = "test-client"
	cfg := testServerConfig(clientID)
	_, baseURL := startTestServer(t, cfg)

	body, _ := json.Marshal(pollmux.ConnectRequest{
		ProtocolVersion: pollmux.ProtocolVersion,
		Meta:            map[string]string{"client_id": clientID},
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/connect", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testClientToken)
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
	pollReq.Header.Set("Authorization", "Bearer "+testClientToken)
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
	delReq.Header.Set("Authorization", "Bearer "+testClientToken)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete status %d", delResp.StatusCode)
	}
}

func TestControlResumeNegotiationAndEndpoint(t *testing.T) {
	const clientID = "resume-client"
	cfg := testServerConfig(clientID)
	cfg.Tunnel.EnableWebSocket = true
	cfg.Tunnel.EnableResume = true
	cfg.Tunnel.ResumeGrace = 2 * time.Second
	_, baseURL := startTestServer(t, cfg)

	body, _ := json.Marshal(pollmux.ConnectRequest{
		ProtocolVersion: pollmux.ProtocolVersion,
		Meta:            map[string]string{"client_id": clientID},
		PreferWebSocket: true,
		PreferResume:    true,
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/connect", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testClientToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var cr pollmux.ConnectResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	if !cr.Resumable {
		t.Fatal("server did not negotiate resumable WebSocket transport")
	}
	if cr.Limits.ResumeGrace() != 2*time.Second {
		t.Fatalf("resume grace = %v, want 2s", cr.Limits.ResumeGrace())
	}

	resumeBody, _ := json.Marshal(pollmux.ResumeRequest{ProtocolVersion: pollmux.ProtocolVersion})
	resumeReq, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/"+cr.SessionID+"/resume", bytes.NewReader(resumeBody))
	resumeReq.Header.Set("Authorization", "Bearer "+testClientToken)
	resumeReq.Header.Set("Content-Type", "application/json")
	resumeResp, err := http.DefaultClient.Do(resumeReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resumeResp.Body.Close()
	if resumeResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resumeResp.Body)
		t.Fatalf("resume status %d: %s", resumeResp.StatusCode, b)
	}
	var rr pollmux.ResumeResponse
	if err := json.NewDecoder(resumeResp.Body).Decode(&rr); err != nil {
		t.Fatal(err)
	}
	if !rr.Resumed {
		t.Fatal("resume endpoint did not report success")
	}
}

func TestControlResumeRejectsWrongToken(t *testing.T) {
	const clientID = "resume-auth-client"
	cfg := testServerConfig(clientID)
	cfg.Tunnel.EnableWebSocket = true
	_, baseURL := startTestServer(t, cfg)

	body, _ := json.Marshal(pollmux.ConnectRequest{
		ProtocolVersion: pollmux.ProtocolVersion,
		Meta:            map[string]string{"client_id": clientID},
		PreferWebSocket: true,
		PreferResume:    true,
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/connect", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testClientToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var cr pollmux.ConnectResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resumeBody, _ := json.Marshal(pollmux.ResumeRequest{ProtocolVersion: pollmux.ProtocolVersion})
	resumeReq, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/"+cr.SessionID+"/resume", bytes.NewReader(resumeBody))
	resumeReq.Header.Set("Authorization", "Bearer wrong")
	resumeResp, err := http.DefaultClient.Do(resumeReq)
	if err != nil {
		t.Fatal(err)
	}
	resumeResp.Body.Close()
	if resumeResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("resume status = %d, want 401", resumeResp.StatusCode)
	}
}

func TestControlResumeNotNegotiatedForBatch(t *testing.T) {
	const clientID = "batch-client"
	cfg := testServerConfig(clientID)
	cfg.Tunnel.EnableResume = true
	_, baseURL := startTestServer(t, cfg)

	body, _ := json.Marshal(pollmux.ConnectRequest{
		ProtocolVersion: pollmux.ProtocolVersion,
		Meta:            map[string]string{"client_id": clientID},
		PreferResume:    true,
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/connect", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testClientToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var cr pollmux.ConnectResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	if cr.Resumable {
		t.Fatal("batch transport must not negotiate resume")
	}
}

func TestControlInvalidToken(t *testing.T) {
	cfg := testServerConfig("test-client")
	_, baseURL := startTestServer(t, cfg)

	body, _ := json.Marshal(pollmux.ConnectRequest{
		ProtocolVersion: pollmux.ProtocolVersion,
		Meta:            map[string]string{"client_id": "test-client"},
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

func TestControlWrongClientToken(t *testing.T) {
	cfg := testServerConfig("registered-client")
	cfg.Clients[0].Token = strings.Repeat("a", 32)
	_, baseURL := startTestServer(t, cfg)

	body, _ := json.Marshal(pollmux.ConnectRequest{
		ProtocolVersion: pollmux.ProtocolVersion,
		Meta:            map[string]string{"client_id": "registered-client"},
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/connect", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testClientToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
}

func TestControlUnknownClientID(t *testing.T) {
	cfg := testServerConfig("registered-client")
	_, baseURL := startTestServer(t, cfg)

	body, _ := json.Marshal(pollmux.ConnectRequest{
		ProtocolVersion: pollmux.ProtocolVersion,
		Meta:            map[string]string{"client_id": "unknown-client"},
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/connect", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testClientToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
}

func TestCloseSessionReturns410(t *testing.T) {
	const clientID = "c1"
	cfg := testServerConfig(clientID)
	srv, baseURL := startTestServer(t, cfg)

	body, _ := json.Marshal(pollmux.ConnectRequest{
		ProtocolVersion: pollmux.ProtocolVersion,
		Meta:            map[string]string{"client_id": clientID},
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/tunnel/connect", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testClientToken)
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
	pollReq.Header.Set("Authorization", "Bearer "+testClientToken)
	pollReq.Header.Set("X-Receive-Only", "true")
	pollResp, _ := http.DefaultClient.Do(pollReq)
	pollResp.Body.Close()
	if pollResp.StatusCode != http.StatusGone {
		t.Fatalf("expected 410, got %d", pollResp.StatusCode)
	}
}
