import { useQueryClient, type QueryKey } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import type { StatusKind } from "@/components/ui/status";

// One call over several rows, as the bulk bars of the Nodes and Users pages run it: at most `limit` at once, in the order
// given. Stop lets the calls in flight finish and starts no more (a started call is never cut off: it may be halfway
// through an SSH login). Every call goes through step-up (one shared dialog); a declined step-up stops the batch. A
// failed row does not stop the others, and Retry runs the failed and the never-started ones again. A server that takes
// many ids in one call (`many`) is called for one slice of the rows after another instead.

export type RowState = "waiting" | "running" | "ok" | "bad" | "skipped" | "notStarted";

/** What a finished call says about its node beyond "done": a status of its own (a check's verdict), its word, a line under it, a link. */
export type Outcome = { kind?: StatusKind; label?: string; note?: string; link?: boolean };

/** What a row is: its id, its name and where it is ("" when there is nothing to add to the name, as for a person). */
export type Target = { id: string; name: string; host: string };
export type Skipped = Target & { note: string };
export type Row = Target & Outcome & { state: RowState };

export type Spec = {
  /** Which action: the panel's title, icon and summary. */
  action: string;
  limit: number;
  /** One node's call. Without one the server already did them all in one call (SetNodesDecoyMode): the rows start done. */
  call?: (id: string) => Promise<Outcome | void>;
  /**
   * One server call per slice of the rows (SetUsersEnabled, ExtendUsers, ResetUserTraffic, DeleteUsers): `chunk` ids at a
   * time, one slice after another; an outcome for an id overrides "done" (a person gone meanwhile). A slice that fails
   * fails its rows, and the next slice goes on.
   */
  many?: (ids: string[]) => Promise<Record<string, Outcome> | void>;
  chunk?: number;
  /** A refusal in words, or an outcome that is not a failure (a key that was not there to unbind). */
  error: (e: unknown, id: string) => string | Outcome;
  /** Read again once the batch ends. */
  invalidate?: QueryKey[];
};

export type Job = { spec: Spec; rows: Row[]; running: boolean; stopping: boolean };

/** Runs `work` for every item, at most `limit` at a time, starting them in order; `stopped()` is asked before each start. */
export async function runQueue<T>(items: readonly T[], limit: number, work: (item: T) => Promise<void>, stopped: () => boolean): Promise<void> {
  let next = 0;
  const lane = async () => {
    while (next < items.length && !stopped()) await work(items[next++]!);
  };
  await Promise.all(Array.from({ length: Math.max(1, Math.min(limit, items.length)) }, lane));
}

const defaultChunk = 200;

export function useBatch() {
  const guard = useStepUp();
  const qc = useQueryClient();
  const [job, setJob] = useState<Job | null>(null);
  const stopped = useRef(false);
  const busy = useRef(false);

  // leaving the page mid-batch: what runs finishes, nothing new starts
  useEffect(
    () => () => {
      stopped.current = true;
    },
    [],
  );

  const patch = (id: string, row: Partial<Row>) => setJob((j) => j && { ...j, rows: j.rows.map((r) => (r.id === id ? { ...r, ...row } : r)) });
  /** Every row of a slice at once (one render for a slice of 200); `row` may differ per id. */
  const patchSlice = (ids: readonly string[], row: (id: string) => Partial<Row>) => {
    const set = new Set(ids);
    setJob((j) => j && { ...j, rows: j.rows.map((r) => (set.has(r.id) ? { ...r, ...row(r.id) } : r)) });
  };

  async function run(spec: Spec, ids: string[]) {
    busy.current = true;
    stopped.current = false;
    const call = spec.call;
    const many = spec.many;
    if (many) {
      const size = Math.max(1, spec.chunk ?? defaultChunk);
      const slices = Array.from({ length: Math.ceil(ids.length / size) }, (_, i) => ids.slice(i * size, (i + 1) * size));
      await runQueue(
        slices,
        1,
        async (slice) => {
          patchSlice(slice, () => ({ state: "running" }));
          try {
            const per = (await guard(() => many(slice))) ?? {};
            patchSlice(slice, (id) => ({ state: "ok", ...per[id] }));
          } catch (e) {
            if (isStepUpCancelled(e)) {
              // the owner said no: this slice and the rest never start
              stopped.current = true;
              patchSlice(slice, () => ({ state: "notStarted" }));
              return;
            }
            patchSlice(slice, (id) => {
              const said = spec.error(e, id);
              return typeof said === "string" ? { state: "bad", note: said } : { state: "ok", ...said };
            });
          }
        },
        () => stopped.current,
      );
    } else if (call) {
      await runQueue(
        ids,
        spec.limit,
        async (id) => {
          patch(id, { state: "running" });
          try {
            patch(id, { state: "ok", ...(await guard(() => call(id))) });
          } catch (e) {
            if (isStepUpCancelled(e)) {
              // the owner said no: this node and the rest never start
              stopped.current = true;
              patch(id, { state: "notStarted" });
              return;
            }
            const said = spec.error(e, id);
            patch(id, typeof said === "string" ? { state: "bad", note: said } : { state: "ok", ...said });
          }
        },
        () => stopped.current,
      );
    }
    busy.current = false;
    setJob((j) => j && { ...j, running: false, stopping: false, rows: j.rows.map((r) => (r.state === "waiting" ? { ...r, state: "notStarted" } : r)) });
    for (const queryKey of spec.invalidate ?? []) void qc.invalidateQueries({ queryKey });
  }

  return {
    job,
    running: !!job?.running,
    /** Starts a batch over `targets`; `skipped` are listed with their reason and never called. One batch at a time. */
    start(spec: Spec, targets: readonly Target[], skipped: readonly Skipped[] = []) {
      if (busy.current) return;
      const works = !!(spec.call || spec.many);
      const rows: Row[] = [...targets.map((x) => ({ ...x, state: works ? ("waiting" as const) : ("ok" as const) })), ...skipped.map((x) => ({ ...x, state: "skipped" as const }))];
      setJob({ spec, rows, running: works, stopping: false });
      void run(spec, targets.map((x) => x.id));
    },
    /** Finishes the calls in flight and starts no more; the rest read "Not started". */
    stop() {
      stopped.current = true;
      setJob((j) => j && { ...j, stopping: true });
    },
    /** The failed and the never-started rows again, with the same call. */
    retry() {
      if (busy.current || !job) return;
      const again = job.rows.filter((r) => r.state === "bad" || r.state === "notStarted").map((r) => r.id);
      if (again.length === 0) return;
      setJob({ ...job, running: true, stopping: false, rows: job.rows.map((r) => (again.includes(r.id) ? { id: r.id, name: r.name, host: r.host, state: "waiting" } : r)) });
      void run(job.spec, again);
    },
    close() {
      if (!busy.current) setJob(null);
    },
  };
}

export type Batch = ReturnType<typeof useBatch>;
