package extension

import (
	"context"
	"errors"
)

// ForegroundCommand is a program an extension runs in the foreground of the interactive terminal. Cwd empty means the host's working directory. Env entries ("KEY=value") are added to the host's environment.
type ForegroundCommand struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
	Env     []string `json:"env,omitempty"`
}

// ForegroundResult is how a foreground program ended. ExitCode is -1 when a signal ended it; Signal then names it ("SIGINT").
type ForegroundResult struct {
	ExitCode int    `json:"exitCode"`
	Signal   string `json:"signal,omitempty"`
}

// ErrNoForegroundTerminal is the failure of a foreground run where the host has no terminal UI to hand over: print, JSON and RPC modes.
var ErrNoForegroundTerminal = errors.New("runForeground requires the interactive terminal UI")

// ForegroundRunner is a UI that owns a terminal it can hand to a program: RunForeground stops rendering, restores the terminal, runs the program with the terminal as its stdin, stdout and stderr, then re-enters raw mode and redraws the whole screen. It returns after the redraw, whether the program succeeded, failed or was interrupted. Pi's equivalent is ctx.ui.custom calling tui.stop(), a synchronous spawn with inherited stdio, then tui.start() and tui.requestRender(true).
type ForegroundRunner interface {
	RunForeground(ctx context.Context, cmd ForegroundCommand) (ForegroundResult, error)
}
