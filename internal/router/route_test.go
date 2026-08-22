package router_test

import (
	"errors"
	"testing"

	"github.com/DiamondGo/HttpHop/internal/config"
	"github.com/DiamondGo/HttpHop/internal/registry"
	"github.com/DiamondGo/HttpHop/internal/router"
)

func TestRouteTableLongestPrefix(t *testing.T) {
	reg := registry.NewRegistry(0)
	bindings := []config.ClientBinding{
		{ClientID: "a", Subdomain: "@", PathPrefix: "/service", StripPrefix: true},
		{ClientID: "b", Subdomain: "@", PathPrefix: "/api/v1", StripPrefix: true},
		{ClientID: "c", Subdomain: "myapp"},
	}
	rt, err := router.NewRouteTable(bindings, reg)
	if err != nil {
		t.Fatal(err)
	}

	r, err := rt.Match("@", "/service/auth")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Strip || r.PathPrefix != "/service" {
		t.Fatalf("unexpected route: %+v", r)
	}

	r, err = rt.Match("@", "/api/v1/x")
	if err != nil {
		t.Fatal(err)
	}
	if r.PathPrefix != "/api/v1" {
		t.Fatalf("prefix = %q", r.PathPrefix)
	}

	r, err = rt.Match("myapp", "/")
	if err != nil {
		t.Fatal(err)
	}
	if r.HostKey != "myapp" {
		t.Fatalf("host = %q", r.HostKey)
	}

	_, err = rt.Match("@", "/other")
	if !errors.Is(err, router.ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute, got %v", err)
	}
}
