package codingagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/internal/linkerexec"
)

// RunForeground hands the terminal to cmd and takes it back (extension.ForegroundRunner).
// pig additive (D19): Pi's extension does this in-process with ctx.ui.custom, tui.stop(), spawnSync(stdio: "inherit") and tui.start(); a subprocess extension's stdio is the host wire, so the host runs the program.
func (u *ExtUIContext) RunForeground(ctx context.Context, cmd extension.ForegroundCommand) (extension.ForegroundResult, error) {
	m := u.m
	if m.onOwnerLoop() {
		// The child runs off the owner loop while the loop waits; from the loop itself that wait could not end.
		return extension.ForegroundResult{}, errors.New("runForeground: called from the TUI loop")
	}
	released := false
	if err := m.runOnMainAndWait(ctx, func() error {
		released = m.releaseTerminal()
		return nil
	}); err != nil {
		return extension.ForegroundResult{}, err
	}
	if !released {
		return extension.ForegroundResult{}, errors.New("runForeground: the terminal is already handed to another program")
	}
	result, runErr := runForegroundCommand(ctx, cmd, &m.foregroundActive)
	ownerCtx := m.runCtx
	if ownerCtx == nil {
		ownerCtx = context.Background()
	}
	// The TUI comes back whatever happened to the program or the request.
	reclaimErr := m.runOnMainAndWait(context.WithoutCancel(ctx), func() error {
		m.reclaimTerminal(ownerCtx, nil)
		return nil
	})
	if runErr != nil {
		return extension.ForegroundResult{}, runErr
	}
	return result, reclaimErr
}

// runForegroundCommand runs cmd with the process's own stdin, stdout and stderr. active is set while the program runs, so the SIGINT a Ctrl+C sends to the terminal's foreground process group stops the program and leaves the session running.
func runForegroundCommand(ctx context.Context, spec extension.ForegroundCommand, active *atomic.Bool) (extension.ForegroundResult, error) {
	c := linkerexec.CommandContext(ctx, spec.Command, spec.Args...)
	c.Dir = spec.Cwd
	if len(spec.Env) > 0 {
		c.Env = append(os.Environ(), spec.Env...)
	}
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	active.Store(true)
	err := c.Run()
	active.Store(false)
	if err == nil {
		return extension.ForegroundResult{ExitCode: 0}, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if signal := editorSignal(exit); signal != "" {
			return extension.ForegroundResult{ExitCode: -1, Signal: signal}, nil
		}
		return extension.ForegroundResult{ExitCode: exit.ExitCode()}, nil
	}
	return extension.ForegroundResult{}, fmt.Errorf("runForeground: start %s: %w", spec.Command, err)
}
