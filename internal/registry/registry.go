package registry

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DiamondGo/pollmux"
	"github.com/hashicorp/yamux"
)

var ErrPoolFull = errors.New("tunnel pool full")

const defaultSupersedeDrainTimeout = 10 * time.Minute

type ClientTunnel struct {
	ID          string
	Subdomain   string
	Session     *pollmux.Session
	Yamux       *yamux.Session
	ConnectedAt time.Time
	RemoteAddr  string

	LocalHealthy  atomic.Bool
	Draining      atomic.Bool
	Closed        atomic.Bool
	ActiveStreams atomic.Int64
}

func (t *ClientTunnel) Alive() bool {
	if t.Yamux == nil || t.Yamux.IsClosed() {
		return false
	}
	if t.Session == nil || t.Session.IsClosed() {
		return false
	}
	return true
}

func (t *ClientTunnel) Close() error {
	t.Closed.Store(true)
	var err error
	if t.Yamux != nil {
		err = t.Yamux.Close()
	}
	if t.Session != nil {
		_ = t.Session.Close()
	}
	return err
}

type Registry struct {
	mu                   sync.RWMutex
	byName               map[string]*TunnelPool
	bySess               map[string]*ClientTunnel
	supersedeDrainTimeout time.Duration
}

func NewRegistry(supersedeDrainTimeout time.Duration) *Registry {
	if supersedeDrainTimeout <= 0 {
		supersedeDrainTimeout = defaultSupersedeDrainTimeout
	}
	return &Registry{
		byName:                make(map[string]*TunnelPool),
		bySess:                make(map[string]*ClientTunnel),
		supersedeDrainTimeout: supersedeDrainTimeout,
	}
}

func (r *Registry) EnsurePool(subdomain string) *TunnelPool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.byName[subdomain]; ok {
		return p
	}
	p := &TunnelPool{bal: firstAvailable{}}
	r.byName[subdomain] = p
	return p
}

func (r *Registry) Register(t *ClientTunnel, maxPerSubdomain int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	pool, ok := r.byName[t.Subdomain]
	if !ok {
		pool = &TunnelPool{bal: firstAvailable{}}
		r.byName[t.Subdomain] = pool
	}

	if old := pool.ByID(t.ID); old != nil && old.Session != nil {
		delete(r.bySess, old.Session.ID)
	}

	if err := pool.Add(t, maxPerSubdomain, r.supersedeDrainTimeout); err != nil {
		return err
	}

	t.LocalHealthy.Store(true)
	if t.Session != nil {
		r.bySess[t.Session.ID] = t
	}
	return nil
}

func (r *Registry) GetBySessionID(id string) (*ClientTunnel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.bySess[id]
	return t, ok
}

func (r *Registry) Pool(subdomain string) (*TunnelPool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byName[subdomain]
	return p, ok
}

func (r *Registry) Subdomains() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byName))
	for name := range r.byName {
		out = append(out, name)
	}
	return out
}

func (r *Registry) SetLocalHealth(sub, clientID string, ok bool) {
	r.mu.RLock()
	pool, exists := r.byName[sub]
	r.mu.RUnlock()
	if !exists {
		return
	}
	if t := pool.ByID(clientID); t != nil {
		t.LocalHealthy.Store(ok)
	}
}

func (r *Registry) RemoveBySessionID(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.bySess[sessionID]
	if !ok {
		return
	}
	delete(r.bySess, sessionID)
	if pool, ok := r.byName[t.Subdomain]; ok {
		pool.Remove(t.ID)
	}
}

type TunnelStatus struct {
	Subdomain string         `json:"subdomain"`
	Clients   []ClientStatus `json:"clients"`
}

type ClientStatus struct {
	ClientID           string    `json:"client_id"`
	SessionID          string    `json:"session_id"`
	RemoteAddr         string    `json:"remote_addr"`
	ConnectedAt        time.Time `json:"connected_at"`
	LastPollAt         time.Time `json:"last_poll_at"`
	PollInFlight       int32     `json:"poll_in_flight"`
	ActiveStreams      int64     `json:"active_streams"`
	LocalHealth        string    `json:"local_health"`
	BufferedToServer   int       `json:"buffered_to_server"`
	BufferedFromServer int       `json:"buffered_from_server"`
}

func (r *Registry) Snapshot() []TunnelStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]TunnelStatus, 0, len(r.byName))
	for sub, pool := range r.byName {
		ts := TunnelStatus{Subdomain: sub}
		for _, t := range pool.Members() {
			health := "down"
			if t.LocalHealthy.Load() {
				health = "ok"
			}
			var lastPoll time.Time
			var pollInFlight int32
			var bufToServer, bufFromServer int
			if t.Session != nil {
				lastPoll = t.Session.LastActive()
				pollInFlight = t.Session.PollInFlight()
				bufToServer, bufFromServer = t.Session.HiWater()
			}
			ts.Clients = append(ts.Clients, ClientStatus{
				ClientID:           t.ID,
				SessionID:          t.Session.ID,
				RemoteAddr:         t.RemoteAddr,
				ConnectedAt:        t.ConnectedAt,
				LastPollAt:         lastPoll,
				PollInFlight:       pollInFlight,
				ActiveStreams:      t.ActiveStreams.Load(),
				LocalHealth:        health,
				BufferedToServer:   bufToServer,
				BufferedFromServer: bufFromServer,
			})
		}
		out = append(out, ts)
	}
	return out
}
