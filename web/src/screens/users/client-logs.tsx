import { useQuery } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { LogViewer } from "@/components/log-viewer";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { GlyphMark, StatusGlyph, type StatusKind } from "@/components/ui/status";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import { Tip } from "@/components/ui/tooltip";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { clientLogPage, clientLogsQuery, logView, useClientLogActions, type ClientLog, type LogView } from "@/lib/client-debug";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { isUnavailable } from "@/lib/ops";
import { meQuery } from "@/lib/session";
import { useTx, type Tx } from "./t";
import { ConfirmModal } from "./ui";

// Logs for support on the person's page: the owner asks a device of the Mistgate app for its logs (a request of 72 hours the person
// answers in the app), and reads what came, or what the person sent on their own. Only the owner, only the VPS edition: on the
// Cloudflare edition OpsService answers UNIMPLEMENTED and none of this is drawn.

/** What the page's rows and menus need of the logs: the list and the four things an owner does with it. */
export type LogsCtl = {
  logs: ClientLog[];
  /** The request window for a device with a key. */
  ask: (device: { id: string; name: string }) => void;
  cancel: (id: string) => void;
  open: (log: ClientLog) => void;
  download: (id: string) => void;
  remove: (log: ClientLog) => void;
};

const stateStyle: Record<LogView, { kind: StatusKind; mark?: string }> = {
  open: { kind: "warn", mark: "M1.5 4h.01M4 4h.01M6.5 4h.01" },
  received: { kind: "ok" },
  unasked: { kind: "blip", mark: "M4 6.6V1.6M1.9 3.6 4 1.5l2.1 2.1" },
  declined: { kind: "bad" },
  cancelled: { kind: "off" },
  expired: { kind: "off" },
};

/** The coloured pill of a request or bundle: what it is, in a word and a glyph (never colour alone). */
export function LogStatePill({ view }: { view: LogView }) {
  const t = useTx();
  const { kind, mark } = stateStyle[view];
  return (
    <span className={cx(`tone-${kind} pill inline-flex h-[22px] flex-none items-center gap-1.5 rounded-xl pe-2 ps-1 text-[11px] leading-none font-bold whitespace-nowrap`)}>
      <StatusGlyph kind={kind} glyph={mark && <GlyphMark d={mark} />} />
      {t(`clientlogs.state.${view}`)}
    </span>
  );
}

const badgeClass = "flex-none cursor-default rounded-md pill px-1.5 py-px text-[10px] font-extrabold whitespace-nowrap";

/**
 * The small label after the name of a device the owner asked for logs, or that answered: yellow while the request is open, green
 * when the logs came (a button that opens them), red when the person declined. The tooltip says who asked and until when.
 */
export function LogsBadge({ log, ctl }: { log: ClientLog | undefined; ctl: LogsCtl }) {
  const t = useTx();
  const fmt = useFmt();
  if (!log) return null;
  const view = logView(log);
  if (view === "open") {
    const tip = t("clientlogs.badge.openTip", { who: log.actor || "—", when: fmt.stamp(log.requestedUnix), until: fmt.stamp(log.expiresUnix) });
    const label = t("clientlogs.badge.open");
    return (
      <Tip label={tip}>
        <span tabIndex={0} aria-label={`${label}. ${tip}`} className={cx("tone-warn", badgeClass)}>
          {label}
        </span>
      </Tip>
    );
  }
  if (view === "received" || view === "unasked") {
    const tip = t(view === "unasked" ? "clientlogs.badge.receivedOwn" : "clientlogs.badge.receivedTip", { when: fmt.stamp(log.closedUnix) });
    const label = t("clientlogs.badge.received");
    return (
      <Tip label={tip}>
        <button type="button" aria-label={`${label}. ${tip}`} onClick={() => ctl.open(log)} className={cx("tone-ok cursor-pointer", badgeClass)}>
          {label}
        </button>
      </Tip>
    );
  }
  if (view === "declined") {
    const tip = t("clientlogs.badge.declinedTip", { when: fmt.stamp(log.closedUnix) });
    const label = t("clientlogs.badge.declined");
    return (
      <Tip label={tip}>
        <span tabIndex={0} aria-label={`${label}. ${tip}`} className={cx("tone-bad", badgeClass)}>
          {label}
        </span>
      </Tip>
    );
  }
  return null;
}

type Asking = { id: string; name: string };

/** The window of a request: a note the person will see, the switch for detailed logs (off), and how long it lasts. */
export function RequestLogsModal({ device, open, onOpenChange, onSend, shot }: { device: Asking | null; open: boolean; onOpenChange: (o: boolean) => void; onSend: (note: string, debug: boolean) => Promise<unknown>; shot?: string }) {
  const t = useTx();
  const [note, setNote] = useState("");
  const [debug, setDebug] = useState(false);
  const [busy, setBusy] = useState(false);
  async function send() {
    setBusy(true);
    try {
      await onSend(note, debug);
      onOpenChange(false);
      setNote("");
      setDebug(false);
    } catch {
      // the caller reports the failure with a toast; the window stays so the owner can retry or cancel
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      shot={shot}
      title={t("clientlogs.askT", { name: device?.name ?? "" })}
      description={t("clientlogs.askBody")}
      footer={
        <>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t("users.cancel")}
          </Button>
          <Button variant="primary" disabled={busy || !device} onClick={() => void send()}>
            {t("clientlogs.send")}
          </Button>
        </>
      }
    >
      <TextField label={t("clientlogs.note")} hint={t("clientlogs.noteHint")} value={note} onChange={(e) => setNote(e.target.value)} maxLength={200} autoComplete="off" />
      <label className="flex cursor-pointer items-center gap-3">
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="text-[13px] font-bold">{t("clientlogs.detailed")}</span>
          <span className="text-xs leading-snug text-muted">{t("clientlogs.detailedHint")}</span>
        </span>
        <Switch checked={debug} onCheckedChange={setDebug} />
      </label>
      <p className="flex items-start gap-2 text-xs leading-snug text-muted">
        <Icon name="clock" size={14} className="mt-px flex-none" />
        {t("clientlogs.ttl")}
      </p>
    </Modal>
  );
}

/** Who asked and what the note says, or that the person sent the bundle on their own: the lines above the records. */
function Provenance({ log, t }: { log: ClientLog; t: Tx }) {
  return (
    <div className="flex flex-col gap-1 text-xs leading-snug text-muted">
      {log.unasked ? <p>{t("clientlogs.unaskedNote")}</p> : <p>{t("clientlogs.askedBy", { who: log.actor || "—" })}</p>}
      {log.note && (
        <p dir="auto" className="[overflow-wrap:anywhere] whitespace-pre-wrap text-fg">
          {t("common.quoted", { name: log.note })}
        </p>
      )}
      {log.droppedAttrs > 0 && <p>{t("clientlogs.dropped", { n: log.droppedAttrs })}</p>}
    </div>
  );
}

/** What the viewer shows of a bundle: who asked and the note, the filters, the records, the download. */
export function LogViewerBody({ log, onDownload, downloading, shot }: { log: ClientLog; onDownload: (id: string) => void; downloading: boolean; shot?: string }) {
  const t = useTx();
  return (
    <>
      <Provenance log={log} t={t} />
      <LogViewer
        shot={shot}
        queryKey={["ops", "client-debug", "records", log.id]}
        load={(f, after) => clientLogPage(log.id, f, after)}
        span={log.firstUnixMs > 0 ? { fromMs: log.firstUnixMs, toMs: log.lastUnixMs } : undefined}
        actions={
          <Button variant="secondary" size="md" disabled={downloading} onClick={() => onDownload(log.id)}>
            <Icon name="arrowDown" size={14} />
            <span>{t("clientlogs.download")}</span>
          </Button>
        }
      />
    </>
  );
}

/** The line under the title of a bundle: when it came, how many lines, from which app version. */
export const aboutText = (log: ClientLog, t: Tx, stamp: (unix: number) => string) => [stamp(log.closedUnix), t.n("clientlogs.lines", log.records), appText(log.app)].filter(Boolean).join(" · ");

/** The bundle open in a wide window. */
export function LogViewerModal({ log, open, onOpenChange, onDownload, downloading, shot }: { log: ClientLog | null; open: boolean; onOpenChange: (o: boolean) => void; onDownload: (id: string) => void; downloading: boolean; shot?: string }) {
  const t = useTx();
  const fmt = useFmt();
  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      wide
      shot={shot}
      closeLabel={t("clientlogs.close")}
      title={t("clientlogs.viewerT", { name: log ? deviceLabel(log) : "" })}
      description={log ? aboutText(log, t, fmt.stamp) : undefined}
    >
      {log && <LogViewerBody key={log.id} log={log} onDownload={onDownload} downloading={downloading} />}
    </Modal>
  );
}

/** "Mistgate/0.3.1" read as "Mistgate 0.3.1". */
export const appText = (app: string) => app.split("/").slice(0, 2).join(" ");

/** The device of a request as the panel named it: its model, else its platform. */
export const deviceLabel = (l: Pick<ClientLog, "deviceModel" | "devicePlatform">) => l.deviceModel || l.devicePlatform || "—";

/**
 * Everything the person's page needs of logs for support: the owner's list (nothing for anyone else, and nothing on the edition
 * without it, so there `ctl` is undefined and no menu item or section is drawn), and the windows, drawn by `dialogs`.
 */
export function useClientLogs(userId: string): { ctl: LogsCtl | undefined; dialogs: ReactNode } {
  const t = useTx();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const list = useQuery({ ...clientLogsQuery(userId), enabled: owner });
  const actions = useClientLogActions();
  // the windows keep their subject while they fade out, so their text does not go blank
  const [asking, setAsking] = useState<Asking | null>(null);
  const [askOpen, setAskOpen] = useState(false);
  const [viewing, setViewing] = useState<ClientLog | null>(null);
  const [viewOpen, setViewOpen] = useState(false);
  const [deleting, setDeleting] = useState<ClientLog | null>(null);
  const [deleteOpen, setDeleteOpen] = useState(false);

  if (!owner || !list.data || (list.isError && isUnavailable(list.error))) return { ctl: undefined, dialogs: null };
  const ctl: LogsCtl = {
    logs: list.data.items,
    ask: (device) => {
      setAsking(device);
      setAskOpen(true);
    },
    cancel: (id) => actions.cancel.mutate(id),
    open: (log) => {
      setViewing(log);
      setViewOpen(true);
    },
    download: (id) => actions.download.mutate(id),
    remove: (log) => {
      setDeleting(log);
      setDeleteOpen(true);
    },
  };
  const bundle = deleting !== null && (logView(deleting) === "received" || logView(deleting) === "unasked");
  const dialogs = (
    <>
      <RequestLogsModal
        device={asking}
        open={askOpen}
        onOpenChange={setAskOpen}
        onSend={(note, debug) => actions.request.mutateAsync({ deviceId: asking!.id, note, debug })}
      />
      <LogViewerModal log={viewing} open={viewOpen} onOpenChange={setViewOpen} onDownload={ctl.download} downloading={actions.download.isPending} />
      <ConfirmModal
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        title={t(bundle ? "clientlogs.deleteT" : "clientlogs.deleteEntryT")}
        description={t(bundle ? "clientlogs.deleteBody" : "clientlogs.deleteEntryBody")}
        confirmLabel={t("clientlogs.delete")}
        danger
        onConfirm={() => actions.remove.mutateAsync(deleting!.id)}
      />
    </>
  );
  return { ctl, dialogs };
}
