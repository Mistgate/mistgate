import { act, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setEffectKind, setEffectsEnabled } from "@/lib/effects";
import { resetPageFx, useListEntrance, usePageTransition } from "./page";
import { isFresh, markFresh, resetFresh } from "./registry";

const sweep = vi.fn((_el: Element, _o?: unknown) => Promise.resolve());
const cancelSweep = vi.fn();
vi.mock("./sweep", () => ({ sweep: (el: Element, o?: unknown) => sweep(el, o), cancelSweep: (el: Element) => cancelSweep(el) }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  sweep.mockClear();
  cancelSweep.mockClear();
  resetPageFx();
  resetFresh();
  setEffectsEnabled(true);
  setEffectKind("create", true);
  setEffectKind("pages", true);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  vi.unstubAllGlobals();
  vi.useRealTimers();
  localStorage.clear();
});

function Page({ pathname, search = "", enabled = true, loading = false }: { pathname: string; search?: string; enabled?: boolean; loading?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  usePageTransition(ref, pathname, enabled); // the shell passes the router's pathname: a search-only change keeps it
  return <div ref={ref} data-search={search}>{loading ? <p data-fx-pending="" /> : null}</div>;
}
const render = (ui: React.ReactNode) => act(async () => root!.render(ui));

// a fresh shell: the band plays when the shell first appears, so each case mounts one anew
async function remount(ui: React.ReactNode) {
  act(() => root!.unmount());
  root = createRoot(host!);
  await render(ui);
}

describe("the page sweep", () => {
  it("plays once when the shell first appears, softer than a page, as light over the content (no mask, nothing hidden)", async () => {
    await render(<Page pathname="/overview" />);
    expect(sweep).toHaveBeenCalledTimes(1);
    const first = sweep.mock.calls[0]![1] as { duration: number; reveal: boolean; hideWhileHeld: boolean };
    expect(first.duration).toBeGreaterThan(720);
    expect(first).toMatchObject({ reveal: false, hideWhileHeld: false });
    await render(<Page pathname="/overview" />);
    expect(sweep).toHaveBeenCalledTimes(1);
  });

  it("does not sweep when moving between pages or tabs: the screen settles in by itself", async () => {
    await render(<Page pathname="/nodes" />);
    sweep.mockClear();
    await render(<Page pathname="/nodes" search="?tab=ways" />);
    await render(<Page pathname="/nodes/n1" />);
    await render(<Page pathname="/users" />);
    expect(sweep).not.toHaveBeenCalled();
  });

  it("waits for the page's loading marker to go, and gives up at the cap instead of sweeping over a page still loading", async () => {
    vi.useFakeTimers({ toFake: ["requestAnimationFrame", "cancelAnimationFrame", "performance"] });
    await remount(<Page pathname="/b" loading />);
    const el = host!.firstElementChild!;
    const hold = sweep.mock.calls[0]![1] as { hold: Promise<void> };
    let ready = false;
    void hold.hold.then(() => (ready = true));
    await vi.advanceTimersByTimeAsync(200);
    expect(ready).toBe(false); // still loading
    el.innerHTML = ""; // the data arrived
    await vi.advanceTimersByTimeAsync(50);
    expect(ready).toBe(true);
    expect(cancelSweep).not.toHaveBeenCalled();

    sweep.mockClear(); // a page that never finishes: no band over its "Loading…"
    await remount(<Page pathname="/c" loading />);
    await vi.advanceTimersByTimeAsync(1600);
    expect(cancelSweep).toHaveBeenCalledTimes(1);
  });

  it("stays out when a dialog is open", async () => {
    vi.useFakeTimers({ toFake: ["requestAnimationFrame", "cancelAnimationFrame", "performance"] });
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    document.body.append(dialog);
    await remount(<Page pathname="/profiles" />);
    await vi.advanceTimersByTimeAsync(50);
    expect(cancelSweep).toHaveBeenCalledTimes(1);
    dialog.remove();
  });

  it("does nothing when pages are off, when the effects are off, and under reduced motion", async () => {
    setEffectKind("pages", false);
    await remount(<Page pathname="/b" />);
    expect(sweep).not.toHaveBeenCalled();
    setEffectKind("pages", true);
    setEffectsEnabled(false);
    await remount(<Page pathname="/c" />);
    expect(sweep).not.toHaveBeenCalled();
    setEffectsEnabled(true);
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("reduce"), media: q }));
    await remount(<Page pathname="/d" />);
    expect(sweep).not.toHaveBeenCalled();
    // "create" is its own switch: the first band does not depend on it
    vi.unstubAllGlobals();
    setEffectKind("create", false);
    await remount(<Page pathname="/e" />);
    expect(sweep).toHaveBeenCalledTimes(1);
  });
});

describe("the list entrance", () => {
  const Rows = ({ ready, n = 20 }: { ready: boolean; n?: number }) => {
    const entrance = useListEntrance(ready);
    return (
      <ul>
        {Array.from({ length: n }, (_, i) => (
          <li key={i} style={entrance(i)} />
        ))}
      </ul>
    );
  };
  const animated = () => [...host!.querySelectorAll("li")].filter((l) => l.style.animation !== "").length;

  it("a list that was waited for appears as one (every row together, no stagger), and never again for refetches or filters", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    await render(<Rows ready={false} />);
    expect(animated()).toBe(0);
    await render(<Rows ready />);
    expect(animated()).toBe(20);
    const rows = [...host!.querySelectorAll("li")];
    expect(rows[0]!.style.animation).toBe(rows[19]!.style.animation);
    await act(async () => void vi.advanceTimersByTime(1100));
    expect(animated()).toBe(0);
    await render(<Rows ready={false} />); // polling / a filter: loading again, then data
    await render(<Rows ready n={5} />);
    expect(animated()).toBe(0);
  });

  it("a list ready on its first render (from the cache) is simply there", async () => {
    await render(<Rows ready />);
    expect(animated()).toBe(0);
  });

  it("is quiet when pages are off or with reduced motion", async () => {
    setEffectKind("pages", false);
    await render(<Rows ready={false} />);
    await render(<Rows ready />);
    expect(animated()).toBe(0);
    act(() => root!.unmount());
    root = createRoot(host!);
    setEffectKind("pages", true);
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("reduce"), media: q }));
    await render(<Rows ready={false} />);
    await render(<Rows ready />);
    expect(animated()).toBe(0);
  });
});

describe("the create switch", () => {
  it("makes markFresh a no-op when 'something is created' is off, and leaves pages alone", () => {
    setEffectKind("create", false);
    markFresh("user", "u1");
    expect(isFresh("user", "u1")).toBe(false);
    setEffectKind("create", true);
    markFresh("user", "u1");
    expect(isFresh("user", "u1")).toBe(true);
  });

  it("keeps each choice per device, both on by default", async () => {
    setEffectKind("pages", false);
    expect(localStorage.getItem("fx-pages")).toBe("off");
    expect(localStorage.getItem("fx-create")).toBeNull();
    vi.resetModules();
    const fresh = await import("@/lib/effects");
    expect(fresh.effectsFor("pages")).toBe(false);
    expect(fresh.effectsFor("create")).toBe(true);
    fresh.setEffectKind("pages", true);
    expect(localStorage.getItem("fx-pages")).toBeNull();
  });
});
