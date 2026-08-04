package server

import (
	"net/http"

	"github.com/DiamondGo/HttpHop/internal/config"
	"github.com/DiamondGo/HttpHop/internal/registry"
)

type statusResponse struct {
	Version       string                   `json:"version"`
	UptimeSeconds int64                    `json:"uptime_seconds"`
	Tunnels       []registry.TunnelStatus  `json:"tunnels"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	writeJSON(w, http.StatusOK, statusResponse{
		Version:       config.Version,
		UptimeSeconds: int64(s.uptime().Seconds()),
		Tunnels:       s.registry.Snapshot(),
	})
}
