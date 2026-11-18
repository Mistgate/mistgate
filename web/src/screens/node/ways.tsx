import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { Segmented } from "@/components/ui/segmented";
import { useIsPhone } from "@/lib/media";
import { readPref, writePref } from "@/lib/storage";
import { WaysGraph } from "./ways-graph";
import { useState, type ReactNode } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { walkTabs } from "@/components/ui/master-detail";
import { Notice } from "@/components/ui/notice";
import { Pending } from "@/components/ui/query-error";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { relays as relaysApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { countryQuery } from "@/lib/globalping";
import { checksQuery, countryColumn, countryIn, doctorQuery, type CountryCheck } from "@/lib/health";
import { nodeLoad } from "@/lib/node-load";
import type { Plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import { useFresh } from "@/components/fx/fresh";
import { activeOf } from "@/lib/relay-bridge";
import { isCut, relayState, relaysQuery, seenHint, viaLabel, type Hint, type RelayMsg } from "@/lib/relay";
import { useNow } from "@/lib/time";
import { ipv6Helps } from "@/lib/reachability";
import { useIsOwner } from "@/lib/updates";
import { entryOf, entryOffered, entryState, hintOf, ipv6FactOf, lanesOf, type Ipv6Entry, type Ipv6Fact, type Lane, type Ring, type WayId, type Ways } from "@/lib/ways";
import { CountryRow } from "@/screens/health/country";
import { Glyph } from "@/screens/overview-parts";
import { EntryDialog, entryReason, EntryState, TurnOffDialog } from "./ipv6-entry";
import { Spin, Wa } from "./parts";
import { CheckPortsButton, portRows, Ports, usePortCheck } from "./ports";
import { RelayStrip } from "./relay-strip";
import { RelayWizard } from "./relay-wizard";
import { PairWay } from "./relay-pair";
import { RelayRows } from "./relay-rows";
import { useOpenWizard } from "./relay-open";
import { useCountryOf, useRelayRuns } from "./relays";
import { InSubscriptions } from "./subs-switches";
import type { NodeSearch, WaysView } from "./tabs";
import "./ways.css";

// The node page's "Ways in" tab (node-ia plan §3.4, the board PnNode): every way people reach the node, a list with the open way beside it
// (the board's "How they get here"): straight to its address, through a CDN, through another node, over IPv6; on an entry node the
// nodes reached through it come first ("Who it passes"), each an exit with how its relays work, what was measured and its servers. A list
// or a graph by the switch of the card; `?way=` is the open one, `?add=` the "New way in" dialog ("ipv6": the IPv6 entry's), `?full=1`
// the whole route map. The verdicts are lib/ways.ts's, the relay rows and dialog relay-rows.tsx's and relays.tsx's, the IPv6 entry's
// card and dialog ipv6-entry.tsx's.

type Data = Plain<GetNodeResponse>;

export type WaysModel = Ways & {
  owner: boolean;
  /** The relays have answered (an owner's lanes depend on them): the line is drawn once, without lanes popping in. */
  ready: boolean;
  /** The country people look from (the country check's, else the Reachability setting). */
  cc: string;
  /** The node seen from it, and every other node (once there is a relay to judge). */
  country?: CountryCheck;
  countryOf: (nodeId: string) => CountryCheck | undefined;
  ipv6: Ipv6Fact;
  /** The IPv6 entry (Node.ipv6): on, or whether and why the node can take one. */
  v6: Ipv6Entry;
  /** The last read of the relays failed: with relays already read (`stale`) the pair says so; with none, the tab does. */
  readError: { stale: boolean; retry: () => void } | null;
};

/** The lanes of a node with what they rest on: the node page computes them once, for the tab, its badge and the Overview. */
export function useWays(data: Data): WaysModel {
  const node = data.node!;
  const live = node.status !== NodeStatus.RETIRED;
  const owner = useIsOwner();
  const own = useQuery({ ...checksQuery(node.id), enabled: live });
  const relays = useQuery({ ...relaysQuery(node.id), enabled: owner && live });
  // the entries are judged against the networks that see this node dark: the fleet's country checks, once there is one
  const fleet = useQuery({ ...checksQuery(""), enabled: owner && live && (relays.data ?? []).some((r) => r.exitNodeId === node.id) });
  const doctor = useQuery({ ...doctorQuery(node.id), enabled: live });
  const asked = useQuery({ ...countryQuery, enabled: owner }).data?.country;
  const column = countryColumn(own.data?.countryChecks);
  const fleetColumn = countryColumn(fleet.data?.countryChecks);
  const country = column?.byNode.get(node.id);
  const countryOf = (id: string) => fleetColumn?.byNode.get(id);
  const ipv6 = ipv6FactOf(doctor.data?.nodes.find((n) => n.nodeId === node.id)?.items);
  const v6 = entryOf(node.ipv6, ipv6);
  const ways = lanesOf({
    node,
    inbounds: data.inbounds,
    portChecks: data.portChecks,
    tcpChecks: data.tcpChecks,
    cells: own.data?.rows.find((r) => r.nodeId === node.id)?.cells,
    country,
    relays: owner ? (relays.data ?? []) : undefined,
    countryOf,
    ipv6,
    hasIpv6: data.facts?.hasIpv6,
    entry: entryState(v6),
  });
  const readError = owner && live && relays.isError ? { stale: relays.data !== undefined, retry: () => void relays.refetch() } : null;
  return { ...ways, owner, ready: !owner || !live || relays.data !== undefined || relays.isError, cc: column?.code || asked || "RU", country, countryOf, ipv6, v6, readError };
}

// ---- words of a lane ----

type Look = { kind: StatusKind; word: MessageKey };

/** A lane's state in a word: what is ours to fix first, then how much of the country sees it. An IPv6 entry the node cannot take is "not available". */
function lookOf(l: Lane, w: { offline: boolean; v6: Ipv6Entry }): Look {
  if (w.offline && (l.kind === "direct" || l.kind === "cdn")) return { kind: "off", word: "ways.pill.offline" };
  // every server of the lane is kept out of subscriptions: people are not given this way, the servers still run
  if (l.hidden) return { kind: "off", word: "ways.pill.hidden" };
  switch (l.state) {
    case "bad":
      return { kind: "bad", word: "ways.pill.down" };
    case "warn":
      return { kind: "warn", word: "ways.pill.partly" };
    case "busy":
      return { kind: "busy", word: "ways.pill.busy" };
    case "off":
      return { kind: "off", word: l.kind === "direct" ? "ways.pill.noProfiles" : l.kind === "ipv6" && !w.v6.entrySupported ? "ways.pill.na" : "ways.pill.off" };
  }
  if (l.seen === "none") return { kind: "bad", word: "ways.pill.blocked" };
  if (l.seen === "part") return { kind: "warn", word: "ways.pill.notEverywhere" };
  return { kind: "ok", word: "ways.pill.works" };
}

const toneText: Record<StatusKind, string> = { ok: "text-muted", off: "text-muted", blip: "text-muted", busy: "text-muted", warn: "text-warn-text", bad: "text-danger-text" };

/** The node's servers that people reach through a CDN (the "cdn" lane), whatever their state. */
const cdnOf = (data: Data) => data.inbounds.filter((i) => i.cdn);

/** The rows of the Direct lane's port check: its count in the list is the rows the lane shows. */
const portsOf = (data: Data) => {
  const r = portRows(data);
  return r.udp.length + r.tcp.length;
};

/** "From Russia: all networks", "Dark on MTS, Tele2", "Does not open from Russia", "From Russia: no data". */
function seenWords(t: T, seen: Lane["seen"], dark: string[], cc: string): Hint {
  const country = countryIn(t, cc);
  switch (seen) {
    case "all":
      return { tone: "ok", text: t("ways.seen.all", { country }) };
    case "part":
      return { tone: "warn", text: dark.length ? t("ways.seen.part", { networks: dark.join(", ") }) : t("ways.seen.unknown", { country }) };
    case "none":
      return { tone: "bad", text: t("ways.seen.none", { country }) };
  }
  return { tone: "off", text: t("ways.seen.unknown", { country }) };
}

/** The third line of a lane: is it seen from the country (an entry against the networks that see this node dark). */
function seenLine(t: T, l: Lane, w: WaysModel): Hint | null {
  if (l.kind === "out") return null;
  if ((l.kind === "direct" || l.kind === "cdn") && w.offline) return { tone: "off", text: t("ways.offline") };
  if (l.kind === "cdn") return { tone: "off", text: t("ways.cdn.seen") };
  if (l.kind === "ipv6") {
    // an entry the node cannot take, or one that is on while the server's IPv6 is down: why, in the doctor's words
    if (l.state === "bad" || (l.state === "off" && !w.v6.entrySupported)) {
      return { tone: l.state === "bad" || w.v6.unavailableReason === "ipv6_broken" ? "bad" : "off", text: entryReason(t, w.v6.unavailableReason) };
    }
    return w.country?.ipv6 ? seenWords(t, l.seen, l.dark, w.cc) : null;
  }
  if (l.kind === "via") {
    const own = hintOf(w.country);
    const entry = hintOf(w.countryOf(l.peer!.id));
    const seen = isCut(own) ? seenHint(t, l.peer!.name, own, entry) : null;
    if (seen) return seen;
  }
  return seenWords(t, l.seen, l.dark, w.cc);
}

// ---- the ring of the strip ----

const ringTone: Record<Ring, string> = { ok: "tone-ok", half: "tone-warn", warn: "tone-warn", bad: "tone-bad", busy: "tone-busy", off: "tone-off" };

/** The ring of a lane: filled when it works (half when part of the country does not see it), hollow when off. */
function RingMark({ ring, className }: { ring: Ring; className?: string }) {
  return (
    <span
      aria-hidden
      className={cx(
        ringTone[ring],
        "box-border flex size-2.5 flex-none overflow-hidden rounded-full border-2 border-(color:--c)",
        ring === "ok" || ring === "warn" || ring === "bad" ? "bg-(--c)" : "bg-surface",
        ring === "busy" && "animate-[mg-ui-spin_1s_linear_infinite] border-t-transparent",
        className,
      )}
    >
      {ring === "half" && <span className="h-full w-1/2 bg-(--c)" />}
    </span>
  );
}

// ---- the cards ----

const gtone: Record<Lane["kind"], string> = { direct: "sky", cdn: "sky", via: "lav", out: "lav", ipv6: "" };
const laneGlyph: Record<Lane["kind"], string> = {
  direct: "M5 12h14M13 6l6 6-6 6",
  cdn: "M7 18a4 4 0 0 1-.5-8A5.5 5.5 0 0 1 17 8.5 3.8 3.8 0 0 1 17 18z",
  via: "M4 8h13l-3-3M20 16H7l3 3",
  out: "M4 8h13l-3-3M20 16H7l3 3",
  ipv6: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18",
};
/** A state as the board's two words of colour: the dot of a row says it ("ok", "warn", "crit", "idle"); a turning-on one spins. */
const dotTone = (k: StatusKind): "ok" | "warn" | "crit" | "idle" => (k === "ok" ? "ok" : k === "warn" ? "warn" : k === "bad" ? "crit" : "idle");
const emTone = (t: Hint["tone"]) => (t === "warn" ? "warn" : t === "bad" ? "crit" : "");

function Dot({ kind }: { kind: StatusKind }) {
  const tone = dotTone(kind);
  return <i aria-hidden className={cx("wy-dot", tone !== "ok" && tone, kind === "busy" && "busy")} />;
}

/** The state of a row: the dot, then the word. */
function Gs({ kind, word }: { kind: StatusKind; word: string }) {
  return (
    <span className={cx("wy-gs", dotTone(kind))}>
      <Dot kind={kind} />
      {word}
    </span>
  );
}

const cardCls = "relative overflow-hidden rounded-card border border-line bg-surface";

/** The Segmented control of a card's head: the list or the graph. */
function ViewSeg({ view, onView }: { view: WaysView; onView: (v: WaysView) => void }) {
  const t = useT();
  return (
    <Segmented
      variant="flat"
      aria-label={t("wg.view")}
      value={view}
      onValueChange={onView}
      options={[
        { value: "list", label: t("wg.view.list") },
        { value: "graph", label: t("wg.view.graph") },
      ]}
    />
  );
}

/** The people of the list: their country's flag and who they are. */
function People({ w, sub }: { w: WaysModel; sub: ReactNode }) {
  const t = useT();
  const fmt = useFmt();
  return (
    <div className="wy-src">
      <span className="wy-g">{w.cc ? <Flag code={w.cc} size={20} /> : null}</span>
      <div>
        <b>{t("ways.people", { country: fmt.country(w.cc) })}</b>
        <span>{sub}</span>
      </div>
      <span />
    </div>
  );
}

/** The y of the n-th of `count` nodes of the graph, in percent. */
const graphY = (i: number, count: number) => (count === 1 ? 50 : count === 2 ? [30, 70][i]! : count === 3 ? [18, 50, 82][i]! : count === 4 ? [12, 37, 63, 88][i]! : 8 + (84 * i) / (count - 1));
const graphH = (count: number) => Math.max(280, count * 64 + 24);

function laneTitle(t: T, l: Lane) {
  return l.kind === "direct" ? t("ways.direct") : l.kind === "cdn" ? t("ways.cdn") : l.kind === "via" ? t("ways.via", { entry: l.peer!.name }) : l.kind === "ipv6" ? t("ways.ipv6") : l.peer!.name;
}

function LaneRow({ l, w, data, selected, tabbable, onPick }: { l: Lane; w: WaysModel; data: Data; selected: boolean; tabbable: boolean; onPick: () => void }) {
  const t = useT();
  const fresh = useFresh<HTMLButtonElement>("lane", `${data.node!.id}:${l.id}`); // a lane that just came to be sweeps in once
  const look = lookOf(l, w);
  const seen = seenLine(t, l, w);
  // how many of its servers go through the bridge now
  const bridged = l.relays.filter((r) => r.enabled && activeOf(r) === "reverse").length;
  const where =
    l.kind === "direct"
      ? data.inbounds.length === 0
        ? t("ways.noProfiles")
        : portsOf(data) > 0
          ? `${data.node!.address} · ${t.n("ways.ports", portsOf(data))}`
          : data.node!.address
      : l.kind === "ipv6"
        ? w.v6.entry || t(w.v6.entrySupported ? "ways.ipv6.lane.off" : "ways.ipv6.lane.na")
        : l.kind === "cdn"
          ? t.n("ways.servers", cdnOf(data).length)
          : [t.n("wy.lane.relay", l.relays.length), bridged > 0 ? t("relay.way.lane", { n: bridged }) : ""].filter(Boolean).join(" · ");
  const ghost = l.state === "off" && l.kind !== "direct";
  const mono = (l.kind === "direct" && data.inbounds.length > 0) || (l.kind === "ipv6" && !!w.v6.entry);
  return (
    <button
      type="button"
      role="tab"
      ref={fresh}
      id={`way-tab-${l.id}`}
      aria-selected={selected}
      aria-controls="way-panel"
      tabIndex={tabbable ? 0 : -1}
      onClick={onPick}
      data-ring={l.ring}
      className={cx("wy-way", selected && "on", ghost && "ghost")}
    >
      <span className={cx("wy-g", gtone[l.kind])}>
        <Glyph d={laneGlyph[l.kind]} />
      </span>
      <div>
        <b>{laneTitle(t, l)}</b>
        <span className={mono ? "font-mono text-xs" : undefined}>{where}</span>
        {seen && <em className={emTone(seen.tone)}>{seen.text}</em>}
      </div>
      <Gs kind={look.kind} word={t(look.word)} />
    </button>
  );
}

/** A dashed row that adds a way: the board's "Add a way…". */
function AddRow({ label, note, on, onClick }: { label: string; note?: string; on?: boolean; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className={cx("wy-way add", on && "on")}>
      <span className="wy-g">
        <Icon name="plus" size={16} />
      </span>
      <div>
        <b>{label}</b>
        {note && <span>{note}</span>}
      </div>
      <span />
    </button>
  );
}

/** The graph of the exit: people, the ways, the node; the dotted lines run along the open way. */
function MiniGraph({ data, w, open, onOpen }: { data: Data; w: WaysModel; open: WayId; onOpen: (id: WayId) => void }) {
  const t = useT();
  const node = data.node!;
  const lanes = w.lanes;
  const nets = new Set((w.country?.vantages ?? []).map((v) => v.network || v.key)).size;
  return (
    <div className="wy-wg" style={{ height: graphH(lanes.length) }} data-testid="ways-graph">
      <svg viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden>
        {lanes.flatMap((l, i) => {
          const y = graphY(i, lanes.length);
          const k = cx(l.kind === "via" ? "lav" : "sky", l.id === open && "on", l.state === "bad" && "crit", l.state === "off" && "idle");
          return [<path key={`${l.id}a`} className={k} d={`M22 50C30 50 30 ${y} 38 ${y}`} />, <path key={`${l.id}b`} className={k} d={`M62 ${y}C70 ${y} 70 50 78 50`} />];
        })}
      </svg>
      <div className="wy-wn a">
        <span className="wy-g">{w.cc ? <Flag code={w.cc} size={20} /> : null}</span>
        <div>
          <b>{t("wy.people")}</b>
          {nets > 0 && <span>{t.n("wy.nets", nets)}</span>}
        </div>
      </div>
      {lanes.map((l, i) => {
        const look = lookOf(l, w);
        return (
          <button key={l.id} type="button" className={cx("wy-wn m", l.id === open && "on", l.state === "off" && l.kind !== "direct" && "ghost")} style={{ top: `${graphY(i, lanes.length)}%` }} aria-pressed={l.id === open} aria-label={laneTitle(t, l)} onClick={() => onOpen(l.id)}>
            <span className={cx("wy-g", gtone[l.kind])}>
              <Glyph d={laneGlyph[l.kind]} />
            </span>
            <div>
              <b>{laneTitle(t, l)}</b>
              <span>{t(look.word)}</span>
            </div>
            <Dot kind={look.kind} />
          </button>
        );
      })}
      <div className="wy-wn z">
        <span className="wy-g">
          <Flag code={node.countryCode} size={20} />
        </span>
        <div>
          <b className="font-mono">{node.name}</b>
          <span>{node.location || node.address}</span>
        </div>
      </div>
    </div>
  );
}

/** The ways of an exit: people at the top, a button per way, "Add a way…" last; a graph by the switch. */
function WayList({ data, w, open, onPick, onAdd, onMake, onView, view, onFull }: { data: Data; w: WaysModel; open: WayId; onPick: (id: WayId) => void; onAdd: () => void; onMake: () => void; onView: (v: WaysView) => void; view: WaysView; onFull: () => void }) {
  const t = useT();
  const node = data.node!;
  const own = w.lanes[0]!;
  const dark = own.seen === "part" || own.seen === "none";
  const others = (useQuery({ ...nodesQuery, enabled: w.owner }).data?.nodes ?? []).some((n) => n.id !== node.id && n.status !== NodeStatus.RETIRED);
  // the owner adds a way in when there is another node to go through, or an IPv6 entry to turn on
  const canAdd = w.owner && (others || entryOffered(w.v6));
  const noRelay = !w.lanes.some((l) => l.kind === "via");
  return (
    <section className={cx(cardCls, "wy-ways")} aria-label={t("ways.list", { node: node.name })}>
      <div className="wy-ch">
        <h3>{t("wy.ways.title")}</h3>
        <ViewSeg view={view} onView={onView} />
      </div>
      {view === "list" ? (
        <div role="tablist" aria-label={t("ways.list", { node: node.name })} aria-orientation="vertical" onKeyDown={walkTabs} className="wy-wl">
          <People w={w} sub={t("wy.people.sub")} />
          {w.lanes.map((l) => (
            <LaneRow key={l.id} l={l} w={w} data={data} selected={l.id === open} tabbable={l.id === open} onPick={() => onPick(l.id)} />
          ))}
          {/* a way the node has no relay for: a ghost row, so the owner sees the way that is missing */}
          {canAdd && noRelay && others && !dark && (
            <button type="button" className="wy-way ghost" onClick={onAdd}>
              <span className="wy-g lav">
                <Glyph d={laneGlyph.via} />
              </span>
              <div>
                <b>{t("wy.ghost.title")}</b>
                <span>{t("wy.ghost.sub")}</span>
              </div>
              <Gs kind="off" word={t("wy.ghost.state")} />
            </button>
          )}
          {canAdd && dark && (
            <Button variant="primary" size="lg" full className="wy-mk" onClick={onMake}>
              {t("wy.make", { country: countryIn(t, w.cc) })}
            </Button>
          )}
          {canAdd && (
            <AddRow
              label={t("ways.add")}
              note={dark ? (own.seen === "none" || own.dark.length === 0 ? t("ways.add.darkAll", { country: countryIn(t, w.cc) }) : t("ways.add.dark", { networks: own.dark.join(", ") })) : t("wy.add.sub")}
              onClick={onAdd}
            />
          )}
        </div>
      ) : (
        <>
          <MiniGraph data={data} w={w} open={open} onOpen={onPick} />
          <div className="px-6 pb-4 max-[720px]:px-4">
            <button type="button" className="wy-lk" onClick={onFull}>
              {t("wy.graph.full")}
            </button>
          </div>
        </>
      )}
    </section>
  );
}

/** The head of a card: the glyph chip, the title and what it is, the state at the end. */
function WayCard({ lane, icon, tone, title, sub, pill, children }: { lane: Lane; icon: string; tone: string; title: ReactNode; sub: ReactNode; pill?: ReactNode; children: ReactNode }) {
  return (
    <section role="tabpanel" id="way-panel" aria-labelledby={`way-tab-${lane.id}`} className={cx(cardCls, "wy-wd min-w-0")}>
      <div className="wy-ch">
        <span className={cx("nd-wi", tone)} aria-hidden>
          <Glyph d={icon} />
        </span>
        <div className="nd-wt">
          <h3>{title}</h3>
          <span>{sub}</span>
        </div>
        {pill}
      </div>
      {children}
    </section>
  );
}

const Pill = ({ look }: { look: Look }) => {
  const t = useT();
  return <StatusPill kind={look.kind} label={t(look.word)} />;
};

// ---- the tab ----

export function WaysTab({ data, ways: w }: { data: Data; ways: WaysModel }) {
  const t = useT();
  const navigate = useNavigate();
  const node = data.node!;
  const search = useSearch({ strict: false }) as NodeSearch;
  const phone = useIsPhone();
  // an entry passes relays on: its exits come first, the ways in to the entry itself below
  const entry = w.out.length > 0;
  const laneIds = w.lanes.map((l) => l.id);
  const asked: WaysView | undefined = search.view === "graph" || search.view === "list" ? search.view : undefined;
  // the view is the URL's, else the one this browser used last, else the list
  const view: WaysView = asked ?? (readPref("ways.view") === "graph" ? "graph" : "list");
  const go = (next: NodeSearch, replace = true) =>
    void navigate({ to: "/nodes/$id", params: { id: node.id }, search: { tab: "ways", ...(asked ? { view: asked } : {}), ...(search.map ? { map: search.map } : {}), ...(search.full ? { full: search.full } : {}), ...(search.from ? { from: search.from } : {}), ...next }, replace, resetScroll: false });
  // the route map (the canvas) is the graph's second face; a phone has none
  const map = view === "graph" && search.map === "1" && !phone;
  const full = map && search.full === "1";
  const setView = (v: WaysView) => {
    writePref("ways.view", v);
    go({ way: search.way, view: v });
  };
  // each of the two lists (an entry has both) keeps its own open way; `?way=` is the one clicked last
  const [lastLane, setLastLane] = useState<WayId | undefined>();
  const [lastOut, setLastOut] = useState<WayId | undefined>();
  const chosenLane = search.way && laneIds.includes(search.way) ? search.way : undefined;
  const chosenOut = search.way && w.out.some((l) => l.id === search.way) ? search.way : undefined;
  const openLane = chosenLane ?? (lastLane && laneIds.includes(lastLane) ? lastLane : entry ? laneIds[0]! : w.first);
  const openOut = chosenOut ?? (lastOut && w.out.some((l) => l.id === lastOut) ? lastOut : w.out[0]?.id);
  const pickLane = (id: WayId) => {
    setLastLane(id);
    go({ way: id });
  };
  const pickOut = (id: WayId) => {
    setLastOut(id);
    go({ way: id });
  };

  if (!w.ready) return <Pending />;
  const unread = w.readError && !w.readError.stale ? w.readError : null;
  const lane = w.lanes.find((l) => l.id === openLane) ?? w.lanes[0]!;
  const pair = w.out.find((l) => l.id === openOut);
  const make = () => go({ way: search.way, add: "1" });
  const laneDetail = (
    <>
      {lane.kind === "direct" ? (
        <DirectDetail data={data} w={w} lane={lane} />
      ) : lane.kind === "cdn" ? (
        <CdnDetail data={data} w={w} lane={lane} />
      ) : lane.kind === "ipv6" ? (
        <Ipv6Detail data={data} w={w} lane={lane} onAdd={() => go({ way: "ipv6", add: "ipv6" })} />
      ) : (
        <RelayDetail key={lane.id} data={data} w={w} lane={lane} onOther={make} onMore={() => go({ way: lane.id, add: `via:${lane.peer!.id}` })} />
      )}
    </>
  );
  // the entry's ways in are one line when it has no profiles of its own and a single way: people come to it directly
  const compact = entry && w.lanes.length === 1 && w.lanes[0]!.kind === "direct" && data.inbounds.length === 0;
  const dlgs = (
    <>
      {w.owner && <RelayWizard data={data} w={w} start={search.add} onClose={(way) => go({ way: way ?? search.way })} />}
      {w.owner && (
        <EntryDialog open={search.add === "ipv6" && entryOffered(w.v6)} onClose={() => go({ way: "ipv6" })} node={node} v6={w.v6} awg={data.inbounds.some((i) => i.protocol === "awg")} />
      )}
    </>
  );

  if (map)
    return (
      <div className="flex min-w-0 flex-col gap-3.5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <button type="button" className="wy-lk" onClick={() => go({ way: search.way, map: undefined, full: undefined })}>
            ‹ {t("wy.map.back")}
          </button>
          <ViewSeg view={view} onView={setView} />
        </div>
        <WaysGraph
          data={data}
          w={w}
          open={openLane}
          onOpen={(id) => go({ way: id })}
          onIpv6={() => go({ way: "ipv6", add: "ipv6" })}
          detail={<div role="tabpanel" id="way-panel" aria-labelledby={`way-tab-${lane.id}`} className="flex-1">{laneDetail}</div>}
          full={full}
          onFull={(on) => void navigate({ to: "/nodes/$id", params: { id: node.id }, search: { tab: "ways", view: "graph", map: "1", ...(search.way ? { way: search.way } : {}), ...(on ? { full: "1" as const } : {}) }, replace: true, resetScroll: false })}
        />
        {dlgs}
      </div>
    );

  return (
    <div className="flex min-w-0 flex-col">
      {unread && (
        <div className="wy-plt mb-4" data-testid="relays-unread">
          <div>
            <span>{t("wy.br.read")}</span>
          </div>
          <Button size="md" onClick={unread.retry}>
            {t("wy.br.retry")}
          </Button>
        </div>
      )}
      <RelayStrip w={w} node={node} onOpen={(id) => go({ way: search.way, add: `via:${id}` })} />
      {entry && (
        <div className={cx("wy-in wy-in2", view === "graph" && "graph")}>
          <PassList data={data} w={w} open={openOut} onPick={pickOut} onMore={() => void navigate({ to: "/nodes", search: { view: "ways" } })} view={view} onView={setView} />
          {pair && <PairCard key={pair.id} data={data} w={w} lane={pair} onOther={make} />}
        </div>
      )}
      {compact ? (
        <SelfCard data={data} w={w} lane={w.lanes[0]!} />
      ) : (
        <div className={cx("wy-in", view === "graph" && "graph")}>
          <WayList
            data={data}
            w={w}
            open={openLane}
            onPick={pickLane}
            onAdd={() => go({ way: search.way, add: "1" })}
            onMake={make}
            onView={setView}
            view={view}
            onFull={() => go({ way: search.way, view: "graph", map: "1" })}
          />
          {laneDetail}
        </div>
      )}
      {dlgs}
    </div>
  );
}

// ---- the entry: who it passes ----

/** The pair's state of the entry's list: its lane's look. */
function PairRow({ l, w, selected, onPick }: { l: Lane; w: WaysModel; selected: boolean; onPick: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const ccOf = useCountryOf();
  const look = lookOf(l, w);
  const bridged = l.relays.filter((r) => r.enabled && activeOf(r) === "reverse").length;
  const mode = bridged === 0 ? t("nw.c.fwd") : bridged === l.relays.filter((r) => r.enabled).length ? t("nw.c.bridge") : t("relay.way.lane", { n: bridged });
  const bps = l.relays.reduce((a, r) => a + Number(r.bps), 0);
  return (
    <button type="button" role="tab" id={`way-tab-${l.id}`} aria-selected={selected} aria-controls="pair-panel" tabIndex={selected ? 0 : -1} onClick={onPick} data-ring={l.ring} className={cx("wy-way", selected && "on")}>
      <span className="wy-g">
        <Flag code={ccOf(l.peer!.id)} size={20} />
      </span>
      <div>
        <b className="font-mono">{l.peer!.name}</b>
        <span>{[t.n("ways.servers", l.relays.length), mode].join(" · ")}</span>
        {bps > 0 && <em>{fmt.mbit(bps * 8)}</em>}
      </div>
      <Gs kind={look.kind} word={t(look.word)} />
    </button>
  );
}

/** "Who it passes": the entry's exits, a button per exit, the graph by the switch, its own load under them. */
function PassList({ data, w, open, onPick, onMore, view, onView }: { data: Data; w: WaysModel; open: WayId | undefined; onPick: (id: WayId) => void; onMore: () => void; view: WaysView; onView: (v: WaysView) => void }) {
  const t = useT();
  const fmt = useFmt();
  const ccOf = useCountryOf();
  const node = data.node!;
  const nowS = Math.floor(useNow(30_000) / 1000);
  const load = nodeLoad(data.metrics, node.bandwidthMbps, nowS);
  const relays = w.out.reduce((a, l) => a + l.relays.length, 0);
  const down = w.out.reduce((a, l) => a + l.relays.filter((r) => relayState(r) === "failed").length, 0);
  const sum = [t.n("wy.pass.nodes", w.out.length), t.n("wy.pass.relays", relays), down > 0 ? t.n("wy.pass.down", down) : t("wy.pass.allOk")].join(" · ");
  const rank: Record<StatusKind, number> = { bad: 0, warn: 1, busy: 2, off: 3, blip: 3, ok: 4 };
  const pairs = [...w.out].sort((a, b) => rank[lookOf(a, w).kind] - rank[lookOf(b, w).kind]);
  const selected = pairs.find((l) => l.id === open) ?? pairs[0];
  const m = data.metrics;
  return (
    <section className={cx(cardCls, "wy-pl")}>
      <div className="wy-ch">
        <h3>{t("wy.pass.title")}</h3>
        <ViewSeg view={view} onView={onView} />
      </div>
      {view === "list" ? (
        <div role="tablist" aria-label={t("wy.pass.title")} aria-orientation="vertical" onKeyDown={walkTabs} className="wy-wl">
          <People w={w} sub={<>{t("wy.pass.through", { node: node.name })} {sum}</>} />
          {pairs.map((l) => (
            <PairRow key={l.id} l={l} w={w} selected={l.id === selected?.id} onPick={() => onPick(l.id)} />
          ))}
          {w.owner && <AddRow label={t("wy.pass.more")} note={t("wy.pass.moreSub")} onClick={onMore} />}
        </div>
      ) : (
        <div className="wy-wg" style={{ height: graphH(pairs.length) }} data-testid="pass-graph">
          <svg viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden>
            <path className="lav on" d="M22 50L38 50" />
            {pairs.map((l, i) => {
              const bad = lookOf(l, w).kind;
              return <path key={l.id} className={cx("lav", l.id === selected?.id && "on", bad === "bad" && "crit", (bad === "off" || bad === "busy") && "idle")} d={`M62 50C70 50 70 ${graphY(i, pairs.length)} 78 ${graphY(i, pairs.length)}`} />;
            })}
          </svg>
          <div className="wy-wn a">
            <span className="wy-g">{w.cc ? <Flag code={w.cc} size={20} /> : null}</span>
            <div>
              <b>{t("wy.people")}</b>
            </div>
          </div>
          <div className="wy-wn m">
            <span className="wy-g">
              <Flag code={node.countryCode} size={20} />
            </span>
            <div>
              <b className="font-mono">{node.name}</b>
              <span>{[t.n("wy.pass.nodes", pairs.length), t.n("wy.pass.relays", relays)].join(" · ")}</span>
            </div>
          </div>
          {pairs.map((l, i) => {
            const look = lookOf(l, w);
            return (
              <button key={l.id} type="button" className={cx("wy-wn z", l.id === selected?.id && "on")} style={{ top: `${graphY(i, pairs.length)}%` }} aria-pressed={l.id === selected?.id} aria-label={l.peer!.name} onClick={() => onPick(l.id)}>
                <span className="wy-g">
                  <Flag code={ccOf(l.peer!.id)} size={20} />
                </span>
                <div>
                  <b className="font-mono">{l.peer!.name}</b>
                  <span>{t.n("ways.servers", l.relays.length)}</span>
                </div>
                <Dot kind={look.kind} />
              </button>
            );
          })}
        </div>
      )}
      <div className="wy-ld">
        {load ? (
          <span>
            {t("wy.load")} <b className="mono">{load.percent} %</b>
          </span>
        ) : null}
        {m && (
          <span className="mono">
            ↓{fmt.mbit(m.netRxBps)} ↑{fmt.mbit(m.netTxBps)} · CPU {Math.round(m.cpuPct)} %
          </span>
        )}
        {node.bandwidthMbps <= 0 && (
          <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "server", sv: "ch" }} className="wy-lk">
            {t("wy.load.set")}
          </Link>
        )}
      </div>
    </section>
  );
}

/** The newest check of a group of relays, in unix seconds. */
const checkedAt = (rs: readonly RelayMsg[]) => Math.max(0, ...rs.map((r) => r.check?.atUnix ?? 0));

/** The exit reached through this entry: how it works, what was measured, its servers. */
function PairCard({ data, w, lane, onOther }: { data: Data; w: WaysModel; lane: Lane; onOther: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const runs = useRelayRuns();
  const ccOf = useCountryOf();
  const nowS = Math.floor(useNow(15_000) / 1000);
  const node = data.node!;
  const peer = lane.peer!;
  const look = lookOf(lane, w);
  const rs = lane.relays;
  const openWizard = useOpenWizard();
  const [removing, setRemoving] = useState(false);
  const bps = rs.reduce((a, r) => a + Number(r.bps), 0);
  const at = checkedAt(rs);
  const forwardDrop = rs.some((r) => r.enabled && r.lastError === "forward_drop");
  const port = node.bridgePort;
  return (
    <section role="tabpanel" id="pair-panel" aria-labelledby={`way-tab-${lane.id}`} className={cx(cardCls, "wy-wd wy-pd min-w-0")}>
      <div className="wy-ch">
        <span className="nd-wi lav" aria-hidden>
          <Glyph d={laneGlyph.out} />
        </span>
        <div className="nd-wt">
          <h3>
            <span className="font-mono">{peer.name}</span> {t("wy.pass.via")} <span className="font-mono">{node.name}</span>
          </h3>
          <span>{t("ways.out.sub", { exit: peer.name })}</span>
        </div>
        <Pill look={look} />
      </div>
      <dl className="wy-fx">
        <div>
          <dt>{t("wy.fx.traffic")}</dt>
          <dd>{bps > 0 ? fmt.mbit(bps * 8) : "—"}</dd>
        </div>
        <div>
          <dt>{t("wy.fx.checked")}</dt>
          <dd>{at > 0 ? fmt.ago(at, nowS * 1000) : t("nw.nodata")}</dd>
        </div>
      </dl>
      {forwardDrop && (
        <div className="px-6 pb-2 max-[720px]:px-4">
          <Notice tone="danger">{t("relay.err.forward_drop")}</Notice>
        </div>
      )}
      <PairWay rs={rs} entry={node.name} exit={peer.name} cc={w.cc} exitSeen={w.countryOf(peer.id)} entrySeen={w.country} runs={runs} readOnly={!w.owner} nowS={nowS} onOther={w.owner ? onOther : undefined} readError={w.readError?.stale} onRetry={w.readError?.retry} />
      <div className="wy-sh">
        <h4>{t("ways.block.servers")}</h4>
        <span>{t.n("wy.pass.onPath", rs.length)}</span>
      </div>
      <RelayRows list={rs} runs={runs} readOnly={!w.owner} people={w.cc} onOther={w.owner ? onOther : undefined} />
      {w.owner && (
        <div className="wy-act">
          <Button size="md" disabled={runs.checking} onClick={() => void runs.checkAll(rs)}>
            {runs.checking && <Spin />}
            {t("ways.checkAll")}
          </Button>
          <Button size="md" onClick={() => openWizard(peer.id, node.id)}>
            {t("nw.a.more")}
          </Button>
          <Link to="/nodes/$id" params={{ id: peer.id }} search={{ tab: "ways", way: `via:${node.id}` }} className="wy-lk">
            {t("ways.open", { node: peer.name })} ›
          </Link>
        </div>
      )}
      {w.owner && (
        <RemovePair rs={rs} entry={node.name} exit={peer.name} entryCc={node.countryCode} exitCc={ccOf(peer.id)} asking={removing} onAsk={setRemoving} onDone={() => setRemoving(false)} />
      )}
      <p className="wy-foot">
        <span>{t("nw.foot.twice")}</span>
        <span>{t("wy.foot.sees", { entry: node.name })}</span>
        {port > 0 && (
          <span>
            {port === 443 ? t("wy.foot.bridgeSite", { entry: node.name }) : t("wy.foot.bridgePort", { port, reason: bridgeWhy(t, node.bridgePortReason) })}
            {node.bridgePortProfileId && port !== 443 && (
              <>
                {" "}
                <Link to="/profiles/$id" params={{ id: node.bridgePortProfileId }} className="wy-lk">
                  {t("wy.foot.profile")}
                </Link>
              </>
            )}
          </span>
        )}
      </p>
    </section>
  );
}

const bridgeWhy = (t: T, code: string) => (code === "reality_external_target" || code === "port_443_taken" ? t(`wy.foot.why.${code}` as const) : code);

/** "Remove the path through msk1…": the relays of the pair go together, after a question that says what people lose. */
function RemovePair({ rs, entry, exit, entryCc, exitCc, asking, onAsk, onDone }: { rs: readonly RelayMsg[]; entry: string; exit: string; entryCc: string; exitCc: string; asking: boolean; onAsk: (v: boolean) => void; onDone: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [error, setError] = useState<unknown>(null);
  const hasAwg = rs.some((r) => r.protocol === "amneziawg");
  const remove = async () => {
    setError(null);
    try {
      for (const r of rs) await relaysApi.deleteRelay({ id: r.id });
      toast(t("relay.removed"));
      onDone();
    } catch (e) {
      setError(e);
    } finally {
      await qc.invalidateQueries({ queryKey: ["relays"] });
    }
  };
  if (!asking)
    return (
      <button type="button" className="wy-rm" onClick={() => onAsk(true)}>
        {t("nw.a.remove", { entry })}
      </button>
    );
  return (
    <div className="wy-del" role="group" aria-label={t("nw.a.remove", { entry })}>
      <b>{t.n("nw.del.t", rs.length, { entry, exit })}</b>
      <p>{t("nw.del.s", { label: `${exitCc.toUpperCase()} · ${viaLabel(t, entryCc)}`, exit })}</p>
      {hasAwg && <p className="warn">{t("nw.del.awg")}</p>}
      {error != null && <p className="crit">{errorText(error, t)}</p>}
      <div>
        <Button size="md" onClick={() => onAsk(false)}>
          {t("common.cancel")}
        </Button>
        <Button size="md" variant="outline" className="border-danger! text-danger-text! hover:bg-danger-soft!" onClick={() => void remove()}>
          {t("relay.remove")}
        </Button>
      </div>
    </div>
  );
}

/** The seen-from-the-country section: a chip per network, the sources behind them on request. */
function Nets({ w, name, check, six }: { w: WaysModel; name: string; check?: CountryCheck; six?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const [more, setMore] = useState(false);
  const hint = hintOf(check);
  const nets = new Map<string, boolean>();
  for (const v of check?.vantages ?? []) {
    const n = v.network || v.key;
    nets.set(n, (nets.get(n) ?? false) || v.state === "dark");
  }
  const dark = hint?.verdict === "blocked" ? "crit" : "warn";
  return (
    <>
      <div className="wy-sh">
        <h4>{t(six ? "ways.block.seen6" : "ways.block.seen", { node: name, country: countryIn(t, w.cc) })}</h4>
        {check && <span>{fmt.ago(check.atUnix)}</span>}
        {check && (
          <button type="button" className="wy-lk wy-sa" aria-expanded={more} onClick={() => setMore((x) => !x)}>
            {more ? t("wy.nets.less") : t("wy.nets.more")}
          </button>
        )}
      </div>
      {nets.size > 0 && (
        <div className="wy-nets">
          {[...nets].map(([n, off]) => (
            <span key={n} className={off ? dark : ""}>
              <Dot kind={off ? (dark === "crit" ? "bad" : "warn") : "ok"} />
              <b>{n}</b>
              <em>{off ? t("wy.nets.shut") : ""}</em>
            </span>
          ))}
        </div>
      )}
      {check && (more || nets.size === 0) && (
        <div className="px-6 pb-5 max-[720px]:px-4">
          <CountryRow check={check} code={w.cc} />
        </div>
      )}
      {!check && (
        <p className="wy-hint !mt-1">
          {t("ways.noCountry")}{" "}
          {w.owner && (
            <Link to="/reachability" className="wy-lk">
              {t("ways.sources")}
            </Link>
          )}
        </p>
      )}
    </>
  );
}

/** The entry's own way in, when people come to it directly and it has nothing else: the board's one card. */
function SelfCard({ data, w, lane }: { data: Data; w: WaysModel; lane: Lane }) {
  const t = useT();
  const node = data.node!;
  const look = lookOf(lane, w);
  return (
    <section className={cx(cardCls, "wy-self")} id="way-panel" aria-label={t("wy.self.title")}>
      <div className="wy-ch">
        <span className="nd-wi sky" aria-hidden>
          <Glyph d={laneGlyph.direct} />
        </span>
        <div className="nd-wt">
          <h3>{t("wy.self.title")}</h3>
          <span>{t("wy.self.sub", { node: node.name })}</span>
        </div>
        <Pill look={look} />
      </div>
      <Nets w={w} name={node.name} check={w.country} />
    </section>
  );
}

// ---- the open lane ----

function DirectDetail({ data, w, lane }: { data: Data; w: WaysModel; lane: Lane }) {
  const t = useT();
  const node = data.node!;
  const look = lookOf(lane, w);
  const run = usePortCheck(node.id);
  const none = data.inbounds.length === 0;
  return (
    <WayCard lane={lane} icon={laneGlyph.direct} tone="sky" title={t("ways.direct")} sub={t("ways.direct.sub", { address: node.address })} pill={<Pill look={look} />}>
      {w.offline && (
        <Wa tone="crit" title={t("wy.note.offline")} className="mx-6 mb-2 max-[720px]:mx-4">
          {t("ways.offline")}
        </Wa>
      )}
      {none ? (
        <div className="wy-off">
          <b>{t("ways.noProfilesBody")}</b>
          <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "profiles" }} className={buttonClass("primary", "lg")}>
            <Icon name="plus" size={14} />
            {t("node.profiles.add")}
          </Link>
        </div>
      ) : (
        <>
          <div className="wy-sh">
            <h4>{t("wy.ports.title")}</h4>
            <span>{t("wy.ports.note")}</span>
            <span className="wy-sa">
              <CheckPortsButton data={data} run={run} />
            </span>
          </div>
          <Ports data={data} run={run} subs />
        </>
      )}
      <Nets w={w} name={node.name} check={w.country} />
      {!none && (
        <div className="wy-act">
          <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "profiles" }} className="wy-lk !ms-0">
            {t("node.tab.profiles")} ›
          </Link>
        </div>
      )}
    </WayCard>
  );
}

/** The profiles of the node that people reach through a CDN: the country sees the CDN, so there is no country verdict here. */
function CdnDetail({ data, w, lane }: { data: Data; w: WaysModel; lane: Lane }) {
  const t = useT();
  const node = data.node!;
  const look = lookOf(lane, w);
  return (
    <WayCard lane={lane} icon={laneGlyph.cdn} tone="sky" title={t("ways.cdn")} sub={t("ways.cdn.sub")} pill={<Pill look={look} />}>
      <InSubscriptions nodeId={node.id} nodeName={node.name} address={node.address} inbounds={cdnOf(data)} cdn />
      <div className="wy-act">
        <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "profiles" }} className="wy-lk !ms-0">
          {t("node.tab.profiles")} ›
        </Link>
      </div>
    </WayCard>
  );
}

/** Through an entry (on the exit): the relays, how they work, what was measured, the servers, the other node. */
function RelayDetail({ data, w, lane, onOther, onMore }: { data: Data; w: WaysModel; lane: Lane; onOther: () => void; onMore: () => void }) {
  const t = useT();
  const ccOf = useCountryOf();
  const runs = useRelayRuns();
  const nowS = Math.floor(useNow(15_000) / 1000);
  const [removing, setRemoving] = useState(false);
  const node = data.node!;
  const peer = lane.peer!;
  const look = lookOf(lane, w);
  const forwardDrop = lane.relays.some((r) => r.enabled && r.lastError === "forward_drop");
  return (
    <WayCard lane={lane} icon={laneGlyph.via} tone="lav" title={t("ways.via", { entry: peer.name })} sub={t("ways.via.sub", { entry: peer.name, node: node.name })} pill={<Pill look={look} />}>
      {forwardDrop && (
        <div className="px-6 pb-2 max-[720px]:px-4">
          <Notice tone="danger">{t("relay.err.forward_drop")}</Notice>
        </div>
      )}
      <PairWay rs={lane.relays} entry={peer.name} exit={node.name} cc={w.cc} exitSeen={w.country} entrySeen={w.countryOf(peer.id)} runs={runs} readOnly={!w.owner} nowS={nowS} onOther={w.owner ? onOther : undefined} readError={w.readError?.stale} onRetry={w.readError?.retry} />
      <div className="wy-sh">
        <h4>{t("ways.block.servers")}</h4>
        <span>{t.n("wy.pass.onPath", lane.relays.length)}</span>
      </div>
      <RelayRows list={lane.relays} runs={runs} readOnly={!w.owner} people={w.cc} onOther={w.owner ? onOther : undefined} />
      <Nets w={w} name={peer.name} check={w.countryOf(peer.id)} />
      {w.owner && (
        <div className="wy-act">
          <Button size="md" disabled={runs.checking} onClick={() => void runs.checkAll(lane.relays)}>
            {runs.checking && <Spin />}
            {t("ways.checkAll")}
          </Button>
          <Button size="md" onClick={onMore}>
            {t("nw.a.more")}
          </Button>
          <Link to="/nodes/$id" params={{ id: peer.id }} search={{ tab: "ways", way: `out:${node.id}` }} className="wy-lk">
            {t("ways.open", { node: peer.name })} ›
          </Link>
        </div>
      )}
      {w.owner && <RemovePair rs={lane.relays} entry={peer.name} exit={node.name} entryCc={ccOf(peer.id)} exitCc={node.countryCode} asking={removing} onAsk={setRemoving} onDone={() => setRemoving(false)} />}
    </WayCard>
  );
}

/** IPv4 where people are, for the IPv6 entry: cut while IPv6 gets through ("helps"), not cut ("ok"), or nothing to say. */
const v4Note = (c?: CountryCheck): "helps" | "ok" | undefined => (ipv6Helps(c) ? "helps" : c?.verdict === CountryVerdict.OK ? "ok" : undefined);

/** The IPv6 entry: its state, the last outside check, and for the owner the way to turn it on or off. */
function Ipv6Detail({ data, w, lane, onAdd }: { data: Data; w: WaysModel; lane: Lane; onAdd: () => void }) {
  const t = useT();
  const node = data.node!;
  const look = lookOf(lane, w);
  const [turningOff, setTurningOff] = useState(false);
  const offered = entryOffered(w.v6);
  const off = !w.v6.entry;
  return (
    <WayCard lane={lane} icon={laneGlyph.ipv6} tone="" title={t("ways.ipv6")} sub={t("ways.ipv6.sub")} pill={<Pill look={look} />}>
      {off && (
        <div className="wy-off">
          <b>{t("wy.v6.off")}</b>
          <span>{offered ? t("wy.v6.offSub") : entryReason(t, w.v6.unavailableReason)}</span>
          {w.owner && offered && (
            <Button variant="primary" size="lg" onClick={onAdd}>
              {t("ways.ipv6.add")}
            </Button>
          )}
        </div>
      )}
      <div className="px-6 pb-4 max-[720px]:px-4">
        <EntryState node={node} v6={w.v6} v4={v4Note(w.country)} country={countryIn(t, w.cc)} />
      </div>
      {w.country?.ipv6 && <Nets w={w} name={node.name} check={w.country.ipv6} six />}
      {w.owner && !off && (
        <>
          <button type="button" className="wy-rm" onClick={() => setTurningOff(true)}>
            {t("ways.ipv6.turnOff")}
          </button>
          <TurnOffDialog open={turningOff} onOpenChange={setTurningOff} node={node} />
        </>
      )}
    </WayCard>
  );
}

// ---- the Overview's strip ----

/** "Ways in  People → (◐ Direct · MTS) (● CDN) (● through msk1) (○ IPv6) → de1   All ways in ›" on the node's Overview. */
export function WaysStrip({ data, w }: { data: Data; w: WaysModel }) {
  const t = useT();
  const node = data.node!;
  const nowS = Math.floor(useNow(30_000) / 1000);
  if (!w.ready) return null;
  // The note after a lane's name. A way seen on part of the networks names the dark ones; past two they become "+N" and the
  // full list goes to the tooltip, so a long list cannot push the chip out of the card.
  const short = (l: Lane): { text: string; full?: string } => {
    const look = lookOf(l, w);
    if (look.kind === "ok") return { text: "" };
    if (l.state === "ok" && l.seen === "part" && l.dark.length) {
      const full = l.dark.join(", ");
      if (l.dark.length <= 2) return { text: full, full };
      return { text: t("ways.darkMore", { first: l.dark.slice(0, 2).join(", "), n: l.dark.length - 2 }), full };
    }
    return { text: t(look.word) };
  };
  const arrow = (
    <i
      aria-hidden
      className="relative h-px w-[18px] flex-none bg-[color-mix(in_srgb,var(--text)_22%,transparent)] after:absolute after:-top-[3px] after:inset-e-0 after:size-1.5 after:rotate-45 after:border-t after:border-e after:border-[color-mix(in_srgb,var(--text)_30%,transparent)] rtl:-scale-x-100"
    />
  );
  const pass = w.out.length > 0;
  const load = nodeLoad(data.metrics, node.bandwidthMbps, nowS);
  return (
    <div className="mb-4 flex flex-col gap-2">
      <section aria-label={t("node.tab.ways")} className="no-scrollbar flex min-h-12 items-center gap-3 overflow-x-auto rounded-[14px] border border-line bg-surface px-4 py-2">
        <span className="w-[92px] flex-none text-xs text-muted max-[720px]:w-auto">{t("np.ways")}</span>
        <span className="inline-flex flex-none items-center gap-2 text-[13px] whitespace-nowrap">
          {w.cc && <Flag code={w.cc} size={14} className="rounded-[3px]" />}
          {t("ways.peopleShort")}
        </span>
        {arrow}
        <div className="flex flex-none gap-1.5">
          {w.lanes.map((l) => {
            const note = short(l);
            const title = l.kind === "direct" ? t("ways.direct") : l.kind === "cdn" ? t("ways.cdn") : l.kind === "via" ? t("ways.via", { entry: l.peer!.name }) : t("ways.ipv6");
            return (
              <Link
                key={l.id}
                to="/nodes/$id"
                params={{ id: node.id }}
                search={{ tab: "ways", way: l.id }}
                title={note.full ? `${title} · ${t("ways.darkTitle", { list: note.full })}` : undefined}
                className="inline-flex h-7 max-w-full min-w-0 items-center gap-1.5 rounded-[14px] border border-line px-2.5 text-[13px] whitespace-nowrap transition-[border-color,background-color] duration-150 ease-out hover:border-[color-mix(in_srgb,var(--text)_26%,transparent)] hover:bg-step"
              >
                <RingMark ring={l.ring} />
                <span className="shrink-0">{title}</span>
                {note.text && <span className={cx("min-w-0 truncate", toneText[lookOf(l, w).kind])}>· {note.text}</span>}
              </Link>
            );
          })}
        </div>
        {arrow}
        <span className="inline-flex flex-none items-center gap-2 text-[13px] whitespace-nowrap">
          <Flag code={node.countryCode} size={14} className="rounded-[3px]" />
          <b className="font-mono font-medium">{node.name}</b>
        </span>
        <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "ways" }} className="ms-auto flex flex-none items-center gap-1 ps-2 text-[13px] text-sec hover:text-fg">
          {t("ways.back")}
          <Icon name="chevronRight" size={13} className="rtl:rotate-180" />
        </Link>
      </section>
      {pass && (
        <section aria-label={t("wy.strip.pass")} className="no-scrollbar flex min-h-12 items-center gap-3 overflow-x-auto rounded-[14px] border border-line bg-surface px-4 py-2">
          <span className="w-[92px] flex-none text-xs text-muted max-[720px]:w-auto">{t("wy.strip.pass")}</span>
          <span className="inline-flex flex-none items-center gap-2 text-[13px] whitespace-nowrap">
            <Flag code={node.countryCode} size={14} className="rounded-[3px]" />
            <b className="font-mono font-medium">{node.name}</b>
          </span>
          {arrow}
          <div className="flex flex-none gap-1.5">
            {w.out.map((l) => {
              const bridged = l.relays.some((r) => r.enabled && activeOf(r) === "reverse");
              return (
                <Link
                  key={l.id}
                  to="/nodes/$id"
                  params={{ id: node.id }}
                  search={{ tab: "ways", way: l.id }}
                  className="inline-flex h-7 max-w-full min-w-0 items-center gap-1.5 rounded-[14px] border border-line px-2.5 text-[13px] whitespace-nowrap transition-[border-color,background-color] duration-150 ease-out hover:border-[color-mix(in_srgb,var(--text)_26%,transparent)] hover:bg-step"
                >
                  <RingMark ring={l.ring} />
                  <span className="shrink-0 font-mono">{l.peer!.name}</span>
                  {bridged && <span className="text-muted">· {t("nw.c.bridge")}</span>}
                </Link>
              );
            })}
          </div>
          {load && <span className="ms-auto flex-none ps-2 text-xs text-muted">{t("wy.strip.load", { pct: load.percent })}</span>}
        </section>
      )}
    </div>
  );
}
