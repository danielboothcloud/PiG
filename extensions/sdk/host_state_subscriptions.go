package sdk

import (
	"fmt"
	"os"
	"runtime/debug"
)

// ThemeChangeHandler receives the host's new theme after it changed.
type ThemeChangeHandler func(ctx Context, theme UITheme)

// ToolsExpandedChangeHandler receives the new tool-expansion state after the host toggled it.
type ToolsExpandedChangeHandler func(ctx Context, expanded bool)

// OnThemeChange subscribes to changes of the host's theme: the user switching theme, a custom theme's file reloading, the terminal reporting new colors. handler receives the new theme after [Context.UITheme] was updated.
//
// Pi calls a widget component's invalidate() on a theme change, and the component reads ctx.ui.theme at its next render, so it never paints the colors of an old theme. A subprocess extension pushes rows that were colored when they were built, so it re-renders from this subscription. [Context.SetWidgetRenderer] colors through the live theme at every render and needs no handler for it.
//
// Handlers run on their own goroutine, one at a time in the order the changes arrived, never on the message loop or the goroutine that reads the host's replies, so a handler can make a blocking host call. A handler that panics is recovered and does not stop the others. The returned unsubscribe is idempotent; a delivery already queued still reaches the handlers subscribed when the change arrived. A palette equal to the one before is not a change.
func (c Context) OnThemeChange(handler ThemeChangeHandler) (func(), error) {
	if handler == nil {
		return func() {}, fmt.Errorf("OnThemeChange: handler must not be nil")
	}
	return c.ext.themeFeed.subscribe(handler), nil
}

// OnToolsExpandedChange subscribes to the host toggling tool-output expansion (Pi's expand key, or ctx.ui.setToolsExpanded from any extension). handler receives the new state after it changed.
//
// The original of a widget reads ui.getToolsExpanded() at every frame, and Pi redraws on its expand key; a subprocess extension pushes rows built for one state and re-renders from this subscription. It follows the contract of [Context.OnThemeChange].
func (c Context) OnToolsExpandedChange(handler ToolsExpandedChangeHandler) (func(), error) {
	if handler == nil {
		return func() {}, fmt.Errorf("OnToolsExpandedChange: handler must not be nil")
	}
	return c.ext.expandedFeed.subscribe(handler), nil
}

// ForegroundOptions configures [Context.RunForeground]. Cwd empty means the host's working directory. Env entries ("KEY=value") are added to the host's environment.
type ForegroundOptions struct {
	Cwd string
	Env []string
}

// RunForeground hands the interactive terminal to a program and takes it back: the host stops rendering, restores the terminal from raw mode, runs command with the terminal as its stdin, stdout and stderr, then re-enters raw mode and redraws the whole screen. It returns the program's exit code, or -1 when a signal (a Ctrl+C) ended it. The TUI comes back whether the program succeeded, failed or was interrupted. The call fails at once in print, JSON and RPC modes, where there is no terminal to hand over, and when the program cannot start.
//
// A Pi extension does this with ctx.ui.custom, tui.stop(), a spawn with inherited stdio, then tui.start(); a subprocess extension's own stdio is the host wire, so the host runs the program.
func (c Context) RunForeground(command string, args []string, opts ForegroundOptions) (int, error) {
	payload := map[string]any{"command": command, "args": args}
	if opts.Cwd != "" {
		payload["cwd"] = opts.Cwd
	}
	if len(opts.Env) > 0 {
		payload["env"] = opts.Env
	}
	result, err := hostDecoded[struct {
		ExitCode int    `json:"exitCode"`
		Signal   string `json:"signal"`
	}](c, "ui.runForeground", payload)
	if err != nil {
		return -1, err
	}
	return result.ExitCode, nil
}

// Bell rings the terminal bell (BEL) of the interactive terminal between frames, the call a Pi extension makes by writing "\x07" to process.stdout. It does nothing where there is no terminal UI: print, JSON and RPC modes. The returned error reports a failure to reach the host only.
func (c Context) Bell() error {
	result, err := c.callHost("ui.bell", nil)
	return callResultError(result, err)
}

// ExtensionSourceInfo is the provenance the loader stamped on this extension: upstream's extension.sourceInfo (types.ts SourceInfo), where ExtensionRunner.getExtensionPaths reads the extension's path from. Path is the entry file and BaseDir the package root when the extension came from a Package; a Pi extension reads both from import.meta.url, which a compiled subprocess extension has no equivalent of. ok is false when the host holds no source for this extension.
func (c Context) ExtensionSourceInfo() (info SourceInfo, ok bool) {
	c.ext.mu.RLock()
	defer c.ext.mu.RUnlock()
	return c.ext.sourceInfo, c.ext.sourceInfoKnown
}

// OnLoaded registers handler to run once the extension is registered with the host and has received its state, before it serves any request (session_start included). It is the place for work a factory cannot do: a [Context] exists, so the handler can read session state, call the host and emit on [Context.Events]. Handlers run in registration order, one at a time, each to completion, on a goroutine of their own, so a host call the host answers itself is answered. The host's requests wait behind them (a listener dispatch excepted), so a handler must not wait for something that needs this extension to serve a request, such as a call that runs one of its own tools or event handlers: that wait never ends. A handler that panics is recovered and does not stop the next. Pi has no such hook: its factory runs with no context (its action methods throw until the runner binds) and the extension's first handler is session_start, where an await of any host call works. A handler registered after Run has started never runs.
func (e *Extension) OnLoaded(handler func(ctx Context)) {
	if handler == nil {
		panic("sdk: OnLoaded: handler must not be nil")
	}
	e.loadedMu.Lock()
	e.loadedFuncs = append(e.loadedFuncs, handler)
	e.loadedMu.Unlock()
}

// startLoaded runs the OnLoaded handlers on a goroutine of their own, and returns the channel that closes when they are done; requests other than listener dispatches wait for it.
func (e *Extension) startLoaded() <-chan struct{} {
	e.loadedMu.Lock()
	handlers := e.loadedFuncs
	e.loadedFuncs = nil
	e.loadedMu.Unlock()
	done := make(chan struct{})
	if len(handlers) == 0 {
		close(done)
		return done
	}
	e.requestWG.Go(func() {
		defer close(done)
		ctx := Context{ext: e}
		for _, handler := range handlers {
			func() {
				defer func() {
					if r := recover(); r != nil {
						fmt.Fprintf(os.Stderr, "extension: recovered from panic in an OnLoaded handler: %v\n%s\n", r, debug.Stack())
					}
				}()
				handler(ctx)
			}()
		}
	})
	return done
}
