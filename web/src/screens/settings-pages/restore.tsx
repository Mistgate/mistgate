import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Banner, Notice } from "@/components/ui/notice";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import type { Backup, KeptData, RestoreSummary } from "@/gen/mistgate/admin/v1/backup_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { backups } from "@/lib/api";
import { uploadBackup, waitForRestart } from "@/lib/backup-restore";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { plain, type Plain } from "@/lib/plain";
import { backupTime, formatBytes } from "./backup-format";
import { CopyRow } from "./domains";

// Settings -> Backups -> "Restore from a backup" (ADR 0013): the window that checks a backup and then hands it to the restore
// helper, the result of the last restore, the data kept from before each restore, and the way to restore on a new server.

type Source = "file" | "r2";
type KeyKind = "identity" | "passphrase";
type Step = "pick" | "busy" | "confirm" | "restarting" | "back" | "timeout";
type Summary = Plain<RestoreSummary>;

// The typed confirmation: the name as the summary shows it, whatever the case (the field's label is upper-cased).
const sameName = (typed: string, name: string) => typed.trim().toLocaleLowerCase() === name.trim().toLocaleLowerCase() && name.trim() !== "";

// The installer's restore on a new server: a command, so it is the same in every language (and its pipe is no plural form).
const installerRestore = (archive: string, identity: string) => `curl -fsSL https://mistgate.app/install.sh | sudo bash -s -- --restore ${archive} --identity-file ${identity}`;

const keptKey = ["backups", "kept"] as const;
const resultKey = ["backups", "restore-result"] as const;

/** The section on Settings -> Backups. r2: the R2 backups listed above (empty when R2 is not set up); pick: one to start with. */
export function RestoreSection({ r2, pick, onPicked, identityFile, archiveFile, dataDir }: { r2: Plain<Backup>[]; pick?: string; onPicked?: () => void; identityFile: string; archiveFile: string; dataDir: string }) {
  const t = useT();
  const [manual, setManual] = useState(false);
  const open = manual || pick !== undefined; // a backup picked in the history opens the window with it
  return (
    <section className="flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4">
      <div className="flex flex-wrap items-center gap-3">
        <div className="flex min-w-[14rem] flex-1 flex-col gap-0.5">
          <SectionLabel as="h3" icon="shield" tone="mint">
            {t("set.backups.rs.title")}
          </SectionLabel>
          <p className="text-[13px] leading-normal text-pretty text-muted">{t("set.backups.rs.body")}</p>
        </div>
        <Button type="button" onClick={() => setManual(true)}>
          {t("set.backups.rs.start")}
        </Button>
      </div>
      <div className="flex flex-col gap-1.5">
        <p className="text-xs leading-normal text-pretty text-muted">{t("set.backups.rs.keygen")}</p>
        <CopyRow value={t("set.backups.keygenCommand", { path: identityFile })} />
      </div>
      <LastResult />
      <KeptList />
      <details className="group border-t border-line pt-3">
        <summary className="flex cursor-pointer list-none items-center gap-1.5 text-[13px] font-bold">
          <Icon name="chevronRight" size={14} className="transition-transform group-open:rotate-90 rtl:-scale-x-100" />
          {t("set.backups.rs.new.title")}
        </summary>
        <div className="mt-2.5 flex flex-col gap-2">
          <p className="text-xs leading-normal text-pretty text-muted">{t("set.backups.rs.new.body")}</p>
          <CopyRow value={installerRestore(archiveFile, identityFile)} />
          <p className="text-xs leading-normal text-pretty text-muted">{t("set.backups.restoreHint")}</p>
          <CopyRow value={t("set.backups.restoreCommand", { identity: identityFile, archive: archiveFile, dataDir })} />
          <p className="text-xs leading-normal text-pretty text-muted">{t("set.backups.externalCredential")}</p>
        </div>
      </details>
      <RestoreDialog
        key={pick ?? "manual"}
        open={open}
        onOpenChange={(v) => {
          setManual(v && pick === undefined);
          if (!v) onPicked?.();
        }}
        r2={r2}
        initialR2={pick}
      />
    </section>
  );
}

/** The outcome of the last restore, for a week after it: the panel reads it from the helper's result after it starts again. */
function LastResult() {
  const t = useT();
  const { locale } = useFmt();
  const q = useQuery({
    queryKey: resultKey,
    queryFn: async ({ signal }) => {
      const r = plain(await backups.getRestoreResult({}, { signal }));
      return { ...r, recent: Date.now() / 1000 - Number(r.completedUnix) < 7 * 86400 };
    },
    staleTime: 60_000,
  });
  const r = q.data;
  if (!r || !r.outcome || !r.recent) return null;
  const ok = r.outcome === "restored";
  const file = r.fileName || r.source;
  return (
    <Banner kind={ok ? "ok" : "bad"} title={t(ok ? "set.backups.rs.res.ok" : "set.backups.rs.res.fail")}>
      {ok
        ? t("set.backups.rs.res.okBody", { file, version: r.madeByVersion || "—", time: backupTime(Number(r.completedUnix), locale) })
        : t("set.backups.rs.res.failBody", { file, time: backupTime(Number(r.completedUnix), locale) })}
    </Banner>
  );
}

/** The data directories the panel ran on before each restore: kept until the owner deletes them. */
function KeptList() {
  const t = useT();
  const { locale } = useFmt();
  const toast = useToast();
  const guard = useStepUp();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: keptKey, queryFn: async ({ signal }) => plain(await backups.listKeptData({}, { signal })).directories });
  const [asking, setAsking] = useState<Plain<KeptData> | null>(null);
  const del = useMutation({
    mutationFn: (name: string) => guard(() => backups.deleteKeptData({ name })),
    onSuccess: () => {
      setAsking(null);
      toast(t("set.backups.rs.kept.deleted"));
      void qc.invalidateQueries({ queryKey: keptKey });
    },
    onError: (e) => {
      if (!isStepUpCancelled(e)) toast.error(errorText(e, t));
    },
  });
  const list = q.data ?? [];
  if (list.length === 0) return null;
  return (
    <div className="flex flex-col gap-2 border-t border-line pt-3">
      <div className="flex flex-col gap-0.5">
        <span className="text-[13px] font-bold">{t("set.backups.rs.kept.title")}</span>
        <span className="text-xs leading-snug text-pretty text-muted">{t("set.backups.rs.kept.body")}</span>
      </div>
      <div className="flex flex-col divide-y divide-line">
        {list.map((d) => (
          <div key={d.name} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2 first:pt-0 last:pb-0">
            <span className="min-w-0 flex-1 truncate font-mono text-xs">{d.name}</span>
            <span className="text-xs text-muted tabular-nums">{formatBytes(Number(d.sizeBytes), locale)}</span>
            <span className="text-xs text-muted">{backupTime(Number(d.modifiedUnix), locale)}</span>
            <Button type="button" size="sm" variant="ghostDanger" onClick={() => setAsking(d)}>
              {t("set.backups.rs.kept.delete")}
            </Button>
          </div>
        ))}
      </div>
      <Modal
        open={asking !== null}
        onOpenChange={(v) => !v && setAsking(null)}
        title={t("set.backups.rs.kept.confirmT")}
        description={asking ? t("set.backups.rs.kept.confirmBody", { time: backupTime(Number(asking.modifiedUnix), locale), size: formatBytes(Number(asking.sizeBytes), locale) }) : undefined}
        footer={
          <>
            <Button type="button" variant="secondary" onClick={() => setAsking(null)}>
              {t("common.cancel")}
            </Button>
            <Button type="button" variant="danger" disabled={del.isPending} onClick={() => asking && del.mutate(asking.name)}>
              {t("set.backups.rs.kept.delete")}
            </Button>
          </>
        }
      />
    </div>
  );
}

/** Which words a refusal of each call gets; the step decides what a code means (FAILED_PRECONDITION of the check is the key). */
function refusal(e: unknown, step: "upload" | "prepare" | "confirm"): MessageKey | null {
  const code = ConnectError.from(e).code;
  if (code === Code.Aborted) return "set.backups.rs.err.busy";
  if (code === Code.ResourceExhausted) return "set.backups.rs.err.size";
  if (step === "prepare") {
    if (code === Code.InvalidArgument) return "set.backups.rs.err.key";
    if (code === Code.FailedPrecondition) return "set.backups.rs.err.open";
    if (code === Code.NotFound) return "set.backups.rs.err.expired";
    if (code === Code.Unavailable) return "set.backups.rs.err.r2";
  }
  if (step === "confirm") {
    if (code === Code.NotFound) return "set.backups.rs.err.expired";
    if (code === Code.FailedPrecondition) return "set.backups.rs.err.helper";
  }
  return null;
}

export type RestoreDemo = { step?: Step; source?: Source; keyKind?: KeyKind; fileName?: string; progress?: number | null; summary?: Summary; typed?: string; error?: MessageKey };

/**
 * The window. Pick: the backup (a file from this computer, or one of the R2 backups) and its key (the recovery key file, or
 * the file's passphrase). Check: the file goes up, the panel decrypts and verifies it and says what is in it, next to what
 * is here now; nothing changes yet. Confirm: the panel's name typed, then the helper swaps the data and the window waits for
 * the panel to come back (sign in again: the sessions are the backup's). The key is read in the browser and sent once.
 */
export function RestoreDialog({ open, onOpenChange, r2, initialR2, demo }: { open: boolean; onOpenChange: (open: boolean) => void; r2: Plain<Backup>[]; initialR2?: string; demo?: RestoreDemo }) {
  const t = useT();
  const { locale } = useFmt();
  const guard = useStepUp();
  const qc = useQueryClient();
  const [step, setStep] = useState<Step>(demo?.step ?? "pick");
  const [source, setSource] = useState<Source>(demo?.source ?? (initialR2 ? "r2" : "file"));
  const [file, setFile] = useState<File | null>(null);
  const [r2Key, setR2Key] = useState(initialR2 ?? r2[0]?.key ?? "");
  const [keyKind, setKeyKind] = useState<KeyKind>(demo?.keyKind ?? "identity");
  const [identity, setIdentity] = useState<File | null>(null);
  const [passphrase, setPassphrase] = useState("");
  const [progress, setProgress] = useState<number | null>(demo?.progress ?? null);
  const [prepared, setPrepared] = useState<{ id: string; summary: Summary } | null>(demo?.summary ? { id: "demo", summary: demo.summary } : null);
  const [typed, setTyped] = useState(demo?.typed ?? "");
  const [error, setError] = useState<MessageKey | string | null>(demo?.error ?? null);
  const waiting = useRef<AbortController | null>(null);

  useEffect(() => () => waiting.current?.abort(), []);

  const reset = () => {
    setStep("pick");
    setProgress(null);
    setPrepared(null);
    setTyped("");
    setError(null);
    setPassphrase("");
  };
  const close = (v: boolean) => {
    if (step === "busy" || step === "restarting") return; // a running upload, check or restore is not abandoned by a click outside
    if (!v) reset();
    onOpenChange(v);
  };

  const sourceName = source === "file" ? (file?.name ?? demo?.fileName ?? "") : r2Key;
  const ready = (source === "file" ? file !== null : r2Key !== "") && (keyKind === "identity" ? identity !== null : passphrase.length > 0);

  async function check() {
    setError(null);
    setStep("busy");
    let at: "upload" | "prepare" = "upload";
    try {
      const identityFile = keyKind === "identity" && identity ? new Uint8Array(await identity.arrayBuffer()) : new Uint8Array();
      let uploadId = "";
      if (source === "file" && file) {
        setProgress(0);
        uploadId = await guard(() => uploadBackup(file, setProgress));
      }
      at = "prepare";
      setProgress(null);
      const res = await guard(() =>
        backups.prepareRestore({ uploadId, r2ObjectKey: source === "r2" ? r2Key : "", identityFile, passphrase: keyKind === "passphrase" ? passphrase : "" }),
      );
      setPassphrase("");
      setPrepared({ id: res.restoreId, summary: plain(res.summary!) });
      setStep("confirm");
    } catch (e) {
      setProgress(null);
      setStep("pick");
      if (!isStepUpCancelled(e)) setError(refusal(e, at) ?? errorText(e, t));
    }
  }

  async function restore() {
    if (!prepared) return;
    setError(null);
    setStep("busy");
    try {
      await guard(() => backups.confirmRestore({ restoreId: prepared.id, typedPanelName: typed.trim() }));
    } catch (e) {
      setStep("confirm");
      if (!isStepUpCancelled(e)) setError(refusal(e, "confirm") ?? errorText(e, t));
      return;
    }
    setStep("restarting");
    const ctl = new AbortController();
    waiting.current = ctl;
    const how = await waitForRestart(ctl.signal);
    if (ctl.signal.aborted) return;
    void qc.invalidateQueries({ queryKey: ["backups"] });
    setStep(how === "back" ? "back" : "timeout");
  }

  const s = prepared?.summary;
  const name = s?.panelName ?? "";
  const made = s ? backupTime(Number(s.createdUnix), locale) : "";
  const errorLine = error && (error.startsWith("set.backups.") ? t(error as MessageKey) : error);

  let body: ReactNode;
  let footer: ReactNode;
  if (step === "restarting" || step === "back" || step === "timeout") {
    const done = step === "back";
    body = (
      <div className="flex flex-col items-center gap-3 py-4 text-center">
        <span className={cx("grid size-12 place-items-center rounded-2xl", done ? "tone-ok tint" : step === "timeout" ? "tone-warn tint" : "bg-surface-2")}>
          {step === "restarting" ? <span aria-hidden className="size-5 animate-spin rounded-full border-2 border-faint border-t-accent" /> : <Icon name={done ? "check" : "clock"} size={22} />}
        </span>
        <b className="text-[15px]">{t(step === "restarting" ? "set.backups.rs.restarting.title" : done ? "set.backups.rs.done.title" : "set.backups.rs.timeout.title")}</b>
        <p className="max-w-[26rem] text-[13px] leading-normal text-pretty text-muted">
          {t(step === "restarting" ? "set.backups.rs.restarting.body" : done ? "set.backups.rs.done.body" : "set.backups.rs.timeout.body")}
        </p>
      </div>
    );
    footer =
      step === "restarting" ? null : (
        <Button type="button" variant="primary" onClick={() => location.reload()}>
          {t(done ? "set.backups.rs.done.signin" : "set.backups.rs.timeout.reload")}
        </Button>
      );
  } else if ((step === "confirm" || (step === "busy" && prepared)) && s) {
    const rows: [MessageKey, number, number][] = [
      ["set.backups.rs.sum.nodes", Number(s.backupNodes), Number(s.currentNodes)],
      ["set.backups.rs.sum.users", Number(s.backupUsers), Number(s.currentUsers)],
      ["set.backups.rs.sum.profiles", Number(s.backupProfiles), Number(s.currentProfiles)],
    ];
    body = (
      <div className="flex flex-col gap-3.5">
        <div className="flex items-start gap-3 rounded-field bg-surface-2 px-3.5 py-3">
          <Icon name="shield" size={18} className="mt-0.5 flex-none text-accent-text" />
          <div className="flex min-w-0 flex-col gap-0.5">
            <span className="text-[13px] font-bold break-all">{sourceName}</span>
            <span className="text-xs leading-snug text-muted">{t("set.backups.rs.sum.made", { name, version: s.madeByVersion || "—", time: made })}</span>
          </div>
        </div>
        <table className="w-full text-[13px]">
          <thead>
            <tr className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">
              <th className="pb-1.5 text-start font-bold" />
              <th className="pb-1.5 text-end font-bold">{t("set.backups.rs.sum.backup")}</th>
              <th className="pb-1.5 text-end font-bold">{t("set.backups.rs.sum.now")}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {rows.map(([label, then, now]) => (
              <tr key={label}>
                <td className="py-1.5">{t(label)}</td>
                <td className="py-1.5 text-end font-bold tabular-nums">{then.toLocaleString(locale)}</td>
                <td className={cx("py-1.5 text-end tabular-nums", then === now ? "text-muted" : "text-fg")}>{now.toLocaleString(locale)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <Notice tone="warn">
          <span className="flex flex-col gap-1">
            <span className="font-semibold">{t("set.backups.rs.sum.warn", { time: made })}</span>
            <span>{t("set.backups.rs.sum.kept")}</span>
          </span>
        </Notice>
        <TextField label={t("set.backups.rs.sum.type")} placeholder={name} value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} />
        {errorLine && <p className="text-[13px] text-danger-text">{errorLine}</p>}
      </div>
    );
    footer = (
      <>
        <Button type="button" variant="secondary" disabled={step === "busy"} onClick={reset}>
          {t("set.backups.rs.back")}
        </Button>
        <Button type="button" variant="danger" disabled={step === "busy" || !sameName(typed, name)} onClick={() => void restore()}>
          {t("set.backups.rs.do")}
        </Button>
      </>
    );
  } else {
    const busy = step === "busy";
    body = (
      <div className="flex flex-col gap-4">
        <fieldset className="flex flex-col gap-2" disabled={busy}>
          <legend className="mb-1.5 text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("set.backups.rs.src")}</legend>
          <Choice checked={source === "file"} onPick={() => setSource("file")} title={t("set.backups.rs.src.file")} hint={t("set.backups.rs.src.fileBody")}>
            {source === "file" && <FilePick label={t("set.backups.rs.src.pick")} accept=".age,application/octet-stream" file={file} fallback={demo?.fileName} onFile={setFile} />}
          </Choice>
          <Choice checked={source === "r2"} disabled={r2.length === 0} onPick={() => setSource("r2")} title={t("set.backups.rs.src.r2")} hint={r2.length === 0 ? t("set.backups.rs.src.r2None") : t("set.backups.rs.src.r2Body")}>
            {source === "r2" && r2.length > 0 && (
              <Select
                aria-label={t("set.backups.rs.src.r2")}
                value={r2Key}
                onValueChange={(v) => setR2Key(String(v))}
                options={r2.map((b) => ({ value: b.key, label: `${backupTime(Number(b.createdUnix), locale)} · ${formatBytes(Number(b.sizeBytes), locale)}` }))}
              />
            )}
          </Choice>
        </fieldset>
        <fieldset className="flex flex-col gap-2" disabled={busy}>
          <legend className="mb-1.5 text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("set.backups.rs.key")}</legend>
          <Segmented
            aria-label={t("set.backups.rs.key")}
            value={keyKind}
            onValueChange={setKeyKind}
            options={[
              { value: "identity", label: t("set.backups.rs.key.identity") },
              { value: "passphrase", label: t("set.backups.rs.key.pass") },
            ]}
          />
          <p className="text-xs leading-snug text-pretty text-muted">{t(keyKind === "identity" ? "set.backups.rs.key.identityBody" : "set.backups.rs.key.passBody")}</p>
          {keyKind === "identity" ? (
            <FilePick label={t("set.backups.rs.key.pick")} accept=".txt,.key,text/plain" file={identity} onFile={setIdentity} />
          ) : (
            <TextField type="password" label={t("set.backups.rs.key.passLabel")} value={passphrase} onChange={(e) => setPassphrase(e.target.value)} autoComplete="off" />
          )}
          <p className="flex items-center gap-1.5 text-xs text-muted">
            <Icon name="lock" size={12} className="flex-none" />
            {t("set.backups.rs.key.note")}
          </p>
        </fieldset>
        {errorLine && <p className="text-[13px] text-danger-text">{errorLine}</p>}
      </div>
    );
    footer = (
      <>
        <Button type="button" variant="secondary" disabled={busy} onClick={() => close(false)}>
          {t("common.cancel")}
        </Button>
        <Button type="button" variant="primary" disabled={busy || !ready} onClick={() => void check()}>
          {busy ? (progress !== null ? t("set.backups.rs.uploading", { pct: Math.round(progress * 100) }) : t("set.backups.rs.checking")) : t("set.backups.rs.check")}
        </Button>
      </>
    );
  }

  return (
    <Modal open={open} onOpenChange={close} title={t("set.backups.rs.dlg.title")} footer={footer}>
      {body}
    </Modal>
  );
}

function Choice({ checked, disabled, onPick, title, hint, children }: { checked: boolean; disabled?: boolean; onPick: () => void; title: string; hint: string; children?: ReactNode }) {
  return (
    <div className={cx("flex flex-col gap-2.5 rounded-field border p-3 transition-colors duration-200", checked ? "border-accent-line bg-accent-soft" : "border-line bg-surface", disabled && "opacity-60")}>
      <label className={cx("flex items-start gap-3", disabled ? "cursor-default" : "cursor-pointer")}>
        <input type="radio" className="peer sr-only" checked={checked} disabled={disabled} onChange={onPick} />
        <span aria-hidden className={cx("mt-0.5 grid size-4 flex-none place-items-center rounded-full border-[1.5px] peer-focus-visible:ring-2 peer-focus-visible:ring-accent", checked ? "border-accent" : "border-faint")}>
          {checked && <span className="size-2 rounded-full bg-accent" />}
        </span>
        <span className="flex min-w-0 flex-col gap-0.5">
          <span className="text-[13px] font-bold">{title}</span>
          <span className="text-xs leading-snug text-pretty text-muted">{hint}</span>
        </span>
      </label>
      {children && <div className="ps-7">{children}</div>}
    </div>
  );
}

function FilePick({ label, accept, file, fallback, onFile }: { label: string; accept: string; file: File | null; fallback?: string; onFile: (f: File | null) => void }) {
  const { locale } = useFmt();
  const input = useRef<HTMLInputElement>(null);
  const name = file?.name ?? fallback;
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2.5">
      <input ref={input} type="file" accept={accept} className="sr-only" tabIndex={-1} aria-hidden onChange={(e) => onFile(e.target.files?.[0] ?? null)} />
      <Button type="button" size="sm" variant="secondary" onClick={() => input.current?.click()}>
        <Icon name="upload" size={13} />
        {label}
      </Button>
      {name && (
        <span className="min-w-0 truncate text-xs text-muted">
          {name}
          {file ? ` · ${formatBytes(file.size, locale)}` : ""}
        </span>
      )}
    </div>
  );
}
