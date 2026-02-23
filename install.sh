#!/usr/bin/env bash
# install.sh – mymac agent installer
# Downloads and installs the pre-built agent binary as a root LaunchDaemon.
# NO compilation required on the Mac. No dependencies beyond curl (built-in).
#
# Usage: sudo bash install.sh
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; RESET='\033[0m'

info()    { echo -e "${CYAN}[•]${RESET} $*"; }
success() { echo -e "${GREEN}[✓]${RESET} $*"; }
warn()    { echo -e "${YELLOW}[!]${RESET} $*"; }
die()     { echo -e "${RED}[✗]${RESET} $*" >&2; exit 1; }

# ── Guards ────────────────────────────────────────────────────────────────────
[[ $EUID -eq 0 ]]          || die "Run as root: sudo bash install.sh"
[[ "$(uname)" == "Darwin" ]] || die "This installer is for macOS only."

command -v curl &>/dev/null || die "curl is required but not found."

ARCH="$(uname -m)"   # arm64 | x86_64
[[ "$ARCH" == "arm64" || "$ARCH" == "x86_64" ]] || die "Unsupported arch: $ARCH"

# ── Install paths ─────────────────────────────────────────────────────────────
AGENT_LABEL="com.apple.sysmon.agent"
BINARY_PATH="/usr/local/libexec/${AGENT_LABEL}"
PLIST_PATH="/Library/LaunchDaemons/${AGENT_LABEL}.plist"
LOG_PATH="/var/log/${AGENT_LABEL}.log"
ERR_PATH="/var/log/${AGENT_LABEL}.err"

echo -e "\n${BOLD}╔══════════════════════════════════════════╗"
echo -e "║       mymac agent – installer v2         ║"
echo -e "╚══════════════════════════════════════════╝${RESET}\n"
echo -e "  Architecture detected: ${CYAN}${ARCH}${RESET}\n"

# ── Ask where to download the binary from ─────────────────────────────────────
echo -e "${BOLD}Binary download${RESET}"
echo -e "The pre-built binary must be hosted at an HTTPS URL."
echo -e "Build it with build.sh and upload to GitHub Releases or any HTTPS host.\n"

read -rp "  Binary download URL (arm64 or amd64 URL for this Mac): " BINARY_URL
[[ -n "$BINARY_URL" ]] || die "Binary URL required."

# Optional: GitHub personal access token for private releases
read -rp "  GitHub token (for private repo releases, or press Enter to skip): " GH_TOKEN

echo ""

# ── Download binary ───────────────────────────────────────────────────────────
info "Downloading agent binary..."

TMPBIN="$(mktemp)"
CURL_OPTS=(-fsSL --retry 3 --retry-delay 2 -o "$TMPBIN")

if [[ -n "$GH_TOKEN" ]]; then
  # GitHub releases with auth
  CURL_OPTS+=(-H "Authorization: token ${GH_TOKEN}")
fi

curl "${CURL_OPTS[@]}" "$BINARY_URL" || die "Download failed. Check the URL and token."

# Basic sanity check: macOS Mach-O binary
file "$TMPBIN" | grep -qi "mach-o" || die "Downloaded file does not appear to be a macOS binary. Check the URL."

success "Downloaded ($(du -sh "$TMPBIN" | cut -f1))"

# ── Checksum verification (optional) ─────────────────────────────────────────
read -rp "  SHA-256 checksum to verify (from dist/checksums.txt, or press Enter to skip): " EXPECTED_SHA

if [[ -n "$EXPECTED_SHA" ]]; then
  info "Verifying checksum..."
  ACTUAL_SHA="$(shasum -a 256 "$TMPBIN" | awk '{print $1}')"
  if [[ "$ACTUAL_SHA" != "$EXPECTED_SHA" ]]; then
    rm -f "$TMPBIN"
    die "Checksum mismatch!\n  Expected: $EXPECTED_SHA\n  Got:      $ACTUAL_SHA"
  fi
  success "Checksum verified."
fi

# ── Install binary ────────────────────────────────────────────────────────────
info "Installing binary to ${BINARY_PATH}..."
mkdir -p "$(dirname "$BINARY_PATH")"
install -m 0755 -o root -g wheel "$TMPBIN" "$BINARY_PATH"
rm -f "$TMPBIN"

# Strip quarantine so Gatekeeper won't block it
xattr -c "$BINARY_PATH" 2>/dev/null || true

success "Binary installed."

# ── Write LaunchDaemon plist ──────────────────────────────────────────────────
info "Installing LaunchDaemon..."

cat > "$PLIST_PATH" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${AGENT_LABEL}</string>

  <key>ProgramArguments</key>
  <array>
    <string>${BINARY_PATH}</string>
  </array>

  <!-- Run as root — survives all user/password changes -->
  <key>UserName</key>
  <string>root</string>

  <!-- Start at boot, restart automatically if it crashes -->
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>

  <!-- Wait 10 s before restarting after a crash -->
  <key>ThrottleInterval</key>
  <integer>10</integer>

  <key>StandardOutPath</key>
  <string>${LOG_PATH}</string>
  <key>StandardErrorPath</key>
  <string>${ERR_PATH}</string>

  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/opt/homebrew/bin</string>
    <key>HOME</key>
    <string>/var/root</string>
  </dict>

  <key>Nice</key>
  <integer>0</integer>
</dict>
</plist>
PLIST

chmod 644 "$PLIST_PATH"
chown root:wheel "$PLIST_PATH"
success "LaunchDaemon plist installed."

# ── Set up log files ──────────────────────────────────────────────────────────
touch "$LOG_PATH" "$ERR_PATH"
chmod 640 "$LOG_PATH" "$ERR_PATH"
chown root:wheel "$LOG_PATH" "$ERR_PATH"

# ── Unload existing service if upgrading ──────────────────────────────────────
if launchctl list "$AGENT_LABEL" &>/dev/null 2>&1; then
  info "Stopping existing agent (upgrade)..."
  launchctl unload "$PLIST_PATH" 2>/dev/null || true
  sleep 1
fi

# ── Load and start the daemon ─────────────────────────────────────────────────
info "Loading LaunchDaemon (starts now and on every boot)..."
launchctl load -w "$PLIST_PATH"
sleep 3

if launchctl list "$AGENT_LABEL" &>/dev/null 2>&1; then
  success "Agent is running as a root system daemon."
else
  warn "Daemon registered but may still be starting. Check:"
  warn "  sudo launchctl list ${AGENT_LABEL}"
fi

# ── Summary ───────────────────────────────────────────────────────────────────
echo ""
echo -e "${BOLD}╔══════════════════════════════════════════╗"
echo -e "║         Installation complete ✓          ║"
echo -e "╚══════════════════════════════════════════╝${RESET}"
echo ""
echo -e "  Binary:       ${CYAN}${BINARY_PATH}${RESET}"
echo -e "  Daemon plist: ${CYAN}${PLIST_PATH}${RESET}"
echo -e "  Log:          ${CYAN}${LOG_PATH}${RESET}"
echo ""
echo -e "  ${BOLD}The agent:${RESET}"
echo -e "  • Runs as root — no password ever needed"
echo -e "  • Starts automatically on every boot"
echo -e "  • Restarts automatically if it crashes"
echo -e "  • Persists through macOS updates, user changes, and password resets"
echo ""
echo -e "  ${BOLD}Useful commands:${RESET}"
echo -e "    Logs:     tail -f ${LOG_PATH}"
echo -e "    Status:   sudo launchctl list ${AGENT_LABEL}"
echo -e "    Stop:     sudo launchctl unload ${PLIST_PATH}"
echo -e "    Start:    sudo launchctl load -w ${PLIST_PATH}"
echo -e "    Uninstall: sudo bash uninstall.sh"
echo ""
echo -e "  ${BOLD}Next:${RESET} Open Telegram and check your bot — this Mac will appear in /devices."
echo ""
