//go:build !windows

package cli

import (
	"errors"
	"syscall"
)

// relaunchSupported reports whether this platform can replace the process image (D238).
const relaunchSupported = true

var errRelaunchUnsupported = errors.New("relaunch is not supported on this platform")

// execImage replaces the process image. Go opens every descriptor it creates close-on-exec, so only the standard streams pass to the new image.
func execImage(path string, argv, env []string) error {
	//nolint:gosec // G204 (class b): path is this pig's own executable and argv the arguments an extension asked pig to restart with; execve runs no shell.
	return syscall.Exec(path, argv, env)
}
