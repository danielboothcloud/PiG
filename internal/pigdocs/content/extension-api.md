# Extension API

This document is the canonical reference for the wire protocol and host API surface that pig exposes to extensions. The same API is bridged by all official subprocess SDKs (Go in `extensions/sdk`, Rust in `extensions/sdk-rs`, Python in `extensions/sdk-py`). Node extensions use Pi's canonical TypeScript API through PiG's compatibility runtime. `extensions/sdk-ts` adds declarations for PiG-only calls. Method names below are normalized; SDK bindings use language-idiomatic spellings.

## Wire protocol (subprocess, current unversioned shape)

The host and extension exchange length-prefixed JSON envelopes over a Unix domain socket. Each frame is a 4-byte big-endian unsigned length followed by exactly that many bytes of one JSON object; a single frame is capped at `MaxFrameSize` (128 MB), a constant the host and every SDK share. The host binds the listening socket and passes the path via `PIG_EXT_SOCKET` (or `PIG_EXT_SOCKET_<EXT_NAME>` for each contained extension in a packed cell). Every envelope carries a `type`, an optional `id` for request/response correlation, and a body field named after the type:

```json
{ "type": "request", "id": "r-7", "request": { ... } }
```

So a `register` envelope nests its body under `register`, a `ready` under `ready`, a `call` under `call`, and so on. You do not frame or parse this yourself when using an SDK; the SDK owns the length prefix and the envelope shape.

### Message types

| Direction | Type | Purpose |
|---|---|---|
| ext → host | `register` | Initial handshake declaring capabilities. |
| host → ext | `ready` | Acks register, ships session state and host context. |
| host → ext | `request` | Tool call, command invocation, event, or render. Expects `response`. |
| host → ext | `notify` | Fire-and-forget (e.g. lifecycle hints). |
| host → ext | `cancel` | Cancel an in-flight `request` by ID. |
| ext → host | `response` | Reply to a `request`. |
| ext → host | `call` | Extension invokes a host method. Host replies with `call_result`. |
| host → ext | `call_result` | Reply to an extension `call`. |
| ext → host | `widget_push` | Push pre-rendered widget lines to the host's widget cache. |
| host → ext | `shutdown` | Graceful shutdown. Extension flushes pending IO and exits. |

Cancellation is cooperative. The host emits a `cancel` envelope when the user aborts, when a new turn starts, or on shutdown; the extension is expected to short-circuit the corresponding handler and reply with a typed cancelled response.

A host `request` is awaited: the Go SDK already executes its handler on a dedicated goroutine and sends `response` only after the handler returns. Keep awaited TypeScript work inside that handler and return its error. Do not start another goroutine and return early unless the upstream operation is deliberately unawaited and the extension owns an independent cancellation/error/shutdown lifecycle. The request-scoped `Context` is cancelled when the handler returns. See [Extensions: TypeScript async](extensions.md#typescript-async-is-a-contract-not-a-goroutine-instruction).

### Register payload (sketch)

```json
{
  "name": "my-extension",
  "tools":     [{ "name": "x", "description": "...", "parameters": {...JSON schema...} }],
  "commands":  [{ "name": "x", "description": "..." }],
  "shortcuts": [{ "key": "ctrl+shift+x", "description": "..." }],
  "flags":     [{ "name": "verbose", "type": "boolean", "default": false }],
  "providers": [{ "name": "my-provider", "config": { ... } }],
  "renderers": [{ "customType": "my.entry" }],
  "events":    ["session_start", "session_shutdown", "session_info_changed", "tool_result", "my.custom"]
}
```

The host rejects empty names, duplicate names within any list, schemas that do not parse as JSON, and provider configs that are not JSON-serializable.

## Built-in Anthropic transport

Use the raw API factory when a registered provider wraps the built-in transport. Do not call the registry again for that same provider: it dispatches back to your own `streamSimple`.

```go
api := sdk.AnthropicMessagesAPI(ctx)
stream, err := api.StreamSimple(model, transcript, sdk.ProviderStreamOptions{
    Signal: signal,
    Values: map[string]any{"apiKey": key, "fetch": sdk.ModelFetchFunc(fetch)},
    OnPayload: onPayload,
    OnResponse: onResponse,
})
```

`Stream` and `StreamSimple` return immediately. Read provider failures from the stream's error event or `Result()`. The `modelStream` wire call sets `apiRequest: true` to bypass registered callbacks and uses the supplied model endpoint and credential. Cancellation uses `Signal` and the `cancelModelStream` call; a `Signal` already cancelled ends the stream as aborted and sends nothing. `OnProviderStreamEvent` is forwarded as well.

Python: `pig_sdk.anthropic_messages_api(ctx).stream_simple(model, transcript, pig_sdk.ProviderStreamOptions(values=..., signal=..., on_payload=...))`. Node: Pi's own `anthropicMessagesApi()` from `@earendil-works/pi-ai/compat`. Rust and Lisp do not have it yet.

A provider registered with `streamSimple` receives the caller's request callbacks in its options, as Pi's composer passes them: Go `options["onPayload"]` (`sdk.OnPayloadFunc`), `"onResponse"` (`sdk.OnResponseFunc`), `"onProviderStreamEvent"` (`sdk.OnProviderStreamEventFunc`) and `"fetch"` (`sdk.ModelFetchFunc`); Node and Python get functions under the same keys. Compose the caller's `onPayload` before your own change and pass the rest through to the transport, or other extensions' request hooks stop working. They call back into the host and work only while the provider request runs. Rust and Lisp providers do not receive them yet.

## Host API (extension → host)

These are the call names the host accepts. SDK bindings wrap each into a method on the per-call `Context`. Cancellation is propagated automatically - when a handler's `Context` is cancelled, all in-flight host calls made by that handler are also cancelled.

### Context introspection

- `getFlag(name)` - value of a host or extension flag.
- `getActiveTools()` - names of tools currently active for the model.
- `getAllTools()` - every tool in the session's registry, active or not, as Pi's `ToolInfo`: `name`, `description`, `parameters`, `promptGuidelines` (when set), `exposure`, `namespace` and `annotations` (when set), `reachesTools` (when the tool defines it) and `sourceInfo`. `reachesTools(input)` answers `"all"` or the words of the input that may name the tools a call reaches; in every SDK it runs the tool's hook through the host, wherever the tool is defined. The Go, Rust and Python SDKs' host call also returns PiG's per-tool `source`, which the Go SDK keeps as the deprecated `ToolInfo.Source`.
- `getCommands()` - extension commands, prompt templates and skills, as Pi's `SlashCommandInfo`: `name`, `description`, `source` and `sourceInfo`.
- `getThinkingLevel()` - `"none" | "low" | "medium" | "high" | "xhigh"`.
- `setThinkingLevel(level)` - switch reasoning effort if the active model supports it.
- `getContextUsage()` - `{ tokens, contextWindow, percent }`, or absent when no usable model window exists. Counts can be null after compaction.
- `getSystemPrompt()` - current system prompt text.
- `getSessionName()` / `setSessionName(name)` - read or change the display name. Name changes emit `session_info_changed` to extensions that registered that event.
- `getSessionID()`, `getSessionFile()` - session JSONL on disk.
- `getLeafID()`, `getBranch()`, `getEntries()` - tree navigation primitives.

### Session control

- `sendMessage(customMessageRef, options)` - inject a structured message into the session.
- `sendUserMessage(content, options)` - synthesize a user turn. `deliverAs` is `"steer" | "followUp" | "nextTurn"`.
- `appendEntry(customType, data)` - append a custom JSONL entry without invoking the model.
- `setLabel(entryID, label)` - set a small display label on an existing entry.
- `setActiveTools(tools)` - pin the tool set for the next model turn.
- `refreshTools()` - re-resolve enabled tools from settings + extensions.
- `setModel(spec)` - switch the active model. `spec` is `provider/model` (preferred) or bare model ID (legacy).
- `getModelAuth(provider, model)` - read credentials/headers the host would send.
- `complete(model, prompt, options)` - request a one-shot completion through the host's provider stack (typed unsupported error if not wired in this build).
- `isIdle()`, `hasPendingMessages()`, `waitForIdle()`, `abort()`, `shutdown()`, `reload()`, `compact(options)`.
- `newSession(opts)`, `fork(entryID, opts)`, `navigateTree(targetID, opts)`, `switchSession(path, opts)` - return `{cancelled: bool}` so the extension can detect user dismissal.

### Host calls that run your own handlers before they return

Some host calls emit an event. The host sends that event to every extension that subscribed to it, the calling extension included, and answers the call only after the handler ran. Pi does the same: a Pi extension runs in Pi's process, and these calls run the handlers inside the call. The behavior is the same in every SDK (Go, Rust, Python, Lisp and Node) and in isolated, packed and fused extensions.

While a call waits for its answer, the SDK keeps reading host messages and runs the event handler on another goroutine (a thread or task in the other SDKs). The handler therefore runs before the call returns. If the caller holds a lock across the call and the handler takes the same lock, the handler waits for the caller, the host waits for the handler, and the caller waits for the host. This deadlock has no timeout.

| Host call (Go method) | Events the calling extension can receive before the call returns |
|---|---|
| `setThinkingLevel` (`SetThinkingLevel`) | `thinking_level_select`, when the effective level changes. |
| `setModel` (`SetModel`) | `thinking_level_select`, when the new model changes the effective level, then `model_select`. |
| `setSessionName` (`SetSessionName`) | `session_info_changed`. |
| `newSession` (`NewSession`), `switchSession` (`SwitchSession`) | `session_before_switch`, then `session_shutdown` unless a handler cancels. |
| `fork` (`Fork`) | `session_before_fork`, then `session_shutdown` unless a handler cancels or the fork fails. |
| `navigateTree` (`NavigateTree`) | `session_before_tree`, then `session_tree` unless a handler cancels. |
| `reload` (`Reload`) | `session_shutdown`. |
| `sendUserMessage` (`SendUserMessage`) | `input` and `before_agent_start`, and `agent_start` in print and JSON mode. See the mode notes below. |
| `sendMessage` with `triggerTurn` on an idle session (`SendMessage`, `SendCustomMessage`) | `before_agent_start` in RPC mode. See the mode notes below. |
| `executeTool` (`ExecuteTool`) | `tool_execution_start`, `tool_call`, `tool_execution_update`, `tool_result` and `tool_execution_end` for the nested call, and the nested tool itself when the calling extension registered it. |
| `pi.events` emit (`Events().Emit`) | The calling extension's own listeners on the channel. |

- For `thinking_level_select` and `session_info_changed`, Pi does not await the handlers. The host waits until the first subscribed handler, in extension load order, returns or makes a host call. A handler of a later extension can run after the call returns. For the other events, the host waits until every handler returns.
- After `newSession`, `switchSession`, `fork` and `reload`, the replacement session's `session_start` goes to the replacement extension instance (a new process for a subprocess extension), not to the caller.
- Callbacks that you pass to a call also run before it returns, on another goroutine: `ExecuteToolOptions.OnUpdate`, the `setup` callback of `NewSession`, and the `withSession` callback of `NewSession`, `Fork` and `SwitchSession`. A model call (`complete`, `stream`) whose provider the calling extension registered runs that provider's operations in the calling extension. Do not hold a lock across these calls that the callback or provider takes.
- Mode notes: the rows from `setThinkingLevel` to `reload`, and the `executeTool` and `pi.events` rows, are the same in interactive, print, JSON and RPC mode. `sendUserMessage` and `sendMessage` with `triggerTurn` differ by mode. Interactive mode answers both calls before `input` and `before_agent_start` run. RPC mode answers `sendUserMessage` after the `input` and `before_agent_start` handlers, and `sendMessage` after the `before_agent_start` handlers. Print and JSON mode answer `sendUserMessage` after the `input`, `before_agent_start` and `agent_start` handlers, and answer `sendMessage` before `before_agent_start`. Treat both calls as calls that run your handlers in every mode.
- Other calls send no event back to the calling extension before they return, for example `appendEntry`, `setLabel`, `setActiveTools`, `refreshTools`, `exec`, `compact` and the `ui.*` notifications. `compact` starts compaction and returns; `session_before_compact`, `session_compact` and `session_compact_failed` arrive after it returns. Holding a lock across these calls does not deadlock on an event of this extension.

Release the lock before the call, or record what you need under the lock, make the call without the lock, and apply the result after the call returns:

```go
// Deadlock: the thinking_level_select handler takes s.mu while SetThinkingLevel waits for that handler.
s.mu.Lock()
s.wanted = level
ctx.SetThinkingLevel(level)
s.mu.Unlock()

// Safe: the handler can take s.mu while the call waits.
s.mu.Lock()
s.wanted = level
s.mu.Unlock()
ctx.SetThinkingLevel(level)
```

### UI bridge

- `ui.notify(message, level)` - toast in the footer. `level` is `info | warn | error`.
- `ui.setStatus(key, text)` - pin a small status line keyed by `key`. In the interactive footer empty text clears; in RPC mode it is written as given, and `ClearStatus(key)` (Go), `clear_status(key)` (Python, Rust) or `setStatus(key, undefined)` (Node) is the request that carries no text.
- `ui.setLogin(definition)` - validate and install a native login in the shared header slot.
- `ui.setWorkingMessage(message)`, `ui.setWorkingVisible(bool)`, `ui.setWorkingIndicator(options)` - control the bordered loader.
- `ui.setHiddenThinkingLabel(label)` - label shown when reasoning is hidden.
- `ui.setTitle(title)` - terminal window title.
- `ui.select(title, options)` - modal single-choice picker.
- `ui.confirm(title, message)` - yes/no dialog.
- `ui.input(title, placeholder)` - single-line input.
- `ui.editor(title, prefill)` - multi-line editor.
- `getEditorText()` / `setEditorText(text)` / `pasteToEditor(text)`.

All UI calls return after the user dismisses the dialog; pair them with `Context.Done()` if you want responsiveness to cancellation.

### Focused custom components

`ui.custom` runs a component in the extension process while PiG owns the focus
shell. The extension sends replaceable line snapshots. PiG sends ordered input
events only while that overlay owns terminal focus.

An inline component (no `overlay: true`) can pass `blocked: { kind, message }` to report the
terminal's program status as blocked while it is visible.

Go components implement `RemoteComponent`. Implement
`RemoteComponentInvalidator` when a timer changes component state. Rust
components implement `set_invalidate` on `RemoteComponent`. Python components
can implement `set_invalidate(callback)`. Node component factories call the
supplied `tui.requestRender()` function.

Call the invalidation callback after state changes. PiG coalesces timer requests
to one pending frame and limits timer rendering to the 16 ms TUI interval.
Input-driven rendering remains immediate. The TUI render loop reads only the
latest host-side cache and performs no extension IPC.

One extension dialog or overlay owns terminal focus at a time. Waiting calls remain off
the TUI loop and respond to cancellation. The SDK detaches invalidation before
it calls `dispose`. A component callback must return promptly. A callback that
does not stop by the cleanup deadline produces an error and is not disposed
concurrently.

### Widgets, headers, footers

- `setWidget(key, content, options)` - show a widget above the editor. `content` may be string, lines, or a factory.
- `setHeader(lines)`, `setFooter(lines)` - install custom layout lines.
- `widget_push` (envelope, not a call) - push pre-rendered lines from the extension into the host cache, keyed by `widget` + `frameID`. Use for animated or expensive widgets. Include `width`, the terminal width the lines were rendered at; the Node runtime sets it. The host never paints a frame rendered for another width, and the extension re-renders on `width_change`. `ui.setHeader`/`ui.setFooter` lines take the same optional `width`.

> **Fit header and footer lines to `width()` yourself.**
>
> Upstream Pi takes a component factory here, so its `render(width)` is called on
> every frame and always sees the current width. A PiG extension is a subprocess
> and cannot hand over a live component, so it sends **static strings** instead.
> Nothing downstream re-measures them.
>
> As in Pi, a line wider than the terminal is not corrected by the host. When it
> reaches a differential render, PiG writes `pi-tui-crash.log`, restores the
> terminal, and exits with `Rendered line N exceeds terminal width`, exactly as
> Pi does for a component that does not truncate its output.
>
> Build the line as segments ordered most to least valuable, then keep only the
> ones that fit:
>
> ```go
> width := ctx.Width() // 0 until the host reports a size; keep everything then
> line := fitSegments([]string{
>     cwd,                    // always keep
>     "Context: " + pct,
>     "Model: " + model,
>     "Think: " + level,      // first to go in a narrow pane
> }, "  │ ", width)
> ```
>
> Measure **display columns, not bytes**: `len()` counts SGR escapes, so a styled
> segment looks far wider than it renders and the line collapses to one field.
> Strip escapes before measuring, and count East Asian Wide and Fullwidth runes as
> two columns.
>
> `width()` is current at the moment you build the line. There is no width-change
> callback, so a footer pushed before a resize stays sized for the old width until
> something else triggers a re-push; re-push on an event you already handle
> (`turn_end` is usually enough).

### Native login

`ui.setLogin` accepts one strict current unversioned definition. It contains
`brand`, `hero`, `mascot`, `palette`, `name`, `description`, and `tagline`.
The grids have exact dimensions:

| Grid | Width | Height |
|---|---:|---:|
| `brand` | 41 | 5 |
| `hero` | 32 | 14 |
| `mascot` | 16 | 14 |

Use ASCII symbols in each grid. `.` means transparent. Map each other symbol to
one `#RRGGBB` palette color. Palette keys must be one printable non-space ASCII
character. The palette can contain at most 32 colors, and each color must be
used.

PiG rejects unknown or duplicate fields, wrong grid sizes, non-ASCII grid data,
missing or unused symbols, invalid colors, and invalid text. All text must be
non-empty valid UTF-8 without control or line-separator characters. The display
width limits are 24 columns for `name`, 48 for `description`, and 76 for
`tagline`. Name plus description and spacing are limited to 80 columns.

`setLogin` and `setHeader` share one slot. The last successful call wins. Clear
the header to restore Stock Pig's text header. A rejected definition returns a host
error and keeps the current header. Quiet startup keeps the header hidden.
Terminals below 66 columns show compact text without art. PiG caches validated
render data, so rendering performs no extension IPC.

Minimal calls:

```go
err := ctx.SetLogin(sdk.LoginDefinition{Brand: brand, Hero: hero, Mascot: mascot, Palette: palette, Name: "Example Bot", Description: "Custom coding agent", Tagline: "Build with care."})
```

```rust
ctx.set_login(&LoginDefinition { brand, hero, mascot, palette, name: "Example Bot".into(), description: "Custom coding agent".into(), tagline: "Build with care.".into() })?;
```

```python
ctx.set_login(LoginDefinition(brand=brand, hero=hero, mascot=mascot, palette=palette, name="Example Bot", description="Custom coding agent", tagline="Build with care."))
```

```js
await ctx.ui.setLogin({ brand, hero, mascot, palette, name: "Example Bot", description: "Custom coding agent", tagline: "Build with care." });
```

Preview one extension with `pig extension preview-login <path>`. The command
fails for zero or multiple resolved extensions, a failed `session_start`, a
missing login, or an invalid definition. Use `/reload` after an edit. A login
remains an extension Resource. Activate it through a Piglet `extensions` entry,
user extension policy, or `pig -e <path>`. Verify activation by starting or
reloading through that path and checking the identity name. Keep product
identities outside Stock Pig and select them through an ordinary Piglet
extension entry.

### Themes

- `getAllThemes()` - list of `ThemeMeta`.
- `getTheme(name)` - full theme object.
- `setTheme(name)` - applies and returns `(applied, message)`.

### Autocomplete, terminal input, editor component

- `addAutocompleteProvider(factory)` - install a custom completion source for the editor.
- `onTerminalInput(handler)` - receive raw terminal input before normal handling; return `{ consume: true }` to swallow a chunk or `{ data }` to replace it; returns an unsubscribe. Handlers see chunks in the order they were typed. Input typed after a chunk, including Ctrl+C, waits for your verdict, so answer promptly. A listener can consume or rewrite Ctrl+C before normal handling. PiG waits off the TUI loop and cancels the request when the mode shuts down.
- `setEditorComponent(factory)` / `getEditorComponent()` - replace the editor widget.
  - Go: `Context.SetEditorComponent(factory sdk.EditorFactory) error` and `Context.GetEditorComponent() any` (a non-nil result is an `sdk.EditorFactory`). Rust: `Context::set_editor_component(factory)` and `Context::get_editor_component() -> Option<EditorFactory>`. Python: `Context.set_editor_component(factory)` and `Context.get_editor_component()`.
  - The factory receives the host's default editor, which the component embeds as its `super`. A nil or `undefined` factory, or the SDK's clear call, restores the host's editor.
  - The base carries what Pi's `CustomEditor` keeps as public fields. Set `onEscape`, `onCtrlD`, `onPasteImage`, `onExtensionShortcut` or an `actionHandlers` entry (`onAction(action, handler)`) on it and the component's handler replaces the host's for that action: `handleInput` runs the extension shortcut first, then, as Pi's `CustomEditor` does, the paste-image, interrupt (only while no autocomplete shows) and exit (only on an empty editor) actions and the other app actions. Go: `Editor.SetOnEscape`, `SetOnCtrlD`, `SetOnPasteImage`, `SetOnExtensionShortcut`, `OnAction` and the live `ActionHandlers()` map. Rust: `EditorBase::set_on_escape`, `set_on_ctrl_d`, `set_on_paste_image`, `set_on_extension_shortcut`, `on_action` and `action_handlers()`. Python: the `on_escape`, `on_ctrl_d`, `on_paste_image`, `on_extension_shortcut` and `action_handlers` attributes of `EditorBase`, and `on_action`.
  - The base also reaches the members `CustomEditor` inherits from `Editor`: `borderColor(text)`, `disableSubmit`, `focused`, `getPaddingX()`, `getAutocompleteMaxVisible()`, `setAutocompleteProvider(CombinedAutocompleteProvider(commands, basePath))` (a `kit.Autocomplete`), and the host's `onSubmit(text)` and `onChange(text)` callbacks, which it calls.
  - The host calls the component's `setWorkingStatusIndicator(status)` when an editor that embeds the working status gets a new status, a new frame of it, or none, and its `invalidate()` when the theme changes. Go: override `Editor.SetWorkingStatusIndicator` and `Editor.Invalidate`. Rust: `EditorComponent::set_working_status_indicator` and `invalidate`. Python: the methods of `EditorComponent`.
  - The getter returns the factory this extension installed last, in every runtime. It returns nothing before an install, after the editor is cleared, with no UI, and in RPC mode, where installing does nothing, as in Pi. It never returns another extension's factory.
- `setToolsExpanded(bool)` / `getToolsExpanded()`.

### Terminal string units

Terminal-input callbacks preserve JavaScript UTF-16 units, including lone surrogate halves. The wire represents an unmatched unit with a standard JSON `\u` escape. Do not join input chunks before returning a verdict.

Go callback strings use WTF-8 for unmatched units. Use `github.com/MichaelKinsy/PiG/extensions/sdk/json` for your own serialization of those strings. Python and Node retain unmatched units in their native strings. Rust terminal and focused-component input callbacks receive `&JsString`, and verdict `data` is `Option<JsString>`. Read `as_units()` or construct `JsString::from_units(...)` for lossless handling. `to_string_lossy()` is an explicit display conversion. Rust editor getters return `JsString`; setters accept ordinary strings or `JsString`. Serialize Rust `JsString` directly, not through `serde_json::Value`.

### Process & filesystem

- `exec(command, args, options)` - run a command with stdin/stdout/stderr piped back. Subject to permissions.
- `configHome()` - pig's config root (`~/.pig` or `PIG_HOME`).
- `cwd()` - working directory of the host session. The built-in tools (`read`, `write`, `edit`, `ls`, `find`, `grep`, `bash`) resolve relative paths against it, as Pi's resolve them against `ctx.cwd` and not `process.cwd()`.
- `width()` - terminal columns visible to the host.
- `model()` - current model identifier.
- `toolCallID()` - only meaningful inside a tool handler.

### Writing terminal control strings

A Pi extension writes an OSC notification or a bell to `process.stdout`, the terminal Pi's TUI draws on. A PiG extension's stdout is its host wire, and a write to `/dev/tty` can land inside a frame, so it asks the host to write the string between TUI frames (D237):

```go
// Pi: process.stdout.write("\x1b]777;notify;Pi;Finished\x07")
if err := ctx.WriteTerminal("\x1b]777;notify;Pi;Finished\x07"); err != nil {
	// print, JSON or RPC mode, or data the host refused; nothing was written
}
```

- Go `ctx.WriteTerminal(data)`; Rust and Python `write_terminal(data)`; Lisp `ctx-write-terminal`; Node `await ctx.ui.writeTerminal(data)`. The wire call is `ui.writeTerminal` with `{"data": string}`.
- The host accepts only strings that neither print nor move the cursor: BEL, OSC strings ended by BEL or `ESC \` (no control bytes inside, no OSC 8 hyperlink), and tmux passthroughs (`ESC P tmux; ... ESC \`, inner ESC doubled) of those, at most 64 KiB. Anything else is refused whole, and the error names the offending byte.
- The interactive UI writes the string on its owner loop, the path `ctx.Bell()` takes. The call returns once the write is queued.
- Print, JSON and RPC mode write nothing and return an error that names the mode and what stdout carries there. `ctx.Bell()` is unchanged: it does nothing and returns no error there.

### Shared environment with Node extensions and providers

Pi's extensions and Node providers run in one process and share one `process.env`. In Pig each extension has its own process, so a native extension changes a value that Node extensions and Node providers read at request time through the host (D234):

```go
// Pi: process.env.PI_CURSOR_EXPOSE_BUILTIN_TOOLS = "1"
if err := ctx.SetSharedEnv("PI_CURSOR_EXPOSE_BUILTIN_TOOLS", "1"); err != nil {
	return err
}
// Pi: delete process.env.PI_CURSOR_EXPOSE_BUILTIN_TOOLS
if err := ctx.UnsetSharedEnv("PI_CURSOR_EXPOSE_BUILTIN_TOOLS"); err != nil {
	return err
}
```

- Go `ctx.SetSharedEnv(key, value)` and `ctx.UnsetSharedEnv(key)`; Rust and Python `set_shared_env` and `unset_shared_env`; Lisp `ctx-set-shared-env` and `ctx-unset-shared-env`. The wire call is `env.setShared` with `{"key": string, "value": string | null}`; `null` deletes the key.
- The call returns after the host has queued the change to every running Node realm, isolated or packed. The Node runtime applies it to `process.env` on receipt, so the next request handler in any Node realm, including a provider's `streamSimple`, reads the new value. Node code needs no change.
- A Node realm that starts later inherits the values in its spawn environment. Unset deletes the key, including a value inherited from pig's own environment.
- The environment of the pig process and of native extension processes does not change. `os.Getenv` in the calling extension still returns its own value. A Node extension's own `process.env` writes stay in its process.
- The host rejects an empty key, a key that contains `=` or NUL, a value that contains NUL, and keys that start with `PIG_EXT_` or `PIG_HARNESS_` (case-insensitive). A rejected call changes nothing.

### Providers

Provider-object access has documented 0.3.x known gaps (D78, owner decision 2026-09-28). Go, Rust and Python cannot yet retrieve every builtin/composed raw Provider. Foreign registered-configuration data is a snapshot, not an author-held live object. Implemented callable handles do not preserve arbitrary property identity or mutation. Same-process Node object semantics must remain Pi-exact.

Partial-message observation across the process boundary is D82: retained snapshots do not acquire later producer revisions. The strict RPC33 comparison keeps its raw failure. Every Node extension, including strict isolation and exact standalones, shares one `pi.events` bus; a listener in another process receives the original payload by reference. D83 names the remaining cross-process boundaries: timing order of independent tasks, microtask order beyond a listener's first post-await continuation, cross-process reference cycles, brand checks and `util.types.isProxy` on foreign objects, an empty placeholder from `util.inspect` with `customInspect: false`, the Proxy wrapper from `showProxy: true`, and, on Node 26.0.0 and later, `Proxy(...)` around a non-extensible foreign payload in `util.inspect`. These known gaps do not waive event order, callback completion, cancellation, final results, persistence or cleanup. See [Divergences](divergences.md).

Extensions may register additional inference providers. The registration payload is opaque to the host except for the name. Providers registered by an extension are torn down when the extension shuts down, fails to register on reload, or its quarantined packed cell fissions. Names registered by a replacement extension at reload are preserved across the swap.

### Cancellation contract

- `Context.Done()` closes when the host sends `cancel` for that request, when the parent session shuts down, or when the extension's connection drops.
- `Context.Err()` reports `cancelled`, `deadline`, or `closed`.
- Handlers should periodically check `Done()` between expensive steps. Host calls made from a cancelled context return a typed cancellation error promptly.

### Cancelling model calls (Go)

Pi aborts `ctx.modelRegistry.complete(model, context, { signal })` and `stream(...)` through `options.signal`. The Go SDK takes a `context.Context` instead (D235):

```go
callCtx, cancel := context.WithCancel(context.Background())
defer cancel()
message := ctx.ModelRegistry().CompleteContext(callCtx, model, request, nil)
// message["stopReason"] == "aborted" after cancel()
```

- `ModelRegistry.StreamContext`, `StreamSimpleContext` and `CompleteContext`, and the same methods on `*sdk.ModelRuntime`, take the context as the first argument. The other arguments are those of `Stream`, `StreamSimple` and `Complete`.
- Cancelling the context sends `cancelModelStream` for the stream. The host cancels the provider request, closes its HTTP connection, and the stream ends with the provider's aborted message (`stopReason` `"aborted"`), as Pi's providers end an aborted signal. A context that is already cancelled aborts the request as soon as it starts.
- `Stream`, `StreamSimple` and `Complete` are unchanged and cannot be cancelled except through the handler's own request.
- The cancelling context is independent of the `sdk.Context` that makes the call. Work that outlives a handler still calls through the `sdk.Context` from `OnLoaded`, and passes its own cancellable context, for example one derived from `context.Background()`.

### Reading the launch and exiting with a status (Go)

A Pi extension runs in Pi's process. It reads `process.argv` and `process.stdin.isTTY`, and it stops startup with `process.exit(code)`. A Go extension runs in its own process, so it asks the host (D236):

```go
ext.OnSessionStart(func(ctx sdk.Context, _ map[string]any) (any, error) {
	launch, err := ctx.HostLaunch()
	if err != nil {
		return nil, err
	}
	// launch.Argv is process.argv.slice(2): ["--no-approve", "--models", "a,b", "-e", "./ext"]
	if slices.Contains(launch.Argv, "--no-approve") && launch.StdinIsTTY {
		// ...
	}
	if misconfigured {
		return nil, ctx.ShutdownWithCode(1) // Pi: process.exit(1)
	}
	return nil, nil
})
```

- `ctx.HostLaunch()` returns `Argv` (pig's arguments after the executable, in order and unchanged, including a secret passed as an argument, as Pi's `process.argv` does), `Mode` (`tui`, `rpc`, `json` or `print`, the value of `ctx.Mode()`), and `StdinIsTTY` and `StdoutIsTTY` (pig's stdin and stdout when it started). It returns nothing from the environment. `os.Args` in a Go extension is its own process's.
- `ctx.ShutdownWithCode(code)` ends pig with exit status `code` (0 to 255). In interactive and RPC mode it behaves as `ctx.Shutdown()` with that status. In print and JSON mode, where `ctx.Shutdown()` does nothing, it aborts a run in progress, sends no further prompt and writes no result. Called in `session_start`, it stops before the command-line prompt is sent in every mode. `session_shutdown` handlers still run. Only a termination signal's status replaces `code`. It returns an error for a code outside 0 to 255.
- Node extensions keep Pi's `process.argv`, `process.stdin.isTTY` and `process.stdout.isTTY`. The Rust, Python and Lisp SDKs do not have these calls yet.

### Relaunching pig with other arguments

A Pi extension restarts Pi with another extension set or model scope by spawning a new Pi with stdio `"inherit"` and exiting with the child's status. In PiG that spawn starts a second pig on the extension's pipes. The extension asks the host instead, and pig replaces its own process image (D238):

```go
// Pi: spawn(process.execPath, [...entry, "-e", aio, "--models", scope, ...rest], { stdio: "inherit", env: {...process.env, AIO_RECIPE_STAGE: "1"} })
stage := "1"
err := ctx.Relaunch(sdk.RelaunchOptions{
	Args: []string{"-ne", "-e", aioPath, "--models", "provider/model-id"},
	Env:  map[string]*string{"AIO_RECIPE_STAGE": &stage, "AIO_RECIPE_ORIGIN": nil},
})
if err != nil {
	// pig cannot restart now; the runtime keeps running
}
```

- Go `ctx.Relaunch(sdk.RelaunchOptions{Args, Env})`; Rust `ctx.relaunch(&args, &env)` (`BTreeMap<String, Option<String>>`); Python `ctx.relaunch(args, env)` (`None` removes); Lisp `(ctx-relaunch ctx args :env (obj ...))` (`:null` removes); Node `await ctx.relaunch({ args, env })` (`null` or `undefined` removes). The wire call is `relaunch` with `{"args": string[], "env": {name: string | null}}`.
- `Args` is the complete argument list after the pig executable. Read the current one with `ctx.HostLaunch()` (Go) or `process.argv.slice(2)` (Node). `Env` changes pig's current environment for the relaunched process. `PIG_EXT_*` and `PIG_HARNESS_*` are refused.
- When `Args` choose no session (`--session`, `--session-id`, `--continue`, `--resume`, `--fork`, `--no-session`) and the current session file exists, pig puts `--session <file>` first, so the relaunched runtime continues the same session. A session file that is not written yet (no assistant reply yet) is not carried.
- A nil error means pig accepted. Pig then shuts down as for `ctx.Shutdown()`: interactive mode when idle, RPC mode at its shutdown check, and print and JSON mode abort a run in progress and write no result. A relaunch in `session_start` sends no command-line prompt. `session_shutdown` runs with reason `quit`. Pig then replaces its process image: the same PID, terminal and standard streams. The relaunched pig starts as `pig <args>` does. Guard against a loop with an environment marker.
- The call returns an error, and the runtime keeps running, on Windows, after pig read piped stdin, in RPC mode once pig reads commands (relaunch in `session_start`), while pig shuts down, and when a relaunch is pending.
- A termination signal, a crash, or a later non-zero `ShutdownWithCode` ends pig instead, without a relaunch.

## Unsupported / typed-error APIs

Any host call exposed by an SDK that is not wired in a given build returns a typed `unsupported` error rather than silently no-oping. Coverage is tracked in `docs/extension-api-parity.md` in the pig source tree; this file is a snapshot.

## SDK quickstart (Go)

```go
package myextension

import sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

func Extension() *sdk.Extension {
    e := sdk.New("my-extension")
    e.Tool("hello", "Say hello", sdk.Schema{
        "type": "object",
        "properties": map[string]any{
            "name": map[string]any{"type": "string"},
        },
    }, func(ctx sdk.Context, args map[string]any) (any, error) {
        return "hello " + args["name"].(string), nil
    })
    e.OnSessionStart(func(ctx sdk.Context, _ map[string]any) (any, error) {
        ctx.Notify("ready", "info")
        return nil, nil
    })
    return e
}

```

The Go SDK is a nested module. `pig extension init ./my-extension` creates a
factory and a portable `go.mod`. Each PiG build stages its own copy of the SDK
under `<config-root>/state/pigsdk/sdk-<hash>`, named for the SDK's content hash,
so PiG versions sharing a config root never replace each other's SDK. PiG adds
a temporary filesystem replacement during the build. The authored file contains
no machine-specific path.

Build PiG and Go extensions with Go 1.27.3. The generated module keeps Go 1.26
as its language floor:

```go.mod
module example.com/my-extension

go 1.26

require github.com/MichaelKinsy/PiG/extensions/sdk v0.0.0

```

Run `pig reload --sdk-path` to print the running build's staged directory. See `extensions.md` for the full quickstart.

## Go SDK migration to 0.3.0

Context usage keeps Pi's nullable values: `Tokens` is `*int`, and `Percent` is `*float64`. Check `usage != nil` and each field before dereferencing. For an explicit caller fallback, replace `usage.Tokens` arithmetic with `usage.TokensOr(0)` and `usage.Percent` arithmetic with `usage.PercentOr(0)`. Unknown usage after compaction is not a known zero; these helpers do not change the fields or JSON.

Replace `sdk.SendMessageOptions{TriggerTurn: true}` with `sdk.SendMessageOptions{TriggerTurn: sdk.Bool(true)}`. Use `sdk.Bool(false)` only for explicit false; nil applies Pi's default. An omitted `triggerTurn` steers during an active turn, while false defers the message. The helpers are Go ergonomics for the shared SDK contract (D19), not a compatibility wire layer.

## SDK quickstart (Python)

```python
from pig_sdk import Extension, Context

def new_extension() -> Extension:
    ext = Extension("my-extension")

    @ext.tool("hello", "Say hello",
              {"type": "object", "properties": {"name": {"type": "string"}}})
    def hello(ctx: Context, args: dict):
        return f"hello {args['name']}"

    @ext.on_session_start
    def started(ctx: Context, _data):
        ctx.notify("ready", "info")

    return ext

```

## SDK quickstart (Rust)

```rust
use pig_sdk::{Extension, Context, Schema};

pub fn new_extension() -> Extension {
    let mut ext = Extension::new("my-extension");
    ext.tool("hello", "Say hello", Schema::object_with(&[("name", Schema::string())]),
        |ctx: Context, args| {
            let name = args.get("name").and_then(|v| v.as_str()).unwrap_or("");
            Ok(serde_json::json!(format!("hello {name}")))
        });
    ext
}

```
