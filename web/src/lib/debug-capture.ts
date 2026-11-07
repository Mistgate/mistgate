import { keepPreviousData, queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import type { DebugCaptureInfo, GetDebugCaptureResponse } from "@/gen/mistgate/ops/v1/ops_pb";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { ops } from "./api";
import { errorText } from "./errors";
import { plain, type Plain } from "./plain";

// The debug capture of one node (proto/mistgate/ops/v1, agent.proto "DEBUG CAPTURE"): the owner raises the node's log level for a
// few minutes, the node ships its cleaned records, the panel keeps them 24 hours. The words are in i18n/debug.ts.

export type DebugCapture = Plain<DebugCaptureInfo>;
export type DebugRecords = Plain<GetDebugCaptureResponse>;

/** The log sources a capture may name (what the agent labels its records with); none picked means all. */
export const debugSources = ["agent", "hysteria2", "vless", "xray", "awg", "update"] as const;
/** The lengths the button offers; the panel takes 1 to 900 seconds. */
export const debugMinutes = [1, 5, 10, 15] as const;
export const debugLevels = ["debug", "info"] as const;
export type DebugLevel = (typeof debugLevels)[number];

export type DebugFilter = { minLevel: "" | "info" | "warn" | "error"; source: string; query: string };
export const defaultDebugFilter: DebugFilter = { minLevel: "", source: "", query: "" };

/** The node's captures, newest first; the list refreshes every few seconds while one is running (its records are on their way). */
export const debugListQuery = (nodeId: string) =>
  queryOptions({
    queryKey: ["ops", "debug", "list", nodeId],
    queryFn: async () => plain(await ops.listDebugCaptures({ nodeId })),
    refetchInterval: (q) => (q.state.data?.captures.some(isLive) ? 4_000 : false),
    retry: false,
  });

const pageSize = 100;

/** One page of a capture's records, oldest first; `after` is the cursor of the previous page (0 = from the first). */
export async function debugPage(captureId: string, f: DebugFilter, after: number): Promise<DebugRecords> {
  return plain(
    await ops.getDebugCapture({ captureId, minLevel: f.minLevel, source: f.source, query: f.query.trim().slice(0, 100), limit: pageSize, afterSeq: BigInt(after) }),
  );
}

export const debugRecordsQuery = (captureId: string, f: DebugFilter, state: string) =>
  queryOptions({
    queryKey: ["ops", "debug", "records", captureId, f, state],
    queryFn: () => debugPage(captureId, f, 0),
    placeholderData: keepPreviousData,
    retry: false,
  });

/** Seconds left of a running capture at `nowMs` (never below 0). */
export const secondsLeft = (endsUnix: number, nowMs: number) => Math.max(0, Math.ceil(endsUnix - nowMs / 1000));

/** "4:07" for a countdown. */
export function clockLeft(seconds: number): string {
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

/** Whether the node may still be capturing or shipping. */
export function isLive(c: DebugCapture | undefined): boolean {
  return c?.state === "running";
}

/** Starting and stopping, with the toasts. Both refresh the node's list. */
export function useDebugActions(nodeId: string, nodeName: string) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: ["ops", "debug"] });
  const start = useMutation({
    mutationFn: (req: { level: DebugLevel; sources: string[]; minutes: number }) =>
      ops.startDebugCapture({ nodeId, level: req.level, sources: req.sources, durationS: req.minutes * 60 }),
    onSuccess: () => {
      toast(t("debug.started", { node: nodeName }));
      void refresh();
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  const stop = useMutation({
    mutationFn: (captureId: string) => ops.stopDebugCapture({ captureId }),
    onSuccess: () => {
      toast(t("debug.stopped"));
      void refresh();
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  return { start, stop };
}
