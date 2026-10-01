import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Fragment } from "react";
import { WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { nodesQuery } from "@/lib/queries";
import { useTx } from "@/screens/users/t";

/** States in which a node's WARP cannot carry traffic at all (no account, no backend, paused). */
const unusable = new Set<WarpState>([WarpState.UNSPECIFIED, WarpState.NOT_CONFIGURED, WarpState.UNAVAILABLE, WarpState.DISABLED]);

type NodeRef = { id: string; name: string };

/** Nodes among `nodeIds` that cannot give a WARP exit (by name, and as id + name for links); with no nodeIds, whether any node can. */
export function warpGaps(nodes: { id: string; name: string; warp?: { state: WarpState } }[], nodeIds: string[]): { missing: string[]; missingNodes: NodeRef[]; anyNode: boolean } {
  const usable = (n: { warp?: { state: WarpState } }) => !unusable.has(n.warp?.state ?? WarpState.UNSPECIFIED);
  const missingNodes = nodes.filter((n) => nodeIds.includes(n.id) && !usable(n)).map((n) => ({ id: n.id, name: n.name }));
  return { missing: missingNodes.map((n) => n.name), missingNodes, anyNode: nodes.some(usable) };
}

const linkClass = "font-bold underline decoration-dotted underline-offset-2";

/** "No working WARP on: de1, fi1." with every name a link to that node's WARP card (?tab=settings#warp). */
export function WarpMissing({ nodes, textKey = "warp.egress.missing" }: { nodes: NodeRef[]; textKey?: "warp.egress.missing" | "twin.warpMissing" }) {
  const t = useTx();
  // the sentence comes whole from the dictionary; the names go where {nodes} stands
  const [before, after = ""] = t(textKey, { nodes: "\u0000" }).split("\u0000");
  return (
    <>
      {before}
      {nodes.map((n, i) => (
        <Fragment key={n.id}>
          {i > 0 && ", "}
          <Link to="/nodes/$id" params={{ id: n.id }} search={{ tab: "settings" }} hash="warp" title={t("warp.egress.nodeLink", { node: n.name })} className={linkClass}>
            {n.name}
          </Link>
        </Fragment>
      ))}
      {after}
    </>
  );
}

/** Under the "Exit: WARP" choice of a profile: which of its nodes have no WARP yet, in words, each a way to its WARP card. */
export function EgressNote({ nodeIds }: { nodeIds: string[] }) {
  const t = useTx();
  const nodes = useQuery(nodesQuery);
  if (!nodes.data) return null;
  const { missingNodes, anyNode } = warpGaps(nodes.data.nodes, nodeIds);
  if (nodeIds.length > 0 && missingNodes.length > 0) {
    return (
      <span className="text-[11px] leading-snug text-warn-text">
        <WarpMissing nodes={missingNodes} />
      </span>
    );
  }
  if (nodeIds.length > 0) return null;
  if (anyNode) return <span className="text-[11px] leading-snug text-muted">{t("warp.egress.general")}</span>;
  return (
    <span className="text-[11px] leading-snug text-warn-text">
      {t("warp.egress.none")}{" "}
      <Link to="/nodes" className={linkClass}>
        {t("warp.egress.open")}
      </Link>
    </span>
  );
}
