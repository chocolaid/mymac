// Package proc provides utilities for process enumeration, PID lookup,
// entitlement inspection, and dylib injection — all via subprocess CLIs
// (ps, codesign, launchctl, etc.) with no CGo dependency.
//
// Usage:
//
//	import "mackit/proc"
//
//	procs, err := proc.List()
//	pid, err  := proc.GetPID("fileproviderd")
//	running   := proc.IsRunning("VoiceOverSystem")
//	ents, err := proc.Entitlements(pid)
package proc

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ─── Types ───────────────────────────────────────────────────────────────────

// Process represents a running process.
type Process struct {
	PID  int
	PPID int
	User string
	Name string
	Args string
}

// ─── List ────────────────────────────────────────────────────────────────────

// List enumerates all running processes using ps.
func List() ([]Process, error) {
	out, err := exec.Command("ps", "axo", "pid,ppid,user,comm").Output()
	if err != nil {
		return nil, fmt.Errorf("proc.List ps: %w", err)
	}

	var procs []Process
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "PID") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		pid, _ := strconv.Atoi(fields[0])
		ppid, _ := strconv.Atoi(fields[1])
		procs = append(procs, Process{
			PID:  pid,
			PPID: ppid,
			User: fields[2],
			Name: fields[3],
		})
	}
	return procs, nil
}

// ─── GetPID ──────────────────────────────────────────────────────────────────

// GetPID returns the PID of the first process whose comm field contains name.
// Returns an error if no matching process is found.
func GetPID(name string) (int, error) {
	out, err := exec.Command("pgrep", "-x", name).Output()
	if err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		if pid > 0 {
			return pid, nil
		}
	}

	// Fallback: walk full list (matches partial name).
	procs, err := List()
	if err != nil {
		return 0, err
	}
	for _, p := range procs {
		if strings.Contains(p.Name, name) {
			return p.PID, nil
		}
	}
	return 0, fmt.Errorf("proc.GetPID: no process matching %q", name)
}

// ─── IsRunning ───────────────────────────────────────────────────────────────

// IsRunning reports whether a process whose comm matches name is currently
// running.
func IsRunning(name string) bool {
	_, err := GetPID(name)
	return err == nil
}

// ─── Entitlements ────────────────────────────────────────────────────────────

// Entitlements dumps the code-signing entitlements for the given PID using
// codesign and returns the raw XML plist as a string.
func Entitlements(pid int) (string, error) {
	// codesign requires the path, not the PID.
	// Resolve executable path via /proc (Linux-style) or sysctl.
	path, err := executablePath(pid)
	if err != nil {
		return "", fmt.Errorf("proc.Entitlements resolveExec(%d): %w", pid, err)
	}

	out, err := exec.Command("codesign", "--display", "--entitlements", "-", "--xml", path).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("proc.Entitlements codesign: %w — %s", err, bytes.TrimSpace(out))
	}
	return string(out), nil
}

// EntitlementsPath is like Entitlements but takes a file path instead of PID.
func EntitlementsPath(path string) (string, error) {
	out, err := exec.Command("codesign", "--display", "--entitlements", "-", "--xml", path).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("proc.EntitlementsPath codesign: %w — %s", err, bytes.TrimSpace(out))
	}
	return string(out), nil
}

// ─── HasEntitlement ──────────────────────────────────────────────────────────

// HasEntitlement reports whether the process at path has a specific
// entitlement key (e.g., "com.apple.private.tcc.allow").
func HasEntitlement(path, key string) bool {
	out, err := exec.Command("codesign", "--display", "--entitlements", "-", "--xml", path).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), key)
}

// ─── InjectDylib ─────────────────────────────────────────────────────────────

// InjectDylib attempts to inject a dylib into the target process using
// DYLD_INSERT_LIBRARIES via a re-exec of the process.
//
// WARNING: This only works if the target binary is not hardened (lacks the
// com.apple.security.cs.disable-library-validation entitlement or the
// Hardened Runtime flag).  On modern macOS most system daemons are hardened.
//
// Returns an error if injection fails.
func InjectDylib(pid int, dylibPath string) error {
	path, err := executablePath(pid)
	if err != nil {
		return fmt.Errorf("proc.InjectDylib resolveExec: %w", err)
	}

	cmd := exec.Command(path)
	cmd.Env = append(cmd.Env, "DYLD_INSERT_LIBRARIES="+dylibPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("proc.InjectDylib exec: %w — %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// ─── Kill ────────────────────────────────────────────────────────────────────

// Kill sends SIGKILL to the given PID.
func Kill(pid int) error {
	return exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
}

// Restart restarts a launchd-managed service by label.
func Restart(label string) error {
	_ = exec.Command("launchctl", "bootout", "user/"+strconv.Itoa(os.Getuid()), label).Run()
	return exec.Command("launchctl", "bootstrap", "user/"+strconv.Itoa(os.Getuid()), label).Run()
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// executablePath returns the executable path for a PID using sysctl (macOS).
func executablePath(pid int) (string, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", fmt.Errorf("no comm for pid %d", pid)
	}
	return p, nil
}
