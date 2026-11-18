import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState, type DragEvent, type ReactNode } from "react";
import { Graph, HelpGroup, Kbd, KeyRow, computeLayout, type GraphEdge, type GraphNode } from "@/components/graph";
import "./nodes.css";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { Pending } from "@/components/ui/query-error";
import { StatusDot, type StatusKind } from "@/components/ui/status";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { Switch } from "@/components/ui/switch";
import { Button, buttonClass } from "@/components/ui/button";
import { useToast } from "@/components/ui/toast";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { agentLinked } from "@/lib/node-status";
import { meQuery } from "@/lib/session";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { findNodes, fleetMap, relayPairRefusal, type FleetNodeIn, type FmNode, type FmVerdict } from "@/lib/fleet-map";
import { countryQuery } from "@/lib/globalping";
import { checksQuery, countryColumn } from "@/lib/health";
import type { Plain } from "@/lib/plain";
import { nodeQuery } from "@/lib/queries";
import { relaysQuery, type RelayMsg } from "@/lib/relay";
import { routeMap } from "@/lib/route-map";
import { hintOf } from "@/lib/ways";
import { useOpenWizard } from "@/screens/node/relay-open";
import { RemoveRelay, ChangeRelayPort, useRelayRuns, type Entry } from "@/screens/node/relays";
import { usePortCheck } from "@/screens/node/ports";
import { useInSubscriptions } from "@/screens/node/subs-switches";
import { useWays } from "@/screens/node/ways";
import { badgeKind, say } from "@/screens/node/ways-graph";
import { Badge, PopItem } from "@/screens/node/ways-map-parts";

// The Nodes page's Map view: People on the left, the nodes in columns by role, the exits on the right; a wire per way people
// reach a node, per pair of nodes a relay joins, and per node to its exits. The mapping is lib/fleet-map.ts's (pure, with
// tests). Clicking a node or a wire opens its drawer; double-clicking a node opens the node's page on its route map; dragging
// "Relay from here" onto another node opens the relay dialog, prefilled. Everything is the owner's to change.

type Node = Plain<NodeMsg>;
const NODE = "application/x-mg-node";
const colors = { udp: "var(--muted)" };

const statusKind: Record<FmNode["status"], StatusKind> = { online: "ok", blip: "blip", offline: "bad", pending: "off", updating: "busy" };
const verdictOf = (word: string | undefined): FmVerdict => (word === "ok" ? "ok" : word === "partial" ? "partial" : word === "blocked" || word === "listed" ? "blocked" : "unknown");

type Dialog = { kind: "draw"; entry: Entry; exitId?: string } | { kind: "remove"; r: RelayMsg } | { kind: "port"; r: RelayMsg };

export function FleetMap({ nodes, owner, focus, onFocus, onOpenNode }: { nodes: Node[]; owner: boolean; focus?: string; onFocus?: (id: string | undefined) => void; onOpenNode?: (id: string) => void }) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const navigate = useNavigate();
  const relaysQ = useQuery({ ...relaysQuery(""), enabled: owner });
  const checks = useQuery(checksQuery(""));
  const asked = useQuery({ ...countryQuery, enabled: owner }).data?.country;
  const [dlg, setDlg] = useState<Exclude<Dialog, { kind: "draw" }> | null>(null);
  // a relay is drawn in the wizard on the exit's page; an entry with no exit chosen goes to the list of the ways, where the exits wait for one
  const openWizard = useOpenWizard();
  const onDialog = (d: Dialog) => (d.kind !== "draw" ? setDlg(d) : d.exitId ? openWizard(d.exitId, d.entry.id) : void navigate({ to: "/nodes", search: { view: "ways" } }));
  const [multi, setMulti] = useState<string[]>([]);
  const [wireId, setWireId] = useState<string | null>(null);
  const [q, setQ] = useState("");

  const column = countryColumn(checks.data?.countryChecks);
  const country = column?.code || asked || "RU";
  const verdicts = new Map<string, FmVerdict>();
  for (const [id, c] of column?.byNode ?? []) verdicts.set(id, verdictOf(hintOf(c)?.verdict));
  const nodesIn: FleetNodeIn[] = nodes.map((n) => ({
    id: n.id,
    name: n.name,
    countryCode: n.countryCode,
    location: n.location,
    status: n.status,
    protocols: n.protocols,
    online: n.online.reduce((a, o) => a + o.users, 0),
    cpu: n.hasMetrics ? n.cpuPct : null,
    warp: n.warp?.state ?? 0,
  }));
  const model = fleetMap({ nodes: nodesIn, relays: relaysQ.data ?? [], verdicts, country });
  const nodeById = (id: string) => nodes.find((n) => n.id === id);

  // the URL keeps the selected node (/nodes?view=map&focus=<id>) unless the page that hosts the map says otherwise
  const go = (search: { view?: "map"; focus?: string }) => (onFocus ? onFocus(search.focus) : void navigate({ to: "/nodes", search, replace: true }));
  const sel = multi.length > 1 ? multi : focus ? [`node:${focus}`] : [];
  const wire = wireId ? model.wires.find((w) => w.id === wireId) : undefined;
  const selected = sel.length === 1 && !wire ? model.nodes.find((n) => n.id === sel[0]) : undefined;

  // ---- relays: drag "Relay from here" onto another node
  const dropOn = (target: FmNode) => (e: DragEvent) => {
    const id = e.dataTransfer.getData(NODE);
    if (!owner || !id) return;
    e.preventDefault();
    e.stopPropagation();
    const why = relayPairRefusal(id, target.nodeId, nodesIn);
    if (why) return toast.error(t(`fm.refuse.${why}` as MessageKey));
    const entry = nodeById(id)!;
    onDialog({ kind: "draw", entry: { id, name: entry.name, cc: entry.countryCode }, exitId: target.nodeId });
  };
  const overNode = (e: DragEvent) => {
    if (owner && e.dataTransfer.types.includes(NODE)) e.preventDefault();
  };

  // a long column is split in two: the nodes relays reach stay next to their entries, the rest follows
  const layer3 = model.nodes.filter((n) => n.layer === 3 && n.relaysIn === 0);
  const crowded = model.nodes.filter((n) => n.layer === 3).length > 5;
  const rankOf = (n: FmNode) => (n.layer === 3 && crowded && n.relaysIn === 0 && layer3.indexOf(n) % 2 === 0 ? 3.5 : n.layer);

  // ---- the cards
  const nodeCard = (n: FmNode): GraphNode => {
    const src = nodeById(n.nodeId)!;
    const sub = [src.location || fmt.country(n.countryCode), n.load !== null ? t("fm.sub.full", { load: n.load, n: n.online }) : t("fm.sub.online", { n: n.online })].filter(Boolean).join(" · ");
    return {
      id: n.id,
      title: n.name,
      label: n.name,
      width: 224,
      headless: true,
      anchors: true,
      draggable: true,
      rank: rankOf(n),
      ports: [
        { id: "in", side: "in", type: "udp" },
        { id: "out", side: "out", type: "udp" },
      ],
      body: (
        <div className={cx("grid h-14 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2.5 px-3", n.status === "offline" && "fz-bad")} onDragOver={overNode} onDrop={dropOn(n)}>
          <Flag code={n.countryCode} size={16} />
          <div className="min-w-0">
            <b className="block truncate font-mono leading-5 font-medium tabular-nums">{n.name}</b>
            <span className="block truncate text-xs leading-4 text-muted">{n.status === "offline" ? t("fm.st.offline") : n.role === "entry" || n.role === "idle" ? [src.location || fmt.country(n.countryCode), t(n.role === "entry" ? "fm.entryOnly" : "fm.idle")].filter(Boolean).join(" · ") : sub}</span>
          </div>
          <span title={t(`fm.st.${n.status}`)} className="flex">
            <StatusDot kind={statusKind[n.status]} />
          </span>
        </div>
      ),
    };
  };
  const plain = (id: string, w: number, rank: number, body: ReactNode, label: string, ports: GraphNode["ports"]): GraphNode => ({ id, title: label, label, width: w, headless: true, anchors: true, draggable: true, selectable: false, rank, ports, body });
  // the cards of the two ends: the people at the start, the places the traffic leaves to at the end (the board's raised cards, 184px)
  const end = (glyph: ReactNode, chip: string, title: string, sub: string) => (
    <div className="fz-raise grid h-14 grid-cols-[auto_minmax(0,1fr)] items-center gap-2.5 px-3">
      <span aria-hidden className={cx("grid size-7 place-items-center rounded-ctl", chip)}>
        {glyph}
      </span>
      <div className="min-w-0">
        <b className="block truncate leading-5 font-medium">{title}</b>
        <span className="block truncate text-xs leading-4 text-muted">{sub}</span>
      </div>
    </div>
  );
  const graphNodes: GraphNode[] = [
    plain(
      "people",
      184,
      0,
      end(<Icon name="people" size={16} />, "bg-step text-sec", t("fm.people", { country: fmt.country(country) }), t("fm.people.sub")),
      t("fm.people", { country: fmt.country(country) }),
      [{ id: "out", side: "out", type: "udp" }],
    ),
    ...model.nodes.map(nodeCard),
    ...model.exits.map((e) =>
      plain(
        `exit:${e}`,
        184,
        4,
        end(<Icon name={e === "warp" ? "cloud" : "globe"} size={16} />, e === "warp" ? "bg-tone-sand-bg text-tone-sand" : "bg-tone-sky-bg text-tone-sky", t(e === "warp" ? "fm.warp" : "fm.internet"), t(e === "warp" ? "fm.warp.sub" : "fm.internet.sub")),
        t(e === "warp" ? "fm.warp" : "fm.internet"),
        [{ id: "in", side: "in", type: "udp" }],
      ),
    ),
  ];
  const edges: GraphEdge[] = model.wires.map((w) => ({
    id: w.id,
    from: { node: w.from, port: "out" },
    to: { node: w.to, port: "in" },
    state: w.style === "flow" ? "ok" : w.style === "partial" ? "warn" : w.style === "fail" ? "bad" : w.style === "plain" ? undefined : "blocked",
    animated: w.style === "flow",
    weight: Math.min(3.5, 2 + (w.count - 1) * 0.25),
    label: w.label ? <span className="text-[11px]">{say(t, w.label)}</span> : undefined,
    grabbable: false,
  }));

  // ---- the search box
  const matches = findNodes(model.nodes.map((n) => ({ id: n.nodeId, name: n.name })), q);
  const focusNode = (id: string) => {
    setWireId(null);
    setMulti([]);
    setQ("");
    go({ view: "map", focus: id });
  };

  const graph = (
    <Graph
      nodes={graphNodes}
      edges={edges}
      typeColors={colors}
      ariaLabel={t("fm.fleetMap")}
      height={680}
      layoutKey="fleetmap"
      autoArrange
      arrange={(sizes, es) => computeLayout(sizes, es, { colGap: 76, rowGap: 14 })}
      minFitZoom={0.7}
      fitPad={44}
      readOnly
      focusPaths
      selected={sel}
      onSelectionChange={(ids) => {
        const nodeIds = ids.filter((i) => i.startsWith("node:"));
        if (nodeIds.length > 1) setMulti(nodeIds);
        else {
          setMulti([]);
          go({ view: "map", ...(nodeIds[0] ? { focus: nodeIds[0].slice(5) } : {}) });
        }
      }}
      onEdgeSelect={setWireId}
      onNodeActivate={(id) => id.startsWith("node:") && (onOpenNode ? onOpenNode(id.slice(5)) : void navigate({ to: "/nodes/$id", params: { id: id.slice(5) }, search: { tab: "ways", view: "graph", from: "map" } }))}
      legend={<FleetLegend t={t} />}
      extraShortcuts={owner ? <FleetShortcuts t={t} /> : undefined}
    />
  );

  if (!nodes.length || checks.isPending) return <Pending />;
  return (
    <div className="flex min-w-0 flex-col gap-2.5">
      <div className="relative w-[260px]">
        <div className="relative">
          <Icon name="search" size={14} className="pointer-events-none absolute top-1/2 inset-s-3 -translate-y-1/2 text-muted" />
          <input
            aria-label={t("fm.search")}
            placeholder={t("fm.search")}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && matches[0]) focusNode(matches[0]);
              if (e.key === "Escape") setQ("");
            }}
            className="h-9 w-full rounded-field border border-line bg-surface ps-9 pe-3 text-sm outline-none transition-colors duration-150 focus:border-accent-line"
          />
        </div>
        {q.trim() && (
          <ul className="absolute z-20 mt-1 w-full rounded-pop border border-line bg-surface p-1.5 shadow-(--shadow-pop)">
            {matches.length === 0 && <li className="px-2.5 py-2 text-[12.5px] text-muted">{t("fm.searchNone")}</li>}
            {matches.slice(0, 6).map((id) => (
              <li key={id}>
                <PopItem onClick={() => focusNode(id)}>{nodeById(id)?.name}</PopItem>
              </li>
            ))}
          </ul>
        )}
      </div>
      <div className="fz-skin relative min-w-0">
        {graph}
        {(selected || wire) && (
          // over the canvas's right edge, under its toolbar: the map keeps its size and its fit
          <aside className="absolute inset-e-3 top-16 z-20 flex max-h-[calc(100%-76px)] w-80 max-w-[calc(100%-24px)] animate-[mg-up_0.25s_var(--e)_both] flex-col overflow-hidden rounded-card border border-line bg-surface-2 max-md:inset-s-3 max-md:top-auto max-md:bottom-11 max-md:w-auto" aria-label={t("fm.d.actions")}>
            {wire && wire.kind === "relay" && wire.relays ? (
              <WireDrawer key={wire.id} wire={wire} nodes={nodes} owner={owner} onDialog={onDialog} onClose={() => setWireId(null)} />
            ) : selected ? (
              <NodeDrawer key={selected.nodeId} id={selected.nodeId} node={nodeById(selected.nodeId)!} fm={selected} country={country} owner={owner} relays={relaysQ.data ?? []} onDialog={onDialog} onClose={() => go({ view: "map" })} />
            ) : null}
          </aside>
        )}
      </div>
      {owner && dlg?.kind === "remove" && <RemoveRelay r={dlg.r} onClose={() => setDlg(null)} />}
      {owner && dlg?.kind === "port" && <ChangeRelayPort r={dlg.r} onClose={() => setDlg(null)} />}
    </div>
  );
}

/** The head of a drawer (the board's card): what it is about at the start, a cross at the end. */
function DrawerHead({ title, sub, onClose, children }: { title: ReactNode; sub?: ReactNode; onClose: () => void; children?: ReactNode }) {
  const t = useT();
  return (
    <header className="flex flex-col gap-1 px-4 pt-4">
      <div className="flex items-center gap-2">
        <div className="flex min-w-0 flex-1 items-center gap-2 text-base leading-6 font-medium">{title}</div>
        <button type="button" aria-label={t("fm.d.close")} onClick={onClose} className="-my-1 -me-2 grid size-8 flex-none place-items-center rounded-field text-sec transition-colors duration-150 ease-out hover:bg-step hover:text-fg">
          <Icon name="x" size={16} />
        </button>
      </div>
      {sub && <p className="text-[13px] text-muted">{sub}</p>}
      {children}
    </header>
  );
}

/** A section of a drawer: a hairline above it, its small heading, its count at the end. */
function Section({ title, count, children }: { title: ReactNode; count?: ReactNode; children: ReactNode }) {
  return (
    <section className="flex flex-col border-t border-line px-4 py-3 first:border-t-0">
      <h4 className="mb-1 flex items-baseline justify-between text-[13px] font-medium text-sec">
        {title}
        {count !== undefined && <span className="text-xs font-normal text-muted">{count}</span>}
      </h4>
      {children}
    </section>
  );
}
const muted = "text-[13px] leading-6 text-muted";
const footerGrid = "grid grid-cols-2 gap-2 border-t border-line px-4 pt-3 pb-4";

function NodeDrawer({ id, node, fm, country, owner, relays, onDialog, onClose }: { id: string; node: Node; fm: FmNode; country: string; owner: boolean; relays: RelayMsg[]; onDialog: (d: Dialog) => void; onClose: () => void }) {
  const q = useQuery(nodeQuery(id));
  const t = useT();
  const fmt = useFmt();
  const word = t(`fm.st.${fm.status}`);
  return (
    <>
      <DrawerHead
        title={
          <>
            <Flag code={fm.countryCode} size={16} />
            <b className="font-mono font-medium tabular-nums">{fm.name}</b>
            <span className={cx("inline-flex items-center gap-1.5 text-xs font-normal text-sec", `tone-${statusKind[fm.status]}`, fm.status === "offline" || fm.status === "blip" || fm.status === "updating" ? "tone-text" : "")}>
              <StatusDot kind={statusKind[fm.status]} />
              {word}
            </span>
          </>
        }
        sub={
          <>
            {[node.location || fmt.country(fm.countryCode)].filter(Boolean).join("")}
            {node.location || fm.countryCode ? " · " : ""}
            <span className="font-mono">{node.address}</span>
          </>
        }
        onClose={onClose}
      />
      <dl className="mx-4 mt-3 grid grid-cols-3 gap-2 pb-3">
        {[
          [t("nodes.col.online"), String(fm.online)],
          [t("nodes.col.today"), fmt.bytes(node.trafficTodayBytes)],
          [t("nodes.col.cpu"), node.hasMetrics ? `${Math.round(node.cpuPct)}%` : "—"],
        ].map(([k, v]) => (
          <div key={k}>
            <dt className="text-[12px] text-muted">{k}</dt>
            <dd className="mt-0.5 font-mono text-sm font-medium tabular-nums">{v}</dd>
          </div>
        ))}
      </dl>
      {q.data ? (
        <NodeDrawerBody data={q.data} id={id} fm={fm} country={country} owner={owner} relays={relays} onDialog={onDialog} />
      ) : (
        <div className="border-t border-line p-4">
          <Pending />
        </div>
      )}
    </>
  );
}

function NodeDrawerBody({ data, id, fm, country, owner, relays, onDialog }: { data: Plain<import("@/gen/mistgate/admin/v1/node_pb").GetNodeResponse>; id: string; fm: FmNode; country: string; owner: boolean; relays: RelayMsg[]; onDialog: (d: Dialog) => void }) {
  const t = useT();
  const w = useWays(data);
  const subs = useInSubscriptions(id, fm.name);
  const ports = usePortCheck(id);
  const role = useQuery(meQuery).data?.admin?.role;
  const model = routeMap({ node: { id, name: fm.name, address: data.node!.address, countryCode: fm.countryCode }, inbounds: data.inbounds, ways: w, country, warp: false, ipv6: { fact: w.ipv6, v6: w.v6 }, relaysKnown: owner });
  const out = relays.filter((r) => r.viaNodeId === id);
  const inn = relays.filter((r) => r.exitNodeId === id);
  const dotOf = (s: InboundState): StatusKind => (s === InboundState.ACTIVE ? "ok" : s === InboundState.FAILED ? "bad" : s === InboundState.DISABLED ? "off" : "warn");
  const inSubs = data.inbounds.filter((i) => i.directInSubscriptions).length;
  const linked = agentLinked(data.node!.status);
  return (
    <>
      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain border-t border-line [scrollbar-width:thin]">
        <Section title={t("fm.d.servers")} count={data.inbounds.length > 0 ? t("nv.map.inSubs", { n: String(inSubs), m: String(data.inbounds.length) }) : undefined}>
          {data.inbounds.length === 0 && <p className={muted}>{t("fm.idle")}</p>}
          {data.inbounds.map((i) => (
            <div key={i.id} className="grid h-10 grid-cols-[28px_minmax(0,1fr)_auto_36px] items-center gap-2.5">
              <ProtocolTag protocol={i.protocol} compact />
              <b className="truncate font-medium">{i.profileName}</b>
              <span className="flex items-center gap-2 font-mono text-xs text-muted">
                <StatusDot kind={dotOf(i.state)} />
                {i.port}
              </span>
              {owner && !i.cdn && i.enabled ? (
                <Switch checked={i.directInSubscriptions} disabled={subs.busy} aria-label={t("ways.subs.switch", { name: i.profileName })} onCheckedChange={(on) => void subs.toggle(i, on, t(on ? "wg.subs.in" : "wg.subs.out", { name: i.profileName }))} />
              ) : (
                <span />
              )}
            </div>
          ))}
        </Section>
        <Section
          title={t("fm.d.ways")}
          count={
            <Link to="/nodes/$id" params={{ id }} search={{ tab: "ways", view: "graph", from: "map" }} className="text-sec underline decoration-line underline-offset-4 hover:text-fg hover:decoration-current">
              {t("fm.waysMap")}
            </Link>
          }
        >
          {model.ways.length === 0 && <p className={muted}>{t("fm.d.noWays")}</p>}
          {model.ways.map((x) => (
            <div key={x.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 py-1.5">
              <div className="min-w-0">
                <b className="block truncate font-medium">{x.kind === "via" ? t("rm.way.via", { node: x.peer?.name ?? "" }) : t(`rm.way.${x.kind}` as MessageKey)}</b>
                <span className="block text-xs leading-4 text-muted">{say(t, x.sub)}</span>
              </div>
              <Badge kind={badgeKind[x.state]}>{say(t, x.badge)}</Badge>
            </div>
          ))}
        </Section>
        <Section title={t("fm.d.relaysOut")}>
          {out.length === 0 ? <p className={muted}>{t("fm.d.none")}</p> : <RelayLines list={out} side="entry" />}
          {owner && fm.status === "online" && (
            <button
              type="button"
              draggable
              onDragStart={(e) => e.dataTransfer.setData(NODE, id)}
              title={t("fm.relayHandleHint")}
              onClick={() => onDialog({ kind: "draw", entry: { id, name: fm.name, cc: fm.countryCode } })}
              className="mt-1 -ms-2 inline-flex h-8 cursor-pointer items-center gap-2 self-start rounded-ctl ps-2 pe-2.5 text-[13px] text-sec transition-colors duration-150 ease-out hover:bg-step hover:text-fg"
            >
              <Icon name="plus" size={16} />
              {t("fm.d.addRelay")}
            </button>
          )}
        </Section>
        <Section title={t("fm.d.relaysIn")}>{inn.length === 0 ? <p className={muted}>{t("fm.d.none")}</p> : <RelayLines list={inn} side="exit" />}</Section>
      </div>
      <footer className={footerGrid}>
        <Link to="/nodes/$id" params={{ id }} search={{ from: "map" }} className={cx(buttonClass("secondary", "lg"), "justify-center gap-2 px-2")}>
          {t("fm.openNode")}
        </Link>
        {(role === Role.OWNER || role === Role.HELPER) && (
          <Button variant="secondary" size="lg" className="justify-center gap-2 px-2" disabled={ports.isPending || !linked} title={linked ? undefined : t("ports.offline")} onClick={() => ports.mutate()}>
            {ports.isPending ? <span aria-hidden className="size-3.5 animate-[mg-ui-spin_0.7s_linear_infinite] rounded-full border-[1.5px] border-current border-t-transparent" /> : null}
            {ports.isPending ? t("ports.checking") : t("ports.check")}
          </Button>
        )}
      </footer>
      {subs.dialog}
    </>
  );
}

function RelayLines({ list, side }: { list: RelayMsg[]; side: "entry" | "exit" }) {
  return (
    <ul className="flex flex-col">
      {list.map((r) => (
        <li key={r.id} className="grid h-8 grid-cols-[8px_minmax(0,1fr)_auto] items-center gap-2.5">
          <StatusDot kind={r.enabled ? (r.state === InboundState.ACTIVE ? "ok" : r.state === InboundState.FAILED ? "bad" : "warn") : "off"} />
          <b className="truncate font-medium">
            {r.profileName} · {side === "entry" ? r.exitNodeName : r.viaNodeName}
          </b>
          <span className="font-mono text-xs text-muted">
            {r.network}/{r.port}
          </span>
        </li>
      ))}
    </ul>
  );
}

function WireDrawer({ wire, nodes, owner, onDialog, onClose }: { wire: NonNullable<ReturnType<typeof fleetMap>["wires"][number]>; nodes: Node[]; owner: boolean; onDialog: (d: Dialog) => void; onClose: () => void }) {
  const t = useT();
  const runs = useRelayRuns();
  const list = wire.relays ?? [];
  const first = list[0]!;
  const entry = nodes.find((n) => n.id === first.viaNodeId);
  return (
    <>
      <DrawerHead
        title={
          <b className="font-mono font-medium">
            {first.viaNodeName} → {first.exitNodeName}
          </b>
        }
        sub={t.n("fm.w.sub", list.length)}
        onClose={onClose}
      />
      <div className="mt-3 min-h-0 flex-1 overflow-y-auto overscroll-contain border-t border-line px-4 py-3 [scrollbar-width:thin]">
        <div className="flex flex-col gap-2">
          {list.map((r) => (
            <div key={r.id} className="flex flex-col gap-1 rounded-field border border-line p-2.5">
              <div className="grid grid-cols-[8px_minmax(0,1fr)_auto] items-center gap-2.5">
                <StatusDot kind={r.enabled ? (r.state === InboundState.ACTIVE ? "ok" : r.state === InboundState.FAILED ? "bad" : "warn") : "off"} />
                <b className="truncate font-medium">{r.profileName}</b>
                <span className="font-mono text-xs text-muted">
                  {r.network}/{r.port}
                </span>
              </div>
              {owner && (
                <div className="flex flex-wrap gap-1">
                  <Button size="xs" variant="ghost" onClick={() => runs.check(r)}>
                    {t("wg.menu.check")}
                  </Button>
                  <Button size="xs" variant="ghost" onClick={() => (r.enabled ? runs.off(r) : runs.on(r))}>
                    {r.enabled ? t("wg.menu.off") : t("wg.menu.on")}
                  </Button>
                  <Button size="xs" variant="ghost" onClick={() => onDialog({ kind: "port", r })}>
                    {t("wg.menu.port")}
                  </Button>
                  <Button size="xs" variant="ghost" onClick={() => onDialog({ kind: "remove", r })}>
                    {t("wg.menu.remove")}
                  </Button>
                </div>
              )}
            </div>
          ))}
        </div>
      </div>
      {owner && (
        <footer className={footerGrid}>
          <Button variant="secondary" size="lg" className="justify-center gap-2 px-2" onClick={() => void runs.checkAll(list)}>
            {t("wg.menu.check")}
          </Button>
          <Button variant="secondary" size="lg" className="justify-center gap-2 px-2" onClick={() => onDialog({ kind: "draw", entry: { id: first.viaNodeId, name: first.viaNodeName, cc: entry?.countryCode ?? "" }, exitId: first.exitNodeId })}>
            <Icon name="plus" size={16} />
            {t("fm.w.add")}
          </Button>
        </footer>
      )}
    </>
  );
}

function FleetLegend({ t }: { t: T }) {
  const line = (cls: string, color: string, dash?: string, opacity = 1, width = 2) => (
    <svg width="28" height="10" aria-hidden className="shrink-0">
      <line x1="2" y1="5" x2="26" y2="5" className={cls} style={{ stroke: `color-mix(in oklch, ${color} var(--wp, 100%), #000)`, strokeWidth: width, strokeLinecap: "round", strokeDasharray: dash, opacity }} />
    </svg>
  );
  const row = (glyph: ReactNode, text: string) => (
    <li className="flex h-7 items-center gap-2">
      <span className="flex w-7 shrink-0 items-center justify-center">{glyph}</span>
      <span>{text}</span>
    </li>
  );
  return (
    <div className="flex w-full flex-col gap-3 text-[12px]">
      <p className="text-[13px] font-semibold">{t("graph.legend")}</p>
      <HelpGroup title={t("wg.legend.lines")}>
        <ul>
          {row(line("mg-gwire mg-gwire-flow", "var(--accent-mint)", undefined, 1, 2.5), t("rm.lg.flow"))}
          {row(line("mg-gwire", "var(--accent-sand)"), t("rm.lg.partial"))}
          {row(line("mg-gwire", "var(--muted)", "1.5 5", 0.35, 1.5), t("rm.lg.muted"))}
          {row(line("mg-gwire", "var(--accent-rose)", undefined, 1, 1.5), t("rm.lg.fail"))}
        </ul>
      </HelpGroup>
      <HelpGroup title={t("rm.lg.cards")}>
        <ul>
          {row(<Badge kind="busy">{t("fm.entryOnly")}</Badge>, t("fm.lg.entry"))}
          {row(<Icon name="server" size={15} />, t("fm.lg.serve"))}
        </ul>
      </HelpGroup>
    </div>
  );
}

function FleetShortcuts({ t }: { t: T }) {
  return (
    <HelpGroup title={t("fm.k.group")}>
      <ul className="flex flex-col gap-1">
        <KeyRow keys={<Kbd>{t("graph.key.dblclick")}</Kbd>}>{t("fm.k.open")}</KeyRow>
        <KeyRow keys={<Kbd>{t("fm.key.handle")}</Kbd>}>{t("fm.k.relay")}</KeyRow>
      </ul>
    </HelpGroup>
  );
}

