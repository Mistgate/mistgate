import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState, type DragEvent, type ReactNode } from "react";
import { Graph, HelpGroup, Kbd, KeyRow, computeLayout, type GraphEdge, type GraphNode } from "@/components/graph";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { Button } from "@/components/ui/button";
import { Pending } from "@/components/ui/query-error";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { findNodes, fleetMap, relayPairRefusal, type FleetNodeIn, type FmNode, type FmVerdict } from "@/lib/fleet-map";
import { countryQuery } from "@/lib/globalping";
import { checksQuery, countryColumn } from "@/lib/health";
import type { Plain } from "@/lib/plain";
import { nodeQuery } from "@/lib/queries";
import { relaysQuery, type RelayMsg } from "@/lib/relay";
import { routeMap } from "@/lib/route-map";
import { protocolShort } from "@/lib/series";
import { hintOf } from "@/lib/ways";
import { AddRelayDialog, RemoveRelay, ChangeRelayPort, useRelayRuns, type Entry } from "@/screens/node/relays";
import { CheckPortsButton, usePortCheck } from "@/screens/node/ports";
import { useInSubscriptions } from "@/screens/node/subs-switches";
import { useWays } from "@/screens/node/ways";
import { badgeKind, say } from "@/screens/node/ways-graph";
import { Badge, Dot, PopItem, type ChipKind } from "@/screens/node/ways-map-parts";

// The Nodes page's Map view: People on the left, the nodes in columns by role, the exits on the right; a wire per way people
// reach a node, per pair of nodes a relay joins, and per node to its exits. The mapping is lib/fleet-map.ts's (pure, with
// tests). Clicking a node or a wire opens its drawer; double-clicking a node opens the node's page on its route map; dragging
// "Relay from here" onto another node opens the relay dialog, prefilled. Everything is the owner's to change.

type Node = Plain<NodeMsg>;
const NODE = "application/x-mg-node";
const colors = { udp: "var(--muted)" };

const statusDot: Record<FmNode["status"], ChipKind> = { online: "ok", blip: "warn", offline: "bad", pending: "off", updating: "busy" };
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
  const [dlg, setDlg] = useState<Dialog | null>(null);
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
    setDlg({ kind: "draw", entry: { id, name: entry.name, cc: entry.countryCode }, exitId: target.nodeId });
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
      width: 236,
      headless: true,
      anchors: true,
      draggable: true,
      rank: rankOf(n),
      ports: [
        { id: "in", side: "in", type: "udp" },
        { id: "out", side: "out", type: "udp" },
      ],
      body: (
        <div className="flex flex-col gap-1.5 p-3.5" onDragOver={overNode} onDrop={dropOn(n)}>
          <div className="flex items-center gap-2">
            <Flag code={n.countryCode} size={16} />
            <span className="min-w-0 flex-1 truncate text-[14px] font-semibold">{n.name}</span>
            <Badge kind={statusDot[n.status]}>{t(`fm.st.${n.status}`)}</Badge>
          </div>
          <p className="text-[12px] leading-snug text-muted">{sub}</p>
          <div className="flex flex-wrap items-center gap-1.5">
            {n.role === "entry" && <Badge kind="busy">{t("fm.entryOnly")}</Badge>}
            {n.role === "idle" && <span className="text-[11.5px] text-muted">{t("fm.idle")}</span>}
            {n.protocols.map((p) => (
              <span key={p} className="inline-flex h-[20px] items-center rounded-full bg-surface-2 px-2 text-[11px] font-medium text-fg">
                {protocolShort(p)}
              </span>
            ))}
            {owner && n.status === "online" && (
              <span
                data-nodrag
                draggable
                onDragStart={(e) => e.dataTransfer.setData(NODE, n.nodeId)}
                title={t("fm.relayHandleHint")}
                className="ms-auto inline-flex h-[20px] cursor-grab items-center gap-1 rounded-full border border-dashed border-line px-2 text-[10.5px] font-semibold text-muted transition-colors hover:border-accent hover:text-accent-text"
              >
                <Icon name="chevronRight" size={11} />
                {t("fm.relayHandle")}
              </span>
            )}
          </div>
        </div>
      ),
    };
  };
  const plain = (id: string, w: number, rank: number, body: ReactNode, label: string, ports: GraphNode["ports"]): GraphNode => ({ id, title: label, label, width: w, headless: true, anchors: true, draggable: true, selectable: false, rank, ports, body });
  const graphNodes: GraphNode[] = [
    plain(
      "people",
      190,
      0,
      <div className="p-3.5">
        <div className="flex items-center gap-2">
          <Icon name="people" size={15} />
          <span className="text-[14px] font-semibold">{t("fm.people", { country: fmt.country(country) })}</span>
        </div>
        <p className="mt-1 text-[12px] text-muted">{t("fm.people.sub")}</p>
      </div>,
      t("fm.people", { country: fmt.country(country) }),
      [{ id: "out", side: "out", type: "udp" }],
    ),
    ...model.nodes.map(nodeCard),
    ...model.exits.map((e) =>
      plain(
        `exit:${e}`,
        200,
        4,
        <div className="p-3.5">
          <div className="flex items-center gap-2">
            <Icon name={e === "warp" ? "cloud" : "globe"} size={15} />
            <span className="text-[14px] font-semibold">{t(e === "warp" ? "fm.warp" : "fm.internet")}</span>
          </div>
          <p className="mt-1 text-[12px] text-muted">{t(e === "warp" ? "fm.warp.sub" : "fm.internet.sub")}</p>
        </div>,
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
      height="fit"
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
            className="h-9 w-full rounded-ctl border border-line bg-surface ps-9 pe-3 text-[13px] outline-none focus:border-accent"
          />
        </div>
        {q.trim() && (
          <ul className="absolute z-20 mt-1 w-full rounded-card border border-line bg-surface p-1 shadow-(--shadow-toast)">
            {matches.length === 0 && <li className="px-2.5 py-2 text-[12.5px] text-muted">{t("fm.searchNone")}</li>}
            {matches.slice(0, 6).map((id) => (
              <li key={id}>
                <PopItem onClick={() => focusNode(id)}>{nodeById(id)?.name}</PopItem>
              </li>
            ))}
          </ul>
        )}
      </div>
      <div className="relative min-w-0">
        {graph}
        {(selected || wire) && (
          // over the canvas's right edge, under its toolbar: the map keeps its size and its fit
          <aside className="absolute inset-e-3 top-[62px] bottom-3 z-20 flex w-[340px] max-w-[calc(100%-24px)] flex-col overflow-y-auto rounded-card border border-line bg-surface shadow-(--shadow-toast)" aria-label={t("fm.d.actions")}>
            {wire && wire.kind === "relay" && wire.relays ? (
              <WireDrawer key={wire.id} wire={wire} nodes={nodes} owner={owner} onDialog={setDlg} onClose={() => setWireId(null)} />
            ) : selected ? (
              <NodeDrawer key={selected.nodeId} id={selected.nodeId} node={nodeById(selected.nodeId)!} fm={selected} country={country} owner={owner} relays={relaysQ.data ?? []} onDialog={setDlg} onClose={() => go({ view: "map" })} />
            ) : null}
          </aside>
        )}
      </div>
      {owner && dlg?.kind === "draw" && <AddRelayDialog key={`${dlg.entry.id}/${dlg.exitId ?? ""}`} open onOpenChange={() => setDlg(null)} entry={dlg.entry} exitId={dlg.exitId} />}
      {owner && dlg?.kind === "remove" && <RemoveRelay r={dlg.r} onClose={() => setDlg(null)} />}
      {owner && dlg?.kind === "port" && <ChangeRelayPort r={dlg.r} onClose={() => setDlg(null)} />}
    </div>
  );
}

function DrawerHead({ title, sub, onClose, children }: { title: ReactNode; sub?: ReactNode; onClose: () => void; children?: ReactNode }) {
  const t = useT();
  return (
    <div className="flex items-start gap-2 border-b border-line p-3.5">
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <div className="flex items-center gap-2 text-[15px] font-semibold">{title}</div>
        {sub && <p className="text-[12px] text-muted">{sub}</p>}
        {children}
      </div>
      <button type="button" aria-label={t("fm.d.close")} onClick={onClose} className="flex size-7 shrink-0 items-center justify-center rounded-md text-muted hover:bg-surface-2 hover:text-fg">
        <Icon name="x" size={14} />
      </button>
    </div>
  );
}

const section = "flex flex-col gap-1.5 border-b border-line p-3.5 last:border-b-0";
const h = "text-[11px] font-semibold tracking-[0.04em] text-faint uppercase";
const linkCls = "flex h-8 w-full items-center gap-2 rounded-lg px-2.5 text-[13px] font-medium hover:bg-surface-2";

function NodeDrawer({ id, node, fm, country, owner, relays, onDialog, onClose }: { id: string; node: Node; fm: FmNode; country: string; owner: boolean; relays: RelayMsg[]; onDialog: (d: Dialog) => void; onClose: () => void }) {
  const q = useQuery(nodeQuery(id));
  const t = useT();
  return (
    <div className="flex flex-col">
      <DrawerHead
        title={
          <>
            <Flag code={fm.countryCode} size={16} />
            {fm.name}
            <Badge kind={statusDot[fm.status]}>{t(`fm.st.${fm.status}`)}</Badge>
          </>
        }
        sub={[node.location, node.address].filter(Boolean).join(" · ")}
        onClose={onClose}
      >
        <p className="text-[12px] text-muted">
          {fm.load !== null ? `${t("fm.d.load")} ${fm.load}% · ` : ""}
          {t("fm.d.people", { n: fm.online })}
        </p>
      </DrawerHead>
      {q.data ? <NodeDrawerBody data={q.data} id={id} fm={fm} country={country} owner={owner} relays={relays} onDialog={onDialog} /> : <div className="p-3.5"><Pending /></div>}
    </div>
  );
}

function NodeDrawerBody({ data, id, fm, country, owner, relays, onDialog }: { data: Plain<import("@/gen/mistgate/admin/v1/node_pb").GetNodeResponse>; id: string; fm: FmNode; country: string; owner: boolean; relays: RelayMsg[]; onDialog: (d: Dialog) => void }) {
  const t = useT();
  const w = useWays(data);
  const subs = useInSubscriptions(id, fm.name);
  const ports = usePortCheck(id);
  const model = routeMap({ node: { id, name: fm.name, address: data.node!.address, countryCode: fm.countryCode }, inbounds: data.inbounds, ways: w, country, warp: false, ipv6: { fact: w.ipv6, v6: w.v6 }, relaysKnown: owner });
  const out = relays.filter((r) => r.viaNodeId === id);
  const inn = relays.filter((r) => r.exitNodeId === id);
  const dotOf = (s: InboundState): ChipKind => (s === InboundState.ACTIVE ? "ok" : s === InboundState.FAILED ? "bad" : s === InboundState.DISABLED ? "off" : "warn");
  return (
    <>
      <div className={section}>
        <p className={h}>{t("fm.d.servers")}</p>
        {data.inbounds.length === 0 && <p className="text-[12.5px] text-muted">{t("fm.idle")}</p>}
        {data.inbounds.map((i) => (
          <div key={i.id} className="flex items-center gap-2 text-[13px]">
            <Dot kind={dotOf(i.state)} />
            <span className="min-w-0 flex-1 truncate font-medium">{i.profileName}</span>
            <span className="font-mono text-[11px] text-muted">{i.port}</span>
            {owner && !i.cdn && i.enabled && (
              <Switch checked={i.directInSubscriptions} disabled={subs.busy} aria-label={t("ways.subs.switch", { name: i.profileName })} onCheckedChange={(on) => void subs.toggle(i, on, t(on ? "wg.subs.in" : "wg.subs.out", { name: i.profileName }))} />
            )}
          </div>
        ))}
        {owner && data.inbounds.some((i) => !i.cdn && i.enabled) && <p className="text-[11px] text-faint">{t("fm.d.subs")}</p>}
      </div>
      <div className={section}>
        <p className={h}>{t("fm.d.ways")}</p>
        {model.ways.length === 0 && <p className="text-[12.5px] text-muted">{t("fm.d.noWays")}</p>}
        {model.ways.map((x) => (
          <div key={x.id} className="flex flex-col gap-0.5">
            <div className="flex items-center gap-2 text-[13px] font-medium">
              <span className="min-w-0 flex-1 truncate">{x.kind === "via" ? t("rm.way.via", { node: x.peer?.name ?? "" }) : t(`rm.way.${x.kind}` as MessageKey)}</span>
              <Badge kind={badgeKind[x.state]}>{say(t, x.badge)}</Badge>
            </div>
            <p className="text-[11.5px] text-muted">{say(t, x.sub)}</p>
          </div>
        ))}
      </div>
      <div className={section}>
        <p className={h}>{t("fm.d.relaysOut")}</p>
        {out.length === 0 ? <p className="text-[12.5px] text-muted">{t("fm.d.none")}</p> : <RelayLines list={out} side="entry" />}
        <p className={cx(h, "mt-1")}>{t("fm.d.relaysIn")}</p>
        {inn.length === 0 ? <p className="text-[12.5px] text-muted">{t("fm.d.none")}</p> : <RelayLines list={inn} side="exit" />}
      </div>
      <div className="flex flex-col gap-0.5 p-2">
        <Link to="/nodes/$id" params={{ id }} search={{ from: "map" }} className={linkCls}>
          <Icon name="externalLink" size={14} />
          {t("fm.openNode")}
        </Link>
        <Link to="/nodes/$id" params={{ id }} search={{ tab: "ways", view: "graph", from: "map" }} className={linkCls}>
          <Icon name="network" size={14} />
          {t("fm.waysMap")}
        </Link>
        {owner && fm.status === "online" && (
          <PopItem icon={<Icon name="plus" size={14} />} onClick={() => onDialog({ kind: "draw", entry: { id, name: fm.name, cc: fm.countryCode } })}>
            {t("fm.d.addRelay")}
          </PopItem>
        )}
        <div className="px-2.5 pt-1.5">
          <CheckPortsButton data={data} run={ports} />
        </div>
      </div>
      {subs.dialog}
    </>
  );
}

function RelayLines({ list, side }: { list: RelayMsg[]; side: "entry" | "exit" }) {
  return (
    <ul className="flex flex-col gap-1">
      {list.map((r) => (
        <li key={r.id} className="flex items-center gap-2 text-[12.5px]">
          <Dot kind={r.enabled ? (r.state === InboundState.ACTIVE ? "ok" : r.state === InboundState.FAILED ? "bad" : "warn") : "off"} />
          <span className="min-w-0 flex-1 truncate">
            {r.profileName} · {side === "entry" ? r.exitNodeName : r.viaNodeName}
          </span>
          <span className="font-mono text-[11px] text-muted">
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
  const anyOff = list.some((r) => !r.enabled);
  return (
    <div className="flex flex-col">
      <DrawerHead title={`${first.viaNodeName} → ${first.exitNodeName}`} sub={t.n("fm.w.sub", list.length)} onClose={onClose} />
      <div className={section}>
        {list.map((r) => (
          <div key={r.id} className="flex flex-col gap-1 rounded-lg border border-line p-2.5">
            <div className="flex items-center gap-2 text-[13px]">
              <Dot kind={r.enabled ? (r.state === InboundState.ACTIVE ? "ok" : r.state === InboundState.FAILED ? "bad" : "warn") : "off"} />
              <span className="min-w-0 flex-1 truncate font-medium">{r.profileName}</span>
              <span className="font-mono text-[11px] text-muted">
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
      {owner && (
        <div className="flex flex-col gap-0.5 p-2">
          <PopItem icon={<Icon name="pulse" size={14} />} onClick={() => void runs.checkAll(list)}>
            {anyOff ? t("wg.menu.check") : t("wg.menu.check")}
          </PopItem>
          <PopItem icon={<Icon name="plus" size={14} />} onClick={() => onDialog({ kind: "draw", entry: { id: first.viaNodeId, name: first.viaNodeName, cc: entry?.countryCode ?? "" }, exitId: first.exitNodeId })}>
            {t("fm.w.add")}
          </PopItem>
        </div>
      )}
    </div>
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

