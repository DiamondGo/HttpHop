package client

import (
	"context"

	"github.com/DiamondGo/pollmux"
)

// sessionEndDetail classifies why a tunnel session ended, for diagnostics.detail.
func sessionEndDetail(ctx context.Context, conn pollmux.Conn, outcome pollmux.Outcome) string {
	switch outcome {
	case pollmux.OutcomeShutdown:
		if err := context.Cause(ctx); err != nil {
			return err.Error()
		}
		return "client process shutting down"

	case pollmux.OutcomeSuperseded:
		return "session superseded by a newer connect with the same client_id " +
			"(duplicate httphop-client process or overlapping reconnect)"

	case pollmux.OutcomePeerClosed:
		return "yamux session closed while HTTP transport was still healthy (server-side session end)"

	case pollmux.OutcomeTransportFailed:
		if channelClosed(conn.SessionSuperseded()) {
			return "HTTP transport failed after session superseded — " +
				"check for duplicate httphop-client using the same client_id"
		}
		if channelClosed(conn.TransportFailed()) {
			return "HTTP transport failed (poll/send network error, timeout, or server 410 session closed)"
		}
		return "transport failed (see pollmux transport problem logs above for the exact HTTP error)"
	default:
		return ""
	}
}

func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
