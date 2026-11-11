package subprocess

import (
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

// Pi's setCustomEditorComponent hands the rebuilt provider to the component's setAutocompleteProvider. A delegated component (a CustomEditor subclass in the Go, Rust, Python or Lisp SDK) inherits that setter from its base, the host's editor, which the rebuild already updated. The bridge must not send it the "changed" notice, which those SDKs cannot answer: the failed rebuild crashed the host with "autocomplete provider is no longer available". The proxy has no connection, so any request would fail.
func TestSyncAutocompleteLeavesADelegatedEditorToItsBase(t *testing.T) {
	bridge := NewUIBridge(func() {})
	bridge.editor = &editorProxy{ext: "edprobe", key: "editor-1", delegated: true, bound: make(chan struct{})}
	provider := &extension.AutocompleteProvider{}
	if err := bridge.SyncAutocomplete(t.Context(), provider); err != nil {
		t.Fatalf("SyncAutocomplete for a delegated editor = %v; want nil", err)
	}
	bridge.mu.RLock()
	defer bridge.mu.RUnlock()
	if len(bridge.autocompleteReferences) != 0 {
		t.Fatalf("SyncAutocomplete retained %d provider references for a delegated editor; want none", len(bridge.autocompleteReferences))
	}
}
