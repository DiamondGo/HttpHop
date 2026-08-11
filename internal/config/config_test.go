package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DiamondGo/pollmux"

	"github.com/DiamondGo/HttpHop/internal/config"
)

func TestLoadServerRequiresTokenFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "server.yaml")
	cfgBody := `root_domain: example.com
clients:
  - client_id: dev-1
    subdomain: app
`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadServer(cfgPath); err == nil || !strings.Contains(err.Error(), "token_file") {
		t.Fatalf("expected token_file error, got %v", err)
	}
}

func TestLoadServerExample(t *testing.T) {
	dir := t.TempDir()
	localDir := filepath.Join(dir, "local")
	secretsDir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secretsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 32)
	if err := os.WriteFile(filepath.Join(secretsDir, "home-gpu-01.token"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	example, err := os.ReadFile("../../configs/examples/local/server.yaml.example")
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(localDir, "server.yaml")
	if err := os.WriteFile(cfgPath, example, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadServer(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RootDomain != "httphop.io" {
		t.Fatalf("root_domain = %q", cfg.RootDomain)
	}
	if len(cfg.Clients) < 1 {
		t.Fatalf("expected at least one client, got %d", len(cfg.Clients))
	}
	if cfg.Clients[0].Subdomain != "myapp" {
		t.Fatalf("subdomain = %q, want myapp", cfg.Clients[0].Subdomain)
	}
	if len(cfg.Clients[0].Token) < 32 {
		t.Fatal("expected token loaded from token_file")
	}
}

func TestLoadClientTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "secret.token")
	token := strings.Repeat("c", 32)
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "client.yaml")
	cfgBody := `client_id: dev-1
server:
  url: http://127.0.0.1:1
  token_file: secret.token
local:
  target: 127.0.0.1:8080
`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Token != token {
		t.Fatalf("token = %q, want %q", cfg.Server.Token, token)
	}
}

func TestSessionTimeoutValidation(t *testing.T) {
	cfg := config.Defaults()
	cfg.RootDomain = "example.com"
	cfg.Tunnel.PollTimeout = 30 * time.Second
	cfg.Tunnel.SessionTimeout = 30 * time.Second
	cfg.Clients = []config.ClientBinding{{
		ClientID:  "app-1",
		Subdomain: "app",
		Token:     strings.Repeat("a", 32),
	}}

	err := config.ValidateServer(&cfg)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "session_timeout") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDuplicateClientID(t *testing.T) {
	cfg := config.Defaults()
	cfg.RootDomain = "example.com"
	cfg.Clients = []config.ClientBinding{
		{ClientID: "same", Subdomain: "a", Token: strings.Repeat("a", 32)},
		{ClientID: "same", Subdomain: "b", Token: strings.Repeat("b", 32)},
	}
	err := config.ValidateServer(&cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicate client_id") {
		t.Fatalf("expected duplicate client_id error, got %v", err)
	}
}

func TestDuplicateClientToken(t *testing.T) {
	tok := strings.Repeat("x", 32)
	cfg := config.Defaults()
	cfg.RootDomain = "example.com"
	cfg.Clients = []config.ClientBinding{
		{ClientID: "a", Subdomain: "a", Token: tok},
		{ClientID: "b", Subdomain: "b", Token: tok},
	}
	err := config.ValidateServer(&cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicate token") {
		t.Fatalf("expected duplicate token error, got %v", err)
	}
}

func TestPathPrefixConflict(t *testing.T) {
	cfg := config.Defaults()
	cfg.RootDomain = "example.com"
	cfg.Clients = []config.ClientBinding{
		{ClientID: "a1", Subdomain: "@", PathPrefix: "/api", Token: strings.Repeat("a", 32)},
		{ClientID: "b1", Subdomain: "@", PathPrefix: "/api/v1", Token: strings.Repeat("b", 32)},
	}
	err := config.ValidateServer(&cfg)
	if err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestPollmuxServerConfigMapping(t *testing.T) {
	cfg := config.Defaults()
	cfg.Tunnel.PollTimeout = 30 * time.Second
	cfg.Tunnel.SessionTimeout = 60 * time.Second
	pcfg := cfg.PollmuxServerConfig(nil)
	if pcfg.PollTimeout != 30*time.Second {
		t.Fatalf("poll timeout mismatch")
	}
}

func TestPollModeStreamAccepted(t *testing.T) {
	cfg := config.Defaults()
	cfg.RootDomain = "example.com"
	cfg.Tunnel.PollMode = "stream"
	cfg.Clients = []config.ClientBinding{{
		ClientID:  "app-1",
		Subdomain: "app",
		Token:     strings.Repeat("a", 32),
	}}
	if err := config.ValidateServer(&cfg); err != nil {
		t.Fatalf("expected stream poll_mode to validate, got %v", err)
	}
	pcfg := cfg.PollmuxServerConfig(nil)
	if pcfg.PollMode != "stream" {
		t.Fatalf("PollMode = %q, want stream", pcfg.PollMode)
	}
	if pcfg.HeartbeatInterval == 0 || pcfg.StreamMaxDuration == 0 {
		t.Fatalf("expected heartbeat/stream-max-duration defaults to carry through, got %v/%v",
			pcfg.HeartbeatInterval, pcfg.StreamMaxDuration)
	}
}

func TestPollModeInvalidRejected(t *testing.T) {
	cfg := config.Defaults()
	cfg.RootDomain = "example.com"
	cfg.Tunnel.PollMode = "chunked"
	cfg.Clients = []config.ClientBinding{{
		ClientID:  "app-1",
		Subdomain: "app",
		Token:     strings.Repeat("a", 32),
	}}
	err := config.ValidateServer(&cfg)
	if err == nil || !strings.Contains(err.Error(), "poll_mode") {
		t.Fatalf("expected poll_mode error, got %v", err)
	}
}

func TestStreamMaxDurationTooCloseToHeartbeat(t *testing.T) {
	cfg := config.Defaults()
	cfg.RootDomain = "example.com"
	cfg.Tunnel.PollMode = "stream"
	cfg.Tunnel.HeartbeatInterval = 10 * time.Second
	cfg.Tunnel.StreamMaxDuration = 15 * time.Second
	cfg.Clients = []config.ClientBinding{{
		ClientID:  "app-1",
		Subdomain: "app",
		Token:     strings.Repeat("a", 32),
	}}
	err := config.ValidateServer(&cfg)
	if err == nil || !strings.Contains(err.Error(), "stream_max_duration") {
		t.Fatalf("expected stream_max_duration error, got %v", err)
	}
}

func TestLoadServerStreamDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte(strings.Repeat("a", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "server.yaml")
	cfgBody := `root_domain: example.com
tunnel:
  poll_mode: stream
clients:
  - client_id: dev-1
    subdomain: app
    token_file: token
`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadServer(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tunnel.HeartbeatInterval != pollmux.DefaultHeartbeatInterval {
		t.Fatalf("HeartbeatInterval = %v, want default %v", cfg.Tunnel.HeartbeatInterval, pollmux.DefaultHeartbeatInterval)
	}
	if cfg.Tunnel.StreamMaxDuration != pollmux.DefaultStreamMaxDuration {
		t.Fatalf("StreamMaxDuration = %v, want default %v", cfg.Tunnel.StreamMaxDuration, pollmux.DefaultStreamMaxDuration)
	}
	pcfg := cfg.PollmuxServerConfig(nil)
	if pcfg.PollMode != pollmux.PollModeStream {
		t.Fatalf("PollMode = %q, want stream", pcfg.PollMode)
	}
}

func TestLoadServerStreamExplicitDurationsHonored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte(strings.Repeat("a", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "server.yaml")
	cfgBody := `root_domain: example.com
tunnel:
  poll_mode: stream
  heartbeat_interval: 1s
  stream_max_duration: 3s
clients:
  - client_id: dev-1
    subdomain: app
    token_file: token
`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadServer(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tunnel.HeartbeatInterval != time.Second {
		t.Fatalf("HeartbeatInterval = %v, want 1s", cfg.Tunnel.HeartbeatInterval)
	}
	if cfg.Tunnel.StreamMaxDuration != 3*time.Second {
		t.Fatalf("StreamMaxDuration = %v, want 3s", cfg.Tunnel.StreamMaxDuration)
	}
}

func TestEnableWebSocketMapping(t *testing.T) {
	cfg := config.Defaults()
	cfg.Tunnel.EnableWebSocket = true
	pcfg := cfg.PollmuxServerConfig(nil)
	if !pcfg.EnableWebSocket {
		t.Fatal("expected EnableWebSocket to carry through to pollmux.ServerConfig")
	}
}

func TestUploadStreamPreferenceValidation(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "secret.token")
	token := strings.Repeat("c", 32)
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "client.yaml")
	cfgBody := `client_id: dev-1
server:
  url: http://127.0.0.1:1
  token_file: secret.token
local:
  target: 127.0.0.1:8080
transport:
  upload_stream_preference: "bogus"
`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadClient(cfgPath)
	if err == nil || !strings.Contains(err.Error(), "upload_stream_preference") {
		t.Fatalf("expected upload_stream_preference error, got %v", err)
	}
}
