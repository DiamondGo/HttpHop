package router_test

import (
	"errors"
	"testing"

	"github.com/DiamondGo/HttpHop/internal/router"
)

func TestHostKey(t *testing.T) {
	root := "builderrors.com"
	cases := []struct {
		host    string
		want    string
		wantErr error
	}{
		{"myapp.builderrors.com", "myapp", nil},
		{"myapp.builderrors.com:443", "myapp", nil},
		{"builderrors.com", "@", nil},
		{"evil.com", "", router.ErrRootMismatch},
		{"a.b.builderrors.com", "", router.ErrNestedSubdomain},
	}
	for _, tc := range cases {
		got, err := router.HostKey(tc.host, root)
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("HostKey(%q): got err %v, want %v", tc.host, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("HostKey(%q): unexpected err %v", tc.host, err)
			continue
		}
		if got != tc.want {
			t.Errorf("HostKey(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestStripPathPrefix(t *testing.T) {
	cases := []struct {
		path, prefix string
		want         string
		ok           bool
	}{
		{"/service/auth", "/service", "/auth", true},
		{"/service", "/service", "/", true},
		{"/service/auth", "/service", "/auth", true},
		{"/other", "/service", "/other", false},
	}
	for _, tc := range cases {
		got, ok := router.StripPathPrefix(tc.path, tc.prefix)
		if ok != tc.ok || got != tc.want {
			t.Errorf("StripPathPrefix(%q,%q) = (%q,%v), want (%q,%v)", tc.path, tc.prefix, got, ok, tc.want, tc.ok)
		}
	}
}
