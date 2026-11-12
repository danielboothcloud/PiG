# The Python twin of testfixture/anthropic_api.go: it calls the built-in Anthropic transport through anthropic_messages_api with its own onPayload, fetch, onResponse and cancellation signal.
import http.client
import json
import socket
import threading
import urllib.parse

import pig_sdk


def _fetch(request):
    """A fetch over http.client whose signal shuts the connection down, as an aborted fetch closes its request."""
    signal = request.get("signal")
    url = urllib.parse.urlsplit(request["url"])
    conn = http.client.HTTPConnection(url.hostname, url.port)
    conn.connect()

    def abort():
        try:
            conn.sock.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass

    unsubscribe = signal.subscribe(abort) if signal is not None else (lambda: None)
    try:
        headers = {name: ", ".join(values) if isinstance(values, list) else values for name, values in (request.get("headers") or {}).items()}
        headers["X-Raw-Fetch"] = "extension"
        path = url.path + ("?" + url.query if url.query else "")
        conn.request(request["method"], path, body=request.get("body"), headers=headers)
        response = conn.getresponse()
    except Exception:
        conn.close()
        raise
    finally:
        unsubscribe()
    return {"status": response.status, "statusText": response.reason, "headers": response.getheaders(), "body": response}


def new_extension() -> pig_sdk.Extension:
    extension = pig_sdk.Extension("raw-anthropic")
    lock = threading.Lock()
    current = {}

    def raw_cancel(ctx, args):
        with lock:
            signal = current.get("signal")
        if signal is None:
            raise RuntimeError("no raw request")
        signal.set()

    def raw_probe(ctx, args):
        spec = json.loads(args)
        signal = pig_sdk.ProviderSignal()
        with lock:
            current["signal"] = signal
        order = []
        returned = threading.Event()

        def on_payload(payload, model):
            order.append("payload")
            if spec["Fail"]:
                raise RuntimeError("fixture payload rejection")
            payload["metadata"] = {"user_id": "raw-payload-hook"}
            return payload

        def fetch(request):
            returned.wait()
            order.append("fetch")
            return _fetch(request)

        def on_response(response, model):
            order.append("response")
            if response.get("status") != 200:
                raise RuntimeError(f"response status = {response.get('status')}")

        try:
            api = pig_sdk.anthropic_messages_api(ctx)
            run = api.stream_simple if spec["Simple"] else api.stream
            options = pig_sdk.ProviderStreamOptions(values={"apiKey": spec["Key"], "fetch": fetch}, signal=signal, on_payload=on_payload, on_response=on_response)
            stream = run(spec["Model"], {"messages": [{"role": "user", "content": "hello", "timestamp": 1}]}, options)
            returned.set()
            result = stream.result()
            terminal = None
            for event in stream.events():
                terminal = event
        finally:
            with lock:
                current.pop("signal", None)
        want_event = "error" if spec["Cancel"] or spec["Fail"] else "done"
        if terminal is None or terminal.get("type") != want_event:
            raise RuntimeError(f"terminal event = {terminal}, want {want_event}")
        reason, expected = "stop", ["payload", "fetch", "response"]
        if spec["Cancel"]:
            reason, expected = "aborted", ["payload", "fetch"]
        if spec["Fail"]:
            reason, expected = "error", ["payload"]
        if result.get("stopReason") != reason or order != expected:
            raise RuntimeError(f"raw result={result} order={order}")
        if spec["Fail"] and result.get("errorMessage") != "fixture payload rejection":
            raise RuntimeError(f"payload error = {result}")

    extension.command("raw-probe", "Call the builtin API without registry dispatch", raw_probe)
    extension.command("raw-cancel", "Cancel the raw API request", raw_cancel)
    return extension
