import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button, buttonClass } from "@/components/ui/button";
import { useToast } from "@/components/ui/toast";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { TextField } from "@/components/ui/text-field";
import { useT } from "@/i18n";
import { backups } from "@/lib/api";
import { BackupDownloadError, backupFileName, downloadBackup, lastDownload } from "@/lib/backup-download";
import { asChannel, type Channel } from "@/lib/channels";
import { useEdition } from "@/lib/edition";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { plain } from "@/lib/plain";
import type { UpdateActions, Updates } from "@/lib/updates";
import { G } from "@/screens/users/user-bits";
import { D } from "./bits";
import { channelName } from "./channel";

// The window before the panel updates itself (ADR 0012): what happens, then a backup — to Cloudflare R2 when the owner set
// it up, and a file on this computer in any case — and Update. Nothing blocks the update but a backup to R2 that is still
// running (the panel must not stop in the middle of its snapshot).

/** What the window knows of the backups: Settings → Backups (GetBackupSettings), and this browser's last download. */
export type BackupView = {
  /** R2 is set up: account, bucket, keys and the recovery recipient (scheduled or not). */
  r2: boolean;
  /** The archive is encrypted to the owner's recovery key; without it the download asks for a passphrase. */
  recipient: boolean;
  lastSuccessUnix: number;
  lastErrorCode: string;
  running: boolean;
  /** When this browser last downloaded a backup of this version (0 = never). */
  downloadedUnix: number;
};

const day = 24 * 3600;

/**
 * The passphrase of a downloaded backup when Settings → Backups has no recovery key: made up for this file alone, typed
 * twice. Shared by the update window and Settings → Backups.
 */
export function PassphraseForm({ busy, onCancel, onDownload }: { busy: boolean; onCancel: () => void; onDownload: (passphrase: string) => void }) {
  const t = useT();
  const [a, setA] = useState("");
  const [b, setB] = useState("");
  const ok = a.length >= 12 && a === b;
  return (
    <div className="flex flex-col gap-2.5 border-t border-line pt-3">
      {/* a passphrase made up for this file alone: the words say whose it is not, so nobody types an account's */}
      <div className="flex items-start gap-2">
        <Icon name="lock" size={14} className="mt-0.5 flex-none text-muted" aria-hidden />
        <div className="flex min-w-0 flex-col gap-1">
          <span className="text-[13px] font-bold">{t("up.dlg.pass.title")}</span>
          <p className="text-xs leading-snug text-pretty text-muted">{t("up.dlg.pass.why")}</p>
        </div>
      </div>
      <TextField label={t("up.dlg.pass.a")} type="password" autoComplete="new-password" value={a} onChange={(e) => setA(e.target.value)} />
      <TextField
        label={t("up.dlg.pass.b")}
        type="password"
        autoComplete="new-password"
        value={b}
        error={b && a !== b ? t("up.dlg.pass.mismatch") : a && a.length < 12 ? t("up.dlg.pass.short") : undefined}
        onChange={(e) => setB(e.target.value)}
      />
      <div className="flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel}>
          {t("common.cancel")}
        </Button>
        <Button variant="secondary" size="sm" disabled={!ok || busy} onClick={() => onDownload(a)}>
          <Icon name="arrowDown" size={12} />
          {t("up.dlg.file.download")}
        </Button>
      </div>
      <p className="text-[11px] leading-snug text-pretty text-faint">{t("up.dlg.pass.key")}</p>
    </div>
  );
}

export function UpdatePanelDialog({
  open,
  onOpenChange,
  from,
  to,
  backup,
  fileName,
  now,
  onBackupR2,
  setUp,
  onDownload,
  onUpdate,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  from: { version: string; channel: Channel };
  to: { version: string; channel: Channel };
  backup: BackupView;
  /** The name the file will get: mistgate-backup-<panel>-<version>-<UTC date>.tar.gz.age. */
  fileName: string;
  now: number;
  onBackupR2: () => Promise<boolean>;
  /** The way to Settings → Backups when R2 is not set up (a link, so leaving closes the window). */
  setUp: ReactNode;
  /** Downloads the encrypted archive; the passphrase only when there is no recovery recipient. */
  onDownload: (passphrase?: string) => Promise<boolean>;
  onUpdate: () => Promise<boolean>;
}) {
  const t = useT();
  const fmt = useFmt();
  const [passOpen, setPassOpen] = useState(false);
  const [busy, setBusy] = useState<"" | "r2" | "file" | "update">("");
  const fresh = (backup.r2 && now - backup.lastSuccessUnix < day) || now - backup.downloadedUnix < day;
  const act = async (what: "r2" | "file" | "update", fn: () => Promise<boolean>) => {
    setBusy(what);
    const ok = await fn();
    setBusy("");
    return ok;
  };

  const r2Line = !backup.r2
    ? t("up.dlg.r2.off")
    : backup.running
      ? t("up.dlg.r2.running")
      : backup.lastErrorCode
        ? t("up.dlg.r2.failed")
        : backup.lastSuccessUnix > 0
          ? t("up.dlg.r2.last", { ago: fmt.ago(backup.lastSuccessUnix) })
          : t("up.dlg.r2.never");
  const r2Tone = backup.r2 && !backup.running ? (backup.lastErrorCode ? "bad" : now - backup.lastSuccessUnix < 600 ? "ok" : undefined) : undefined;
  const downloaded = backup.downloadedUnix > 0 && now - backup.downloadedUnix < day;

  return (
    <Modal
      open={open}
      onOpenChange={(o) => !busy && onOpenChange(o)}
      title={t("up.dlg.title", { version: to.version })}
      closeLabel={t("common.close")}
      className="uq-dlg"
      shot="updates-update-dialog"
      footer={
        <div className="flex w-full flex-col gap-3">
          {!fresh && <Notice tone="warn">{t("up.dlg.noFresh")}</Notice>}
          <div className="flex flex-wrap justify-end gap-2">
            <Button variant="secondary" size="lg" disabled={!!busy} onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button
              variant="primary"
              size="lg"
              disabled={!!busy || backup.running}
              title={backup.running ? t("up.dlg.waitBackup") : undefined}
              onClick={() => void act("update", onUpdate).then((ok) => ok && onOpenChange(false))}
            >
              {backup.running ? t("up.dlg.waitBackup") : t("up.dlg.update")}
            </Button>
          </div>
        </div>
      }
    >
      <div className="uq-db">
        <div className="uq-fromto">
          <span className="mono">{from.version}</span>
          <G d={D.arrow} size={16} stroke={1.75} />
          <b className="mono">{to.version}</b>
          <em>{channelName(t, to.channel)}</em>
        </div>
        <ul className="uq-ul">
          <li>{t("up.dlg.what.restart")}</li>
          <li>{t("up.dlg.what.nodes")}</li>
          <li>{t("up.dlg.what.rollback")}</li>
        </ul>
        <div className="uq-bk" role="group" aria-label={t("up.dlg.backup")}>
          <span className="rw-sl">{t("up.dlg.backup")}</span>
          <div>
            <span>
              <b>{t("up.dlg.r2")}</b>
              <em className={r2Tone}>{r2Line}</em>
            </span>
            {backup.r2 ? (
              <Button variant="secondary" size="md" disabled={!!busy || backup.running} onClick={() => void act("r2", onBackupR2)}>
                {backup.running && <span className="an-sp" aria-hidden />}
                {backup.running ? t("up.dlg.r2.running.short") : t("up.dlg.r2.now")}
              </Button>
            ) : (
              setUp
            )}
          </div>
          <div>
            <span>
              <b>{t("up.dlg.file")}</b>
              {downloaded ? (
                <em className="ok">
                  <Icon name="check" size={11} className="me-1 inline" aria-hidden />
                  {t("up.dlg.file.done", { time: fmt.clock(backup.downloadedUnix) })}
                </em>
              ) : (
                // breaks only after its dashes: the panel, the version and the date stay whole
                <em className="mono">
                  {fileName.split("-").map((part, i, all) => (
                    <span key={i}>
                      {part}
                      {i < all.length - 1 && (
                        <>
                          -<wbr />
                        </>
                      )}
                    </span>
                  ))}
                </em>
              )}
            </span>
            {!passOpen && (
              <Button variant="secondary" size="md" disabled={!!busy} onClick={() => (backup.recipient ? void act("file", () => onDownload()) : setPassOpen(true))}>
                {t("up.dlg.file.download")}
              </Button>
            )}
          </div>
          {passOpen && (
            <div className="uq-bkx">
              <PassphraseForm busy={!!busy} onCancel={() => setPassOpen(false)} onDownload={(passphrase) => void act("file", () => onDownload(passphrase)).then((ok) => ok && setPassOpen(false))} />
            </div>
          )}
        </div>
      </div>
    </Modal>
  );
}

/**
 * The window on the Updates page: reads the backup settings while it is open (following a running backup), backs up to R2,
 * saves a file, and installs exactly the release the owner saw.
 */
export function PanelUpdateWindow({ open, onOpenChange, data, actions, onStarted }: { open: boolean; onOpenChange: (open: boolean) => void; data: Updates; actions: UpdateActions; /** The install was accepted: the page follows the update from here. */ onStarted?: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const r2Off = useEdition().off("backups");
  const p = data.panel;
  const update = p?.update;
  const version = p?.version ?? "";
  const [downloaded, setDownloaded] = useState(() => lastDownload(version));
  const key = ["backup-settings"];
  const settings = useQuery({
    queryKey: key,
    queryFn: async ({ signal }) => plain(await backups.getBackupSettings({}, { signal })).settings,
    enabled: open && !r2Off,
    refetchInterval: (q) => (q.state.data?.running ? 3000 : false),
  }).data;
  const r2 = !!settings && !!settings.accountId && !!settings.bucket && !!settings.accessKeyId && settings.hasSecret && !!settings.ageRecipient;
  const failed = (e: unknown) => {
    if (isStepUpCancelled(e)) return false;
    if (e instanceof BackupDownloadError) toast.error(e.code === "backup_busy" ? t("up.dlg.err.busy") : e.code.startsWith("backup_") ? t("up.dlg.err.failed") : e.message);
    else toast.error(errorText(e, t));
    return false;
  };
  const backupR2 = useMutation({ mutationFn: () => guard(() => backups.createBackup({})) });
  if (!update) return null;
  return (
    <UpdatePanelDialog
      open={open}
      onOpenChange={onOpenChange}
      from={{ version, channel: asChannel(data.runningVersionChannel) }}
      to={{ version: update.version, channel: asChannel(data.offeredReleaseChannel) }}
      backup={{
        r2,
        recipient: !!settings?.ageRecipient,
        lastSuccessUnix: settings?.lastSuccessUnix ?? 0,
        lastErrorCode: settings?.lastErrorCode ?? "",
        running: !!settings?.running,
        downloadedUnix: downloaded,
      }}
      fileName={backupFileName(location.hostname, version, new Date(data.nowUnix * 1000))}
      now={data.nowUnix}
      onBackupR2={() =>
        backupR2.mutateAsync().then(
          () => {
            void qc.invalidateQueries({ queryKey: key });
            return true;
          },
          failed,
        )
      }
      setUp={
        <Link to="/settings/$section" params={{ section: "backups" }} className={buttonClass("ghost", "sm")}>
          {t("up.dlg.r2.setUp")}
          <Icon name="chevronRight" size={12} />
        </Link>
      }
      onDownload={(passphrase) =>
        guard(() => downloadBackup(version, passphrase)).then(() => {
          setDownloaded(lastDownload(version));
          return true;
        }, failed)
      }
      onUpdate={() =>
        actions.installPanel({ version: update.version, sha256: update.sha256 }).then((ok) => {
          if (ok) onStarted?.();
          return ok;
        })
      }
    />
  );
}
