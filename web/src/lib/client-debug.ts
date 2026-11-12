import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { useToast } from "@/components/ui/toast";
import type { ClientDebugInfo, GetClientDebugResponse } from "@/gen/mistgate/ops/v1/ops_pb";
import type { RecordFilter } from "@/components/log-viewer";
import { useT } from "@/i18n";
import { ops } from "./api";
import { errorText } from "./errors";
import { plain, type Plain } from "./plain";

// The logs people send for support (proto/mistgate/ops/v1, "LOGS FOR SUPPORT"): the owner asks one device of the Mistgate app for
// its logs, the person sends them or refuses, the panel keeps them 7 days. A person can also send their logs on their own. The
// words are in i18n/clientlogs.ts. Only the owner asks, reads, downloads or deletes; the Cloudflare edition has none of it
// (OpsService answers UNIMPLEMENTED, see isUnavailable).

export type ClientLog = Plain<ClientDebugInfo>;
export type ClientLogPage = Plain<GetClientDebugResponse>;

/** How a row reads to the owner. "unasked" is a bundle the person sent on their own. */
export type LogView = "open" | "received" | "unasked" | "declined" | "cancelled" | "expired";

/** The state of a request or bundle as the owner sees it: an open request whose time has passed reads as expired, as the panel says it. */
export function logView(l: Pick<ClientLog, "state" | "unasked" | "expiresUnix">, nowSec = Math.floor(Date.now() / 1000)): LogView {
  if (l.state === "received") return l.unasked ? "unasked" : "received";
  if (l.state === "open") return l.expiresUnix <= nowSec ? "expired" : "open";
  if (l.state === "declined" || l.state === "cancelled" || l.state === "expired") return l.state;
  return "expired";
}

/** The request that is open on a device (at most one), if any. */
export const openRequestOf = (logs: readonly ClientLog[], deviceId: string, nowSec?: number) =>
  logs.find((l) => l.deviceId === deviceId && logView(l, nowSec) === "open");

/** The newest request or bundle of a device that is worth a badge beside it: an open request, else the newest answer (received, declined). */
export function badgeOf(logs: readonly ClientLog[], deviceId: string, nowSec?: number): ClientLog | undefined {
  const mine = logs.filter((l) => l.deviceId === deviceId);
  return mine.find((l) => logView(l, nowSec) === "open") ?? mine.find((l) => ["received", "unasked", "declined"].includes(logView(l, nowSec)));
}

/** Requests and bundles of a person (everyone when empty), newest first. */
export const clientLogsQuery = (userId: string) =>
  queryOptions({
    queryKey: ["ops", "client-debug", "list", userId],
    queryFn: async () => plain(await ops.listClientDebug({ userId })),
    retry: false,
  });

const pageSize = 100;

/** One page of a bundle's records, oldest first; `after` is the cursor of the previous page (0 = from the first). */
export async function clientLogPage(id: string, f: RecordFilter, after: number): Promise<ClientLogPage> {
  return plain(
    await ops.getClientDebug({
      id,
      minLevel: f.minLevel,
      source: f.source,
      query: f.query.trim().slice(0, 100),
      fromMs: BigInt(f.fromMs),
      toMs: BigInt(f.toMs),
      limit: pageSize,
      afterSeq: BigInt(after),
    }),
  );
}

/** The file name of a download: the id only, never a word the person wrote. */
export const clientLogFileName = (id: string) => `mistgate-client-logs-${id.replace(/[^A-Za-z0-9_-]/g, "")}.ndjson`;

/** Streams the NDJSON of a bundle into one Blob and saves it. Asks for a step-up first if the session needs one (the guard repeats the call). */
export async function downloadClientLogs(guard: <T>(call: () => Promise<T>) => Promise<T>, id: string): Promise<void> {
  const parts = await guard(async () => {
    const out: Uint8Array<ArrayBuffer>[] = [];
    for await (const chunk of ops.exportClientDebug({ id })) out.push(new Uint8Array(chunk.data));
    return out;
  });
  const url = URL.createObjectURL(new Blob(parts, { type: "application/x-ndjson" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = clientLogFileName(id);
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

/** Asking, withdrawing, deleting and downloading, with the toasts. Each refreshes the lists. */
export function useClientLogActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const refresh = () => qc.invalidateQueries({ queryKey: ["ops", "client-debug", "list"] });
  const onError = (e: unknown) => {
    if (!isStepUpCancelled(e)) toast.error(errorText(e, t));
  };
  const request = useMutation({
    mutationFn: (req: { deviceId: string; note: string; debug: boolean }) =>
      ops.requestClientDebug({ deviceId: req.deviceId, note: req.note.trim(), level: req.debug ? "debug" : "info" }),
    onSuccess: () => {
      toast(t("clientlogs.asked"));
      void refresh();
    },
    onError,
  });
  const cancel = useMutation({
    mutationFn: (id: string) => ops.cancelClientDebug({ id }),
    onSuccess: () => {
      toast(t("clientlogs.cancelled"));
      void refresh();
    },
    onError,
  });
  const remove = useMutation({
    mutationFn: (id: string) => ops.deleteClientDebug({ id }),
    onSuccess: () => {
      toast(t("clientlogs.deleted"));
      void qc.invalidateQueries({ queryKey: ["ops", "client-debug"] });
    },
    onError,
  });
  const download = useMutation({
    mutationFn: (id: string) => downloadClientLogs(guard, id),
    onSuccess: () => toast(t("clientlogs.downloaded")),
    onError,
  });
  return { request, cancel, remove, download };
}
