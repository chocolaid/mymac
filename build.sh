#!/usr/bin/env bash
# build.sh – cross-compile the Mac agent for both Apple Silicon and Intel
# Run this on any machine with Go 1.21+ installed (Linux/Mac/Windows WSL)
# Output: dist/agent-darwin-arm64   dist/agent-darwin-amd64
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; RESET='\033[0m'
info()    { echo -e "${CYAN}[•]${RESET} $*"; }
success() { echo -e "${GREEN}[✓]${RESET} $*"; }
die()     { echo -e "${RED}[✗]${RESET} $*" >&2; exit 1; }

# ── Check Go ──────────────────────────────────────────────────────────────────
command -v go &>/dev/null || die "Go is not installed. Install from https://go.dev/dl/"
GO_VERSION=$(go version)
info "Using $GO_VERSION"

# ── Collect build parameters ──────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
AGENT_DIR="$SCRIPT_DIR/agent"
DIST_DIR="$SCRIPT_DIR/dist"

echo ""
echo "These values will be baked permanently into the binary."
echo "They cannot be changed after compilation (except adminToken which is in Vercel)."
echo ""

# Config server URL (permanent Vercel URL)
read -rp "  Vercel config server URL (e.g. https://mymac-config.vercel.app): " CONFIG_URL
[[ -n "$CONFIG_URL" ]] || die "Config server URL required."
CONFIG_URL="${CONFIG_URL%/}" # strip trailing slash

# Admin token (for Vercel config server)
read -rsp "  Admin token (x-admin-token for Vercel, same as ADMIN_TOKEN): " ADMIN_TOKEN_VAL
echo ""
[[ -n "$ADMIN_TOKEN_VAL" ]] || die "Admin token required."

# Agent version
read -rp "  Agent version [2.0.0]: " VERSION
VERSION="${VERSION:-2.0.0}"

echo ""

# ── Fetch Go dependencies ─────────────────────────────────────────────────────
info "Fetching Go dependencies..."
cd "$AGENT_DIR"
go mod tidy

# ── Build flags ───────────────────────────────────────────────────────────────
LDFLAGS="-s -w \
  -X main.configServerURL=${CONFIG_URL} \
  -X main.adminToken=${ADMIN_TOKEN_VAL} \
  -X main.agentVersion=${VERSION}"

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
success "Checksums written to dist/checksums.txt"

echo ""
echo -e "${GREEN}Build complete.${RESET}"
echo ""
echo "Next steps:"
echo "  1. Upload dist/agent-darwin-arm64 and dist/agent-darwin-amd64"
echo "     to a private GitHub Release (or any HTTPS URL you control)"
echo ""
echo "  2. On each Mac, run the installer:"
echo "     curl -fsSL https://raw.githubusercontent.com/YOUR/REPO/main/install.sh | sudo bash"
echo "     (or copy install.sh to the Mac and run: sudo bash install.sh)"
echo ""
cat "$DIST_DIR/checksums.txt"
