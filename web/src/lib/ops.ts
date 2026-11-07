import { Code, ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { useToast } from "@/components/ui/toast";
import type { PanelLogEntry, RetentionItem } from "@/gen/mistgate/ops/v1/ops_pb";
import { useT } from "@/i18n";
import { ops } from "./api";
import { errorText } from "./errors";

// The panel's own diagnostics on Settings -> System (proto/mistgate/ops/v1): the log level and recent logs, retention, the
// export and the metrics switch. The VPS edition serves them; the Cloudflare edition answers UNIMPLEMENTED, which is how a page
// knows to say so instead of showing a card that cannot work.

export const logLevels = ["debug", "info", "warn", "error"] as const;
export type LogLevel = (typeof logLevels)[number];
export const isLogLevel = (v: string): v is LogLevel => (logLevels as readonly string[]).includes(v);

/** The panel does not have this part (the Cloudflare edition). */
export const isUnavailable = (e: unknown) => ConnectError.from(e).code === Code.Unimplemented;

export const logSettingsQuery = queryOptions({
  queryKey: ["ops", "log-settings"],
  queryFn: () => ops.getLogSettings({}),
  retry: false,
});

export type LogFilter = { minLevel: LogLevel; minutes: number; query: string; component: string; code: string; requestId: string };
export const defaultLogFilter: LogFilter = { minLevel: "info", minutes: 60, query: "", component: "", code: "", requestId: "" };

/** One page of logs, newest first; `before` is the cursor of the previous page (0 = from the newest). */
export const logsQuery = (f: LogFilter, live: boolean) =>
  queryOptions({
    queryKey: ["ops", "logs", f],
    queryFn: () =>
      ops.getPanelLogs({
        minLevel: f.minLevel,
        sinceUnix: f.minutes > 0 ? BigInt(Math.floor(Date.now() / 1000) - f.minutes * 60) : 0n,
        query: f.query.trim().slice(0, 100),
        component: f.component.trim(),
        code: f.code.trim(),
        requestId: f.requestId.trim(),
        limit: 100,
      }),
    refetchInterval: live ? 5_000 : false,
    retry: false,
  });

/** The next page after `cursor` with the same filter. */
export function olderLogs(f: LogFilter, cursor: bigint) {
  return ops.getPanelLogs({
    minLevel: f.minLevel,
    sinceUnix: f.minutes > 0 ? BigInt(Math.floor(Date.now() / 1000) - f.minutes * 60) : 0n,
    query: f.query.trim().slice(0, 100),
    component: f.component.trim(),
    code: f.code.trim(),
    requestId: f.requestId.trim(),
    limit: 100,
    beforeSeq: cursor,
  });
}

export const retentionQuery = queryOptions({ queryKey: ["ops", "retention"], queryFn: () => ops.getRetention({}), retry: false });
export const metricsQuery = queryOptions({ queryKey: ["ops", "metrics"], queryFn: () => ops.getMetricsSettings({}), retry: false });

/** The retention fields the owner changed, as the RPC takes them: only those whose draft differs from the stored value. */
export function changedRetention(items: readonly RetentionItem[], draft: Record<string, string>): Record<string, number> {
  const out: Record<string, number> = {};
  for (const i of items) {
    const raw = draft[i.key];
    if (raw === undefined || raw.trim() === "") continue;
    const v = Number(raw);
    if (Number.isInteger(v) && v !== i.value) out[i.key] = v;
  }
  return out;
}

/** What is wrong with a draft value, if anything: not a whole number, or outside the limits. */
export function retentionProblem(i: RetentionItem, raw: string): boolean {
  if (raw.trim() === "") return false;
  const v = Number(raw);
  return !Number.isInteger(v) || v < i.min || v > i.max;
}

/** The file name of an export: "mistgate-export-2026-11-07.ndjson". */
export const exportFileName = (now = new Date()) => `mistgate-export-${now.toISOString().slice(0, 10)}.ndjson`;

/** Streams the export into one Blob. Asks for a step-up first if the session needs one (the guard repeats the call). */
export async function downloadExport(guard: <T>(call: () => Promise<T>) => Promise<T>, hours: number): Promise<void> {
  const now = Math.floor(Date.now() / 1000);
  const parts = await guard(async () => {
    const out: Uint8Array<ArrayBuffer>[] = [];
    for await (const chunk of ops.exportData({ sinceUnix: BigInt(now - hours * 3600), untilUnix: BigInt(now), sources: [] })) {
      out.push(new Uint8Array(chunk.data));
    }
    return out;
  });
  const url = URL.createObjectURL(new Blob(parts, { type: "application/x-ndjson" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = exportFileName();
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

/** Changes that ask for a step-up and say so in a toast when they went through; a cancelled dialog is not an error. */
export function useOpsActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const onError = (e: unknown) => {
    if (!isStepUpCancelled(e)) toast.error(errorText(e, t));
  };

  const setLevel = useMutation({
    mutationFn: (level: LogLevel) => ops.setLogLevel({ level }),
    onSuccess: (r) => {
      toast(t("obs.logs.levelSet", { level: t(`obs.level.${isLogLevel(r.level) ? r.level : "info"}`) }));
      void qc.invalidateQueries({ queryKey: ["ops"] });
    },
    onError,
  });
  const saveRetention = useMutation({
    mutationFn: (values: Record<string, number>) => guard(() => ops.setRetention({ values })),
    onSuccess: () => {
      toast(t("obs.ret.saved"));
      void qc.invalidateQueries({ queryKey: retentionQuery.queryKey });
    },
    onError,
  });
  const setMetrics = useMutation({
    mutationFn: (req: { enabled?: boolean; rotateToken?: boolean }) => guard(() => ops.setMetricsSettings(req)),
    onSuccess: () => {
      toast(t("obs.met.saved"));
      void qc.invalidateQueries({ queryKey: metricsQuery.queryKey });
    },
    onError,
  });
  const exportNow = useMutation({
    mutationFn: (hours: number) => downloadExport(guard, hours),
    onSuccess: () => toast(t("obs.exp.done")),
    onError,
  });
  return { setLevel, saveRetention, setMetrics, exportNow };
}

export type { PanelLogEntry };
