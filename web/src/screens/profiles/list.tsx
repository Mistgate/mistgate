import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useId, useState, type ReactNode } from "react";
import type { ProfileSummary, ProtocolInfo } from "@/gen/mistgate/admin/v1/profile_pb";
import { Fresh } from "@/components/fx/fresh";
import { Button } from "@/components/ui/button";
import { Segmented } from "@/components/ui/segmented";
import { cx } from "@/lib/cx";
import { useEffectsEnabled } from "@/lib/effects";
import { userCountQuery } from "@/lib/queries";
import { useMove } from "@/lib/paging";
import { useIsOwner } from "@/lib/updates";
import { G } from "@/screens/users/user-bits";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { useTx, type Tx } from "@/screens/users/t";
import { dropsOf, dropWords, keysOnly } from "./formats";
import { ProfilesMatrix } from "./list-matrix";
import { D, Gl, kindOf, Mark, PK, type Kind } from "./pf-bits";
import { useServers, type Servers } from "./profiles-data";
import { worstOf, type Gk, type PCell } from "./profiles-model";
import { byProtocol, viaWarp } from "./protocol-meta";
import { facetIds, validateProfilesSearch, type FacetId } from "./search";
import "./profiles.css";

const Lnk = Link as unknown as (props: Record<string, unknown>) => ReactNode;

const warnText = (w: { code: string; params: Record<string, string> }, t: Tx) => t.opt(`profiles.warn.${w.code}`, w.params) ?? w.code.replaceAll("_", " ");

/** What the dev kit starts the list with: the matrix's open cell ("profile id|node id") and the state filter the URL would hold. The app starts with none. */
export type ProfilesDemo = { open?: string; facet?: FacetId };

/**
 * /profiles (the board's PnProfiles): the title and "New profile", one line of what there is, the search and the view (the list, or
 * the matrix "Where it passes"), the protocol chips with the states beside them. Every filter is in the URL (`protocol`, `q`,
 * `state`, `view`); each count is what that choice would show with the other filters kept.
 */
export function ProfilesScreen({ demo }: { demo?: ProfilesDemo } = {}) {
  const t = useTx();
  const owner = useIsOwner(); // policy.go: only the owner makes, changes and deletes profiles; the rest read them
  const move = useMove();
  const navigate = useNavigate();
  const fx = useEffectsEnabled();
  const search = validateProfilesSearch(useSearch({ strict: false }) as Record<string, unknown>);
  const list = useQuery(profileListQuery);
  const protocols = useQuery(protocolsQuery);
  const groups = useQuery(groupsQuery);
  const people = useQuery(userCountQuery);
  const servers = useServers(list.data);
  // the box is the reader's own; the URL mirrors it (replace: no history entry per letter)
  const [q, setQ] = useState(search.q ?? "");
  const [open, setOpen] = useState(demo?.open ?? "");
  const protocol = search.protocol ?? "";
  const state = search.state ?? demo?.facet;
  const view = search.view === "where" ? "where" : "list";

  const head = (
    <div className="pf-ph pv-ph">
      <h1>{t("profiles.title")}</h1>
      {/* looking at one protocol, a new profile is most likely one more of it: its wizard opens at once on that protocol */}
      {owner && (
        <Button variant="primary" size="lg" onClick={() => navigate({ to: "/profiles/new", search: protocol ? { protocol } : {} } as never)}>
          <G d={D.plus} size={16} stroke={1.5} />
          {t("profiles.create")}
        </Button>
      )}
    </div>
  );
  const page = (body: ReactNode) => (
    <div className={cx("pf pv-page", fx && "pf-fx")}>
      {head}
      {body}
    </div>
  );

  // the groups and the protocol names decide the filters and the rows: the screen waits for them, so nothing jumps in later
  if (list.isError) return page(<LoadError t={t} onRetry={() => void list.refetch()} />);
  if (list.isPending || groups.isPending || protocols.isPending) return page(<Loading t={t} where={view === "where"} />);
  if (list.data.length === 0) return page(<Empty t={t} owner={owner} />);

  const all = list.data;
  const protoOf = (id: string) => protocols.data?.find((x) => x.id === id);
  const protoName = (id: string) => protoOf(id)?.displayName ?? id;
  const groupsOf = (id: string) => groups.data?.filter((g) => g.profileIds.includes(id)) ?? [];
  const has: Record<FacetId, (p: ProfileSummary) => boolean> = {
    warn: (p) => p.warnings.length > 0,
    idle: (p) => p.nodeCount === 0,
    nogroup: (p) => groupsOf(p.id).length === 0,
    warp: (p) => viaWarp(p.summary),
  };
  const needle = q.trim().toLocaleLowerCase();
  const fits = (p: ProfileSummary, proto: string, st: FacetId | undefined) => (!needle || p.name.toLocaleLowerCase().includes(needle)) && (!proto || p.protocol === proto) && (!st || has[st](p));
  const countOf = (proto: string, st: FacetId | undefined) => all.filter((p) => fits(p, proto, st)).length;
  const shown = all.filter((p) => fits(p, protocol, view === "list" ? state : undefined)).sort((a, b) => byProtocol(a.protocol, b.protocol));

  // only the protocols there are profiles of (and the one in the URL, so it can be left); a state only when something is in it
  const present = [...new Set([...all.map((p) => p.protocol), ...(protocol ? [protocol] : [])])].sort(byProtocol);
  const states = facetIds.filter((f) => f === state || all.some(has[f]));
  const nowhere = all.filter(has.idle).length;
  const reset = () => {
    setQ("");
    move({ protocol: undefined, q: undefined, state: undefined }, { replace: true });
  };
  const views = [
    { value: "list", label: t("pf.view.list") },
    { value: "where", label: t("pf.view.where") },
  ] as const;

  return page(
    <>
      <div className="pv-top">
        <p className="pv-sum">
          <span>{t.n("pf.sum.profiles", all.length)}</span>
          {people.data?.counts && <span>{t.n("pf.sum.people", people.data.counts.all)}</span>}
          {nowhere > 0 && <span>{t.n("pf.sum.nowhere", nowhere)}</span>}
        </p>
        <div className="pv-tr">
          <label className={cx("pv-q", q && "has")}>
            <G d={D.search} size={16} stroke={1.5} />
            <input
              type="search"
              aria-label={t("pf.search")}
              placeholder={t("pf.search")}
              value={q}
              onChange={(e) => {
                setQ(e.target.value);
                move({ q: e.target.value.trim() || undefined }, { replace: true });
              }}
            />
          </label>
          <Segmented
            variant="flat"
            aria-label={t("pf.view")}
            value={view}
            onValueChange={(v) => {
              setOpen("");
              move({ view: v === "where" ? "where" : undefined }, { replace: true });
            }}
            className="pv-seg"
            options={views}
          />
        </div>
      </div>
      <div className="pv-fl">
        <div className="pv-chips" role="group" aria-label={t("pf.proto")}>
          <Chip on={!protocol} onClick={() => move({ protocol: undefined }, { replace: true })} n={countOf("", view === "list" ? state : undefined)}>
            {t("pf.all")}
          </Chip>
          {present.map((id) => (
            <Chip key={id} on={protocol === id} onClick={() => move({ protocol: protocol === id ? undefined : id }, { replace: true })} n={countOf(id, view === "list" ? state : undefined)} kind={kindOf(id)}>
              {protoName(id)}
            </Chip>
          ))}
          {view === "list" && states.length > 0 && <i className="pv-dv" aria-hidden />}
          {view === "list" &&
            states.map((f) => (
              <Chip key={f} on={state === f} onClick={() => move({ state: state === f ? undefined : f }, { replace: true })} n={countOf(protocol, f)}>
                {t(`pf.state.${f === "idle" ? "none" : f}`)}
              </Chip>
            ))}
        </div>
      </div>

      {view === "list" ? (
        <section className="card pv-tb">
          <div className="pv-r pv-hd" aria-hidden>
            <span>{t("pf.col.profile")}</span>
            <span>{t("pf.col.where")}</span>
            <span>{t("pf.col.people")}</span>
            <span>{t("pf.col.remarks")}</span>
            <span />
          </div>
          <div role="list" aria-label={t("profiles.title")} className="pv-rows">
            {shown.map((p) => (
              <Row key={p.id} p={p} t={t} protocol={protoOf(p.protocol)} groups={groupsOf(p.id)} servers={servers} owner={owner} />
            ))}
          </div>
          {shown.length === 0 && <NoFind t={t} onReset={reset} />}
        </section>
      ) : (
        <>
          {shown.length === 0 && (
            <section className="card pv-card-nf">
              <NoFind t={t} onReset={reset} />
            </section>
          )}
          <ProfilesMatrix profiles={shown} servers={servers} owner={owner} open={open} onOpen={setOpen} />
        </>
      )}
    </>,
  );
}

/** One chip of the bar: the protocol's mark where it has one, the words and the count. */
function Chip({ on, onClick, n, kind, children }: { on: boolean; onClick: () => void; n: number; kind?: Kind; children: ReactNode }) {
  return (
    <button type="button" aria-pressed={on} onClick={onClick}>
      {kind && <G d={PK[kind].d} size={14} stroke={2} className={cx("pv-t", kind)} />}
      {children}
      <span className="mono">{n}</span>
    </button>
  );
}

/** "Nothing fits these filters": what the list and the matrix say, and the way back. */
function NoFind({ t, onReset }: { t: Tx; onReset: () => void }) {
  return (
    <div className="pv-nf">
      <p>{t("pf.nf")}</p>
      <Button size="md" onClick={onReset}>
        {t("pf.nf.reset")}
      </Button>
    </div>
  );
}

/** The first state: no profile yet (the owner is offered the wizard, anyone else is told whose it is). */
function Empty({ t, owner }: { t: Tx; owner: boolean }) {
  const navigate = useNavigate();
  return (
    <section className="card pf-none">
      <span className="g" aria-hidden>
        <G d={D.stack} size={20} stroke={1.5} />
      </span>
      <b>{t("pf.empty.title")}</b>
      <span>{t("pf.empty.body")}</span>
      {owner ? (
        <Button variant="primary" size="lg" onClick={() => navigate({ to: "/profiles/new" } as never)}>
          <G d={D.plus} size={16} stroke={1.5} />
          {t("profiles.create")}
        </Button>
      ) : (
        <em className="pv-quiet">{t("pf.empty.owner")}</em>
      )}
    </section>
  );
}

/** The panel did not answer: what it says, and that nothing changed on the nodes. */
function LoadError({ t, onRetry }: { t: Tx; onRetry: () => void }) {
  return (
    <section className="card pf-none pv-qe" role="alert">
      <span className="g bad" aria-hidden>
        <G d={D.warn} size={20} stroke={1.5} />
      </span>
      <b>{t("pf.err.title")}</b>
      <span>{t("pf.err.body")}</span>
      <Button size="lg" onClick={onRetry}>
        {t("pf.err.retry")}
      </Button>
    </section>
  );
}

const skW = [
  ["148px", "236px"],
  ["132px", "212px"],
  ["156px", "248px"],
  ["120px", "176px"],
  ["128px", "228px"],
  ["112px", "196px"],
];

/** The load: the line over the list, the chips and six rows of grey bars, in the shape of the rows (or of the matrix). */
function Loading({ t, where }: { t: Tx; where: boolean }) {
  return (
    <>
      <div className="pv-top" aria-hidden>
        <p className="pv-sum">
          <i className="pf-sk" style={{ width: 280 }} />
        </p>
        <i className="pf-sk" style={{ width: 200, height: 36 }} />
      </div>
      <div className="pv-fl" aria-hidden>
        <i className="pf-sk" style={{ width: 520, height: 32 }} />
      </div>
      <section className="card pv-tb pv-skc" aria-busy="true" aria-label={t("pf.loading")}>
        {skW.map(([a, b], i) => (
          <div key={i} className={cx("pv-r pv-sk", where && "mx")}>
            <span className="pv-n">
              <i className="pf-sk sq" />
              <span>
                <i className="pf-sk" style={{ width: a }} />
                <i className="pf-sk s" style={{ width: b }} />
              </span>
            </span>
            <span className="pv-wh">
              <i className="pf-sk p" />
              <i className="pf-sk p" />
              <i className="pf-sk p" />
            </span>
            <span className="pv-2">
              <i className="pf-sk" style={{ width: 28 }} />
              <i className="pf-sk s" style={{ width: 96 }} />
            </span>
            <span />
            <span />
          </div>
        ))}
      </section>
    </>
  );
}

/** What the profile's own words say under its name ("Hysteria2 · UDP 443 · Salamander"), and in its tooltip what the formats leave out. */
export function words(p: ProfileSummary, protocol: ProtocolInfo | undefined, t: Tx) {
  const w = `${protocol?.displayName ?? p.protocol} · ${p.summary}`;
  const drops = dropsOf(p.formats).filter((d) => !keysOnly([d]));
  return { w, title: drops.length ? `${w} · ${drops.map((d) => dropWords(d, t).text).join(" · ")}` : w };
}

type Remark = { g: Gk; text: string };

/** The remarks of a profile: the servers' own warnings (red when a server did not start), then the formats that leave it out. */
function remarksOf(p: ProfileSummary, t: Tx): Remark[] {
  const warn: Remark[] = p.warnings.map((w) => ({ g: w.code === "inbound_failed" ? "bad" : "part", text: warnText(w, t) }));
  const drops: Remark[] = dropsOf(p.formats)
    .filter((d) => !keysOnly([d]))
    .map((d) => ({ g: "part" as const, text: dropWords(d, t).full }));
  return [...warn, ...drops];
}

/** The people cell: the count (with what the protocol reaches them by, where that is not the subscription) and the groups' names. */
function peopleOf(p: ProfileSummary, kind: Kind, groups: readonly { name: string }[], t: Tx) {
  const n = String(p.userCount);
  const head = groups.length === 0 ? "—" : kind === "tg" ? t("pf.row.tg", { n }) : kind === "tt" ? t("pf.row.tt", { n }) : n;
  return { head, line: groups.length ? groups.map((g) => g.name).join(", ") : t("pf.row.noGroup"), warn: groups.length === 0 };
}

/** The chips of the servers' nodes: up to four, "+N" for the rest; "nowhere" and the offer to put it on nodes for a profile with none. */
function Where({ p, cells, servers, owner, t }: { p: ProfileSummary; cells: PCell[]; servers: Servers; owner: boolean; t: Tx }) {
  if (p.nodeCount === 0 && cells.length === 0) {
    return (
      <span className="pv-wh">
        <span className="off">
          <Gl k="none" />
          {t("pf.row.nowhere")}
        </span>
        {owner && <u>{t("pf.row.put")}</u>}
      </span>
    );
  }
  // the checks have not come (or failed): the profile's own count says how many nodes, the names wait for them
  if (!servers.ready) {
    return (
      <span className="pv-wh">
        {servers.checks.isPending ? (
          <>
            <i className="pf-sk p" />
            <i className="pf-sk p" />
          </>
        ) : (
          <span>{t.n("pf.row.nodesCount", p.nodeCount)}</span>
        )}
      </span>
    );
  }
  return (
    <span className="pv-wh">
      {cells.slice(0, 4).map((c) => (
        <span key={c.nodeId} className={c.kind === "bad" ? "bad" : c.kind === "gone" || c.kind === "off" ? "off" : ""}>
          <span className="mono">{c.nodeName}</span>
        </span>
      ))}
      {cells.length > 4 && <em>+{cells.length - 4}</em>}
    </span>
  );
}

/**
 * One profile as a row of the board's table: the mark and the name over its words, the servers' nodes, the people and the groups,
 * the count of remarks (the words in a tooltip, on hover and on focus) and a chevron. The name is the row's link; its ::after
 * covers the row, the remarks cell stays over it so its tooltip can be reached.
 */
function Row({ p, t, protocol, groups, servers, owner }: { p: ProfileSummary; t: Tx; protocol?: ProtocolInfo; groups: readonly { id: string; name: string }[]; servers: Servers; owner: boolean }) {
  const kind = kindOf(p.protocol);
  const cells = servers.cells(p.id);
  const { w, title } = words(p, protocol, t);
  const remarks = remarksOf(p, t);
  const ppl = peopleOf(p, kind, groups, t);
  const worst = worstOf(remarks.map((r) => r.g));
  const tip = useId();
  return (
    <Fresh kind="profile" id={p.id} role="listitem" className="pv-r">
      <span className="pv-n">
        <Mark k={kind} />
        <span>
          <Lnk to="/profiles/$id" params={{ id: p.id }} aria-label={t("pf.row.open", { name: p.name })} aria-describedby={remarks.length ? tip : undefined} className="pv-lnk">
            <b>{p.name}</b>
          </Lnk>
          <span title={title}>{w}</span>
        </span>
      </span>
      <Where p={p} cells={cells} servers={servers} owner={owner} t={t} />
      <span className={cx("pv-2", ppl.warn && "warn")}>
        <b>{ppl.head}</b>
        <em>{ppl.line}</em>
      </span>
      <span className={cx("pv-rm", worst || (remarks.length ? "part" : ""))}>
        {remarks.length > 0 && (
          <>
            <b>{worst === "bad" ? remarks.filter((r) => r.g === "bad").length : remarks.length}</b>
            <em id={tip} className="pr-tip pv-tip" role="tooltip">
              {remarks.map((r, i) => (
                <span key={i}>
                  <Gl k={r.g} />
                  {r.text}
                </span>
              ))}
            </em>
          </>
        )}
      </span>
      <G d={D.chev} size={16} stroke={1.5} className="pr-go" />
    </Fresh>
  );
}
