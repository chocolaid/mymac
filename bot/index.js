require('dotenv').config();
const TelegramBot = require('node-telegram-bot-api');
const express = require('express');
const helmet = require('helmet');
const rateLimit = require('express-rate-limit');
const { v4: uuidv4 } = require('uuid');

// ─── Config ───────────────────────────────────────────────────────────────────
const {
  TELEGRAM_TOKEN,    // From @BotFather
  ALLOWED_CHAT_ID,   // Your personal Telegram chat ID
  AGENT_SECRET,      // Shared secret with all Mac agents
  ADMIN_TOKEN,       // Shared secret with Vercel config server
  CONFIG_SERVER_URL, // e.g. https://mymac-config.vercel.app
  PORT = 3000,
} = process.env;

['TELEGRAM_TOKEN', 'ALLOWED_CHAT_ID', 'AGENT_SECRET', 'ADMIN_TOKEN', 'CONFIG_SERVER_URL']
  .forEach((name) => {
    if (!process.env[name]) { console.error(`Missing env var: ${name}`); process.exit(1); }
  });

// ─── State ────────────────────────────────────────────────────────────────────
// Per-device command queues:  deviceId → [{ id, cmd, chatId, requestedAt }]
const deviceQueues = {};
// Result store:  id → { output, exitCode, finishedAt }
const resultStore  = {};
// Known devices cache (refreshed from Vercel every 30 s or on demand)
let devicesCache   = [];
let devicesCacheAt = 0;

// ─── Helpers ──────────────────────────────────────────────────────────────────
function isAuthorized(msg) {
  return String(msg.chat.id) === String(ALLOWED_CHAT_ID);
}

function reply(chatId, text, extra = {}) {
  return bot.sendMessage(chatId, text, { parse_mode: 'Markdown', ...extra });
}

// ─── Vercel config-server helpers ─────────────────────────────────────────────
async function configReq(method, path, body) {
  const fetch = (...args) => import('node-fetch').then(({ default: f }) => f(...args));
  const res = await (await fetch)(`${CONFIG_SERVER_URL}${path}`, {
    method,
    headers: { 'x-admin-token': ADMIN_TOKEN, 'Content-Type': 'application/json' },
    ...(body ? { body: JSON.stringify(body) } : {}),
  });
  return res.json();
}

async function getDevices(force = false) {
  if (!force && Date.now() - devicesCacheAt < 30_000 && devicesCache.length > 0) {
    return devicesCache;
  }
  const data = await configReq('GET', '/api/devices');
  devicesCache = data.devices ?? [];
  devicesCacheAt = Date.now();
  return devicesCache;
}

async function resolveDevice(query) {
  const devices = await getDevices(true);
  const q = query.toLowerCase();
  return devices.find(
    (d) => d.deviceId.toLowerCase().startsWith(q) || d.hostname.toLowerCase().startsWith(q)
  );
}

function onlineStatus(device) {
  const age = Date.now() - new Date(device.lastSeen).getTime();
  return age < 90_000 ? '🟢' : age < 300_000 ? '🟡' : '🔴';
}

// ─── Telegram Bot ─────────────────────────────────────────────────────────────
const bot = new TelegramBot(TELEGRAM_TOKEN, { polling: true });

// /start
bot.onText(/^\/start$/, (msg) => {
  if (!isAuthorized(msg)) return;
  reply(msg.chat.id,
    `*mymac admin*\n\nControl all your Macs remotely.\n\n` +
    `• \`/devices\` – list all Macs\n` +
    `• \`/run <cmd>\` – broadcast to all Macs\n` +
    `• \`/run @<hostname> <cmd>\` – specific Mac\n` +
    `• \`/config\` – view server config\n` +
    `• \`/help\` – all commands`
  );
});

// /help
bot.onText(/^\/help$/, (msg) => {
  if (!isAuthorized(msg)) return;
  reply(msg.chat.id,
    `*Commands*\n\n` +
    `*Run commands:*\n` +
    `\`/run <cmd>\` – broadcast to all Macs\n` +
    `\`/run @<hostname> <cmd>\` – specific Mac\n\n` +
    `*Built-in shortcuts (broadcast to all):*\n` +
    `\`/sysinfo\` \`/procs\` \`/netstat\` \`/uptime\`\n` +
    `\`/wifi\` \`/disk\` \`/screenshot\` \`/lock\` \`/sleep\`\n\n` +
    `*Devices:*\n` +
    `\`/devices\` – list all Macs with status\n` +
    `\`/forget @<hostname>\` – remove a device\n\n` +
    `*Config (change VPS server):*\n` +
    `\`/config\` – show current config\n` +
    `\`/setserver <url>\` – update bot server URL\n` +
    `\`/setsecret <secret>\` – update agent secret\n\n` +
    `*Releases (agent self-update):*\n` +
    `\`/release\` – show published release\n` +
    `\`/setrelease <ver> <arm64-url> [amd64-url]\` – publish new release\n\n` +
    `*Queue:*\n` +
    `\`/status\` – pending command counts\n` +
    `\`/clear\` – clear all queues`
  );
});

// /devices
bot.onText(/^\/devices$/, async (msg) => {
  if (!isAuthorized(msg)) return;
  const chatId = msg.chat.id;
  try {
    const devices = await getDevices(true);
    if (!devices.length) return reply(chatId, 'No devices registered yet.');
    const lines = devices.map((d) => {
      const q = (deviceQueues[d.deviceId] ?? []).length;
      const last = new Date(d.lastSeen).toLocaleString();
      return (
        `${onlineStatus(d)} *${d.hostname}* (${d.arch})\n` +
        `  ID: \`${d.deviceId.slice(0, 8)}\`  Queue: ${q}  Last seen: ${last}`
      );
    });
    reply(chatId, `*Registered Macs (${devices.length}):*\n\n${lines.join('\n\n')}`);
  } catch (e) {
    reply(chatId, `Error: ${e.message}`);
  }
});

// /forget @hostname
bot.onText(/^\/forget @?(\S+)$/, async (msg, match) => {
  if (!isAuthorized(msg)) return;
  const device = await resolveDevice(match[1].trim());
  if (!device) return reply(msg.chat.id, `Device \`${match[1]}\` not found.`);
  await configReq('POST', '/api/devices/remove', { deviceId: device.deviceId });
  delete deviceQueues[device.deviceId];
  await getDevices(true);
  reply(msg.chat.id, `✅ Removed: *${device.hostname}*`);
});

// /config
bot.onText(/^\/config$/, async (msg) => {
  if (!isAuthorized(msg)) return;
  try {
    const cfg = await configReq('GET', '/api/config');
    reply(msg.chat.id,
      `*Server config* (v${cfg.version ?? '?'})\n` +
      `URL: \`${cfg.serverUrl || '(not set)'}\`\n` +
      `Secret: \`${cfg.agentSecret ? cfg.agentSecret.slice(0, 8) + '…' : '(not set)'}\`\n` +
      `Updated: ${cfg.updatedAt ?? 'never'}\n\n` +
      `Use /setserver and /setsecret to change.`
    );
  } catch (e) {
    reply(msg.chat.id, `Error: ${e.message}`);
  }
});

// /setserver <url>
bot.onText(/^\/setserver (.+)$/, async (msg, match) => {
  if (!isAuthorized(msg)) return;
  const url = match[1].trim().replace(/\/$/, '');
  try {
    await configReq('POST', '/api/config', { serverUrl: url });
    reply(msg.chat.id,
      `✅ Server URL updated:\n\`${url}\`\n\n` +
      `All agents will reconnect within ~5 min on their next config poll.`
    );
  } catch (e) {
    reply(msg.chat.id, `Error: ${e.message}`);
  }
});

// /setrelease <version> <arm64-url> [amd64-url]
bot.onText(/^\/setrelease (.+)$/, async (msg, match) => {
  if (!isAuthorized(msg)) return;
  const parts = match[1].trim().split(/\s+/);
  const [version, arm64Url, amd64Url] = parts;
  if (!version || !arm64Url) {
    return reply(msg.chat.id, 'Usage: `/setrelease <version> <arm64-url> [amd64-url]`');
  }
  try {
    await configReq('POST', '/api/release', {
      version,
      arm64Url,
      ...(amd64Url ? { amd64Url } : {}),
    });
    reply(msg.chat.id,
      `✅ Release published: \`${version}\`\n` +
      `arm64: \`${arm64Url}\`\n\n` +
      `Agents will update within ~1 hour on their next check.`
    );
  } catch (e) {
    reply(msg.chat.id, `Error: ${e.message}`);
  }
});

// /release – show current published release
bot.onText(/^\/release$/, async (msg) => {
  if (!isAuthorized(msg)) return;
  try {
    const rel = await configReq('GET', '/api/release');
    if (!rel.version) return reply(msg.chat.id, 'No release published yet. Use /setrelease.');
    reply(msg.chat.id,
      `*Current release:* \`${rel.version}\`\n` +
      `arm64: \`${rel.arm64Url || '(not set)'}\`\n` +
      `amd64: \`${rel.amd64Url || '(not set)'}\`\n` +
      `Published: ${rel.publishedAt ?? 'unknown'}`
    );
  } catch (e) {
    reply(msg.chat.id, `Error: ${e.message}`);
  }
});

// /setsecret <secret>
bot.onText(/^\/setsecret (.+)$/, async (msg, match) => {
  if (!isAuthorized(msg)) return;
  const secret = match[1].trim();
  try {
    await configReq('POST', '/api/config', { agentSecret: secret });
    process.env.AGENT_SECRET = secret; // update in-memory immediately
    reply(msg.chat.id,
      `✅ Agent secret updated in Firebase and applied immediately.\n\n` +
      `Agents will pick up the new secret within ~5 min on their next config poll.`
    );
  } catch (e) {
    reply(msg.chat.id, `Error: ${e.message}`);
  }
});

// /status
bot.onText(/^\/status$/, async (msg) => {
  if (!isAuthorized(msg)) return;
  const devices = await getDevices();
  const lines = devices.map(
    (d) => `${onlineStatus(d)} *${d.hostname}*: ${(deviceQueues[d.deviceId] ?? []).length} pending`
  );
  reply(msg.chat.id,
    `*Queue status*\n${lines.join('\n') || 'No devices'}\n\nResults buffered: ${Object.keys(resultStore).length}`
  );
});

// /clear
bot.onText(/^\/clear$/, (msg) => {
  if (!isAuthorized(msg)) return;
  Object.keys(deviceQueues).forEach((k) => { deviceQueues[k] = []; });
  reply(msg.chat.id, '✅ All queues cleared.');
});

// ── Built-in shortcut commands (broadcast to all) ─────────────────────────────
const SHORTCUTS = {
  '/sysinfo':    { cmd: 'system_profiler SPHardwareDataType SPSoftwareDataType 2>/dev/null' },
  '/procs':      { cmd: 'ps aux | sort -rk 3 | head -20' },
  '/netstat':    { cmd: 'lsof -i -n -P | grep ESTABLISHED' },
  '/uptime':     { cmd: 'uptime && sw_vers' },
  '/wifi':       { cmd: '/System/Library/PrivateFrameworks/Apple80211.framework/Versions/Current/Resources/airport -I' },
  '/disk':       { cmd: 'df -h' },
  '/screenshot': { cmd: 'screencapture -t jpg /tmp/_sc.jpg && base64 /tmp/_sc.jpg && rm -f /tmp/_sc.jpg', type: 'screenshot' },
  '/lock':       { cmd: "osascript -e 'tell application \"System Events\" to keystroke \"q\" using {command down, control down}'" },
  '/sleep':      { cmd: 'pmset sleepnow' },
};

Object.entries(SHORTCUTS).forEach(([trigger, { cmd, type }]) => {
  bot.onText(new RegExp(`^\\${trigger}$`), async (msg) => {
    if (!isAuthorized(msg)) return;
    await broadcast(msg.chat.id, cmd, trigger, type);
  });
});

// /run [@hostname] <cmd>
bot.onText(/^\/run(?: @([\w.-]+))? ([\s\S]+)$/, async (msg, match) => {
  if (!isAuthorized(msg)) return;
  const target = match[1];
  const cmd    = match[2].trim();
  const chatId = msg.chat.id;

  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, cmd);
  }
});

// ─── Core: enqueue + broadcast ────────────────────────────────────────────────
async function broadcast(chatId, cmd, label, type) {
  const devices = await getDevices();
  if (!devices.length) return reply(chatId, 'No devices registered yet.');
  reply(chatId, `📡 Broadcasting to *${devices.length}* Mac(s):\n\`\`\`\n${label}\n\`\`\``);
  for (const d of devices) enqueue(chatId, d.deviceId, d.hostname, cmd, type);
}

function enqueue(chatId, deviceId, hostname, cmd, type) {
  const id = uuidv4();
  if (!deviceQueues[deviceId]) deviceQueues[deviceId] = [];
  deviceQueues[deviceId].push({ id, cmd, chatId, requestedAt: Date.now() });
  waitForResult(id, chatId, hostname, cmd, type);
}

function waitForResult(id, chatId, hostname, cmd, type, deadline = Date.now() + 90_000) {
  const tick = () => {
    if (resultStore[id]) {
      const { output, exitCode } = resultStore[id];
      delete resultStore[id];

      if (type === 'screenshot' && exitCode === 0 && output.trim().length > 0) {
        const buf = Buffer.from(output.trim(), 'base64');
        bot.sendPhoto(chatId, buf, { caption: `📸 *${hostname}*`, parse_mode: 'Markdown' })
          .catch((err) => {
            console.error(`[bot] sendPhoto failed for ${hostname}:`, err.message);
            reply(chatId, `📋 *${hostname}* — screenshot failed: ${err.message}`);
          });
        return;
      }

      const text = output.length > 3500
        ? output.slice(0, 3500) + '\n…(truncated)'
        : output || '(no output)';
      reply(chatId, `📋 *${hostname}* \\(exit ${exitCode}\\)\n\`\`\`\n${text}\n\`\`\``);
      return;
    }
    if (Date.now() > deadline) {
      reply(chatId, `⏰ *${hostname}* — no response for: \`${cmd.slice(0, 60)}\``);
      return;
    }
    setTimeout(tick, 1500);
  };
  setTimeout(tick, 1500);
}

// ─── Config sync (keeps AGENT_SECRET in sync with Firebase) ──────────────────
// After the Firebase migration the .env value and the Firebase value can drift.
// We fix this by fetching the canonical secret from the config server on startup
// and then refreshing it every 5 minutes so /setsecret changes propagate here
// automatically without needing a bot restart.
let _configSyncVersion = -1;

async function syncConfigFromServer() {
  try {
    const cfg = await configReq('GET', '/api/config');
    if (cfg.agentSecret && cfg.agentSecret !== process.env.AGENT_SECRET) {
      process.env.AGENT_SECRET = cfg.agentSecret;
      console.log(`[bot] AGENT_SECRET synced from config server (config v${cfg.version ?? '?'}).`);
    }
    _configSyncVersion = cfg.version ?? _configSyncVersion;
  } catch (e) {
    console.error('[bot] Failed to sync config from server:', e.message);
  }
}

// ─── Express REST API (Mac agents connect here) ───────────────────────────────
const app = express();
app.set('trust proxy', 1); // required when behind localtunnel / reverse proxy
app.use(helmet());
app.use(express.json({ limit: '20mb' }));
app.use('/api', rateLimit({ windowMs: 10_000, max: 60 }));

// Agent auth — reads process.env.AGENT_SECRET which is kept fresh by syncConfigFromServer()
app.use('/api', (req, res, next) => {
  if (req.headers['x-agent-secret'] !== process.env.AGENT_SECRET) {
    return res.status(401).json({ error: 'unauthorized' });
  }
  next();
});

// GET /api/command?device=<deviceId>
app.get('/api/command', (req, res) => {
  const { device } = req.query;
  if (!device) return res.status(400).json({ error: 'device query param required' });
  const queue = deviceQueues[device] ?? [];
  if (!queue.length) return res.status(204).end();
  res.json(queue.shift());
});

// POST /api/result  { id, deviceId, hostname, output, exitCode }
app.post('/api/result', (req, res) => {
  const { id, output, exitCode } = req.body;
  if (!id) return res.status(400).json({ error: 'id required' });
  resultStore[id] = { output: String(output ?? ''), exitCode: Number(exitCode ?? -1), finishedAt: Date.now() };
  res.json({ ok: true });
});

// POST /api/alert  { deviceId, hostname, message }
app.post('/api/alert', (req, res) => {
  const { hostname, message } = req.body ?? {};
  if (!message) return res.status(400).json({ error: 'message required' });
  bot.sendMessage(ALLOWED_CHAT_ID, `🔔 *Alert from ${hostname ?? 'Mac'}*\n${message}`, { parse_mode: 'Markdown' });
  res.json({ ok: true });
});

// GET /health
app.get('/health', (_req, res) => res.json({ ok: true, uptime: process.uptime() }));

// ─── Startup ──────────────────────────────────────────────────────────────────
(async () => {
  // Pull the canonical agentSecret from Firebase before accepting connections.
  // This fixes the post-Firebase-migration gap where the .env value lags behind.
  console.log('[bot] Syncing config from server before startup...');
  await syncConfigFromServer();

  if (!process.env.AGENT_SECRET) {
    console.error('[bot] AGENT_SECRET is still empty after sync — agents will not be able to authenticate. Set it with /setsecret.');
  }

  // Keep re-syncing every 5 min so /setsecret changes propagate without a restart.
  setInterval(syncConfigFromServer, 5 * 60 * 1000);

  app.listen(PORT, () => {
    console.log(`[bot] Listening on :${PORT}`);
    console.log(`[bot] Authorized chat: ${ALLOWED_CHAT_ID}`);
  });
})();
