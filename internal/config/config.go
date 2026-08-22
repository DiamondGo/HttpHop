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
	ControlPath  string          `mapstructure:"control_path"`
	DevListen    string          `mapstructure:"dev_listen"`
	TLS          TLSConfig       `mapstructure:"tls"`
	Tunnel       TunnelConfig    `mapstructure:"tunnel"`
	Proxy        ProxyConfig     `mapstructure:"proxy"`
	Clients      []ClientBinding `mapstructure:"clients"`
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
	HeartbeatInterval   time.Duration `mapstructure:"heartbeat_interval"`
	StreamMaxDuration   time.Duration `mapstructure:"stream_max_duration"`
	EnableWebSocket     bool          `mapstructure:"enable_websocket"`
	MaxStreamsPerTunnel   int           `mapstructure:"max_streams_per_tunnel"`
	SupersedeDrainTimeout time.Duration `mapstructure:"supersede_drain_timeout"`
}

type ProxyConfig struct {
	ResponseHeaderTimeout time.Duration `mapstructure:"response_header_timeout"`
	ReadHeaderTimeout     time.Duration `mapstructure:"read_header_timeout"`
	MaxHeaderBytes        int           `mapstructure:"max_header_bytes"`
}

type ClientBinding struct {
	ClientID    string `mapstructure:"client_id"`
	Subdomain   string `mapstructure:"subdomain"`
	PathPrefix  string `mapstructure:"path_prefix"`
	StripPrefix bool   `mapstructure:"strip_prefix"`
	TokenFile   string `mapstructure:"token_file"`
	Token       string `mapstructure:"-"` // loaded from token_file at startup
	MaxClients  int    `mapstructure:"max_clients"`
}

type StatusConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Listen  string `mapstructure:"listen"`
}

type LoggingConfig struct {
	Level string `mapstructure:"level"`
}

type ServiceEntry struct {
	ClientID  string       `mapstructure:"client_id"`
	TokenFile string       `mapstructure:"token_file"`
	Token     string       `mapstructure:"-"`
	Local     LocalConfig  `mapstructure:"local"`
	Server    *ServerRef   `mapstructure:"server"`
	Health    *HealthConfig `mapstructure:"health"`
}

type MultiClientConfig struct {
	Server    ServerRef       `mapstructure:"server"`
	Transport TransportConfig `mapstructure:"transport"`
	Health    HealthConfig    `mapstructure:"health"`
	Logging   LoggingConfig   `mapstructure:"logging"`
	Services  []ServiceEntry  `mapstructure:"services"`
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
	TokenFile          string `mapstructure:"token_file"`
	ControlPath        string `mapstructure:"control_path"`
	InsecureSkipVerify bool   `mapstructure:"insecure_skip_verify"`
}

type LocalConfig struct {
	Target      string `mapstructure:"target"`
	HostRewrite string `mapstructure:"host_header_rewrite"`
}

type TransportConfig struct {
	PollInterval           time.Duration `mapstructure:"poll_interval"`
	PollGrace              time.Duration `mapstructure:"poll_grace"`
	SendTimeout            time.Duration `mapstructure:"send_timeout"`
	DialTimeout            time.Duration `mapstructure:"dial_timeout"`
	CoalesceWindow         time.Duration `mapstructure:"coalesce_window"`
	MaxSendChunk           int           `mapstructure:"max_send_chunk"`
	PreferStream           bool          `mapstructure:"prefer_stream"`
	UploadStreamPreference string        `mapstructure:"upload_stream_preference"`
	UploadProbeTimeout     time.Duration `mapstructure:"upload_probe_timeout"`
	PreferWebSocket        bool          `mapstructure:"prefer_websocket"`
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
		ControlPath:  "/tunnel",
		DevListen:    ":8443",
		Tunnel: TunnelConfig{
			PollTimeout:         30 * time.Second,
			SessionTimeout:      60 * time.Second,
			SweepInterval:       5 * time.Second,
			CoalesceWindow:      2 * time.Millisecond,
			PollBufferSize:      256 << 10,
			MaxSendBytes:        1 << 20,
			PollMode:            pollmux.PollModeBatch,
			HeartbeatInterval:   pollmux.DefaultHeartbeatInterval,
			StreamMaxDuration:   pollmux.DefaultStreamMaxDuration,
			MaxStreamsPerTunnel:   256,
			SupersedeDrainTimeout: 10 * time.Minute,
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
			// PreferStream/UploadStreamPreference/PreferWebSocket default to
			// off ("", false): an old-server/new-client pair negotiates down
			// to plain batch polling with no behavior change. Opt in per
			// deployment once the link in front of the server (see server's
			// enable_websocket / poll_mode) is known to support it — a
			// buffering intermediate proxy (e.g. Cloudflare's non-Enterprise
			// tiers) can silently hang a long-held upload request.
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
		PollTimeout:       cfg.Tunnel.PollTimeout,
		SessionTimeout:    cfg.Tunnel.SessionTimeout,
		SweepInterval:     cfg.Tunnel.SweepInterval,
		CoalesceWindow:    cfg.Tunnel.CoalesceWindow,
		PollBufferSize:    cfg.Tunnel.PollBufferSize,
		MaxSendBytes:      cfg.Tunnel.MaxSendBytes,
		HighWaterWarn:     cfg.Tunnel.HighWaterWarn,
		PollMode:          pollMode,
		HeartbeatInterval: cfg.Tunnel.HeartbeatInterval,
		StreamMaxDuration: cfg.Tunnel.StreamMaxDuration,
		EnableWebSocket:   cfg.Tunnel.EnableWebSocket,
		Logger:            logger,
	}
}
