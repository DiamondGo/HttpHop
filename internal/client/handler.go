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

// bridgeBidirectional copies both directions concurrently and waits for
// both to finish before returning. Returning as soon as the first direction
// finished (the previous behavior) raced the still-in-flight direction: for
// a request whose response is still being written when the request body
// side reaches a clean EOF (yamux's Stream.Close is a graceful per-direction
// FIN, not a hard reset — see hashicorp/yamux's streamRemoteClose state —
// so this EOF is a normal, common event, not an error condition), the
// caller's deferred Close calls would tear down the connection mid-response
// and truncate it. Waiting for both, and half-closing whichever side
// supports it as its own direction finishes, lets the still-running
// direction complete on its own terms instead.
func bridgeBidirectional(a, b net.Conn) error {
	errCh := make(chan error, 2)
	go func() { errCh <- copyHalfClose(b, a) }()
	go func() { errCh <- copyHalfClose(a, b) }()
	err1 := <-errCh
	err2 := <-errCh
	if err1 != nil {
		return err1
	}
	return err2
}

// copyHalfClose copies src into dst, then half-closes dst's write side if
// it supports CloseWrite (e.g. *net.TCPConn does; a yamux.Stream does not
// and this is a no-op there) so the peer sees a clean end of that direction
// without dst's read side — and the other bridgeBidirectional goroutine
// still reading from dst — being disturbed.
func copyHalfClose(dst, src net.Conn) error {
	_, err := io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
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
