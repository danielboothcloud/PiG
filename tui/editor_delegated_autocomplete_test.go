package tui

import "testing"

// Pi's CustomEditor subclass reaches its base editor through super.handleInput, and the base opens the "/" menu in the Promise jobs after the read, then calls tui.requestRender, which renders the subclass again (editor.ts requestAutocomplete, applyAutocompleteSuggestions). A delegated component's base is this editor: the request a base key made must still run once the base call returned, and the menu it opens must ask the component, which renders in its extension's process, to render again.
func TestDelegatedBaseAutocompleteOpensAndAsksTheComponentToRender(t *testing.T) {
	e := NewEditor()
	e.Focused = true
	var queued []func()
	e.SetAsyncApply(func(fn func()) { queued = append(queued, fn) })
	// The interactive host owns the request tasks, which makes each key's request wait for the end of its read (FlushAutocomplete).
	e.SetAutocompleteTaskOwner(t.Context(), func(fn func()) { fn() }, func(err error) { t.Error(err) })
	e.SetAutocomplete(NewCombinedProvider([]SlashCommand{{Name: "model", Description: "Select model"}}, t.TempDir(), ""))
	remote := &recordingRemote{}
	e.SetDelegatedRemote(remote)

	e.AsBase(func() { e.HandleInput("/") })
	before := remote.changes
	for len(queued) > 0 {
		fn := queued[0]
		queued = queued[1:]
		fn()
	}
	var open bool
	e.AsBase(func() { open = e.AutocompleteOpen() })
	if !open {
		t.Fatal("the base editor's slash menu did not open after the base key")
	}
	if remote.changes == before {
		t.Fatal("the delegated component was not asked to render the menu its base opened")
	}
}
