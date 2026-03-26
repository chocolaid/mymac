// Package sip queries System Integrity Protection (SIP) state and provides
// guards for operations that require SIP to be disabled.
//
// SIP state is stored in NVRAM under "csr-active-config" as a little-endian
// uint32 bitmask (matching xnu bsd/sys/csr.h).  Reading that key requires no
// root privilege and works even from a sandboxed context.
//
// Usage:
//
//	import "mackit/sip"
//
//	flags, err := sip.Flags()
//	disabled   := sip.IsDisabled()
//	st         := sip.Check()
//	err        := sip.AssertDisabled()
package sip

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
)

// ─── CSR flag constants (xnu bsd/sys/csr.h) ──────────────────────────────────

const (
	// CSRAllowUntrustedKexts permits loading unsigned / third-party kexts.
	CSRAllowUntrustedKexts uint32 = 0x0001
	// CSRAllowUnrestrictedFS removes the filesystem write restrictions on
	// protected paths (/System, /usr, /bin, /sbin, /Applications).
	CSRAllowUnrestrictedFS uint32 = 0x0002
	// CSRAllowTaskForPID permits any process to call task_for_pid on any other
	// process (required for Mach-level process injection without entitlements).
	CSRAllowTaskForPID uint32 = 0x0004
	// CSRAllowKernelDebugger grants kernel debugging.
	CSRAllowKernelDebugger uint32 = 0x0008
	// CSRAllowAppleInternal enables Apple-internal developer features.
	CSRAllowAppleInternal uint32 = 0x0010
	// CSRAllowUnrestrictedDTrace removes DTrace restrictions.
	CSRAllowUnrestrictedDTrace uint32 = 0x0020
	// CSRAllowUnrestrictedNVRAM permits arbitrary NVRAM writes.
	CSRAllowUnrestrictedNVRAM uint32 = 0x0040
	// CSRAllowDeviceConfiguration permits device configuration changes.
	CSRAllowDeviceConfiguration uint32 = 0x0080
	// CSRAllowAnyRecoveryOS permits booting any signed recovery OS.
	CSRAllowAnyRecoveryOS uint32 = 0x0100
	// CSRAllowUnapprovedKexts permits kext approval bypass.
	CSRAllowUnapprovedKexts uint32 = 0x0200
	// CSRAllowExecutablePolicyOverride bypasses executable policy enforcement.
	CSRAllowExecutablePolicyOverride uint32 = 0x0400
	// CSRAllowUnauthenticatedRoot permits mounting an unsigned root volume
	// (required to patch system files on macOS 11+).
	CSRAllowUnauthenticatedRoot uint32 = 0x0800

	// CSRFullyEnabled is the default SIP-on value (all flags cleared).
	CSRFullyEnabled uint32 = 0x0000
	// CSRFullyDisabled is the value produced by "csrutil disable" in Recovery.
	// All flags through 0x0800 are set.
	CSRFullyDisabled uint32 = 0x0FFF
)

// ─── Status types ─────────────────────────────────────────────────────────────

// Status holds the parsed state of every CSR flag.
type Status struct {
	// Raw is the uint32 value read from NVRAM (or 0 if NVRAM is unavailable).
	Raw uint32

	// Per-flag breakdown
	UntrustedKexts          bool
	UnrestrictedFS          bool
	TaskForPID              bool
	KernelDebugger          bool
	AppleInternal           bool
	UnrestrictedDTrace      bool
	UnrestrictedNVRAM       bool
	DeviceConfiguration     bool
	AnyRecoveryOS           bool
	UnapprovedKexts         bool
	ExecutablePolicyOverride bool
	UnauthenticatedRoot     bool
}

// Disabled returns true if any SIP restriction has been lifted.
func (s Status) Disabled() bool { return s.Raw != CSRFullyEnabled }

// String returns a one-line human-readable summary.
func (s Status) String() string {
	if !s.Disabled() {
		return fmt.Sprintf("SIP enabled (csr=0x%04X)", s.Raw)
	}
	var parts []string
	if s.UnrestrictedFS {
		parts = append(parts, "unrestricted-fs")
	}
	if s.TaskForPID {
		parts = append(parts, "task-for-pid")
	}
	if s.UntrustedKexts {
		parts = append(parts, "untrusted-kexts")
	}
	if s.UnauthenticatedRoot {
		parts = append(parts, "unauthenticated-root")
	}
	if s.UnrestrictedNVRAM {
		parts = append(parts, "unrestricted-nvram")
	}
	extra := ""
	if len(parts) > 0 {
		extra = " [" + strings.Join(parts, ", ") + "]"
	}
	return fmt.Sprintf("SIP disabled (csr=0x%04X)%s", s.Raw, extra)
}

// ─── Primary API ──────────────────────────────────────────────────────────────

// Flags reads the CSR bitmask from NVRAM.
// On a machine with SIP fully enabled the key is absent; Flags returns (0, nil).
// On a machine booted with SIP disabled, returns the active flag bitmask.
// Does NOT require root.
func Flags() (uint32, error) {
	out, err := exec.Command("nvram", "csr-active-config").Output()
	if err != nil {
		// Key missing → SIP fully enabled (all zeros).
		if strings.Contains(err.Error(), "Error getting variable") ||
			strings.Contains(string(out), "Error") {
			return 0, nil
		}
		return 0, fmt.Errorf("nvram: %w", err)
	}

	// nvram output format: "csr-active-config\t%data\n"
	// %data may be printed as raw bytes or as a hex string depending on macOS version.
	line := strings.TrimSpace(string(out))
	idx := strings.Index(line, "\t")
	if idx < 0 {
		return 0, fmt.Errorf("unexpected nvram output: %q", line)
	}
	raw := strings.TrimSpace(line[idx+1:])

	return parseCSR(raw)
}

// IsDisabled returns true if any SIP flag is set (i.e. SIP is not fully on).
// Does NOT require root.
func IsDisabled() bool {
	flags, _ := Flags()
	return flags != CSRFullyEnabled
}

// Check returns the full parsed Status.  Never returns an error — on failure
// it falls back to assuming SIP is fully enabled (the safe default).
func Check() Status {
	flags, _ := Flags()
	return parseStatus(flags)
}

// AssertDisabled returns an error (with the current flags) if SIP is enabled.
// Use as a pre-condition guard in operations that require unrestricted access.
//
//	if err := sip.AssertDisabled(); err != nil { ... }
func AssertDisabled() error {
	st := Check()
	if !st.Disabled() {
		return fmt.Errorf("SIP is enabled (csr=0x%04X); reboot into Recovery and run 'csrutil disable'", st.Raw)
	}
	return nil
}

// AssertTaskForPID returns an error if the CSRAllowTaskForPID flag is not set.
// This specific flag controls task_for_pid access needed for Mach injection.
func AssertTaskForPID() error {
	st := Check()
	if !st.TaskForPID {
		return fmt.Errorf(
			"SIP task_for_pid flag not set (csr=0x%04X); needed for Mach injection — "+
				"disable SIP or enable with: csrutil enable --without debug",
			st.Raw)
	}
	return nil
}

// AssertUnrestrictedFS returns an error if the CSRAllowUnrestrictedFS flag is
// not set.  That flag is required to write to /System, /usr, or swap system
// agent binaries in /System/Library.
func AssertUnrestrictedFS() error {
	st := Check()
	if !st.UnrestrictedFS {
		return fmt.Errorf(
			"SIP unrestricted-fs flag not set (csr=0x%04X); required to modify protected paths",
			st.Raw)
	}
	return nil
}

// ─── csrutil passthrough ──────────────────────────────────────────────────────

// CsrutilStatus runs `csrutil status` and returns the raw output line.
// This is the human-facing status; Flags() is the canonical machine-readable
// source of truth.  Requires csrutil to be in PATH (always true on macOS).
func CsrutilStatus() (string, error) {
	out, err := exec.Command("csrutil", "status").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("csrutil: %w — %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// parseCSR converts the nvram output value (raw bytes or hex string) to uint32.
// nvram on macOS Monterey+ prints bytes literally; older versions print hex.
func parseCSR(raw string) (uint32, error) {
	// Case 1: printed as something like "%00%00%00%00" (URL-encoded bytes)
	if strings.Contains(raw, "%") {
		raw = strings.ReplaceAll(raw, "%", "")
		// Now looks like "00000000" — 8 hex chars for a uint32 LE
		decoded, err := hex.DecodeString(raw)
		if err != nil {
			return 0, fmt.Errorf("parseCSR hex-decode: %w", err)
		}
		if len(decoded) < 4 {
			return 0, nil
		}
		return binary.LittleEndian.Uint32(decoded[:4]), nil
	}

	// Case 2: raw bytes printed literally as "\x77\x00\x00\x00" or similar.
	// Go's exec.Output returns the actual bytes, so we check length.
	if len(raw) >= 4 {
		return binary.LittleEndian.Uint32([]byte(raw)[:4]), nil
	}

	// Case 3: plain hex string "77000000"
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		return 0, fmt.Errorf("parseCSR: unrecognised format %q: %w", raw, err)
	}
	if len(decoded) < 4 {
		return 0, nil
	}
	return binary.LittleEndian.Uint32(decoded[:4]), nil
}

func parseStatus(flags uint32) Status {
	return Status{
		Raw:                     flags,
		UntrustedKexts:          flags&CSRAllowUntrustedKexts != 0,
		UnrestrictedFS:          flags&CSRAllowUnrestrictedFS != 0,
		TaskForPID:              flags&CSRAllowTaskForPID != 0,
		KernelDebugger:          flags&CSRAllowKernelDebugger != 0,
		AppleInternal:           flags&CSRAllowAppleInternal != 0,
		UnrestrictedDTrace:      flags&CSRAllowUnrestrictedDTrace != 0,
		UnrestrictedNVRAM:       flags&CSRAllowUnrestrictedNVRAM != 0,
		DeviceConfiguration:     flags&CSRAllowDeviceConfiguration != 0,
		AnyRecoveryOS:           flags&CSRAllowAnyRecoveryOS != 0,
		UnapprovedKexts:         flags&CSRAllowUnapprovedKexts != 0,
		ExecutablePolicyOverride: flags&CSRAllowExecutablePolicyOverride != 0,
		UnauthenticatedRoot:     flags&CSRAllowUnauthenticatedRoot != 0,
	}
}
