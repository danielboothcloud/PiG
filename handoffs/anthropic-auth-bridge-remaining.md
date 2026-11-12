# Anthropic auth bridge: remaining work

State as of commit `edc646e2c` ("feat: auth bridge") on `feature/fork`. That commit contains all the work below; the worktree was clean when this file was written. The consumer is the Go port of `@gotgenes/pi-anthropic-auth` 3.4.4 in `pi-aio` (`go/anthropic-auth`, `go/internal/anthropicauth`), which another session finished and tested against a pig built from this tree.

## What the commit does

1. **Raw Anthropic transport for extensions.** Go `sdk.AnthropicMessagesAPI(ctx) sdk.ProviderStreams` and Python `pig_sdk.anthropic_messages_api(ctx)` port Pi's `anthropicMessagesApi()`. They send the existing `modelStream` call with `apiRequest: true`, so a provider's `streamSimple` can wrap the built-in transport without dispatching back to itself. A signal that is already cancelled ends the stream as aborted (`Request aborted`) and sends nothing, as Pi's `pi-ai-bridge.mjs` does. Node already serves Pi's own factory.
2. **Caller callbacks reach a legacy `registerProvider({streamSimple})` wrapper.** Pi's composer passes the caller's options object whole to `extension.streamSimple`. PiG serialized only the data and dropped `onPayload`, `onResponse`, `onProviderStreamEvent` and `fetch`, so any wrapper silently broke other extensions' request hooks. The host now:
   - advertises the callbacks in `provider_stream_simple` as `callbacks`;
   - serves them through `provider.callback` with that request as the parent, and only while the request runs (`coding/extension/host/subprocess/legacy_stream_callbacks.go`);
   - runs the caller's `fetch` in the host, using the bounded `fetch`/`fetchRead`/`fetchClose` reads, and closes any body left open when the request ends.

   Go, Node and Python rebuild them as functions in the handler's options.
3. **`onProviderStreamEvent`** is forwarded through the `modelStream` callback bridge like the other callbacks (`ModelStreamCall.OnProviderStreamEvent`, `extension.ModelStreamRequest.OnProviderStreamEvent`).

## Evidence already gathered

- **`TestLegacyWrapperRawTransportCallbacks`** (`test/extension-conformance/legacy_api_test.go`) passes in Go isolated, packed and fused, Node isolated and packed, and Python isolated and packed. It checks:
  - concurrent callers keep their own callbacks and credentials;
  - the caller's payload change precedes the wrapper's;
  - the caller's fetch carries the request;
  - an `onResponse` rejection fails the request;
  - the observer sees stream events;
  - cancelling the caller closes the HTTP exchange.
- **`TestAnthropicMessagesAPIRawTransport`** (`test/extension-conformance/anthropic_api_test.go`) passes in Go isolated, packed and fused and Python isolated and packed.
- **Pre-cancel unit tests:** `TestAnthropicMessagesAPICancelledSignalSendsNothing` (Go) and `extensions/sdk-py/tests/test_anthropic_messages_api.py` (Python).
- **Mutations:** each was applied, run and reverted.
  - Go host and SDK: no callbacks advertised; fetch not forwarded; observer not flagged; SDK ignoring callbacks; host not admitting `provider.callback`. Each failed the callback row.
  - Node: callbacks dropped; observer flag dropped. Each failed.
  - Python: callbacks dropped; observer dropped; raw cancellation ignored; `apiRequest` missing. Each failed. The pre-cancel check removal failed the Python unit test.
- **Wider runs:** the cross-SDK conformance run filtered to `Provider|ModelStream|ModelFetch|StreamSimple|Callback|Anthropic|Legacy`, with `MISE_RUST_VERSION=1.99.0`, had no failures other than Lisp rows. `sbcl` is not installed, so every Lisp row fails at build.
  - That run caught one regression, which I fixed before the commit: I had made a cancelled legacy `streamSimple` end as `aborted`, and `TestProviderProducersAcrossSDKs` pins `error` (see task 4).
- **Other suites:** `go test ./extensions/sdk` passes, `TestIndependentAPIRequest*` passes in `./coding`, and the host provider tests in `./coding/extension/host/subprocess` pass (they need `MISE_RUST_VERSION=1.99.0`).
- **Gates:** `make sdk-bundles-drift`, `make interface-go-drift`, `make interface-recommendations-drift`, `go run ./test/parity/cmd/sdksurface -check` and `python3 automation/gen/gen-docs-bundle.py --check` are clean. `go vet` and `golangci-lint` report 0 issues on the touched packages.

## Remaining tasks

### 1. Confirm `make port-lint` (fast, do first)
The new `provider_stream_simple` argument map tripped `mapkeyorder` at `coding/extension/host/subprocess/provider_stream.go`. I moved the existing `//portlint:allow mapkeyorder` comment onto the `json.Marshal` line, but stopped the confirming run.
- Re-run it and check that no finding names `provider_stream.go`, `legacy_stream_callbacks.go` or `extensions/sdk/provider_stream_callbacks.go`.
- Two findings were present before this work and belong to other changes: `coding/extension/foreground.go` and `coding/extension/host/subprocess/relaunch.go` (D238, both `emptydrop`).

### 2. Rust: callback forwarding and `anthropic_messages_api`
Rust has neither feature. The gap is recorded in `legacyCallbackGaps` (`legacy_api_test.go`) and in `test/parity/sdk-exports.toml` lane `SdkRawAnthropic`.
- **The blocker:** `ProviderStreamSimpleFn` (`extensions/sdk-rs/src/provider.rs`) takes the options as a `serde_json::Value`, which cannot carry functions. Add a callback-carrying form without breaking the existing signature, for example a second registration that receives `ProviderStreamOptions` with callback fields. The forced language shape needs a sentence in `docs/extension-api-parity.md`.
- **The transport factory:** add `anthropic_messages_api(ctx) -> ProviderStreams` (`extensions/sdk-rs/src/create_provider.rs` already has `ProviderStreams`). Reuse the existing model-stream callback and fetch bridge in `extensions/sdk-rs/src/context.rs` (`stream_simple_with_callbacks`), add the `apiRequest` flag, and add the pre-cancel short circuit.
- **Fixtures and tests:**
  - Add `testdata/legacy-api-rust` and `testdata/raw-anthropic-rust`, mirroring the Go and Python fixtures.
  - Add `rust-isolated` and `rust-packed` to both tests.
  - Delete the `rust` entry of `legacyCallbackGaps`. `TestLegacyCallbackGapsAreListed` fails until it is deleted.
  - Set the Rust cell in `sdk-exports.toml`, then run `go run ./test/parity/cmd/sdksurface`.
- **Mutation:** prove the new rows fail with forwarding removed.

### 3. Lisp: callback forwarding and `anthropic-messages-api`
This needs the same two pieces in `extensions/sdk-lisp/src` (exported in `package.lisp`), fixtures `testdata/legacy-api-lisp` and `raw-anthropic-lisp`, and `lisp` rows. It cannot be verified without `sbcl` 2.6.9 (`docs/extension-authoring.md`, Lisp). Delete the `lisp` gap entry when it lands.

### 4. Decide the reason a cancelled legacy `streamSimple` reports
- **Current behavior:** when the caller cancels, the host ends a legacy `streamSimple` at once with `stopReason: "error"` and `context canceled` (`providerStreamCallback` in `provider_stream.go`). `TestProviderProducersAcrossSDKs` pins that, citing `api/lazy.ts:4-22`.
- **What Pi might do:** in Pi, a wrapper that delegates to `anthropicMessagesApi()` would forward the delegate's aborted event instead.
- **To resolve:**
  - Probe Pi 1.5.0 with a wrapper extension and a cancelled request.
  - If Pi reports `aborted`, fix the host to wait for the extension's terminal event, or use `aborted` when the caller cancelled. Then update both tests together.
  - If not, leave it and drop the note in `legacy_api_test.go`.

### 5. Public request signal for Python and Go wrappers
- **Python:** the Python fixture passes `ctx._cancelled`, a private attribute, as the delegate's signal. A Python wrapper needs a public way to get the provider request's cancellation.
- **Go:** wrappers wrap `sdk.Context` in a `requestSignal` adapter that adds `Deadline` and `Value`, because `sdk.Context` has `Done` and `Err` but is not a `context.Context`. The Go fixture (`testfixture/legacy_api.go`) and `pi-aio/go/anthropic-auth/extension.go` both carry that adapter.
- **To decide:** whether `sdk.Context` should implement `context.Context`, or offer a public accessor in each SDK. Either changes the SDK surface, so run `make interface-go` and `sdksurface`.

### 6. Unpaired UTF-16 surrogates across the SDK boundary
`pi-aio` hashes prompt text by UTF-16 code unit, as the original does. `encoding/json` turns an unpaired surrogate into U+FFFD, so prompt text containing one may reach the Go extension changed. Nothing tests this across the host-to-SDK boundary. Add a row that sends a lone surrogate through `onPayload` and compares the hashed result with Pi's. The Go SDK has `extensions/sdk/json` for WTF-8.

### 7. Gates not run on this machine
- **`make generate`:** fails at its help-text step because it cannot delete read-only files in a temporary npm directory. The generators it covers were run one by one, as listed above.
- **`make divergence-guard`:** needs the `.upstream/current` mirror (`make upstream-mirror`).
- **Full suites:** `make test`, `make check`, `make parity` and the full `test/extension-conformance` package were not run. The filtered run above took about 10 minutes and builds Rust fixtures.
- **Lisp rows:** need `sbcl`.

### 8. Before publishing
- **Changelog:** add a fragment `changelog.d/<lane>-<sha>.md` (`### Added` for the raw transport, `### Fixed` for the dropped caller callbacks with `<!-- fix: edc646e2c -->`). Check it with `make changelog-check`.
- **Signing:** `edc646e2c` must carry `Signed-off-by` and a verified signature (`git commit --signoff -S`). Amend it if it does not, and rename the changelog fragment after the new SHA.
- **Not a divergence:** this work ports Pi's `anthropicMessagesApi` and fixes PiG toward Pi's composer behavior, so it needs no D-number.

## Running the checks off this machine

The full set needs Go (`.go-version`), Node (`.node-version`), Python 3.12, Rust 1.97.1 or newer, and `sbcl` for Lisp. Before any test, clear the variables a running pig exports and isolate the agent directories:

```sh
for v in $(env | rg -o "^(PIG_EXT|PIG_HARNESS|PIG_PRODUCT)[A-Z_]*"); do unset $v; done
T=$(mktemp -d); export PIG_CODING_AGENT_DIR=$T/a PI_CODING_AGENT_DIR=$T/a PIG_HOME=$T/h PI_HOME=$T/h
export MISE_RUST_VERSION=1.99.0   # only where mise has no Rust version set
```

Cheapest useful set, in order:

```sh
make port-lint
go test -trimpath ./extensions/sdk -run 'AnthropicMessagesAPI'
go test -trimpath ./test/extension-conformance -run 'TestLegacyWrapperRawTransportCallbacks|TestAnthropicMessagesAPIRawTransport|TestLegacyCallbackGapsAreListed|TestProviderProducersAcrossSDKs' -timeout 20m
python3 -m pytest -q extensions/sdk-py/tests/test_anthropic_messages_api.py
```

Broad check:

```sh
go test -trimpath ./test/extension-conformance -run 'Provider|ModelStream|ModelFetch|StreamSimple|Callback|Anthropic|Legacy' -timeout 40m
```

After any SDK or Node runtime edit, run `make sdk-bundles` (and `make node-runtime` for `runtime.mjs`). After exported Go API changes, run `make interface-go`, `make interface-go-drift`, `make interface-recommendations-drift` and `go run ./test/parity/cmd/sdksurface -check`.
