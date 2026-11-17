import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type DragEvent } from "react";
import { halo } from "@/components/fx/halo";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { decoySites, nodes as nodesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { maxBulkNodes, maxBundleBytes } from "@/lib/decoy";
import { assignPreviewQuery, decoyErrorText, nodeServes, panelSourceOf, uploadError, type Node, type Site, type SiteCheck, type SiteVersion } from "@/lib/decoysites";
import { useFmt } from "@/lib/format";
import { dialogWidth } from "./create";
import { Arrow, DeliveryWord, DzIcon, Msg, onText, PanelGlyph, TargetMark } from "./parts";
import { Flag } from "@/components/ui/flag";
import type { Place } from "./site-side";

// The windows of a site's page: assign, take off, delete, replace the files, revert a version. Each one asks the panel for what it
// would do before it does it where the panel can say (the assignment), and says in words what a refusal means.

const keys = ["decoy-sites", "decoy-site", "nodes", "node"] as const;
function useRefresh() {
  const qc = useQueryClient();
  return () => keys.forEach((k) => void qc.invalidateQueries({ queryKey: [k] }));
}

export type AssignTarget = { kind: "site"; id: string; name: string } | { kind: "builtin" } | { kind: "dir"; available: boolean };
export type AssignDemo = { picks?: readonly string[]; q?: string; busy?: boolean };

/** What a node or the panel shows now, as the assign window lists it: the entry's words, or the name of the site. */
function nowOf(t: ReturnType<typeof useT>, serves: { kind: "builtin" | "dir" | "site"; siteId: string }, siteName: (id: string) => string) {
  if (serves.kind === "builtin") return { text: t("dz.now.builtin"), own: false };
  if (serves.kind === "dir") return { text: t("dz.now.dir"), own: false };
  return { text: siteName(serves.siteId), own: true };
}

/**
 * "Assign the site": the nodes (with a search) and the panel, ticked; under them the warnings of the site and what each pick
 * replaces. For a site of the library the panel itself answers what would be replaced (a dry run) before anything is changed;
 * the two built-in entries are put on nodes by their mode, and the summary is read from what the nodes show now.
 */
export function AssignDialog({
  target,
  nodes,
  panelSource,
  panelAddress,
  siteName,
  standsOn,
  onClose,
  demo,
}: {
  target: AssignTarget;
  nodes: readonly Node[];
  panelSource: string;
  /** The address the panel answers on, under its row; empty when the panel does not know it. */
  panelAddress?: string;
  siteName: (id: string) => string;
  /** The nodes (and the panel as "panel") the target already stands on: their rows are locked. */
  standsOn: ReadonlySet<string>;
  onClose: () => void;
  demo?: AssignDemo;
}) {
  const t = useT();
  const toast = useToast();
  const refresh = useRefresh();
  const [picked, setPicked] = useState<ReadonlySet<string>>(new Set(demo?.picks));
  const [q, setQ] = useState(demo?.q ?? "");
  const [problem, setProblem] = useState("");
  const free = nodes.filter((n) => !standsOn.has(n.id));
  const need = q.trim().toLowerCase();
  const shown = nodes.filter((n) => !need || `${n.name} ${n.address}`.toLowerCase().includes(need));
  const panelRow = !need || `${t("dz.pl.panel")} panel`.toLowerCase().includes(need);
  const picks = [...picked].filter((p) => !standsOn.has(p)).sort();
  const pickedNodes = picks.filter((p) => p !== "panel");
  const panel = picks.includes("panel");
  const allOn = free.length > 0 && free.slice(0, maxBulkNodes).every((n) => picked.has(n.id));
  const toggle = (id: string) => setPicked((s) => (s.has(id) ? new Set([...s].filter((x) => x !== id)) : new Set([...s, id])));

  const dry = useQuery({ ...assignPreviewQuery(target.kind === "site" ? target.id : "", picks), enabled: target.kind === "site" && picks.length > 0 });
  const nodeById = new Map(nodes.map((n) => [n.id, n]));
  const panelNow = panelSourceOf(panelSource);
  const nowFor = (id: string) => nowOf(t, id === "panel" ? panelNow : nodeServes(nodeById.get(id) ?? { decoyMode: "builtin", decoySiteId: "" }), siteName);
  const toName = target.kind === "site" ? target.name : t(target.kind === "builtin" ? "dz.now.builtin" : "dz.now.dir");
  const replaced = new Map((dry.data?.replaced ?? []).map((r) => [r.target, r]));
  const fromOf = (id: string) => {
    const r = replaced.get(id);
    if (!r) return nowFor(id);
    if (r.previous === "own" || r.previous === "site") return { text: r.previousSiteName || siteName(r.previousSiteId), own: true };
    return { text: t(r.previous === "builtin" ? "dz.now.builtin" : "dz.now.dir"), own: false };
  };

  const apply = useMutation({
    mutationFn: async () => {
      if (target.kind === "site") return void (await decoySites.assignDecoySite({ siteId: target.id, nodeIds: pickedNodes, panel }));
      const mode = target.kind === "builtin" ? "builtin" : "panel";
      if (pickedNodes.length) await nodesApi.setNodesDecoyMode({ nodeIds: pickedNodes, mode });
      if (panel) await decoySites.setPanelDecoySource({ source: target.kind === "builtin" ? "builtin" : "dir" });
    },
    onSuccess: () => {
      refresh();
      toast(t("dz.t.assigned"));
      onClose();
    },
    onError: (e) => setProblem(decoyErrorText(e, t, (id) => nodeById.get(id)?.name ?? id)),
  });
  const busy = apply.isPending || !!demo?.busy;

  const warnings = (dry.data?.warnings ?? []) as SiteCheck[];
  const shared = warnings.find((w) => w.code === "shared_domains");
  const others = warnings.filter((w) => w.code !== "shared_domains");
  const sumRows = picks.map((id) => ({ id, from: fromOf(id) }));

  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("dz.d.as")}
      closeLabel={t("common.close")}
      wide
      className={dialogWidth}
      shot="decoy-dialog-assign"
      footer={
        <>
          <span className="dz-foot">{picks.length ? t("dz.as.foot", { where: onText(t, pickedNodes.length, panel) }) : t("dz.as.footNone")}</span>
          <Button variant="secondary" size="lg" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="lg" disabled={picks.length === 0 || busy || (target.kind === "dir" && !target.available)} onClick={() => apply.mutate()}>
            {busy && <span aria-hidden className="dz-spin" />}
            {t("dz.act.assign")}
          </Button>
        </>
      }
    >
      <div className="dz-asb">
        <label className="dz-q">
          <Icon name="search" size={16} />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("dz.as.q")} aria-label={t("dz.as.qLabel")} autoComplete="off" />
        </label>
        <Button
          variant="secondary"
          size="md"
          onClick={() => setPicked(allOn ? new Set([...picked].filter((id) => standsOn.has(id) || !free.some((n) => n.id === id))) : new Set([...picked, ...free.slice(0, maxBulkNodes).map((n) => n.id)]))}
        >
          {allOn ? t("dz.as.clear") : t("dz.as.all")}
        </Button>
      </div>
      <div className="dz-asl" role="group" aria-label={t("dz.d.as")}>
        {shown.map((n) => {
          const has = standsOn.has(n.id);
          const on = has || picked.has(n.id);
          const now = nowFor(n.id);
          return (
            <button key={n.id} type="button" className={cx("dz-asr", has && "lock")} role="checkbox" aria-checked={on} aria-disabled={has || undefined} onClick={() => !has && toggle(n.id)}>
              <span className={cx("dz-cb2", on && "on")} aria-hidden>
                <i>
                  <Icon name="check" size={14} strokeWidth={2.5} />
                </i>
              </span>
              <span className="dz-pli" aria-hidden>
                <Flag code={n.countryCode} size={16} />
              </span>
              <div>
                <b className="dz-m">{n.name}</b>
                <span className="dz-m" dir="ltr">
                  {n.address}
                </span>
              </div>
              <em className={has ? "on" : ""}>
                {has ? t("dz.as.has") : t("dz.as.now")} {!has && <span dir="auto">{now.text}</span>}
              </em>
            </button>
          );
        })}
        {panelRow && (
          <>
            <span className="dz-wsep">{t("dz.as.panelSep")}</span>
            <button type="button" className={cx("dz-asr", standsOn.has("panel") && "lock")} role="checkbox" aria-checked={standsOn.has("panel") || picked.has("panel")} aria-disabled={standsOn.has("panel") || undefined} onClick={() => !standsOn.has("panel") && toggle("panel")}>
              <span className={cx("dz-cb2", (standsOn.has("panel") || picked.has("panel")) && "on")} aria-hidden>
                <i>
                  <Icon name="check" size={14} strokeWidth={2.5} />
                </i>
              </span>
              <span className="dz-pli" aria-hidden>
                <PanelGlyph />
              </span>
              <div>
                <b>{t("dz.pl.panel")}</b>
                {panelAddress && (
                  <span className="dz-m" dir="ltr">
                    {panelAddress}
                  </span>
                )}
              </div>
              <em>
                {standsOn.has("panel") ? t("dz.as.has") : t("dz.as.now")} {!standsOn.has("panel") && <span dir="auto">{nowFor("panel").text}</span>}
              </em>
            </button>
          </>
        )}
        {shown.length === 0 && !panelRow && <p className="dz-emp">{t("dz.as.nobody")}</p>}
      </div>
      {picks.length > 0 && dry.isPending && target.kind === "site" && <p className="dz-lc" role="status">{t("dz.as.checking")}</p>}
      {shared && (
        <Msg kind="part" title={t("dz.as.shared")}>
          <span>{t("dz.as.sharedText")}</span>
          {shared.paths.length > 0 && (
            <span className="dz-m" dir="ltr">
              {shared.paths.join(", ")}
            </span>
          )}
        </Msg>
      )}
      {others.map((w) => (
        <Msg key={w.code} kind="part" title={t(`dz.ck.${w.code}` as MessageKey)}>
          <span>{t("dz.as.warnText")}</span>
        </Msg>
      ))}
      {problem && (
        <Msg kind="bad" role="alert" title={problem} />
      )}
      {sumRows.length > 0 && (
        <div className="dz-sum">
          <span>{t("dz.as.sum")}</span>
          {sumRows.map((r) => (
            <div key={r.id}>
              <b className={r.id === "panel" ? "" : "dz-m"}>{r.id === "panel" ? t("dz.pl.panel") : (nodeById.get(r.id)?.name ?? r.id)}</b>
              <span dir={r.from.own ? "auto" : undefined}>{r.from.text}</span>
              <Arrow />
              <span dir="auto">{toName}</span>
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}

// ---- take off

export type UnassignTarget = { panel: true } | { node: Node };

/** "Take it off": the node (or the panel) gets the built-in page or the panel's directory instead; the person chooses which. */
export function UnassignDialog({ siteId, target, dirAvailable, onClose, demo }: { siteId: string; target: UnassignTarget; dirAvailable: boolean; onClose: () => void; demo?: { to?: "builtin" | "panel" } }) {
  const t = useT();
  const toast = useToast();
  const refresh = useRefresh();
  const [to, setTo] = useState<"builtin" | "panel">(demo?.to ?? "builtin");
  const [problem, setProblem] = useState("");
  const isPanel = "panel" in target;
  const go = useMutation({
    mutationFn: () => decoySites.unassignDecoySite({ siteId, nodeIds: isPanel ? [] : [target.node.id], panel: isPanel, fallback: to }),
    onSuccess: () => {
      refresh();
      toast(t("dz.t.unassigned"));
      onClose();
    },
    onError: (e) => setProblem(decoyErrorText(e, t)),
  });
  const options = [
    { value: "builtin" as const, title: t("dz.un.builtin"), text: t("dz.un.builtinText"), off: false },
    { value: "panel" as const, title: t("dz.un.dir"), text: t("dz.un.dirText"), off: !dirAvailable },
  ];
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={isPanel ? t("dz.d.unPanel") : t("dz.d.unNode", { name: target.node.name })}
      closeLabel={t("common.close")}
      wide
      className={dialogWidth}
      shot="decoy-dialog-unassign"
      footer={
        <>
          <Button variant="secondary" size="lg" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="lg" disabled={go.isPending} onClick={() => go.mutate()}>
            {go.isPending && <span aria-hidden className="dz-spin" />}
            {t("dz.un.go")}
          </Button>
        </>
      }
    >
      <p className="dz-p">{isPanel ? t("dz.un.panel") : t("dz.un.node", { name: target.node.name })}</p>
      <div className="dz-rad" role="radiogroup" aria-label={t("dz.un.pick")}>
        {options.map((o) => (
          <button key={o.value} type="button" className="dz-ro" role="radio" aria-checked={to === o.value} disabled={o.off} onClick={() => setTo(o.value)}>
            <span className="dz-rd" aria-hidden>
              <i />
            </span>
            <div>
              <b>{o.title}</b>
              <span>{o.text}</span>
            </div>
          </button>
        ))}
      </div>
      {problem && <Msg kind="bad" role="alert" title={problem} />}
    </Modal>
  );
}

// ---- delete

/** A draft or a site that stands nowhere is deleted after a question; a site that stands on nodes cannot be, and the window lists where. */
export function DeleteDialog({ site, places, onClose, onDone }: { site: Site; places: readonly Place[]; onClose: () => void; onDone: () => void }) {
  const t = useT();
  const toast = useToast();
  const refresh = useRefresh();
  const [problem, setProblem] = useState("");
  const used = places.length > 0;
  const del = useMutation({
    mutationFn: () => decoySites.deleteDecoySite({ siteId: site.id }),
    onSuccess: () => {
      refresh();
      toast(t("dz.t.deleted"));
      onDone();
    },
    onError: (e) => setProblem(decoyErrorText(e, t, (id) => places.find((p) => p.node?.id === id)?.name ?? id)),
  });
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={used ? t("dz.d.delUsed") : t(site.state === "draft" ? "dz.d.delDraft" : "dz.d.delSite")}
      closeLabel={t("common.close")}
      wide
      className={dialogWidth}
      shot="decoy-dialog-delete"
      footer={
        <>
          <Button variant="secondary" size="lg" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          {used ? (
            <Button variant="primary" size="lg" onClick={onClose}>
              {t("dz.del.first")}
            </Button>
          ) : (
            <Button variant="secondary" size="lg" className="dz-risk" disabled={del.isPending} onClick={() => del.mutate()}>
              {del.isPending && <span aria-hidden className="dz-spin" />}
              {t("dz.del.go")}
            </Button>
          )}
        </>
      }
    >
      {!used && <p className="dz-p">{t(site.state === "draft" ? "dz.del.draft" : "dz.del.site")}</p>}
      {used && (
        <>
          <Msg kind="part" title={t("dz.del.usedTitle")}>
            <span>{t("dz.del.usedText")}</span>
          </Msg>
          <div className="dz-asl">
            {places.map((p) => (
              <div key={p.key} className="dz-plr">
                <TargetMark node={p.node} panel={p.panel} />
                <div>
                  <b className={p.panel ? "" : "dz-m"}>{p.panel ? t("dz.pl.panel") : p.name}</b>
                  {p.node?.address && (
                    <span className="dz-m" dir="ltr">
                      {p.node.address}
                    </span>
                  )}
                </div>
                <DeliveryWord d={p.delivery} />
              </div>
            ))}
          </div>
        </>
      )}
      {problem && <Msg kind="bad" role="alert" title={problem} />}
    </Modal>
  );
}

// ---- replace the files, revert a version

/** "Replace the files": an archive becomes a new version; a site that stands on nodes sends them the new files at once, and the window says so. */
export function ReplaceDialog({ site, where, onClose, demo }: { site: Site; where: string; onClose: () => void; demo?: { file?: { name: string; size: number }; error?: { title: string; text: string } } }) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const refresh = useRefresh();
  const pick = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | undefined>();
  const [meta, setMeta] = useState(demo?.file);
  const [over, setOver] = useState(false);
  const [problem, setProblem] = useState<{ title: string; text: string } | undefined>(demo?.error);
  const take = (f: File | undefined) => {
    if (!f) return;
    setMeta({ name: f.name, size: f.size });
    if (f.size > maxBundleBytes) {
      setFile(undefined);
      return setProblem({ title: t("dz.er.decoy_bundle_too_large"), text: t("dz.er.decoy_bundle_too_large.s") });
    }
    setProblem(undefined);
    setFile(f);
  };
  const save = useMutation({
    mutationFn: async () => file && decoySites.addDecoySiteVersion({ siteId: site.id, bundleArchive: new Uint8Array(await file.arrayBuffer()) }),
    onSuccess: () => {
      refresh();
      halo("save");
      toast(t("dz.t.version"));
      onClose();
    },
    onError: (e) => setProblem(uploadError(e, t)),
  });
  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    setOver(false);
    take(e.dataTransfer.files[0]);
  };
  const ready = !!file && !problem;
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("dz.d.rep")}
      closeLabel={t("common.close")}
      wide
      className={dialogWidth}
      shot="decoy-dialog-replace"
      footer={
        <>
          <span className="dz-foot">{t("dz.rep.foot")}</span>
          <Button variant="secondary" size="lg" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="lg" disabled={!ready || save.isPending} onClick={() => save.mutate()}>
            {save.isPending && <span aria-hidden className="dz-spin" />}
            {t("dz.rep.go")}
          </Button>
        </>
      }
    >
      <input
        ref={pick}
        type="file"
        hidden
        aria-label={t("dz.d.rep")}
        accept=".zip,.tar,.tgz,.gz,application/zip,application/x-tar,application/gzip"
        onChange={(e) => {
          take(e.target.files?.[0]);
          e.target.value = "";
        }}
      />
      <button type="button" className={cx("dz-drop", over && "on")} onClick={() => pick.current?.click()} onDragOver={(e) => { e.preventDefault(); setOver(true); }} onDragLeave={() => setOver(false)} onDrop={onDrop}>
        <span className="g" aria-hidden>
          <DzIcon kind="upload" />
        </span>
        <b dir="auto" className={meta ? "dz-m" : undefined}>{meta ? meta.name : t("dz.drop.title")}</b>
        <span>{meta ? fmt.bytes(meta.size, 1024) : t("dz.drop.sub")}</span>
      </button>
      {problem && (
        <Msg kind="bad" role="alert" title={problem.title}>
          <span>{problem.text}</span>
        </Msg>
      )}
      {where && (
        <Msg kind="part" title={t("dz.rep.used", { where })}>
          <span>{t("dz.rep.usedText")}</span>
        </Msg>
      )}
    </Modal>
  );
}

/** "Revert the version": the site goes back to the files of an earlier version, saved as a new one; a site that stands on nodes sends them the old files. */
export function RevertDialog({ site, version, where, onClose }: { site: Site; version: SiteVersion; where: string; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const refresh = useRefresh();
  const [problem, setProblem] = useState("");
  const go = useMutation({
    mutationFn: () => decoySites.revertDecoySite({ siteId: site.id, seq: version.seq }),
    onSuccess: () => {
      refresh();
      toast(t("dz.t.reverted"));
      onClose();
    },
    onError: (e) => setProblem(decoyErrorText(e, t)),
  });
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("dz.d.ver")}
      closeLabel={t("common.close")}
      wide
      className={dialogWidth}
      shot="decoy-dialog-revert"
      footer={
        <>
          <Button variant="secondary" size="lg" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="lg" disabled={go.isPending} onClick={() => go.mutate()}>
            {go.isPending && <span aria-hidden className="dz-spin" />}
            {t("dz.ver.go")}
          </Button>
        </>
      }
    >
      <p className="dz-p">{t("dz.ver.text")}</p>
      {where && (
        <Msg kind="part" title={t("dz.ver.used", { where })}>
          <span>{t("dz.ver.usedText")}</span>
        </Msg>
      )}
      {problem && <Msg kind="bad" role="alert" title={problem} />}
    </Modal>
  );
}

