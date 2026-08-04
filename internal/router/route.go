package router

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/DiamondGo/HttpHop/internal/config"
	"github.com/DiamondGo/HttpHop/internal/registry"
)

type PathRewrite struct {
	StripPrefix string
}

type Route struct {
	HostKey    string
	PathPrefix string
	Strip      bool
	Pool       *registry.TunnelPool
}

type hostRoutes struct {
	routes []Route
}

type RouteTable struct {
	byHost map[string]*hostRoutes
}

func NormalizePathPrefix(p string) string {
	if p == "" {
		return ""
	}
	cleaned := path.Clean(p)
	if cleaned == "/" {
		return "/"
	}
	return strings.TrimSuffix(cleaned, "/")
}

func StripPathPrefix(pathStr, prefix string) (newPath string, ok bool) {
	prefix = NormalizePathPrefix(prefix)
	if prefix == "" {
		return pathStr, true
	}
	if prefix == "/" {
		return pathStr, true
	}
	if pathStr == prefix {
		return "/", true
	}
	if strings.HasPrefix(pathStr, prefix+"/") {
		return pathStr[len(prefix):], true
	}
	return pathStr, false
}

func NewRouteTable(bindings []config.ClientBinding, reg *registry.Registry) (*RouteTable, error) {
	rt := &RouteTable{byHost: make(map[string]*hostRoutes)}

	for _, b := range bindings {
		pool := reg.EnsurePool(b.Subdomain)
		hr, ok := rt.byHost[b.Subdomain]
		if !ok {
			hr = &hostRoutes{}
			rt.byHost[b.Subdomain] = hr
		}
		hr.routes = append(hr.routes, Route{
			HostKey:    b.Subdomain,
			PathPrefix: NormalizePathPrefix(b.PathPrefix),
			Strip:      b.StripPrefix,
			Pool:       pool,
		})
	}

	for _, hr := range rt.byHost {
		sort.Slice(hr.routes, func(i, j int) bool {
			return len(hr.routes[i].PathPrefix) > len(hr.routes[j].PathPrefix)
		})
	}

	return rt, rt.Validate()
}

func (t *RouteTable) Validate() error {
	return nil
}

func (t *RouteTable) Match(hostKey, pathStr string) (*Route, error) {
	hr, ok := t.byHost[hostKey]
	if !ok {
		return nil, ErrNoRoute
	}

	cleanPath := path.Clean(pathStr)
	var fallback *Route

	for i := range hr.routes {
		r := &hr.routes[i]
		if r.PathPrefix == "" {
			fallback = r
			continue
		}
		if r.PathPrefix == "/" || cleanPath == r.PathPrefix || strings.HasPrefix(cleanPath, r.PathPrefix+"/") {
			return r, nil
		}
	}

	if fallback != nil {
		return fallback, nil
	}
	return nil, ErrNoRoute
}

func (t *RouteTable) HostKeys() []string {
	keys := make([]string, 0, len(t.byHost))
	for k := range t.byHost {
		keys = append(keys, k)
	}
	return keys
}

func BindingHostKeys(bindings []config.ClientBinding) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, b := range bindings {
		if _, ok := seen[b.Subdomain]; ok {
			continue
		}
		seen[b.Subdomain] = struct{}{}
		out = append(out, b.Subdomain)
	}
	return out
}

func PublicHost(hostKey, rootDomain string) string {
	return FQDN(hostKey, rootDomain)
}

func MatchError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%v", err)
}
