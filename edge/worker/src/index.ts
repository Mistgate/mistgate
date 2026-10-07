import type { Env } from "./env";
import { getPanel } from "./panel";
import { forwardLink, panelURL, toFetchRequest, toResponse } from "./shell";

// The Durable Object classes behind the LIMITER and NODELINK bindings must be exported from the Worker's entry module,
// and so must PanelLink, the entrypoint through which a NodeLink object calls the panel (ctx.exports).
export { Limiter } from "./limiter";
export { NodeLink } from "./nodelink";
export { PanelLink } from "./panellink";

// Every request goes to the Go panel, which owns all routing (the admin path is a secret kept in D1, so the Worker
// cannot know it). The Worker only converts between the Workers Request/Response and the panel's fetch contract, and
// hands an agent link upgrade, which the panel marks instead of serving, to the node's NodeLink object.
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
      const answer = await panel.fetch(await toFetchRequest(request));
      return (await forwardLink(env.NODELINK, request, answer)) ?? toResponse(answer);
    } catch (error) {
      console.error("panel request failed:", error instanceof Error ? error.message : String(error));
      return new Response("Internal Server Error", { status: 500 });
    }
  },
} satisfies ExportedHandler<Env>;