import { useQueries, useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { ActionBar, Block, SectionLabel } from "@/components/ui/bits";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon, IconChip, type IconName, type Tone } from "@/components/ui/icons";
import { BackButton, DetailCard, useTurns, walkTabs } from "@/components/ui/master-detail";
import { Modal } from "@/components/ui/modal";
import { EmptyState, Notice } from "@/components/ui/notice";
import { Pending } from "@/components/ui/query-error";
import { StatusDot, StatusPill, type StatusKind } from "@/components/ui/status";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { countryQuery } from "@/lib/globalping";
import { checksQuery, countryColumn, countryIn, doctorQuery, type CountryCheck } from "@/lib/health";
import { nodeLoad } from "@/lib/node-load";
import type { Plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import { candidatesQuery, countryHint, isCut, reachHint, relaysQuery, seenHint, type CountryHint, type Hint } from "@/lib/relay";
import { useNow } from "@/lib/time";
import { ipv6Helps } from "@/lib/reachability";
import { useIsOwner } from "@/lib/updates";
import { entryOf, entryOffered, entryState, hintOf, ipv6FactOf, lanesOf, type Ipv6Entry, type Ipv6Fact, type Lane, type Ring, type WayId, type Ways } from "@/lib/ways";
import { CountryRow } from "@/screens/health/country";
import { EntryDialog, entryReason, EntryState, TurnOffDialog } from "./ipv6-entry";
import { CheckPortsButton, portRows, Ports, usePortCheck } from "./ports";
import { AddRelayDialog, ccChip, RelayRows, useAddRelay, useCountryOf, useRelayRuns, type Entry } from "./relays";
import type { NodeSearch } from "./tabs";

// The node page's "Ways in" tab (node-ia plan §3.4): every way people reach the node, drawn as one line from people down
// to the node with a branch per way (straight to its address, through another node, over IPv6), and on an entry node the
// nodes reached through it. A list and the open way, like Reachability's sources; `?way=` is the open one, `?add=` the
// "New way in" dialog ("ipv6": the IPv6 entry's). The verdicts are lib/ways.ts's, the relay rows and dialog relays.tsx's,
// the IPv6 entry's card and dialog ipv6-entry.tsx's.

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
  return { ...ways, owner, ready: !owner || !live || relays.data !== undefined || relays.isError, cc: column?.code || asked || "RU", country, countryOf, ipv6, v6 };
}

// ---- words of a lane ----

type Look = { kind: StatusKind; word: MessageKey };

/** A lane's state in a word: what is ours to fix first, then how much of the country sees it. An IPv6 entry the node cannot take is "not available". */
function lookOf(l: Lane, w: { offline: boolean; v6: Ipv6Entry }): Look {
  if (w.offline && l.kind === "direct") return { kind: "off", word: "ways.pill.offline" };
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
  if (l.kind === "direct" && w.offline) return { tone: "off", text: t("ways.offline") };
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

// ---- the line ----

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

// the geometry of the line, in the list's own coordinates: the trunk runs under the middle of the country chips (12 px in,
// 30 wide), a branch leaves it for the ring, the words start after the ring
const trunk = "pointer-events-none absolute left-[26px] z-[1] w-0.5 bg-faint";
const branch = "absolute top-[19px] left-[28px] w-3 border-t-2";
const branchOf: Record<Ring, string> = {
  ok: "border-faint group-hover:border-accent",
  half: "border-faint group-hover:border-accent",
  warn: "border-faint group-hover:border-accent",
  busy: "border-faint group-hover:border-accent",
  bad: "border-dashed border-(color:--danger)",
  off: "border-dotted border-faint group-hover:border-accent",
};
const rowCls =
  "group relative grid w-full min-w-0 scroll-mt-20 gap-0.5 rounded-card py-2.5 pr-3 pl-[58px] text-left transition-colors duration-200 hover:bg-surface-2 focus-visible:outline-2 focus-visible:outline-accent";

function LaneRow({ l, w, data, selected, tabbable, onPick }: { l: Lane; w: WaysModel; data: Data; selected: boolean; tabbable: boolean; onPick: () => void }) {
  const t = useT();
  const ccOf = useCountryOf();
  const look = lookOf(l, w);
  const seen = seenLine(t, l, w);
  const title = l.kind === "direct" ? t("ways.direct") : l.kind === "via" ? t("ways.via", { entry: l.peer!.name }) : l.kind === "ipv6" ? t("ways.ipv6") : l.peer!.name;
  const where =
    l.kind === "direct"
      ? data.inbounds.length === 0
        ? t("ways.noProfiles")
        : portsOf(data) > 0
          ? `${data.node!.address} · ${t.n("ways.ports", portsOf(data))}`
          : data.node!.address
      : l.kind === "ipv6"
        ? w.v6.entry || t(w.v6.entrySupported ? "ways.ipv6.lane.off" : "ways.ipv6.lane.na")
        : t.n("ways.servers", l.relays.length);
  const mono = (l.kind === "direct" && data.inbounds.length > 0) || (l.kind === "ipv6" && !!w.v6.entry);
  // a lane broken on our side says so in words next to the ring's colour (how much of the country sees it is the line
  // below); the rest is said to a screen reader
  const said = l.state === "bad" || l.state === "warn" || l.state === "busy" ? t(look.word) : "";
  return (
    <button
      type="button"
      role="tab"
      id={`way-tab-${l.id}`}
      aria-selected={selected}
      aria-controls="way-panel"
      tabIndex={tabbable ? 0 : -1}
      onClick={onPick}
      data-ring={l.ring}
      className={cx(rowCls, "md:aria-selected:bg-surface-2 md:aria-selected:shadow-[inset_0_0_0_1px_var(--accent-line)]")}
    >
      <span aria-hidden className={cx(branch, branchOf[l.ring])} />
      <RingMark ring={l.ring} className={cx("absolute top-[15px] left-[40px]", selected && "md:outline-2 md:outline-offset-2 md:outline-accent")} />
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-[13px] leading-5 font-bold">{title}</span>
        {l.peer && ccChip(ccOf(l.peer.id))}
        <span className="sr-only">{t(look.word)}</span>
        <Icon name="chevronRight" size={14} aria-hidden className="ml-auto flex-none text-muted md:hidden" />
      </span>
      <span className="truncate text-xs text-muted">
        <span className={mono ? "font-mono text-[11px]" : undefined}>{where}</span>
        {said && <span className={cx("font-semibold", toneText[look.kind])}> · {said}</span>}
      </span>
      {seen && <span className={cx("text-xs leading-snug text-pretty", seen.tone === "warn" ? "text-warn-text" : seen.tone === "bad" ? "text-danger-text" : "text-muted")}>{seen.text}</span>}
    </button>
  );
}

/** A dashed branch that adds a lane: "Add a way in…", amber with why when the node is dark on some networks. */
function AddRow({ label, note, amber, onClick }: { label: string; note?: string; amber?: boolean; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className={cx(rowCls, "text-muted hover:text-fg", amber && "tone-warn")}>
      <span aria-hidden className={cx(branch, "border-dashed", amber ? "border-(color:--c)" : "border-faint group-hover:border-accent")} />
      <span
        aria-hidden
        className={cx(
          "absolute top-[13px] left-[38px] flex size-3.5 items-center justify-center rounded-full border border-dashed",
          amber ? "border-(color:--c) tone-text" : "border-faint group-hover:border-accent",
        )}
      >
        <Icon name="plus" size={10} />
      </span>
      <span className={cx("text-[13px] leading-5 font-bold", amber && "tone-text")}>{label}</span>
      {note && <span className={cx("text-xs leading-snug text-pretty", amber ? "tone-text" : "text-muted")}>{note}</span>}
    </button>
  );
}

/** People on top, a branch per way, the node at the bottom; on an entry, the nodes reached through it below. */
function Line({ data, w, open, onPick, onAdd, onRelayOther, className }: { data: Data; w: WaysModel; open: WayId; onPick: (id: WayId) => void; onAdd: () => void; onRelayOther: () => void; className?: string }) {
  const t = useT();
  const fmt = useFmt();
  const node = data.node!;
  const nowS = Math.floor(useNow(30_000) / 1000);
  const own = w.lanes[0]!;
  const dark = own.seen === "part" || own.seen === "none";
  const others = (useQuery({ ...nodesQuery, enabled: w.owner }).data?.nodes ?? []).some((n) => n.id !== node.id && n.status !== NodeStatus.RETIRED);
  // the owner adds a way in when there is another node to go through, or an IPv6 entry to turn on
  const canAdd = w.owner && (others || entryOffered(w.v6));
  const load = nodeLoad(data.metrics, node.bandwidthMbps, nowS);
  const row = (l: Lane) => <LaneRow key={l.id} l={l} w={w} data={data} selected={l.id === open} tabbable={l.id === open} onPick={() => onPick(l.id)} />;
  return (
    <div
      role="tablist"
      aria-label={t("ways.list", { node: node.name })}
      aria-orientation="vertical"
      onKeyDown={walkTabs}
      className={cx("flex flex-col self-start rounded-card-lg border border-line bg-surface p-1.5", className)}
    >
      <div className="flex items-center gap-2.5 px-3 pt-2.5 pb-1.5">
        {ccChip(w.cc)}
        <span className="text-[13px] font-bold">{t("ways.people", { country: fmt.country(w.cc) })}</span>
      </div>
      <div className="relative flex flex-col">
        <span aria-hidden className={cx(trunk, "ways-draw top-0 bottom-0")} />
        {w.lanes.map(row)}
        {canAdd && (
          <AddRow
            label={t("ways.add")}
            amber={dark}
            note={
              dark
                ? own.seen === "none" || own.dark.length === 0
                  ? t("ways.add.darkAll", { country: countryIn(t, w.cc) })
                  : t("ways.add.dark", { networks: own.dark.join(", ") })
                : !w.lanes.some((l) => l.kind === "via")
                  ? t("ways.add.hint")
                  : undefined
            }
            onClick={onAdd}
          />
        )}
      </div>
      <div className="flex items-center gap-2.5 px-3 pt-1.5 pb-2.5">
        {ccChip(node.countryCode)}
        <span className="text-sm font-extrabold tracking-[-0.02em]">{node.name}</span>
      </div>
      {w.out.length > 0 && (
        <>
          <SectionLabel as="h4" className="mx-1.5 border-t border-line px-1.5 pt-3 pb-1">
            {t("ways.outTitle", { node: node.name })}
          </SectionLabel>
          <div className="relative flex flex-col">
            <span aria-hidden className={cx(trunk, "ways-draw top-0 bottom-5")} />
            {w.out.map(row)}
            <AddRow label={t("ways.relayOther")} onClick={onRelayOther} />
          </div>
          <p className="px-3 pt-1 pb-2.5 text-[11px] leading-snug text-pretty text-muted">{load ? t("relay.footerLoad", { load: `${load.percent}%` }) : t("relay.footer")}</p>
        </>
      )}
    </div>
  );
}

// ---- the tab ----

export function WaysTab({ data, ways: w }: { data: Data; ways: WaysModel }) {
  const t = useT();
  const navigate = useNavigate();
  const node = data.node!;
  const search = useSearch({ strict: false }) as NodeSearch;
  const all = [...w.lanes, ...w.out];
  const chosen = all.some((l) => l.id === search.way) ? search.way : undefined;
  const open = chosen ?? w.first;
  const go = (next: NodeSearch, replace = true) => void navigate({ to: "/nodes/$id", params: { id: node.id }, search: { tab: "ways", ...next }, replace, resetScroll: false });
  // a phone takes turns: opening moves the focus to the way back, going back (the link or the browser's) to the lane left
  const { pick, back } = useTurns({ chosen, backId: "way-back", rowId: (id) => `way-tab-${id}`, open: (id, replace) => go({ way: id }, replace), close: () => go({}) });
  const [relayOther, setRelayOther] = useState(false);

  if (!w.ready) return <Pending />;
  const lane = all.find((l) => l.id === open) ?? w.lanes[0]!;
  return (
    // side by side from lg; between 720 and 1023 px (the sidebar still shown) the line stands above the open lane, and a
    // phone takes turns
    <div className="grid gap-3.5 lg:grid-cols-[336px_minmax(0,1fr)]">
      <Line
        data={data}
        w={w}
        open={open}
        onPick={pick}
        onAdd={() => go({ way: search.way, add: "1" })}
        onRelayOther={() => setRelayOther(true)}
        className={cx("lg:sticky lg:top-[72px]", chosen && "max-md:hidden")}
      />
      <div className={cx("flex min-w-0 flex-col gap-2.5", !chosen && "max-md:hidden")}>
        <BackButton id="way-back" label={t("ways.back")} onClick={back} />
        <div role="tabpanel" id="way-panel" aria-labelledby={`way-tab-${lane.id}`} className="flex-1">
          {lane.kind === "direct" ? (
            <DirectDetail data={data} w={w} lane={lane} />
          ) : lane.kind === "ipv6" ? (
            <Ipv6Detail data={data} w={w} lane={lane} onAdd={() => go({ way: "ipv6", add: "ipv6" })} />
          ) : (
            <RelayDetail key={lane.id} data={data} w={w} lane={lane} />
          )}
        </div>
      </div>
      {w.owner && (
        <NewWayDialog
          data={data}
          w={w}
          start={search.add}
          onClose={(way) => go({ way: way ?? search.way })}
          onIpv6={() => go({ way: "ipv6", add: "ipv6" })}
        />
      )}
      {w.owner && (
        <EntryDialog
          open={search.add === "ipv6" && entryOffered(w.v6)}
          onClose={() => go({ way: "ipv6" })}
          node={node}
          v6={w.v6}
          awg={data.inbounds.some((i) => i.protocol === "awg")}
        />
      )}
      {w.owner && <AddRelayDialog open={relayOther} onOpenChange={setRelayOther} entry={{ id: node.id, name: node.name, cc: node.countryCode }} />}
    </div>
  );
}

// ---- the open lane ----

const linkCls = "ml-auto flex items-center gap-1 text-[13px] font-bold text-accent-text hover:underline";

function DirectDetail({ data, w, lane }: { data: Data; w: WaysModel; lane: Lane }) {
  const t = useT();
  const node = data.node!;
  const look = lookOf(lane, w);
  const run = usePortCheck(node.id);
  const none = data.inbounds.length === 0;
  return (
    <DetailCard icon="globe" tone="mint" title={t("ways.direct")} sub={t("ways.direct.sub", { address: node.address })} control={<StatusPill sm kind={look.kind} label={t(look.word)} />}>
      {none && (
        <Block flush>
          <EmptyState
            title={t("ways.noProfilesBody")}
            action={
              <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "profiles" }} className={buttonClass("primary", "md")}>
                <Icon name="plus" size={14} />
                {t("node.profiles.add")}
              </Link>
            }
          />
        </Block>
      )}
      {(!none || (data.portChecks?.length ?? 0) + (data.tcpChecks?.length ?? 0) > 0) && (
        <Block flush={!none} title={t("ports.title")}>
          <Ports data={data} run={run} />
        </Block>
      )}
      <SeenBlock w={w} name={node.name} check={w.country} />
      <ActionBar>
        <CheckPortsButton data={data} run={run} />
        <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "profiles" }} className={linkCls}>
          {t("node.tab.profiles")}
          <Icon name="chevronRight" size={14} />
        </Link>
      </ActionBar>
    </DetailCard>
  );
}

/** "Is msk1 seen from Russia": the country check's row, with its breakdown on a partial block. */
function SeenBlock({ w, name, check, six }: { w: WaysModel; name: string; check?: CountryCheck; six?: boolean }) {
  const t = useT();
  return (
    <Block title={t(six ? "ways.block.seen6" : "ways.block.seen", { node: name, country: countryIn(t, w.cc) })}>
      {check ? (
        <CountryRow check={check} code={w.cc} />
      ) : (
        <p className="text-[13px] text-muted">
          {t("ways.noCountry")}{" "}
          {w.owner && (
            <Link to="/reachability" className="font-bold text-accent-text underline decoration-dotted underline-offset-2">
              {t("ways.sources")}
            </Link>
          )}
        </p>
      )}
    </Block>
  );
}

/** Through an entry (on the exit), or to an exit (on the entry): the relays, their checks, more servers, the other node. */
function RelayDetail({ data, w, lane }: { data: Data; w: WaysModel; lane: Lane }) {
  const t = useT();
  const ccOf = useCountryOf();
  const runs = useRelayRuns();
  const [adding, setAdding] = useState(false);
  const node = data.node!;
  const peer = lane.peer!;
  const look = lookOf(lane, w);
  const via = lane.kind === "via";
  // the entry of these relays and the exit they reach
  const entry: Entry = via ? { id: peer.id, name: peer.name, cc: ccOf(peer.id) } : { id: node.id, name: node.name, cc: node.countryCode };
  const exit = via ? node : { id: peer.id, name: peer.name };
  const forwardDrop = lane.relays.some((r) => r.enabled && r.lastError === "forward_drop");
  return (
    <DetailCard
      icon="share"
      tone="sky"
      title={via ? t("ways.via", { entry: peer.name }) : peer.name}
      extra={ccChip(ccOf(peer.id))}
      sub={via ? t("ways.via.sub", { entry: peer.name, node: node.name }) : t("ways.out.sub", { exit: peer.name })}
      control={<StatusPill sm kind={look.kind} label={t(look.word)} />}
    >
      {forwardDrop && (
        <Block flush>
          <Notice tone="danger">{t("relay.err.forward_drop")}</Notice>
        </Block>
      )}
      <Block flush={!forwardDrop} title={t("ways.block.servers")}>
        <RelayRows list={lane.relays} side={via ? "exit" : "entry"} runs={runs} />
      </Block>
      {via && <SeenBlock w={w} name={peer.name} check={w.countryOf(peer.id)} />}
      <ActionBar>
        <Button variant="secondary" size="md" disabled={runs.checking} onClick={() => void runs.checkAll(lane.relays)}>
          {t("ways.checkAll")}
        </Button>
        <Button variant="secondary" size="md" onClick={() => setAdding(true)}>
          <Icon name="plus" size={14} />
          {t("ways.addServer")}
        </Button>
        <Link to="/nodes/$id" params={{ id: peer.id }} search={{ tab: "ways", way: via ? `out:${node.id}` : `via:${node.id}` }} className={linkCls}>
          {t("ways.open", { node: peer.name })}
          <Icon name="chevronRight" size={14} />
        </Link>
      </ActionBar>
      <AddRelayDialog open={adding} onOpenChange={setAdding} entry={entry} exitId={exit.id} />
    </DetailCard>
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
  return (
    <DetailCard icon="network" tone="lavender" title={t("ways.ipv6")} sub={t("ways.ipv6.sub")} control={<StatusPill sm kind={look.kind} label={t(look.word)} />}>
      <Block flush title={t("ways.block.state")}>
        <EntryState node={node} v6={w.v6} v4={v4Note(w.country)} country={countryIn(t, w.cc)} />
      </Block>
      {w.country?.ipv6 && <SeenBlock w={w} name={node.name} check={w.country.ipv6} six />}
      {w.owner && (offered || w.v6.entry) && (
        <ActionBar>
          {offered ? (
            <Button variant="primary" size="md" onClick={onAdd}>
              <Icon name="plus" size={14} />
              {t("ways.ipv6.add")}
            </Button>
          ) : (
            <Button variant="secondary" size="md" onClick={() => setTurningOff(true)}>
              {t("ways.ipv6.turnOff")}
            </Button>
          )}
        </ActionBar>
      )}
      {w.owner && <TurnOffDialog open={turningOff} onOpenChange={setTurningOff} node={node} />}
    </DetailCard>
  );
}

// ---- the dialog ----

type Pick = { node: { id: string; name: string; countryCode: string }; hint?: CountryHint; fit: boolean; already: number };

/**
 * "New way in" (§3.4): what kind of way, then through which node (the ones people see from the networks where this node is
 * dark first), then the relay dialog for this node's servers. `start` is ?add=: "1", or "via:<entry>" straight at step 3. The
 * IPv6 entry is the other kind: picked, it hands over to the entry's own dialog (`onIpv6`).
 */
function NewWayDialog({ data, w, start, onClose, onIpv6 }: { data: Data; w: WaysModel; start?: string; onClose: (way?: WayId) => void; onIpv6: () => void }) {
  const t = useT();
  const node = data.node!;
  const open = start === "1" || !!start?.startsWith("via:");
  const ccOf = useCountryOf();
  const nodes = useQuery({ ...nodesQuery, enabled: open }).data?.nodes ?? [];
  const fleet = countryColumn(useQuery({ ...checksQuery(""), enabled: open }).data?.countryChecks);
  const relays = useQuery({ ...relaysQuery(node.id), enabled: open }).data ?? [];
  const startEntry = start?.startsWith("via:") ? start.slice(4) : "";
  const [picked, setPicked] = useState<string | null>(null);
  const [step, setStep] = useState<"kind" | "pick" | null>(null);
  const entryId = picked ?? startEntry;
  const at = step ?? (entryId ? "relay" : "kind");
  const entryNode = nodes.find((n) => n.id === entryId);
  const entry: Entry = { id: entryId, name: entryNode?.name ?? "", cc: entryNode?.countryCode ?? ccOf(entryId) };

  const own = hintOf(w.country);
  const cut = isCut(own) && own!.dark.length > 0;
  const darkText = own?.dark.join(", ") ?? "";
  const picks: Pick[] = nodes
    .filter((n) => n.id !== node.id && n.status !== NodeStatus.RETIRED)
    .map((n) => {
      const hint = hintOf(fleet?.byNode.get(n.id));
      const fit = cut && !!hint && (hint.verdict === "ok" || hint.verdict === "partial") && !own!.dark.some((d) => hint.dark.includes(d));
      return { node: n, hint, fit, already: relays.filter((r) => r.exitNodeId === node.id && r.viaNodeId === n.id).length };
    })
    // good fits first; among them a node inside the people's country (their route to it stays at home), then one every network sees
    .sort(
      (a, b) =>
        Number(b.fit) - Number(a.fit) ||
        Number(b.node.countryCode === w.cc) - Number(a.node.countryCode === w.cc) ||
        Number(b.hint?.verdict === "ok") - Number(a.hint?.verdict === "ok") ||
        a.node.name.localeCompare(b.node.name),
    );
  const fits = picks.filter((p) => p.fit);
  // does the entry reach this node: the TSPU probe's row, asked for the good fits only (it builds every server of the fleet)
  const reach = useQueries({ queries: fits.map((p) => ({ ...candidatesQuery(p.node.id), enabled: open && at === "pick" })) });
  const reachOf = (id: string) => reach[fits.findIndex((p) => p.node.id === id)]?.data?.candidates.find((c) => c.nodeId === node.id)?.reach;

  const finish = (way?: WayId) => {
    setPicked(null);
    setStep(null);
    onClose(way);
  };
  // the relay step closes through its own close (it waits for a run to end and resets), then opens the lane it made
  const add = useAddRelay({
    entry,
    exitId: node.id,
    enabled: open && at === "relay" && !!entryId,
    onClose: () => finish(`via:${entryId}`),
    onBack: () => {
      setPicked(null);
      setStep("pick");
    },
  });
  const close = () => (at === "relay" ? add.close() : finish());

  const intro = !cut
    ? own?.verdict === "blocked"
      ? t("ways.new.introAll", { country: countryIn(t, w.cc), node: node.name })
      : t("ways.new.introOk", { node: node.name })
    : t("ways.new.intro", { country: countryIn(t, w.cc), node: node.name, networks: darkText });
  const best = fits[0];
  const offered = entryOffered(w.v6);

  // a way that can be taken is a button; one that cannot (IPv6 already on, or the node cannot take it) is plain text that
  // says why, with its state on the right: no hover, no focus
  const option = (icon: IconName, tone: Tone, title: string, what: string, note: ReactNode, onPick?: () => void, pill?: Look) => {
    const inner = (
      <>
        <IconChip icon={icon} tone={tone} size={30} className={cx("mt-0.5 rounded-[9px]", !onPick && "opacity-50 grayscale")} />
        <span className="flex min-w-0 flex-1 flex-col gap-1">
          <span className={cx("text-sm font-bold", !onPick && "text-muted")}>{title}</span>
          <span className="text-xs leading-snug text-pretty text-muted">{what}</span>
          {note}
        </span>
        {onPick ? <Icon name="chevronRight" size={16} className="mt-1 flex-none text-muted group-hover:text-fg" /> : pill && <StatusPill sm kind={pill.kind} label={t(pill.word)} />}
      </>
    );
    const cls = "flex w-full min-w-0 items-start gap-3 rounded-card border px-3.5 py-3 text-left";
    return onPick ? (
      <button type="button" onClick={onPick} className={cx(cls, "group border-line transition-colors duration-200 hover:border-accent-line hover:bg-surface-2")}>
        {inner}
      </button>
    ) : (
      <div aria-disabled="true" data-testid="way-option-off" className={cx(cls, "border-dashed border-line")}>
        {inner}
      </div>
    );
  };

  const hintLine = (h: Hint) => (
    <li key={h.text} className="flex items-start gap-1.5 text-xs leading-snug">
      <span className="mt-[4px] flex">
        <StatusDot kind={h.tone} />
      </span>
      <span className={h.tone === "bad" ? "text-danger-text" : "text-fg"}>{h.text}</span>
    </li>
  );

  const pickCard = (p: Pick) => {
    const seen = (cut ? seenHint(t, p.node.name, own, p.hint) : null) ?? countryHint(t, w.cc, p.hint);
    const r = p.fit ? reachOf(p.node.id) : undefined;
    const hints = [seen, r ? reachHint(t, p.node.name, node.name, r) : null].filter((h): h is Hint => !!h);
    return (
      <button
        key={p.node.id}
        type="button"
        data-testid="way-entry"
        onClick={() => {
          setPicked(p.node.id);
          setStep(null);
        }}
        className="group flex w-full min-w-0 items-start gap-3 rounded-card border border-line px-3.5 py-2.5 text-left transition-colors duration-200 hover:border-accent-line hover:bg-surface-2"
      >
        {ccChip(p.node.countryCode)}
        <span className="flex min-w-0 flex-1 flex-col gap-1">
          <span className="text-[13px] font-extrabold">{p.node.name}</span>
          <ul className="flex flex-col gap-0.5">{hints.map(hintLine)}</ul>
          {p.already > 0 && <span className="text-[11px] text-muted">{t.n("ways.new.already", p.already, { node: node.name })}</span>}
        </span>
        <Icon name="chevronRight" size={16} className="mt-0.5 flex-none text-muted group-hover:text-fg" />
      </button>
    );
  };

  const body =
    at === "kind" ? (
      <div className="flex flex-col gap-2">
        {option(
          "share",
          "sky",
          t("ways.new.via"),
          t("ways.new.viaWhat", { node: node.name }),
          picks.length === 0 ? (
            <span className="text-xs text-muted">{t("ways.new.noNodes")}</span>
          ) : best && cut ? (
            <span className="tone-ok tone-text flex items-center gap-1.5 text-xs font-semibold">
              <Icon name="check" size={13} className="flex-none" />
              {t("ways.new.best", { entry: best.node.name, networks: darkText })}
            </span>
          ) : null,
          picks.length > 0 ? () => setStep("pick") : undefined,
        )}
        {option(
          "network",
          "lavender",
          t("ways.ipv6"),
          t("ways.new.ipv6What", { node: node.name }),
          offered ? (
            ipv6Helps(w.country) && (
              <span className="tone-ok tone-text flex items-center gap-1.5 text-xs font-semibold">
                <Icon name="check" size={13} className="flex-none" />
                {t("ways.ipv6.helps", { country: countryIn(t, w.cc) })}
              </span>
            )
          ) : (
            <span className="text-xs leading-snug text-pretty text-muted [overflow-wrap:anywhere]">
              {w.v6.entry ? t("ways.new.ipv6On", { addr: w.v6.entry }) : entryReason(t, w.v6.unavailableReason)}
            </span>
          ),
          offered
            ? () => {
                setPicked(null);
                setStep(null);
                onIpv6();
              }
            : undefined,
          w.v6.entry ? { kind: "ok", word: "ways.pill.on" } : { kind: "off", word: "ways.pill.na" },
        )}
      </div>
    ) : at === "pick" ? (
      <div className="flex flex-col gap-3">
        {fits.length > 0 && (
          <section className="flex flex-col gap-2">
            <SectionLabel as="h3">{t("ways.new.fits", { networks: darkText })}</SectionLabel>
            {fits.map(pickCard)}
          </section>
        )}
        {picks.length > fits.length && (
          <section className="flex flex-col gap-2">
            {fits.length > 0 && <SectionLabel as="h3">{t("ways.new.others")}</SectionLabel>}
            {picks.filter((p) => !p.fit).map(pickCard)}
          </section>
        )}
      </div>
    ) : (
      add.body
    );

  return (
    <Modal
      open={open}
      onOpenChange={(o) => !o && close()}
      wide
      closeLabel={t("common.close")}
      title={at === "relay" && entry.name ? `${t("ways.new.title", { node: node.name })} · ${t("ways.new.through", { entry: entry.name })}` : t("ways.new.title", { node: node.name })}
      description={at === "kind" ? intro : at === "pick" ? t("ways.new.pick") : t("relay.dlg.intro", { entry: entry.name })}
      footer={
        at === "relay" ? (
          add.footer
        ) : at === "pick" ? (
          <Button variant="ghost" size="md" onClick={() => setStep("kind")}>
            {t("ways.new.back")}
          </Button>
        ) : (
          <Button variant="ghost" size="md" onClick={() => close()}>
            {t("common.cancel")}
          </Button>
        )
      }
    >
      {body}
    </Modal>
  );
}

// ---- the Overview's strip ----

/** "Ways in  People → (◐ Direct · MTS) (● through msk1) (○ IPv6) → de1   All ways in ›" on the node's Overview. */
export function WaysStrip({ data, w }: { data: Data; w: WaysModel }) {
  const t = useT();
  const node = data.node!;
  if (!w.ready) return null;
  const short = (l: Lane): string => {
    const look = lookOf(l, w);
    if (look.kind === "ok") return "";
    if (l.state === "ok" && l.seen === "part" && l.dark.length) return l.dark.join(", ");
    return t(look.word);
  };
  return (
    <section aria-label={t("node.tab.ways")} className="flex flex-wrap items-center gap-x-2 gap-y-2 rounded-card border border-line bg-surface px-4 py-3">
      <SectionLabel as="h2" icon="share" tone="sky" className="mr-1">
        {t("node.tab.ways")}
      </SectionLabel>
      <span className="text-xs font-semibold text-muted">{t("ways.peopleShort")}</span>
      <Icon name="chevronRight" size={14} className="text-faint" aria-hidden />
      {w.lanes.map((l) => {
        const note = short(l);
        const title = l.kind === "direct" ? t("ways.direct") : l.kind === "via" ? t("ways.via", { entry: l.peer!.name }) : t("ways.ipv6");
        return (
          <Link
            key={l.id}
            to="/nodes/$id"
            params={{ id: node.id }}
            search={{ tab: "ways", way: l.id }}
            className="inline-flex h-[26px] items-center gap-1.5 rounded-xl border border-line px-2.5 text-xs font-bold whitespace-nowrap transition-colors hover:border-accent-line hover:bg-surface-2"
          >
            <RingMark ring={l.ring} />
            {title}
            {note && <span className={cx("font-semibold", toneText[lookOf(l, w).kind])}>· {note}</span>}
          </Link>
        );
      })}
      <Icon name="chevronRight" size={14} className="text-faint" aria-hidden />
      <span className="font-mono text-xs font-bold">{node.name}</span>
      <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "ways" }} className="ml-auto flex items-center gap-1 text-xs font-bold text-accent-text hover:underline">
        {t("ways.back")}
        <Icon name="chevronRight" size={13} />
      </Link>
    </section>
  );
}
