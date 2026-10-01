import type { T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";

// Why a profile did not start on a node, in words: the agent (or the panel, for a profile that cannot be built) leaves an
// English last_error such as "listen udp :8443: bind: address already in use". This sorts it into a class with a sentence;
// the raw text stays on screen under it. A heuristic: a text it does not know is "other", never a wrong guess.

export type InboundErrorKind = "port" | "cert" | "warp" | "permission" | "awg" | "other";

// order matters: "cannot build inbound: a host name is required for a Let's Encrypt certificate" is about the certificate
const kinds: [InboundErrorKind, RegExp][] = [
  ["port", /address already in use/i],
  ["cert", /acme|certificate|let'?s encrypt|needs a dns name|host name is required|x509/i],
  ["permission", /permission denied|operation not permitted/i],
  ["awg", /amneziawg|awgnl|\bawg:/i],
  ["warp", /\bwarp\b|egress/i],
];

export function inboundErrorKind(error: string): InboundErrorKind {
  return kinds.find(([, re]) => re.test(error))?.[0] ?? "other";
}

/** The port the text names ("listen udp [::]:8443: bind: ...", "udp/8443"); undefined when there is none. */
export function inboundErrorPort(error: string): number | undefined {
  const m = /(?:udp|tcp)\/(\d{1,5})|:(\d{1,5})(?=:|\s|$)/i.exec(error);
  const n = Number(m?.[1] ?? m?.[2]);
  return n > 0 && n <= 65535 ? n : undefined;
}

const sentence: Record<InboundErrorKind, MessageKey> = {
  port: "node.inbound.err.portAny",
  cert: "node.inbound.err.cert",
  warp: "node.inbound.err.warp",
  permission: "node.inbound.err.permission",
  awg: "node.inbound.err.awg",
  other: "node.inbound.err.other",
};

/**
 * The sentence for a profile's last_error. `port` is the profile's port on the node (else the one the text names),
 * `process` what holds it when the node's doctor knows ("caddy").
 */
export function inboundErrorText(t: T, error: string, ctx: { port?: number; process?: string } = {}): string {
  const kind = inboundErrorKind(error);
  const port = ctx.port || inboundErrorPort(error);
  if (kind === "port" && port) {
    const vars = { port: `udp/${port}`, process: ctx.process ?? "" };
    return t(ctx.process ? "node.inbound.err.portProcess" : "node.inbound.err.port", vars);
  }
  return t(sentence[kind]);
}
