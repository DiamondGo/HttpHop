package client

import (
	"sync"
	"time"

	"github.com/DiamondGo/pollmux"
	"go.uber.org/zap"
)

const reconnectWaveGap = 5 * time.Second

// sessionDiagnostics tracks reconnect patterns to surface the first failure
// in a storm and flag sustained fast reconnect loops.
type sessionDiagnostics struct {
	mu              sync.Mutex
	lastEndedAt     time.Time
	consecutiveFast int
}

func newSessionDiagnostics() *sessionDiagnostics {
	return &sessionDiagnostics{}
}

func (d *sessionDiagnostics) record(logger *zap.Logger, clientID, sessionID string, served time.Duration, outcome pollmux.Outcome, detail string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	gap := time.Duration(0)
	if !d.lastEndedAt.IsZero() {
		gap = time.Since(d.lastEndedAt)
	}
	d.lastEndedAt = time.Now()

	fields := []zap.Field{
		zap.String("client_id", clientID),
		zap.String("session_id", sessionID),
		zap.Duration("served_for", served),
		zap.String("outcome", outcome.String()),
	}
	if gap > 0 {
		fields = append(fields, zap.Duration("since_last_session", gap))
	}
	if detail != "" {
		fields = append(fields, zap.String("detail", detail))
	}

	switch outcome {
	case pollmux.OutcomeSuperseded:
		logger.Info("tunnel session superseded by newer connect, stopping reconnect loop", fields...)
		d.consecutiveFast = 0

	case pollmux.OutcomeTransportFailed:
		waveBreak := gap == 0 || gap >= reconnectWaveGap
		if waveBreak {
			d.consecutiveFast = 1
			logger.Warn("tunnel session ended — possible reconnect wave start (check for duplicate client processes or network blips)", fields...)
		} else {
			d.consecutiveFast++
			fields = append(fields, zap.Int("consecutive_fast_failures", d.consecutiveFast))
			logger.Warn("tunnel session ended — fast reconnect loop in progress", fields...)
		}

	case pollmux.OutcomePeerClosed:
		d.consecutiveFast = 0
		logger.Info("tunnel session closed by peer, reconnecting promptly", fields...)

	case pollmux.OutcomeShutdown:
		d.consecutiveFast = 0
		logger.Info("tunnel session shutting down", fields...)
	}
}
