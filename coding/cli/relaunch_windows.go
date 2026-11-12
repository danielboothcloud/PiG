//go:build windows

package cli

import "errors"

// relaunchSupported reports whether this platform can replace the process image (D238). Windows cannot: a restart there would be a second pig process.
const relaunchSupported = false

var errRelaunchUnsupported = errors.New("relaunch: Windows cannot replace a running process; restart pig instead")

func execImage(string, []string, []string) error { return errRelaunchUnsupported }
