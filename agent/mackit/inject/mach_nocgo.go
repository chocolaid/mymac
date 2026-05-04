//go:build !cgo

package inject

import "errors"

// machInjectDylib is the stub used when CGO is disabled (e.g. cross-compilation
// from Linux).  The real implementation lives in mach_darwin.go and requires
// CGO + macOS SDK headers.
func machInjectDylib(pid int, dylibPath string) error {
	return errors.New("mach injection requires CGO (not available in this build)")
}
