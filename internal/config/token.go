package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func readTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file %q: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file %q is empty", path)
	}
	return token, nil
}

func resolveClientBindingTokens(clients []ClientBinding, configPath string) error {
	baseDir := filepath.Dir(configPath)
	for i := range clients {
		cb := &clients[i]
		if cb.TokenFile == "" {
			return fmt.Errorf("client_id %q requires token_file", cb.ClientID)
		}
		path := cb.TokenFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		tok, err := readTokenFile(path)
		if err != nil {
			return fmt.Errorf("client_id %q: %w", cb.ClientID, err)
		}
		cb.Token = tok
		if len(cb.Token) < 32 {
			return fmt.Errorf("token for client_id %q must be at least 32 characters", cb.ClientID)
		}
	}
	return nil
}

func resolveClientAuthToken(cfg *ClientConfig, configPath string) error {
	if cfg.Server.TokenFile != "" {
		path := cfg.Server.TokenFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(configPath), path)
		}
		tok, err := readTokenFile(path)
		if err != nil {
			return err
		}
		cfg.Server.Token = tok
		return nil
	}
	if cfg.Server.Token != "" {
		return nil
	}
	return fmt.Errorf("server.token_file is required")
}
