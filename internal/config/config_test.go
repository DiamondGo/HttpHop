package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/DiamondGo/HttpHop/internal/config"
)

func TestLoadServerExample(t *testing.T) {
	cfg, err := config.LoadServer("../../configs/server.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RootDomain != "httphop.io" {
		t.Fatalf("root_domain = %q", cfg.RootDomain)
	}
	if len(cfg.Tunnels) < 1 {
		t.Fatalf("expected at least one tunnel, got %d", len(cfg.Tunnels))
	}
	if cfg.Tunnels[0].Subdomain != "myapp" {
		t.Fatalf("subdomain = %q, want myapp", cfg.Tunnels[0].Subdomain)
	}
}

func TestSessionTimeoutValidation(t *testing.T) {
	cfg := config.Defaults()
	cfg.RootDomain = "example.com"
	cfg.ControlHost = "tunnel.example.com"
	cfg.Tunnel.PollTimeout = 30 * time.Second
	cfg.Tunnel.SessionTimeout = 30 * time.Second
	cfg.Tunnels = []config.TunnelBinding{{
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

func TestDuplicateToken(t *testing.T) {
	tok := strings.Repeat("x", 32)
	cfg := config.Defaults()
	cfg.RootDomain = "example.com"
	cfg.ControlHost = "tunnel.example.com"
	cfg.Tunnels = []config.TunnelBinding{
		{Subdomain: "a", Token: tok},
		{Subdomain: "b", Token: tok},
	}
	err := config.ValidateServer(&cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate token error, got %v", err)
	}
}

func TestPathPrefixConflict(t *testing.T) {
	cfg := config.Defaults()
	cfg.RootDomain = "example.com"
	cfg.ControlHost = "tunnel.example.com"
	cfg.Tunnels = []config.TunnelBinding{
		{Subdomain: "@", PathPrefix: "/api", Token: strings.Repeat("a", 32)},
		{Subdomain: "@", PathPrefix: "/api/v1", Token: strings.Repeat("b", 32)},
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
