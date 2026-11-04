import { act, createElement, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { StepUpCancelled } from "@/components/step-up";
import { runQueue, useBatch, type Batch, type Spec } from "./batch";

// The queue under the Nodes page's bulk bar: at most `limit` calls at once, started in order, a stop that lets the started
// ones finish, a failure that does not stop the rest, a declined step-up that does, and Retry for what failed or never ran.

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

/** A call per id that waits until the test lets it go. */
function gate() {
  const open = new Map<string, () => void>();
  const started: string[] = [];
  let inFlight = 0;
  let most = 0;
  const work = (id: string) =>
    new Promise<void>((resolve) => {
      started.push(id);
      most = Math.max(most, ++inFlight);
      open.set(id, () => {
        inFlight--;
        resolve();
      });
    });
  return { work, started, open: (id: string) => open.get(id)!(), most: () => most };
}
const tick = () => new Promise((r) => setTimeout(r, 0));

describe("runQueue", () => {
  it("runs at most `limit` at once, starting them in order", async () => {
    const g = gate();
    const done = runQueue(["a", "b", "c", "d", "e"], 2, g.work, () => false);
    await tick();
    expect(g.started).toEqual(["a", "b"]);
    g.open("b");
    await tick();
    expect(g.started).toEqual(["a", "b", "c"]);
    for (const id of ["a", "c"]) g.open(id);
    await tick();
    for (const id of ["d", "e"]) g.open(id);
    await done;
    expect(g.started).toEqual(["a", "b", "c", "d", "e"]);
    expect(g.most()).toBe(2);
  });

  it("one at a time with a limit of 1", async () => {
    const g = gate();
    const done = runQueue(["a", "b", "c"], 1, g.work, () => false);
    for (const id of ["a", "b", "c"]) {
      await tick();
      expect(g.started.at(-1)).toBe(id);
      g.open(id);
    }
    await done;
    expect(g.most()).toBe(1);
  });

  it("stops starting new ones but lets the started ones finish", async () => {
    const g = gate();
    let stop = false;
    const done = runQueue(["a", "b", "c"], 1, g.work, () => stop);
    await tick();
    stop = true;
    g.open("a");
    await done;
    expect(g.started).toEqual(["a"]);
  });

  it("does nothing for no items", async () => {
    const work = vi.fn();
    await runQueue([], 4, work, () => false);
    expect(work).not.toHaveBeenCalled();
  });
});

let root: Root | null = null;
afterEach(() => {
  act(() => root?.unmount());
  root = null;
});

async function mountBatch(): Promise<() => Batch> {
  let batch!: Batch;
  function Probe() {
    const b = useBatch();
    useEffect(() => {
      batch = b;
    });
    return null;
  }
  const host = document.createElement("div");
  root = createRoot(host);
  await act(async () => root!.render(createElement(QueryClientProvider, { client: new QueryClient() }, createElement(Probe))));
  return () => batch;
}
const settle = () => act(async () => void (await tick()));
const targets = ["de1", "nl1", "fi1"].map((name) => ({ id: `nod_${name}`, name, host: `${name}.example.com` }));
const states = (b: Batch) => b.job!.rows.map((r) => `${r.name}:${r.state}${r.note ? `:${r.note}` : ""}`);

describe("useBatch", () => {
  it("goes on past a failed node, lists the skipped, and retries only what failed", async () => {
    const batch = await mountBatch();
    const call = vi.fn(async (id: string) => {
      if (id === "nod_nl1") throw new Error("refused");
    });
    const spec: Spec = { action: "ports", limit: 1, call, error: (e) => (e as Error).message };
    await act(async () => batch().start(spec, targets, [{ id: "nod_se1", name: "se1", host: "se1.example.com", note: "Offline" }]));
    await settle();
    expect(call.mock.calls.map((c) => c[0])).toEqual(["nod_de1", "nod_nl1", "nod_fi1"]);
    expect(states(batch())).toEqual(["de1:ok", "nl1:bad:refused", "fi1:ok", "se1:skipped:Offline"]);
    expect(batch().running).toBe(false);

    call.mockClear();
    call.mockResolvedValue(undefined);
    await act(async () => batch().retry());
    await settle();
    expect(call.mock.calls.map((c) => c[0])).toEqual(["nod_nl1"]);
    expect(states(batch())).toEqual(["de1:ok", "nl1:ok", "fi1:ok", "se1:skipped:Offline"]);
  });

  it("a declined step-up stops the batch: that node and the rest read Not started", async () => {
    const batch = await mountBatch();
    const call = vi.fn(async (id: string) => {
      if (id === "nod_nl1") throw new StepUpCancelled();
    });
    await act(async () => batch().start({ action: "password", limit: 1, call, error: () => "x" }, targets));
    await settle();
    expect(call).toHaveBeenCalledTimes(2);
    expect(states(batch())).toEqual(["de1:ok", "nl1:notStarted", "fi1:notStarted"]);
  });

  it("stop finishes the node in progress and starts no more; Retry picks up the rest", async () => {
    const batch = await mountBatch();
    let release!: () => void;
    const call = vi.fn((id: string) => (id === "nod_de1" ? new Promise<void>((r) => (release = r)) : Promise.resolve()));
    await act(async () => batch().start({ action: "doctor", limit: 1, call, error: () => "x" }, targets));
    expect(states(batch())).toEqual(["de1:running", "nl1:waiting", "fi1:waiting"]);
    await act(async () => batch().stop());
    expect(batch().job!.stopping).toBe(true);
    await act(async () => release());
    await settle();
    expect(call).toHaveBeenCalledTimes(1);
    expect(states(batch())).toEqual(["de1:ok", "nl1:notStarted", "fi1:notStarted"]);
    await act(async () => batch().retry());
    await settle();
    expect(states(batch())).toEqual(["de1:ok", "nl1:ok", "fi1:ok"]);
  });

  it("an outcome that is not a failure keeps the row done, with its own word", async () => {
    const batch = await mountBatch();
    const call = vi.fn(async () => {
      throw new Error("ssh_public_key_not_found");
    });
    await act(async () => batch().start({ action: "unbindKey", limit: 1, call, error: () => ({ kind: "off", label: "Was not there" }) }, targets.slice(0, 1)));
    await settle();
    expect(batch().job!.rows[0]).toMatchObject({ state: "ok", kind: "off", label: "Was not there" });
  });

  it("runs one batch at a time, and Close waits for its end", async () => {
    const batch = await mountBatch();
    let release!: () => void;
    const call = vi.fn(() => new Promise<void>((r) => (release = r)));
    await act(async () => batch().start({ action: "doctor", limit: 1, call, error: () => "x" }, targets.slice(0, 1)));
    const other = vi.fn(async () => {});
    await act(async () => batch().start({ action: "ports", limit: 1, call: other, error: () => "x" }, targets));
    await act(async () => batch().close());
    expect(other).not.toHaveBeenCalled();
    expect(batch().job?.spec.action).toBe("doctor");
    await act(async () => release());
    await settle();
    await act(async () => batch().close());
    expect(batch().job).toBeNull();
  });
});

describe("useBatch, one server call per slice of the rows", () => {
  const people = (n: number) => Array.from({ length: n }, (_, i) => ({ id: `usr_${i}`, name: `p${i}`, host: "" }));
  const sizes = (calls: string[][]) => calls.map((c) => c.length);

  it("calls 450 ids as 200, 200 and 50, one slice after another", async () => {
    const batch = await mountBatch();
    const calls: string[][] = [];
    let release!: () => void;
    const many = vi.fn((ids: string[]) => {
      calls.push(ids);
      return new Promise<void>((r) => (release = r));
    });
    await act(async () => batch().start({ action: "enable", limit: 1, many, error: () => "x" }, people(450)));
    // the first slice runs, the rest wait their turn
    expect(sizes(calls)).toEqual([200]);
    expect(batch().job!.rows.filter((r) => r.state === "running")).toHaveLength(200);
    expect(batch().job!.rows.filter((r) => r.state === "waiting")).toHaveLength(250);
    await act(async () => release());
    await settle();
    expect(sizes(calls)).toEqual([200, 200]);
    await act(async () => release());
    await settle();
    await act(async () => release());
    await settle();
    expect(sizes(calls)).toEqual([200, 200, 50]);
    expect(calls[0]![0]).toBe("usr_0");
    expect(calls[2]![0]).toBe("usr_400");
    expect(batch().running).toBe(false);
    expect(batch().job!.rows.every((r) => r.state === "ok")).toBe(true);
  });

  it("a failed slice fails its rows with the reason and the next one goes on; Retry runs only the failed", async () => {
    const batch = await mountBatch();
    const many = vi.fn(async (ids: string[]) => {
      if (ids[0] === "usr_3") throw new Error("busy");
    });
    const spec: Spec = { action: "reset", limit: 1, chunk: 3, many, error: (e) => (e as Error).message };
    await act(async () => batch().start(spec, people(8)));
    await settle();
    expect(many.mock.calls.map((c) => c[0])).toEqual([
      ["usr_0", "usr_1", "usr_2"],
      ["usr_3", "usr_4", "usr_5"],
      ["usr_6", "usr_7"],
    ]);
    expect(batch().job!.rows.map((r) => r.state)).toEqual(["ok", "ok", "ok", "bad", "bad", "bad", "ok", "ok"]);
    expect(batch().job!.rows[4]!.note).toBe("busy");

    many.mockClear();
    many.mockResolvedValue(undefined);
    await act(async () => batch().retry());
    await settle();
    expect(many.mock.calls.map((c) => c[0])).toEqual([["usr_3", "usr_4", "usr_5"]]);
    expect(batch().job!.rows.every((r) => r.state === "ok")).toBe(true);
  });

  it("Stop lets the slice in flight finish and starts no more; Retry picks up the slices that never started", async () => {
    const batch = await mountBatch();
    let release!: () => void;
    const many = vi.fn((ids: string[]) => (ids[0] === "usr_0" ? new Promise<void>((r) => (release = r)) : Promise.resolve()));
    await act(async () => batch().start({ action: "delete", limit: 1, chunk: 2, many, error: () => "x" }, people(5)));
    await act(async () => batch().stop());
    await act(async () => release());
    await settle();
    expect(many).toHaveBeenCalledTimes(1);
    expect(batch().job!.rows.map((r) => r.state)).toEqual(["ok", "ok", "notStarted", "notStarted", "notStarted"]);
    many.mockClear();
    await act(async () => batch().retry());
    await settle();
    expect(many.mock.calls.map((c) => c[0])).toEqual([["usr_2", "usr_3"], ["usr_4"]]);
    expect(batch().job!.rows.every((r) => r.state === "ok")).toBe(true);
  });

  it("a declined step-up leaves that slice and the rest not started", async () => {
    const batch = await mountBatch();
    const many = vi.fn(async (ids: string[]) => {
      if (ids[0] === "usr_2") throw new StepUpCancelled();
    });
    await act(async () => batch().start({ action: "delete", limit: 1, chunk: 2, many, error: () => "x" }, people(6)));
    await settle();
    expect(many).toHaveBeenCalledTimes(2);
    expect(batch().job!.rows.map((r) => r.state)).toEqual(["ok", "ok", "notStarted", "notStarted", "notStarted", "notStarted"]);
  });

  it("an outcome for one id overrides done (a person who was gone by then), and the rest read done", async () => {
    const batch = await mountBatch();
    const many = vi.fn(async () => ({ usr_1: { kind: "off" as const, label: "Gone" } }));
    await act(async () => batch().start({ action: "enable", limit: 1, many, error: () => "x" }, people(3)));
    await settle();
    expect(batch().job!.rows.map((r) => `${r.state}:${r.label ?? ""}`)).toEqual(["ok:", "ok:Gone", "ok:"]);
  });

  it("takes the skipped into the rows without calling them", async () => {
    const batch = await mountBatch();
    const many = vi.fn(async (_ids: string[]) => {});
    await act(async () => batch().start({ action: "enable", limit: 1, many, error: () => "x" }, people(2), [{ id: "usr_9", name: "p9", host: "", note: "Already on" }]));
    await settle();
    expect(many.mock.calls[0]![0]).toEqual(["usr_0", "usr_1"]);
    expect(batch().job!.rows.map((r) => r.state)).toEqual(["ok", "ok", "skipped"]);
  });
});
