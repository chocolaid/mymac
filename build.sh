#!/usr/bin/env bash
# build.sh – build, upload to GitHub, and auto-publish release to Vercel
# Agents will self-update within ~1 hour automatically. No Telegram step needed.
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; RESET='\033[0m'
info()    { echo -e "${CYAN}[•]${RESET} $*"; }
success() { echo -e "${GREEN}[✓]${RESET} $*"; }
warn()    { echo -e "${YELLOW}[!]${RESET} $*"; }
die()     { echo -e "${RED}[✗]${RESET} $*" >&2; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
AGENT_DIR="$SCRIPT_DIR/agent"
DIST_DIR="$SCRIPT_DIR/dist"
CFG_FILE="$SCRIPT_DIR/.build-config"  # saved settings — never commit this

# ── Check Go ──────────────────────────────────────────────────────────────────
command -v go &>/dev/null || die "Go is not installed. Install from https://go.dev/dl/"
info "Using $(go version)"

# ── Load saved settings ───────────────────────────────────────────────────────
CONFIG_URL=""
ADMIN_TOKEN_VAL=""
GITHUB_REPO=""

if [[ -f "$CFG_FILE" ]]; then
  # shellcheck source=/dev/null
  source "$CFG_FILE"
  info "Loaded saved settings from .build-config"
fi

# ── Prompt for any missing/updated values ─────────────────────────────────────
echo ""
[[ -n "$CONFIG_URL" ]] && echo "  Vercel config server URL [${CONFIG_URL}]: " || echo "  Vercel config server URL (e.g. https://mymac-config.vercel.app): "
read -rp "  > " _input
[[ -n "$_input" ]] && CONFIG_URL="${_input%/}"
[[ -n "$CONFIG_URL" ]] || die "Config server URL required."

if [[ -z "$ADMIN_TOKEN_VAL" ]]; then
  read -rsp "  Admin token (x-admin-token, same as ADMIN_TOKEN env var): " ADMIN_TOKEN_VAL; echo ""
  [[ -n "$ADMIN_TOKEN_VAL" ]] || die "Admin token required."
else
  info "Using saved admin token."
fi

[[ -n "$GITHUB_REPO" ]] && echo "  GitHub repo [${GITHUB_REPO}] (owner/repo, blank to skip upload): " || echo "  GitHub repo (owner/repo, e.g. yourname/mymac — blank to skip upload): "
read -rp "  > " _input
[[ -n "$_input" ]] && GITHUB_REPO="$_input"

read -rp "  Agent version [2.0.0]: " VERSION
VERSION="${VERSION:-2.0.0}"

echo ""

# ── Save settings for next time ───────────────────────────────────────────────
cat > "$CFG_FILE" <<EOF
CONFIG_URL="${CONFIG_URL}"
ADMIN_TOKEN_VAL="${ADMIN_TOKEN_VAL}"
GITHUB_REPO="${GITHUB_REPO}"
EOF
chmod 600 "$CFG_FILE"
info "Settings saved to .build-config (chmod 600)"

# ── Fetch Go dependencies ─────────────────────────────────────────────────────
info "Fetching Go dependencies..."
cd "$AGENT_DIR"
go mod tidy

# ── Build flags ───────────────────────────────────────────────────────────────
LDFLAGS="-s -w \
  -X main.configServerURL=${CONFIG_URL} \
  -X main.adminToken=${ADMIN_TOKEN_VAL} \
  -X main.agentVersion=${VERSION} \
  -X main.githubRepo=${GITHUB_REPO}"

mkdir -p "$DIST_DIR"

# ── Build: Apple Silicon (arm64) ──────────────────────────────────────────────
info "Building for Darwin/arm64 (Apple Silicon)..."
GOOS=darwin GOARCH=arm64 go build \
  -ldflags="$LDFLAGS" \
  -trimpath \
  -o "$DIST_DIR/agent-darwin-arm64" \
  .
success "Built: dist/agent-darwin-arm64  ($(du -sh "$DIST_DIR/agent-darwin-arm64" | cut -f1))"

# ── Build: Intel (amd64) ──────────────────────────────────────────────────────
info "Building for Darwin/amd64 (Intel)..."
GOOS=darwin GOARCH=amd64 go build \
  -ldflags="$LDFLAGS" \
  -trimpath \
  -o "$DIST_DIR/agent-darwin-amd64" \
  .
success "Built: dist/agent-darwin-amd64  ($(du -sh "$DIST_DIR/agent-darwin-amd64" | cut -f1))"

# ── Checksums ─────────────────────────────────────────────────────────────────
cd "$DIST_DIR"
sha256sum agent-darwin-arm64 agent-darwin-amd64 > checksums.txt 2>/dev/null || \
  shasum -a 256 agent-darwin-arm64 agent-darwin-amd64 > checksums.txt
SHA_ARM64=$(awk '/agent-darwin-arm64/{print $1}' checksums.txt)
SHA_AMD64=$(awk '/agent-darwin-amd64/{print $1}' checksums.txt)
success "Checksums: arm64=${SHA_ARM64:0:16}…  amd64=${SHA_AMD64:0:16}…"

# ── Upload to GitHub Release ──────────────────────────────────────────────────
ARM64_URL=""
AMD64_URL=""

if [[ -n "$GITHUB_REPO" ]]; then
  command -v gh &>/dev/null || die "'gh' CLI not found. Install from https://cli.github.com/ or set GITHUB_REPO to blank to skip."
  gh auth status &>/dev/null || die "Not logged in to gh. Run: gh auth login"

  TAG="v${VERSION}"
  info "Creating GitHub release ${TAG} on ${GITHUB_REPO}..."

  # Delete existing release/tag if it exists (allows re-publishing same version)
  gh release delete "$TAG" --repo "$GITHUB_REPO" --yes 2>/dev/null && warn "Deleted existing release ${TAG}" || true
  git -C "$SCRIPT_DIR" tag -d "$TAG" 2>/dev/null || true

  gh release create "$TAG" \
    --repo "$GITHUB_REPO" \
    --title "mymac-agent ${TAG}" \
    --notes "Auto-published by build.sh on $(date -u '+%Y-%m-%d %H:%M UTC')" \
    "$DIST_DIR/agent-darwin-arm64" \
    "$DIST_DIR/agent-darwin-amd64"

  ARM64_URL="https://github.com/${GITHUB_REPO}/releases/download/${TAG}/agent-darwin-arm64"
  AMD64_URL="https://github.com/${GITHUB_REPO}/releases/download/${TAG}/agent-darwin-amd64"
  success "Uploaded to GitHub: ${TAG}"
else
  warn "No GitHub repo set — skipping upload."
  echo ""
  echo "Paste the download URLs for the binaries (or leave blank to skip Vercel publish):"
  read -rp "  arm64 URL: " ARM64_URL
  read -rp "  amd64 URL: " AMD64_URL
fi

# ── Publish release to Vercel config server ───────────────────────────────────
if [[ -n "$ARM64_URL" ]]; then
  info "Publishing release ${VERSION} to Vercel config server..."

  HTTP_STATUS=$(curl -s -o /tmp/_release_resp.json -w "%{http_code}" \
    -X POST "${CONFIG_URL}/api/release" \
    -H "x-admin-token: ${ADMIN_TOKEN_VAL}" \
    -H "Content-Type: application/json" \
    -d "{
      \"version\":     \"${VERSION}\",
      \"arm64Url\":    \"${ARM64_URL}\",
      \"amd64Url\":    \"${AMD64_URL}\",
      \"arm64Sha256\": \"${SHA_ARM64}\",
      \"amd64Sha256\": \"${SHA_AMD64}\"
    }")

  if [[ "$HTTP_STATUS" == "200" ]]; then
    success "Release ${VERSION} published to Vercel — agents will self-update within ~1 hour."
  else
    warn "Vercel publish returned HTTP ${HTTP_STATUS}:"
    cat /tmp/_release_resp.json
    echo ""
    die "Fix the error above and re-run, or manually call POST /api/release."
  fi
else
  warn "No binary URLs — skipping Vercel publish. Agents will NOT auto-update."
fi

echo ""
echo -e "${GREEN}Done.${RESET}"
echo ""
echo "Agents are already polling for updates — they'll apply ${VERSION} within ~1 hour."
echo "No Telegram command needed."
