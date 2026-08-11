package client

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/DiamondGo/HttpHop/internal/config"
)

func TestBuildConnectorStreamOptionsWired(t *testing.T) {
	cfg := &config.ClientConfig{
		ClientID: "test",
		Server: config.ServerRef{
			URL:   "http://127.0.0.1:1",
			Token: "tok",
		},
		Transport: config.TransportConfig{
			PreferStream:           true,
			UploadStreamPreference: "stream",
			UploadProbeTimeout:     5 * time.Second,
			PreferWebSocket:        true,
		},
	}
	c := New(cfg, zap.NewNop())
	connector := c.buildConnector(nil)

	if !connector.PreferStream {
		t.Error("PreferStream not wired from transport config")
	}
	if connector.UploadStreamPreference != "stream" {
		t.Errorf("UploadStreamPreference = %q, want %q", connector.UploadStreamPreference, "stream")
	}
	if connector.UploadProbeTimeout != 5*time.Second {
		t.Errorf("UploadProbeTimeout = %v, want 5s", connector.UploadProbeTimeout)
	}
	if !connector.PreferWebSocket {
		t.Error("PreferWebSocket not wired from transport config")
	}
}

func TestBuildConnectorStreamOptionsDefaultOff(t *testing.T) {
	cfg := &config.ClientConfig{
		ClientID: "test",
		Server: config.ServerRef{
			URL:   "http://127.0.0.1:1",
			Token: "tok",
		},
	}
	c := New(cfg, zap.NewNop())
	connector := c.buildConnector(nil)

	if connector.PreferStream {
		t.Error("PreferStream should default to false")
	}
	if connector.UploadStreamPreference != "" {
		t.Errorf("UploadStreamPreference = %q, want empty", connector.UploadStreamPreference)
	}
	if connector.PreferWebSocket {
		t.Error("PreferWebSocket should default to false")
	}
}
