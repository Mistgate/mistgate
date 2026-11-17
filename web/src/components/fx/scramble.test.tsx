import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { setEffectsEnabled } from "@/lib/effects";
import { play, playing, settle } from "./scramble";
import { Scramble } from "./scramble-react";

// The number effect of the Overview: digits shuffle among digits and land one by one; off with the switch.

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});
afterEach(() => {
  vi.useRealTimers();
  setEffectsEnabled(false);
});

const frames = (ms: number) => {
  for (let t = 0; t < ms; t += 16) vi.advanceTimersByTime(16);
};

describe("the scramble engine", () => {
  it("keeps the shape of the text while it plays (digits stay digits, the rest stays) and ends on the text", () => {
    vi.useFakeTimers();
    const el = document.createElement("span");
    document.body.append(el);
    play(el, "76,2 GB");
    expect(playing(el)).toBe(true);
    expect(el.textContent).toHaveLength(7);
    frames(80);
    const mid = el.textContent!;
    expect(mid).toHaveLength(7);
    expect(mid[2]).toBe(",");
    expect(mid[4]).toBe(" ");
    expect(/^\d\d,\d [A-Z]{2}$/.test(mid)).toBe(true);
    frames(2500);
    expect(el.textContent).toBe("76,2 GB");
    expect(playing(el)).toBe(false);
    el.remove();
  });

  it("settles at once on request", () => {
    vi.useFakeTimers();
    const el = document.createElement("span");
    document.body.append(el);
    play(el, "123");
    settle(el, "456");
    expect(el.textContent).toBe("456");
    expect(playing(el)).toBe(false);
    el.remove();
  });
});

describe("the Scramble element", () => {
  let root: Root | null = null;
  let host: HTMLDivElement | null = null;
  afterEach(() => {
    act(() => root?.unmount());
    host?.remove();
    root = host = null;
  });
  const mount = (value: string) => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    act(() => root!.render(<Scramble value={value} />));
  };

  it("is the plain text, labelled with the value, while the effects are off", () => {
    mount("42");
    const el = host!.firstElementChild!;
    expect(el.textContent).toBe("42");
    expect(el.getAttribute("aria-label")).toBe("42");
    act(() => root!.render(<Scramble value="43" />));
    expect(el.textContent).toBe("43");
  });

  it("plays with the effects on, and the text is what it says in the end", () => {
    vi.useFakeTimers();
    setEffectsEnabled(true);
    mount("42");
    const el = host!.firstElementChild as HTMLElement;
    expect(playing(el)).toBe(true); // no IntersectionObserver in the test: it plays at once
    frames(2500);
    expect(el.textContent).toBe("42");
    act(() => root!.render(<Scramble value="1,5" />));
    expect(playing(el)).toBe(true);
    frames(2500);
    expect(el.textContent).toBe("1,5");
  });
});
