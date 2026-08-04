package server

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"go.uber.org/zap"
	"golang.org/x/crypto/acme/autocert"

	"github.com/DiamondGo/HttpHop/internal/config"
	"github.com/DiamondGo/HttpHop/internal/registry"
	"github.com/DiamondGo/HttpHop/internal/router"
)

func (s *Server) setupTLS() (*autocert.Manager, error) {
	if s.cfg.TLS.Disable {
		return nil, nil
	}
	if s.cfg.TLS.CacheDir == "" {
		return nil, fmt.Errorf("tls.cache_dir is required when TLS is enabled")
	}
	m := &autocert.Manager{
		Cache:      autocert.DirCache(s.cfg.TLS.CacheDir),
		Prompt:     autocert.AcceptTOS,
		Email:      s.cfg.TLS.Email,
		HostPolicy: router.HostPolicyFunc(s.registry, s.cfg.RootDomain, s.cfg.ControlHost),
	}
	s.certManager = m
	return m, nil
}

func (s *Server) warmCert(subdomain string) {
	if s.certManager == nil {
		return
	}
	host := router.FQDN(subdomain, s.cfg.RootDomain)
	go func() {
		_, err := s.certManager.GetCertificate(&tls.ClientHelloInfo{ServerName: host})
		if err != nil {
			s.logger.Warn("certificate warm-up failed",
				zap.String("host", host), zap.Error(err))
		}
	}()
}

func httpsRedirectHandler(_ string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := "https://" + r.Host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})
}

func buildRouteTable(cfg *config.ServerConfig, reg *registry.Registry) (*router.RouteTable, error) {
	for _, b := range cfg.Tunnels {
		reg.EnsurePool(b.Subdomain)
	}
	return router.NewRouteTable(cfg.Tunnels, reg)
}
