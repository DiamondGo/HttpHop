package client

import (
	"log/slog"
	"testing"

	"go.uber.org/zap"

	"github.com/DiamondGo/HttpHop/internal/config"
)

func TestBuildConnectorMapsResumeSettings(t *testing.T) {
	cfg := config.DefaultClient()
	cfg.Transport.PreferResume = true
	cfg.Transport.MaxReplayBytes = 7 << 20

	c := New(&cfg, zap.NewNop())
	connector := c.buildConnector(slog.Default())
	if !connector.PreferResume {
		t.Fatal("PreferResume was not mapped to pollmux.Connector")
	}
	if connector.MaxReplayBytes != 7<<20 {
		t.Fatalf("MaxReplayBytes = %d, want %d", connector.MaxReplayBytes, 7<<20)
	}

}
