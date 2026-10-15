import { Code, ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import type { SelectOption } from "@/components/ui/select";
import type { StatusKind } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import type { TestTspuProbeResponse, TspuProbeRun, TspuProbeSettings as SettingsMsg } from "@/gen/mistgate/admin/v1/tspu_probe_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { tspuProbe } from "./api";
import type { Fmt } from "./format";
import { sourceErrorText } from "./globalping";
import { hasKey } from "./health";
import { plain, type Plain } from "./plain";

// The TSPU probe card of the "Reachability from a country" group (tspu_probe.proto): one of the owner's own nodes inside Russia,
// the probe node, finds the TSPU box on its path and tests whether the SYNs to the other nodes die there. Owner only; switching,
// the probe node, the interval and Disconnect need a step-up, Test does not. A Test runs for minutes; the client sets no deadline
// on it. The server answers with short codes; the words are in i18n/country.ts.

export type TspuSettings = Plain<SettingsMsg>;
export type TspuRun = Plain<TspuProbeRun>;
export type TspuTest = Plain<TestTspuProbeResponse>;
export type TspuNode = TspuSettings["nodes"][number];

export const tspuQuery = queryOptions({
  queryKey: ["tspu-probe"],
  queryFn: async ({ signal }) => plain((await tspuProbe.getTspuProbe({}, { signal })).settings!),
});

/** Minutes between runs on offer (the server takes 30..1440, default 120); a stored value outside the list is shown too. */
export const tspuIntervals = [30, 60, 120, 240, 360, 720, 1440] as const;
export const tspuDefaultInterval = 120;

const errorKeys: Record<string, MessageKey> = {
  tspu_off: "tp.err.off",
  tspu_unavailable_for_country: "tp.err.country",
  tspu_no_probe_node: "tp.err.noNode",
  tspu_probe_node_unfit: "tp.err.unfit",
  tspu_run_in_progress: "tp.err.running",
  probe_node_unknown: "tp.err.nodeUnknown",
  probe_node_offline: "tp.err.nodeOffline",
  probe_node_not_capable: "tp.err.nodeOld",
  "interval_min: 30..1440": "tp.err.interval",
};

/** A failed call of the card in words; a second Test within the minute says how long to wait ("try again in 42 s" from the server). */
export function tspuErrorText(e: unknown, t: T): string {
  const c = ConnectError.from(e);
  if (c.code === Code.ResourceExhausted) return t("tp.tooSoon", { s: Number(/in (\d+) s/.exec(c.rawMessage)?.[1] ?? 60) });
  return sourceErrorText(e, t, errorKeys);
}

/** A Test that is not a failure of the probe: asked again within the minute, or a scheduled run going on right now. */
export const isNotAFailure = (e: unknown) => {
  const c = ConnectError.from(e);
  return c.code === Code.ResourceExhausted || c.rawMessage === "tspu_run_in_progress";
};

/**
 * What the last Test said: its run and the verdict per node, or the error. `at` is the run's own time, or for an error the time
 * the Test started, so a run stored after it (the answer was lost on the way, a scheduled run came later) wins.
 */
export type TspuOutcome = { at: number } & ({ ok: TspuTest } | { error: unknown });

export type TspuState =
  | { kind: "off" }
  | { kind: "unavailable"; reason: string }
  | { kind: "noNode" }
  | { kind: "untested" }
  | { kind: "running" }
  | { kind: "run"; run: TspuRun; nodes?: TspuTest["nodes"] }
  | { kind: "fails"; error: unknown };

/**
 * The state line of the card. The Cloudflare edition has no probe at all; off stays off whatever the country; another country
 * than Russia says so. Then a Test in flight, a missing probe node, and the newer of the last Test and the stored run.
 */
export function tspuState(s: TspuSettings, test: TspuOutcome | null, running: boolean): TspuState {
  if (s.unavailableReason === "edition") return { kind: "unavailable", reason: "edition" };
  if (!s.enabled) return { kind: "off" };
  if (!s.available) return { kind: "unavailable", reason: s.unavailableReason };
  if (running) return { kind: "running" };
  if (!s.probeNodeId) return { kind: "noNode" };
  const stored = s.lastRun;
  if (test && (!stored || test.at >= stored.atUnix)) {
    if ("error" in test) return { kind: "fails", error: test.error };
    if (test.ok.run) return { kind: "run", run: test.ok.run, nodes: test.ok.nodes };
  }
  if (stored) return { kind: "run", run: stored };
  return { kind: "untested" };
}

/** Why a run failed (TspuProbeRun.detail) in words; a code this page does not know is shown as sent. */
const failText = (t: T, detail: string) => (detail && hasKey(`tp.fail.${detail}`) ? t(`tp.fail.${detail}` as MessageKey) : detail);

/**
 * A run in a dot, a word and a line: "Works · last run 12 min ago · TSPU after hop 6 · reference 31.13.72.36 · 3 nodes
 * checked", or what went wrong and when. The card's state line and the line under the Health matrix.
 */
export function runLook(t: T, fmt: Fmt, run: TspuRun): { kind: StatusKind; word: string; detail: string } {
  const at = t("tp.run.at", { ago: fmt.ago(run.atUnix) });
  const line = (...parts: string[]) => parts.filter(Boolean).join(" · ");
  switch (run.status) {
    case "ok":
      return {
        kind: "ok",
        word: t("cr.state.works"),
        detail: line(at, run.dpiHop ? t("tp.run.hop", { hop: run.dpiHop }) : "", run.blockedRef ? t("tp.run.ref", { ref: run.blockedRef }) : "", t.n("tp.run.nodes", run.nodesChecked)),
      };
    case "control_failed":
    case "no_dpi_hop":
      return { kind: "warn", word: t(`tp.run.${run.status}`), detail: line(t(`tp.run.${run.status}.why`), at) };
    case "busy":
      return { kind: "blip", word: t("tp.run.busy"), detail: line(t("tp.run.busy.why"), at) };
    default: // "failed", and a status this page does not know
      return { kind: "bad", word: t("cr.state.fails"), detail: line(failText(t, run.detail), at) };
  }
}

/**
 * The probe-node picker: the server's order (nodes in the selected country first). A node that cannot be the probe node now is
 * greyed out with the reason: its agent does not declare country-probe/1 ("update the agent"), or it is offline. With none chosen
 * the list starts with "not chosen"; a chosen node the server no longer lists stays, greyed, under its id.
 */
export function probeNodeOptions(t: T, fmt: Fmt, nodes: readonly TspuNode[], current: string): SelectOption[] {
  const options: SelectOption[] = nodes.map((n) => ({
    value: n.id,
    label: n.name,
    hint: [!n.declares ? t("tp.node.old") : "", !n.online ? t("tp.node.offline") : "", n.countryCode ? fmt.country(n.countryCode) : ""].filter(Boolean).join(" · "),
    disabled: !n.declares || !n.online,
  }));
  if (!current) return [{ value: "", label: t("tp.node.none") }, ...options];
  if (!nodes.some((n) => n.id === current)) return [{ value: current, label: current, disabled: true }, ...options];
  return options;
}

export function useTspuActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const fail = (e: unknown) => {
    if (!isStepUpCancelled(e)) toast.error(tspuErrorText(e, t));
  };
  // the verdicts on Health and the node pages follow at once: off hides the probe's word, a run stores new ones
  const fresh = () => void qc.invalidateQueries({ queryKey: ["health", "checks"] });
  const settle = (s: TspuSettings) => {
    qc.setQueryData(tspuQuery.queryKey, s);
    fresh();
  };

  // every change asks for a fresh step-up; only the fields that change are sent
  const update = useMutation({
    mutationFn: async (req: { enabled?: boolean; probeNodeId?: string; intervalMin?: number }) => plain((await guard(() => tspuProbe.updateTspuProbe(req))).settings!),
    onSuccess: (s, req) => {
      settle(s);
      toast(req.enabled !== undefined ? t(s.enabled ? "tp.on" : "tp.off") : t("common.saved"));
    },
    onError: fail,
  });
  const disconnect = useMutation({
    mutationFn: async () => plain((await guard(() => tspuProbe.disconnectTspuProbe({}))).settings!),
    onSuccess: (s) => {
      settle(s);
      toast(t("tp.disconnected"));
    },
    onError: fail,
  });
  // no step-up, as on the other cards. It runs for minutes; whatever it ends with, the stored run is read again, because it is
  // newer than a lost answer
  const test = useMutation({
    mutationFn: async () => plain(await tspuProbe.testTspuProbe({})),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: tspuQuery.queryKey });
      fresh();
    },
  });
  return { update, disconnect, test };
}
