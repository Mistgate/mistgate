import { describe, expect, it, vi } from "vitest";
import { BundleStatus, NodeUpdateState, RolloutStatus, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import type { Updates } from "@/lib/updates";
import { fleetCards, rolloutNow } from "./fleet";
import { runMatrix } from "./matrix";
import { releaseItems } from "./notes";
import { trackOf } from "./track";



describe("releaseItems", () => {
  it("gives a line to every bullet, with the word of its section, and keeps a nested bullet under its line", () => {
    const items = releaseItems(["## Features", "", "- **Fleet map:** paths between nodes.", "  - per device", "", "## Fixes", "", "- A fix.", "- Another fix.", "", "## Security", "", "- A hole."].join("\n"));
    expect(items.map((i) => [i.tag, i.label, i.sub.length])).toEqual([
      ["new", "Features", 1],
      ["fix", "Fixes", 0],
      ["fix", "Fixes", 0],
      ["sec", "Security", 0],
    ]);
  });

  it("knows the words of the sections in the languages the releases are written in", () => {
    expect(releaseItems("## Исправления\n\n- x").map((i) => i.tag)).toEqual(["fix"]);
    expect(releaseItems("## 新功能\n\n- x").map((i) => i.tag)).toEqual(["new"]);
    expect(releaseItems("## Безопасность\n\n- x").map((i) => i.tag)).toEqual(["sec"]);
  });

  it("keeps a section it does not know under its own name, a paragraph as a line, and a text with no section as lines with no word", () => {
    const [docs] = releaseItems("## Docs\n\n- A page.");
    expect([docs!.tag, docs!.label]).toEqual(["", "Docs"]);
    expect(releaseItems("Just a sentence about the release.").map((i) => [i.tag, i.label])).toEqual([["", ""]]);
    expect(releaseItems("")).toEqual([]);
  });
});

describe("trackOf", () => {
  it("ticks the steps before the one under way and fills the line to it", () => {
    const t = trackOf({ run: true, step: 2 });
    expect(t.steps.map((s) => s.c)).toEqual(["ok", "ok", "now", ""]);
    expect(t.width).toBe(67);
  });
  it("crosses the last step of an update that went back, and the first of one whose signature did not match", () => {
    expect(trackOf({ back: true }).steps.map((s) => s.c)).toEqual(["ok", "ok", "ok", "bad"]);
    expect(trackOf({ back: true }).width).toBe(100);
    expect(trackOf({ sig: true }).steps.map((s) => s.c)).toEqual(["bad", "", "", ""]);
    expect(trackOf({}).steps.every((s) => s.c === "")).toBe(true);
  });
});

const node = (id: string, state: NodeUpdateState, over: Record<string, unknown> = {}) => ({ nodeId: `nod_${id}`, name: id, version: "v1", built: 1, state, onlineUsers: 1, inbounds: 1, ...over });
const step = (id: string, stage: number, state: StepState, over: Record<string, unknown> = {}) => ({ nodeId: `nod_${id}`, nodeName: id, stage, state, finishedUnix: 0, errorKey: "", params: {}, downloadPhase: "", downloadedBytes: 0, totalBytes: 0, ...over });
const data = (nodes: unknown[], rollout: unknown = null) => ({ nowUnix: 1000, nodes, rollout, bundle: { status: BundleStatus.TRUSTED, version: "v2", built: 2 } }) as unknown as Updates;
const run = (steps: unknown[], status = RolloutStatus.RUNNING) => ({ id: "rol_1", status, toVersion: "v2", toBuilt: 2, steps });

describe("fleetCards", () => {
  it("colours a node by where it stands, and names the quietest outdated one as the canary, first", () => {
    const cards = fleetCards(data([node("de1", NodeUpdateState.UP_TO_DATE), node("fi1", NodeUpdateState.OUTDATED, { onlineUsers: 0 }), node("nl1", NodeUpdateState.OUTDATED, { onlineUsers: 4 }), node("kz1", NodeUpdateState.OFFLINE)]));
    expect(cards.map((c) => [c.name, c.kind, c.word, c.canary])).toEqual([
      ["fi1", "part", "can", true],
      ["de1", "ok", "ok", false],
      ["nl1", "part", "can", false],
      ["kz1", "off", "off", false],
    ]);
    expect(cards[0]!.wordVersion).toBe("v2");
  });

  it("takes a running rollout's step over the node's own state, with how far the agent got", () => {
    const d = data(
      [node("nl1", NodeUpdateState.UP_TO_DATE), node("de1", NodeUpdateState.UPDATING), node("fi1", NodeUpdateState.OUTDATED), node("kz1", NodeUpdateState.UPDATING)],
      run([step("nl1", 0, StepState.PASSED), step("de1", 1, StepState.SENT, { downloadPhase: "downloading", downloadedBytes: 50, totalBytes: 100 }), step("fi1", 2, StepState.PENDING), step("kz1", 1, StepState.GATING)]),
    );
    const by = Object.fromEntries(fleetCards(d).map((c) => [c.name, c]));
    expect(by.nl1).toMatchObject({ kind: "ok", canary: true });
    expect(by.de1).toMatchObject({ kind: "now", word: "sent", pg: 35, canary: false });
    expect(by.kz1).toMatchObject({ kind: "now", word: "gate", pg: 70 });
    expect(by.fi1).toMatchObject({ kind: "", word: "wait", canary: false });
    expect(fleetCards(d)[0]!.name).toBe("nl1");
  });

  it("marks a node that failed or was rolled back, and flashes one that has just passed", () => {
    const rolled = fleetCards(data([node("fi1", NodeUpdateState.ROLLED_BACK, { version: "v1" })], run([step("fi1", 0, StepState.ROLLED_BACK)], RolloutStatus.PAUSED)));
    expect(rolled[0]).toMatchObject({ kind: "bad", word: "back", wordVersion: "v1" });
    const failed = fleetCards(data([node("fi1", NodeUpdateState.FAILED)]));
    expect(failed[0]).toMatchObject({ kind: "bad", word: "failed" });
    const fresh = fleetCards(data([node("fi1", NodeUpdateState.UP_TO_DATE)], run([step("fi1", 0, StepState.PASSED, { finishedUnix: 990 })], RolloutStatus.DONE)));
    expect(fresh[0]!.just).toBe(true);
    const old = fleetCards(data([node("fi1", NodeUpdateState.UP_TO_DATE)], run([step("fi1", 0, StepState.PASSED, { finishedUnix: 100 })], RolloutStatus.DONE)));
    expect(old[0]!.just).toBe(false);
  });

  it("says a node that needs a hand is waiting for one", () => {
    expect(fleetCards(data([node("old1", NodeUpdateState.UNSUPPORTED)]))[0]).toMatchObject({ kind: "part", word: "manual" });
  });
});

describe("rolloutNow", () => {
  it("says what the rollout does this moment: the canary being installed, a node being checked, a batch, or that it waits", () => {
    const at = (steps: unknown[]) => rolloutNow(run(steps) as never);
    expect(at([step("fi1", 0, StepState.SENT)])).toMatchObject({ kind: "canary", node: "fi1" });
    expect(at([step("fi1", 0, StepState.PASSED), step("de1", 1, StepState.GATING)])).toMatchObject({ kind: "gate", node: "de1" });
    expect(at([step("fi1", 0, StepState.PASSED), step("de1", 1, StepState.SENT), step("nl1", 1, StepState.SENT)])).toMatchObject({ kind: "batch", names: "de1, nl1" });
    expect(at([step("fi1", 0, StepState.PASSED), step("de1", 1, StepState.PENDING)])).toMatchObject({ kind: "wait", node: "" });
  });
});

describe("runMatrix", () => {
  /** A canvas with a size and a 2D context that counts what is drawn. */
  const canvas = (w = 200, h = 100) => {
    const calls = { rects: 0, clears: 0, styles: new Set<string>() };
    const ctx = {
      setTransform: vi.fn(),
      clearRect: () => void calls.clears++,
      fillRect: () => void calls.rects++,
      set fillStyle(v: string) {
        calls.styles.add(v);
      },
    };
    const cv = { clientWidth: w, clientHeight: h, width: 0, height: 0, getContext: () => ctx } as unknown as HTMLCanvasElement;
    return { cv, calls };
  };
  const loop = (n: number) => {
    let t = 0;
    const queue: FrameRequestCallback[] = [];
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => queue.push(cb));
    vi.stubGlobal("cancelAnimationFrame", () => void (queue.length = 0));
    return () => {
      for (let i = 0; i < n; i++) {
        t += 40;
        queue.splice(0).forEach((cb) => cb(t));
      }
    };
  };

  it("draws dots on the canvas frame after frame, in the colour of the mode, and stops when asked", () => {
    const advance = loop(6);
    const { cv, calls } = canvas();
    const stop = runMatrix(cv, () => ({ k: 1, mode: "", light: false }));
    advance();
    expect(cv.width).toBeGreaterThan(0);
    expect(calls.clears).toBeGreaterThan(1);
    expect(calls.rects).toBeGreaterThan(50);
    expect([...calls.styles].some((s) => s.startsWith("rgba(125,211,160"))).toBe(true);
    const drawn = calls.clears;
    stop();
    advance();
    expect(calls.clears).toBe(drawn);
    vi.unstubAllGlobals();
  });

  it("turns amber when the panel is slow to answer, and takes the light theme's greens", () => {
    const advance = loop(4);
    const slow = canvas();
    runMatrix(slow.cv, () => ({ k: 2, mode: "slow", light: false }));
    advance();
    expect([...slow.calls.styles].some((s) => s.startsWith("rgba(245,185,90"))).toBe(true);
    const light = canvas();
    runMatrix(light.cv, () => ({ k: 0, mode: "", light: true }));
    advance();
    expect([...light.calls.styles].some((s) => s.startsWith("rgba(16,140,100"))).toBe(true);
    vi.unstubAllGlobals();
  });

  it("waits for a canvas that has no size yet, and draws nothing on it", () => {
    const advance = loop(3);
    const { cv, calls } = canvas(0, 0);
    runMatrix(cv, () => ({ k: 0, mode: "", light: false }));
    advance();
    expect(calls.clears).toBe(0);
    vi.unstubAllGlobals();
  });
});


