import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { ConnectError, Code } from "@connectrpc/connect";
import { useState } from "react";
import { halo } from "@/components/fx/halo";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Menu, MenuItem } from "@/components/ui/menu";
import { EmptyState } from "@/components/ui/notice";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { decoySites } from "@/lib/api";
import { cx } from "@/lib/cx";
import {
  builtinId,
  continueTask,
  copyName,
  decoyErrorText,
  deliveryOf,
  isBad,
  isBuiltinId,
  isDraft,
  linkKey,
  maxNameLen,
  maxNoteLen,
  nodeServes,
  panelSourceOf,
  saveBytes,
  siteQuery,
  sitesQuery,
  type Node,
  type SiteVersion,
} from "@/lib/decoysites";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { Skeleton } from "@/components/ui/query-error";
import { TaskDialog } from "./create";
import { FilesCard, type FilesDemo } from "./site-files";
import { PreviewCard, type PreviewDemo } from "./site-preview";
import { AssignDialog, DeleteDialog, ReplaceDialog, RevertDialog, UnassignDialog, type AssignDemo, type AssignTarget, type UnassignTarget } from "./site-dialogs";
import { ChecksCard, checkLines, FactsCard, PlacementCard, VersionsCard, type Place } from "./site-side";
import { asSource, Field, liveNodes, Msg, onText, DzIcon, sourceKey, useAssistant } from "./parts";
import "./decoys.css";

export type SiteDemo = {
  dialog?: "as" | "un" | "del" | "rep" | "ver" | "mcp";
  menu?: boolean;
  edit?: boolean;
  copied?: boolean;
  checksOpen?: boolean;
  unTarget?: string;
  verSeq?: number;
  preview?: PreviewDemo;
  files?: FilesDemo;
  assign?: AssignDemo;
  unTo?: "builtin" | "panel";
  repFile?: { name: string; size: number };
};

type Dlg = NonNullable<SiteDemo["dialog"]>;

/**
 * The page of one decoy site (the board's PnDecoy): the site in a frame with its pages, its files, where it stands, what its
 * checks say, its versions, and the actions of the table in the spec (rename, new version from an archive, copy, mark ready,
 * assign, take off, download, delete, continue with the assistant or over MCP). Only the owner sees it. "builtin" and
 * "panel_dir" are the two entries that are not sites: they can be looked at and put on nodes, nothing more.
 */
export function DecoySiteScreen({ id: idProp, demo }: { id?: string; demo?: SiteDemo } = {}) {
  const t = useT();
  const params = useParams({ strict: false }) as { id?: string };
  const id = idProp ?? params.id ?? "";
  const fixed = isBuiltinId(id) ? (id === builtinId ? "builtin" : "dir") : undefined;
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const { edge } = useEdition();
  const detail = useQuery({ ...siteQuery(id), enabled: owner && !edge && !fixed });
  const lib = useQuery({ ...sitesQuery, enabled: owner && !edge });
  const nodes = useQuery({ ...nodesQuery, enabled: owner });

  const back = (
    <Link to="/decoys" className="dz-back">
      <Icon name="back" size={16} />
      {t("nav.decoys")}
    </Link>
  );
  // the states that have no title of their own (no rights, loading, no such site) still have a heading for a reader
  const heading = <h1 className="sr-only">{t("nav.decoys")}</h1>;
  if (!owner) {
    return (
      <div className="dzs flex flex-col gap-4">
        {back}
        {heading}
        <section className="dz-card">
          <EmptyState icon={<Icon name="lock" size={20} />} title={t("dz.deny.title")}>
            {t("dz.deny.text")}
          </EmptyState>
        </section>
      </div>
    );
  }
  if (!fixed && (edge || (detail.isError && ConnectError.from(detail.error).code === Code.NotFound))) {
    return (
      <div className="dzs flex flex-col gap-4">
        {back}
        {heading}
        <section className="dz-card" role="status">
          <EmptyState
            icon={<Icon name="file" size={20} />}
            title={edge ? t("dz.edge.title") : t("dz.nf.title")}
            action={
              <Link to="/decoys" className="inline-flex h-10 items-center rounded-field border border-line bg-step px-4 text-sm font-medium hover:bg-(--step-hover)">
                {t("dz.nf.back")}
              </Link>
            }
          >
            {edge ? t("dz.edge.text") : t("dz.nf.text")}
          </EmptyState>
        </section>
      </div>
    );
  }
  if ((!fixed && detail.isError && !detail.data) || (!edge && lib.isError && !lib.data)) {
    return (
      <div className="dzs flex flex-col gap-4">
        {back}
        {heading}
        <section className="dz-card" role="alert">
          <EmptyState
            bad
            icon={<Icon name="warn" size={20} />}
            title={t("dz.err.title")}
            action={
              <Button variant="secondary" size="lg" onClick={() => void detail.refetch()}>
                {t("common.retry")}
              </Button>
            }
          >
            {t("dz.err.text")}
          </EmptyState>
        </section>
      </div>
    );
  }
  if ((!fixed && !detail.data) || (!edge && !lib.data) || !nodes.data) {
    return (
      <div className="dzs flex flex-col gap-4" aria-busy="true">
        {back}
        {heading}
        <Skeleton className="h-8 w-64 rounded-lg" />
        <Skeleton className="h-3 w-80" />
        <span className="sr-only" role="status">
          {t("dz.loading")}
        </span>
      </div>
    );
  }
  return <Loaded key={id} id={id} fixed={fixed} back={back} nodesAll={nodes.data.nodes} demo={demo} />;
}

function Loaded({ id, fixed, back, nodesAll, demo }: { id: string; fixed?: "builtin" | "dir"; back: React.ReactNode; nodesAll: readonly Node[]; demo?: SiteDemo }) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const assistant = useAssistant();
  const { edge } = useEdition();
  const detail = useQuery({ ...siteQuery(id), enabled: !fixed && !edge });
  const lib = useQuery({ ...sitesQuery, enabled: !edge });
  const live = liveNodes(nodesAll);
  const byId = new Map(nodesAll.map((n) => [n.id, n]));
  const [dlg, setDlg] = useState<Dlg | "">(demo?.dialog ?? "");
  const [edit, setEdit] = useState(!!demo?.edit);
  const [copied, setCopied] = useState(!!demo?.copied);
  const [unTarget, setUnTarget] = useState<UnassignTarget | undefined>(() => (demo?.dialog === "un" ? (demo.unTarget === "panel" ? { panel: true } : { node: byId.get(demo.unTarget ?? "") ?? live[0]! }) : undefined));
  const [version, setVersion] = useState<SiteVersion | undefined>(() => detail.data?.versions.find((v) => v.seq === demo?.verSeq));

  const site = detail.data?.site;
  const files = detail.data?.files ?? [];
  const library = lib.data;
  const panelSource = library?.panelSource ?? "builtin";
  const panel = panelSourceOf(panelSource);
  const dirItem = library?.builtin.find((b) => b.id === "panel_dir");
  const dirAvailable = dirItem?.available ?? false;
  const sitesById = new Map((library?.sites ?? []).map((s) => [s.id, s]));
  const siteName = (sid: string) => sitesById.get(sid)?.name ?? t("dz.now.noSite");

  // where it stands: the site's own list (with the delivery of each node), or the nodes that show the built-in entry
  const places: Place[] = [];
  if (fixed) {
    for (const n of live) {
      const s = nodeServes(n);
      if (s.kind === (fixed === "builtin" ? "builtin" : "dir")) places.push({ key: n.id, node: n, name: n.name, delivery: deliveryOf({ reasonCode: n.decoyReasonCode, supported: n.decoySupported }) });
    }
    if (panel.kind === (fixed === "builtin" ? "builtin" : "dir")) places.push({ key: "panel", panel: true, name: t("dz.pl.panel"), delivery: { kind: "ok", word: "dz.dl.ok" } });
  } else if (site) {
    for (const n of detail.data?.nodes ?? []) places.push({ key: n.nodeId, node: byId.get(n.nodeId), name: n.nodeName, delivery: deliveryOf({ reasonCode: n.reasonCode, supported: n.supported }) });
    if (site.onPanel) places.push({ key: "panel", panel: true, name: t("dz.pl.panel"), delivery: { kind: "ok", word: "dz.dl.ok" } });
  }
  const placeNodes = places.filter((p) => !p.panel).length;
  const placePanel = places.some((p) => p.panel);
  const used = places.length > 0;
  const where = used ? onText(t, placeNodes, placePanel) : "";

  const draft = !!site && isDraft(site);
  const bad = !!site && isBad(site);
  const invalidate = () => {
    for (const k of ["decoy-sites", "decoy-site", "nodes", "node"]) void qc.invalidateQueries({ queryKey: [k] });
  };

  // rename and the note
  const [name, setName] = useState(site?.name ?? "");
  const [note, setNote] = useState(site?.note ?? "");
  const others = (library?.sites ?? []).filter((s) => s.id !== id).map((s) => s.name.toLowerCase());
  const nameErr = !name.trim() ? t("dz.nameNeed") : others.includes(name.trim().toLowerCase()) ? t("dz.nameTaken") : "";
  const [problem, setProblem] = useState("");
  const rename = useMutation({
    mutationFn: () => decoySites.updateDecoySite({ siteId: id, name: name.trim(), note: note.trim() }),
    onSuccess: () => {
      setEdit(false);
      setProblem("");
      halo("save");
      toast(t("dz.t.saved"));
      invalidate();
    },
    onError: (e) => setProblem(decoyErrorText(e, t)),
  });
  const ready = useMutation({
    mutationFn: () => decoySites.setDecoySiteReady({ siteId: id, ready: true }),
    onSuccess: () => {
      toast(t("dz.t.ready"));
      invalidate();
    },
    onError: (e) => toast.error(decoyErrorText(e, t)),
  });
  const copy = useMutation({
    mutationFn: () => decoySites.copyDecoySite({ siteId: id, name: copyName(t, site?.name ?? "", (library?.sites ?? []).map((s) => s.name)) }),
    onSuccess: () => {
      setCopied(true);
      toast(t("dz.t.copied"));
      invalidate();
    },
    onError: (e) => toast.error(decoyErrorText(e, t)),
  });
  const zip = useMutation({
    mutationFn: async () => decoySites.downloadDecoySite({ siteId: id, seq: 0 }),
    onSuccess: (r) => saveBytes(r.filename || `${site?.name ?? "site"}.zip`, r.archive),
    onError: (e) => toast.error(decoyErrorText(e, t)),
  });

  const close = () => setDlg("");
  const standsOn = new Set(places.map((p) => (p.panel ? "panel" : p.key)));
  const title = fixed ? t(fixed === "builtin" ? "dz.bi.builtin" : "dz.bi.dir") : (site?.name ?? "");
  const noteText = fixed ? t(fixed === "builtin" ? "dz.bi.builtinNote" : "dz.bi.dirNote") : (site?.note ?? "");
  const src = site ? asSource(site.source) : "upload";
  const canAssign = !draft;
  const domains = [...new Set(places.filter((p) => p.node).map((p) => linkKey(p.node!.address, p.node!.id)))];
  const lines = site ? checkLines(t, fmt, site, domains.length > 1 ? [...new Set(places.filter((p) => p.node).map((p) => p.node!.address))] : []) : undefined;
  const banner = bad
    ? { kind: "bad" as const, title: t("dz.ban.bad"), text: lines?.required.some((l) => l.kind === "bad" && l.title === t("dz.ck.indexBad")) ? t("dz.ban.badText") : t("dz.ban.badOther") }
    : copied
      ? { kind: "" as const, title: t("dz.ban.copied"), text: t("dz.ban.copiedText") }
      : fixed === "builtin"
        ? { kind: "" as const, title: t("dz.ban.builtin"), text: t("dz.ban.builtinText") }
        : fixed === "dir" && !dirAvailable
          ? { kind: "part" as const, title: t("dz.ban.dirOff"), text: t(`err.${dirItem?.reasonCode || "panel_unavailable"}` as MessageKey) }
          : fixed === "dir"
            ? { kind: "" as const, title: t("dz.ban.dir"), text: t("dz.ban.dirText") }
          : draft
            ? { kind: "" as const, title: t("dz.ban.draft"), text: t("dz.ban.draftText") }
            : undefined;

  const assignTarget: AssignTarget = fixed === "builtin" ? { kind: "builtin" } : fixed === "dir" ? { kind: "dir", available: dirAvailable } : { kind: "site", id, name: site?.name ?? "" };
  const menu: { key: string; label: string; icon: string; risk?: boolean; go: () => void }[] = [
    { key: "zip", label: t("dz.mn.zip"), icon: "M12 4v11M7.5 10.5L12 15l4.5-4.5M5 19h14", go: () => zip.mutate() },
    { key: "rep", label: t("dz.mn.rep"), icon: "M12 16V5M7.5 9.5L12 5l4.5 4.5M5 19h14", go: () => setDlg("rep") },
    { key: "mcp", label: t("dz.mn.mcp"), icon: "M9 3v5M15 3v5M6 8h12v3a6 6 0 0 1-12 0zM12 17v4", go: () => setDlg("mcp") },
    { key: "copy", label: t("dz.mn.copy"), icon: "M9 9h11v11H9zM5 15H4V4h11v1", go: () => copy.mutate() },
    { key: "del", label: t("dz.mn.del"), icon: "M5 7h14M10 7V4h4v3M7 7l1 13h8l1-13", risk: true, go: () => setDlg("del") },
  ];

  const chip = (cls: string, text: string) => <span className={cx("inline-flex h-6 flex-none items-center rounded-chip bg-step px-2 text-xs text-sec", cls)}>{text}</span>;

  return (
    <div className="dzs flex flex-col gap-4" data-shot="decoy-site">
      {back}
      <header className="dz-h">
        <div className="dz-ht">
          {!edit ? (
            <>
              <div className="dz-h1">
                <h1 dir="auto">{title}</h1>
                {fixed ? chip("", t(fixed === "builtin" ? "dz.st.builtin" : "dz.st.dir")) : chip(draft ? "dz-dr" : "", t(draft ? "dz.st.draft" : "dz.st.ready"))}
                {!fixed && (
                  <IconButton
                    className="text-muted"
                    aria-label={t("dz.edit")}
                    title={t("dz.edit")}
                    onClick={() => {
                      setName(site?.name ?? "");
                      setNote(site?.note ?? "");
                      setProblem("");
                      setEdit(true);
                    }}
                  >
                    <svg aria-hidden width={16} height={16} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round">
                      <path d="M4 20h4L19 9l-4-4L4 16zM14 6l4 4" />
                    </svg>
                  </IconButton>
                )}
              </div>
              <div className="dz-meta">
                <span dir="auto" className={noteText ? "" : "none"}>
                  {noteText || t("dz.card.noNote")}
                </span>
                <span className="dz-src">
                  <DzIcon kind={fixed ? "folder" : src} />
                  {fixed ? t("dz.srcBuiltin") : t(sourceKey[src])}
                  {src === "mcp" && site?.sourceLabel && <span className="dz-m">{site.sourceLabel}</span>}
                </span>
                <span className={cx("dz-use", used && "on")}>{used ? t("dz.on.stands", { where }) : t("dz.use.none")}</span>
              </div>
            </>
          ) : (
            <div className="dz-ed">
              <h1 className="sr-only">{title}</h1>
              <Field label={t("dz.fld.name")} value={name} max={maxNameLen} onChange={setName} error={nameErr || undefined} aria-label={t("dz.fld.name")} />
              <Field label={t("dz.fld.note")} value={note} max={maxNoteLen} onChange={setNote} placeholder={t("dz.fld.notePh")} aria-label={t("dz.fld.note")} />
              {problem && <Msg kind="bad" role="alert" title={problem} />}
              <div className="dz-eda">
                <Button variant="primary" size="md" disabled={!!nameErr || rename.isPending} onClick={() => rename.mutate()}>
                  {t("dz.save")}
                </Button>
                <Button variant="secondary" size="md" onClick={() => setEdit(false)}>
                  {t("common.cancel")}
                </Button>
              </div>
            </div>
          )}
        </div>
        <div className="dz-ha">
          {!fixed && (
            <span title={assistant.off ? t("dz.way.asOff") : undefined} className="flex">
              <Button variant="secondary" size="lg" disabled={assistant.off} onClick={() => assistant.ask(t("dz.ask.continue", { name: site?.name ?? "", id }))}>
                <DzIcon kind="assistant" />
                {t("dz.act.ask")}
              </Button>
            </span>
          )}
          {draft && (
            <span title={bad ? t("dz.act.readyTip") : undefined} className="flex">
              <Button variant="primary" size="lg" disabled={bad || ready.isPending} onClick={() => ready.mutate()}>
                {ready.isPending && <span aria-hidden className="dz-spin" />}
                {t("dz.act.ready")}
              </Button>
            </span>
          )}
          {canAssign && (
            <Button variant="primary" size="lg" onClick={() => setDlg("as")}>
              {t("dz.act.assign")}
            </Button>
          )}
          {!fixed && (
            <Menu
              align="end"
              defaultOpen={demo?.menu}
              className="w-60"
              pop
              aria-label={t("dz.act.more")}
              trigger={
                <Button variant="secondary" size="lg" className="dz-mb gap-1.5 ps-3.5 pe-2.5">
                  {t("dz.act.more")}
                  <Icon name="chevronDown" size={16} />
                </Button>
              }
            >
              {menu.map((m) => (
                <MenuItem key={m.key} danger={m.risk} onClick={m.go}>
                  <svg aria-hidden width={16} height={16} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round" className="flex-none">
                    <path d={m.icon} />
                  </svg>
                  {m.label}
                </MenuItem>
              ))}
            </Menu>
          )}
        </div>
      </header>

      {banner && (
        <Msg kind={banner.kind} title={banner.title}>
          <span>{banner.text}</span>
        </Msg>
      )}

      <div className="dz-cols">
        <div className="dz-col">
          <PreviewCard id={id} sha={site?.bundleSha256 ?? ""} kind={fixed ?? "site"} bad={bad} demo={demo?.preview} />
          {site && files.length > 0 && <FilesCard site={site} files={files} demo={demo?.files} />}
        </div>
        <div className="dz-col">
          <PlacementCard
            places={places}
            draft={draft}
            onOff={
              fixed
                ? undefined
                : (p) => {
                    setUnTarget(p.panel ? { panel: true } : { node: p.node ?? ({ id: p.key, name: p.name } as Node) });
                    setDlg("un");
                  }
            }
          />
          {lines && <ChecksCard lines={lines} open={demo?.checksOpen} />}
          {site && detail.data && (
            <>
              <VersionsCard
                versions={detail.data.versions}
                max={library?.versionLimit || 5}
                onBack={(v) => {
                  setVersion(v);
                  setDlg("ver");
                }}
              />
              <FactsCard site={site} />
            </>
          )}
        </div>
      </div>

      {dlg === "as" && (
        <AssignDialog target={assignTarget} nodes={live} panelSource={panelSource} siteName={siteName} standsOn={standsOn} onClose={close} demo={demo?.assign} />
      )}
      {dlg === "un" && unTarget && site && <UnassignDialog siteId={id} target={unTarget} dirAvailable={dirAvailable} onClose={close} demo={{ to: demo?.unTo }} />}
      {dlg === "del" && site && <DeleteDialog site={site} places={places} onClose={close} onDone={() => void navigate({ to: "/decoys" })} />}
      {dlg === "rep" && site && <ReplaceDialog site={site} where={where} onClose={close} demo={{ file: demo?.repFile }} />}
      {dlg === "ver" && site && version && <RevertDialog site={site} version={version} where={where} onClose={close} />}
      {dlg === "mcp" && site && (
        <TaskDialog title={t("dz.d.cmcp")} text={t("dz.cmcp.text")} task={continueTask(t, site)} foot={t(used ? "dz.cmcp.footUsed" : "dz.cmcp.footFree")} onClose={close} shot="decoy-dialog-continue" />
      )}
    </div>
  );
}
