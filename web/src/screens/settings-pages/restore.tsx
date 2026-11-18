import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
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
import { BusyButton, CmdRow, ICONS, Pb, Sec, Spin } from "./sx-bits";
import { SxWindow } from "./sx-dlg";

// Settings -> Backups -> "Restore from a backup" (ADR 0013): the window that checks a backup and then hands it to the restore
// helper, the result of the last restore, the data kept from before each restore, and the way to restore on a new server.

type Source = "file" | "r2";
type KeyKind = "identity" | "passphrase";
type Step = "pick" | "busy" | "confirm" | "restarting" | "back" | "timeout";
type Summary = Plain<RestoreSummary>;

// The typed confirmation: the name as the summary shows it, whatever the case.
const sameName = (typed: string, name: string) => typed.trim().toLocaleLowerCase() === name.trim().toLocaleLowerCase() && name.trim() !== "";

// The installer's restore on a new server: a command, so it is the same in every language (and its pipe is no plural form).
export const installerRestore = (archive: string, identity: string) => `curl -fsSL https://mistgate.app/install.sh | sudo bash -s -- --restore ${archive} --identity-file ${identity}`;

const keptKey = ["backups", "kept"] as const;
const resultKey = ["backups", "restore-result"] as const;

/** "Recovery key" (the board's card of the aside): the command that makes it, and what to do with what it makes. */
export function KeygenCard({ identityFile }: { identityFile: string }) {
  const t = useT();
  return (
    <Sec tone="mint" d={ICONS.key} title={t("sx.bk.key")} lead={t("sx.bk.key.lead")}>
      <div className="sx-pad">
        <CmdRow value={t("set.backups.keygenCommand", { path: identityFile })} wrap />
      </div>
    </Sec>
  );
}

/** "On a new server" (the board's card of the aside): the installer's command, the manual one, and the master key's note. */
export function NewServerCard({ identityFile, archiveFile, dataDir }: { identityFile: string; archiveFile: string; dataDir: string }) {
  const t = useT();
  return (
    <Sec tone="sky" d={ICONS.terminal} title={t("set.backups.rs.new.title")} lead={t("set.backups.rs.new.body")} foot={t("set.backups.externalCredential")}>
      <div className="sx-pad">
        <CmdRow value={installerRestore(archiveFile, identityFile)} wrap />
      </div>
      <p className="sb-lead sx-l2">{t("set.backups.restoreHint")}</p>
      <div className="sx-pad">
        <CmdRow value={t("set.backups.restoreCommand", { identity: identityFile, archive: archiveFile, dataDir })} wrap />
      </div>
    </Sec>
  );
}

/** The card that starts a restore from a file or from R2, and under it the outcome of the last one and the data kept from before each. */
export function RestoreCard({ onStart }: { onStart: () => void }) {
  const t = useT();
  return (
    <Sec
      tone="rose"
      d={ICONS.refresh}
      title={t("set.backups.rs.title")}
      lead={t("set.backups.rs.body")}
      aside={
        <span className="ai-hb">
          <Button size="md" onClick={onStart}>
            {t("set.backups.rs.start")}
          </Button>
        </span>
      }
    >
      <LastResult />
      <KeptList />
    </Sec>
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
    <Pb tone={ok ? "ok" : "bad"} className="sx-last" title={t(ok ? "set.backups.rs.res.ok" : "set.backups.rs.res.fail")}>
      {ok ? t("set.backups.rs.res.okBody", { file, version: r.madeByVersion || "—", time: backupTime(Number(r.completedUnix), locale) }) : t("set.backups.rs.res.failBody", { file, time: backupTime(Number(r.completedUnix), locale) })}
    </Pb>
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
    <>
      <div className="sx-ks">
        <b>{t("set.backups.rs.kept.title")}</b>
        <span>{t("set.backups.rs.kept.body")}</span>
      </div>
      <div className="sx-kept">
        {list.map((d) => (
          <div key={d.name} className="sx-it">
            <span className="sx-nm">{d.name}</span>
            <span className="sx-sz">{formatBytes(Number(d.sizeBytes), locale)}</span>
            <span className="sx-sz">{backupTime(Number(d.modifiedUnix), locale)}</span>
            <button type="button" className="an-lk ig-lk" onClick={() => setAsking(d)}>
              {t("set.backups.rs.kept.delete")}
            </button>
          </div>
        ))}
      </div>
      {asking && (
        <SxWindow
          open
          onOpenChange={(v) => !v && !del.isPending && setAsking(null)}
          title={t("set.backups.rs.kept.confirmT")}
          shot="restore-kept-delete"
          footer={
            <>
              <Button size="lg" disabled={del.isPending} onClick={() => setAsking(null)}>
                {t("common.cancel")}
              </Button>
              <Button variant="primary" size="lg" className="sx-dng" disabled={del.isPending} onClick={() => del.mutate(asking.name)}>
                {t("set.backups.rs.kept.delete")}
              </Button>
            </>
          }
        >
          <p className="sx-dp">{t("set.backups.rs.kept.confirmBody", { time: backupTime(Number(asking.modifiedUnix), locale), size: formatBytes(Number(asking.sizeBytes), locale) })}</p>
        </SxWindow>
      )}
    </>
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
 * The window (the board's "Restore from a backup"). Pick: the backup (a file from this computer, or one of the R2 backups) and its key (the
 * recovery key file, or the file's passphrase). Check: the file goes up, the panel decrypts and verifies it and says what is in it, next to
 * what is here now; nothing changes yet. Confirm: the panel's name typed, then the helper swaps the data and the window waits for the panel
 * to come back (sign in again: the sessions are the backup's). The key is read in the browser and sent once.
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
      const res = await guard(() => backups.prepareRestore({ uploadId, r2ObjectKey: source === "r2" ? r2Key : "", identityFile, passphrase: keyKind === "passphrase" ? passphrase : "" }));
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
  if (step === "restarting") {
    body = (
      <div className="sx-rsw">
        <Spin />
        <b>{t("set.backups.rs.restarting.title")}</b>
        <span>{t("set.backups.rs.restarting.body")}</span>
      </div>
    );
    footer = null;
  } else if (step === "back" || step === "timeout") {
    const done = step === "back";
    body = (
      <div className="sx-end">
        <span className={cx("g", done ? "ok" : "warn")} aria-hidden>
          <Icon name={done ? "check" : "clock"} size={22} />
        </span>
        <b>{t(done ? "set.backups.rs.done.title" : "set.backups.rs.timeout.title")}</b>
        <p>{t(done ? "set.backups.rs.done.body" : "set.backups.rs.timeout.body")}</p>
      </div>
    );
    footer = (
      <Button variant="primary" size="lg" onClick={() => location.reload()}>
        {t(done ? "set.backups.rs.done.signin" : "set.backups.rs.timeout.reload")}
      </Button>
    );
  } else if ((step === "confirm" || (step === "busy" && prepared)) && s) {
    const rows: [MessageKey, number, number][] = [
      ["set.backups.rs.sum.nodes", Number(s.backupNodes), Number(s.currentNodes)],
      ["set.backups.rs.sum.users", Number(s.backupUsers), Number(s.currentUsers)],
      ["set.backups.rs.sum.profiles", Number(s.backupProfiles), Number(s.currentProfiles)],
    ];
    const busy = step === "busy";
    body = (
      <>
        <div className="sx-sum">
          <Icon name="shield" size={18} />
          <div>
            <b>{sourceName}</b>
            <span>{t("set.backups.rs.sum.made", { name, version: s.madeByVersion || "—", time: made })}</span>
          </div>
        </div>
        <table className="sx-cmp">
          <thead>
            <tr>
              <th />
              <th>{t("set.backups.rs.sum.backup")}</th>
              <th>{t("set.backups.rs.sum.now")}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map(([label, then, now]) => (
              <tr key={label}>
                <td>{t(label)}</td>
                <td>{then.toLocaleString(locale)}</td>
                <td className={cx(then === now && "same")}>{now.toLocaleString(locale)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <Pb tone="part" title={t("set.backups.rs.sum.warn", { time: made })}>
          {t("set.backups.rs.sum.kept")}
        </Pb>
        <label className="an-f">
          <span>{t("set.backups.rs.sum.type")}</span>
          <input className="an-i" value={typed} placeholder={name} autoComplete="off" spellCheck={false} onChange={(e) => setTyped(e.target.value)} />
        </label>
        {errorLine && <p className="sx-er">{errorLine}</p>}
      </>
    );
    footer = (
      <>
        <Button size="lg" disabled={busy} onClick={reset}>
          {t("set.backups.rs.back")}
        </Button>
        <BusyButton variant="primary" size="lg" className="sx-dng" busy={busy} disabled={!sameName(typed, name)} onClick={() => void restore()}>
          {t("set.backups.rs.do")}
        </BusyButton>
      </>
    );
  } else if (step === "busy") {
    // the board's check: a ring, "checking", and the promise that nothing changes yet
    body = (
      <>
        <div className="sx-rsw">
          <Spin />
          <b>{progress !== null ? t("set.backups.rs.uploading", { pct: Math.round(progress * 100) }) : t("sx.bk.rs.chk")}</b>
          <span>{t("sx.bk.rs.chk.body")}</span>
        </div>
      </>
    );
    footer = null;
  } else {
    const picked = source === "r2" ? r2.find((b) => b.key === r2Key) : undefined;
    body = (
      <>
        {initialR2 && picked && (
          <dl className="uq-dl ai-dl sx-dl sx-rsd">
            <div>
              <dt>{t("sx.bk.rs.file")}</dt>
              <dd>
                <b className="mono">{picked.key}</b>
              </dd>
            </div>
            <div>
              <dt>{t("sx.bk.rs.at")}</dt>
              <dd>
                <b className="mono">{backupTime(Number(picked.createdUnix), locale)}</b>
              </dd>
            </div>
            <div>
              <dt>{t("sx.bk.rs.size")}</dt>
              <dd>
                <b className="mono">{formatBytes(Number(picked.sizeBytes), locale)}</b>
              </dd>
            </div>
          </dl>
        )}
        {!initialR2 && (
          <fieldset className="sx-fs">
            <legend>{t("set.backups.rs.src")}</legend>
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
        )}
        <fieldset className="sx-fs">
          <legend>{t("set.backups.rs.key")}</legend>
          <Segmented
            aria-label={t("set.backups.rs.key")}
            value={keyKind}
            onValueChange={setKeyKind}
            options={[
              { value: "identity", label: t("set.backups.rs.key.identity") },
              { value: "passphrase", label: t("set.backups.rs.key.pass") },
            ]}
          />
          <p className="sx-dp">{t(keyKind === "identity" ? "set.backups.rs.key.identityBody" : "set.backups.rs.key.passBody")}</p>
          {keyKind === "identity" ? (
            <FilePick label={t("set.backups.rs.key.pick")} accept=".txt,.key,text/plain" file={identity} onFile={setIdentity} />
          ) : (
            <label className="an-f">
              <span>{t("set.backups.rs.key.passLabel")}</span>
              <input className="an-i" type="password" value={passphrase} autoComplete="off" onChange={(e) => setPassphrase(e.target.value)} />
            </label>
          )}
          <p className="sx-note">
            <Icon name="lock" size={12} />
            {t("set.backups.rs.key.note")}
          </p>
        </fieldset>
        {errorLine && <p className="sx-er">{errorLine}</p>}
      </>
    );
    footer = (
      <>
        <Button size="lg" onClick={() => close(false)}>
          {t("common.cancel")}
        </Button>
        <Button variant="primary" size="lg" disabled={!ready} onClick={() => void check()}>
          {t("set.backups.rs.check")}
        </Button>
      </>
    );
  }

  return (
    <SxWindow open={open} onOpenChange={close} title={t("set.backups.rs.dlg.title")} shot="restore" locked={step === "busy" || step === "restarting"} footer={footer}>
      {body}
    </SxWindow>
  );
}

function Choice({ checked, disabled, onPick, title, hint, children }: { checked: boolean; disabled?: boolean; onPick: () => void; title: string; hint: string; children?: ReactNode }) {
  return (
    <div className={cx("sx-ch", checked && "on", disabled && "dis")}>
      <label>
        <input type="radio" className="sr-only" checked={checked} disabled={disabled} onChange={onPick} />
        <span aria-hidden className="sx-rd" />
        <span>
          <b>{title}</b>
          <em>{hint}</em>
        </span>
      </label>
      {children && <div>{children}</div>}
    </div>
  );
}

function FilePick({ label, accept, file, fallback, onFile }: { label: string; accept: string; file: File | null; fallback?: string; onFile: (f: File | null) => void }) {
  const { locale } = useFmt();
  const input = useRef<HTMLInputElement>(null);
  const name = file?.name ?? fallback;
  return (
    <div className="sx-pick">
      <input ref={input} type="file" accept={accept} className="sr-only" tabIndex={-1} aria-hidden onChange={(e) => onFile(e.target.files?.[0] ?? null)} />
      <Button size="md" onClick={() => input.current?.click()}>
        <Icon name="upload" size={13} />
        {label}
      </Button>
      {name && (
        <span>
          {name}
          {file ? ` · ${formatBytes(file.size, locale)}` : ""}
        </span>
      )}
    </div>
  );
}
