import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useState, type DragEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Graph, HelpGroup, Kbd, KeyRow, type GraphEdge, type GraphNode, type HelpTab } from "@/components/graph";
import { Icon, type IconName } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { relays as relaysApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import type { Plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import type { RelayMsg } from "@/lib/relay";
import { routeLayout, routeMap, type AltRef, type RmChip, type RmState, type RmWay, type Say } from "@/lib/route-map";
import { protocolShort } from "@/lib/series";
import { readPref, writePref } from "@/lib/storage";
import type { WayId } from "@/lib/ways";
import { relayRefusal } from "@/lib/ways-graph";
import { TurnOffDialog } from "./ipv6-entry";
import { AddRelayDialog, ChangeRelayPort, RemoveRelay, useCountryOf, useRelayRuns, type Entry } from "./relays";
import { useInSubscriptions } from "./subs-switches";
import type { WaysModel } from "./ways";
import { Badge, Dot, MoreButton, Pop, PopItem, ServerChip, type ChipKind } from "./ways-map-parts";

// The Ways in tab as a route map (ADR 0016, revised): four labelled columns, Who -> How they get in -> the Node -> Where they
// exit (and, on an entry node, Forwards to), one card per way and one wire per way. The mapping is lib/route-map.ts's (pure,
// with tests); this file draws it and routes the owner's edits to the dialogs and calls the list uses. Menus are primary;
// dragging a server onto a way (a relay) and a relay out of one (remove, confirmed) are a bonus.

type Data = Plain<GetNodeResponse>;

const colors = { udp: "var(--muted)" };
const SERVER = "application/x-mg-server";
const RELAY = "application/x-mg-relay";

const badgeKind: Record<RmState, ChipKind> = { ok: "ok", partial: "warn", blocked: "cut", off: "off", bad: "bad", checking: "busy" };

/** The card a way is (the selected card is the open way), and back. */
const cardOfWay = (id: WayId): string => (id.startsWith("out:") ? `fwd:${id.slice(4)}` : `way:${id}`);
const wayOfCard = (id: string): WayId | undefined => (id.startsWith("fwd:") ? (`out:${id.slice(4)}` as WayId) : id.startsWith("way:") ? (id.slice(4) as WayId) : undefined);

const say = (t: T, s: Say) => (s.n !== undefined ? t.n(s.key as MessageKey, s.n, s.params) : t(s.key as MessageKey, s.params));

type Dialog =
  | { kind: "draw"; entry: Entry; inboundId?: string }
  | { kind: "remove"; r: RelayMsg }
  | { kind: "port"; r: RelayMsg }
  | { kind: "removeWay"; entry: { id: string; name: string }; relays: RelayMsg[] }
  | { kind: "v6off" };

export function WaysGraph({
  data,
  w,
  open,
  onOpen,
  onIpv6,
  detail,
  full = false,
  onFull,
  initialExtra,
}: {
  initialExtra?: { id: string; name: string }[];
  data: Data;
  w: WaysModel;
  open: WayId;
  onOpen: (id: WayId) => void;
  onIpv6: () => void;
  detail?: ReactNode;
  full?: boolean;
  onFull?: (full: boolean) => void;
}) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const node = data.node!;
  const owner = w.owner;
  const ccOf = useCountryOf();
  const runs = useRelayRuns();
  const subs = useInSubscriptions(node.id, node.name);
  const nodesList = useQuery({ ...nodesQuery, enabled: owner }).data?.nodes ?? [];
  const [extra, setExtra] = useState<{ id: string; name: string }[]>(initialExtra ?? []);
  const [dlg, setDlg] = useState<Dialog | null>(null);
  const [help, setHelp] = useState<HelpTab | "">(() => (readPref("mg.graph.help") === "legend" ? "legend" : readPref("mg.graph.help") === "shortcuts" ? "shortcuts" : ""));
  const cc = w.cc;

  const model = routeMap({
    node: { id: node.id, name: node.name, address: node.address, countryCode: node.countryCode },
    inbounds: data.inbounds,
    ways: w,
    country: cc,
    warp: false,
    extraEntries: extra,
    ipv6: { fact: w.ipv6, v6: w.v6 },
    relaysKnown: owner,
  });
  const allRelays = [...model.ways.flatMap((x) => x.relays), ...model.forwards.flatMap((f) => f.relays)];
  const inboundOf = (id: string) => data.inbounds.find((i) => i.id === id);
  const serverOf = (id: string) => model.node.chips.find((c) => c.inboundId === id)?.server;

  // full screen: the page behind does not scroll
  useEffect(() => {
    if (!full) return;
    const before = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.body.style.overflow = before;
    };
  }, [full]);
  const closeFull = () => {
    onFull?.(false);
    setTimeout(() => document.querySelector<HTMLElement>("[data-graph-expand]")?.focus(), 0);
  };

  // ---- words
  const altText = (alt: AltRef[]) => alt.map((a) => (a.kind === "via" ? t("rm.alt.via", { name: a.name ?? "" }) : t(`rm.alt.${a.kind}` as MessageKey))).join(", ");
  const wayTitle = (x: RmWay) => (x.kind === "via" ? t("rm.way.via", { node: x.peer?.name ?? "" }) : t(`rm.way.${x.kind}` as MessageKey));
  const dot = (c: RmChip): ChipKind => c.state;

  // ---- chips with their popovers
  const serverState = (c: RmChip) => (c.server?.way === "hidden" ? t("rm.st.hidden") : t(`rm.st.${c.state === "warn" ? "ok" : c.state}` as MessageKey));
  const serverChip = (c: RmChip): ReactNode => {
    const s = c.server!;
    const i = inboundOf(c.inboundId);
    return (
      <Pop
        key={c.id}
        trigger={<ServerChip kind={dot(c)} label={c.label} port={c.port} net={c.net} tabIndex={-1} />}
        label={c.label}
        triggerProps={{
          draggable: owner && !s.noRelay,
          onDragStart: (e: DragEvent) => e.dataTransfer.setData(SERVER, c.inboundId),
          className: "max-w-full",
        }}
      >
        {() => (
          <div className="flex flex-col gap-2.5 p-2">
            <p className="text-[14px] font-semibold">{c.label}</p>
            <dl className="grid grid-cols-[88px_1fr] gap-x-3 gap-y-1 text-[12px]">
              <dt className="text-muted">{t("rm.chip.protocol")}</dt>
              <dd>{protocolShort(s.protocol)}</dd>
              <dt className="text-muted">{t("rm.chip.port")}</dt>
              <dd className="font-mono">
                {s.net}/{s.port}
              </dd>
              <dt className="text-muted">{t("rm.chip.state")}</dt>
              <dd className="flex items-center gap-1.5">
                <Dot kind={dot(c)} />
                {serverState(c)}
              </dd>
              <dt className="text-muted">{t("rm.chip.exit")}</dt>
              <dd>{s.egress === "warp" ? t("rm.chip.warp") : t("rm.chip.internet")}</dd>
            </dl>
            {owner && !s.cdn && i && s.on && (
              <label className="flex items-center justify-between gap-3 text-[12.5px] font-medium">
                {t("rm.chip.subs")}
                <Switch
                  checked={i.directInSubscriptions}
                  disabled={subs.busy}
                  aria-label={t("ways.subs.switch", { name: s.name })}
                  onCheckedChange={(on) => void subs.toggle(i, on, t(on ? "wg.subs.in" : "wg.subs.out", { name: s.name }))}
                />
              </label>
            )}
            {s.noRelay === "cdn" || s.noRelay === "awg" ? <p className="text-[11.5px] leading-snug text-muted">{t(s.noRelay === "cdn" ? "wg.noRelay.cdn" : "wg.noRelay.awg")}</p> : null}
          </div>
        )}
      </Pop>
    );
  };
  const relayChip = (c: RmChip): ReactNode => {
    const r = c.relay!;
    return (
      <Pop
        key={c.id}
        trigger={<ServerChip kind={dot(c)} label={c.label} port={c.port} net={c.net} tabIndex={-1} />}
        label={c.label}
        triggerProps={{ draggable: owner, onDragStart: (e: DragEvent) => e.dataTransfer.setData(RELAY, r.id), className: "max-w-full" }}
      >
        {(close) => (
          <div className="flex flex-col gap-1.5 p-1">
            <div className="px-2 pt-1.5">
              <p className="text-[14px] font-semibold">{c.label}</p>
              <p className="mt-0.5 flex items-center gap-1.5 text-[12px] text-muted">
                <Dot kind={dot(c)} />
                {t(`rm.st.${c.state === "warn" ? "ok" : c.state}` as MessageKey)} · {r.viaNodeName} → {r.exitNodeName} · <span className="font-mono">{r.network}/{r.port}</span>
              </p>
            </div>
            {owner && (
              <div className="flex flex-col">
                <PopItem
                  onClick={() => {
                    close();
                    runs.check(r);
                  }}
                >
                  {t("wg.menu.check")}
                </PopItem>
                <PopItem
                  onClick={() => {
                    close();
                    if (r.enabled) runs.off(r);
                    else runs.on(r);
                  }}
                >
                  {r.enabled ? t("wg.menu.off") : t("wg.menu.on")}
                </PopItem>
                <PopItem
                  onClick={() => {
                    close();
                    setDlg({ kind: "port", r });
                  }}
                >
                  {t("wg.menu.port")}
                </PopItem>
                <PopItem
                  danger
                  onClick={() => {
                    close();
                    setDlg({ kind: "remove", r });
                  }}
                >
                  {t("wg.menu.remove")}
                </PopItem>
              </div>
            )}
          </div>
        )}
      </Pop>
    );
  };
  const chipsOf = (chips: RmChip[]) => chips.length > 0 && <div className="flex flex-wrap gap-1.5 pt-1">{chips.map((c) => (c.relay ? relayChip(c) : serverChip(c)))}</div>;

  // ---- the ⋯ menu of a way
  const wayMenu = (x: RmWay): ReactNode => {
    if (!owner) return null;
    return (
      <MoreButton label={wayTitle(x)}>
        {(close) => {
          const pick = (fn: () => void) => () => {
            close();
            fn();
          };
          if (x.kind === "via" && x.peer) {
            const peer = x.peer;
            const entry: Entry = { id: peer.id, name: peer.name, cc: ccOf(peer.id) };
            const anyOff = x.relays.some((r) => !r.enabled);
            return (
              <>
                {x.relays.length > 0 && <PopItem onClick={pick(() => void runs.checkAll(x.relays))}>{t("rm.menu.checkAll")}</PopItem>}
                {x.relays.length > 0 && (
                  <PopItem onClick={pick(() => x.relays.filter((r) => r.enabled === !anyOff).forEach((r) => (anyOff ? runs.on(r) : runs.off(r))))}>{anyOff ? t("rm.menu.onAll") : t("rm.menu.offAll")}</PopItem>
                )}
                <PopItem onClick={pick(() => setDlg({ kind: "draw", entry }))}>{t("rm.menu.addRelay")}</PopItem>
                {x.relays.length > 0 ? (
                  <PopItem danger onClick={pick(() => setDlg({ kind: "removeWay", entry: peer, relays: x.relays }))}>
                    {t("rm.menu.removeWay")}
                  </PopItem>
                ) : (
                  <PopItem onClick={pick(() => setExtra((e) => e.filter((n) => n.id !== peer.id)))}>{t("rm.menu.removeEntry")}</PopItem>
                )}
              </>
            );
          }
          if (x.kind === "direct") {
            const list = model.node.chips.filter((c) => c.server && !c.server.cdn && c.server.on);
            return (
              <div className="flex min-w-[230px] flex-col gap-1 p-1.5">
                <p className="px-1 pb-1 text-[11px] font-semibold text-faint">{t("rm.menu.subs")}</p>
                {list.map((c) => {
                  const i = inboundOf(c.inboundId);
                  return (
                    i && (
                      <label key={c.id} className="flex items-center justify-between gap-3 rounded-lg px-1 py-1 text-[13px] font-medium">
                        <span className="flex min-w-0 items-center gap-2">
                          <span className="truncate">{c.label}</span>
                          <span className="font-mono text-[10.5px] font-normal text-muted">{c.port}</span>
                        </span>
                        <Switch checked={i.directInSubscriptions} disabled={subs.busy} aria-label={t("ways.subs.switch", { name: c.label })} onCheckedChange={(on) => void subs.toggle(i, on, t(on ? "wg.subs.in" : "wg.subs.out", { name: c.label }))} />
                      </label>
                    )
                  );
                })}
              </div>
            );
          }
          if (x.kind === "ipv6")
            return x.state === "ok" || x.state === "partial" || x.state === "blocked" || (x.state === "bad" && w.v6.entry) ? (
              <PopItem onClick={pick(() => setDlg({ kind: "v6off" }))}>{t("wg.menu.v6off")}</PopItem>
            ) : (
              <PopItem onClick={pick(onIpv6)}>{t("rm.add.ipv6")}</PopItem>
            );
          // cdn: the profile of its first server
          const first = x.chips[0] && inboundOf(x.chips[0].inboundId);
          return first ? (
            <Link to="/profiles/$id" params={{ id: first.profileId }} className="flex h-8 w-full items-center gap-2 rounded-lg px-2.5 text-[13px] font-medium hover:bg-surface-2">
              <Icon name="externalLink" size={14} />
              {t("rm.menu.profile")}
            </Link>
          ) : null;
        }}
      </MoreButton>
    );
  };

  // ---- dragging: a server onto a way relays it from that entry, a relay out onto the canvas removes it
  const dropOnWay = (x: RmWay) => (e: DragEvent) => {
    if (!owner || x.kind !== "via" || !x.peer) return;
    if (e.dataTransfer.types.includes(RELAY)) return e.stopPropagation();
    const id = e.dataTransfer.getData(SERVER);
    if (!id) return;
    e.preventDefault();
    e.stopPropagation();
    const s = serverOf(id);
    const why = s ? relayRefusal(x.peer.id, node.id, s, w.lanes.flatMap((l) => (l.kind === "via" ? l.relays : []))) : "net";
    if (why) return toast.error(`${t("rm.drag.refused")}: ${t(`wg.refuse.${why}` as MessageKey)}`);
    setDlg({ kind: "draw", entry: { id: x.peer.id, name: x.peer.name, cc: ccOf(x.peer.id) }, inboundId: id });
  };
  const wayOver = (x: RmWay) => (e: DragEvent) => {
    if (owner && x.kind === "via" && e.dataTransfer.types.includes(SERVER)) e.preventDefault();
  };

  // ---- the cards
  const card = (id: string, width: number, body: ReactNode, ports: { in?: boolean; out?: boolean }, label: string, extraProps: Partial<GraphNode> = {}): GraphNode => ({
    id,
    title: label,
    label,
    width,
    headless: true,
    anchors: true,
    draggable: false,
    ports: [...(ports.in ? [{ id: "in", side: "in" as const, type: "udp" }] : []), ...(ports.out ? [{ id: "out", side: "out" as const, type: "udp" }] : [])],
    body,
    ...extraProps,
  });
  const head = (icon: IconName | undefined, title: ReactNode, right?: ReactNode) => (
    <div className="flex items-center gap-2">
      {icon && <Icon name={icon} size={15} />}
      <span className="min-w-0 flex-1 truncate text-[14px] font-semibold">{title}</span>
      {right}
    </div>
  );
  const sub = (s: ReactNode) => <p className="mt-1 text-[12px] leading-snug text-muted">{s}</p>;
  const nodes: GraphNode[] = [];
  const colLabels = [t("rm.col.who"), t("rm.col.how"), t("rm.col.node"), t("rm.col.exit"), ...(model.hasForwards ? [t("rm.col.fwd")] : [])];
  colLabels.forEach((txt, k) =>
    nodes.push({ id: `col:${k}`, title: txt, label: txt, ports: [], width: 170, headless: true, variant: "label", draggable: false, selectable: false, body: <p className="px-0.5 text-[11px] font-bold tracking-[0.09em] text-muted uppercase">{txt}</p> }),
  );
  const peopleTitle = t("ways.people", { country: fmt.country(cc) });
  nodes.push(
    card(
      "people",
      178,
      <div className="p-3.5">
        {head("people", peopleTitle)}
        {sub(
          <>
            {t("rm.people.sub")}
            {owner && (
              <>
                {" · "}
                <Link to="/reachability" hash="sources" className="underline hover:text-fg">
                  {t("rm.people.change")}
                </Link>
              </>
            )}
          </>,
        )}
      </div>,
      { out: true },
      peopleTitle,
      { selectable: false },
    ),
  );
  for (const x of model.ways) {
    const title = wayTitle(x);
    const cons = x.consequence ? (x.consequence.alt.length > 0 ? t(x.consequence.kind === "blocked" ? "rm.cons.blocked" : "rm.cons.bad", { cc, via: altText(x.consequence.alt) }) : t("rm.cons.none")) : null;
    nodes.push(
      card(
        x.id,
        258,
        <div className="p-3.5" onDragOver={wayOver(x)} onDrop={dropOnWay(x)}>
          {head(undefined, title, <><Badge kind={badgeKind[x.state]}>{say(t, x.badge)}</Badge>{wayMenu(x)}</>)}
          {sub(say(t, x.sub))}
          {cons && sub(cons)}
          {chipsOf(x.chips)}
        </div>,
        { in: true, out: true },
        title,
      ),
    );
  }
  if (owner) {
    const entryIds = new Set(model.ways.filter((x) => x.peer).map((x) => x.peer!.id));
    const addable = nodesList.filter((n) => n.id !== node.id && n.status !== NodeStatus.RETIRED && !entryIds.has(n.id));
    nodes.push(
      card(
        "ghost:way",
        246,
        <Pop
          label={t("rm.ghost.way")}
          trigger={
            <span className="flex w-full items-start gap-2 px-3.5 py-3 text-start text-[13px] leading-snug">
              <Icon name="plus" size={14} />
              {t("rm.ghost.way")}
            </span>
          }
          triggerProps={{ className: "block w-full rounded-[10px] text-muted hover:text-fg" }}
        >
          {(close) => (
            <>
              {addable.length === 0 && <PopItem disabled>{t("rm.add.nodeNone")}</PopItem>}
              {addable.map((n) => (
                <PopItem
                  key={n.id}
                  onClick={() => {
                    close();
                    setExtra((e) => [...e, { id: n.id, name: n.name }]);
                  }}
                >
                  {t("rm.add.node", { node: n.name })}
                </PopItem>
              ))}
              <Link to="/profiles" className="flex h-8 w-full items-center gap-2 rounded-lg px-2.5 text-[13px] font-medium hover:bg-surface-2">
                {t("rm.add.cdn")}
              </Link>
              {model.canAddIpv6 && (
                <PopItem
                  onClick={() => {
                    close();
                    onIpv6();
                  }}
                >
                  {t("rm.add.ipv6")}
                </PopItem>
              )}
            </>
          )}
        </Pop>,
        {},
        t("rm.ghost.way"),
        { variant: "ghost", selectable: false },
      ),
    );
  }
  const nodeTitle = node.name;
  nodes.push(
    card(
      "node",
      258,
      <div className="p-3.5">
        {head("shield", nodeTitle, <Badge kind={model.node.online ? "ok" : "off"}>{t(model.node.online ? "rm.node.online" : "rm.node.offline")}</Badge>)}
        {sub(t("rm.node.sub", { country: model.node.countryCode ? fmt.country(model.node.countryCode) : "", address: model.node.address }).replace(/^ · | · $/g, ""))}
        {chipsOf(model.node.chips)}
      </div>,
      { in: true, out: true },
      nodeTitle,
      { selectable: false },
    ),
  );
  for (const e of model.exits) {
    const title = t(e.id === "warp" ? "rm.exit.warpTitle" : "rm.exit.internetTitle");
    nodes.push(
      card(
        `exit:${e.id}`,
        204,
        <div className="p-3.5">
          {head(e.id === "warp" ? "cloud" : "globe", title)}
          {sub(say(t, e.sub))}
        </div>,
        { in: true },
        title,
        { selectable: false },
      ),
    );
  }
  nodes.push(
    card(
      "ghost:exit",
      204,
      <Pop
        label={t("rm.ghost.exit")}
        trigger={
          <span className="flex w-full items-start gap-2 px-3.5 py-3 text-start text-[13px] leading-snug">
            <Icon name="plus" size={14} />
            {t("rm.ghost.exit")}
          </span>
        }
        triggerProps={{ className: "block w-full rounded-[10px] text-muted hover:text-fg" }}
      >
        {() => (
          <>
            <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "exit" }} className="flex h-8 w-full items-center gap-2 rounded-lg px-2.5 text-[13px] font-medium hover:bg-surface-2">
              <Icon name="cloud" size={14} />
              {t("rm.exit.warpOpen")}
            </Link>
            <PopItem disabled title={t("rm.exit.viaNodeHint")}>
              {t("rm.exit.viaNode")}
            </PopItem>
          </>
        )}
      </Pop>,
      {},
      t("rm.ghost.exit"),
      { variant: "ghost", selectable: false },
    ),
  );
  for (const f of model.forwards) {
    nodes.push(
      card(
        f.id,
        232,
        <div className="p-3.5">
          {head("server", f.peer.name, <Badge kind={badgeKind[f.state]}>{say(t, f.badge)}</Badge>)}
          {sub(say(t, f.sub))}
          {chipsOf(f.chips)}
        </div>,
        { in: true },
        f.peer.name,
      ),
    );
  }

  // ---- the wires: one per way
  const edges: GraphEdge[] = model.wires.map((x) => ({
    id: x.id,
    from: { node: x.from, port: "out" },
    to: { node: x.to, port: "in" },
    state: x.style === "flow" ? "ok" : x.style === "partial" ? "warn" : x.style === "fail" ? "bad" : "blocked",
    animated: x.style === "flow",
    weight: Math.min(3.5, 2 + (x.count - 1) * 0.3),
    label: x.label && x.label.n ? <span className="text-[11px]">{say(t, x.label)}</span> : undefined,
    grabbable: false,
  }));

  const dialogs = (
    <>
      {owner && dlg?.kind === "draw" && (
        <AddRelayDialog key={`${dlg.entry.id}/${dlg.inboundId ?? ""}`} open onOpenChange={() => setDlg(null)} entry={dlg.entry} exitId={node.id} preselect={dlg.inboundId} />
      )}
      {owner && dlg?.kind === "remove" && <RemoveRelay r={dlg.r} onClose={() => setDlg(null)} />}
      {owner && dlg?.kind === "port" && <ChangeRelayPort r={dlg.r} onClose={() => setDlg(null)} />}
      {owner && dlg?.kind === "removeWay" && <RemoveWay entry={dlg.entry} exit={node.name} relays={dlg.relays} onClose={() => setDlg(null)} />}
      {owner && <TurnOffDialog open={dlg?.kind === "v6off"} onOpenChange={(o) => !o && setDlg(null)} node={node} />}
      {subs.dialog}
    </>
  );

  const onDropCanvas = (e: DragEvent) => {
    const id = e.dataTransfer.getData(RELAY);
    if (!owner || !id) return;
    e.preventDefault();
    const r = allRelays.find((x) => x.id === id);
    if (r) setDlg({ kind: "remove", r });
  };

  const graph = (
    <div className={cx(full ? "h-full" : "")} onDragOver={(e) => owner && e.dataTransfer.types.includes(RELAY) && e.preventDefault()} onDrop={onDropCanvas}>
      <Graph
        nodes={owner ? nodes : nodes.filter((n) => n.id !== "ghost:exit")}
        edges={edges}
        typeColors={colors}
        ariaLabel={t("wg.graph", { node: node.name })}
        height={full ? "100%" : "fit"}
        className={full ? "h-full rounded-none border-0" : undefined}
        layoutKey={`routemap:${node.id}`}
        arrange={(sizes) => routeLayout(sizes, { colGap: 56 })}
        rearrangeOnChange
        minFitZoom={0.7}
        fitPad={52}
        fitFocus="node"
        wheelPans={full}
        onExpand={onFull ? () => onFull(!full) : undefined}
        expanded={full}
        help={help}
        onHelpChange={setHelp}
        readOnly
        selected={[cardOfWay(open)]}
        onSelectionChange={(ids) => {
          const way = ids[0] ? wayOfCard(ids[0]) : undefined;
          if (way && way !== open && (w.lanes.some((l) => l.id === way) || w.out.some((l) => l.id === way))) onOpen(way);
        }}
        legend={<Legend cc={cc} owner={owner} t={t} />}
        extraShortcuts={owner ? <RouteShortcuts /> : undefined}
      />
    </div>
  );

  if (!full) {
    return (
      <div className="flex min-w-0 flex-col gap-2.5">
        {graph}
        {detail}
        {dialogs}
      </div>
    );
  }
  const helpBtn = (tab: HelpTab, label: string) => (
    <button
      type="button"
      className="mg-glegend-btn"
      data-active={help === tab || undefined}
      aria-expanded={help === tab}
      onClick={() => {
        const next = help === tab ? "" : tab;
        setHelp(next);
        writePref("mg.graph.help", next || null);
      }}
    >
      {label}
    </button>
  );
  return (
    <>
      {createPortal(
        <div
          role="dialog"
          aria-modal="true"
          aria-label={t("wg.full.bar", { node: node.name })}
          className="fixed inset-0 z-[45] flex flex-col bg-canvas"
          onKeyDown={(e) => {
            if (e.key === "Escape" && !help && !(e.target as HTMLElement).closest("[data-gui]")) closeFull();
          }}
        >
          <header className="flex h-12 shrink-0 items-center gap-3 border-b border-line bg-surface px-4">
            <span className="min-w-0 truncate text-[14px] font-semibold">{t("wg.full.bar", { node: node.name })}</span>
            <span className="flex-1" />
            {helpBtn("legend", t("graph.legend"))}
            {helpBtn("shortcuts", t("graph.shortcuts"))}
            <button type="button" aria-label={t("wg.full.close")} title={`${t("wg.full.close")} (Esc)`} onClick={closeFull} className="flex size-8 items-center justify-center rounded-lg text-muted hover:bg-surface-2 hover:text-fg">
              <Icon name="x" size={16} />
            </button>
          </header>
          <div className="flex min-h-0 flex-1">
            <div className="relative min-w-0 flex-1">{graph}</div>
            {detail && <aside className="hidden w-[380px] shrink-0 overflow-y-auto border-s border-line bg-surface p-4 min-[1000px]:block">{detail}</aside>}
          </div>
        </div>,
        document.body,
      )}
      {dialogs}
    </>
  );
}

/** Remove every relay of an entry for this node: one confirmation, then the calls the single removal uses. */
function RemoveWay({ entry, exit, relays, onClose }: { entry: { id: string; name: string }; exit: string; relays: RelayMsg[]; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const go = async () => {
    setPending(true);
    try {
      for (const r of relays) await relaysApi.deleteRelay({ id: r.id });
      onClose();
      toast(t("relay.removed"));
    } catch (e) {
      setError(errorText(e, t));
    } finally {
      setPending(false);
      void qc.invalidateQueries({ queryKey: ["relays"] });
    }
  };
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !pending && onClose()}
      title={t("rm.remove.title", { entry: entry.name })}
      description={t.n("rm.remove.body", relays.length, { exit, entry: entry.name })}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={pending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={pending} onClick={() => void go()}>
            {t("rm.remove.go")}
          </Button>
        </>
      }
    >
      {error && <p className="text-[13px] text-danger-text">{error}</p>}
    </Modal>
  );
}

/** The legend of the route map: the wires, the chips and the cards, drawn with the canvas's own SVG and components. */
function Legend({ cc, owner, t }: { cc: string; owner: boolean; t: T }) {
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
        <p className="text-[11px] text-faint">{t("rm.lg.thick")}</p>
      </HelpGroup>
      <HelpGroup title={t("wg.legend.status")}>
        <div className="flex flex-wrap gap-1.5">
          <Badge kind="ok">{t("rm.badge.ok")}</Badge>
          <Badge kind="warn">{t("rm.badge.partial")}</Badge>
          <Badge kind="cut">{t("rm.badge.blocked", { cc })}</Badge>
          <Badge kind="off">{t("rm.badge.off")}</Badge>
          <Badge kind="bad">{t("rm.badge.bad")}</Badge>
          <Badge kind="busy">{t("rm.badge.checking")}</Badge>
        </div>
      </HelpGroup>
      <HelpGroup title={t("rm.lg.cards")}>
        <ul>
          {row(<Icon name="server" size={15} />, t("rm.lg.way"))}
          {row(<ServerChip kind="ok" label="" tabIndex={-1} className="!h-3 !w-3 !gap-0 !px-0" />, t("rm.lg.chip"))}
          {row(<Icon name="plus" size={14} />, t("rm.lg.ghost"))}
        </ul>
      </HelpGroup>
      {owner && <p className="text-[11px] leading-snug text-faint">{t("rm.lg.help")}</p>}
    </div>
  );
}

function RouteShortcuts() {
  const t = useT();
  return (
    <HelpGroup title={t("rm.k.group")}>
      <ul className="flex flex-col gap-1">
        <KeyRow keys={<Kbd>⋯</Kbd>}>{t("rm.k.menu")}</KeyRow>
        <KeyRow keys={<Kbd>{t("rm.key.dragChip")}</Kbd>}>{t("rm.k.relay")}</KeyRow>
        <KeyRow keys={<Kbd>{t("rm.key.dragOut")}</Kbd>}>{t("rm.k.unrelay")}</KeyRow>
        <KeyRow keys={<Kbd>{t("graph.key.click")}</Kbd>}>{t("rm.k.select")}</KeyRow>
      </ul>
    </HelpGroup>
  );
}

