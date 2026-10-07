import type { Env } from "./env";
import { fetchPanelRequest } from "./fetch";

// The Durable Object classes behind the LIMITER and NODELINK bindings must be exported from the Worker's entry module,
// and so must PanelLink, the entrypoint through which a NodeLink object calls the panel (ctx.exports).
export { Limiter } from "./limiter";
export { NodeLink } from "./nodelink";
export { PanelLink } from "./panellink";

// Every request goes to the Go panel, which owns all routing (the admin path is a secret kept in D1, so the Worker
// cannot know it). The Worker only converts between the Workers Request/Response and the panel's fetch contract, and
// hands an agent link upgrade, which the panel marks instead of serving, to the node's NodeLink object.
export default {
  fetch: fetchPanelRequest,
} satisfies ExportedHandler<Env>;
