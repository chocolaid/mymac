// Package tcc provides utilities for enumerating, testing, and bypassing
// macOS Transparency, Consent, and Control (TCC) privacy controls.
//
// Usage:
//
//	import "mackit/tcc"
//
//	grants, err := tcc.Recon()
//	ok := tcc.IsGranted(tcc.Desktop)
//	results := tcc.LiveTest()
package tcc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// consoleUserHome returns the home directory of the currently logged-in GUI user.
// When the agent runs as root this avoids using /var/root, which lacks the usual
// TCC-protected folders (Desktop, Documents, Downloads, etc.).
func consoleUserHome() string {
	// Get the console user's login name
	out, err := exec.Command("stat", "-f", "%Su", "/dev/console").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" || strings.TrimSpace(string(out)) == "root" {
		// Fallback: try to find a real user home under /Users
		entries, err2 := os.ReadDir("/Users")
		if err2 == nil {
			for _, e := range entries {
				if e.IsDir() && e.Name() != "Shared" && !strings.HasPrefix(e.Name(), ".") {
					return "/Users/" + e.Name()
				}
			}
		}
		home, _ := os.UserHomeDir()
		return home
	}
	user := strings.TrimSpace(string(out))
	// Look up the home directory via dscl
	homeOut, err := exec.Command("dscl", ".", "-read", "/Users/"+user, "NFSHomeDirectory").Output()
	if err == nil {
		for _, line := range strings.Split(string(homeOut), "\n") {
			if strings.HasPrefix(line, "NFSHomeDirectory:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "NFSHomeDirectory:"))
			}
		}
	}
	// Most Macs: home is /Users/<username>
	return "/Users/" + user
}

// ─── Service constants ────────────────────────────────────────────────────────

// Service identifies a TCC privacy service.
type Service string

const (
	Desktop        Service = "kTCCServiceSystemPolicyDesktopFolder"
	Documents      Service = "kTCCServiceSystemPolicyDocumentsFolder"
	Downloads      Service = "kTCCServiceSystemPolicyDownloadsFolder"
	AllFiles       Service = "kTCCServiceSystemPolicyAllFiles"
	ScreenCapture  Service = "kTCCServiceScreenCapture"
	Camera         Service = "kTCCServiceCamera"
	Microphone     Service = "kTCCServiceMicrophone"
	Contacts       Service = "kTCCServiceAddressBook"
	Calendar       Service = "kTCCServiceCalendar"
	Photos         Service = "kTCCServicePhotos"
	Accessibility  Service = "kTCCServiceAccessibility"
	Reminders      Service = "kTCCServiceReminders"
	Location       Service = "kTCCServiceLocation"
	iCloud         Service = "kTCCServiceUbiquity"
	DeveloperTool  Service = "kTCCServiceDeveloperTool"
)

// ServiceName maps Service keys to short human-readable labels.
var ServiceName = map[Service]string{
	Desktop:       "Desktop",
	Documents:     "Documents",
	Downloads:     "Downloads",
	AllFiles:      "Full Disk Access",
	ScreenCapture: "Screen Capture",
	Camera:        "Camera",
	Microphone:    "Microphone",
	Contacts:      "Contacts",
	Calendar:      "Calendar",
	Photos:        "Photos",
	Accessibility: "Accessibility",
	Reminders:     "Reminders",
	Location:      "Location",
	iCloud:        "iCloud Drive",
	DeveloperTool: "Developer Tool",
}

// AuthValue mirrors the TCC.db auth_value column.
type AuthValue int

const (
	AuthDenied  AuthValue = 0
	AuthUnknown AuthValue = 1
	AuthAllowed AuthValue = 2
	AuthLimited AuthValue = 3
)

func (a AuthValue) String() string {
	switch a {
	case AuthDenied:
		return "DENIED"
	case AuthUnknown:
		return "UNKNOWN"
	case AuthAllowed:
		return "ALLOWED"
	case AuthLimited:
		return "LIMITED"
	default:
		return fmt.Sprintf("AUTH(%d)", int(a))
	}
}

// ─── Grant represents a single TCC row ───────────────────────────────────────

// Grant is one row from TCC.db.
type Grant struct {
	Service   Service
	Client    string
	AuthValue AuthValue
}

// ─── Recon ───────────────────────────────────────────────────────────────────

// Recon reads the user TCC.db (via sqlite3 CLI) and returns all grants for
// the well-known services listed in ServiceName.
// Returns an error if sqlite3 is unavailable or SIP blocks TCC.db access.
func Recon() ([]Grant, error) {
	home := consoleUserHome()
	db := filepath.Join(home, "Library/Application Support/com.apple.TCC/TCC.db")

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, fmt.Errorf("sqlite3 CLI not found: %w", err)
	}
	if _, err := os.Stat(db); err != nil {
		return nil, fmt.Errorf("TCC.db not accessible: %w", err)
	}

	keys := make([]string, 0, len(ServiceName))
	for svc := range ServiceName {
		keys = append(keys, "'"+string(svc)+"'")
	}
	query := fmt.Sprintf(
		`SELECT service, client, auth_value FROM access WHERE service IN (%s) ORDER BY service, client;`,
		strings.Join(keys, ","),
	)

	out, err := exec.Command("sqlite3", db, query).Output()
	if err != nil {
		return nil, fmt.Errorf("sqlite3 query: %w", err)
	}

	var grants []Grant
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		var av int
		fmt.Sscan(parts[2], &av)
		grants = append(grants, Grant{
			Service:   Service(parts[0]),
			Client:    parts[1],
			AuthValue: AuthValue(av),
		})
	}
	return grants, nil
}

// IsGranted returns true if the current process (identified by its bundle ID
// or executable path) has AuthAllowed for the given service in TCC.db.
func IsGranted(svc Service) bool {
	grants, err := Recon()
	if err != nil {
		return false
	}
	self := selfBundleID()
	for _, g := range grants {
		if g.Service == svc && (g.Client == self) && g.AuthValue == AuthAllowed {
			return true
		}
	}
	return false
}

// ─── LiveTest ────────────────────────────────────────────────────────────────

// LiveTestResult holds the outcome of a direct filesystem access attempt.
type LiveTestResult struct {
	Service  Service
	Path     string
	Allowed  bool
	Entries  int
	ErrorMsg string
}

// LiveTest attempts to open each well-known TCC-protected directory directly
// and reports whether the OS permits access.  This is the most reliable way
// to confirm TCC enforcement without requiring sqlite3 access.
func LiveTest() []LiveTestResult {
	home := consoleUserHome()

	targets := map[Service]string{
		Desktop:   filepath.Join(home, "Desktop"),
		Documents: filepath.Join(home, "Documents"),
		Downloads: filepath.Join(home, "Downloads"),
		Photos:    filepath.Join(home, "Pictures/Photos Library.photoslibrary"),
		iCloud:    filepath.Join(home, "Library/Mobile Documents"),
	}

	results := make([]LiveTestResult, 0, len(targets))
	for svc, path := range targets {
		r := LiveTestResult{Service: svc, Path: path}
		entries, err := os.ReadDir(path)
		if err != nil {
			r.Allowed = false
			r.ErrorMsg = err.Error()
		} else {
			r.Allowed = true
			r.Entries = len(entries)
		}
		results = append(results, r)
	}
	return results
}

// ─── Database paths ──────────────────────────────────────────────────────────

// DBPaths returns the paths to both the per-user and system-wide TCC databases.
func DBPaths() (user, system string) {
	home := consoleUserHome()
	user = filepath.Join(home, "Library/Application Support/com.apple.TCC/TCC.db")
	system = "/Library/Application Support/com.apple.TCC/TCC.db"
	return
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func selfBundleID() string {
	// For a command-line daemon there is no bundle ID — use the executable
	// path, which is what TCC records in the client column for non-app binaries.
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	return exe
}
