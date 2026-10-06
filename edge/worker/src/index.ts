import type { Env } from "./env";
import { getPanel } from "./panel";
import { panelURL, toFetchRequest, toResponse } from "./shell";

// The Durable Object class behind the LIMITER binding must be exported from the Worker's entry module.
export { Limiter } from "./limiter";

// Every request goes to the Go panel, which owns all routing (the admin path is a secret kept in D1, so the Worker
// cannot know it). The Worker only converts between the Workers Request/Response and the panel's fetch contract.
export default {
  async fetch(request, env): Promise<Response> {
    let panel;
    try {
      panel = await getPanel(env, new URL(panelURL(request.url)).origin);
    } catch (error) {
      console.error("panel init failed:", error instanceof Error ? error.message : String(error));
      return new Response("Service Unavailable", { status: 503, headers: { "Retry-After": "5" } });
    }
    try {
      return toResponse(await panel.fetch(await toFetchRequest(request)));
    } catch (error) {
      console.error("panel request failed:", error instanceof Error ? error.message : String(error));
      return new Response("Internal Server Error", { status: 500 });
    }
  },
} satisfies ExportedHandler<Env>;