import { queryOptions } from "@tanstack/react-query";
import { probe } from "@/lib/api";

// "Measure the channel" of a node (screens/node/channel-measure.tsx): the owner's own action, never a schedule. The panel runs 8 parallel
// streams for 10 seconds in each direction against up to three other nodes (ProbeService.ChannelMeasure*), and this file is what the
// screen reads of it: the plan, the run and the number it offers for the node's channel field.

/** What a run says right now; the panel keeps the run of a node in memory, so after a restart it reads "idle" and the stored last measure. */
export type RunState = "idle" | "running" | "done" | "failed" | "canceled";

export const channelPlanQuery = (nodeId: string) =>
  queryOptions({ queryKey: ["channel-plan", nodeId], queryFn: () => probe.channelMeasurePlan({ nodeId }), staleTime: 0, gcTime: 0 });

/** The run and the stored last measure; while a run is going it is read every second. */
export const channelStatusQuery = (nodeId: string) =>
  queryOptions({
    queryKey: ["channel-status", nodeId],
    queryFn: () => probe.channelMeasureStatus({ nodeId }),
    refetchInterval: (q) => (q.state.data?.state === "running" ? 1000 : false),
  });

/** The steps of a run in the order the panel runs them (a partner at a time, receiving then sending). */
export type Step = { partner: string; direction: string; state: string; mbitS: number; error: string; streams: number; capped: boolean };

/**
 * The step to say "now" about: the one that runs, else the first that waits. A finished run has none.
 */
export function currentStep(steps: readonly Step[]): Step | undefined {
  return steps.find((s) => s.state === "running") ?? steps.find((s) => s.state === "waiting");
}

/** How many steps are over (done or failed) of all of them. */
export function progressOf(steps: readonly Step[]): { over: number; total: number } {
  return { over: steps.filter((s) => s.state === "done" || s.state === "failed").length, total: steps.length };
}

/**
 * The step of a number written as a plan is, going down: under 10 as it is, under 100 to 5, under 1000 to 10, above to 50. A measure
 * is an estimate at one moment, so the number offered for the field is rounded DOWN: "930" says what the link was seen to carry, "937"
 * would only look more exact, and a number rounded up would promise what was not seen.
 */
export function floorMbps(measured: number): number {
  const step = measured < 10 ? 1 : measured < 100 ? 5 : measured < 1000 ? 10 : 50;
  return Math.min(1_000_000, Math.floor(measured / step) * step);
}

/** The slower of the two directions, as for the field everywhere else (every byte a person downloads also goes out to them). 0 when a direction did not work. */
export function slowerSide(down: number, up: number): number {
  return down > 0 && up > 0 ? Math.min(down, up) : 0;
}

/**
 * What the screen offers for the channel field after a run: the slower side floored, 0 when there is nothing to offer (a direction did
 * not work, or the figure rounds to nothing). `same` says the number written already is that one.
 */
export function offer(configured: number, down: number, up: number): { value: number; same: boolean } {
  const value = floorMbps(slowerSide(down, up));
  return { value, same: value > 0 && value === configured };
}

/** The number typed in the field of the offer: a whole number of 1..1 000 000, else null. */
export function parseLimit(text: string): number | null {
  const s = text.trim();
  if (!/^\d{1,7}$/.test(s)) return null;
  const n = Number(s);
  return n >= 1 && n <= 1_000_000 ? n : null;
}

/** Minutes to wait out of the seconds the panel says, at least one. */
export const minutesOf = (seconds: number) => Math.max(1, Math.ceil(seconds / 60));

/** The words of the panel's error codes in the dictionary: an unknown one is "failed". */
export const errorCodes = ["node_offline", "agent_too_old", "busy", "too_soon", "no_partner", "limit", "unreachable", "timeout", "no_route", "canceled", "failed"] as const;
export type ErrorCode = (typeof errorCodes)[number];
export const errorCodeOf = (code: string): ErrorCode => ((errorCodes as readonly string[]).includes(code) ? (code as ErrorCode) : "failed");
