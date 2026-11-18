import { ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { CopyButton } from "@/components/copy-button";
import { EditionNotice } from "@/components/edition-notice";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { Pagination } from "@/components/ui/pagination";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { Backup, BackupSettings } from "@/gen/mistgate/admin/v1/backup_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { backups } from "@/lib/api";
import { downloadBackup } from "@/lib/backup-download";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { clampPage, usePaging } from "@/lib/paging";
import { plain, type Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { G } from "@/screens/users/user-bits";
import { backupTime, formatBytes } from "./backup-format";
import { KeygenCard, NewServerCard, RestoreCard, RestoreDialog, type RestoreDemo } from "./restore";
import { BusyButton, Foot, ICONS, Pb, Sec, Swr, SxGate } from "./sx-bits";
import { SxWindow, useFreshKeys } from "./sx-dlg";

type Stored = Plain<BackupSettings>;
type BackupItem = Plain<Backup>;
type Draft = {
  accountId: string;
  jurisdiction: string;
  bucket: string;
  accessKeyId: string;
  ageRecipient: string;
  enabled: boolean;
  intervalHours: number;
  retentionDays: number;
  secretAccessKey: string;
};

export const settingsKey = ["backup-settings"] as const;
export const backupsKey = ["backup-list"] as const;
const historySize = 10;
const identityFile = "./mistgate-recovery.txt";
const archiveFile = "./backup.tar.gz.age";
const dataDir = "/var/lib/mistgate-restored";

function draftFrom(stored: Stored): Draft {
  return {
    accountId: stored.accountId,
    jurisdiction: stored.jurisdiction || "default",
    bucket: stored.bucket,
    accessKeyId: stored.accessKeyId,
    ageRecipient: stored.ageRecipient,
    enabled: stored.enabled,
    intervalHours: stored.intervalHours || 24,
    retentionDays: stored.retentionDays,
    secretAccessKey: "",
  };
}

function hasConfiguration(stored?: Stored) {
  return !!stored?.accountId && !!stored.bucket && !!stored.accessKeyId && !!stored.ageRecipient && stored.hasSecret;
}

const codeKeys: Record<string, MessageKey> = {
  backup_not_configured: "set.backups.error.notConfigured",
  backup_settings_invalid: "set.backups.error.invalid",
  backup_storage_failed: "set.backups.error.storage",
  backup_storage_delete_failed: "set.backups.error.delete",
  backup_create_failed: "set.backups.error.create",
  backup_already_running: "set.backups.error.busy",
};

function backupErrorKey(error: unknown): MessageKey | null {
  return codeKeys[ConnectError.from(error).rawMessage] ?? null;
}

/** What a scene says about the page: what is going on (a backup being made, a check of R2, a download), and the windows that are open. */
export type BackupsDemo = {
  r2?: "busy" | "ok" | "bad";
  running?: boolean;
  download?: "busy";
  windows?: "secret" | "pass";
  fresh?: string;
  secret?: boolean;
  restore?: RestoreDemo & { pick?: string; manual?: boolean };
};

/** Settings -> Backups (the board's sxIs_bk, owner only): the state of the backups, encrypted R2 backups, the recovery key and how to restore. */
export function BackupsPage({ demo }: { demo?: BackupsDemo }) {
  const t = useT();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const unavailable = useEdition().off("backups");
  const settingsQuery = useQuery({
    queryKey: settingsKey,
    queryFn: async ({ signal }) => plain(await backups.getBackupSettings({}, { signal })).settings,
    enabled: owner && !unavailable,
    refetchInterval: (q) => (q.state.data?.running ? 3000 : false), // a running backup reports through the settings
  });
  const stored = settingsQuery.data;
  if (unavailable) {
    return (
      <div className="sx-one">
        <Sec tone="sky" d={ICONS.cloud} title={t("set.backups.title")}>
          <div className="sx-pad">
            <EditionNotice feature="backups" />
          </div>
        </Sec>
      </div>
    );
  }
  if (!owner) {
    return (
      <div className="sx-one">
        <Sec tone="sky" d={ICONS.cloud} title={t("set.backups.title")}>
          <p className="sb-emp">{t("set.backups.ownerOnly")}</p>
        </Sec>
      </div>
    );
  }
  return (
    <SxGate pending={settingsQuery.isPending} error={settingsQuery.isError} onRetry={() => void settingsQuery.refetch()}>
      {stored && <BackupsForm stored={stored} demo={demo} />}
    </SxGate>
  );
}

function BackupsForm({ stored, demo }: { stored: Stored; demo?: BackupsDemo }) {
  const t = useT();
  const { locale } = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const [draft, setDraft] = useState(() => draftFrom(stored));
  const [secretVisible, setSecretVisible] = useState(!!demo?.secret);
  const [dropSecret, setDropSecret] = useState(demo?.windows === "secret");
  const [r2Bad, setR2Bad] = useState<string | null>(demo?.r2 === "bad" ? t("sx.bk.r2bad.body") : null);
  const [r2Done, setR2Done] = useState(demo?.r2 === "ok");
  const configured = hasConfiguration(stored);
  // a restore: the window, opened by a row of the history (with its backup) or by the card (a file, or any of them)
  const [restore, setRestore] = useState<{ pick?: string } | null>(demo?.restore ? { pick: demo.restore.pick } : null);
  const dirty =
    draft.accountId !== stored.accountId ||
    draft.jurisdiction !== (stored.jurisdiction || "default") ||
    draft.bucket !== stored.bucket ||
    draft.accessKeyId !== stored.accessKeyId ||
    draft.ageRecipient !== stored.ageRecipient ||
    draft.enabled !== stored.enabled ||
    draft.intervalHours !== (stored.intervalHours || 24) ||
    draft.retentionDays !== stored.retentionDays ||
    draft.secretAccessKey !== "";

  const backupsQuery = useQuery({
    queryKey: backupsKey,
    queryFn: async ({ signal }) => plain(await backups.listBackups({}, { signal })).backups,
    enabled: configured,
  });
  const list = backupsQuery.data;
  const fresh = useFreshKeys(list?.map((b) => b.key), demo?.fresh ? [demo.fresh] : []);

  // the list is whole (one object per day at most, kept for the retention): the pages are cut here, ten rows each
  const paging = usePaging({ sizes: [historySize], defaultSize: historySize });
  const historyPage = clampPage(paging.page, list?.length ?? 0, historySize);

  const settingsRequest = (over: Partial<Parameters<typeof backups.updateBackupSettings>[0]> = {}) => ({
    accountId: draft.accountId,
    jurisdiction: draft.jurisdiction,
    bucket: draft.bucket,
    accessKeyId: draft.accessKeyId,
    secretAccessKey: draft.secretAccessKey,
    ageRecipient: draft.ageRecipient,
    enabled: draft.enabled,
    intervalHours: draft.intervalHours,
    retentionDays: draft.retentionDays,
    clearSecret: false,
    ...over,
  });
  const failed = (error: unknown) => {
    if (isStepUpCancelled(error)) return;
    const key = backupErrorKey(error);
    toast.error(key ? t(key) : errorText(error, t));
  };

  const save = useMutation({
    mutationFn: (request: Parameters<typeof backups.updateBackupSettings>[0]) => guard(() => backups.updateBackupSettings(request)),
    onSuccess: (result, request) => {
      const settings = plain(result).settings;
      if (settings) qc.setQueryData(settingsKey, settings);
      setDraft((current) => ({ ...current, secretAccessKey: "" }));
      void qc.invalidateQueries({ queryKey: backupsKey });
      setDropSecret(false);
      setR2Bad(null);
      toast(request.clearSecret ? t("sx.bk.secret.gone") : t("set.backups.saved"));
    },
    onError: failed,
  });

  const test = useMutation({
    mutationFn: () => guard(() => backups.testBackupStorage({})),
    onMutate: () => {
      setR2Bad(null);
      setR2Done(false);
    },
    onSuccess: () => {
      toast(t("set.backups.testOk"));
      setR2Done(true);
      setTimeout(() => setR2Done(false), 2200);
    },
    onError: (error) => {
      if (isStepUpCancelled(error)) return;
      // the board's red banner: R2 would not let the panel in, and the reason as exactly as the panel knows it
      const key = backupErrorKey(error);
      setR2Bad(key ? t(key) : t("sx.bk.r2bad.body"));
    },
  });

  // The backup runs in the panel after the request returns (it can take longer than a request may): the settings say
  // when it is over, and how it went.
  const started = useRef<{ lastSuccess: number } | null>(null);
  const create = useMutation({
    mutationFn: () => guard(() => backups.createBackup({})),
    onSuccess: () => {
      started.current = { lastSuccess: stored.lastSuccessUnix };
      qc.setQueryData(settingsKey, (s: Stored | undefined) => s && { ...s, running: true });
      void qc.invalidateQueries({ queryKey: settingsKey });
      toast(t("set.backups.started"));
    },
    onError: failed,
  });
  useEffect(() => {
    const mine = started.current;
    if (stored.running || !mine) return;
    started.current = null;
    void qc.invalidateQueries({ queryKey: backupsKey });
    if (stored.lastSuccessUnix !== mine.lastSuccess) {
      toast(stored.lastErrorCode ? t("set.backups.createdWarning") : t("set.backups.created"));
    } else toast.error(t(codeKeys[stored.lastErrorCode] ?? "set.backups.error.create"));
  }, [stored.running, stored.lastSuccessUnix, stored.lastErrorCode, qc, t, toast]);

  function update<K extends keyof Draft>(key: K, value: Draft[K]) {
    setDraft((current) => ({ ...current, [key]: value }));
    setR2Bad(null);
  }
  const digits = (v: string) => Number(v.replace(/\D/g, "").slice(0, 4));

  function submit(event: FormEvent) {
    event.preventDefault();
    if (save.isPending || !dirty) return;
    save.mutate(settingsRequest());
  }

  // the state card: the colour is the state, never alone (the title and the line say it)
  const running = stored.running || create.isPending || !!demo?.running;
  const state = !configured ? "off" : stored.lastErrorCode && !running ? "bad" : "ok";
  const title = !configured ? t("sx.bk.st.off") : state === "bad" ? t("sx.bk.st.bad") : stored.enabled ? t("sx.bk.st.ok") : t("sx.bk.st.manual");
  const sub = !configured
    ? t("sx.bk.st.off.body")
    : state === "bad"
      ? codeKeys[stored.lastErrorCode]
        ? t(codeKeys[stored.lastErrorCode]!)
        : t("set.backups.lastError", { code: stored.lastErrorCode })
      : [t("set.backups.lastSuccess", { time: backupTime(stored.lastSuccessUnix, locale) }), t("set.backups.retentionStatus", { days: stored.retentionDays === 0 ? t("set.backups.retention.never") : t("set.backups.retention.days", { n: stored.retentionDays }) })].join(" · ");

  return (
    <div className="pf-g pp-grid sb-grid sx-grid">
      <div className="col">
        <section className={cx("card sx-bkst", state)}>
          <span className="pp-ic" aria-hidden>
            <G d={ICONS.box} size={22} stroke={1.5} />
          </span>
          <div>
            <b>{title}</b>
            <span>{sub}</span>
          </div>
          <span className="ai-na">
            {configured && (
              <BusyButton variant="primary" size="md" busy={running} disabled={dirty} onClick={() => create.mutate()}>
                {running ? t("sx.bk.running") : t("set.backups.create")}
              </BusyButton>
            )}
            <FileBackup recipient={!!stored.ageRecipient} demo={demo} />
          </span>
        </section>

        <Sec tone="sky" d={ICONS.cloud} title={t("set.backups.title")} lead={t("set.backups.body")}>
          <form onSubmit={submit}>
            <Swr label={t("set.backups.enabled")} hint={t("set.backups.enabledHint")}>
              <Switch className="fz-sw" checked={draft.enabled} onCheckedChange={(checked) => update("enabled", checked)} aria-label={t("set.backups.enabled")} />
            </Swr>
            <div className="sx-form sx-f2">
              <label className="an-f">
                <span>{t("set.backups.accountId")}</span>
                <input className="an-i mono" type="text" value={draft.accountId} autoComplete="off" spellCheck={false} onChange={(e) => update("accountId", e.target.value)} />
              </label>
              <div className="an-f">
                <span>{t("set.backups.jurisdiction")}</span>
                <Select
                  value={draft.jurisdiction}
                  onValueChange={(value) => update("jurisdiction", value)}
                  aria-label={t("set.backups.jurisdiction")}
                  options={[
                    { value: "default", label: t("set.backups.jurisdiction.default") },
                    { value: "eu", label: t("set.backups.jurisdiction.eu") },
                    { value: "us", label: t("set.backups.jurisdiction.us") },
                    { value: "fedramp", label: t("set.backups.jurisdiction.fedramp") },
                  ]}
                />
              </div>
              <label className="an-f">
                <span>{t("set.backups.bucket")}</span>
                <input className="an-i mono" type="text" value={draft.bucket} autoComplete="off" spellCheck={false} onChange={(e) => update("bucket", e.target.value)} />
              </label>
              <label className="an-f">
                <span>{t("set.backups.accessKeyId")}</span>
                <input className="an-i mono" type="text" value={draft.accessKeyId} autoComplete="off" spellCheck={false} onChange={(e) => update("accessKeyId", e.target.value)} />
              </label>
              <div className="an-f sx-w2">
                <span id="sx-bk-sl">{t("set.backups.secretAccessKey")}</span>
                <input
                  className="an-i mono"
                  type={secretVisible ? "text" : "password"}
                  aria-labelledby="sx-bk-sl"
                  value={draft.secretAccessKey}
                  autoComplete="off"
                  spellCheck={false}
                  onChange={(e) => update("secretAccessKey", e.target.value)}
                />
                <small>{stored.hasSecret ? t("set.backups.secretSaved") : t("set.backups.secretHint")}</small>
                <span className="sx-lks">
                  <button type="button" className="an-lk ig-lk" onClick={() => setSecretVisible((v) => !v)}>
                    {secretVisible ? t("set.backups.hideSecret") : t("set.backups.showSecret")}
                  </button>
                  {stored.hasSecret && (
                    <button type="button" className="an-lk ig-lk" onClick={() => setDropSecret(true)}>
                      {t("set.backups.clearSecret")}
                    </button>
                  )}
                </span>
              </div>
              <label className="an-f sx-w2">
                <span>{t("set.backups.ageRecipient")}</span>
                <textarea className="an-i sb-ta mono" rows={2} value={draft.ageRecipient} spellCheck={false} onChange={(e) => update("ageRecipient", e.target.value)} />
                <small>{t("sx.bk.age.hint")}</small>
              </label>
              <label className="an-f">
                <span>{t("set.backups.interval")}</span>
                <input className="an-i mono" type="text" inputMode="numeric" value={String(draft.intervalHours)} onChange={(e) => update("intervalHours", digits(e.target.value))} />
                <small>{t("set.backups.intervalHint")}</small>
              </label>
              <label className="an-f">
                <span>{t("set.backups.retention")}</span>
                <input className="an-i mono" type="text" inputMode="numeric" value={String(draft.retentionDays)} onChange={(e) => update("retentionDays", digits(e.target.value))} />
                <small>{t("set.backups.retentionHint")}</small>
              </label>
            </div>
            {r2Bad && (
              <Pb tone="bad" className="sx-pbm" title={t("sx.bk.r2bad")}>
                {r2Bad}
              </Pb>
            )}
            <Foot>
              <BusyButton type="submit" variant="primary" size="md" busy={save.isPending} disabled={!dirty}>
                {save.isPending ? t("set.backups.saving") : t("set.backups.save")}
              </BusyButton>
              <BusyButton size="md" busy={test.isPending || demo?.r2 === "busy"} disabled={!configured || dirty} className={cx(r2Done && "sx-done")} onClick={() => test.mutate()}>
                {test.isPending || demo?.r2 === "busy" ? t("sx.bk.r2.busy") : t("set.backups.test")}
              </BusyButton>
            </Foot>
          </form>
        </Sec>

        <Sec tone="sand" d={ICONS.history} title={t("set.backups.history")} foot={t("sx.bk.hist.foot")}>
          {!configured && <p className="sb-emp ai-emp">{t("set.backups.configureFirst")}</p>}
          {configured && backupsQuery.isPending && (
            <div className="sx-pad">
              <Pending compact />
            </div>
          )}
          {configured && backupsQuery.isError && (
            <div className="sx-pad">
              <QueryError compact error={backupsQuery.error} onRetry={() => void backupsQuery.refetch()} />
            </div>
          )}
          {configured && list && list.length === 0 && <p className="sb-emp ai-emp">{t("sx.bk.none")}</p>}
          {configured && list && list.length > 0 && (
            <div className="sx-list sx-bks">
              {list.slice((historyPage - 1) * historySize, historyPage * historySize).map((item) => (
                <BackupRow key={item.key} item={item} fresh={fresh.has(item.key)} onRestore={() => setRestore({ pick: item.key })} />
              ))}
            </div>
          )}
          {configured && list && list.length > historySize && (
            <div className="sx-pad">
              <Pagination className="-mb-1" total={list.length} page={historyPage} size={historySize} onPage={paging.setPage} label={t("set.backups.history")} />
            </div>
          )}
        </Sec>

        <RestoreCard onStart={() => setRestore({})} />
      </div>
      <aside className="col pp-as">
        <KeygenCard identityFile={identityFile} />
        <NewServerCard identityFile={identityFile} archiveFile={archiveFile} dataDir={dataDir} />
      </aside>

      <RestoreDialog
        key={restore?.pick ?? "manual"}
        open={restore !== null}
        onOpenChange={(v) => !v && setRestore(null)}
        r2={configured ? (list ?? []) : []}
        initialR2={restore?.pick}
        demo={demo?.restore}
      />
      {dropSecret && (
        <SxWindow
          open
          onOpenChange={(o) => !o && !save.isPending && setDropSecret(false)}
          title={t("sx.bk.secret.title")}
          shot="backups-secret-remove"
          footer={
            <>
              <Button size="lg" disabled={save.isPending} onClick={() => setDropSecret(false)}>
                {t("common.cancel")}
              </Button>
              {/* the request holds what is saved, so a draft that is half typed is neither sent nor lost */}
              <Button
                variant="primary"
                size="lg"
                className="sx-dng"
                disabled={save.isPending}
                onClick={() =>
                  save.mutate({
                    accountId: stored.accountId,
                    jurisdiction: stored.jurisdiction || "default",
                    bucket: stored.bucket,
                    accessKeyId: stored.accessKeyId,
                    secretAccessKey: "",
                    ageRecipient: stored.ageRecipient,
                    enabled: stored.enabled,
                    intervalHours: stored.intervalHours || 24,
                    retentionDays: stored.retentionDays,
                    clearSecret: true,
                  })
                }
              >
                {t("sx.bk.secret.go")}
              </Button>
            </>
          }
        >
          <p className="sx-dp">{t("sx.bk.secret.body")}</p>
        </SxWindow>
      )}
    </div>
  );
}

function BackupRow({ item, fresh, onRestore }: { item: BackupItem; fresh: boolean; onRestore: () => void }) {
  const t = useT();
  const { locale } = useFmt();
  return (
    <div className={cx("sx-it sx-bk", fresh && "fresh")}>
      <div>
        <b className="mono">{backupTime(item.createdUnix, locale)}</b>
        <em className="mono">{item.key}</em>
      </div>
      <code className="mono">{formatBytes(item.sizeBytes, locale)}</code>
      <span className="ai-na">
        <CopyButton value={item.key} size="md" />
        <Button size="md" onClick={onRestore}>
          {t("set.backups.rs.do")}
        </Button>
      </span>
    </div>
  );
}

/**
 * A backup on this computer (ADR 0012): the same encrypted archive as the R2 backups, R2 or not; to the recovery key when
 * there is one, else with a passphrase made up for the file (a window asks for it).
 */
function FileBackup({ recipient, demo }: { recipient: boolean; demo?: BackupsDemo }) {
  const t = useT();
  const toast = useToast();
  const guard = useStepUp();
  const version = useQuery(meQuery).data?.version ?? "";
  const [passOpen, setPassOpen] = useState(demo?.windows === "pass");
  const download = useMutation({
    mutationFn: (passphrase?: string) => guard(() => downloadBackup(version, passphrase)),
    onSuccess: (name) => {
      setPassOpen(false);
      toast(t("set.backups.file.done", { name }));
    },
    onError: (error) => {
      if (!isStepUpCancelled(error)) toast.error(errorText(error, t));
    },
  });
  const busy = download.isPending || demo?.download === "busy";
  return (
    <>
      <BusyButton size="md" busy={busy} onClick={() => (recipient ? download.mutate(undefined) : setPassOpen(true))}>
        {busy ? t("sx.bk.dl.busy") : t("set.backups.file.download")}
      </BusyButton>
      {passOpen && <PassWindow busy={download.isPending} onCancel={() => setPassOpen(false)} onDownload={(p) => download.mutate(p)} />}
    </>
  );
}

/** The passphrase of a backup file that has no recovery key: made up for this file alone, twice, 12 characters or more. */
function PassWindow({ busy, onCancel, onDownload }: { busy: boolean; onCancel: () => void; onDownload: (passphrase: string) => void }) {
  const t = useT();
  const [a, setA] = useState("");
  const [b, setB] = useState("");
  const ok = a.length >= 12 && a === b;
  const err = b && a !== b ? t("up.dlg.pass.mismatch") : a && a.length < 12 ? t("up.dlg.pass.short") : "";
  return (
    <SxWindow
      open
      onOpenChange={(o) => !o && !busy && onCancel()}
      title={t("up.dlg.pass.title")}
      shot="backups-passphrase"
      footer={
        <>
          <Button size="lg" disabled={busy} onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <BusyButton variant="primary" size="lg" busy={busy} disabled={!ok} onClick={() => onDownload(a)}>
            {t("up.dlg.file.download")}
          </BusyButton>
        </>
      }
    >
      <>
        <p className="sx-dp">{t("up.dlg.pass.why")}</p>
        <label className="an-f">
          <span>{t("up.dlg.pass.a")}</span>
          <input className="an-i" type="password" autoComplete="new-password" value={a} onChange={(e) => setA(e.target.value)} />
        </label>
        <label className={cx("an-f", err && "err")}>
          <span>{t("up.dlg.pass.b")}</span>
          <input className="an-i" type="password" autoComplete="new-password" value={b} aria-invalid={!!err || undefined} onChange={(e) => setB(e.target.value)} />
          <small>{err}</small>
        </label>
        <small className="sx-fine">{t("up.dlg.pass.key")}</small>
      </>
    </SxWindow>
  );
}
