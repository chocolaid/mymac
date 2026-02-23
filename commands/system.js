// commands/system.js
// Pre-built macOS command strings for the bot.
// Import this wherever you need to build shell commands to send to Mac agents.
'use strict';

const system = {
  // ── System info ─────────────────────────────────────────────────────────────
  sysinfo:       () => 'system_profiler SPHardwareDataType SPSoftwareDataType 2>/dev/null',
  uptime:        () => 'uptime && sw_vers',
  diskUsage:     () => 'df -h',
  memoryPressure:() => 'vm_stat && sysctl -n hw.memsize',
  cpuLoad:       () => 'top -l 1 -s 0 | head -25',

  // ── Processes ────────────────────────────────────────────────────────────────
  processes:     (n = 20) => `ps aux | sort -rk 3 | head -${n + 1}`,
  killProcess:   (pid)    => `kill -9 ${parseInt(pid)}`,

  // ── Network ──────────────────────────────────────────────────────────────────
  networkInterfaces: () => 'ifconfig',
  openConnections:   () => 'lsof -i -n -P | grep -E "ESTABLISHED|LISTEN"',
  wifiInfo: () =>
    '/System/Library/PrivateFrameworks/Apple80211.framework/Versions/Current/Resources/airport -I',
  wifiScan: () =>
    '/System/Library/PrivateFrameworks/Apple80211.framework/Versions/Current/Resources/airport -s',
  publicIp:  () => 'curl -s https://api.ipify.org',
  flushDns:  () => 'dscacheutil -flushcache && killall -HUP mDNSResponder',

  // ── Files ────────────────────────────────────────────────────────────────────
  listDir:    (path = '~') => `ls -lah "${path}" 2>&1`,
  readFile:   (path)       => `cat "${path}" 2>&1`,
  deleteFile: (path)       => `rm -rf "${path}" 2>&1`,

  // ── Users & sessions ─────────────────────────────────────────────────────────
  whoIsLoggedIn: () => 'who && last | head -10',
  listUsers:     () => 'dscl . list /Users | grep -v "^_"',

  // ── Power ────────────────────────────────────────────────────────────────────
  sleep:    () => 'pmset sleepnow',
  restart:  () => 'shutdown -r now',
  shutdown: () => 'shutdown -h now',

  // ── Screen ───────────────────────────────────────────────────────────────────
  lockScreen:  () =>
    `osascript -e 'tell application "System Events" to keystroke "q" using {command down, control down}'`,
  screenshot:  (path = '/tmp/_sc.jpg') =>
    `screencapture -t jpg "${path}" && base64 "${path}" && rm -f "${path}"`,

  // ── Clipboard ────────────────────────────────────────────────────────────────
  getClipboard: () => 'pbpaste',
  setClipboard: (text) =>
    `printf '%s' '${text.replace(/'/g, "'\\''")}' | pbcopy`,

  // ── Applications ─────────────────────────────────────────────────────────────
  openApp:       (name) => `open -a "${name}"`,
  quitApp:       (name) => `osascript -e 'quit app "${name}"'`,
  installedApps: ()     => 'ls /Applications',

  // ── Security ─────────────────────────────────────────────────────────────────
  firewallStatus:   () => '/usr/libexec/ApplicationFirewall/socketfilterfw --getglobalstate',
  gatekeeperStatus: () => 'spctl --status',
  sipStatus:        () => 'csrutil status',

  // ── Notifications / audio ────────────────────────────────────────────────────
  say:          (msg)   => `say "${msg.replace(/"/g, '\\"')}"`,
  notification: (title, msg) =>
    `osascript -e 'display notification "${msg}" with title "${title}"'`,

  // ── Raw passthrough ───────────────────────────────────────────────────────────
  raw: (cmd) => cmd,
};

module.exports = system;
