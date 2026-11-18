import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo, useState } from "react";
import { useFresh } from "@/components/fx/fresh";
import { PageTitle } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { EmptyState } from "@/components/ui/notice";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { builtinId, countOfFilter, deliveryOf, filterSites, isBad, isDraft, isUsed, nodeServes, panelDirId, panelFallbackKey, panelSourceOf, sitesQuery, siteFilters, warningsOf, type Node, type PanelState, type Site, type SiteFilter } from "@/lib/decoysites";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { errorText } from "@/lib/errors";
import { McpDialog, NewSiteDialog, UploadDialog, Ways, type UploadDemo } from "./create";
import { asSource, DzIcon, Gl, liveNodes, PanelGlyph, SiteLink, sourceKey, useAssistant, usageText, whenText } from "./parts";
import { BuiltinLogo, Thumb } from "./thumb";
import "./decoys.css";

/** What the dev kit starts the screen with (a window, a filter, a search); the app starts with none. */
export type DecoysDemo = { dialog?: "new" | "up" | "mcp"; upload?: UploadDemo; filter?: SiteFilter; q?: string };

/**
 * The Decoy sites page (the board's PnDecoys): what each node and the panel show now, the two built-in entries, and the own sites
 * as cards with a live miniature, with filters and a search. Only the owner sees it; the Cloudflare edition keeps no sites and
 * only explains that; the three ways to a site (an archive, the assistant, an agent over MCP) open from here.
 */
export function DecoysScreen({ demo }: { demo?: DecoysDemo } = {}) {
  const t = useT();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const { edge } = useEdition();
  const lib = useQuery({ ...sitesQuery, enabled: owner && !edge });
  const nodes = useQuery({ ...nodesQuery, enabled: owner });
  const [dialog, setDialog] = useState<"" | "new" | "up" | "mcp">(demo?.dialog ?? "");
  const assistant = useAssistant();
  const ready = lib.data !== undefined || edge;
  const sites = useMemo(() => lib.data?.sites ?? [], [lib.data]);
  const limit = lib.data?.siteLimit || 100;
  const full = sites.length >= limit;
  const canNew = owner && !edge && lib.isSuccess;
  const close = () => setDialog("");
  const askNew = () => {
    close();
    assistant.ask(t("dz.ask.create"));
  };

  return (
    <div className="dzp flex flex-col gap-4">
      <div className="mb-1 flex items-center gap-3">
        <PageTitle>{t("nav.decoys")}</PageTitle>
        {canNew && (
          <span title={full ? t("dz.newFull", { n: limit }) : undefined} className="ms-auto flex">
            <Button variant="primary" size="lg" disabled={full} onClick={() => setDialog("new")}>
              <Icon name="plus" size={16} />
              {t("dz.new")}
            </Button>
          </span>
        )}
      </div>
      <p className="dz-lead">{t("dz.lead")}</p>

      {!owner ? (
        <section className="dz-card">
          <EmptyState icon={<Icon name="lock" size={20} />} title={t("dz.deny.title")}>
            {t("dz.deny.text")}
          </EmptyState>
        </section>
      ) : edge ? (
        <>
          <section className="dz-card dz-edge">
            <span className="inline-flex h-6 flex-none items-center rounded-chip bg-step px-2 text-xs text-sec">Edge</span>
            <div>
              <b>{t("dz.edge.title")}</b>
              <span>{t("dz.edge.text")}</span>
            </div>
          </section>
          <Showing nodes={liveNodes(nodes.data?.nodes)} sites={[]} panelSource="builtin" ready={nodes.isSuccess} />
          <Builtins edge dirAvailable={false} dirReason="" />
        </>
      ) : lib.isError && !lib.data ? (
        <section className="dz-card" role="alert">
          <EmptyState
            bad
            icon={<Icon name="warn" size={20} />}
            title={t("dz.err.title")}
            action={
              <Button variant="secondary" size="lg" onClick={() => void lib.refetch()}>
                {t("common.retry")}
              </Button>
            }
          >
            {t("dz.err.text")}
            <span className="sr-only"> {errorText(lib.error, t)}</span>
          </EmptyState>
        </section>
      ) : !ready || !lib.data ? (
        <div className="dz-grid" aria-busy="true" aria-label={t("dz.loading")}>
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <div key={i} className="dz-card dz-c dz-sk">
              <i className="dz-th" />
              <div className="dz-cb">
                <i />
                <i />
                <i />
              </div>
            </div>
          ))}
        </div>
      ) : (
        <>
          <Showing nodes={liveNodes(nodes.data?.nodes)} sites={sites} panelSource={lib.data.panelSource} panel={lib.data.panel} ready={nodes.isSuccess} />
          <Builtins dirAvailable={lib.data.builtin.find((b) => b.id === "panel_dir")?.available ?? false} dirReason={lib.data.builtin.find((b) => b.id === "panel_dir")?.reasonCode ?? ""} />
          <Mine sites={sites} nodes={liveNodes(nodes.data?.nodes)} limit={limit} demo={demo} onWay={{ up: () => setDialog("up"), as: askNew, mcp: () => setDialog("mcp") }} />
        </>
      )}

      {dialog === "new" && <NewSiteDialog onClose={close} onUpload={() => setDialog("up")} onAssistant={askNew} onMcp={() => setDialog("mcp")} />}
      {dialog === "up" && <UploadDialog onClose={close} demo={demo?.upload} />}
      {dialog === "mcp" && <McpDialog onClose={close} />}
    </div>
  );
}

// ---- showing now

/**
 * What each node and the panel show now: the site, the built-in page or the panel's directory, with the delivery where it is not done.
 * The panel says itself what it shows: a chosen site that is gone or is a draft is not shown, and the card says what stands instead.
 */
export function Showing({ nodes, sites, panelSource, panel: panelState, ready }: { nodes: readonly Node[]; sites: readonly Site[]; panelSource: string; panel?: PanelState; ready: boolean }) {
  const t = useT();
  const byId = new Map(sites.map((s) => [s.id, s]));
  const panel = panelSourceOf(panelState?.serving || panelSource);
  const fallback = panelFallbackKey(panelState);
  const cells = [
    ...nodes.map((n) => ({ key: n.id, node: n, serves: nodeServes(n) })),
    { key: "panel", node: undefined, serves: { kind: panel.kind, siteId: panel.siteId } },
  ];
  const own = cells.some((c) => c.serves.kind === "site" && byId.has(c.serves.siteId));
  const sum = nodes.length > 0 ? t(own ? "dz.now.sum" : "dz.now.sumBi", { nodes: t.n("dz.now.nodes", nodes.length) }) : "";
  if (!ready) return null;
  return (
    <>
      <div className="dz-sec">
        <h2>{t("dz.now")}</h2>
        <span>{sum}</span>
      </div>
      <section className="dz-card dz-now" data-shot="decoy-now">
        {cells.map((c) => {
          const site = c.serves.kind === "site" ? byId.get(c.serves.siteId) : undefined;
          const d = c.node ? deliveryOf({ reasonCode: c.node.decoyReasonCode, supported: c.node.decoySupported }) : undefined;
          const to = site ? site.id : c.serves.kind === "dir" ? panelDirId : builtinId;
          const fb = c.node ? "" : fallback;
          return (
            <SiteLink key={c.key} id={to} className="dz-nw">
              <span className="dz-nwh">
                {c.node ? <Flag code={c.node.countryCode} size={16} /> : <PanelGlyph />}
                <span className={c.node ? "dz-m" : ""}>{c.node ? c.node.name : t("dz.now.panel")}</span>
                {d && d.kind !== "ok" && (
                  <span title={t(d.word)} className="ms-auto flex">
                    <Gl kind={d.kind} />
                    <span className="sr-only">{t(d.word)}</span>
                  </span>
                )}
              </span>
              {site ? (
                <>
                  <b dir="auto">{site.name}</b>
                  <em dir="auto">{site.note}</em>
                </>
              ) : c.serves.kind === "dir" ? (
                <>
                  <b>{t("dz.now.dir")}</b>
                  {fb ? (
                    <em className="w">
                      <Gl kind="part" />
                      {t(fb)}
                    </em>
                  ) : (
                    <em>{t("dz.now.dirSub")}</em>
                  )}
                </>
              ) : c.serves.kind === "site" ? (
                <>
                  <b>{t("dz.now.builtin")}</b>
                  <em className="w">
                    <Gl kind="bad" />
                    {t("dz.now.noSite")}
                  </em>
                </>
              ) : (
                <>
                  <b>{t("dz.now.builtin")}</b>
                  <em className="w">
                    <Gl kind="part" />
                    {fb ? t(fb) : t("dz.now.builtinSub")}
                  </em>
                </>
              )}
            </SiteLink>
          );
        })}
      </section>
    </>
  );
}

// ---- the built-in entries

/** The two entries that are not sites: the Mistgate page, and the panel's --decoy-dir with its state. */
export function Builtins({ edge, dirAvailable, dirReason }: { edge?: boolean; dirAvailable: boolean; dirReason: string }) {
  const t = useT();
  return (
    <>
      <div className="dz-sec">
        <h2>{t("dz.bi")}</h2>
        <span>{t("dz.bi.sub")}</span>
      </div>
      <div className="dz-bi">
        <SiteLink id={builtinId} className="dz-card dz-b">
          <span className="dz-bt" aria-hidden>
            <BuiltinLogo />
          </span>
          <div className="dz-bx">
            <b>{t("dz.bi.builtin")}</b>
            <span>{t("dz.bi.builtinText")}</span>
          </div>
          <Icon name="chevronRight" size={16} className="dz-go" />
        </SiteLink>
        {!edge && (
          <SiteLink id={panelDirId} className="dz-card dz-b">
            <span className="dz-bt" aria-hidden>
              <DzIcon kind="folder" size={20} />
            </span>
            <div className="dz-bx">
              <b>{t("dz.bi.dir")}</b>
              <span>{t("dz.bi.dirText")}</span>
              {!dirAvailable && (
                <span className="bad">
                  <Gl kind="part" />
                  {dirReason ? t(`err.${dirReason}` as MessageKey) : t("err.panel_unavailable")}
                </span>
              )}
            </div>
            <Icon name="chevronRight" size={16} className="dz-go" />
          </SiteLink>
        )}
      </div>
    </>
  );
}

// ---- own sites

function Mine({ sites, nodes, limit, demo, onWay }: { sites: readonly Site[]; nodes: readonly Node[]; limit: number; demo?: DecoysDemo; onWay: { up: () => void; as: () => void; mcp: () => void } }) {
  const t = useT();
  const [filter, setFilter] = useState<SiteFilter>(demo?.filter ?? "all");
  const [q, setQ] = useState(demo?.q ?? "");
  const shown = filterSites(sites, filter, q);
  const filterWord: Record<SiteFilter, string> = { all: t("dz.f.all"), draft: t("dz.f.draft"), used: t("dz.f.used"), free: t("dz.f.free") };
  return (
    <>
      <div className="dz-sec">
        <h2>{t("dz.mine")}</h2>
        <span>
          <span className="dz-m">{sites.length}</span> {t("dz.of", { max: limit })}
        </span>
      </div>
      {sites.length === 0 ? (
        <section className="dz-card dz-first">
          <div>
            <h2>{t("dz.first.title")}</h2>
            <p>{t("dz.first.text")}</p>
          </div>
          <Ways onUpload={onWay.up} onAssistant={onWay.as} onMcp={onWay.mcp} />
        </section>
      ) : (
        <>
          <div className="dz-bar">
            <div className="dz-ch" role="group" aria-label={t("dz.filter")}>
              {siteFilters.map((f) => (
                <button key={f} type="button" aria-pressed={filter === f} onClick={() => setFilter(f)}>
                  {filterWord[f]}
                  <span className="dz-m">{countOfFilter(sites, f)}</span>
                </button>
              ))}
            </div>
            <label className="dz-q">
              <Icon name="search" size={16} />
              <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("dz.q")} aria-label={t("dz.qLabel")} autoComplete="off" />
            </label>
          </div>
          {shown.length === 0 && (
            <section className="dz-card">
              <EmptyState
                title={t("dz.none.title")}
                action={
                  <Button
                    variant="secondary"
                    size="lg"
                    onClick={() => {
                      setFilter("all");
                      setQ("");
                    }}
                  >
                    {t("dz.none.reset")}
                  </Button>
                }
              >
                {q.trim() ? t("dz.none.search") : t("dz.none.empty")}
              </EmptyState>
            </section>
          )}
          <div className="dz-grid">
            {shown.map((s) => (
              <SiteCard key={s.id} site={s} nodes={nodes} />
            ))}
          </div>
        </>
      )}
    </>
  );
}

/** One site: the miniature, the name and the note, who made it and how big, where it stands, and what its checks said. */
export function SiteCard({ site, nodes }: { site: Site; nodes: readonly Node[] }) {
  const t = useT();
  const fmt = useFmt();
  const ref = useFresh<HTMLAnchorElement>("site", site.id);
  const flags = [...new Set(nodes.filter((n) => n.decoySiteId === site.id && n.decoyMode === "own").map((n) => n.countryCode))].filter(Boolean);
  const used = isUsed(site);
  const warns = warningsOf(site.checks).length;
  const src = asSource(site.source);
  const bad = isBad(site);
  return (
    <Link ref={ref} to="/decoys/$id" params={{ id: site.id }} className="dz-card dz-c" data-draft={isDraft(site) || undefined}>
      <Thumb site={site} />
      <div className="dz-cb">
        <b className="dz-cn" dir="auto" title={site.name}>
          {site.name}
        </b>
        <span className="dz-nt" dir="auto">
          {site.note || t("dz.card.noNote")}
        </span>
        <div className="dz-ml">
          <span className="dz-src">
            <DzIcon kind={src} size={14} />
            {t(sourceKey[src])}
            {src === "mcp" && site.sourceLabel && <span className="dz-m">{site.sourceLabel}</span>}
          </span>
          <span className="dz-m">{fmt.bytes(site.sizeBytes, 1024)}</span>
          <span>{whenText(fmt, t, site.updatedUnix)}</span>
        </div>
        <div className="dz-cf">
          <span className={cx("dz-use", used && "on")}>
            <span className="dz-fls" aria-hidden>
              {flags.map((c) => (
                <Flag key={c} code={c} size={12} />
              ))}
            </span>
            <UseLine site={site} />
          </span>
          {(bad || warns > 0) && (
            <span className="dz-w">
              <Gl kind={bad ? "bad" : "part"} />
              {bad ? t("dz.card.failed") : t.n("dz.card.warns", warns)}
            </span>
          )}
        </div>
      </div>
    </Link>
  );
}

function UseLine({ site }: { site: Site }) {
  const t = useT();
  return <>{usageText(t, site.nodeCount, site.onPanel)}</>;
}
