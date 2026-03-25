// Command example demonstrates every mackit sub-package with a short runnable
// tour of the toolkit's API surface.
//
// Run on macOS:
//
//	cd example && go run .
//
// The program is non-destructive: screenshots are written to /tmp, staging
// directories are created and removed, and no files in protected locations are
// modified.
package main

import (
	"fmt"
	"os"
	"time"

	"mackit/ax"
	"mackit/fs"
	"mackit/proc"
	"mackit/screen"
	"mackit/script"
	"mackit/tcc"
)

func main() {
	fmt.Println("=== mackit toolkit demo ===")
	fmt.Println()

	demoTCC()
	demoScreen()
	demoAX()
	demoProc()
	demoFS()
	demoScript()
}

// ─── TCC ─────────────────────────────────────────────────────────────────────

func demoTCC() {
	fmt.Println("── TCC ──────────────────────────────────────────────────────")

	// Show TCC database paths the runtime is aware of.
	paths, err := tcc.DBPaths()
	if err != nil {
		fmt.Println("DBPaths:", err)
	} else {
		fmt.Println("TCC DB paths:")
		for _, p := range paths {
			fmt.Println("  ", p)
		}
	}

	// Attempt TCC recon via direct sqlite3 read.
	grants, err := tcc.Recon()
	if err != nil {
		fmt.Println("Recon (expected if SIP blocks DB access):", err)
	} else {
		fmt.Printf("Recon: %d grants found\n", len(grants))
		for _, g := range grants {
			fmt.Printf("  %-30s  client=%-40s  auth=%s\n", g.Service, g.Client, g.Auth)
		}
	}

	// Live permission test — no DB access required.
	fmt.Println("Live permission tests:")
	results := tcc.LiveTest()
	for _, r := range results {
		fmt.Printf("  %-20s  readable=%v\n", r.Service, r.Readable)
	}

	// Point query for a single service.
	granted, err := tcc.IsGranted(tcc.Desktop)
	if err != nil {
		fmt.Println("IsGranted(Desktop):", err)
	} else {
		fmt.Println("Desktop accessible:", granted)
	}

	fmt.Println()
}

// ─── Screen ──────────────────────────────────────────────────────────────────

func demoScreen() {
	fmt.Println("── Screen ───────────────────────────────────────────────────")

	// Snapshot via screencapture(1).
	res, err := screen.Capture(screen.Options{
		Technique: screen.TechniqueScreencapture,
		OutputDir: "/tmp",
	})
	if err != nil {
		fmt.Println("Capture (screencapture):", err)
	} else {
		fmt.Printf("Screenshot saved: %s (%d bytes)\n", res.Path, res.Size)
		_ = os.Remove(res.Path) // clean up
	}

	// Snapshot via AppleScript Screencapture bridge.
	res2, err := screen.Capture(screen.Options{
		Technique: screen.TechniqueAppleScript,
		OutputDir: "/tmp",
	})
	if err != nil {
		fmt.Println("Capture (AppleScript):", err)
	} else {
		fmt.Printf("Screenshot saved: %s (%d bytes)\n", res2.Path, res2.Size)
		_ = os.Remove(res2.Path)
	}

	// Enumerate visible windows.
	windows, err := screen.WindowList()
	if err != nil {
		fmt.Println("WindowList:", err)
	} else {
		fmt.Printf("Visible windows: %d\n", len(windows))
		for i, w := range windows {
			if i >= 5 {
				fmt.Printf("  ... and %d more\n", len(windows)-5)
				break
			}
			fmt.Printf("  [%d] %s – %s\n", w.WindowID, w.OwnerName, w.Name)
		}
	}

	// Active window.
	aw, err := screen.ActiveWindow()
	if err != nil {
		fmt.Println("ActiveWindow:", err)
	} else {
		fmt.Printf("Active window: [%d] %s – %s\n", aw.WindowID, aw.OwnerName, aw.Name)
	}

	// Short screen recording.
	rec, err := screen.Record(screen.RecordOptions{
		OutputDir: "/tmp",
		Duration:  2 * time.Second,
	})
	if err != nil {
		fmt.Println("Record start:", err)
	} else {
		fmt.Println("Recording started, waiting 2 s…")
		if err := rec.Wait(); err != nil {
			fmt.Println("Record wait:", err)
		} else {
			fmt.Printf("Recording saved: %s\n", rec.Path)
			_ = os.Remove(rec.Path)
		}
	}

	fmt.Println()
}

// ─── AX ──────────────────────────────────────────────────────────────────────

func demoAX() {
	fmt.Println("── Accessibility (ax) ───────────────────────────────────────")

	// Check whether Accessibility is enabled for this process.
	enabled := ax.IsAccessibilityEnabled()
	fmt.Println("Accessibility enabled:", enabled)

	if !enabled {
		fmt.Println("  (skipping AX attribute demos — grant access in System Settings → Privacy → Accessibility)")
		fmt.Println()
		return
	}

	// Describe a public file on disk.
	desc, err := ax.GetDescription("/Applications/Finder.app")
	if err != nil {
		fmt.Println("GetDescription:", err)
	} else {
		fmt.Println("Finder.app AX description:", desc)
	}

	// Fetch a named attribute from an element targeting Finder.
	el := ax.Element{
		App:       "Finder",
		URLPath:   "/Applications",
		Qualifier: "",
	}
	attr, err := ax.GetAttribute(el, ax.AttrDescription)
	if err != nil {
		fmt.Println("GetAttribute:", err)
	} else {
		fmt.Println("AXDescription:", attr)
	}

	// Inject a forged AX focus-change event pointing at a public file.
	if err := ax.PostFocusChanged("Finder", "/Applications/Utilities"); err != nil {
		fmt.Println("PostFocusChanged:", err)
	} else {
		fmt.Println("PostFocusChanged: OK")
	}

	fmt.Println()
}

// ─── Process ─────────────────────────────────────────────────────────────────

func demoProc() {
	fmt.Println("── Process (proc) ───────────────────────────────────────────")

	// List all running processes.
	procs, err := proc.List()
	if err != nil {
		fmt.Println("List:", err)
	} else {
		fmt.Printf("Running processes: %d total\n", len(procs))
		for i, p := range procs {
			if i >= 5 {
				fmt.Printf("  ... and %d more\n", len(procs)-5)
				break
			}
			fmt.Printf("  PID %-6d  %-30s  user=%s\n", p.PID, p.Name, p.User)
		}
	}

	// Check if Finder is running and get its PID.
	if pid, err := proc.GetPID("Finder"); err != nil {
		fmt.Println("GetPID(Finder):", err)
	} else {
		fmt.Println("Finder PID:", pid)

		// Read its entitlements.
		ents, err := proc.Entitlements(pid)
		if err != nil {
			fmt.Println("Entitlements:", err)
		} else {
			fmt.Printf("Finder entitlements (%d keys):\n", len(ents))
			i := 0
			for k, v := range ents {
				if i >= 5 {
					fmt.Printf("  ... and %d more\n", len(ents)-5)
					break
				}
				fmt.Printf("  %-60s = %v\n", k, v)
				i++
			}
		}
	}

	// Check whether VoiceOverSystem daemon is running.
	running := proc.IsRunning("VoiceOverSystem")
	fmt.Println("VoiceOverSystem running:", running)

	fmt.Println()
}

// ─── Filesystem ──────────────────────────────────────────────────────────────

func demoFS() {
	fmt.Println("── Filesystem (fs) ──────────────────────────────────────────")

	home, _ := os.UserHomeDir()

	// Build standard TCC symlink staging tree.
	targets := fs.StandardTargets(home)
	stageDir, err := fs.CreateSymlinkStage("mackit-demo", targets)
	if err != nil {
		fmt.Println("CreateSymlinkStage:", err)
		return
	}
	fmt.Println("Staging directory:", stageDir)
	for name, target := range targets {
		fmt.Printf("  %s → %s\n", name, target)
	}

	// Create an exfil destination.
	exfilDir, err := fs.CreateExfilDir("mackit-demo")
	if err != nil {
		fmt.Println("CreateExfilDir:", err)
	} else {
		fmt.Println("Exfil directory:", exfilDir)
	}

	// Copy a harmless public file to the exfil dir to show CopyFile.
	src := "/etc/hosts"
	dst := exfilDir + "/hosts.txt"
	if err := fs.CopyFile(src, dst); err != nil {
		fmt.Println("CopyFile:", err)
	} else {
		fmt.Println("CopyFile: /etc/hosts →", dst)
	}

	// Harvest walk.
	results, err := fs.Harvest(exfilDir)
	if err != nil {
		fmt.Println("Harvest:", err)
	} else {
		fmt.Printf("Harvest: %d file(s)\n", len(results))
		for _, r := range results {
			fmt.Printf("  %s  (%d bytes, mod %s)\n", r.Path, r.Size, r.ModTime.Format(time.RFC3339))
		}
	}

	// Clean up.
	if err := fs.CleanStage(stageDir, exfilDir); err != nil {
		fmt.Println("CleanStage:", err)
	} else {
		fmt.Println("Cleanup: OK")
	}

	fmt.Println()
}

// ─── Script ──────────────────────────────────────────────────────────────────

func demoScript() {
	fmt.Println("── Script ───────────────────────────────────────────────────")

	// Simple AppleScript evaluation.
	out, err := script.Run(`return (current date) as string`)
	if err != nil {
		fmt.Println("Run:", err)
	} else {
		fmt.Println("Current date (AppleScript):", out)
	}

	// Run with timeout.
	out2, err := script.RunWithTimeout(`delay 0.1
return "ok"`, 3*time.Second)
	if err != nil {
		fmt.Println("RunWithTimeout:", err)
	} else {
		fmt.Println("RunWithTimeout result:", out2)
	}

	// Write script to temp file and execute it.
	tmpScript := `tell application "Finder" to return name of startup disk`
	path, err := script.WriteTemp(tmpScript)
	if err != nil {
		fmt.Println("WriteTemp:", err)
	} else {
		defer os.Remove(path)
		result, err := script.RunFile(path)
		if err != nil {
			fmt.Println("RunFile:", err)
		} else {
			fmt.Println("Startup disk:", result)
		}
	}

	// Tap the Unified Log for com.apple.loginwindow for 1 second.
	lines, err := script.CaptureLog("loginwindow", 1*time.Second, []string{"Login", "auth"})
	if err != nil {
		fmt.Println("CaptureLog:", err)
	} else {
		fmt.Printf("CaptureLog: %d matching lines\n", len(lines))
		for i, l := range lines {
			if i >= 3 {
				fmt.Printf("  ... and %d more\n", len(lines)-3)
				break
			}
			fmt.Println(" ", l)
		}
	}

	// Send a Notification Center notification.
	if err := script.Notify("mackit demo", "All packages exercised successfully"); err != nil {
		fmt.Println("Notify:", err)
	} else {
		fmt.Println("Notification sent")
	}

	fmt.Println()
	fmt.Println("=== demo complete ===")
}
