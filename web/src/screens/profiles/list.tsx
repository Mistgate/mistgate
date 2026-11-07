import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState, type CSSProperties, type ReactNode } from "react";
import type { App, StatusReason } from "@/gen/mistgate/admin/v1/common_pb";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import { Card, Chip, PageTitle } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon, IconChip, type IconName } from "@/components/ui/icons";
import { EmptyState } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { Tip } from "@/components/ui/tooltip";
import { cx } from "@/lib/cx";
import { useMove } from "@/lib/paging";
import { AppLink, useGo } from "@/screens/users/nav";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { useTx, type Tx } from "@/screens/users/t";
import { dropsOf, dropWords, keysOnly, nameOf } from "./formats";
import { byProtocol, lookOf, summaryParts, telegramOnly, viaWarp } from "./protocol-meta";
import { Fresh } from "@/components/fx/fresh";
import { useListEntrance } from "@/components/fx/page";
import { facetIds, validateProfilesSearch, type FacetId } from "./search";

const warnText = (w: StatusReason, t: Tx) => t.opt(`profiles.warn.${w.code}`, w.params) ?? w.code.replaceAll("_", " ");

// The table follows the width the list really has (a container, not the window). From 760px: icon | profile | settings |
// formats | nodes | people | chevron, every row one height (subgrid: the columns line up across the rows); what is left over
// is shared by the name and the settings, so nothing gapes in the middle. Below that a row
// stacks: the name, the settings, then the protocol and the counts.
const table = "@min-[760px]:grid @min-[760px]:grid-cols-[auto_minmax(160px,1.3fr)_minmax(max-content,1fr)_auto_auto_auto_auto]";
const subgrid = "@min-[760px]:col-span-full @min-[760px]:grid @min-[760px]:grid-cols-subgrid @min-[760px]:gap-x-4 @min-[760px]:px-5 @min-[1000px]:gap-x-6";

/**
 * /profiles: one row per profile, in the protocols' order. One toolbar: the protocol (with counts), a search by name and the
 * state (a server warning, on no node, in no group, leaving through WARP). Every filter is in the URL; each count is what that
 * choice would show with the other filters kept.
 */
export function ProfilesScreen() {
  const t = useTx();
  const go = useGo();
  const move = useMove();
  const navigate = useNavigate();
  const search = validateProfilesSearch(useSearch({ strict: false }) as Record<string, unknown>);
  const list = useQuery(profileListQuery);
  const protocols = useQuery(protocolsQuery);
  const groups = useQuery(groupsQuery);
  const entrance = useListEntrance(list.data !== undefined && groups.data !== undefined && protocols.data !== undefined);
  // the box is the reader's own; the URL mirrors it (replace: no history entry per letter)
  const [q, setQ] = useState(search.q ?? "");
  const protocol = search.protocol ?? "";
  const state = search.state;

  const header = (
    <div className="flex items-center gap-3">
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <PageTitle>{t("profiles.title")}</PageTitle>
        <span className="text-xs text-muted">{t("profiles.sub")}</span>
      </div>
      {/* looking at one protocol, a new profile is most likely one more of it: its form opens at once (with "Change") */}
      <Button variant="primary" onClick={() => navigate({ to: "/profiles/new", search: protocol ? { protocol } : {} } as never)}>
        <Icon name="plus" size={14} />
        {t("profiles.create")}
      </Button>
    </div>
  );
  const page = (body: ReactNode) => (
    <div className="@container flex flex-col gap-3.5">
      {header}
      {body}
    </div>
  );

  // the groups and the protocol names decide the filters and the rows: the screen waits for them, so nothing jumps in later
  if (list.isPending || groups.isPending || protocols.isPending) return page(<Pending />);
  if (list.isError) return page(<QueryError error={list.error} onRetry={() => void list.refetch()} />);
  if (list.data.length === 0) {
    return page(
      <Card lg className="border-dashed">
        <EmptyState
          title={t("profiles.none.title")}
          action={
            <Button variant="primary" size="lg" onClick={() => go("/profiles/new")}>
              {t("profiles.new")}
            </Button>
          }
        >
          {t("profiles.none.body")}
        </EmptyState>
      </Card>,
    );
  }

  const all = list.data;
  const protoName = (id: string) => protocols.data?.find((x) => x.id === id)?.displayName ?? id;
  const groupsOf = (id: string) => groups.data?.filter((g) => g.profileIds.includes(id));
  const has: Record<FacetId, (p: ProfileSummary) => boolean> = {
    warn: (p) => p.warnings.length > 0,
    idle: (p) => p.nodeCount === 0,
    nogroup: (p) => groupsOf(p.id)?.length === 0,
    warp: (p) => viaWarp(p.summary),
  };
  const needle = q.trim().toLocaleLowerCase();
  const fits = (p: ProfileSummary, proto: string, st: FacetId | undefined) =>
    (!needle || p.name.toLocaleLowerCase().includes(needle)) && (!proto || p.protocol === proto) && (!st || has[st](p));
  const countOf = (proto: string, st: FacetId | undefined) => all.filter((p) => fits(p, proto, st)).length;
  const shown = all.filter((p) => fits(p, protocol, state)).sort((a, b) => byProtocol(a.protocol, b.protocol));

  // only the protocols there are profiles of (and the one in the URL, so it can be left); a state only when something is in it
  const present = [...new Set([...all.map((p) => p.protocol), ...(protocol ? [protocol] : [])])].sort(byProtocol);
  const states = facetIds.filter((f) => f === state || all.some(has[f]));
  const reset = () => {
    setQ("");
    move({ protocol: undefined, q: undefined, state: undefined }, { replace: true });
  };

  return page(
    <>
      <div className="flex flex-wrap items-center gap-2">
        {(present.length > 1 || protocol) && (
          // the phone scrolls it sideways, from edge to edge, instead of wrapping it into rows
          <div className="no-scrollbar max-w-full overflow-x-auto max-md:-mx-4 max-md:w-[calc(100%+2rem)] max-md:max-w-none max-md:px-4">
            <Segmented
              variant="flat"
              aria-label={t("profiles.list.protocols")}
              value={protocol || "all"}
              onValueChange={(v) => move({ protocol: v === "all" ? undefined : v }, { replace: true })}
              className="w-max"
              options={[
                { value: "all", label: <SegText text={t("profiles.list.all")} n={countOf("", state)} /> },
                ...present.map((id) => ({ value: id, label: <SegText proto={id} text={protoName(id)} n={countOf(id, state)} /> })),
              ]}
            />
          </div>
        )}
        <span className="hidden flex-1 @min-[900px]:block" />
        <label className="relative flex min-w-[150px] flex-1 items-center @min-[900px]:w-[220px] @min-[900px]:flex-none">
          <Icon name="search" size={14} className="pointer-events-none absolute inset-s-3 text-muted" />
          <input
            type="search"
            aria-label={t("profiles.list.search")}
            placeholder={t("profiles.list.search")}
            value={q}
            onChange={(e) => {
              setQ(e.target.value);
              move({ q: e.target.value.trim() || undefined }, { replace: true });
            }}
            className="h-9 w-full rounded-ctl border border-line bg-surface pe-3 ps-8 text-xs font-medium text-fg outline-none transition-colors duration-200 focus:border-accent"
          />
        </label>
        {states.length > 0 && (
          <div className="w-[176px] flex-none @min-[900px]:w-[212px]">
            <Select
              compact
              aria-label={t("profiles.list.state")}
              value={state ?? "all"}
              onValueChange={(v) => move({ state: v === "all" ? undefined : v }, { replace: true })}
              className={state && "border-accent-line! bg-accent-soft!"}
              options={[{ value: "all", label: t("profiles.list.anyState") }, ...states.map((f) => ({ value: f, label: `${t(`profiles.facet.${f}`)} · ${countOf(protocol, f)}` }))]}
            />
          </div>
        )}
      </div>

      {shown.length === 0 ? (
        <Card lg className="border-dashed">
          <EmptyState
            icon={<Icon name="search" className="text-muted" />}
            title={t("profiles.list.nothing")}
            action={
              <Button size="lg" onClick={reset}>
                {t("profiles.list.reset")}
              </Button>
            }
          >
            {t("profiles.list.nothingBody")}
          </EmptyState>
        </Card>
      ) : (
        <Card lg className="overflow-hidden">
          <div role="list" aria-label={t("profiles.title")} className={cx("flex flex-col", table)}>
            <div aria-hidden className={cx("hidden h-10 items-center border-b border-line text-xs font-semibold text-muted", subgrid)}>
              <span className="col-span-2">{t("profiles.col.profile")}</span>
              <span>{t("profiles.col.settings")}</span>
              <span>{t("profiles.col.formats")}</span>
              <span>{t("profiles.col.nodes")}</span>
              <span>{t("profiles.col.people")}</span>
              <span />
            </div>
            {shown.map((p, i) => (
              <ProfileRow key={p.id} style={entrance(i)} p={p} t={t} protocol={protocols.data?.find((x) => x.id === p.protocol)} groups={groupsOf(p.id)} />
            ))}
          </div>
        </Card>
      )}
    </>,
  );
}

/** A protocol choice's text and count, led by the protocol's small mark in its colour (as in the rows) where there is room for it. */
function SegText({ proto, text, n }: { proto?: string; text: string; n: number }) {
  return (
    <span className="flex items-center gap-1.5">
      {proto && (
        <span data-tone={lookOf(proto).tone} className="id-fg hidden @min-[640px]:flex">
          <Icon name={lookOf(proto).icon} size={14} />
        </span>
      )}
      {text}
      <span className="font-mono text-xs text-muted tabular-nums">{n}</span>
    </span>
  );
}

type Line = { icon?: IconName; text: string; warn?: boolean };

/**
 * One cell of a row: icons and a number, and the words behind them in a tooltip (hover and keyboard focus), each line with its
 * icon, so the tooltip is also the legend. In the table it sits above the row's link; on a narrow screen it stays under it, so a
 * tap anywhere opens the profile, whose page says all of it.
 */
function Cell({ lines, children }: { lines: Line[]; children: ReactNode }) {
  return (
    <Tip
      label={
        <span className="flex flex-col gap-1">
          {lines.map((l, i) => (
            <span key={i} className="flex items-start gap-1.5">
              {l.icon && <Icon name={l.icon} size={12} className={cx("mt-[2px]", l.warn ? "text-warn-text" : "text-muted")} />}
              <span>{l.text}</span>
            </span>
          ))}
        </span>
      }
    >
      <span tabIndex={0} aria-label={lines.map((l) => l.text).join(". ")} className="inline-flex min-w-0 cursor-default items-center gap-1.5 rounded-md @min-[760px]:relative">
        {children}
      </span>
    </Tip>
  );
}

/** An icon for a setting of the profile (its words are in the cell's tooltip). */
const Tile = ({ icon }: { icon: IconName }) => <Icon name={icon} size={16} aria-hidden className="flex-none text-muted" />;

/** A number led by its icon: formats, nodes, people. */
const Count = ({ icon, n, dim }: { icon: IconName; n: number; dim?: boolean }) => (
  <>
    <Icon name={icon} size={14} className={dim ? "text-faint" : "text-muted"} />
    <span className={cx("font-mono text-[13px] font-medium tabular-nums", dim ? "text-muted" : "text-fg")}>{n}</span>
  </>
);

/**
 * One part of the server's summary: the port stays words ("UDP 443"), the rest becomes an icon with its sentence for the
 * tooltip. A part this list does not know stays a chip of its own words.
 */
function factOf(part: string, protocol: string, t: Tx): { chip?: string; icon?: IconName; line: string } {
  if (/^(UDP|TCP|XHTTP|WebSocket) \d+$/.test(part)) return { chip: part, line: part };
  const hop = /^hop (\S+)$/.exec(part);
  if (hop) return { icon: "traffic", line: t("profiles.part.hop", { range: hop[1]! }) };
  if (part === "Salamander" || part === "Gecko") return { icon: "mask", line: t("profiles.fact.obfs", { v: part }) };
  if (part === "No obfuscation") return { line: t("profiles.part.noObfs") };
  if (part === "Let's Encrypt" || part === "Let's Encrypt IP") return { icon: "lock", line: t("profiles.fact.cert", { v: part }) };
  if (part === "Self-signed") return { icon: "pin", line: t("profiles.fact.cert", { v: t("profiles.part.selfSigned") }) };
  if (part === "REALITY") return { icon: "mask", line: t("profiles.fact.security", { v: part }) };
  if (part === "TLS") return { icon: "lock", line: t("profiles.fact.security", { v: part }) };
  if (part === "Vision") return { icon: "eye", line: t("profiles.fact.vision") };
  if (part === "WARP") return { icon: "cloud", line: t("profiles.facet.warp") };
  if (part === "Fake TLS") return { icon: "mask", line: t("profiles.fact.fakeTls") };
  // MTProto names the site it passes for after "Fake TLS" ("www.google.com"), when it is another site
  if (protocol === "mtproto" && /^[a-z0-9.-]+.[a-z]{2,}$/i.test(part)) return { icon: "globe", line: t("profiles.fact.site", { v: part }) };
  const awg = /^AWG (\S+)$/.exec(part);
  if (awg) return { chip: `v${awg[1]}`, line: t("profiles.fact.version", { v: awg[1]! }) };
  // what follows the version of an AmneziaWG profile is the look it takes ("DNS", "QUIC")
  if (protocol === "awg") return { icon: "mask", line: t("profiles.fact.look", { v: part }) };
  return { chip: part, line: part };
}

function Facts({ p, t }: { p: ProfileSummary; t: Tx }) {
  const facts = summaryParts(p.summary).map((x) => factOf(x, p.protocol, t));
  if (facts.length === 0) return <span className="text-muted">—</span>;
  return (
    <Cell lines={facts.map((f) => ({ icon: f.icon, text: f.line }))}>
      {facts.map((f, i) =>
        f.chip ? (
          <Chip key={i} mono>
            {f.chip}
          </Chip>
        ) : f.icon ? (
          <Tile key={i} icon={f.icon} />
        ) : null,
      )}
    </Cell>
  );
}

/** In how many formats the profile is, the ones that drop it as one amber mark; AmneziaWG's keys and Telegram's links by design. */
function Formats({ p, protocol, t }: { p: ProfileSummary; protocol?: { apps: App[] }; t: Tx }) {
  if (protocol && telegramOnly(protocol.apps)) {
    return (
      <Cell lines={[{ icon: "plane", text: t("profiles.list.tgTip") }]}>
        <Icon name="plane" size={14} className="text-muted" />
        <span className="font-mono text-xs text-muted">tg://</span>
      </Cell>
    );
  }
  if (p.formats.length === 0) return <span className="text-muted">—</span>;
  const ok = p.formats.filter((f) => f.ok);
  const drops = dropsOf(p.formats);
  const keys = keysOnly(drops);
  const lines: Line[] = [
    { icon: "link", text: t("profiles.list.formatsOk", { list: ok.map((f) => nameOf(f.format, t)).join(t("profiles.fmt.sep")) }) },
    ...(keys
      ? [{ icon: "key" as const, text: `${t("profiles.fmt.keys")}. ${t("profiles.fmt.tip.awg")}` }]
      : drops.map((d) => dropWords(d, t)).map((w) => ({ icon: "warn" as const, warn: true, text: `${w.text}. ${w.full}` }))),
  ];
  return (
    <Cell lines={lines}>
      <Count icon="link" n={ok.length} />
      {keys && <Icon name="key" size={13} className="text-muted" />}
      {!keys && drops.length > 0 && <Icon name="warn" size={13} className="text-warn-text" />}
    </Cell>
  );
}

/** On how many nodes, the nodes' own warnings ("Did not start on fi1") as one amber mark. */
function Nodes({ p, t }: { p: ProfileSummary; t: Tx }) {
  const warns = p.warnings.map((w) => warnText(w, t));
  const lines: Line[] = [
    { icon: "server", text: p.nodeCount > 0 ? t.n("profiles.list.onNodes", p.nodeCount) : t("profiles.list.notOnNodes") },
    ...warns.map((w) => ({ icon: "warn" as const, warn: true, text: w })),
  ];
  return (
    <Cell lines={lines}>
      <Count icon="server" n={p.nodeCount} dim={p.nodeCount === 0} />
      {warns.length > 0 && <Icon name="warn" size={13} className="text-warn-text" />}
    </Cell>
  );
}

/** How many people get the profile; their groups in the tooltip. */
function People({ p, t, groups }: { p: ProfileSummary; t: Tx; groups?: readonly { name: string }[] }) {
  const lines: Line[] = [
    { icon: "people", text: `${p.userCount} ${t.n("profiles.users", p.userCount)}` },
    ...(groups ? [{ icon: "layers" as const, text: groups.length > 0 ? t("profiles.list.groups", { list: groups.map((g) => g.name).join(", ") }) : t("profiles.label.noGroup") }] : []),
  ];
  return (
    <Cell lines={lines}>
      <Count icon="people" n={p.userCount} dim={p.userCount === 0} />
    </Cell>
  );
}

/**
 * One profile as a row: the protocol's mark, the name (the row's link: its ::after covers the row) over the protocol's name,
 * then icons with numbers, the words of each in its tooltip: the settings, the formats, the nodes, the people, and a chevron.
 * Below 760px the row stacks: the name, the settings, then the protocol's name with the three counts.
 */
export function ProfileRow({ p, t, protocol, groups, style }: { style?: CSSProperties; p: ProfileSummary; t: Tx; protocol?: { displayName: string; apps: App[] }; groups?: readonly { id: string; name: string }[] }) {
  const look = lookOf(p.protocol);
  const name = protocol?.displayName ?? p.protocol;
  return (
    <Fresh
      kind="profile"
      id={p.id}
      role="listitem"
      style={style}
      className={cx(
        "group relative grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1.5 border-b border-line px-4 py-3 text-[13px] transition-colors duration-200 last:border-b-0 hover:bg-surface-2/50 has-[a:focus-visible]:bg-surface-2/50",
        subgrid,
        "@min-[760px]:min-h-16 @min-[760px]:py-2.5",
      )}
    >
      <IconChip icon={look.icon} tone={look.tone} size={32} className="row-span-3 self-start rounded-[10px] @min-[760px]:row-span-1 @min-[760px]:self-center" />
      <div className="flex min-w-0 flex-col gap-0.5">
        <AppLink
          to="/profiles/$id"
          params={{ id: p.id }}
          className="truncate text-sm leading-5 font-semibold after:absolute after:inset-0 focus-visible:outline-none focus-visible:after:outline-2 focus-visible:after:-outline-offset-2 focus-visible:after:outline-accent"
        >
          {p.name}
        </AppLink>
        <span className="hidden truncate text-xs text-muted @min-[760px]:block">{name}</span>
      </div>
      <div className="col-start-2 row-start-2 flex min-w-0 @min-[760px]:col-start-auto @min-[760px]:row-start-auto">
        <Facts p={p} t={t} />
      </div>
      {/* the table's three cells; in the stacked row the last line, after the protocol's name */}
      <div className="col-start-2 row-start-3 flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1 @min-[760px]:contents">
        <span className="w-[76px] flex-none truncate text-xs text-muted @min-[760px]:hidden">{name}</span>
        <div className="flex">
          <Formats p={p} protocol={protocol} t={t} />
        </div>
        <div className="flex">
          <Nodes p={p} t={t} />
        </div>
        <div className="flex">
          <People p={p} t={t} groups={groups} />
        </div>
      </div>
      <Icon
        name="chevronRight"
        size={16}
        className="col-start-3 row-span-3 row-start-1 text-faint transition-colors group-hover:text-muted @min-[760px]:col-start-auto @min-[760px]:row-span-1 @min-[760px]:row-start-auto"
      />
    </Fresh>
  );
}
