package config

import (
	"fmt"
	"path"
	"strings"
)

const defaultControlPath = "/tunnel"

// NormalizeControlPath returns a canonical control-plane URL path prefix.
func NormalizeControlPath(p string) string {
	normalized, err := validateControlPath(p)
	if err != nil {
		return defaultControlPath
	}
	return normalized
}

func validateControlPath(p string) (string, error) {
	if p == "" {
		return defaultControlPath, nil
	}
	cleaned := path.Clean(p)
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	if cleaned == "/" {
		return "", fmt.Errorf("control_path cannot be /")
	}
	return strings.TrimSuffix(cleaned, "/"), nil
}

func validateControlPathNotConflicting(controlPath string, clients []ClientBinding) error {
	for _, cb := range clients {
		pp := normalizePathPrefix(cb.PathPrefix)
		if pp == "" {
			continue
		}
		if pp == controlPath ||
			strings.HasPrefix(controlPath, pp+"/") ||
			strings.HasPrefix(pp, controlPath+"/") {
			return fmt.Errorf("control_path %q conflicts with client path_prefix %q on client_id %q",
				controlPath, pp, cb.ClientID)
		}
	}
	return nil
}
