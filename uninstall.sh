#!/usr/bin/env bash
# uninstall.sh – fully removes the mymac agent from macOS
# Run with: sudo bash uninstall.sh
set -euo pipefail

[[ $EUID -eq 0 ]]            || { echo "Run as root: sudo bash uninstall.sh"; exit 1; }
[[ "$(uname)" == "Darwin" ]] || { echo "macOS only."; exit 1; }

AGENT_LABEL="com.apple.sysmon.agent"
PLIST_PATH="/Library/LaunchDaemons/${AGENT_LABEL}.plist"
BINARY_PATH="/usr/local/libexec/${AGENT_LABEL}"
LOG_PATH="/var/log/${AGENT_LABEL}.log"
ERR_PATH="/var/log/${AGENT_LABEL}.err"

echo "[•] Stopping daemon..."
launchctl unload "$PLIST_PATH" 2>/dev/null || true

echo "[•] Removing files..."
rm -f "$PLIST_PATH" "$BINARY_PATH" "$LOG_PATH" "$ERR_PATH"

echo "[✓] mymac agent completely removed."
