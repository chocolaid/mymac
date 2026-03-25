// Package mackit is a reusable macOS offensive-utility toolkit for
// proof-of-concept development.
//
// All packages operate without CGo — every macOS API call is routed through
// subprocess CLIs (osascript, screencapture, sqlite3, ps, log, codesign) so
// the module compiles cleanly on any Go toolchain without Xcode.
//
// # Sub-packages
//
//   - mackit/screen  — screenshot and screen-recording (multiple techniques)
//   - mackit/tcc     — TCC database enumeration and live permission testing
//   - mackit/ax      — Accessibility element injection and attribute reading
//   - mackit/fs      — symlink staging trees, exfil harvest, cleanup
//   - mackit/proc    — process enumeration, PID lookup, entitlement inspection
//   - mackit/script  — AppleScript runner, log stream capture, keystroke injection
//
// # Quick start
//
//	import (
//	    "fmt"
//	    "mackit/screen"
//	    "mackit/tcc"
//	)
//
//	func main() {
//	    // Check TCC grants for the running process.
//	    results := tcc.LiveTest()
//	    for _, r := range results {
//	        fmt.Printf("%-20s readable=%v\n", r.Service, r.Readable)
//	    }
//
//	    // Capture a screenshot without triggering a TCC prompt.
//	    res, err := screen.Capture(screen.Options{
//	        Technique: screen.TechniqueAppleScript,
//	    })
//	    if err == nil {
//	        fmt.Println("screenshot saved to", res.Path)
//	    }
//	}
//
// # Platform note
//
// mackit targets macOS only.  All packages include a runtime.GOOS guard that
// returns an error on non-darwin systems.
package mackit
