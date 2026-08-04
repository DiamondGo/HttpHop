package server

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/DiamondGo/HttpHop/internal/config"
)

type TokenStore struct {
	byToken map[string]*config.TunnelBinding
}

func NewTokenStore(bindings []config.TunnelBinding) *TokenStore {
	m := make(map[string]*config.TunnelBinding, len(bindings))
	for i := range bindings {
		b := &bindings[i]
		m[b.Token] = b
	}
	return &TokenStore{byToken: m}
}

func (ts *TokenStore) Lookup(token string) (*config.TunnelBinding, bool) {
	for stored, binding := range ts.byToken {
		if subtle.ConstantTimeCompare([]byte(stored), []byte(token)) == 1 {
			return binding, true
		}
	}
	return nil, false
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}
