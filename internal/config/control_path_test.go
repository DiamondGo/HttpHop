package config

import "testing"

func TestNormalizeControlPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "/tunnel"},
		{"/tunnel", "/tunnel"},
		{"/tunnel/", "/tunnel"},
		{"tunnel", "/tunnel"},
		{"/custom", "/custom"},
	}
	for _, tc := range cases {
		if got := NormalizeControlPath(tc.in); got != tc.want {
			t.Errorf("NormalizeControlPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateControlPathRejectsRoot(t *testing.T) {
	if _, err := validateControlPath("/"); err == nil {
		t.Fatal("expected error for control_path /")
	}
}
