// A registerProvider({streamSimple}) wrapper of the built-in Anthropic transport, as @gotgenes/pi-anthropic-auth registers one (oauth-transport.ts): it composes the caller's onPayload before its own change and keeps every other caller option. testfixture/legacy_api.go is the Go twin.
import { anthropicMessagesApi } from "@earendil-works/pi-ai/compat";

export default function (pi) {
  pi.registerProvider("anthropic", {
    api: "anthropic-messages",
    streamSimple(model, context, options) {
      const caller = options?.onPayload;
      return anthropicMessagesApi().streamSimple(model, context, {
        ...options,
        onPayload: async (payload, payloadModel) => {
          if (typeof caller !== "function") throw new Error("caller onPayload missing");
          const shaped = (await caller(payload, payloadModel)) ?? payload;
          const user = shaped?.metadata?.user_id;
          if (!user) throw new Error("caller did not change the payload first");
          shaped.metadata.user_id = `${user}+wrapped`;
          return shaped;
        },
      });
    },
  });
}
