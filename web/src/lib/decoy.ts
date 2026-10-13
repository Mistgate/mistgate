import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import type { Plain } from "@/lib/plain";

// The per-node decoy site (docs: guide/decoy-site.md, "Nodes: three options"): the three modes, the one limit the panel and the
// browser share, and the reasons a node's site is not applied yet. The server decides everything; this only names it.

type Node = Plain<NodeMsg>;

export const decoyModes = ["builtin", "panel", "own"] as const;
export type DecoyMode = (typeof decoyModes)[number];
export const asDecoyMode = (v: string): DecoyMode => (v === "panel" || v === "own" ? v : "builtin");

/** An own archive (and the panel's --decoy-dir) is refused above this: checked here first so a big file is never sent. */
export const maxBundleBytes = 2 * 1024 * 1024;
/** SetNodesDecoyMode takes 1..50 nodes. */
export const maxBulkNodes = 50;

/** The reasons a configured mode is not applied yet (Node.decoy_reason_code); empty means applied. */
export const decoyReasons = [
  "agent_too_old",
  "node_offline",
  "pending",
  "apply_failed",
  "own_bundle_missing",
  "panel_unavailable",
  "panel_too_large",
  "panel_no_index",
  "panel_invalid",
  "unavailable_in_edition",
] as const;
export type DecoyReason = (typeof decoyReasons)[number];
export const isDecoyReason = (v: string): v is DecoyReason => (decoyReasons as readonly string[]).includes(v);

/** The first 12 hex digits of a SHA-256: enough to tell two bundles apart. */
export const shortHash = (h: string) => h.replace(/^sha256:/, "").slice(0, 12);

/** Own can be chosen for these nodes together only if every one already has its bundle (the server refuses the rest): the names that stand in the way. */
export function ownBlockedBy(nodes: readonly Pick<Node, "name" | "decoyBundleSha256" | "decoyOwnAvailable">[]): string[] {
  return nodes.filter((n) => !n.decoyBundleSha256 || !n.decoyOwnAvailable).map((n) => n.name);
}
