"""anthropic_messages_api: pi-ai's anthropicMessagesApi() for a provider's stream_simple wrapper."""

import pig_sdk


def test_a_signal_already_set_ends_the_stream_as_aborted_without_a_request():
    # upstream: coding-agent shims pi-ai-bridge.mjs bridged: a signal that fired before the request ends it without sending. A None context has no connection, so any host call would raise: the result proves no request started.
    signal = pig_sdk.ProviderSignal()
    signal.set()
    model = {"id": "claude-test", "provider": "anthropic", "api": "anthropic-messages"}
    api = pig_sdk.anthropic_messages_api(None)
    for run in (api.stream, api.stream_simple):
        result = run(model, {"messages": []}, pig_sdk.ProviderStreamOptions(signal=signal)).result()
        assert result["stopReason"] == "aborted"
        assert result["errorMessage"] == "Request aborted"
        assert (result["provider"], result["model"], result["api"]) == ("anthropic", "claude-test", "anthropic-messages")
