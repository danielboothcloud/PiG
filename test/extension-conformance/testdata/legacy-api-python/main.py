# A register_provider({"streamSimple"}) wrapper of the built-in Anthropic transport, as @gotgenes/pi-anthropic-auth registers one: it composes the caller's onPayload before its own change and keeps every other caller option. testfixture/legacy_api.go is the Go twin.
import pig_sdk


def new_extension() -> pig_sdk.Extension:
    extension = pig_sdk.Extension("legacy-api")

    def stream_simple(ctx, model, transcript, options):
        caller = options.get("onPayload")

        def on_payload(payload, payload_model):
            if not callable(caller):
                raise RuntimeError("caller onPayload missing")
            replacement = caller(payload, payload_model)
            if replacement is not None:
                payload = replacement
            user = (payload.get("metadata") or {}).get("user_id")
            if not user:
                raise RuntimeError("caller did not change the payload first")
            payload["metadata"]["user_id"] = user + "+wrapped"
            return payload

        values = dict(options)
        values["onPayload"] = on_payload
        return pig_sdk.anthropic_messages_api(ctx).stream_simple(model, transcript, pig_sdk.ProviderStreamOptions(values=values, signal=ctx._cancelled))

    extension.register_provider("anthropic", {"api": "anthropic-messages", "streamSimple": stream_simple})
    return extension
