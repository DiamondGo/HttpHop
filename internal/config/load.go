package config

import (
	"fmt"
	"path"
	"strings"

	"github.com/DiamondGo/pollmux"
	"github.com/spf13/viper"
)

func LoadServer(path string) (*ServerConfig, error) {
	cfg := Defaults()
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	applyServerDefaults(&cfg)
	if err := ValidateServer(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func LoadClient(path string) (*ClientConfig, error) {
	cfg := DefaultClient()
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	applyClientDefaults(&cfg)
	if err := ValidateClient(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyServerDefaults(cfg *ServerConfig) {
	d := Defaults()
	if cfg.PublicListen == "" {
		cfg.PublicListen = d.PublicListen
	}
	if cfg.HTTPListen == "" {
		cfg.HTTPListen = d.HTTPListen
	}
	if cfg.DevListen == "" {
		cfg.DevListen = d.DevListen
	}
	if cfg.Tunnel.PollTimeout == 0 {
		cfg.Tunnel.PollTimeout = d.Tunnel.PollTimeout
	}
	if cfg.Tunnel.SessionTimeout == 0 {
		cfg.Tunnel.SessionTimeout = d.Tunnel.SessionTimeout
	}
	if cfg.Tunnel.SweepInterval == 0 {
		cfg.Tunnel.SweepInterval = d.Tunnel.SweepInterval
	}
	if cfg.Tunnel.CoalesceWindow == 0 {
		cfg.Tunnel.CoalesceWindow = d.Tunnel.CoalesceWindow
	}
	if cfg.Tunnel.PollBufferSize == 0 {
		cfg.Tunnel.PollBufferSize = d.Tunnel.PollBufferSize
	}
	if cfg.Tunnel.MaxSendBytes == 0 {
		cfg.Tunnel.MaxSendBytes = d.Tunnel.MaxSendBytes
	}
	if cfg.Tunnel.PollMode == "" {
		cfg.Tunnel.PollMode = d.Tunnel.PollMode
	}
	if cfg.Tunnel.MaxStreamsPerTunnel == 0 {
		cfg.Tunnel.MaxStreamsPerTunnel = d.Tunnel.MaxStreamsPerTunnel
	}
	if cfg.Proxy.ResponseHeaderTimeout == 0 {
		cfg.Proxy.ResponseHeaderTimeout = d.Proxy.ResponseHeaderTimeout
	}
	if cfg.Proxy.ReadHeaderTimeout == 0 {
		cfg.Proxy.ReadHeaderTimeout = d.Proxy.ReadHeaderTimeout
	}
	if cfg.Proxy.MaxHeaderBytes == 0 {
		cfg.Proxy.MaxHeaderBytes = d.Proxy.MaxHeaderBytes
	}
	if cfg.Status.Listen == "" {
		cfg.Status.Listen = d.Status.Listen
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = d.Logging.Level
	}
	for i := range cfg.Tunnels {
		if cfg.Tunnels[i].MaxClients == 0 {
			cfg.Tunnels[i].MaxClients = 1
		}
	}
}

func applyClientDefaults(cfg *ClientConfig) {
	d := DefaultClient()
	if cfg.Transport.PollGrace == 0 {
		cfg.Transport.PollGrace = d.Transport.PollGrace
	}
	if cfg.Transport.SendTimeout == 0 {
		cfg.Transport.SendTimeout = d.Transport.SendTimeout
	}
	if cfg.Transport.DialTimeout == 0 {
		cfg.Transport.DialTimeout = d.Transport.DialTimeout
	}
	if cfg.Transport.CoalesceWindow == 0 {
		cfg.Transport.CoalesceWindow = d.Transport.CoalesceWindow
	}
	if cfg.Transport.MaxSendChunk == 0 {
		cfg.Transport.MaxSendChunk = d.Transport.MaxSendChunk
	}
	if cfg.Health.Interval == 0 {
		cfg.Health.Interval = d.Health.Interval
	}
	if cfg.Health.Timeout == 0 {
		cfg.Health.Timeout = d.Health.Timeout
	}
	if cfg.Health.Mode == "" {
		cfg.Health.Mode = d.Health.Mode
	}
	if cfg.Health.Path == "" {
		cfg.Health.Path = d.Health.Path
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = d.Logging.Level
	}
}

func ValidateServer(cfg *ServerConfig) error {
	if cfg.RootDomain == "" {
		return fmt.Errorf("root_domain is required")
	}
	if cfg.ControlHost == "" {
		return fmt.Errorf("control_host is required")
	}
	if !strings.HasSuffix(cfg.ControlHost, cfg.RootDomain) {
		return fmt.Errorf("control_host must end with root_domain %q", cfg.RootDomain)
	}
	if cfg.Tunnel.SessionTimeout < 2*cfg.Tunnel.PollTimeout {
		return fmt.Errorf("session_timeout (%v) must be >= 2 × poll_timeout (%v)",
			cfg.Tunnel.SessionTimeout, cfg.Tunnel.PollTimeout)
	}
	pollMode := cfg.Tunnel.PollMode
	if pollMode == "" {
		pollMode = pollmux.PollModeBatch
	}
	if pollMode != pollmux.PollModeBatch {
		return fmt.Errorf("poll_mode %q is not implemented; use %q", pollMode, pollmux.PollModeBatch)
	}
	if len(cfg.Tunnels) == 0 {
		return fmt.Errorf("at least one tunnel binding is required")
	}

	seenToken := make(map[string]struct{})
	seenKey := make(map[string]struct{})
	for _, tb := range cfg.Tunnels {
		if len(tb.Token) < 32 {
			return fmt.Errorf("tunnel token for subdomain %q must be at least 32 characters", tb.Subdomain)
		}
		if _, ok := seenToken[tb.Token]; ok {
			return fmt.Errorf("duplicate tunnel token")
		}
		seenToken[tb.Token] = struct{}{}

		key := tb.Subdomain + "\x00" + normalizePathPrefix(tb.PathPrefix)
		if _, ok := seenKey[key]; ok {
			return fmt.Errorf("duplicate tunnel binding for subdomain %q path_prefix %q", tb.Subdomain, tb.PathPrefix)
		}
		seenKey[key] = struct{}{}

		if tb.PathPrefix != "" && !strings.HasPrefix(tb.PathPrefix, "/") {
			return fmt.Errorf("path_prefix for subdomain %q must start with /", tb.Subdomain)
		}
	}

	if err := validatePathPrefixConflicts(cfg.Tunnels); err != nil {
		return err
	}
	return nil
}

func validatePathPrefixConflicts(bindings []TunnelBinding) error {
	type entry struct {
		prefix string
		token  string
	}
	byHost := make(map[string][]entry)
	for _, tb := range bindings {
		p := normalizePathPrefix(tb.PathPrefix)
		if p == "" {
			continue
		}
		byHost[tb.Subdomain] = append(byHost[tb.Subdomain], entry{p, tb.Token})
	}
	for host, entries := range byHost {
		for i := range entries {
			for j := range entries {
				if i == j || entries[i].token == entries[j].token {
					continue
				}
				a, b := entries[i].prefix, entries[j].prefix
				if strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") || a == b {
					return fmt.Errorf("conflicting path_prefix on subdomain %q: %q and %q point to different tokens",
						host, entries[i].prefix, entries[j].prefix)
				}
			}
		}
	}
	return nil
}

func normalizePathPrefix(p string) string {
	if p == "" {
		return ""
	}
	cleaned := path.Clean(p)
	if cleaned == "/" {
		return "/"
	}
	return strings.TrimSuffix(cleaned, "/")
}

func ValidateClient(cfg *ClientConfig) error {
	if cfg.ClientID == "" {
		return fmt.Errorf("client_id is required")
	}
	if cfg.Server.URL == "" {
		return fmt.Errorf("server.url is required")
	}
	if cfg.Server.Token == "" {
		return fmt.Errorf("server.token is required")
	}
	if cfg.Local.Target == "" {
		return fmt.Errorf("local.target is required")
	}
	if cfg.Health.Enabled {
		switch cfg.Health.Mode {
		case "tcp", "http":
		default:
			return fmt.Errorf("health.mode must be tcp or http")
		}
	}
	return nil
}
