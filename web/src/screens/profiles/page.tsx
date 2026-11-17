import { Code } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useEffect, useState, type ReactNode } from "react";
import { Menu, MenuItem, MenuLink } from "@/components/ui/menu";
import { Button, buttonClass } from "@/components/ui/button";
import { Tabs, type TabItem } from "@/components/ui/tabs";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { profiles } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useEffectsEnabled } from "@/lib/effects";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { useIsOwner } from "@/lib/updates";
import { groupsQuery, isCode, protocolsQuery } from "@/screens/users/rpc";
import { useTx, type Tx } from "@/screens/users/t";
import { G } from "@/screens/users/user-bits";
import { inboundErrorKind, inboundErrorText } from "@/lib/inbound-error";
import { words } from "./list";
import { PageDialogs } from "./page-dialogs";
import { useProfileForm } from "./page-form";
import { PeopleTab } from "./page-people";
import { ServersTab } from "./page-servers";
import { SettingsTab } from "./page-settings";
import type { Dlg, PageData, TabCtx } from "./page-types";
import { certOfSettings, egressOf } from "./page-util";
import { D, Gl, kindOf, Mark } from "./pf-bits";
import { useServers } from "./profiles-data";
import { serverRows, stateOf, type PState, type ServerRow } from "./profiles-model";
import { validateProfileSearch, type ProfileTab } from "./search";
import "./profiles.css";
import "./profile-page.css";

const Lnk = Link as unknown as (props: Record<string, unknown>) => ReactNode;

/** What a scene fixes: the tab, the window and the state of the check's load (the app starts on the address). */
export type PageScene = { tab?: ProfileTab; dlg?: Dlg; stale?: boolean; gone?: boolean; /** What the scene adds for a tab (its own demo state, named by the tab). */ variant?: string };

/**
 * /profiles/$id (the board's PnProfile): the saved profile on a page of three tabs, Settings, Servers on nodes and People. The page loads the
 * profile and the facts around it; while the form holds typed changes a version saved elsewhere waits behind "Reload" instead of replacing
 * what is on screen.
 */
export function ProfileEditorScreen() {
  const { id = "" } = useParams({ strict: false }) as { id?: string };
  const t = useTx();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["profiles", "detail", id], queryFn: ({ signal }) => profiles.getProfile({ profileId: id }, { signal }) });
  const protocols = useQuery(protocolsQuery);
  // The form starts from what the server holds and is redrawn when the saved version (or name) changes, but not under someone who is
  // typing: a version saved in another window waits behind "Reload" while the open form holds changes.
  const [edits, setEdits] = useState(0);
  const [shown, setShown] = useState<{ key: string; data: PageData } | undefined>();
  const [gone, setGone] = useState(false);
  const profile = q.data?.profile;
  const info = profile && protocols.data?.find((p) => p.id === profile.protocol);
  const live = q.data && profile && info ? { key: `${profile.id}:${profile.version}:${profile.name}`, data: { profile, info, inbounds: q.data.inbounds, initial: { name: profile.name, settingsJson: q.data.settingsJson } } satisfies PageData } : undefined;
  if (live && (!shown || (shown.key !== live.key && (edits === 0 || shown.data.profile.id !== live.data.profile.id)))) setShown(live);
  const reload = () => {
    setEdits(0);
    if (live) setShown(live);
    void qc.invalidateQueries({ queryKey: ["profiles"] });
  };
  const retry = () => void q.refetch();

  if (gone) return <Wrap><Gone t={t} /></Wrap>;
  if (q.isPending || protocols.isPending) return <Wrap><Skeleton t={t} /></Wrap>;
  if (q.isError) {
    if (isCode(q.error, Code.NotFound)) return <Wrap><NotFound t={t} /></Wrap>;
    return <Wrap><Failed t={t} onRetry={retry} /></Wrap>;
  }
  if (protocols.isError) return <Wrap><Failed t={t} onRetry={() => void protocols.refetch()} /></Wrap>;
  if (!info) return <Wrap><NotFound t={t} /></Wrap>;
  const on = shown ?? live!;
  return <ProfilePage key={on.key} data={on.data} behind={!!live && live.key !== on.key} onEdits={setEdits} onReload={reload} onGone={() => setGone(true)} />;
}

/** The page's frame: the way back above whatever the page says. */
function Wrap({ children, fx }: { children: ReactNode; fx?: boolean }) {
  const t = useTx();
  const on = useEffectsEnabled();
  return (
    <div className={cx("pf pp-page", (fx ?? on) && "pf-fx")}>
      <Lnk className="nd-back" to="/profiles">
        <G d={D.back} size={16} stroke={1.5} />
        {t("pf.pg.back")}
      </Lnk>
      {children}
    </div>
  );
}

function Gone({ t }: { t: Tx }) {
  return (
    <section className="card up-gone" role="status">
      <span className="g" aria-hidden>
        <G d="M5 12.5l4.5 4.5L19 7.5" size={22} stroke={1.5} />
      </span>
      <b>{t("pf.pg.gone")}</b>
      <span>{t("pf.pg.goneBody")}</span>
      <Lnk className={buttonClass("secondary", "lg")} to="/profiles">
        {t("pf.pg.goneBack")}
      </Lnk>
    </section>
  );
}

function NotFound({ t }: { t: Tx }) {
  return (
    <section className="card pf-none pv-qe">
      <span className="g" aria-hidden>
        <G d={D.search} size={20} stroke={1.5} />
      </span>
      <b>{t("pf.pg.nf")}</b>
      <span>{t("pf.pg.nfBody")}</span>
      <Lnk className={buttonClass("secondary", "lg")} to="/profiles">
        {t("pf.pg.goneBack")}
      </Lnk>
    </section>
  );
}

function Failed({ t, onRetry }: { t: Tx; onRetry: () => void }) {
  return (
    <section className="card pf-none pv-qe" role="alert">
      <span className="g bad" aria-hidden>
        <G d={D.warn} size={20} stroke={1.5} />
      </span>
      <b>{t("pf.pg.err")}</b>
      <span>{t("pf.err.body")}</span>
      <Button size="lg" onClick={onRetry}>
        {t("pf.err.retry")}
      </Button>
    </section>
  );
}

/** The load: the head, the tabs and two cards of grey bars, in the shape of the page. */
function Skeleton({ t }: { t: Tx }) {
  return (
    <div className="pp-skp" aria-busy="true" aria-label={t("pf.pg.loading")}>
      <div className="pp-skh">
        <i className="pf-sk sq l" />
        <span>
          <i className="pf-sk h" style={{ width: 260 }} />
          <i className="pf-sk s" style={{ width: 340 }} />
        </span>
        <i className="pf-sk" style={{ width: 168, height: 40 }} />
      </div>
      <div className="pp-skt">
        <i className="pf-sk" style={{ width: 84 }} />
        <i className="pf-sk" style={{ width: 132 }} />
        <i className="pf-sk" style={{ width: 52 }} />
      </div>
      <div className="pf-g pp-grid">
        <div className="col">
          <section className="card pp-skc">
            <i className="pf-sk" style={{ width: 140 }} />
            <i className="pf-sk b" />
            <i className="pf-sk b" />
            <i className="pf-sk b" />
          </section>
          <section className="card pp-skc">
            <i className="pf-sk" style={{ width: 196 }} />
            <i className="pf-sk b" />
            <i className="pf-sk b" />
          </section>
        </div>
        <aside className="col">
          <section className="card pp-skc">
            <i className="pf-sk" style={{ width: 150 }} />
            <i className="pf-sk b t" />
          </section>
        </aside>
      </div>
    </div>
  );
}

const stateWord = (s: { kind: PState; on: number; up: number }, t: Tx) =>
  s.kind === "part" ? t("pf.pst.part", { up: s.up, on: s.on }) : t(`pf.pst.${s.kind}` as "pf.pst.ok");

/** The verdict bar under the head: what keeps the profile from working, and the one button that puts it right (the board's .pp-vd2). */
function verdictOf(args: { st: ReturnType<typeof stateOf>; bad: ServerRow[]; owner: boolean; canGroups: boolean; t: Tx; processOf: (r: ServerRow) => string }) {
  const { st, bad, owner, canGroups, t } = args;
  if (bad.length > 0) {
    const r = bad[0]!;
    const text = inboundErrorText(t, r.inbound.lastError, { port: r.inbound.port, protocol: r.inbound.protocol, process: args.processOf(r) });
    const port = inboundErrorKind(r.inbound.lastError) === "port";
    return { tone: "bad", title: t("pf.pg.vd.bad", { node: r.nodeName }), line: text, more: bad.length > 1 ? t("pf.pg.vd.more", { n: bad.length - 1 }) : "", btn: owner ? (port ? t("pf.pg.vd.port", { node: r.nodeName }) : t("pf.pg.vd.fix")) : "", act: "edit" as const, row: r };
  }
  if (st.kind === "none") return { tone: "none", title: t("pf.pg.vd.none"), line: t("pf.pg.vd.noneBody"), more: "", btn: owner ? t("pf.pg.put") : "", act: "put" as const };
  if (st.kind === "nogr") return { tone: "part", title: t("pf.pg.vd.nogr"), line: t("pf.pg.vd.nogrBody"), more: "", btn: canGroups ? t("pf.pg.vd.addGroup") : "", act: "people" as const };
  if (st.kind === "off") return { tone: "none", title: t("pf.pg.vd.off"), line: t("pf.pg.vd.offBody"), more: "", btn: t("pf.pg.vd.toServers"), act: "servers" as const };
  return null;
}

/**
 * One profile on data it is given: the head (the protocol's mark, the name and its state, the words of the profile, the nodes and the
 * people, "Put on nodes" and the menu), the verdict, the three tabs. `scene` (the dev kit) fixes the tab and a window in the page's own
 * state, so a scene's address names them and a click on a tab does not leave the scene.
 */
export function ProfilePage({ data, behind, onEdits, onReload, onGone, scene }: { data: PageData; behind?: boolean; onEdits?: (n: number) => void; onReload?: () => void; onGone?: () => void; scene?: PageScene }) {
  const t = useTx();
  const fx = useEffectsEnabled();
  const navigate = useNavigate();
  const search = validateProfileSearch(useSearch({ strict: false }) as Record<string, unknown>);
  const role = useQuery(meQuery).data?.admin?.role;
  const owner = useIsOwner();
  const canGroups = role === Role.OWNER || role === Role.HELPER;
  const { profile, info, inbounds } = data;
  const nodeList = useQuery(nodesQuery);
  const groupList = useQuery(groupsQuery);
  const checks = useServers([profile]);
  const [sceneTab, setSceneTab] = useState<ProfileTab>(scene?.tab ?? "settings");
  const tab: ProfileTab = scene ? sceneTab : (search.tab ?? "settings");
  const goTab = (next: ProfileTab) => (scene ? setSceneTab(next) : void navigate({ to: "/profiles/$id", params: { id: profile.id }, search: next === "settings" ? {} : { tab: next }, replace: true } as never));
  const [dlg, setDlg] = useState<Dlg>(scene?.dlg ?? "");
  const [edit, setEdit] = useState("");
  const [stale, setStale] = useState(!!scene?.stale);
  const readOnly = !owner;

  const form = useProfileForm(data, readOnly, () => setStale(true));
  useEffect(() => {
    onEdits?.(form.count);
  }, [onEdits, form.count]);

  const servers = serverRows(inbounds, nodeList.data?.nodes ?? [], checks.cells(profile.id));
  const groups = groupList.data ?? [];
  const inGroups = groups.filter((g) => g.profileIds.includes(profile.id));
  const st = stateOf(servers, inGroups.length, servers.length);
  const bad = servers.filter((s) => s.kind === "bad");
  const kind = kindOf(info.id);
  const egress = egressOf(data.initial.settingsJson, form.fields.some((f) => f.id === "egress"));
  const cert = certOfSettings(info.id, form.base);
  const nodeCount = new Set(servers.map((s) => s.nodeId)).size;
  const canPut = owner && (nodeList.data?.nodes.length ?? 0) > nodeCount;

  const ctx: TabCtx = { data, owner, canGroups, servers, groups, inGroups, cert, edits: form.count, egress, openDlg: setDlg, goTab, edit, onEdit: setEdit };
  const verdict = verdictOf({ st, bad, owner, canGroups, t, processOf: () => "" });
  const doVerdict = () => {
    if (!verdict) return;
    if (verdict.act === "edit") {
      setEdit(verdict.row!.inbound.id);
      goTab("servers");
    } else if (verdict.act === "put") setDlg("put");
    else goTab(verdict.act);
  };
  const w = words(profile, info, t);

  const qc = useQueryClient();
  const reload = onReload ?? (() => void qc.invalidateQueries({ queryKey: ["profiles"] }));
  // under the tabs, over whatever the tab says: a version saved elsewhere, and what a reader cannot do
  const lead = (
    <>
      {(stale || behind) && (
        <div className="pp-vd pp-vd2 part" role="alert">
          <span className="pp-vdi" aria-hidden>
            <Gl k="part" />
          </span>
          <span className="pp-vdt">
            <b>{t("pf.pg.stale")}</b>
            <em>{t("pf.pg.staleBody")}</em>
          </span>
          <Button size="md" onClick={reload}>
            {t("profiles.reload")}
          </Button>
        </div>
      )}
      {readOnly && <p className="pp-ro">{t("pf.pg.ro")}</p>}
    </>
  );
  const items: TabItem[] = [
    {
      value: "settings",
      label: (
        <>
          {t("pf.pg.tab.settings")}
          {form.count > 0 && <span className="pp-n dot">•</span>}
        </>
      ),
      content: (
        <>
          {lead}
          <SettingsTab ctx={ctx} form={form} readOnly={readOnly} />
        </>
      ),
    },
    {
      value: "servers",
      label: (
        <>
          {t("pf.pg.tab.servers")}
          {servers.length > 0 && <span className={cx("pp-n", bad.length > 0 ? "crit" : "mut")}>{bad.length > 0 ? bad.length : servers.length}</span>}
        </>
      ),
      content: (
        <>
          {lead}
          <ServersTab {...ctx} />
        </>
      ),
    },
    {
      value: "people",
      label: (
        <>
          {t("pf.pg.tab.people")}
          {profile.userCount > 0 && <span className="pp-n mut">{profile.userCount}</span>}
        </>
      ),
      content: (
        <>
          {lead}
          <PeopleTab {...ctx} />
        </>
      ),
    },
  ];

  const twinWord = egress === "warp" ? t("pf.pg.twin.direct") : t("pf.pg.twin.warp");
  return (
    <Wrap fx={fx}>
      {scene?.gone ? <Gone t={t} /> : null}
      <header className="nd-h up-h pp-h">
        <Mark k={kind} className="pp-g" />
        <div className="nd-ht">
          <div className="nd-h1">
            <h1>{profile.name}</h1>
            <span className={cx("pv-pill pp-pill", st.kind)}>
              <Gl k={st.gl} />
              {stateWord(st, t)}
            </span>
            {info.experimental && (
              <span className="pv-pill part">
                <Gl k="part" />
                {t("pf.pg.exp")}
              </span>
            )}
          </div>
          <div className="nd-meta">
            <span>{w.w}</span>
            <span>
              {t.n("pf.row.nodesCount", nodeCount)} · {t.n("pf.sum.people", profile.userCount)}
            </span>
          </div>
        </div>
        {owner && (
          <div className="nd-ha">
            {canPut && (
              <Button variant="primary" size="lg" onClick={() => setDlg("put")}>
                {t("pf.pg.put")}
              </Button>
            )}
            <Menu
              aria-label={t("pf.pg.more")}
              trigger={
                <Button size="lg" aria-label={t("pf.pg.more")} className="pp-more2">
                  <G d={D.dots} size={18} stroke={1.5} />
                </Button>
              }
            >
              {egress !== "" && (
                <MenuItem disabled={form.count > 0} title={form.count > 0 ? t("pf.pg.twin.dirty") : undefined} onClick={() => form.count === 0 && setDlg("twin")}>
                  {twinWord}
                </MenuItem>
              )}
              <MenuLink render={<Link to="/profiles" search={{ view: "where" }} />}>{t("pf.pg.showWhere")}</MenuLink>
              <MenuItem danger onClick={() => setDlg("del")}>
                {t("pf.pg.delete")}
              </MenuItem>
            </Menu>
          </div>
        )}
      </header>
      {verdict && (
        <div className={cx("pp-vd pp-vd2", verdict.tone)}>
          <span className="pp-vdi" aria-hidden>
            <Gl k={verdict.tone === "bad" ? "bad" : verdict.tone === "part" ? "part" : "none"} />
          </span>
          <span className="pp-vdt">
            <b>{verdict.title}</b>
            <em>{verdict.line}</em>
          </span>
          {verdict.more && (
            <button type="button" className="an-lk" onClick={() => goTab("servers")}>
              {verdict.more}
            </button>
          )}
          {verdict.btn && (
            <Button size="md" onClick={doVerdict}>
              {verdict.btn}
            </Button>
          )}
        </div>
      )}
      <Tabs className="pp-tabs" aria-label={t("pf.pg.tabs")} items={items} value={tab} onValueChange={(v) => goTab(v as ProfileTab)} />
      <PageDialogs ctx={ctx} dlg={dlg} onClose={() => setDlg("")} onGone={() => (onGone ? onGone() : undefined)} />
    </Wrap>
  );
}
