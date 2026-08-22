package registry_test

import (
	"testing"
	"time"

	"github.com/DiamondGo/HttpHop/internal/registry"
)

func TestSupersedeDrainWaitsForActiveStreams(t *testing.T) {
	pool := &registry.TunnelPool{}

	old := &registry.ClientTunnel{ID: "c1", Subdomain: "app"}
	old.ActiveStreams.Store(1)

	replacement := &registry.ClientTunnel{ID: "c1", Subdomain: "app"}
	if err := pool.Add(old, 1, 500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := pool.Add(replacement, 1, 500*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	picked := pool.ByID("c1")
	if picked != replacement {
		t.Fatal("expected new tunnel to replace old in pool")
	}
	if !old.Draining.Load() {
		t.Fatal("expected replaced tunnel to be marked draining")
	}

	time.Sleep(50 * time.Millisecond)
	if old.Closed.Load() {
		t.Fatal("draining tunnel closed before streams finished")
	}

	old.ActiveStreams.Store(0)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if old.Closed.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("draining tunnel was not closed after streams drained")
}

func TestSupersedeDrainTimesOut(t *testing.T) {
	pool := &registry.TunnelPool{}

	old := &registry.ClientTunnel{ID: "c1", Subdomain: "app"}
	old.ActiveStreams.Store(1)

	replacement := &registry.ClientTunnel{ID: "c1", Subdomain: "app"}
	if err := pool.Add(old, 1, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := pool.Add(replacement, 1, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if old.Closed.Load() {
			if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
				t.Fatalf("draining tunnel closed too early: %v", elapsed)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("draining tunnel was not closed after timeout")
}
