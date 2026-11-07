import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo, useState, type ReactNode } from "react";
import { Graph, GraphMenuItem, GraphPortRow, GraphValue, type GraphEdge, type GraphNode, type GraphPort, type PortRef } from "@/components/graph";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Select } from "@/components/ui/select";
import { NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import type { Plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import type { RelayMsg } from "@/lib/relay";
import { protocolShort } from "@/lib/series";
import type { WayId } from "@/lib/ways";
import { port, relayRefusal, waysGraph, waysLayout, type GCard, type GServer, type GWire, type Ipv6State, type ServerWay } from "@/lib/ways-graph";
import { profileListQuery } from "@/screens/users/rpc";
import { TwinPanel } from "@/screens/profiles/twin";
import { TurnOffDialog } from "./ipv6-entry";
import { AddRelayDialog, ChangeRelayPort, RemoveRelay, useCountryOf, useRelayRuns, type Entry } from "./relays";
import { useInSubscriptions } from "./subs-switches";
import type { WaysModel } from "./ways";

// The Ways in tab drawn as a graph (ADR 0016): the same lanes and relays the list shows, as cards and wires. The mapping is
// lib/ways-graph.ts's (pure, with tests); this file turns it into the graph component's nodes and edges, words every state
// (the chip on a row, the badge on a wire's end, the legend) and routes the edits to the dialogs and calls the list uses.
// Colour is spent sparingly: green dashes only on a way that carries traffic, amber for a way that works in part, rose only
// as a small pill or port ring; a way a country cuts is a thin dotted line ending in a badge, never a wide stroke.

type Data = Plain<GetNodeResponse>;

const typeColors = { udp: "var(--accent-sky)", tcp: "var(--accent-lavender)", people: "var(--muted)", egress: "var(--muted)", any: "var(--muted)" };

/** A small status word on a row. */
function Chip({ kind, children, onClick, title }: { kind: "ok" | "warn" | "bad" | "off" | "busy" | "cut"; children: ReactNode; onClick?: () => void; title?: string }) {
  // "cut": a way a country blocks: grey, with rose only in the word
  const cls = cx(kind === "cut" ? "mg-cut" : "pill tone-" + kind, "inline-flex h-[18px] shrink-0 items-center rounded-full px-2 text-[10.5px] leading-none font-bold whitespace-nowrap");
  return onClick ? (
    <button type="button" className={cx(cls, "cursor-pointer hover:brightness-110")} title={title} onClick={onClick}>
      {children}
    </button>
  ) : (
    <span className={cls} title={title}>
      {children}
    </span>
  );
}

const wayChip: Record<ServerWay, ["ok" | "warn" | "bad" | "off" | "cut", MessageKey] | null> = {
  works: ["ok", "wg.chip.works"],
  partial: ["warn", "wg.chip.partial"],
  blocked: ["cut", "wg.chip.blocked"],
  off: ["off", "wg.chip.off"],
  failed: ["bad", "wg.chip.failed"],
  hidden: ["off", "wg.chip.hidden"],
  unknown: null,
};
const v6Chip: Record<Ipv6State, ["ok" | "warn" | "bad" | "off" | "busy", MessageKey]> = {
  on: ["ok", "wg.v6.on"],
  on_down: ["bad", "wg.v6.on_down"],
  can: ["busy", "wg.v6.can"],
  none: ["off", "wg.v6.none"],
  broken: ["warn", "wg.v6.broken"],
  unavailable: ["off", "wg.v6.unavailable"],
};

const relayWord = (t: T, w: GWire["state"]) => (w === "pending" ? t("wg.wire.checking") : w === "bad" ? t("wg.wire.failing") : w === "off" ? t("wg.wire.off") : "");

/** The card a way belongs to (the open way is the selected card), and back. */
const cardOfWay = (id: WayId): string => (id.startsWith("via:") ? `entry:${id.slice(4)}` : id.startsWith("out:") ? `exit:${id.slice(4)}` : "node");
const wayOfCard = (id: string): WayId | undefined => (id.startsWith("entry:") ? (`via:${id.slice(6)}` as WayId) : id.startsWith("exit:") ? (`out:${id.slice(5)}` as WayId) : id === "node" ? "direct" : undefined);

type Dialog =
  | { kind: "draw"; entry: Entry; inboundId: string }
  | { kind: "remove"; r: RelayMsg }
  | { kind: "port"; r: RelayMsg }
  | { kind: "twin"; inboundId: string }
  | { kind: "v6off" };

export function WaysGraph({ data, w, open, onOpen, onIpv6 }: { data: Data; w: WaysModel; open: WayId; onOpen: (id: WayId) => void; onIpv6: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const node = data.node!;
  const owner = w.owner;
  const ccOf = useCountryOf();
  const runs = useRelayRuns();
  const subs = useInSubscriptions(node.id, node.name);
  const nodesList = useQuery({ ...nodesQuery, enabled: owner }).data?.nodes ?? [];
  const profiles = useQuery({ ...profileListQuery, enabled: owner }).data;
  const [extra, setExtra] = useState<{ id: string; name: string }[]>([]);
  const [dlg, setDlg] = useState<Dialog | null>(null);
  const cc = w.cc;

  const warp = !!node.warp && node.warp.state !== WarpState.NOT_CONFIGURED && node.warp.state !== WarpState.UNSPECIFIED;
  const model = waysGraph({ node: { id: node.id, name: node.name }, inbounds: data.inbounds, ways: w, country: cc, warp, extraEntries: extra, ipv6: { fact: w.ipv6, v6: w.v6 } });
  const wires = useMemo(() => new Map(model.wires.map((x) => [x.id, x])), [model]);
  const relays = useMemo(() => w.lanes.flatMap((l) => (l.kind === "via" ? l.relays : [])), [w.lanes]);
  const nodeCard = model.cards.find((c): c is Extract<GCard, { kind: "node" }> => c.kind === "node")!;
  const serverOf = (id: string) => nodeCard.servers.find((s) => s.inboundId === id);
  const inboundOf = (id: string) => data.inbounds.find((i) => i.id === id);

  // ---- the cards
  const graphNodes = ((): GraphNode[] => {
    const portsOf = (c: GCard): GraphPort[] =>
      c.ports
        .filter((p) => owner || p.id !== port.newRelay)
        .map((p) => ({ id: p.id, side: p.side, type: p.id === port.newRelay ? "any" : p.type, status: p.status, multi: p.side === "in" ? true : undefined, label: portLabel(c, p.id) }));
    function portLabel(c: GCard, id: string): string {
      if (id === port.people) return t("ways.peopleShort");
      if (id === port.reach) return t("wg.entry.reach");
      if (id === port.ipv6) return "IPv6";
      if (id === port.newRelay) return t("wg.relay.port");
      if (id === port.exit) return c.kind === "warp" ? t("wg.exit.warp") : t("wg.exit.internet");
      const inbound = id.startsWith("in:") || id.startsWith("out:") ? id.slice(id.indexOf(":") + 1) : "";
      if (inbound) return serverOf(inbound)?.name ?? inbound;
      const relay = id.startsWith("relay:") ? [...relays, ...w.out.flatMap((l) => l.relays)].find((r) => r.id === id.slice(6)) : undefined;
      return relay?.profileName ?? id;
    }
    const netBadge = (net: string | undefined, p: number) => (
      <GraphValue>
        {net ?? "?"}/{p}
      </GraphValue>
    );
    const out: GraphNode[] = [];
    for (const c of model.cards) {
      const common = { id: c.id, ports: portsOf(c), draggable: true };
      if (c.kind === "people") {
        out.push({
          ...common,
          title: t("ways.people", { country: fmt.country(cc) }),
          label: t("ways.people", { country: fmt.country(cc) }),
          icon: <Icon name="people" size={13} />,
          tone: "sand",
          width: 210,
          body: (
            <>
              <GraphPortRow port={port.people} align="end">
                <span className="font-medium">{t("wg.people.sub", { country: fmt.country(cc) })}</span>
              </GraphPortRow>
            </>
          ),
        });
      } else if (c.kind === "entry") {
        const mine = c.relays;
        out.push({
          ...common,
          title: (
            <span className="flex items-center gap-2">
              {c.name}
              <span className="ms-auto font-mono text-[10.5px] font-medium text-muted">{ccOf(c.nodeId)}</span>
            </span>
          ),
          label: c.name,
          icon: <Icon name="server" size={13} />,
          tone: "sky",
          width: 236,
          body: (
            <>
              <GraphPortRow port={port.reach}>
                <span className="text-muted">{t("wg.entry.reach")}</span>
              </GraphPortRow>
              {mine.map((r) => {
                const wire = wires.get(`relay:${r.id}`);
                const word = wire ? relayWord(t, wire.state) : "";
                return (
                  <GraphPortRow key={r.id} port={port.relay(r.id)}>
                    <span className="flex items-center justify-end gap-2">
                      <span className="min-w-0 truncate font-medium">{r.profileName}</span>
                      {netBadge(r.network, r.port)}
                      {word && <Chip kind={wire!.state === "bad" ? "bad" : wire!.state === "pending" ? "busy" : "off"}>{word}</Chip>}
                    </span>
                  </GraphPortRow>
                );
              })}
              {owner && (
                <GraphPortRow port={port.newRelay}>
                  <span className="flex items-center justify-end gap-1.5 text-muted">
                    <Icon name="plus" size={12} />
                    {t("wg.relay.port")}
                  </span>
                </GraphPortRow>
              )}
              {owner && mine.length === 0 && <p className="px-3.5 pb-1 text-[11px] leading-snug text-muted">{t("wg.entry.none")}</p>}
            </>
          ),
        });
      } else if (c.kind === "node") {
        const v6 = c.v6;
        out.push({
          ...common,
          title: c.name,
          label: c.name,
          icon: <Icon name="shield" size={13} />,
          tone: "mint",
          width: 318,
          body: (
            <>
              {v6 && (
                <GraphPortRow port={port.ipv6}>
                  <span className="flex items-center gap-2">
                    <span className="flex-1 font-medium">IPv6</span>
                    <V6Chip state={v6.state} blockedCc={v6.state === "on" && wires.get("ipv6")?.blocked ? cc : ""} onClick={owner && v6.state === "can" ? onIpv6 : undefined} />
                  </span>
                </GraphPortRow>
              )}
              {c.servers.map((s) => (
                <GraphPortRow key={s.inboundId} port={[port.serverIn(s.inboundId), port.serverOut(s.inboundId)]}>
                  <ServerRow s={s} cc={cc} onShow={owner && s.way === "hidden" ? () => void subs.toggle(inboundOf(s.inboundId)!, true) : undefined} />
                </GraphPortRow>
              ))}
              {c.ports
                .filter((p) => p.id.startsWith("relay:"))
                .map((p) => {
                  const r = w.out.flatMap((l) => l.relays).find((x) => `relay:${x.id}` === p.id);
                  if (!r) return null;
                  const wire = wires.get(`relay:${r.id}`);
                  const word = wire ? relayWord(t, wire.state) : "";
                  return (
                    <GraphPortRow key={p.id} port={p.id}>
                      <span className="flex items-center justify-end gap-2">
                        <span className="min-w-0 truncate font-medium">{r.profileName}</span>
                        {netBadge(r.network, r.port)}
                        {word && <Chip kind={wire!.state === "bad" ? "bad" : wire!.state === "pending" ? "busy" : "off"}>{word}</Chip>}
                      </span>
                    </GraphPortRow>
                  );
                })}
            </>
          ),
        });
      } else if (c.kind === "exit") {
        out.push({
          ...common,
          title: c.name,
          label: c.name,
          icon: <Icon name="server" size={13} />,
          tone: "lavender",
          width: 224,
          body: (
            <>
              {c.relays.map((r) => (
                <GraphPortRow key={r.id} port={port.serverIn(r.inboundId)}>
                  <span className="flex items-center gap-2">
                    <span className="min-w-0 flex-1 truncate font-medium">{r.profileName}</span>
                    {netBadge(r.network, r.exitPort)}
                  </span>
                </GraphPortRow>
              ))}
            </>
          ),
        });
      } else {
        const warpCard = c.kind === "warp";
        out.push({
          ...common,
          title: warpCard ? t("wg.exit.warp") : t("wg.exit.internet"),
          label: warpCard ? t("wg.exit.warp") : t("wg.exit.internet"),
          icon: <Icon name={warpCard ? "cloud" : "globe"} size={13} />,
          tone: warpCard ? "sage" : "lavender",
          width: 206,
          body: (
            <GraphPortRow port={port.exit}>
              <span className="text-muted">{warpCard ? t("wg.exit.warpSub") : t("wg.exit.internetSub")}</span>
            </GraphPortRow>
          ),
        });
      }
    }
    return out;
  })();

  // ---- the wires
  const edges: GraphEdge[] = model.wires.map((x) => {
        // a server leaving by its exit is plain plumbing: drawn in the quiet type colour unless it fails
    const state: GraphEdge["state"] = x.blocked ? "blocked" : x.kind === "egress" && x.state === "ok" ? undefined : x.state === "unknown" ? "off" : x.state;
        let label: ReactNode;
        let labelAt: GraphEdge["labelAt"];
        if (x.blocked) {
          label = t(x.kind === "reach" ? "wg.wire.dark" : "wg.wire.blocked", { cc });
          labelAt = "end";
        } else if (x.kind === "reach" && x.state === "warn") {
          label = t("wg.wire.partial");
          labelAt = "end";
        } else if (x.kind === "ipv6" && x.state === "bad") {
          label = t("wg.wire.failing");
          labelAt = "end";
        } else if (x.kind === "relay") label = relayWord(t, x.state) || undefined;
        return { id: x.id, from: { node: x.from.card, port: x.from.port }, to: { node: x.to.card, port: x.to.port }, state, animated: x.flowing, label, labelAt };
  });

  // ---- editing (the owner's)
  const canConnect = (from: PortRef, to: PortRef) => {
    if (!from.node.startsWith("entry:")) return t("wg.refuse.source");
    const s = to.node === "node" && to.port.startsWith("in:") ? serverOf(to.port.slice(3)) : undefined;
    if (!s) return t("wg.refuse.target");
    const why = relayRefusal(from.node.slice(6), node.id, s, relays);
    return why ? t(`wg.refuse.${why}` as MessageKey) : true;
  };
  const onConnect = (from: PortRef, to: PortRef) => {
    const id = from.node.slice(6);
    const entry = model.cards.find((c) => c.id === from.node);
    setDlg({ kind: "draw", entry: { id, name: entry && "name" in entry ? entry.name : id, cc: ccOf(id) }, inboundId: to.port.slice(3) });
  };
  const onEdgeDelete = (e: GraphEdge) => {
    const r = wires.get(e.id)?.relay;
    if (r) setDlg({ kind: "remove", r });
  };

  const renderMenu = (e: GraphEdge, close: () => void) => {
    const x = wires.get(e.id);
    if (!x) return null;
    const pick = (fn: () => void) => () => {
      close();
      fn();
    };
    if (x.kind === "relay" && x.relay) {
      const r = x.relay;
      const busy = !!runs.busy[r.id];
      return (
        <>
          <GraphMenuItem icon={<Icon name="pulse" size={14} />} onClick={pick(() => runs.check(r))}>
            {t("wg.menu.check")}
          </GraphMenuItem>
          <GraphMenuItem icon={<Icon name={r.enabled ? "eyeOff" : "eye"} size={14} />} onClick={pick(() => (r.enabled ? runs.off(r) : runs.on(r)))}>
            {r.enabled ? t("wg.menu.off") : t("wg.menu.on")}
          </GraphMenuItem>
          <GraphMenuItem icon={<Icon name="sliders" size={14} />} onClick={pick(() => setDlg({ kind: "port", r }))}>
            {t("wg.menu.port")}
          </GraphMenuItem>
          <div className="my-1 h-px bg-line" />
          <GraphMenuItem danger icon={<Icon name="trash" size={14} />} onClick={pick(() => setDlg({ kind: "remove", r }))}>
            {t("wg.menu.remove")}
          </GraphMenuItem>
          {busy && <span className="sr-only">{t("wg.wire.checking")}</span>}
        </>
      );
    }
    if ((x.kind === "direct" || x.kind === "cdn") && x.inboundId) {
      const i = inboundOf(x.inboundId);
      return (
        i && (
          <GraphMenuItem icon={<Icon name="eyeOff" size={14} />} onClick={pick(() => void subs.toggle(i, false))}>
            {t("wg.menu.subsOff")}
          </GraphMenuItem>
        )
      );
    }
    if (x.kind === "ipv6") {
      return (
        <GraphMenuItem icon={<Icon name="eyeOff" size={14} />} onClick={pick(() => setDlg({ kind: "v6off" }))}>
          {t("wg.menu.v6off")}
        </GraphMenuItem>
      );
    }
    if (x.kind === "egress" && x.inboundId) {
      const s = serverOf(x.inboundId);
      return (
        <GraphMenuItem icon={<Icon name="cloud" size={14} />} onClick={pick(() => setDlg({ kind: "twin", inboundId: x.inboundId! }))}>
          {s?.egress === "warp" ? t("wg.menu.twinDirect") : t("wg.menu.twinWarp")}
        </GraphMenuItem>
      );
    }
    if (x.kind === "reach") {
      const id = x.to.card.slice(6);
      const entry = model.cards.find((c) => c.id === x.to.card);
      return (
        <Link to="/nodes/$id" params={{ id }} search={{ tab: "ways", way: `out:${node.id}` as WayId }} className="flex h-8 w-full items-center gap-2 rounded-lg px-2.5 text-[13px] font-medium hover:bg-surface-2">
          <Icon name="externalLink" size={14} />
          {t("wg.menu.open", { node: entry && entry.kind === "entry" ? entry.name : id })}
        </Link>
      );
    }
    return null;
  };

  const entryIds = new Set(model.cards.filter((c) => c.kind === "entry").map((c) => c.id.slice(6)));
  const addable = nodesList.filter((n) => n.id !== node.id && n.status !== NodeStatus.RETIRED && !entryIds.has(n.id));
  const twinInbound = dlg?.kind === "twin" ? inboundOf(dlg.inboundId) : undefined;
  const twinProfile = twinInbound ? profiles?.find((p) => p.id === twinInbound.profileId) : undefined;

  return (
    <div className="flex min-w-0 flex-col gap-2.5">
      {owner && (
        <div className="w-[220px]">
          <Select
            compact
            aria-label={t("wg.addEntry")}
            value=""
            onValueChange={(id) => {
              const n = nodesList.find((x) => x.id === id);
              if (n) setExtra((e) => [...e, { id: n.id, name: n.name }]);
            }}
            options={[{ value: "", label: addable.length ? `+ ${t("wg.addEntry")}` : t("wg.addEntry.none"), disabled: true }, ...addable.map((n) => ({ value: n.id, label: n.name, hint: ccOf(n.id) }))]}
          />
        </div>
      )}
      <Graph
        nodes={graphNodes}
        edges={edges}
        typeColors={typeColors}
        ariaLabel={t("wg.graph", { node: node.name })}
        height={560}
        layoutKey={`ways:${node.id}`}
        arrange={(sizes) => waysLayout(sizes)}
        readOnly={!owner}
        canConnect={canConnect}
        onConnect={owner ? onConnect : undefined}
        onEdgeDelete={owner ? onEdgeDelete : undefined}
        renderEdgeMenu={owner ? renderMenu : undefined}
        selected={[cardOfWay(open)]}
        onSelectionChange={(ids) => {
          const way = ids[0] ? wayOfCard(ids[0]) : undefined;
          if (way && way !== open && (w.lanes.some((l) => l.id === way) || w.out.some((l) => l.id === way))) onOpen(way);
        }}
        legend={<Legend cc={cc} />}
      />
      {owner && dlg?.kind === "draw" && (
        <AddRelayDialog key={`${dlg.entry.id}/${dlg.inboundId}`} open onOpenChange={() => setDlg(null)} entry={dlg.entry} exitId={node.id} preselect={dlg.inboundId} />
      )}
      {owner && dlg?.kind === "remove" && <RemoveRelay r={dlg.r} onClose={() => setDlg(null)} />}
      {owner && dlg?.kind === "port" && <ChangeRelayPort r={dlg.r} onClose={() => setDlg(null)} />}
      {owner && <TurnOffDialog open={dlg?.kind === "v6off"} onOpenChange={(o) => !o && setDlg(null)} node={node} />}
      {owner && (
        <Modal open={dlg?.kind === "twin"} onOpenChange={(o) => !o && setDlg(null)} title={t("wg.twin.title")} description={t("wg.twin.body")} closeLabel={t("common.close")}>
          {twinProfile && twinInbound && <TwinPanel profile={twinProfile} egress={twinInbound.egress === "warp" ? "warp" : "direct"} dirty={false} />}
        </Modal>
      )}
      {subs.dialog}
    </div>
  );
}

function V6Chip({ state, blockedCc, onClick }: { state: Ipv6State; blockedCc: string; onClick?: () => void }) {
  const t = useT();
  if (blockedCc) return <Chip kind="cut">{t("wg.v6.blocked", { cc: blockedCc })}</Chip>;
  const [kind, word] = v6Chip[state];
  return (
    <Chip kind={kind} onClick={onClick}>
      {t(word)}
    </Chip>
  );
}

function ServerRow({ s, cc, onShow }: { s: GServer; cc: string; onShow?: () => void }) {
  const t = useT();
  const chip = wayChip[s.way];
  const noRelay = s.noRelay === "cdn" || s.noRelay === "awg" ? s.noRelay : undefined;
  return (
    <span className="flex min-w-0 flex-col gap-0.5">
      <span className="flex items-center gap-2">
        <span className={cx("min-w-0 flex-1 truncate font-medium", s.way === "off" && "text-muted")}>{s.name}</span>
        <GraphValue>
          {s.net ?? protocolShort(s.protocol)}/{s.port}
        </GraphValue>
        {chip && (
          <Chip kind={chip[0]} onClick={onShow} title={onShow ? t("wg.chip.hiddenOn") : undefined}>
            {t(chip[1], { cc })}
          </Chip>
        )}
      </span>
      {noRelay && <span className="text-[10.5px] leading-tight font-medium text-muted">{t(noRelay === "cdn" ? "wg.noRelay.cdn" : "wg.noRelay.awg")}</span>}
    </span>
  );
}

/** The legend: line styles and port marks, drawn the way the graph draws them. */
function Legend({ cc }: { cc: string }) {
  const t = useT();
  const line = (cls: string, color: string, dash?: string) => (
    <svg width="38" height="10" aria-hidden className="shrink-0">
      <line x1="2" y1="5" x2="36" y2="5" className={cls} style={{ stroke: `color-mix(in oklch, ${color} var(--wp, 100%), #000)`, strokeWidth: 2, strokeLinecap: "round", strokeDasharray: dash }} />
    </svg>
  );
  const dot = (status: string) => (
    <span className="relative inline-block h-3 w-3 shrink-0">
      <button type="button" tabIndex={-1} aria-hidden className="mg-gport" data-status={status} style={{ position: "absolute", left: 6, top: 6, pointerEvents: "none", ["--pc" as never]: "var(--accent-sky)" }} />
    </span>
  );
  const row = (icon: ReactNode, text: string) => (
    <li className="flex items-center gap-2.5">
      <span className="flex w-[38px] shrink-0 justify-center">{icon}</span>
      <span className="text-muted">{text}</span>
    </li>
  );
  return (
    <div className="flex flex-col gap-2">
      <p className="text-[11px] font-bold tracking-[0.08em] text-muted uppercase">{t("wg.legend.title")}</p>
      <ul className="flex flex-col gap-1.5">
        {row(line("mg-gwire mg-gwire-flow", "var(--accent-mint)"), t("wg.legend.flow"))}
        {row(line("mg-gwire", "var(--accent-sand)"), t("wg.legend.warn"))}
        {row(<span className="mg-cut inline-flex h-[17px] items-center rounded-full px-1.5 text-[10px] font-bold">⊘ {cc}</span>, t("wg.legend.blocked"))}
        {row(line("mg-gwire", "var(--muted)", "5 6"), t("wg.legend.off"))}
        {row(line("mg-gwire mg-gwire-pending", "var(--accent)"), t("wg.legend.pending"))}
        {row(dot("ok"), t("wg.legend.portOk"))}
        {row(dot("none"), t("wg.legend.portNone"))}
        {row(dot("blocked"), t("wg.legend.portBlocked"))}
      </ul>
      <p className="text-muted">{t("wg.legend.chips")}</p>
      <p className="text-muted">{t("wg.legend.use")}</p>
    </div>
  );
}
