package server

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/DiamondGo/HttpHop/internal/config"
)

type ClientRegistry struct {
	byID map[string]*config.ClientBinding
}

func NewClientRegistry(bindings []config.ClientBinding) *ClientRegistry {
	m := make(map[string]*config.ClientBinding, len(bindings))
	for i := range bindings {
		b := &bindings[i]
		m[b.ClientID] = b
	}
	return &ClientRegistry{byID: m}
}

func (cr *ClientRegistry) Lookup(clientID string) (*config.ClientBinding, bool) {
	b, ok := cr.byID[clientID]
	return b, ok
}

func validToken(expected, provided string) bool {
	if expected == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}
