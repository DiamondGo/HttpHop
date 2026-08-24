#!/usr/bin/env bash
# Audit the local httphop-client deployment. Read-only checks.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
STALE_BIN="$REPO_ROOT/client"
LAUNCHAGENT="$HOME/Library/LaunchAgents/com.httphop.client.plist"
ISSUES=0

warn() {
	echo "WARN: $*" >&2
	ISSUES=$((ISSUES + 1))
}
ok() {
	echo "OK:   $*"
}

echo "HttpHop client preflight (local machine)"
echo "repo: $REPO_ROOT"
echo

# P0: single process
PIDS=$(pgrep -f "httphop-client" 2>/dev/null || true)
LOCK_FILE="${HTTPHOP_CLIENT_LOCK:-/tmp/httphop-client.lock}"
if [[ -n "$PIDS" ]]; then
	count=$(echo "$PIDS" | wc -l | tr -d ' ')
	if [[ "$count" -eq 1 ]]; then
		ok "single httphop-client process (PID $PIDS)"
	else
		warn "multiple httphop-client processes: $PIDS"
	fi
elif [[ -f "$LOCK_FILE" ]]; then
	warn "lock file exists but no process: $LOCK_FILE (stale lock?)"
else
	ok "no httphop-client process running"
fi

# P0: stale repo-root binary
if [[ -x "$STALE_BIN" ]]; then
	warn "stale binary at $STALE_BIN (use bin/httphop-client only; remove this file)"
else
	ok "no stale ./client binary in repo root"
fi

# LaunchAgent
if [[ -f "$LAUNCHAGENT" ]]; then
	ok "LaunchAgent installed ($LAUNCHAGENT)"
	if launchctl print "gui/$(id -u)/com.httphop.client" >/dev/null 2>&1; then
		ok "LaunchAgent loaded"
	else
		warn "LaunchAgent plist exists but service is not loaded"
	fi
else
	warn "LaunchAgent not installed (optional)"
fi

# cron / other schedulers
if crontab -l 2>/dev/null | grep -q httphop; then
	warn "crontab references httphop — verify it does not start a second client"
else
	ok "no httphop entries in user crontab"
fi

# scripts only reference bin/httphop-client
if grep -R --include='*.sh' -E '(^|/|\./)client[^/]|/client"' "$REPO_ROOT/scripts" 2>/dev/null \
	| grep -v httphop-client \
	| grep -v client-service \
	| grep -v client-preflight \
	| grep -v client.yaml; then
	warn "script may invoke a non-standard client binary (see grep output above)"
else
	ok "scripts reference bin/httphop-client only"
fi

# P1: other machines — manual
echo
echo "Manual (cannot verify from this host):"
echo "  - No other machine may run httphop-client with the same myai/llm token."
echo "  - Server log 'protocol_version 0' means an old client binary is connecting somewhere."
echo "  - Compare client_id connect timestamps on VPS /status with this machine's logs."

echo
if [[ "$ISSUES" -eq 0 ]]; then
	echo "Preflight passed."
	exit 0
fi
echo "Preflight finished with $ISSUES issue(s)."
exit 1
