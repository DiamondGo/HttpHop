package registry

import (
	"net/http"
	"sync"
	"time"
)

type TunnelPool struct {
	mu      sync.RWMutex
	members []*ClientTunnel
	bal     Balancer
}

type Balancer interface {
	Pick(members []*ClientTunnel, r *http.Request) *ClientTunnel
}

type firstAvailable struct{}

func (firstAvailable) Pick(members []*ClientTunnel, _ *http.Request) *ClientTunnel {
	for _, m := range members {
		if m.Alive() && m.LocalHealthy.Load() && !m.Draining.Load() {
			return m
		}
	}
	return nil
}

func (p *TunnelPool) Add(t *ClientTunnel, max int, drainTimeout time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, m := range p.members {
		if m.ID == t.ID {
			old := p.members[i]
			p.members[i] = t
			beginSupersedeDrain(old, drainTimeout)
			return nil
		}
	}

	if len(p.members) >= max {
		return ErrPoolFull
	}
	p.members = append(p.members, t)
	return nil
}

// beginSupersedeDrain keeps a replaced tunnel alive until in-flight streams
// finish or drainTimeout elapses. New requests route to the replacement.
func beginSupersedeDrain(old *ClientTunnel, drainTimeout time.Duration) {
	if drainTimeout <= 0 {
		drainTimeout = defaultSupersedeDrainTimeout
	}
	old.Draining.Store(true)
	go func() {
		deadline := time.Now().Add(drainTimeout)
		for time.Now().Before(deadline) {
			if old.ActiveStreams.Load() == 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		_ = old.Close()
	}()
}

func (p *TunnelPool) Remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, m := range p.members {
		if m.ID == id {
			p.members = append(p.members[:i], p.members[i+1:]...)
			return
		}
	}
}

func (p *TunnelPool) Pick(r *http.Request) (*ClientTunnel, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.bal == nil {
		p.bal = firstAvailable{}
	}
	t := p.bal.Pick(p.members, r)
	return t, t != nil
}

func (p *TunnelPool) ByID(id string) *ClientTunnel {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, m := range p.members {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func (p *TunnelPool) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.members)
}

func (p *TunnelPool) Members() []*ClientTunnel {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*ClientTunnel, len(p.members))
	copy(out, p.members)
	return out
}

func (p *TunnelPool) CountAlive() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := 0
	for _, m := range p.members {
		if m.Alive() {
			n++
		}
	}
	return n
}
