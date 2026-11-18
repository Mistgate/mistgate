import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setEffectsEnabled } from "@/lib/effects";
import { Fresh } from "./fresh";
import { markFresh, resetFresh } from "./registry";

let boxed = true;
const realRect = HTMLElement.prototype.getBoundingClientRect;

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  HTMLElement.prototype.getBoundingClientRect = function () {
    const size = boxed ? { width: 200, height: 40 } : { width: 0, height: 0 };
    return { x: 0, y: 0, left: 0, top: 0, right: size.width, bottom: size.height, ...size, toJSON: () => ({}) } as DOMRect;
  };
});

afterAll(() => {
  HTMLElement.prototype.getBoundingClientRect = realRect;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  boxed = true;
  resetFresh();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  document.querySelectorAll("[role=dialog]").forEach((d) => d.remove());
  vi.useRealTimers();
  resetFresh();
  localStorage.clear();
});

const List = ({ ids }: { ids: string[] }) => (
  <ul>
    {ids.map((id) => (
      <Fresh key={id} as="li" kind="user" id={id} data-id={id}>
        {id}
      </Fresh>
    ))}
  </ul>
);
const render = (ids: string[]) => act(async () => root!.render(<List ids={ids} />));
const washed = () => [...document.querySelectorAll<HTMLElement>("[data-mg-fresh]")].map((e) => e.dataset.id);

describe("<Fresh>", () => {
  it("washes a created item once, the first time it shows up, and not on a refetch", async () => {
    await render(["u1", "u2"]);
    expect(washed()).toEqual([]); // nothing is marked: page load, plain list

    markFresh("user", "u3"); // the create mutation's success
    await render(["u1", "u2", "u3"]); // the refetch brings it
    expect(washed()).toEqual(["u3"]);

    // the wash is an attribute of its own: a re-render of the row does not take it away early
    await render(["u3", "u1", "u2"]); // reordered
    expect(washed()).toEqual(["u3"]);
  });

  it("takes the wash off by itself when it is over, and a mark is taken once", async () => {
    vi.useFakeTimers();
    markFresh("user", "u5");
    await render(["u5"]);
    expect(washed()).toEqual(["u5"]);
    await act(async () => void vi.advanceTimersByTime(2600));
    expect(washed()).toEqual([]);
    await render([]); // gone and back (navigating away and returning): the mark was taken
    await render(["u5"]);
    expect(washed()).toEqual([]);
  });

  it("washes an item that is already on screen the moment it is marked", async () => {
    await render(["n1"]);
    expect(washed()).toEqual([]);
    act(() => markFresh("user", "n1"));
    expect(washed()).toEqual(["n1"]);
  });

  it("leaves the mark to the twin that is visible (a table row and a card, one hidden)", async () => {
    markFresh("user", "u9");
    boxed = false;
    await render(["u9"]);
    expect(washed()).toEqual([]);
    boxed = true;
    await render(["u9", "u10"]);
    act(() => markFresh("user", "u9"));
    expect(washed()).toEqual(["u9"]);
  });

  it("waits for an open dialog that does not hold the item", async () => {
    vi.useFakeTimers();
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    document.body.append(dialog);
    markFresh("user", "w1");
    await render(["w1"]);
    expect(washed()).toEqual([]);
    dialog.remove();
    await act(async () => void vi.advanceTimersByTime(300));
    expect(washed()).toEqual(["w1"]);
  });

  it("does nothing when the effects are off", async () => {
    setEffectsEnabled(false);
    markFresh("user", "off1");
    await render(["off1"]);
    expect(washed()).toEqual([]);
  });
});
