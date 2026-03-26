// Package mackit is a reusable macOS offensive-utility toolkit for
// proof-of-concept development.
//
// Most packages operate without CGo — API calls are routed through subprocess
// CLIs (osascript, screencapture, sqlite3, ps, log, codesign, nvram, launchctl).
// The inject package uses CGo exclusively for Mach-level task_for_pid and
// thread_create_running primitives that are not callable from pure Go.
//
// # Sub-packages
//
//   - mackit/sip     — SIP status (CSR flags), per-flag guards
//   - mackit/screen  — screenshot and screen-recording (multiple techniques)
//   - mackit/tcc     — TCC enumeration, live testing, WriteGrant / Provision
//   - mackit/ax      — Accessibility element injection and attribute reading
//   - mackit/fs      — symlink staging trees, exfil harvest, cleanup
//   - mackit/proc    — process enumeration, PID lookup, entitlement inspection
//   - mackit/script  — AppleScript runner, log stream capture, keystroke injection
//   - mackit/inject  — dylib injection (DYLD_INSERT_LIBRARIES + Mach task injection)
//   - mackit/agent   — launchd agent discovery, Hijack / Restore, assetsd bootstrap
//
// # Quick start
//
//	import (
//	    "fmt"
//	    "mackit/sip"
//	    "mackit/tcc"
//	    "mackit/inject"
//	    "mackit/agent"
//	)
//
//	func main() {
//	    // Gate on SIP state before attempting privileged ops.
//	    fmt.Println(sip.Check())
//
//	    // Enumerate TCC grants.
//	    grants, _ := tcc.Recon()
//	    for _, g := range grants {
//	        fmt.Printf("%-30s %s  %s\n", g.Service, g.Client, g.AuthValue)
//	    }
//
//	    // Show known injection targets.
//	    for _, t := range inject.KnownTargets() {
//	        fmt.Printf("%-30s  %v\n", t.Name, t.Services)
//	    }
//
//	    // Hijack CoreServicesUIAgent (requires root + SIP off).
//	    _ = agent.Hijack("com.apple.coreservices.uiagent", "/tmp/payload")
//	}
//
// # Platform note
//
// mackit targets macOS only.  All packages include a runtime.GOOS guard that
// returns an error on non-darwin systems.
package mackit
