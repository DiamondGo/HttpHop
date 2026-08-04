package server

import (
	"net/http"
	"testing"
)

func TestRewriteLocation(t *testing.T) {
	prefix := "/service"
	cases := []struct {
		in, want string
	}{
		{"/login", "/service/login"},
		{"/", "/service"},
		{"/service/login", "/service/login"},
		{"/api/v1", "/service/api/v1"},
		{"https://builderrors.com/login", "https://builderrors.com/service/login"},
		{"https://builderrors.com/service/x", "https://builderrors.com/service/x"},
		{"login", "login"},
	}
	for _, tc := range cases {
		got := rewriteLocation(tc.in, prefix)
		if got != tc.want {
			t.Errorf("rewriteLocation(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRewriteSetCookiePath(t *testing.T) {
	prefix := "/service"
	cases := []struct {
		in, want string
	}{
		{
			"session=abc; Path=/; HttpOnly",
			"session=abc; Path=/service; HttpOnly",
		},
		{
			"session=abc; Path=/auth; Secure",
			"session=abc; Path=/service/auth; Secure",
		},
		{
			"session=abc; Path=/service/auth; Secure",
			"session=abc; Path=/service/auth; Secure",
		},
		{
			"session=abc; HttpOnly",
			"session=abc; HttpOnly",
		},
	}
	for _, tc := range cases {
		got := rewriteSetCookiePath(tc.in, prefix)
		if got != tc.want {
			t.Errorf("rewriteSetCookiePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRewritePublicPrefixHeaders(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{
			"Location":   []string{"/dashboard"},
			"Set-Cookie": []string{"sid=1; Path=/"},
		},
	}
	rewritePublicPrefixHeaders(resp, "/service")
	if got := resp.Header.Get("Location"); got != "/service/dashboard" {
		t.Fatalf("Location = %q", got)
	}
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) != 1 || cookies[0] != "sid=1; Path=/service" {
		t.Fatalf("Set-Cookie = %q", cookies)
	}
}
