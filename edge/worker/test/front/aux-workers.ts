// The roles around FRONT in test/front, as plain JavaScript (an auxiliary worker is not bundled): each answers with what it
// received, so a test can see which one a request reached and what arrived unchanged. They stand in for the Go roles; the
// real ones are the wasm of cmd/mistgate-edge-*.

/**
 * A role that answers the one path it serves with a JSON echo (which role, and what it got) and everything else with the decoy,
 * as the Go roles do: the decoy is theirs, FRONT never makes one.
 */
const echo = (role: string, served: string) => `
export default {
  async fetch(request) {
    const url = new URL(request.url);
    if (!url.pathname.startsWith(${JSON.stringify(served)})) return new Response("decoy", { status: 404, headers: { "X-Decoy": "1", "X-Role": ${JSON.stringify(role)}, "Content-Type": "text/plain" } });
    return Response.json({ role: ${JSON.stringify(role)}, method: request.method, url: request.url, path: url.pathname, host: url.host,
      header: request.headers.get("x-test"), body: await request.text() }, { status: 201, headers: { "X-Role": ${JSON.stringify(role)} } });
  },
};`;

/** The subscription surface of the public role is under /sub-secret/; the admin role serves /adm-secret/. Both say which role answered in X-Role, decoy or not. */
export const publicAux = echo("public", "/sub-secret/");
export const adminAux = echo("admin", "/adm-secret/");

/**
 * The link role: PanelLink (the entrypoint FRONT's NodeLink objects reach as PANEL_LINK) with a fixed script, and a fetch that
 * forwards an upgrade of /<anything>/link/<node id> to that node's object in FRONT's script, as the panel's marker makes the
 * TypeScript role do. Frames of the "agent" are echoed by the step.
 */
export const linkAux = `
import { WorkerEntrypoint } from "cloudflare:workers";
const enc = (s) => new TextEncoder().encode(s);
export class PanelLink extends WorkerEntrypoint {
  challenge(audience) { return { nonce: new Uint8Array(32).fill(7), frame: enc("challenge " + audience) }; }
  accept(nodeId, audience, nonce, auth, until) { return { ok: true, frame: enc("accept " + nodeId), certSerial: "c1", certNotAfterUnix: 2000000000 }; }
  step(input) { return { state: (input.state ?? "") + input.event.kind[0], frames: input.event.kind === "frame" ? [input.event.frame] : [] }; }
}
export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const id = url.pathname.includes("/link/") ? url.pathname.split("/link/").pop() : undefined;
    if (id && request.headers.get("upgrade") === "websocket") return env.NODELINK.get(env.NODELINK.idFromName(id)).fetch(request);
    return Response.json({ role: "link", path: url.pathname, method: request.method }, { status: 201, headers: { "X-Role": "link" } });
  },
};`;
