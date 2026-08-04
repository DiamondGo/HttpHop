package router_test

import (
	"testing"

	"github.com/DiamondGo/HttpHop/internal/config"
	"github.com/DiamondGo/HttpHop/internal/registry"
	"github.com/DiamondGo/HttpHop/internal/router"
)

func newTestRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	return registry.NewRegistry()
}

func TestRouteTableMatch(t *testing.T) {
	reg := newTestRegistry(t)
	bindings := []config.TunnelBinding{
		{Subdomain: "@", PathPrefix: "/service", StripPrefix: true, Token: "token-a"},
		{Subdomain: "@", PathPrefix: "/api/v1", StripPrefix: true, Token: "token-b"},
		{Subdomain: "myapp", Token: "token-c"},
	}
	rt, err := router.NewRouteTable(bindings, reg)
	if err != nil {
		t.Fatal(err)
	}

	r, err := rt.Match("@", "/service/auth")
	if err != nil {
		t.Fatal(err)
	}
	if r.PathPrefix != "/service" || !r.Strip {
		t.Fatalf("unexpected route: %+v", r)
	}

	r, err = rt.Match("@", "/api/v1/x")
	if err != nil {
		t.Fatal(err)
	}
	if r.PathPrefix != "/api/v1" {
		t.Fatalf("expected /api/v1 route, got %q", r.PathPrefix)
	}

	r, err = rt.Match("myapp", "/foo")
	if err != nil {
		t.Fatal(err)
	}
	if r.PathPrefix != "" {
		t.Fatalf("expected fallback route")
	}

	_, err = rt.Match("@", "/unknown")
	if err == nil {
		t.Fatal("expected no route")
	}
}
