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
	if err := resolveClientBindingTokens(cfg.Clients, path); err != nil {
		return nil, err
	}
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
	if err := resolveClientAuthToken(&cfg, path); err != nil {
		return nil, err
	}
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
	if cfg.ControlPath == "" {
		cfg.ControlPath = d.ControlPath
	}
	for i := range cfg.Clients {
		if cfg.Clients[i].MaxClients == 0 {
			cfg.Clients[i].MaxClients = 1
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
	normalizedControlPath, err := validateControlPath(cfg.ControlPath)
	if err != nil {
		return err
	}
	cfg.ControlPath = normalizedControlPath
	if err := validateControlPathNotConflicting(cfg.ControlPath, cfg.Clients); err != nil {
		return err
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
	if len(cfg.Clients) == 0 {
		return fmt.Errorf("at least one client binding is required")
	}

	seenClientID := make(map[string]struct{})
	seenRoute := make(map[string]struct{})
	seenToken := make(map[string]string)
	for _, cb := range cfg.Clients {
		if cb.ClientID == "" {
			return fmt.Errorf("client_id is required for each client binding")
		}
		if cb.TokenFile == "" && cb.Token == "" {
			return fmt.Errorf("client_id %q requires token_file", cb.ClientID)
		}
		if _, ok := seenClientID[cb.ClientID]; ok {
			return fmt.Errorf("duplicate client_id %q", cb.ClientID)
		}
		seenClientID[cb.ClientID] = struct{}{}

		if len(cb.Token) < 32 {
			return fmt.Errorf("token for client_id %q must be at least 32 characters", cb.ClientID)
		}
		if other, ok := seenToken[cb.Token]; ok {
			return fmt.Errorf("duplicate token shared by client_id %q and %q", other, cb.ClientID)
		}
		seenToken[cb.Token] = cb.ClientID

		routeKey := cb.Subdomain + "\x00" + normalizePathPrefix(cb.PathPrefix)
		if _, ok := seenRoute[routeKey]; ok {
			return fmt.Errorf("duplicate route for subdomain %q path_prefix %q", cb.Subdomain, cb.PathPrefix)
		}
		seenRoute[routeKey] = struct{}{}

		if cb.PathPrefix != "" && !strings.HasPrefix(cb.PathPrefix, "/") {
			return fmt.Errorf("path_prefix for client_id %q must start with /", cb.ClientID)
		}
	}

	if err := validatePathPrefixConflicts(cfg.Clients); err != nil {
		return err
	}
	return nil
}

func validatePathPrefixConflicts(bindings []ClientBinding) error {
	type entry struct {
		prefix   string
		clientID string
	}
	byHost := make(map[string][]entry)
	for _, cb := range bindings {
		p := normalizePathPrefix(cb.PathPrefix)
		if p == "" {
			continue
		}
		byHost[cb.Subdomain] = append(byHost[cb.Subdomain], entry{p, cb.ClientID})
	}
	for host, entries := range byHost {
		for i := range entries {
			for j := range entries {
				if i == j || entries[i].clientID == entries[j].clientID {
					continue
				}
				a, b := entries[i].prefix, entries[j].prefix
				if strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") || a == b {
					return fmt.Errorf("conflicting path_prefix on subdomain %q: %q and %q point to different client_ids",
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
		return fmt.Errorf("server token is required (set server.token_file)")
	}
	if len(cfg.Server.Token) < 32 {
		return fmt.Errorf("server token must be at least 32 characters")
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
