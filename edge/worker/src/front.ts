import { createFront } from "./route";

// The FRONT script of the split edition (routing: src/route.ts): the one script with the hostname and a route, and no wasm.
//
// The Durable Objects live here for good: NodeLink and Limiter keep their script, their migration tags and their data, so the
// hostname, the objects and every agent socket stay where they are while the roles around them are deployed or rolled back.
// NodeLink reaches the Go side through the PANEL_LINK service binding (the link role's PanelLink entrypoint). Nothing but the
// two classes and the handler is exported: a Worker's exports are its entrypoints.
export { Limiter } from "./limiter";
export { NodeLink } from "./nodelink";

export default createFront();
