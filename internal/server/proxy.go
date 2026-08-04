package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/DiamondGo/HttpHop/internal/config"
	"github.com/DiamondGo/HttpHop/internal/registry"
	"github.com/DiamondGo/HttpHop/internal/router"
)

type ctxKey struct{}

type proxyCtx struct {
	tunnel *registry.ClientTunnel
	strip  string
}

var errNoTunnel = errors.New("no tunnel in context")

func (s *Server) servePublic(w http.ResponseWriter, r *http.Request) {
	hostKey, err := router.HostKey(r.Host, s.cfg.RootDomain)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, "invalid host")
		return
	}

	route, err := s.routes.Match(hostKey, r.URL.Path)
	if err != nil {
		writeHTTPError(w, http.StatusNotFound, "no route for host/path")
		return
	}

	tun, ok := route.Pool.Pick(r)
	if !ok {
		writeHTTPError(w, http.StatusServiceUnavailable, "no available backend")
		return
	}

	if !tun.LocalHealthy.Load() {
		writeHTTPError(w, http.StatusServiceUnavailable, "backend local service unhealthy")
		return
	}

	if tun.ActiveStreams.Load() >= int64(s.cfg.Tunnel.MaxStreamsPerTunnel) {
		writeHTTPError(w, http.StatusServiceUnavailable, "tunnel stream limit reached")
		return
	}

	pctx := proxyCtx{tunnel: tun}
	if route.Strip && route.PathPrefix != "" {
		pctx.strip = route.PathPrefix
	}
	r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, pctx))
	s.proxy.ServeHTTP(w, r)
}

func (s *Server) newProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "tunnel"
			pr.Out.Host = pr.In.Host
			if pctx, ok := pr.In.Context().Value(ctxKey{}).(proxyCtx); ok && pctx.strip != "" {
				if stripped, ok := router.StripPathPrefix(pr.Out.URL.Path, pctx.strip); ok {
					pr.Out.URL.Path = stripped
				}
			}
			pr.SetXForwarded()
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				pctx, _ := ctx.Value(ctxKey{}).(proxyCtx)
				if pctx.tunnel == nil {
					return nil, errNoTunnel
				}
				return OpenStream(pctx.tunnel)
			},
			DisableKeepAlives:     true,
			ForceAttemptHTTP2:     false,
			ResponseHeaderTimeout: s.cfg.Proxy.ResponseHeaderTimeout,
		},
		FlushInterval: -1,
		ErrorHandler:  s.proxyErrorHandler,
		ModifyResponse: s.modifyProxyResponse,
	}
}

func (s *Server) modifyProxyResponse(resp *http.Response) error {
	if resp == nil || resp.Request == nil {
		return nil
	}
	pctx, ok := resp.Request.Context().Value(ctxKey{}).(proxyCtx)
	if !ok || pctx.strip == "" {
		return nil
	}
	rewritePublicPrefixHeaders(resp, pctx.strip)
	return nil
}

func (s *Server) proxyErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeHTTPError(w, http.StatusGatewayTimeout, "backend timeout")
	case errors.Is(err, errNoTunnel):
		writeHTTPError(w, http.StatusServiceUnavailable, "tunnel gone")
	default:
		writeHTTPError(w, http.StatusBadGateway, "backend error")
	}
}

func stripPort(host string) string {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		return host
	}
	return h
}

func (s *Server) rootHandler(w http.ResponseWriter, r *http.Request) {
	if s.isControlPath(r.URL.Path) {
		s.controlMux.ServeHTTP(w, r)
		return
	}
	s.servePublic(w, r)
}

func (s *Server) isControlPath(p string) bool {
	prefix := config.NormalizeControlPath(s.cfg.ControlPath)
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

func (s *Server) publicListenAddr() string {
	if s.cfg.TLS.Disable {
		if s.cfg.DevListen != "" {
			return s.cfg.DevListen
		}
		return s.cfg.PublicListen
	}
	return s.cfg.PublicListen
}

func (s *Server) httpServer(handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              s.publicListenAddr(),
		Handler:           handler,
		ReadHeaderTimeout: s.cfg.Proxy.ReadHeaderTimeout,
		MaxHeaderBytes:    s.cfg.Proxy.MaxHeaderBytes,
	}
}

func (s *Server) uptime() time.Duration {
	return time.Since(s.startedAt)
}
