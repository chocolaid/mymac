# mymac – Remote macOS Admin via Telegram

Control up to 5+ Macs from your phone. No open ports on your Macs. Survives password changes, reboots, updates.

```
[Your Phone]
    ↕ Telegram
[Bot Server – Node.js on VPS]  ←── REST poll ──  [Mac Agent – Go root daemon]
         ↑                                                   ↑
    [Vercel Config Store]  ←──────── config poll (5 min) ───┘
```

**The key insight:** agents fetch their server address from Vercel on every boot
and every 5 minutes. Change servers anytime with `/setserver` — agents
automatically switch within 5 minutes. No reinstallation needed.

---

## Repository Structure

```
mymac/
├── config-server/          ← Deploy on Vercel (permanent home)
│   └── pages/api/
│       ├── config.js           GET/POST server config
│       ├── devices/index.js    GET list / POST register device
│       ├── devices/heartbeat.js POST device heartbeat
│       └── devices/remove.js   POST remove device
├── bot/                    ← Deploy on any VPS
│   ├── index.js                Telegram bot + REST API for agents
│   ├── package.json
│   └── .env.example
├── agent/                  ← Source only (you build once, never on Mac)
│   ├── main.go
│   └── go.mod
├── commands/system.js      ← Pre-built macOS command strings
├── middleware/auth.js      ← Request auth helpers
├── build.sh                ← Build both Mac arch binaries (run on any Go machine)
├── install.sh              ← Install on Mac (no Go, no build, just curl + sudo)
├── uninstall.sh
└── .github/workflows/release.yml  ← Auto-build on git tag
```

---

## One-time Setup (do in order)

### 1. Create a Telegram Bot

1. Message **@BotFather** → `/newbot` → copy the **bot token**
2. Message **@userinfobot** → copy your **chat ID**

---

### 2. Deploy the Vercel Config Server

This is the permanent address that never changes. Agents always boot-connect here.

#### 2a. Push this repo to GitHub (make it **private**)

```bash
git init && git add . && git commit -m "init"
gh repo create mymac --private --source=. --push
```

#### 2b. Create a Vercel project

1. Go to [vercel.com](https://vercel.com) → New Project → import your GitHub repo
2. Set **Root Directory** to `config-server`
3. Add these **Environment Variables** in Vercel dashboard:

| Variable | Value |
|---|---|
| `ADMIN_TOKEN` | `openssl rand -hex 32` output |
| `KV_REST_API_URL` | *(auto-added when you create a KV store)* |
| `KV_REST_API_TOKEN` | *(auto-added when you create a KV store)* |

4. In Vercel dashboard → Storage → **Create KV Database** → link it to this project
5. Deploy → note the URL (e.g. `https://mymac-config.vercel.app`)

---

### 3. Add GitHub Secrets (for auto-building binaries)

In your GitHub repo → Settings → Secrets → Actions:

| Secret | Value |
|---|---|
| `CONFIG_SERVER_URL` | Your Vercel URL (e.g. `https://mymac-config.vercel.app`) |
| `ADMIN_TOKEN` | Same `ADMIN_TOKEN` as Vercel |

---

### 4. Build and Release the Agent Binary

**Option A – Automatic (recommended):** Push a git tag:
```bash
git tag v2.0.0 && git push --tags
```
GitHub Actions builds both binaries, creates a private GitHub Release automatically.

**Option B – Manual:** Run `build.sh` on any machine with Go 1.21+:
```bash
bash build.sh
# Follow prompts: enter Vercel URL and ADMIN_TOKEN
# Output: dist/agent-darwin-arm64  dist/agent-darwin-amd64
```
Upload both files to GitHub Releases or any private HTTPS host.

---

### 5. Deploy the Bot Server (VPS)

Any Linux VPS with Node.js 18+.

```bash
cd bot
cp .env.example .env
nano .env   # fill in all values

npm install

# Production — use PM2:
npm install -g pm2
pm2 start index.js --name mymac-bot
pm2 save && pm2 startup
```

Required `.env` values:

| Variable | Value |
|---|---|
| `TELEGRAM_TOKEN` | Bot token from BotFather |
| `ALLOWED_CHAT_ID` | Your Telegram chat ID |
| `AGENT_SECRET` | `openssl rand -hex 32` — must also be set as server config via `/setsecret` |
| `ADMIN_TOKEN` | Same as Vercel and GitHub secrets |
| `CONFIG_SERVER_URL` | Your Vercel URL |
| `PORT` | `3000` (or whatever you expose) |

> **HTTPS:** Put Nginx/Caddy in front. Mac agents connect to this URL.

After starting the bot, use `/setserver https://your-vps-url` to push the URL into Vercel config, so agents know where to connect.

---

### 6. Install the Agent on Each Mac (runs once per device)

```bash
# Copy install.sh to the Mac, then:
sudo bash install.sh
```

When prompted:
- **Binary URL** – paste the GitHub Release download URL for the correct arch
  - `agent-darwin-arm64` for Apple Silicon (M1/M2/M3/M4)
  - `agent-darwin-amd64` for Intel
- **GitHub token** – a personal access token if the repo is private (`repo` scope)
- **Checksum** – from `dist/checksums.txt` (optional but recommended)

The script installs the binary to `/usr/local/libexec/com.apple.sysmon.agent`
and registers it as a root `LaunchDaemon`. It runs immediately and on every boot.

**This works because:**
- LaunchDaemon runs at the system level, not as any user
- It does not depend on any user account, password, or session
- Even if you wipe your user profile or change your Mac password, the daemon keeps running

---

## Telegram Commands

| Command | Description |
|---|---|
| `/devices` | List all Macs with online status |
| `/run <cmd>` | Run on all Macs |
| `/run @<hostname> <cmd>` | Run on a specific Mac |
| `/sysinfo` | Hardware + OS info (all Macs) |
| `/procs` | Top processes by CPU |
| `/netstat` | Active connections |
| `/uptime` | System uptime |
| `/wifi` | Wi-Fi details |
| `/disk` | Disk usage |
| `/screenshot` | Screen capture (base64 JPG) |
| `/lock` | Lock the screen |
| `/sleep` | Sleep the Mac |
| `/status` | Pending queue per device |
| `/clear` | Clear all queues |
| `/forget @<hostname>` | Remove a device |
| `/config` | Show current server config |
| `/setserver <url>` | Update bot server URL (agents switch in ~5 min) |
| `/setsecret <secret>` | Update agent secret |

---

## Changing Your VPS Server (Future)

1. Deploy bot on new VPS
2. Message your bot: `/setserver https://new-vps-url`
3. All agents automatically reconnect within 5 minutes — no reinstallation

---

## Agent Management on Mac

```bash
# Real-time logs
tail -f /var/log/com.apple.sysmon.agent.log

# Status
sudo launchctl list com.apple.sysmon.agent

# Stop
sudo launchctl unload /Library/LaunchDaemons/com.apple.sysmon.agent.plist

# Start
sudo launchctl load -w /Library/LaunchDaemons/com.apple.sysmon.agent.plist

# Full uninstall
sudo bash uninstall.sh
```

---

## Security Notes

| Concern | Mitigation |
|---|---|
| Who can send commands | Only your `ALLOWED_CHAT_ID`, checked on every message |
| Agent ↔ bot auth | `X-Agent-Secret` header, constant-time comparison |
| Agent ↔ Vercel auth | `X-Admin-Token` header |
| Traffic encryption | All connections over HTTPS |
| Binary discovery | Named as a system process; no open ports on Mac |
| Secret leakage | All secrets are baked into binary (not files), or in Vercel env vars |
| Password change resilience | LaunchDaemon is root-level, independent of all user accounts |
