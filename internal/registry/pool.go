package registry

import (
	"net/http"
	"sync"
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
		if m.Alive() && m.LocalHealthy.Load() {
			return m
		}
	}
	return nil
}

func (p *TunnelPool) Add(t *ClientTunnel, max int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, m := range p.members {
		if m.ID == t.ID {
			old := p.members[i]
			p.members[i] = t
			go old.Close()
			return nil
		}
	}

	if len(p.members) >= max {
		return ErrPoolFull
	}
	p.members = append(p.members, t)
	return nil
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
