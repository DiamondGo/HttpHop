package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
