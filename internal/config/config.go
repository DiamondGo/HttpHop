// Package config loads and validates HttpHop server and client configuration.
package config

import (
	"log/slog"
	"time"

	"github.com/DiamondGo/pollmux"
)

const Version = "0.1.0"

type ServerConfig struct {
	PublicListen string          `mapstructure:"public_listen"`
	HTTPListen   string          `mapstructure:"http_listen"`
	RootDomain   string          `mapstructure:"root_domain"`
	ControlHost  string          `mapstructure:"control_host"`
	DevListen    string          `mapstructure:"dev_listen"`
	TLS          TLSConfig       `mapstructure:"tls"`
	Tunnel       TunnelConfig    `mapstructure:"tunnel"`
	Proxy        ProxyConfig     `mapstructure:"proxy"`
	Tunnels      []TunnelBinding `mapstructure:"tunnels"`
	Status       StatusConfig    `mapstructure:"status"`
	Logging      LoggingConfig   `mapstructure:"logging"`
}

type TLSConfig struct {
	CacheDir string `mapstructure:"cache_dir"`
	Email    string `mapstructure:"email"`
	Disable  bool   `mapstructure:"disable"`
}

type TunnelConfig struct {
	PollTimeout         time.Duration `mapstructure:"poll_timeout"`
	SessionTimeout      time.Duration `mapstructure:"session_timeout"`
	SweepInterval       time.Duration `mapstructure:"sweep_interval"`
	CoalesceWindow      time.Duration `mapstructure:"coalesce_window"`
	PollBufferSize      int           `mapstructure:"poll_buffer_size"`
	MaxSendBytes        int           `mapstructure:"max_send_bytes"`
	HighWaterWarn       int           `mapstructure:"high_water_warn"`
	PollMode            string        `mapstructure:"poll_mode"`
	MaxStreamsPerTunnel int           `mapstructure:"max_streams_per_tunnel"`
}

type ProxyConfig struct {
	ResponseHeaderTimeout time.Duration `mapstructure:"response_header_timeout"`
	ReadHeaderTimeout     time.Duration `mapstructure:"read_header_timeout"`
	MaxHeaderBytes        int           `mapstructure:"max_header_bytes"`
}

type TunnelBinding struct {
	Subdomain   string `mapstructure:"subdomain"`
	PathPrefix  string `mapstructure:"path_prefix"`
	StripPrefix bool   `mapstructure:"strip_prefix"`
	Token       string `mapstructure:"token"`
	MaxClients  int    `mapstructure:"max_clients"`
}

type StatusConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Listen  string `mapstructure:"listen"`
}

type LoggingConfig struct {
	Level string `mapstructure:"level"`
}

type ClientConfig struct {
	ClientID  string          `mapstructure:"client_id"`
	Server    ServerRef       `mapstructure:"server"`
	Local     LocalConfig     `mapstructure:"local"`
	Transport TransportConfig `mapstructure:"transport"`
	Health    HealthConfig    `mapstructure:"health"`
	Logging   LoggingConfig   `mapstructure:"logging"`
}

type ServerRef struct {
	URL                string `mapstructure:"url"`
	Token              string `mapstructure:"token"`
	InsecureSkipVerify bool   `mapstructure:"insecure_skip_verify"`
}

type LocalConfig struct {
	Target      string `mapstructure:"target"`
	HostRewrite string `mapstructure:"host_header_rewrite"`
}

type TransportConfig struct {
	PollInterval   time.Duration `mapstructure:"poll_interval"`
	PollGrace      time.Duration `mapstructure:"poll_grace"`
	SendTimeout    time.Duration `mapstructure:"send_timeout"`
	DialTimeout    time.Duration `mapstructure:"dial_timeout"`
	CoalesceWindow time.Duration `mapstructure:"coalesce_window"`
	MaxSendChunk   int           `mapstructure:"max_send_chunk"`
}

type HealthConfig struct {
	Enabled  bool          `mapstructure:"enabled"`
	Mode     string        `mapstructure:"mode"`
	Path     string        `mapstructure:"path"`
	Interval time.Duration `mapstructure:"interval"`
	Timeout  time.Duration `mapstructure:"timeout"`
}

func Defaults() ServerConfig {
	return ServerConfig{
		PublicListen: ":443",
		HTTPListen:   ":80",
		DevListen:    ":8443",
		Tunnel: TunnelConfig{
			PollTimeout:         30 * time.Second,
			SessionTimeout:      60 * time.Second,
			SweepInterval:       5 * time.Second,
			CoalesceWindow:      2 * time.Millisecond,
			PollBufferSize:      256 << 10,
			MaxSendBytes:        1 << 20,
			PollMode:            pollmux.PollModeBatch,
			MaxStreamsPerTunnel: 256,
		},
		Proxy: ProxyConfig{
			ResponseHeaderTimeout: 60 * time.Second,
			ReadHeaderTimeout:     10 * time.Second,
			MaxHeaderBytes:        65536,
		},
		Status: StatusConfig{
			Listen: "127.0.0.1:9090",
		},
		Logging: LoggingConfig{Level: "info"},
	}
}

func DefaultClient() ClientConfig {
	return ClientConfig{
		Transport: TransportConfig{
			PollInterval:   0,
			PollGrace:      10 * time.Second,
			SendTimeout:    15 * time.Second,
			DialTimeout:    10 * time.Second,
			CoalesceWindow: 2 * time.Millisecond,
			MaxSendChunk:   512 << 10,
		},
		Health: HealthConfig{
			Enabled:  true,
			Mode:     "tcp",
			Path:     "/healthz",
			Interval: 15 * time.Second,
			Timeout:  3 * time.Second,
		},
		Logging: LoggingConfig{Level: "info"},
	}
}

func (cfg ServerConfig) PollmuxServerConfig(logger *slog.Logger) pollmux.ServerConfig {
	pollMode := cfg.Tunnel.PollMode
	if pollMode == "" {
		pollMode = pollmux.PollModeBatch
	}
	return pollmux.ServerConfig{
		PollTimeout:    cfg.Tunnel.PollTimeout,
		SessionTimeout: cfg.Tunnel.SessionTimeout,
		SweepInterval:  cfg.Tunnel.SweepInterval,
		CoalesceWindow: cfg.Tunnel.CoalesceWindow,
		PollBufferSize: cfg.Tunnel.PollBufferSize,
		MaxSendBytes:   cfg.Tunnel.MaxSendBytes,
		HighWaterWarn:  cfg.Tunnel.HighWaterWarn,
		PollMode:       pollMode,
		Logger:         logger,
	}
}
