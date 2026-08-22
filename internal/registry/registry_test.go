package registry_test

import (
	"testing"

	"github.com/DiamondGo/HttpHop/internal/registry"
)

func TestPoolSameIDReplaces(t *testing.T) {
	reg := registry.NewRegistry(0)
	pool := reg.EnsurePool("myapp")

	t1 := &registry.ClientTunnel{ID: "c1", Subdomain: "myapp"}
	t2 := &registry.ClientTunnel{ID: "c1", Subdomain: "myapp"}

	if err := pool.Add(t1, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := pool.Add(t2, 1, 0); err != nil {
		t.Fatal(err)
	}
	if pool.Len() != 1 {
		t.Fatalf("expected 1 member, got %d", pool.Len())
	}
}

func TestPoolFull(t *testing.T) {
	pool := &registry.TunnelPool{}
	t1 := &registry.ClientTunnel{ID: "c1"}
	t2 := &registry.ClientTunnel{ID: "c2"}
	if err := pool.Add(t1, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := pool.Add(t2, 1, 0); err != registry.ErrPoolFull {
		t.Fatalf("expected ErrPoolFull, got %v", err)
	}
}

func TestRegisterPoolFull(t *testing.T) {
	reg := registry.NewRegistry(0)
	t1 := &registry.ClientTunnel{ID: "c1", Subdomain: "app"}
	t2 := &registry.ClientTunnel{ID: "c2", Subdomain: "app"}
	if err := reg.Register(t1, 1); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(t2, 1); err != registry.ErrPoolFull {
		t.Fatalf("expected ErrPoolFull, got %v", err)
	}
}
