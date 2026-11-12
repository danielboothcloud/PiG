// Ports packages/coding-agent/src/core/model-runtime.ts (the provider composer passes the caller's request options, function members included, to an extension's streamSimple).
// Ports packages/ai/src/types.ts (ProviderRequestOptions.onPayload and fetch, StreamOptions.onResponse and onProviderStreamEvent).
// The caller's functions stay in the host. Each one here calls it through provider.callback with the provider_stream_simple request as its parent, so it works only while that request runs.
export function legacyStreamCallbacks(runtime, provider, parent, names) {
  const call = (method, value, model) => runtime.call("provider.callback", { provider, method, params: model === undefined ? { value } : { value, model } }, { parent });
  const options = {};
  for (const name of Array.isArray(names) ? names : []) {
    if (name === "onPayload") options.onPayload = async (payload, model) => (await call(name, payload, model)) ?? undefined;
    else if (name === "onResponse") options.onResponse = async (response, model) => { await call(name, response, model); };
    else if (name === "onProviderStreamEvent") options.onProviderStreamEvent = async (data, model) => { await call(name, data, model); };
    else if (name === "fetch") options.fetch = callerFetch((method, value) => call(method, value));
  }
  return options;
}

// callerFetch is the caller's fetch: the host runs the request through the caller's transport and the response body streams back in bounded reads.
function callerFetch(call) {
  let next = 0;
  return async (input, init) => {
    const request = new Request(input, init);
    request.signal.throwIfAborted();
    const id = String(++next);
    const headers = {};
    for (const [name, value] of request.headers) (headers[name] ??= []).push(value);
    const body = request.body === null ? undefined : Buffer.from(await request.arrayBuffer()).toString("base64");
    const result = await call("fetch", { id, url: request.url, method: request.method, headers, ...(body === undefined ? {} : { body }) });
    let closed = false;
    const close = async () => {
      if (closed) return;
      closed = true;
      await call("fetchClose", { id });
    };
    const stream = result.body ? new ReadableStream({
      async pull(controller) {
        const chunk = await call("fetchRead", { id, size: 64 * 1024 });
        const data = Buffer.from(chunk?.data ?? "", "base64");
        if (data.length > 0) controller.enqueue(new Uint8Array(data));
        if (chunk?.done) controller.close();
      },
      cancel: close,
    }) : null;
    return new Response(stream, { status: result.status, statusText: result.statusText, headers: result.headers });
  };
}
