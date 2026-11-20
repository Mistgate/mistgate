// "Put on nodes" of the profile page: what the check before the click (CreateInbound with validate_only, nothing written, no UDP run) says
// about each node, in the board's words. The answer is either the inbound as it would be, with its warnings, or a coded refusal.

import { ConnectError } from "@connectrpc/connect";
import type { Inbound, StatusReason } from "@/gen/mistgate/admin/v1/common_pb";
import { profiles } from "@/lib/api";
import { errorCode } from "@/lib/errors";
import { plain, type Plain } from "@/lib/plain";
import type { Tx } from "@/screens/users/t";
import { refusalOf, type Refusal } from "./deploy";
import { heldHead, sharedHead, sharedOf, shareOfRefusal, type RowShare, type RowShared } from "./share-row";

/** What a node needs typed or chosen before it can take the profile (the board's `.pp-pnf`). */
export type Fix = { sni?: string; port?: string; anyway?: boolean; backend?: "auto" | "kernel" | "userspace" };

export type Asked = { state: "pending" } | { state: "ok"; inbound?: Plain<Inbound>; warnings: Plain<StatusReason>[]; freePort: number } | { state: "refused"; error: unknown } | { state: "error" };

/** Asks the server about one node. A network failure is not a verdict: the real call reports it. */
export async function askNode(req: { profileId: string; nodeId: string; port: string; sni: string; anyway: boolean }, signal?: AbortSignal): Promise<Asked> {
  try {
    const r = plain(
      await profiles.createInbound({ profileId: req.profileId, nodeId: req.nodeId, portOverride: req.port ? Number(req.port) : 0, tlsServerNameOverride: req.sni, validateOnly: true, ...(req.anyway && { allowLossyPort: true }) }, { signal }),
    );
    return { state: "ok", inbound: r.inbound, warnings: r.warnings ?? [], freePort: r.freePort ?? 0 };
  } catch (e) {
    const code = errorCode(ConnectError.from(e).rawMessage);
    return code ? { state: "refused", error: e } : { state: "error" };
  }
}

/** The result's class: the dot of its words (the board's em.ok / em.part / em.bad, "none" for neutral). */
export type Verdict = "ok" | "part" | "bad" | "none";

export type NodeIn = { id: string; name: string; address: string; linked: boolean; entry: boolean; firstAwg: boolean; awgMode: string };

export type NodeRow = {
  verdict: Verdict;
  text: string;
  /** A node that cannot take the profile as it is: not ticked, not tickable, until the fix below makes the check pass. */
  need: boolean;
  /** Ticked from the start (an entrance node is not: people go through it to other nodes). */
  on: boolean;
  /** The fields under the node: a domain, a port (with the one the server proved free), the AmneziaWG backend, a link to the updates. */
  dom: boolean;
  port?: { free: string; lossy: string };
  upd: boolean;
  backend: boolean;
  /** The name is held on a TCP port the node shares: the way out (a name of its own, a free port), or an update for an old agent. */
  share?: RowShare;
  /** The profile will stand beside others on a shared TCP port: what that means, and the name whose DNS record is still to be made. */
  shared?: RowShared;
  /** Nothing to say beyond "it works": the row needs no line of words under it (the board says only what is not plain). */
  plain: boolean;
};

const warn = (a: Asked, code: string) => (a.state === "ok" ? a.warnings.find((w) => w.code === code) : undefined);

/** What a row says of itself in the board's three sections and five kinds of line: "Can be put" (ticked or not), "Needs an action" (a field or a choice under it), "Cannot" (an update, a refusal). */
export type PtGroup = "can" | "act" | "no";
export type PtKind = "ok" | "warn" | "act" | "go" | "no";
export type PtShape = { group: PtGroup; kind: PtKind; why: string; /** The line is about a node out of touch (a clock, not a triangle). */ clock?: boolean };

export function shapeOf(r: NodeRow, n: NodeIn): PtShape {
  if (r.need) {
    if (r.upd) return { group: "no", kind: "go", why: r.text };
    return r.dom || r.share || r.port ? { group: "act", kind: "act", why: r.text } : { group: "no", kind: "no", why: r.text };
  }
  if (n.entry) return { group: "can", kind: "ok", why: r.text };
  if (!n.linked) return { group: "can", kind: "warn", why: r.text, clock: true };
  if (r.verdict === "part" || r.shared) return { group: "can", kind: "warn", why: r.text };
  return { group: "can", kind: "ok", why: r.plain ? "" : r.text };
}

/** One node's row: the check's answer and the node's own facts put into the board's words. */
export function rowOf(a: Asked, n: NodeIn, o: { t: Tx; protocol: string; fix: Fix }): NodeRow {
  const { t, protocol, fix } = o;
  const base: NodeRow = { verdict: "ok", text: "", need: false, on: !n.entry, dom: false, upd: false, backend: false, plain: false };
  const awg = protocol === "awg";
  if (a.state === "pending") return { ...base, verdict: "none", text: t("ps.pt.check") };
  if (a.state === "error") return { ...base, verdict: "none", text: n.linked ? t("ps.pt.unchecked") : t("ps.pt.offline") };
  if (a.state === "refused") {
    const msg = ConnectError.from(a.error).rawMessage;
    const ref: Refusal = refusalOf(a.error, t, n, protocol);
    if (ref.kind === "port") {
      const free = ref.free && Number(ref.free) > 0 ? ref.free : "";
      // a port the server proved free is taken at once (the field below holds it); without one the node waits for a port
      return { ...base, verdict: free ? "part" : "bad", need: !free && !fix.port, on: !n.entry && !!free, text: ref.text, port: { free, lossy: ref.lossy ?? "" } };
    }
    if (ref.kind === "domain") return { ...base, verdict: "bad", need: true, on: false, text: ref.text, dom: true };
    if (ref.kind === "share") return { ...base, verdict: "bad", need: true, on: false, text: heldHead(t, ref), upd: ref.code === "share_agent_old", share: shareOfRefusal(ref) };
    return { ...base, verdict: "bad", need: true, on: false, text: ref.text, upd: errorCode(msg) === "agent_too_old" };
  }
  const port = a.inbound?.port ? String(a.inbound.port) : "";
  const text = n.entry
    ? t("ps.pt.entry")
    : !n.linked
      ? t("ps.pt.offline")
      : n.firstAwg && awg
        ? t("ps.pt.awgFirst", { mode: t(`awg.backend.${(fix.backend ?? n.awgMode) as "auto" | "kernel" | "userspace"}`) })
        : warn(a, "tcp_port_shared")
          ? sharedHead(t, warn(a, "tcp_port_shared")!)
          : warn(a, "hy2_site_taken") || warn(a, "hy2_decoy_paused")
          ? t("ps.pt.siteYields")
          : fix.sni
            ? t("ps.pt.domainSet")
            : t("ps.pt.free", { port });
  const row: NodeRow = { ...base, verdict: n.entry || !n.linked ? "none" : "ok", text, backend: awg && n.firstAwg && n.linked };
  // a linked node that is not an entry, with nothing to add (no first AmneziaWG, no shared port, no yielding site, nothing typed): "it works"
  row.plain = !n.entry && n.linked && !row.backend && !warn(a, "tcp_port_shared") && !warn(a, "hy2_site_taken") && !warn(a, "hy2_decoy_paused") && !fix.sni && !fix.port && !warn(a, "warp_missing");
  if (fix.port) row.port = { free: "", lossy: "" };
  if (!n.entry && n.linked) row.shared = sharedOf(t, a.warnings, a.inbound?.tlsServerName, n.address);
  const w = warn(a, "warp_missing");
  return w ? { ...row, verdict: "part", text: t("ps.pt.warp", { node: n.name }) } : row;
}
