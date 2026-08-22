package client

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/DiamondGo/pollmux"
	"go.uber.org/zap"
	"go.uber.org/zap/exp/zapslog"

	"github.com/DiamondGo/HttpHop/internal/config"
)

type Client struct {
	cfg          *config.ClientConfig
	handler      *StreamHandler
	health       *Checker
	logger       *zap.Logger
	diagnostics  *sessionDiagnostics
}

func New(cfg *config.ClientConfig, logger *zap.Logger) *Client {
	c := &Client{
		cfg:         cfg,
		logger:      logger,
		diagnostics: newSessionDiagnostics(),
	}
	c.health = NewChecker(cfg.Health, cfg.Local.Target, logger)
	c.handler = NewStreamHandler(cfg.Local, cfg.Transport.DialTimeout, logger)
	return c
}

func (c *Client) Run(ctx context.Context) error {
	if c.cfg.Health.Enabled {
		go c.health.Run(ctx)
	}

	slogLogger := slog.New(zapslog.NewHandler(c.logger.Core()))
	connector := c.buildConnector(slogLogger)

	loop := &pollmux.ReconnectLoop{
		Connect: func(ctx context.Context) (pollmux.Conn, error) {
			return connector.Connect(ctx)
		},
		Serve:  c.serveSession,
		Logger: slogLogger,
	}
	return loop.Run(ctx)
}

func (c *Client) serveSession(ctx context.Context, conn pollmux.Conn) pollmux.Outcome {
	start := time.Now()
	sessionID := conn.SessionID()

	sess, err := pollmux.ServerSession(conn)
	if err != nil {
		outcome := pollmux.OutcomeTransportFailed
		c.diagnostics.record(c.logger, c.cfg.ClientID, sessionID, time.Since(start), outcome, err.Error())
		return outcome
	}
	defer sess.Close()

	outcome := pollmux.AcceptLoop(ctx, sess, conn, c.handler.Handle)
	c.diagnostics.record(c.logger, c.cfg.ClientID, sessionID, time.Since(start), outcome, "")
	return outcome
}

func (c *Client) buildConnector(logger *slog.Logger) *pollmux.Connector {
	var localHealth func() bool
	if c.cfg.Health.Enabled {
		localHealth = c.health.Healthy
	}
	return &pollmux.Connector{
		BaseURL:                c.cfg.Server.URL,
		PathPrefix:             c.cfg.Server.ControlPath,
		AuthToken:              c.cfg.Server.Token,
		Meta:                   map[string]string{"client_id": c.cfg.ClientID},
		PollInterval:           c.cfg.Transport.PollInterval,
		PollGrace:              c.cfg.Transport.PollGrace,
		SendTimeout:            c.cfg.Transport.SendTimeout,
		CoalesceWindow:         c.cfg.Transport.CoalesceWindow,
		MaxSendChunk:           c.cfg.Transport.MaxSendChunk,
		PreferStream:           c.cfg.Transport.PreferStream,
		UploadStreamPreference: c.cfg.Transport.UploadStreamPreference,
		UploadProbeTimeout:     c.cfg.Transport.UploadProbeTimeout,
		PreferWebSocket:        c.cfg.Transport.PreferWebSocket,
		LocalHealth:            localHealth,
		InsecureSkipVerify:     c.cfg.Server.InsecureSkipVerify,
		Logger:                 logger,
	}
}

type Checker struct {
	target   string
	mode     string
	path     string
	interval time.Duration
	timeout  time.Duration
	healthy  atomic.Bool
	logger   *zap.Logger
}

func NewChecker(cfg config.HealthConfig, target string, logger *zap.Logger) *Checker {
	return &Checker{
		target:   target,
		mode:     cfg.Mode,
		path:     cfg.Path,
		interval: cfg.Interval,
		timeout:  cfg.Timeout,
		logger:   logger,
	}
}

func (c *Checker) Healthy() bool {
	return c.healthy.Load()
}

func (c *Checker) Run(ctx context.Context) {
	c.probe()
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.probe()
		}
	}
}

func (c *Checker) probe() {
	ok := c.check()
	prev := c.healthy.Load()
	c.healthy.Store(ok)
	if ok != prev {
		state := "down"
		if ok {
			state = "ok"
		}
		c.logger.Info("local health changed", zap.String("state", state))
	}
}

func (c *Checker) check() bool {
	switch c.mode {
	case "http":
		client := &http.Client{Timeout: c.timeout}
		url := "http://" + c.target + c.path
		resp, err := client.Get(url)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode >= 200 && resp.StatusCode < 400
	default:
		conn, err := net.DialTimeout("tcp", c.target, c.timeout)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
}
