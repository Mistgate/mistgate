import { act, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setEffectKind, setEffectsEnabled } from "@/lib/effects";
import { noteNavigation, resetPageFx, useListEntrance, usePageTransition } from "./page";
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

describe("the page sweep", () => {
  it("plays once when the shell first appears, softer than a page", async () => {
    await render(<Page pathname="/overview" />);
    expect(sweep).toHaveBeenCalledTimes(1);
    const first = sweep.mock.calls[0]![1] as { duration: number; peakAlpha: number };
    expect(first.duration).toBeGreaterThan(720);
    await render(<Page pathname="/overview" />);
    expect(sweep).toHaveBeenCalledTimes(1);
  });

  it("plays once per route change, and not when only the search changes (a tab switch)", async () => {
    await render(<Page pathname="/nodes" />);
    sweep.mockClear();
    await render(<Page pathname="/nodes" search="?tab=ways" />);
    await render(<Page pathname="/nodes" search="?tab=profiles" />);
    expect(sweep).not.toHaveBeenCalled();
    await render(<Page pathname="/nodes/n1" />);
    expect(sweep).toHaveBeenCalledTimes(1);
    expect(sweep.mock.calls[0]![1]).toMatchObject({ duration: 720, hideWhileHeld: true, hold: expect.any(Promise) });
    await render(<Page pathname="/nodes/n1" search="?tab=ways" />);
    expect(sweep).toHaveBeenCalledTimes(1);
    await render(<Page pathname="/users" />);
    expect(sweep).toHaveBeenCalledTimes(2);
  });

  it("waits for the page's loading marker to go, and does not wait past the cap", async () => {
    vi.useFakeTimers({ toFake: ["requestAnimationFrame", "cancelAnimationFrame", "performance"] });
    await render(<Page pathname="/a" />);
    sweep.mockClear();
    await render(<Page pathname="/b" loading />);
    const el = host!.firstElementChild!;
    const hold = sweep.mock.calls[0]![1] as { hold: Promise<void> };
    let ready = false;
    void hold.hold.then(() => (ready = true));
    await vi.advanceTimersByTimeAsync(200);
    expect(ready).toBe(false); // still loading
    el.innerHTML = ""; // the data arrived
    await vi.advanceTimersByTimeAsync(50);
    expect(ready).toBe(true);

    sweep.mockClear(); // a page that never finishes: the cap lets it go
    await render(<Page pathname="/c" loading />);
    const hold2 = sweep.mock.calls[0]![1] as { hold: Promise<void> };
    let late = false;
    void hold2.hold.then(() => (late = true));
    await vi.advanceTimersByTimeAsync(450);
    expect(late).toBe(true);
  });

  it("stays out when a list takes the page's moment, and when a dialog is open", async () => {
    vi.useFakeTimers({ toFake: ["requestAnimationFrame", "cancelAnimationFrame", "performance"] });
    function List() {
      const entrance = useListEntrance(true);
      return <i data-s={entrance(0) ? "1" : "0"} />;
    }
    const Both = ({ path, list }: { path: string; list?: boolean }) => (
      <>
        {list ? <List /> : null}
        <Page pathname={path} />
      </>
    );
    await render(<Both path="/a" />);
    resetPageFx();
    noteNavigation();
    await render(<Both path="/users" list />);
    await vi.advanceTimersByTimeAsync(50);
    expect(cancelSweep).toHaveBeenCalledTimes(1); // the list (a child of the shell, so its effect runs first) was ready first

    cancelSweep.mockClear();
    resetPageFx();
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    document.body.append(dialog);
    await render(<Both path="/profiles" />);
    await vi.advanceTimersByTimeAsync(50);
    expect(cancelSweep).toHaveBeenCalledTimes(1);
    dialog.remove();
  });

  it("does nothing when pages are off, when the effects are off, and under reduced motion", async () => {
    await render(<Page pathname="/a" />);
    sweep.mockClear();
    setEffectKind("pages", false);
    await render(<Page pathname="/b" />);
    expect(sweep).not.toHaveBeenCalled();
    setEffectKind("pages", true);
    setEffectsEnabled(false);
    await render(<Page pathname="/c" />);
    expect(sweep).not.toHaveBeenCalled();
    setEffectsEnabled(true);
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("reduce"), media: q }));
    await render(<Page pathname="/d" />);
    expect(sweep).not.toHaveBeenCalled();
    // "create" is its own switch: the page sweep does not depend on it
    vi.unstubAllGlobals();
    setEffectKind("create", false);
    await render(<Page pathname="/e" />);
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

  it("staggers the first 12 rows by 18 ms on the first data, and never again for refetches or filters", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    await render(<Rows ready={false} />);
    expect(animated()).toBe(0);
    await render(<Rows ready />);
    expect(animated()).toBe(12);
    const rows = [...host!.querySelectorAll("li")];
    expect(rows[0]!.style.animation).toContain("0ms");
    expect(rows[11]!.style.animation).toContain("198ms");
    expect(rows[12]!.style.animation).toBe("");
    await act(async () => void vi.advanceTimersByTime(1100));
    expect(animated()).toBe(0);
    await render(<Rows ready={false} />); // polling / a filter: loading again, then data
    await render(<Rows ready n={5} />);
    expect(animated()).toBe(0);
  });

  it("is quiet when pages are off, with reduced motion, or right after a page sweep", async () => {
    setEffectKind("pages", false);
    await render(<Rows ready />);
    expect(animated()).toBe(0);
    act(() => root!.unmount());
    root = createRoot(host!);
    setEffectKind("pages", true);
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("reduce"), media: q }));
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
