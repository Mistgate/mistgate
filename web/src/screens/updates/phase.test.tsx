import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import type { Updates } from "@/lib/updates";
import { usePanelPhase, type PanelPhase } from "./panel";

// The page follows a panel update by what it can see: a click, `installing`, the panel not answering, a new version. The panel
// reports no step, so the page never invents one.

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const data = (version: string, installing = false): Updates =>
  ({ panel: { version, update: { version: "v0.1.86", installing, available: true } }, panelNotes: [{ version: "v0.1.86" }] }) as unknown as Updates;

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let phase: PanelPhase = { kind: "idle" };
let begin = () => {};
let notes: unknown;
const refetch = vi.fn();

function Probe({ d, answeredAt }: { d: Updates; answeredAt: number }) {
  const r = usePanelPhase(d, refetch, answeredAt);
  useEffect(() => {
    phase = r.phase;
    begin = r.begin;
    notes = r.notes;
  });
  return null;
}
const show = (d: Updates, answeredAt = Date.now()) => act(async () => root!.render(<ToastProvider><Probe d={d} answeredAt={answeredAt} /></ToastProvider>));
const tick = (ms: number) => act(async () => void (await vi.advanceTimersByTimeAsync(ms)));

beforeEach(() => {
  vi.useFakeTimers();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  refetch.mockReset();
  phase = { kind: "idle" };
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  vi.useRealTimers();
});

describe("following a panel update", () => {
  it("is idle until the update is asked for", async () => {
    await show(data("v0.1.71"));
    expect(phase).toEqual({ kind: "idle" });
    await tick(60_000);
    expect(refetch).not.toHaveBeenCalled();
  });

  it("is a run from the click, with no step, and asks the panel again every few seconds", async () => {
    await show(data("v0.1.71"));
    await act(async () => begin());
    expect(phase).toMatchObject({ kind: "run", from: "v0.1.71", to: "v0.1.86", slow: false });
    expect(phase).not.toHaveProperty("step");
    await tick(9_500);
    expect(refetch).toHaveBeenCalledTimes(3);
  });

  it("is a run while the panel says it is installing, and the same when it was not this page that asked", async () => {
    await show(data("v0.1.71", true));
    expect(phase).toMatchObject({ kind: "run", from: "v0.1.71", to: "v0.1.86", slow: false });
  });

  it("goes on while the panel does not answer (it is restarting), and offers a reload after two minutes", async () => {
    const start = Date.now();
    await show(data("v0.1.71"), start);
    await act(async () => begin());
    await tick(60_000);
    await show(data("v0.1.71"), start); // nothing answered since the click
    expect(phase).toMatchObject({ kind: "run", slow: false });
    await tick(65_000);
    await show(data("v0.1.71"), start);
    expect(phase).toMatchObject({ kind: "run", slow: true });
  });

  it("says the new version answered, holds that for a moment, then looks like a panel that was just updated, with the notes it had", async () => {
    await show(data("v0.1.71"));
    await act(async () => begin());
    await show(data("v0.1.86"));
    expect(phase).toEqual({ kind: "out", from: "v0.1.71", to: "v0.1.86" });
    expect(notes).toEqual([{ version: "v0.1.86" }]);
    await tick(1_600);
    expect(phase).toEqual({ kind: "done", from: "v0.1.71", to: "v0.1.86" });
  });

  it("ends the run when the panel answers on the old version and is not installing: it went back", async () => {
    await show(data("v0.1.71"));
    await act(async () => begin());
    await tick(20_000);
    await show(data("v0.1.71", false), Date.now());
    expect(phase).toEqual({ kind: "idle" });
  });

  it("does not end the run in the first moments, when the panel has not yet marked itself as installing", async () => {
    await show(data("v0.1.71"));
    await act(async () => begin());
    await tick(3_000);
    await show(data("v0.1.71", false), Date.now());
    expect(phase.kind).toBe("run");
  });

  it("lets a scene name the phase, and then asks nobody", async () => {
    const scene = { phase: { kind: "run", from: "a", to: "b", step: 2, slow: false } as PanelPhase };
    function Scene() {
      const r = usePanelPhase(data("v0.1.71"), refetch, 0, scene);
      useEffect(() => {
        phase = r.phase;
      });
      return null;
    }
    await act(async () => root!.render(<ToastProvider><Scene /></ToastProvider>));
    expect(phase).toEqual(scene.phase);
    await tick(20_000);
    expect(refetch).not.toHaveBeenCalled();
  });
});
