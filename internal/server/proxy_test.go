package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/DiamondGo/HttpHop/internal/config"
)

const testClientToken = "pppppppppppppppppppppppppppppppp"

func TestRootHandlerRoutesByControlPath(t *testing.T) {
	cfg := config.Defaults()
	cfg.RootDomain = "example.com"
	cfg.TLS.Disable = false
	cfg.ControlPath = "/tunnel"
	cfg.Clients = []config.ClientBinding{{
		ClientID:   "ai-1",
		Subdomain:  "ai",
		Token:      testClientToken,
		MaxClients: 1,
	}}

	srv, err := NewServer(&cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	publicHost := "ai.example.com"
	cases := []struct {
		name       string
		path       string
		wantPublic bool
	}{
		{"public path", "/chat", true},
		{"control connect", "/tunnel/connect", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+publicHost+tc.path, nil)
			req.Host = publicHost
			rec := httptest.NewRecorder()

			srv.rootHandler(rec, req)

			if tc.wantPublic {
				if rec.Code == http.StatusMethodNotAllowed {
					t.Fatalf("path %q hit control plane (status %d)", tc.path, rec.Code)
				}
				return
			}
			if rec.Code == http.StatusNotFound || rec.Code == http.StatusServiceUnavailable {
				t.Fatalf("path %q expected control plane, got public status %d", tc.path, rec.Code)
			}
		})
	}
}

func TestRootHandlerCustomControlPath(t *testing.T) {
	cfg := config.Defaults()
	cfg.RootDomain = "example.com"
	cfg.ControlPath = "/hop"
	cfg.TLS.Disable = true
	cfg.Clients = []config.ClientBinding{{
		ClientID:   "ai-1",
		Subdomain:  "ai",
		Token:      testClientToken,
		MaxClients: 1,
	}}

	srv, err := NewServer(&cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://ai.example.com/hop/connect", nil)
	req.Host = "ai.example.com"
	rec := httptest.NewRecorder()
	srv.rootHandler(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("custom control path was not routed to control plane")
	}
}

func TestClientRegistryLookup(t *testing.T) {
	reg := NewClientRegistry([]config.ClientBinding{{
		ClientID:  strings.Repeat("c", 8),
		Subdomain: "ai",
	}})
	if _, ok := reg.Lookup("missing"); ok {
		t.Fatal("expected miss")
	}
}
