package codingagent

// Ports packages/coding-agent/src/modes/interactive/interactive-mode.ts (handleOpenExternalEditor, toggleToolOutputExpansion).

import (
	"context"
	"errors"

	"github.com/MichaelKinsy/PiG/tui"
)

func (m *InteractiveMode) openExternalEditor(ctx context.Context) {
	m.openExternalEditorBuffer(ctx, m.externalEditorCommand(), m.editor.GetExpandedText(), m.editor.SetText, m.showStatus)
}

// openExternalEditorBuffer hands terminal input and output to the child off the UI loop. Completion returns to the owner loop and only a successful edit replaces the caller's buffer.
func (m *InteractiveMode) openExternalEditorBuffer(ctx context.Context, command, initial string, apply func(string), report func(message string)) {
	if !m.releaseTerminal() {
		return
	}
	ownerCtx := m.runCtx
	if ownerCtx == nil {
		ownerCtx = ctx
	}
	m.backgroundTasks.Go(func() {
		result, runErr := OpenExternalEditor(ctx, initial, command)
		m.runOnMain(ownerCtx, func() {
			m.reclaimTerminal(ownerCtx, func() {
				if runErr == nil && ctx.Err() == nil {
					apply(result)
				}
				var failure *ExternalEditorFailure
				if errors.As(runErr, &failure) && ctx.Err() == nil && report != nil {
					report(failure.Message)
				}
			})
		})
	})
}

// releaseTerminal runs on the owner loop. It stops rendering and terminal input and restores cooked mode, so a child process can own the terminal. It reports false when the terminal is already handed off.
func (m *InteractiveMode) releaseTerminal() bool {
	if m.externalEditorActive {
		return false
	}
	m.externalEditorActive = true
	if m.surface != nil {
		// pig additive (D91): the frontend session keeps its frames while
		// the child owns the terminal and learns of the handoff.
		m.surface.Suspend()
	} else {
		m.tuiInst.Stop()
	}
	if m.inputReader != nil {
		m.inputReader.pause()
	}
	if m.rawRestore != nil {
		m.rawRestore()
		m.rawRestore = nil
		m.rawDrain = nil
	}
	return true
}

// reclaimTerminal runs on the owner loop after releaseTerminal's child exited. It re-enters raw mode, runs settle, redraws the whole screen, and replays input that arrived during the handoff.
func (m *InteractiveMode) reclaimTerminal(ownerCtx context.Context, settle func()) {
	m.externalEditorActive = false
	if ownerCtx.Err() != nil {
		return
	}
	restore, drain, rawErr := tui.EnterRawModeWithDrain()
	if rawErr != nil {
		m.exitIfDeadTerminal(rawErr)
		m.failInputLoop(rawErr)
		return
	}
	m.rawRestore = restore
	m.rawDrain = drain
	if settle != nil {
		settle()
	}
	if m.surface != nil {
		// pig additive (D91): the frame after the handoff carries
		// only what changed, such as the edited text.
		m.surface.Resume()
	} else {
		m.tuiInst.Start()
		m.tuiInst.RepaintAll()
	}
	if m.inputReader != nil {
		m.inputReader.resume()
	}
	if resume := m.externalEditorInput; resume != nil {
		m.externalEditorInput = nil
		resume()
	}
}

// toggleAllTools flips the global tool expansion state for the whole
// transcript. Mirrors upstream toggleToolOutputExpansion().
func (m *InteractiveMode) toggleAllTools() {
	m.toolMu.Lock()
	expanded := !m.toolsExpanded
	m.toolMu.Unlock()
	m.setAllToolsExpanded(expanded)
}

// setAllToolsExpanded applies changed expansion state to the startup header and every mounted expandable child, not detached tracking-list entries. Extension UI calls share this owner-loop path with the default editor.
// upstream: packages/coding-agent/src/modes/interactive/interactive-mode.ts:setToolsExpanded
func (m *InteractiveMode) setAllToolsExpanded(expanded bool) {
	m.toolMu.Lock()
	if m.toolsExpanded == expanded {
		m.toolMu.Unlock()
		return
	}
	m.toolsExpanded = expanded
	m.builtInHeaderExpanded = expanded
	notify := m.toolsExpandedChanged
	m.toolMu.Unlock()
	// ctx.ui.getToolsExpanded() is replicated state: every extension runtime learns the toggle when it happens.
	if notify != nil {
		notify()
	}
	for _, container := range []*tui.Container{m.loadedResourcesContainer, m.chatContainer} {
		if container == nil {
			continue
		}
		for _, child := range container.Children() {
			if expandable, ok := child.(interface{ SetExpanded(bool) }); ok {
				expandable.SetExpanded(expanded)
			}
		}
	}
	m.showStatus("Tool output: " + map[bool]string{true: "expanded", false: "collapsed"}[expanded])
}

// rebuildChatFromSession rebuilds the visible conversation from the active
// path-to-leaf messages.

// externalEditorCommand is the settings' externalEditor command (settings-manager.ts getExternalEditorCommand), "" when no settings are loaded.
func (m *InteractiveMode) externalEditorCommand() string {
	if m.opts.SettingsManager == nil {
		return ""
	}
	return m.opts.SettingsManager.GetExternalEditorCommand()
}
