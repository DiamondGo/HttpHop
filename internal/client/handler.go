package client

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/DiamondGo/HttpHop/internal/config"
)

type StreamHandler struct {
	target      string
	hostRewrite string
	dialTimeout time.Duration
	logger      *zap.Logger
}

func NewStreamHandler(local config.LocalConfig, dialTimeout time.Duration, logger *zap.Logger) *StreamHandler {
	if dialTimeout == 0 {
		dialTimeout = 10 * time.Second
	}
	return &StreamHandler{
		target:      local.Target,
		hostRewrite: local.HostRewrite,
		dialTimeout: dialTimeout,
		logger:      logger,
	}
}

func (h *StreamHandler) Handle(stream net.Conn) {
	defer stream.Close()

	local, err := net.DialTimeout("tcp", h.target, h.dialTimeout)
	if err != nil {
		h.logger.Warn("dial local target failed",
			zap.String("target", h.target), zap.Error(err))
		return
	}
	defer local.Close()

	if h.hostRewrite != "" {
		if err := bridgeWithHostRewrite(stream, local, h.hostRewrite); err != nil {
			h.logger.Debug("stream ended", zap.Error(err))
		}
		return
	}

	if err := bridgeBidirectional(stream, local); err != nil {
		h.logger.Debug("stream ended", zap.Error(err))
	}
}

func bridgeBidirectional(a, b net.Conn) error {
	errCh := make(chan error, 2)
	go func() { _, err := io.Copy(b, a); errCh <- err }()
	go func() { _, err := io.Copy(a, b); errCh <- err }()
	err := <-errCh
	return err
}

func bridgeWithHostRewrite(stream, local net.Conn, newHost string) error {
	br := bufio.NewReader(stream)
	req, err := http.ReadRequest(br)
	if err != nil {
		return err
	}
	req.Host = newHost
	req.Header.Set("Host", newHost)

	var buf bytes.Buffer
	if err := req.Write(&buf); err != nil {
		return err
	}
	if br.Buffered() > 0 {
		extra, _ := io.ReadAll(br)
		buf.Write(extra)
	}
	if _, err := local.Write(buf.Bytes()); err != nil {
		return err
	}
	return bridgeBidirectional(stream, local)
}
