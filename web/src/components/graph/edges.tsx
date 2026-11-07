import { memo, useMemo, useState, type ReactNode } from "react";
import { useT } from "@/i18n";
import { nodeLabel } from "./context";
import { routeWire, type Route } from "./geometry";
import { useStoreVersion, type GraphStore } from "./store";
import type { EdgeState, GraphEdge, GraphNode } from "./types";

export interface RenderEdge extends GraphEdge {
  /** Drawn while a connect is in flight, or briefly after it failed. */
  virtual?: "pending" | "error";
  errorText?: string;
}

const stateColor: Record<EdgeState, string> = {
  ok: "var(--accent-mint)",
  warn: "var(--accent-sand)",
  bad: "var(--danger)",
  off: "var(--muted)",
  pending: "var(--accent)",
};

/** The wire's route from the ports' live positions (backward wires loop below both cards), or null while unmeasured. */
export function routeFor(store: GraphStore, e: Pick<GraphEdge, "from" | "to">): Route | null {
  const a = store.portWorld(e.from);
  const b = store.portWorld(e.to);
  if (!a || !b) return null;
  const ra = store.nodeRect(e.from.node);
  const rb = store.nodeRect(e.to.node);
  return routeWire(a, b, { clearY: ra && rb ? Math.max(ra.y + ra.h, rb.y + rb.h) + 32 : undefined });
}

/** Dark tones as they are; on the light theme the pastels are darkened a little (graph.css --wp). */
export const wireInk = (c: string) => `color-mix(in oklch, ${c} var(--wp, 100%), #000)`;

export function edgeColor(e: RenderEdge, srcType: string | undefined, typeColors: Record<string, string>): string {
  const s: EdgeState | undefined = e.virtual === "pending" ? "pending" : e.virtual === "error" ? "bad" : e.state;
  return s ? stateColor[s] : ((srcType && typeColors[srcType]) ?? "var(--muted)");
}

interface Props {
  store: GraphStore;
  nodes: GraphNode[];
  edges: RenderEdge[];
  typeColors: Record<string, string>;
  selectedEdge: string | null;
  hiddenEdge: string | null;
  onEdgeClick: (id: string) => void;
  onEdgeFocus: (id: string) => void;
}

/** The wires, under the cards: re-rendered by moves and measurements only (it reads the store, not the node list's state). */
export const EdgeLayer = memo(function EdgeLayer({ store, nodes, edges, typeColors, selectedEdge, hiddenEdge, onEdgeClick, onEdgeFocus }: Props) {
  useStoreVersion(store, "geo");
  const t = useT();
  const [hover, setHover] = useState<string | null>(null);
  const types = useMemo(() => {
    const m = new Map<string, string>();
    for (const n of nodes) for (const p of n.ports) m.set(`${n.id}\u0001${p.id}`, p.type);
    return m;
  }, [nodes]);
  const titles = useMemo(() => new Map(nodes.map((n) => [n.id, nodeLabel(n)])), [nodes]);

  const drawn: { e: RenderEdge; route: Route; color: string }[] = [];
  for (const e of edges) {
    if (e.id === hiddenEdge) continue;
    const route = routeFor(store, e);
    if (!route) continue;
    drawn.push({ e, route, color: wireInk(edgeColor(e, types.get(`${e.from.node}\u0001${e.from.port}`), typeColors)) });
  }

  const labels: ReactNode[] = [];
  return (
    <>
      <svg className="mg-gsvg" aria-hidden={false} focusable="false">
        {drawn.map(({ e, route, color }) => {
          const sel = selectedEdge === e.id;
          const hot = sel || hover === e.id;
          const off = e.state === "off" && !e.virtual;
          const label = e.virtual === "error" ? e.errorText : e.label;
          if (label) {
            labels.push(
              <div
                key={e.id}
                className="mg-glabel"
                data-error={e.virtual === "error" || undefined}
                style={{ left: route.mid.x, top: route.mid.y }}
                onClick={() => e.virtual !== "error" && onEdgeClick(e.id)}
              >
                {label}
              </div>,
            );
          }
          const stateWord = t(`graph.edge.${e.virtual === "pending" ? "pending" : (e.state ?? "plain")}` as "graph.edge.ok");
          return (
            <g key={e.id} className="mg-gedge" data-selected={sel || undefined} data-state={e.virtual ?? e.state}>
              {e.virtual !== "error" && (
                <path
                  d={route.d}
                  className="mg-gwire-hit"
                  data-gedge={e.id}
                  tabIndex={e.virtual ? -1 : 0}
                  role="button"
                  aria-label={t("graph.edge", { from: titles.get(e.from.node) ?? e.from.node, to: titles.get(e.to.node) ?? e.to.node, state: stateWord })}
                  onPointerEnter={() => setHover(e.id)}
                  onPointerLeave={() => setHover((h) => (h === e.id ? null : h))}
                  onClick={() => onEdgeClick(e.id)}
                  onFocus={() => onEdgeFocus(e.id)}
                />
              )}
              {hot && <path d={route.d} className="mg-gwire-halo" style={{ stroke: color }} />}
              <path
                d={route.d}
                className={e.virtual === "pending" ? "mg-gwire mg-gwire-pending" : "mg-gwire"}
                style={{ stroke: color, strokeWidth: hot ? 2.5 : 2, strokeDasharray: off ? "5 6" : undefined, opacity: off ? 0.75 : e.animated ? 0.45 : 1 }}
              />
              {e.animated && !off && !e.virtual && <path d={route.d} className="mg-gwire mg-gwire-flow" style={{ stroke: color, strokeWidth: hot ? 2.5 : 2 }} />}
            </g>
          );
        })}
      </svg>
      <div className="mg-glabels">{labels}</div>
    </>
  );
});
