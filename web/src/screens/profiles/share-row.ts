// A node of "Put on nodes" and of the wizard's "Which nodes" whose TCP port the node shares by name: what the row offers when the name is
// held (the profile stands beside the other under a name of its own, or takes a free port) and what it says once the check passes.

import { portNotes } from "@/lib/port-check";
import { shareName, type ShareCode, type ShareOffer } from "@/lib/tcpshare";
import type { Tx } from "@/screens/users/t";
import type { Refusal } from "./deploy";

/** The refusal's way out, for the fields under the node: the offer (null when the name is not the profile's to change), and a free port. */
export type RowShare = { code: ShareCode; offer: ShareOffer | null; free: string };

export const shareOfRefusal = (ref: Extract<Refusal, { kind: "share" }>): RowShare => ({ code: ref.code, offer: ref.offer, free: ref.free ?? "" });

/** What a passing check says of the shared port, under the node: the sentences, and the name whose DNS record is still to be made ("" when the name is the node's own). */
export type RowShared = { text: string; name: string };

export function sharedOf(t: Tx, warnings: readonly { code: string; params: Record<string, string> }[], inboundName: string | undefined, nodeAddress: string): RowShared | undefined {
  const mine = warnings.filter((w) => w.code === "tcp_port_shared" || w.code === "share_two_faces");
  if (!mine.some((w) => w.code === "tcp_port_shared")) return undefined;
  const name = inboundName && shareName(inboundName) !== shareName(nodeAddress) ? inboundName : "";
  return { text: portNotes(t as never, mine).join(" "), name };
}

/** The short line at the head of the node's row for a shared port, and for a held name (the long sentence is the fields' own). */
export const sharedHead = (t: Tx, w: { params: Record<string, string> }) => t("share.row.shared", { profiles: w.params.profiles ?? "", port: w.params.port ?? "" });
export const heldHead = (t: Tx, ref: Extract<Refusal, { kind: "share" }>) =>
  ref.offer ? t("share.row.held", { name: ref.offer.held, profile: ref.offer.profile }) : ref.text;
