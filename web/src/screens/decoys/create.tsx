import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useRef, useState, type DragEvent } from "react";
import { CopyButton } from "@/components/copy-button";
import { halo } from "@/components/fx/halo";
import { markFresh } from "@/components/fx/registry";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { useT } from "@/i18n";
import { decoySites } from "@/lib/api";
import { cx } from "@/lib/cx";
import { maxBundleBytes } from "@/lib/decoy";
import { createTask, isBad, isDraft, maxNameLen, maxNoteLen, nameFromFile, sitesQuery, uploadError, uploadErrorFor, type Site } from "@/lib/decoysites";
import { useFmt } from "@/lib/format";
import { plain } from "@/lib/plain";
import { useNow } from "@/lib/time";
import { DzIcon, Field, Msg, useAssistant } from "./parts";

// The windows that make a site (the board's "New site", "Upload an archive" and "Task for an external agent"). One site can come
// from an archive, from the assistant's chat (which saves a draft by itself) or from an external agent over MCP (also a draft).

/** The window's width on a desktop: the board's dialogs share one class and stand at 920 (pn.css, .us-dlg.ug-dlg). */
export const dialogWidth = "md:max-w-[920px]! md:max-h-[820px]";

export type UploadDemo = {
  /** What the window shows: the picker, an upload that runs, a refusal, or the saved site. */
  phase: "pick" | "drag" | "run" | "err" | "res";
  file?: { name: string; size: number };
  pct?: number;
  error?: { code: string; path?: string };
  site?: Site;
};

/** The three ways to a site, as cards: used by the empty list and by the "New site" window. */
export function Ways({ one, onUpload, onAssistant, onMcp }: { one?: boolean; onUpload: () => void; onAssistant: () => void; onMcp: () => void }) {
  const t = useT();
  const assistant = useAssistant();
  const ways = [
    { kind: "upload" as const, title: t("dz.way.up"), text: t("dz.way.upText"), result: t("dz.way.upResult"), go: onUpload, off: false },
    { kind: "assistant" as const, title: t("dz.way.as"), text: t("dz.way.asText"), result: t("dz.way.asResult"), go: onAssistant, off: assistant.off },
    { kind: "mcp" as const, title: t("dz.way.mcp"), text: t("dz.way.mcpText"), result: t("dz.way.mcpResult"), go: onMcp, off: false },
  ];
  return (
    <div className={cx("dz-ways", one && "one")}>
      {ways.map((w) => (
        <button key={w.kind} type="button" className="dz-way" disabled={w.off} title={w.off ? t("dz.way.asOff") : undefined} onClick={w.go}>
          <span className="g" aria-hidden>
            <DzIcon kind={w.kind === "upload" ? "upload" : w.kind} />
          </span>
          <b>{w.title}</b>
          <span>{w.text}</span>
          <em>{w.result}</em>
        </button>
      ))}
    </div>
  );
}

/** "New site": the three ways, one under another. */
export function NewSiteDialog({ onClose, onUpload, onAssistant, onMcp }: { onClose: () => void; onUpload: () => void; onAssistant: () => void; onMcp: () => void }) {
  const t = useT();
  return (
    <Modal open onOpenChange={(o) => !o && onClose()} title={t("dz.d.new")} closeLabel={t("common.close")} wide className={dialogWidth} shot="decoy-dialog-new" footer={<span className="dz-foot">{t("dz.d.newFoot")}</span>}>
      <Ways one onUpload={onUpload} onAssistant={onAssistant} onMcp={onMcp} />
    </Modal>
  );
}

/** The text of a task for an external agent, ready to copy: the window both "Task for an agent" (a new site) and "Continue over MCP" use. */
export function TaskDialog({ title, text, task, foot, onClose, shot }: { title: string; text: string; task: string; foot: string; onClose: () => void; shot?: string }) {
  const t = useT();
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={title}
      closeLabel={t("common.close")}
      wide
      className={dialogWidth}
      shot={shot}
      footer={
        <>
          <span className="dz-foot">{foot}</span>
          <CopyButton value={task} label={t("dz.copy")} size="lg" variant="primary" />
        </>
      }
    >
      <p className="dz-p">{text}</p>
      <pre className="dz-task" dir="auto">
        {task}
      </pre>
    </Modal>
  );
}

/** The window of a new site's task: the rules, and the themes the library already holds. */
export function McpDialog({ onClose }: { onClose: () => void }) {
  const t = useT();
  const sites = useQuery(sitesQuery).data?.sites ?? [];
  return <TaskDialog title={t("dz.d.mcp")} text={t("dz.mcp.text")} task={createTask(t, sites)} foot={t("dz.mcp.foot")} onClose={onClose} shot="decoy-dialog-mcp" />;
}

/** The progress of a request that has none to report: it creeps toward 90 % while the panel works and stops there, the answer takes it to 100. */
function useCreep(running: boolean, startedAt: number) {
  const now = useNow(running ? 200 : 60_000);
  return running ? Math.round(12 + 78 * (1 - Math.exp(-Math.max(0, now - startedAt) / 1500))) : 100;
}

/**
 * "Upload an archive": drop or choose a file, it goes to the panel as a new site (ready at once if it passes the checks), then the
 * name and the note can be set and the site opened. Every refusal of the bundle rules reads in words. The panel takes only
 * what it can use, so a file over 2 MiB is turned back here, before it is sent.
 */
export function UploadDialog({ onClose, demo }: { onClose: () => void; demo?: UploadDemo }) {
  const t = useT();
  const fmt = useFmt();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const pick = useRef<HTMLInputElement>(null);
  const sites = useQuery(sitesQuery).data?.sites ?? [];
  const [phase, setPhase] = useState<UploadDemo["phase"]>(demo?.phase ?? "pick");
  const [file, setFile] = useState<UploadDemo["file"]>(demo?.file);
  const [error, setError] = useState(() => (demo?.error ? uploadErrorFor(t, demo.error.code, demo.error.path) : undefined));
  const [site, setSite] = useState<Site | undefined>(demo?.site);
  const [name, setName] = useState(demo?.site?.name ?? "");
  const [note, setNote] = useState(demo?.site?.note ?? "");
  const [problem, setProblem] = useState("");
  const running = phase === "run";
  const [startedAt, setStartedAt] = useState(0);
  const creep = useCreep(running, startedAt);
  const pct = demo?.pct ?? (phase === "run" ? creep : 100);

  const upload = useMutation({
    mutationFn: async (f: File) => plain(await decoySites.createDecoySite({ name: nameFromFile(f.name, sites.map((s) => s.name)), note: "", bundleArchive: new Uint8Array(await f.arrayBuffer()), state: "ready", source: "upload" })),
    onSuccess: (r) => {
      // its card in the list sweeps in once the window is gone
      if (r.site) markFresh("site", r.site.id);
      halo("create");
      setSite(r.site);
      setName(r.site?.name ?? "");
      setNote(r.site?.note ?? "");
      setPhase("res");
      void qc.invalidateQueries({ queryKey: sitesQuery.queryKey });
    },
    onError: (e) => {
      setError(uploadError(e, t));
      setPhase("err");
    },
  });

  function take(f: File | undefined) {
    if (!f) return;
    setFile({ name: f.name, size: f.size });
    setProblem("");
    if (f.size > maxBundleBytes) {
      // never sent: the panel would refuse it anyway, and the line says what to do
      setError(uploadErrorFor(t, "decoy_bundle_too_large"));
      setPhase("err");
      return;
    }
    setStartedAt(Date.now());
    setPhase("run");
    upload.mutate(f);
  }
  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    take(e.dataTransfer.files[0]);
  };
  const again = () => {
    setPhase("pick");
    setFile(undefined);
    setSite(undefined);
    setError(undefined);
    setProblem("");
  };

  const dirty = !!site && (name.trim() !== site.name || note.trim() !== site.note);
  const taken = !!site && sites.some((s) => s.id !== site.id && s.name.toLowerCase() === name.trim().toLowerCase());
  const noName = !name.trim();
  const save = useMutation({
    mutationFn: async () => {
      if (!site) return;
      if (dirty) await decoySites.updateDecoySite({ siteId: site.id, name: name.trim(), note: note.trim() });
      void qc.invalidateQueries({ queryKey: sitesQuery.queryKey });
      void qc.invalidateQueries({ queryKey: ["decoy-site", site.id] });
    },
    onSuccess: () => {
      if (site) void navigate({ to: "/decoys/$id", params: { id: site.id } });
    },
    onError: (e) => setProblem(uploadError(e, t).title),
  });

  const result = site ? (isBad(site) ? "failed" : isDraft(site) ? "draft" : "ready") : "ready";
  const resTone = result === "ready" ? "ok" : "part";
  const meta = site ? t.n("dz.up.meta", site.fileCount, { size: fmt.bytes(site.sizeBytes, 1024) }) : file ? fmt.bytes(file.size, 1024) : "";
  const fileInfo = phase === "run" ? t("dz.up.run", { n: pct }) : meta || (file ? fmt.bytes(file.size, 1024) : "");
  const foot =
    phase === "res" ? (taken ? t("dz.up.footTaken") : noName ? t("dz.up.footNeed") : problem || t("dz.up.footName")) : phase === "err" ? t("dz.up.footErr") : phase === "run" ? t("dz.up.footRun") : t("dz.up.footIdle");

  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("dz.d.up")}
      closeLabel={t("common.close")}
      wide
      className={dialogWidth}
      shot="decoy-dialog-upload"
      footer={
        <>
          <span className="dz-foot">{foot}</span>
          {(phase === "res" || phase === "err") && (
            <Button variant="secondary" size="lg" onClick={again}>
              {t("dz.up.again")}
            </Button>
          )}
          {phase === "res" && (
            <Button variant="primary" size="lg" aria-disabled={taken || noName || save.isPending || undefined} className="dz-off" onClick={() => !(taken || noName || save.isPending) && save.mutate()}>
              {save.isPending && <span aria-hidden className="dz-spin" />}
              {t("dz.up.open")}
            </Button>
          )}
        </>
      }
    >
      <input
        ref={pick}
        type="file"
        hidden
        aria-label={t("dz.d.up")}
        accept=".zip,.tar,.tgz,.gz,application/zip,application/x-tar,application/gzip"
        onChange={(e) => {
          take(e.target.files?.[0]);
          e.target.value = ""; // the same file can be chosen again after a refusal
        }}
      />
      {(phase === "pick" || phase === "drag") && (
        <>
          <button
            type="button"
            className={cx("dz-drop", phase === "drag" && "on")}
            onClick={() => pick.current?.click()}
            onDragOver={(e) => {
              e.preventDefault();
              if (phase !== "drag") setPhase("drag");
            }}
            onDragLeave={() => setPhase("pick")}
            onDrop={onDrop}
          >
            <span className="g" aria-hidden>
              <DzIcon kind="upload" />
            </span>
            <b>{phase === "drag" ? t("dz.drop.over") : t("dz.drop.title")}</b>
            <span>{t("dz.drop.sub")}</span>
          </button>
          <ul className="dz-lim">
            <li>
              <span className="dz-m">{t("dz.lim.sizeV")}</span>
              {t("dz.lim.size")}
            </li>
            <li>
              <span className="dz-m">4096</span>
              {t("dz.lim.files")}
            </li>
            <li>
              <span className="dz-m">index.html</span>
              {t("dz.lim.index")}
            </li>
          </ul>
        </>
      )}
      {(phase === "run" || phase === "err" || phase === "res") && file && (
        <div className={cx("dz-uf", phase === "err" ? "bad" : phase === "res" ? "ok" : "")} style={{ ["--w" as string]: `${pct}%` }}>
          <span className="g" aria-hidden>
            <Icon name="file" size={16} />
          </span>
          <div>
            <b className="dz-m" dir="auto">
              {file.name}
            </b>
            <span>{fileInfo}</span>
          </div>
          {running && <span aria-hidden className="dz-spin" />}
          <i className="dz-pg" aria-hidden />
        </div>
      )}
      {phase === "err" && error && (
        <Msg kind="bad" role="alert" title={error.title}>
          <span>{error.text}</span>
        </Msg>
      )}
      {phase === "res" && site && (
        <>
          <Msg kind={resTone} title={t(result === "ready" ? "dz.res.ready" : result === "failed" ? "dz.res.failed" : "dz.res.draft")}>
            <span>{t(result === "ready" ? "dz.res.readyText" : result === "failed" ? "dz.res.failedText" : "dz.res.draftText")}</span>
          </Msg>
          <Field label={t("dz.fld.name")} value={name} max={maxNameLen} onChange={setName} aria-label={t("dz.fld.name")} error={taken ? t("dz.nameTaken") : noName ? t("dz.nameNeed") : undefined} />
          <Field label={t("dz.fld.note")} value={note} max={maxNoteLen} onChange={setNote} placeholder={t("dz.fld.notePh")} hint={t("dz.fld.noteHint")} aria-label={t("dz.fld.note")} />
        </>
      )}
      <span className="sr-only" aria-live="polite">
        {phase === "run" ? t("dz.up.run", { n: pct }) : ""}
      </span>
    </Modal>
  );
}

