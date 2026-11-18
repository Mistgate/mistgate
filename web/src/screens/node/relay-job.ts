import { useSyncExternalStore } from "react";
import type { Candidate, RelayMsg } from "@/lib/relay";
import { runOne, type Entry, type Run, type RunCtx } from "./relay-run";

// The run of "Make reachable from…" (relay-wizard.tsx) outlives its window: the relays of a run are made and turned on one after another, and the owner may
// close the window and go on. The run lives here, by the exit it is for, and the window and the strip over the ways (relay-strip.tsx) read it. The page
// keeps no timers of its own: a run stops when the panel is closed, and what it left (relays made and checked but off) the strip finds again in the data.

export type JobItem = { c: Candidate; /** The relay of this server through this entry that is there already, off. */ existing?: RelayMsg; /** The port asked for on the entry; none: the panel chooses. */ port?: string };

export type Job = {
  exitId: string;
  exitName: string;
  exitCc: string;
  entry: Entry;
  /** The servers of the run, in the order they go. */
  items: readonly JobItem[];
  /** The run goes over a bridge: the exit holds the connection to the entry. */
  bridge: boolean;
  runs: Readonly<Record<string, Run>>;
  /** How many servers are finished (on, or said why not). */
  done: number;
  /** How many of those are on. */
  on: number;
  state: "running" | "stopping" | "finished" | "stopped";
};

const jobs = new Map<string, Job>();
const stops = new Set<string>();
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());
const put = (job: Job) => {
  jobs.set(job.exitId, job);
  emit();
};

export const jobOf = (exitId: string): Job | undefined => jobs.get(exitId);

const watchers = new Map<string, number>();
/** A window is on the run's last step and shows how it goes: the end of the run is no news to tell in a toast. Returns what takes it back. */
export function watch(exitId: string): () => void {
  watchers.set(exitId, (watchers.get(exitId) ?? 0) + 1);
  return () => watchers.set(exitId, Math.max(0, (watchers.get(exitId) ?? 1) - 1));
}
export const isWatched = (exitId: string): boolean => (watchers.get(exitId) ?? 0) > 0;

/** The run for an exit, now: undefined when there is none. */
export function useJob(exitId: string): Job | undefined {
  return useSyncExternalStore(
    (cb) => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    },
    () => jobs.get(exitId),
    () => undefined,
  );
}

/** What the run says when it ends: the window may be closed by then, and the toast is how the owner hears of it. */
export type JobEnd = (job: Job) => void;

/**
 * Starts the run: every server one after another through `runOne`. A second run for the same exit is refused while one goes. `onEnd` is told once,
 * when the last server is through or after "Stop"; the entry in the store stays until `dismissJob`.
 */
export function startJob(ctx: RunCtx, exit: { id: string; name: string; cc: string }, items: readonly JobItem[], bridge: boolean, onEnd?: JobEnd): void {
  const live = jobs.get(exit.id);
  if (live && (live.state === "running" || live.state === "stopping")) return;
  stops.delete(exit.id);
  put({ exitId: exit.id, exitName: exit.name, exitCc: exit.cc, entry: ctx.entry, items, bridge, runs: {}, done: 0, on: 0, state: "running" });
  void (async () => {
    for (const it of items) {
      if (stops.has(exit.id)) break;
      const key = it.c.inboundId;
      const ok = await runOne(
        ctx,
        it.c,
        true,
        it.port ?? "",
        (patch) => {
          const job = jobs.get(exit.id);
          if (job) put({ ...job, runs: { ...job.runs, [key]: { ...({ leg: "waiting", path: "waiting" } satisfies Run), ...job.runs[key], ...patch } } });
        },
        it.existing,
      );
      const job = jobs.get(exit.id)!;
      put({ ...job, done: job.done + 1, on: job.on + (ok ? 1 : 0) });
    }
    const job = jobs.get(exit.id)!;
    const stopped = stops.has(exit.id) && job.done < items.length;
    stops.delete(exit.id);
    put({ ...job, state: stopped ? "stopped" : "finished" });
    // the relays changed on the panel: the lanes and the servers read them again
    await ctx.qc.invalidateQueries({ queryKey: ["relays"] });
    onEnd?.(jobs.get(exit.id)!);
  })();
}

/**
 * One server of a run that is over, run again on another port ("Take 8444" when the one asked for was taken): the run is on its way again for
 * the time it takes, and ends as it ended, with this server's new result counted.
 */
export function retryItem(ctx: RunCtx, exitId: string, inboundId: string, port: string, onEnd?: JobEnd): void {
  const job = jobs.get(exitId);
  const it = job?.items.find((i) => i.c.inboundId === inboundId);
  if (!job || !it || job.state !== "finished") return;
  const { [inboundId]: _gone, ...rest } = job.runs;
  void _gone;
  put({ ...job, runs: rest, done: job.done - 1, state: "running" });
  void (async () => {
    const ok = await runOne(
      ctx,
      it.c,
      true,
      port,
      (patch) => {
        const j = jobs.get(exitId);
        if (j) put({ ...j, runs: { ...j.runs, [inboundId]: { ...({ leg: "waiting", path: "waiting" } satisfies Run), ...j.runs[inboundId], ...patch } } });
      },
      it.existing,
    );
    const j = jobs.get(exitId)!;
    put({ ...j, done: j.done + 1, on: j.on + (ok ? 1 : 0), state: "finished" });
    await ctx.qc.invalidateQueries({ queryKey: ["relays"] });
    onEnd?.(jobs.get(exitId)!);
  })();
}

/** "Stop": the server that runs is finished, the ones after it are left as they were. */
export function stopJob(exitId: string): void {
  const job = jobs.get(exitId);
  if (!job || job.state !== "running") return;
  stops.add(exitId);
  put({ ...job, state: "stopping" });
}

/** The window was closed over a run that is over, or the run was heard of in a toast: it goes. */
export function dismissJob(exitId: string): void {
  const job = jobs.get(exitId);
  if (!job || job.state === "running" || job.state === "stopping") return;
  jobs.delete(exitId);
  emit();
}

/** Development and tests: a run in the state a scene wants, or none. */
export function setJob(job: Job | undefined, exitId?: string): void {
  if (job) jobs.set(job.exitId, job);
  else if (exitId) jobs.delete(exitId);
  else jobs.clear();
  stops.clear();
  emit();
}
