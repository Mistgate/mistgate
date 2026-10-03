import { ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import type { Backup, BackupSettings } from "@/gen/mistgate/admin/v1/backup_pb";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { backups } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { plain, type Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { CopyRow } from "./domains";

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
  clearSecret: boolean;
};

const settingsKey = ["backup-settings"] as const;
const backupsKey = ["backup-list"] as const;
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
    clearSecret: false,
  };
}

function hasConfiguration(stored?: Stored) {
  return !!stored?.accountId && !!stored.bucket && !!stored.accessKeyId && !!stored.ageRecipient && stored.hasSecret;
}

function backupErrorKey(error: unknown): MessageKey | null {
  const raw = ConnectError.from(error).rawMessage;
  const keys: Record<string, MessageKey> = {
    backup_not_configured: "set.backups.error.notConfigured",
    backup_settings_invalid: "set.backups.error.invalid",
    backup_storage_failed: "set.backups.error.storage",
    backup_storage_delete_failed: "set.backups.error.delete",
    backup_create_failed: "set.backups.error.create",
    backup_already_running: "set.backups.error.busy",
  };
  return keys[raw] ?? null;
}

function formatBytes(size: number) {
  if (!Number.isFinite(size) || size < 0) return "—";
  if (size < 1024) return `${size} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let value = size;
  let unit = -1;
  do {
    value /= 1024;
    unit++;
  } while (value >= 1024 && unit < units.length - 1);
  return `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })} ${units[unit]}`;
}

function backupTime(unix: number) {
  return unix > 0 ? new Date(unix * 1000).toLocaleString() : "—";
}

/** Settings -> Backups: owner-managed encrypted R2 backups and offline restore instructions. */
export function BackupsPage() {
  const t = useT();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const settingsQuery = useQuery({
    queryKey: settingsKey,
    queryFn: async ({ signal }) => plain(await backups.getBackupSettings({}, { signal })).settings,
    enabled: owner,
  });
  const stored = settingsQuery.data;
  if (!owner) {
    return (
      <section className="rounded-card-lg border border-line bg-surface p-4">
        <SectionLabel as="h2" icon="shield" tone="lavender">{t("set.backups.title")}</SectionLabel>
        <p className="mt-3 text-[13px] text-muted">{t("set.backups.ownerOnly")}</p>
      </section>
    );
  }
  if (!stored) {
    return (
      <section className="rounded-card-lg border border-line bg-surface px-4 py-1">
        <SectionLabel as="h2" icon="shield" tone="lavender">{t("set.backups.title")}</SectionLabel>
        {settingsQuery.isError ? <QueryError compact className="border-t border-line py-3" error={settingsQuery.error} onRetry={() => void settingsQuery.refetch()} /> : <Pending compact className="border-t border-line py-3.5" />}
      </section>
    );
  }
  return <BackupsForm stored={stored} />;
}

function BackupsForm({ stored }: { stored: Stored }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const [draft, setDraft] = useState(() => draftFrom(stored));
  const [secretVisible, setSecretVisible] = useState(false);
  const configured = hasConfiguration(stored);
  const dirty = draft.accountId !== stored.accountId || draft.jurisdiction !== (stored.jurisdiction || "default") ||
    draft.bucket !== stored.bucket || draft.accessKeyId !== stored.accessKeyId || draft.ageRecipient !== stored.ageRecipient ||
    draft.enabled !== stored.enabled || draft.intervalHours !== (stored.intervalHours || 24) ||
    draft.retentionDays !== stored.retentionDays || draft.secretAccessKey !== "" || draft.clearSecret;

  const backupsQuery = useQuery({
    queryKey: backupsKey,
    queryFn: async ({ signal }) => plain(await backups.listBackups({}, { signal })).backups,
    enabled: configured,
  });

  const save = useMutation({
    mutationFn: (request: Parameters<typeof backups.updateBackupSettings>[0]) => guard(() => backups.updateBackupSettings(request)),
    onSuccess: (result) => {
      const settings = plain(result).settings;
      if (settings) qc.setQueryData(settingsKey, settings);
      setDraft((current) => ({ ...current, secretAccessKey: "", clearSecret: false }));
      void qc.invalidateQueries({ queryKey: backupsKey });
      toast(t("set.backups.saved"));
    },
    onError: (error) => {
      if (isStepUpCancelled(error)) return;
      const key = backupErrorKey(error);
      if (key) toast.error(t(key));
      else toast.error(errorText(error, t));
    },
  });

  const test = useMutation({
    mutationFn: () => guard(() => backups.testBackupStorage({})),
    onSuccess: () => toast(t("set.backups.testOk")),
    onError: (error) => {
      if (isStepUpCancelled(error)) return;
      const key = backupErrorKey(error);
      if (key) toast.error(t(key));
      else toast.error(errorText(error, t));
    },
  });

  const create = useMutation({
    mutationFn: () => guard(() => backups.createBackup({})),
    onSuccess: (result) => {
      void qc.invalidateQueries({ queryKey: backupsKey });
      void qc.invalidateQueries({ queryKey: settingsKey });
      toast(result.warningCode ? t("set.backups.createdWarning") : t("set.backups.created"));
    },
    onError: (error) => {
      if (isStepUpCancelled(error)) return;
      const key = backupErrorKey(error);
      if (key) toast.error(t(key));
      else toast.error(errorText(error, t));
    },
  });

  function update<K extends keyof Draft>(key: K, value: Draft[K]) {
    setDraft((current) => ({ ...current, [key]: value }));
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    if (save.isPending) return;
    save.mutate({
      accountId: draft.accountId,
      jurisdiction: draft.jurisdiction,
      bucket: draft.bucket,
      accessKeyId: draft.accessKeyId,
      secretAccessKey: draft.secretAccessKey,
      ageRecipient: draft.ageRecipient,
      enabled: draft.enabled,
      intervalHours: draft.intervalHours,
      retentionDays: draft.retentionDays,
      clearSecret: draft.clearSecret,
    });
  }

  return (
    <div className="flex flex-col gap-3.5">
      <section className="flex flex-col gap-3.5 rounded-card-lg border border-line bg-surface p-4">
        <SectionLabel as="h2" icon="shield" tone="lavender">{t("set.backups.title")}</SectionLabel>
        <p className="text-[13px] leading-normal text-pretty text-muted">{t("set.backups.body")}</p>

        <form className="flex flex-col gap-4" onSubmit={submit}>
          <label className="flex items-center justify-between gap-4 rounded-field border border-line bg-canvas px-3.5 py-3">
            <span className="flex flex-col gap-0.5">
              <span className="text-sm font-bold text-fg">{t("set.backups.enabled")}</span>
              <span className="text-xs text-muted">{t("set.backups.enabledHint")}</span>
            </span>
            <Switch checked={draft.enabled} onCheckedChange={(checked) => update("enabled", checked)} aria-label={t("set.backups.enabled")} />
          </label>

          <div className="grid gap-3 sm:grid-cols-2">
            <TextField label={t("set.backups.accountId")} value={draft.accountId} onChange={(e) => update("accountId", e.target.value)} autoComplete="off" spellCheck={false} mono />
            <div className="flex flex-col gap-1.5">
              <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("set.backups.jurisdiction")}</span>
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
            <TextField label={t("set.backups.bucket")} value={draft.bucket} onChange={(e) => update("bucket", e.target.value)} autoComplete="off" spellCheck={false} mono />
            <TextField label={t("set.backups.accessKeyId")} value={draft.accessKeyId} onChange={(e) => update("accessKeyId", e.target.value)} autoComplete="off" spellCheck={false} mono />
            <div className="flex flex-col gap-1.5 sm:col-span-2">
              <TextField
                label={t("set.backups.secretAccessKey")}
                hint={stored.hasSecret && !draft.clearSecret ? t("set.backups.secretSaved") : t("set.backups.secretHint")}
                type={secretVisible ? "text" : "password"}
                value={draft.secretAccessKey}
                onChange={(e) => update("secretAccessKey", e.target.value)}
                autoComplete="new-password"
                spellCheck={false}
              />
              {stored.hasSecret && (
                <label className="flex items-center gap-2 text-xs text-muted">
                  <input type="checkbox" checked={draft.clearSecret} onChange={(e) => update("clearSecret", e.target.checked)} />
                  {t("set.backups.clearSecret")}
                </label>
              )}
              <button className="self-start text-xs font-semibold text-accent-text underline decoration-accent-line underline-offset-2" type="button" onClick={() => setSecretVisible((v) => !v)}>
                {secretVisible ? t("set.backups.hideSecret") : t("set.backups.showSecret")}
              </button>
            </div>
            <TextField
              className="sm:col-span-2"
              label={t("set.backups.ageRecipient")}
              hint={t("set.backups.ageRecipientHint")}
              value={draft.ageRecipient}
              onChange={(e) => update("ageRecipient", e.target.value)}
              autoComplete="off"
              spellCheck={false}
              mono
            />
            <TextField
              label={t("set.backups.interval")}
              hint={t("set.backups.intervalHint")}
              type="number"
              min={1}
              max={168}
              value={String(draft.intervalHours)}
              onChange={(e) => update("intervalHours", Number(e.target.value))}
            />
            <TextField
              label={t("set.backups.retention")}
              hint={t("set.backups.retentionHint")}
              type="number"
              min={0}
              max={3650}
              value={String(draft.retentionDays)}
              onChange={(e) => update("retentionDays", Number(e.target.value))}
            />
          </div>

          <Notice>{t("set.backups.privateKeyReminder")}</Notice>

          <div className="flex flex-wrap items-center gap-2 border-t border-line pt-3.5">
            <Button type="submit" variant="primary" disabled={save.isPending || !dirty}>
              {save.isPending ? t("set.backups.saving") : t("set.backups.save")}
            </Button>
            <Button type="button" disabled={!configured || dirty || test.isPending} onClick={() => test.mutate()}>
              {test.isPending ? t("set.backups.testing") : t("set.backups.test")}
            </Button>
            <Button type="button" disabled={!configured || dirty || create.isPending} onClick={() => create.mutate()}>
              {create.isPending ? t("set.backups.creating") : t("set.backups.create")}
            </Button>
          </div>
        </form>

        <div className="grid gap-2 border-t border-line pt-3 text-xs text-muted sm:grid-cols-2">
          <p>{t("set.backups.lastSuccess", { time: backupTime(stored.lastSuccessUnix) })}</p>
          <p>{t("set.backups.retentionStatus", { days: stored.retentionDays === 0 ? t("set.backups.retention.never") : t("set.backups.retention.days", { n: stored.retentionDays }) })}</p>
          {stored.lastErrorCode && <p className="text-danger-text sm:col-span-2">{t("set.backups.lastError", { code: stored.lastErrorCode })}</p>}
        </div>
      </section>

      <section className="flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4">
        <SectionLabel as="h3" icon="shield" tone="sky">{t("set.backups.history")}</SectionLabel>
        {!configured && <p className="text-[13px] text-muted">{t("set.backups.configureFirst")}</p>}
        {configured && backupsQuery.isPending && <Pending compact />}
        {configured && backupsQuery.isError && <QueryError compact error={backupsQuery.error} onRetry={() => void backupsQuery.refetch()} />}
        {configured && backupsQuery.data && backupsQuery.data.length === 0 && <p className="text-[13px] text-muted">{t("set.backups.empty")}</p>}
        {configured && backupsQuery.data && backupsQuery.data.length > 0 && (
          <div className="flex flex-col divide-y divide-line">
            {backupsQuery.data.map((item) => <BackupRow key={item.key} item={item} />)}
          </div>
        )}
      </section>

      <section className="flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4">
        <SectionLabel as="h3" icon="shield" tone="mint">{t("set.backups.restore")}</SectionLabel>
        <p className="text-[13px] leading-normal text-pretty text-muted">{t("set.backups.restoreHint")}</p>
        <div className="flex flex-col gap-2">
          <CopyRow value={t("set.backups.keygenCommand", { path: identityFile })} />
          <CopyRow value={t("set.backups.restoreCommand", { identity: identityFile, archive: archiveFile, dataDir })} />
        </div>
        <p className="text-xs leading-normal text-pretty text-muted">{t("set.backups.externalCredential")}</p>
      </section>
    </div>
  );
}

function BackupRow({ item }: { item: BackupItem }) {
  return (
    <div className="flex flex-col gap-1.5 py-2.5 first:pt-0 last:pb-0">
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs text-muted">
        <span>{backupTime(item.createdUnix)}</span>
        <span>{formatBytes(item.sizeBytes)}</span>
      </div>
      <CopyRow value={item.key} />
    </div>
  );
}
