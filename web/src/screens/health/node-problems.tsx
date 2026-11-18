import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Flag } from "@/components/ui/flag";
import { useT } from "@/i18n";
import { isProblem, nodeKind, useNodeStatus } from "@/lib/node-status";
import { overviewQuery } from "@/lib/queries";
import { Dot, Gl, P, Sec } from "./page-parts";
import "./health.css";

/** The nodes that count as a problem but have no alert (yet, or by nature: a node without profiles, a profile that did not start). */
export function useProblemNodes(covered: ReadonlySet<string>) {
  const cards = useQuery(overviewQuery()).data?.nodes ?? [];
  return cards.filter((n) => isProblem(n) && !covered.has(n.id));
}

/**
 * Nodes that count as a problem but have no alert. They are in the number of the header, so they are named here, each a line to where it is fixed:
 * the profiles of a node that has none or whose profile did not start, the node itself for the rest.
 */
export function NodeProblems({ nodes }: { nodes: ReturnType<typeof useProblemNodes> }) {
  const t = useT();
  const st = useNodeStatus();
  if (nodes.length === 0) return null;
  return (
    <Sec tone="sky" icon={P.server} title={t("hl.nodes.title")} count={nodes.length}>
      <div className="hl-nodes hl-np">
        {nodes.map((n) => {
          const toProfiles = n.reason?.code === "no_profiles" || n.reason?.code === "inbound_failed";
          const kind = nodeKind(n);
          return (
            <Link key={n.id} to="/nodes/$id" params={{ id: n.id }} search={toProfiles ? { tab: "profiles" } : {}}>
              <Flag code={n.countryCode ?? ""} size={13} />
              <b className="mono">{n.name}</b>
              <span className={kind === "bad" ? "crit" : "warn"}>
                <Dot kind={kind === "bad" ? "bad" : "part"} />
                {st.line(n)}
              </span>
              <Gl d={P.chev} className="sb-chev" />
            </Link>
          );
        })}
      </div>
    </Sec>
  );
}
