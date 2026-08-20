#!/usr/bin/env bash
set -euo pipefail

LABEL="com.httphop.client"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
PLIST="$HOME/Library/LaunchAgents/${LABEL}.plist"
DOMAIN="gui/$(id -u)"
SERVICE="${DOMAIN}/${LABEL}"

BINARY="${HTTPHOP_CLIENT_BIN:-$REPO_ROOT/bin/httphop-client}"
CONFIG="${HTTPHOP_CLIENT_CONFIG:-$REPO_ROOT/local/client.yaml}"
LOG_DIR="$REPO_ROOT/local/logs"
STDOUT_LOG="$LOG_DIR/httphop-client.log"
STDERR_LOG="$LOG_DIR/httphop-client.err.log"

usage() {
	cat <<EOF
Usage: $(basename "$0") <command>

Manage HttpHop client as a macOS LaunchAgent (auto-start at login).

Commands:
  install    Write LaunchAgent plist and enable auto-start
  uninstall  Stop service and remove LaunchAgent plist
  start      Start the client (enable + load)
  stop       Stop the client (unload + disable until next start)
  restart    stop then start
  status     Show whether the service is loaded and running
  logs       Tail client stdout/stderr logs

Environment overrides:
  HTTPHOP_CLIENT_BIN     Path to httphop-client binary
  HTTPHOP_CLIENT_CONFIG  Path to client.yaml

Defaults:
  binary: $BINARY
  config: $CONFIG
EOF
}

require_binary() {
	if [[ ! -x "$BINARY" ]]; then
		echo "error: binary not found or not executable: $BINARY" >&2
		echo "Run 'make build' in $REPO_ROOT first." >&2
		exit 1
	fi
}

require_config() {
	if [[ ! -f "$CONFIG" ]]; then
		echo "error: config not found: $CONFIG" >&2
		exit 1
	fi
}

write_plist() {
	mkdir -p "$LOG_DIR" "$(dirname "$PLIST")"
	cat >"$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${LABEL}</string>

  <key>ProgramArguments</key>
  <array>
    <string>${BINARY}</string>
    <string>-config</string>
    <string>${CONFIG}</string>
  </array>

  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>

  <key>StandardOutPath</key>
  <string>${STDOUT_LOG}</string>
  <key>StandardErrorPath</key>
  <string>${STDERR_LOG}</string>
</dict>
</plist>
EOF
}

is_loaded() {
	launchctl print "$SERVICE" >/dev/null 2>&1
}

cmd_install() {
	require_binary
	require_config
	write_plist
	launchctl enable "$SERVICE" >/dev/null 2>&1 || true
	if is_loaded; then
		launchctl bootout "$DOMAIN" "$PLIST" >/dev/null 2>&1 || true
	fi
	launchctl bootstrap "$DOMAIN" "$PLIST"
	echo "Installed and started: $LABEL"
	echo "  config: $CONFIG"
	echo "  logs:   $LOG_DIR/"
}

cmd_uninstall() {
	if is_loaded; then
		launchctl bootout "$DOMAIN" "$PLIST" >/dev/null 2>&1 || true
	fi
	launchctl disable "$SERVICE" >/dev/null 2>&1 || true
	if [[ -f "$PLIST" ]]; then
		rm -f "$PLIST"
	fi
	echo "Uninstalled: $LABEL"
}

cmd_start() {
	require_binary
	require_config
	if [[ ! -f "$PLIST" ]]; then
		write_plist
	fi
	launchctl enable "$SERVICE" >/dev/null 2>&1 || true
	if is_loaded; then
		echo "Already running: $LABEL"
		return 0
	fi
	launchctl bootstrap "$DOMAIN" "$PLIST"
	echo "Started: $LABEL"
}

cmd_stop() {
	if is_loaded; then
		launchctl bootout "$DOMAIN" "$PLIST"
	fi
	launchctl disable "$SERVICE" >/dev/null 2>&1 || true
	echo "Stopped: $LABEL (will stay off until you run start or install)"
}

cmd_restart() {
	cmd_stop
	cmd_start
}

cmd_status() {
	if [[ ! -f "$PLIST" ]]; then
		echo "Not installed (plist missing: $PLIST)"
		exit 1
	fi
	echo "Plist:  $PLIST"
	echo "Binary: $BINARY"
	echo "Config: $CONFIG"
	if is_loaded; then
		echo "State:  loaded"
		launchctl print "$SERVICE" | awk '
			/^[\t ]*state = / { print "Run:   ", $3 }
			/^[\t ]*pid = / { print "PID:   ", $3 }
		'
	else
		echo "State:  not loaded"
	fi
}

cmd_logs() {
	mkdir -p "$LOG_DIR"
	touch "$STDOUT_LOG" "$STDERR_LOG"
	tail -f "$STDOUT_LOG" "$STDERR_LOG"
}

main() {
	local cmd="${1:-}"
	case "$cmd" in
	install) cmd_install ;;
	uninstall) cmd_uninstall ;;
	start) cmd_start ;;
	stop) cmd_stop ;;
	restart) cmd_restart ;;
	status) cmd_status ;;
	logs) cmd_logs ;;
	-h | --help | help | "") usage ;;
	*)
		echo "error: unknown command: $cmd" >&2
		usage >&2
		exit 1
		;;
	esac
}

main "$@"
