package client

import (
	"testing"
	"time"

	"github.com/DiamondGo/pollmux"
	"go.uber.org/zap/zaptest"
)

func TestSessionDiagnosticsWaveDetection(t *testing.T) {
	t.Parallel()

	logger := zaptest.NewLogger(t)
	d := newSessionDiagnostics()

	d.record(logger, "llm", "sess-1", 800*time.Millisecond, pollmux.OutcomeTransportFailed, "")
	d.record(logger, "llm", "sess-2", 900*time.Millisecond, pollmux.OutcomeTransportFailed, "")

	time.Sleep(10 * time.Millisecond)
	d.record(logger, "llm", "sess-3", 2*time.Second, pollmux.OutcomeTransportFailed, "")
}
