package router

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"golang.org/x/crypto/acme/autocert"
)

var (
	ErrRootMismatch    = errors.New("host does not belong to root domain")
	ErrNestedSubdomain = errors.New("nested subdomains are not supported")
	ErrNoRoute         = errors.New("no route for host/path")
)

func HostKey(host, rootDomain string) (string, error) {
	host = stripPort(host)
	rootDomain = strings.ToLower(strings.TrimSpace(rootDomain))
	host = strings.ToLower(host)

	if host == rootDomain {
		return "@", nil
	}

	suffix := "." + rootDomain
	if !strings.HasSuffix(host, suffix) {
		return "", ErrRootMismatch
	}

	sub := strings.TrimSuffix(host, suffix)
	if sub == "" {
		return "@", nil
	}
	if strings.Contains(sub, ".") {
		return "", ErrNestedSubdomain
	}
	return sub, nil
}

func stripPort(host string) string {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		return host
	}
	return h
}

// HostLister returns registered host keys for certificate issuance.
type HostLister interface {
	Subdomains() []string
}

func AllowedHosts(reg HostLister, root string) func(string) bool {
	return func(host string) bool {
		host = stripPort(strings.ToLower(host))
		root = strings.ToLower(root)

		key, err := HostKey(host, root)
		if err != nil {
			return false
		}

		for _, sub := range reg.Subdomains() {
			if sub != key {
				continue
			}
			if key == "@" {
				return host == root
			}
			return host == sub+"."+root
		}
		return false
	}
}

func HostPolicyFunc(reg HostLister, root string) autocert.HostPolicy {
	allowed := AllowedHosts(reg, root)
	return func(_ context.Context, host string) error {
		if allowed(host) {
			return nil
		}
		return fmt.Errorf("acme/autocert: host %q not allowed", host)
	}
}

func FQDN(hostKey, rootDomain string) string {
	if hostKey == "@" {
		return rootDomain
	}
	return hostKey + "." + rootDomain
}
