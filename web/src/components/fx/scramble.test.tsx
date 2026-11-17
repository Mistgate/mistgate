import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Scramble } from "./scramble";

// A figure that scrambles in: the text is there from the first frame, it plays once when the figure is mostly in view, and not at
// all with reduced motion or without an observer.

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let seen: ((entries: { target: Element; isIntersecting: boolean }[]) => void) | null = null;
let frames: ((t: number) => void)[] = [];

beforeEach(() => {
  frames = [];
  class FakeObserver {
    constructor(cb: (entries: { target: Element; isIntersecting: boolean }[]) => void) {
      // one observer serves the module for good: its callback is kept for the next test
      seen = cb;
    }
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal("IntersectionObserver", FakeObserver);
  vi.stubGlobal("requestAnimationFrame", (cb: (t: number) => void) => frames.push(cb));
  vi.stubGlobal("cancelAnimationFrame", () => {});
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  vi.unstubAllGlobals();
});

async function mount(text = "1 234 ГБ") {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<Scramble text={text} />));
}
/** The browser calls the frames, 16 ms apart. */
async function play(n: number) {
  let t = performance.now();
  for (let i = 0; i < n; i++) {
    const run = frames.splice(0);
    t += 16;
    await act(async () => run.forEach((f) => f(t)));
  }
}

describe("Scramble", () => {
  it("shows its text at once, shuffles a copy over it when first seen, and is still again afterwards", async () => {
    await mount("128 GB");
    expect(host!.textContent).toBe("128 GB");
    expect(host!.querySelector("[aria-hidden]")).toBeNull();

    // seen: the copy appears (the real text stays for readers, drawn transparent)
    const span = host!.firstElementChild as HTMLElement;
    await act(async () => seen!([{ target: span, isIntersecting: true }]));
    expect(host!.querySelectorAll("[aria-hidden] .fz-scr-c")).toHaveLength(6);
    expect(host!.querySelector(".text-transparent")?.textContent).toBe("128 GB");

    // every digit and letter lands in its place, then the copy is gone
    await play(200);
    expect(host!.querySelector("[aria-hidden]")).toBeNull();
    expect(host!.querySelector(".text-transparent")).toBeNull();
    expect(host!.textContent).toBe("128 GB");
  });

  it("plays once: a second sighting does nothing", async () => {
    await mount("42");
    const span = host!.firstElementChild as HTMLElement;
    await act(async () => seen!([{ target: span, isIntersecting: true }]));
    await play(200);
    await act(async () => seen!([{ target: span, isIntersecting: true }]));
    expect(host!.querySelector("[aria-hidden]")).toBeNull();
  });

  it("does not play with reduced motion", async () => {
    vi.stubGlobal("matchMedia", () => ({ matches: true, addEventListener: () => {}, removeEventListener: () => {} }));
    await mount("42");
    await act(async () => seen?.([{ target: host!.firstElementChild!, isIntersecting: true }]));
    expect(host!.querySelector(".fz-scr-c")).toBeNull(); // not even observed
    expect(host!.textContent).toBe("42");
  });
});
