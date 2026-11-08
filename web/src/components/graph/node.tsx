import { memo, useCallback, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { cx } from "@/lib/cx";
import { useT } from "@/i18n";
import { NodeContext, nodeLabel, textOf, useGraph, type NodeCtx } from "./context";
import { portKey, type GraphNode, type GraphPort, type Pos } from "./types";
import { useContext } from "react";

function PortDot({ node, port, anchor }: { node: GraphNode; port: GraphPort; anchor?: boolean }) {
  const g = useGraph();
  const t = useT();
  const ref = { node: node.id, port: port.id };
  const key = portKey(ref);
  const h = g.highlight;
  const role = !h ? undefined : key === h.source ? "source" : key === h.current ? "current" : h.targets.has(key) ? "target" : "dim";
  const connected = g.connected.has(key);
  const name = textOf(port.label) || port.id;
  const label = t(port.side === "in" ? "graph.port.in" : "graph.port.out", {
    node: nodeLabel(node),
    port: name,
    type: port.type,
    state: t(connected ? "graph.port.connected" : "graph.port.free"),
  });
  return (
    <button
      type="button"
      className={anchor ? "mg-gport mg-ganchor" : "mg-gport"}
      tabIndex={anchor ? -1 : undefined}
      data-gport={key}
      data-port-id={port.id}
      data-side={port.side}
      data-connected={connected || undefined}
      data-status={port.status}
      data-role={role}
      data-disabled={port.disabled || undefined}
      aria-label={label}
      aria-disabled={port.disabled || undefined}
      style={{ "--pc": g.typeColors[port.type] ?? "var(--muted)" } as React.CSSProperties}
      onKeyDown={(e) => g.onPortKeyDown(e, ref)}
    />
  );
}

/** The labels of a row's ports: the input's at the start, the output's at the end. */
function PortLabels({ ports }: { ports: GraphPort[] }) {
  const left = ports.find((p) => p.side === "in");
  const right = ports.find((p) => p.side === "out");
  const name = (p?: GraphPort) => (p ? (p.label ?? p.id) : null);
  if (left && right) {
    return (
      <span className="flex items-center justify-between gap-3 text-muted">
        <span className="min-w-0 truncate">{name(left)}</span>
        <span className="min-w-0 truncate">{name(right)}</span>
      </span>
    );
  }
  return <span className="text-muted">{name(left ?? right)}</span>;
}

function RowShell({ node, ports, children, align, className }: { node: GraphNode; ports: GraphPort[]; children: ReactNode; align?: "start" | "end"; className?: string }) {
  const left = ports.find((p) => p.side === "in");
  const right = ports.find((p) => p.side === "out");
  const a = align ?? (right && !left ? "end" : "start");
  return (
    <div className={cx("relative flex min-h-[30px] items-center px-3.5 py-1 text-[12.5px] leading-tight", className)}>
      {left && <PortDot node={node} port={left} />}
      <div className={cx("min-w-0 flex-1", a === "end" ? "text-end" : "text-start")}>{children}</div>
      {right && <PortDot node={node} port={right} />}
    </div>
  );
}

/**
 * A row of a node's body with ports on its edges: an input port on the left, an output port on the right, both centred
 * on the row. `port` is a port id or two (an input and an output). Without children the ports' labels are shown.
 */
export function GraphPortRow({ port, children, align, className }: { port: string | string[]; children?: ReactNode; align?: "start" | "end"; className?: string }) {
  const card = useContext(NodeContext);
  if (!card) throw new Error("GraphPortRow must be rendered in a graph node's body");
  const ids = Array.isArray(port) ? port : [port];
  const idsKey = ids.join("\u0001");
  const { claim, release } = card;
  useLayoutEffect(() => {
    const list = idsKey.split("\u0001");
    list.forEach(claim);
    return () => list.forEach(release);
  }, [idsKey, claim, release]);
  const ports = ids.map((id) => card.node.ports.find((p) => p.id === id)).filter((p): p is GraphPort => !!p);
  return (
    <RowShell node={card.node} ports={ports} align={align} className={className}>
      {children ?? <PortLabels ports={ports} />}
    </RowShell>
  );
}

interface CardProps {
  node: GraphNode;
  pos: Pos | undefined;
  selected: boolean;
  /** Off the paths of a focus (focusPaths): drawn faint. */
  dim?: boolean;
}

export const NodeCard = memo(function NodeCard({ node, pos, selected, dim }: CardProps) {
  const g = useGraph();
  const t = useT();
  const ref = useRef<HTMLDivElement>(null);
  const [claimed, setClaimed] = useState<ReadonlySet<string>>(new Set());
  const claim = useCallback((id: string) => setClaimed((s) => (s.has(id) ? s : new Set(s).add(id))), []);
  const release = useCallback((id: string) => {
    setClaimed((s) => {
      if (!s.has(id)) return s;
      const n = new Set(s);
      n.delete(id);
      return n;
    });
  }, []);
  const ctx = useMemo<NodeCtx>(() => ({ node, claim, release }), [node, claim, release]);
  const { store } = g;

  // the card's own transform is written here and by drags/animations (store.setLive), never by a style prop
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || !pos) return;
    el.style.transform = `translate(${pos.x}px, ${pos.y}px)`;
  }, [pos]);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    store.cardEls.set(node.id, el);
    return () => {
      if (store.cardEls.get(node.id) === el) store.cardEls.delete(node.id);
    };
  }, [store, node.id]);

  const measure = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    const z = store.view.z || 1;
    const r = el.getBoundingClientRect();
    const ports = new Map<string, Pos>();
    el.querySelectorAll<HTMLElement>("[data-gport]").forEach((p) => {
      const pr = p.getBoundingClientRect();
      ports.set(p.dataset.portId!, { x: (pr.left + pr.width / 2 - r.left) / z, y: (pr.top + pr.height / 2 - r.top) / z });
    });
    store.setMeasure(node.id, { w: el.offsetWidth, h: el.offsetHeight, ports });
  }, [store, node.id]);

  useLayoutEffect(measure);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [measure]);
  useLayoutEffect(() => () => store.dropNode(node.id), [store, node.id]);

  // ports no row claimed: inputs on the left and outputs on the right, paired by order
  const rest = node.ports.filter((p) => !claimed.has(p.id));
  const ins = rest.filter((p) => p.side === "in");
  const outs = rest.filter((p) => p.side === "out");
  const rows = Array.from({ length: Math.max(ins.length, outs.length) }, (_, i) => [ins[i], outs[i]].filter((p): p is GraphPort => !!p));
  const title = nodeLabel(node);

  return (
    <NodeContext.Provider value={ctx}>
      <div
        ref={ref}
        className="mg-gnode"
        data-variant={node.variant}
        data-gnode={node.id}
        data-tone={node.tone}
        data-selected={selected || undefined}
        data-dim={dim || undefined}
        data-fixed={node.draggable === false || undefined}
        role="group"
        aria-label={title}
        aria-roledescription={t("graph.node")}
        tabIndex={0}
        style={{ width: node.width ?? 220, visibility: pos ? undefined : "hidden" }}
        onFocus={(e) => {
          if (e.target === e.currentTarget) g.onFocusNode(node.id);
        }}
      >
        {!node.headless && (
        <div className="mg-ghead">
          {node.icon && (
            <span className="id-chip size-6 shrink-0" aria-hidden>
              {node.icon}
            </span>
          )}
          <span className="min-w-0 flex-1 truncate">{node.title}</span>
        </div>
        )}
        {node.body && <div className={node.headless ? "" : "py-1.5"}>{node.body}</div>}
        {node.anchors && rest.map((p) => <PortDot key={p.id} node={node} port={p} anchor />)}
        {!node.anchors && rows.length > 0 && (
          <div className={cx("py-1", node.body ? "border-t border-line" : "")}>
            {rows.map((ports, i) => (
              <RowShell key={i} node={node} ports={ports}>
                <PortLabels ports={ports} />
              </RowShell>
            ))}
          </div>
        )}
      </div>
    </NodeContext.Provider>
  );
});
