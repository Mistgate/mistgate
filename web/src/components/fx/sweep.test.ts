import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setEffectsEnabled } from "@/lib/effects";
import { resetPainter } from "./renderer";
import { activeSweeps, cancelSweep, MAX_CONCURRENT, sweep } from "./sweep";

const realRect = HTMLElement.prototype.getBoundingClientRect;
// no WebGL in jsdom (and no noise about it): the CSS painter draws
HTMLCanvasElement.prototype.getContext = (() => null) as typeof HTMLCanvasElement.prototype.getContext;
beforeAll(() => {
  HTMLElement.prototype.getBoundingClientRect = function () {
    return { x: 10, y: 10, left: 10, top: 10, right: 410, bottom: 90, width: 400, height: 80, toJSON: () => ({}) } as DOMRect;
  };
});
afterAll(() => {
  HTMLElement.prototype.getBoundingClientRect = realRect;
});

const make = () => {
  const el = document.createElement("div");
  document.body.append(el);
  return el;
};
const layer = () => document.querySelector("[data-fx-layer]");

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["requestAnimationFrame", "cancelAnimationFrame", "setTimeout", "clearTimeout", "performance"] });
  setEffectsEnabled(true);
});
afterEach(() => {
  document.body.innerHTML = "";
  resetPainter();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  localStorage.clear();
});

describe("sweep()", () => {
  it("hides the content at once, reveals it behind the band, then puts everything back and leaves nothing running", async () => {
    const el = make();
    el.style.maskImage = "";
    let done = false;
    const p = sweep(el, { duration: 1000, fadeOut: 200 }).then(() => (done = true));
    expect(el.style.maskImage).toContain("linear-gradient"); // before the first frame: no flash of the finished state
    expect(activeSweeps()).toBe(1);

    await vi.advanceTimersByTimeAsync(500);
    expect(layer()).not.toBeNull(); // the overlay is in the page while it plays
    expect(done).toBe(false);
    const mid = el.style.maskImage;
    await vi.advanceTimersByTimeAsync(300);
    expect(el.style.maskImage).not.toBe(mid); // the mask follows the band

    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(done).toBe(true);
    expect(el.style.maskImage).toBe("");
    expect(layer()).toBeNull();
    expect(activeSweeps()).toBe(0);
    expect(vi.getTimerCount()).toBeLessThanOrEqual(1); // at most the painter's idle release
  });

  it("is skipped, with no work, when the interface effects are off", async () => {
    setEffectsEnabled(false);
    const el = make();
    await sweep(el);
    expect(el.style.maskImage).toBe("");
    expect(activeSweeps()).toBe(0);
    expect(layer()).toBeNull();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("is a quick fade, not a sweep, under reduced motion", async () => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("reduce"), media: q }));
    const el = make();
    const finish: { cb?: () => void } = {};
    const animate = vi.fn(() => ({
      set onfinish(f: () => void) {
        finish.cb = f;
      },
      set oncancel(_f: () => void) {},
    }));
    (el as unknown as { animate: unknown }).animate = animate;
    const p = sweep(el);
    await Promise.resolve();
    expect(animate).toHaveBeenCalledWith([{ opacity: 0 }, { opacity: 1 }], expect.objectContaining({ duration: 160 }));
    expect(el.style.maskImage).toBe("");
    expect(activeSweeps()).toBe(0);
    await Promise.resolve();
    finish.cb?.();
    await p;
  });

  it("cancels cleanly: the element is as it was and the promise resolves", async () => {
    const el = make();
    const p = sweep(el, { duration: 1000 });
    await vi.advanceTimersByTimeAsync(300);
    cancelSweep(el);
    await p;
    expect(el.style.maskImage).toBe("");
    expect(activeSweeps()).toBe(0);
    expect(layer()).toBeNull();
  });

  it("can be cancelled with a signal", async () => {
    const el = make();
    const ac = new AbortController();
    const p = sweep(el, { duration: 1000, signal: ac.signal });
    await vi.advanceTimersByTimeAsync(100);
    ac.abort();
    await p;
    expect(activeSweeps()).toBe(0);
    expect(el.style.maskImage).toBe("");
  });

  it("ends by itself when the element is removed mid-sweep", async () => {
    const el = make();
    const p = sweep(el, { duration: 1000 });
    await vi.advanceTimersByTimeAsync(200);
    el.remove();
    await vi.advanceTimersByTimeAsync(50);
    await p;
    expect(activeSweeps()).toBe(0);
    expect(layer()).toBeNull();
  });

  it("plays one effect per element: a new sweep replaces the running one", async () => {
    const el = make();
    const first = sweep(el, { duration: 1000 });
    await vi.advanceTimersByTimeAsync(200);
    const second = sweep(el, { duration: 1000 });
    await first; // replaced: resolved at once
    expect(activeSweeps()).toBe(1);
    await vi.advanceTimersByTimeAsync(1500);
    await second;
    expect(activeSweeps()).toBe(0);
    expect(el.style.maskImage).toBe("");
  });

  it("skips what is asked for beyond the concurrent limit, leaving that content as it is", async () => {
    const els = Array.from({ length: MAX_CONCURRENT + 2 }, make);
    els.forEach((e) => void sweep(e, { duration: 500 }));
    expect(activeSweeps()).toBe(MAX_CONCURRENT);
    expect(els[MAX_CONCURRENT]!.style.maskImage).toBe("");
    await vi.advanceTimersByTimeAsync(1000);
    expect(activeSweeps()).toBe(0);
  });

  it("keeps the content hidden while it waits for a hold, then plays", async () => {
    const el = make();
    let release!: () => void;
    const hold = new Promise<void>((r) => (release = r));
    const p = sweep(el, { duration: 400, fadeOut: 100, hold });
    await vi.advanceTimersByTimeAsync(2000);
    expect(el.style.maskImage).toContain("linear-gradient"); // still hidden, not yet started
    expect(activeSweeps()).toBe(1);
    release();
    await vi.advanceTimersByTimeAsync(800);
    await p;
    expect(el.style.maskImage).toBe("");
  });

  it("does not mask when reveal is off (the glow only)", async () => {
    const el = make();
    const p = sweep(el, { reveal: false, duration: 300, fadeOut: 100 });
    expect(el.style.maskImage).toBe("");
    await vi.advanceTimersByTimeAsync(600);
    await p;
  });

  it("sweeps right to left on a right-to-left element by default", async () => {
    const el = make();
    el.style.direction = "rtl";
    void sweep(el, { duration: 400 });
    expect(el.style.maskImage).toContain("to left");
    cancelSweep(el);
    const ltr = make();
    void sweep(ltr, { duration: 400 });
    expect(ltr.style.maskImage).toContain("to right");
    cancelSweep(ltr);
  });
});
