// Package script provides AppleScript execution helpers and macOS Unified
// Logging capture — all via subprocess CLIs (osascript, log) with no CGo.
//
// Usage:
//
//	import "mackit/script"
//
//	out, err := script.Run(`tell application "Finder" to return name of startup disk`)
//	lines, err := script.CaptureLog("VoiceOverSystem", 3*time.Second, []string{"kAXURL", "file://"})
//	err = script.Notify("Alert", "PoC triggered successfully")
package script

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ─── Run ─────────────────────────────────────────────────────────────────────

// Run executes an inline AppleScript and returns stdout + stderr combined.
//
// Equivalent to: osascript -e '<script>'
func Run(appleScript string) (string, error) {
	out, err := exec.Command("osascript", "-e", appleScript).CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)),
			fmt.Errorf("script.Run: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// RunArgs executes osascript with arbitrary arguments (e.g. -s flags).
func RunArgs(args ...string) (string, error) {
	out, err := exec.Command("osascript", args...).CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)),
			fmt.Errorf("script.RunArgs: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ─── RunFile ─────────────────────────────────────────────────────────────────

// RunFile executes an AppleScript stored in a file.
func RunFile(path string) (string, error) {
	out, err := exec.Command("osascript", path).CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)),
			fmt.Errorf("script.RunFile %s: %w — %s", path, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ─── RunWithTimeout ───────────────────────────────────────────────────────────

// RunWithTimeout executes an inline AppleScript with a hard timeout.
// If the script does not complete in time, the osascript process is killed and
// ErrTimeout is returned.
func RunWithTimeout(appleScript string, timeout time.Duration) (string, error) {
	cmd := exec.Command("osascript", "-e", appleScript)
	done := make(chan struct {
		out []byte
		err error
	}, 1)

	go func() {
		out, err := cmd.CombinedOutput()
		done <- struct {
			out []byte
			err error
		}{out, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			return "", fmt.Errorf("script.RunWithTimeout: %w — %s", r.err, strings.TrimSpace(string(r.out)))
		}
		return strings.TrimSpace(string(r.out)), nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("script.RunWithTimeout: osascript timed out after %v", timeout)
	}
}

// ─── Notify ───────────────────────────────────────────────────────────────────

// Notify displays a macOS Notification Center alert.
func Notify(title, message string) error {
	safeMsgTitle := escAS(title)
	safeMsgBody := escAS(message)
	_, err := Run(fmt.Sprintf(`display notification "%s" with title "%s"`, safeMsgBody, safeMsgTitle))
	return err
}

// Dialog shows a blocking alert dialog.
func Dialog(message string) error {
	_, err := Run(fmt.Sprintf(`display alert "%s"`, escAS(message)))
	return err
}

// ─── WriteTemp ────────────────────────────────────────────────────────────────

// WriteTemp writes appleScript content to a temporary .applescript file and
// returns its path.  Caller is responsible for deleting the file.
func WriteTemp(appleScript string) (string, error) {
	f, err := os.CreateTemp("", "mackit-*.applescript")
	if err != nil {
		return "", fmt.Errorf("script.WriteTemp: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(appleScript); err != nil {
		return "", fmt.Errorf("script.WriteTemp write: %w", err)
	}
	return f.Name(), nil
}

// ─── CaptureLog ──────────────────────────────────────────────────────────────

// CaptureLog runs `log stream --process <process>` for the given duration and
// returns lines that contain at least one of the provided keywords.
//
// If keywords is empty, all lines are returned.
//
// This is the passive log-tap used by the CVE-2025-43530 harvest phase to
// catch VoiceOverSystem announcing TCC-protected file paths it opened.
func CaptureLog(process string, duration time.Duration, keywords []string) ([]string, error) {
	args := []string{"stream", "--process", process, "--color", "none"}
	cmd := exec.Command("log", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("script.CaptureLog StdoutPipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("script.CaptureLog Start: %w", err)
	}

	deadline := time.NewTimer(duration)
	var results []string

	scanner := bufio.NewScanner(stdout)
	done := make(chan struct{})

	go func() {
		defer close(done)
		for scanner.Scan() {
			line := scanner.Text()
			if matchesAny(line, keywords) {
				results = append(results, line)
			}
		}
	}()

	<-deadline.C
	_ = cmd.Process.Kill()
	<-done

	return results, nil
}

// ─── Keystroke ────────────────────────────────────────────────────────────────

// Keystroke sends a key combination to the frontmost application via System
// Events.
//
// modifiers can contain any of: "command", "option", "shift", "control"
//
// Example: Keystroke("3", "command", "shift")   ── triggers ⌘⇧3 screenshot
func Keystroke(key string, modifiers ...string) error {
	modStr := ""
	if len(modifiers) > 0 {
		parts := make([]string, len(modifiers))
		for i, m := range modifiers {
			parts[i] = m + " down"
		}
		modStr = " using {" + strings.Join(parts, ", ") + "}"
	}

	script := fmt.Sprintf(`tell application "System Events" to keystroke "%s"%s`, key, modStr)
	_, err := Run(script)
	return err
}

// KeyCode sends a raw key code to the frontmost application.
func KeyCode(code int, modifiers ...string) error {
	modStr := ""
	if len(modifiers) > 0 {
		parts := make([]string, len(modifiers))
		for i, m := range modifiers {
			parts[i] = m + " down"
		}
		modStr = " using {" + strings.Join(parts, ", ") + "}"
	}

	script := fmt.Sprintf(`tell application "System Events" to key code %d%s`, code, modStr)
	_, err := Run(script)
	return err
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// escAS escapes double-quotes in an AppleScript string literal.
func escAS(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

// matchesAny returns true if line contains any keyword (case-sensitive).
// Returns true unconditionally if keywords is empty.
func matchesAny(line string, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	for _, kw := range keywords {
		if strings.Contains(line, kw) {
			return true
		}
	}
	return false
}
