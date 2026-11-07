import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setEffectsEnabled } from "@/lib/effects";
import { Fresh } from "./fresh";
import { markFresh, resetFresh } from "./registry";
import { Sweep } from "./sweep-react";

const sweep = vi.fn((_el: Element, _o?: unknown) => Promise.resolve());
vi.mock("./sweep", () => ({ sweep: (el: Element, o?: unknown) => sweep(el, o), cancelSweep: vi.fn() }));

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
  sweep.mockClear();
  resetFresh();
  setEffectsEnabled(true);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  document.querySelectorAll("[role=dialog]").forEach((d) => d.remove());
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

describe("<Fresh>", () => {
  it("sweeps a created item in once, the first time it shows up, and not on a refetch", async () => {
    await render(["u1", "u2"]);
    expect(sweep).not.toHaveBeenCalled(); // nothing is marked: page load, plain list

    markFresh("user", "u3"); // the create mutation's success
    await render(["u1", "u2", "u3"]); // the refetch brings it
    expect(sweep).toHaveBeenCalledTimes(1);
    expect((sweep.mock.calls[0]![0] as HTMLElement).dataset.id).toBe("u3");

    await render(["u1", "u2", "u3"]); // another refetch, same rows
    await render(["u3", "u1", "u2"]); // reordered
    expect(sweep).toHaveBeenCalledTimes(1);

    await render(["u1", "u2"]); // gone and back (navigating away and returning): the mark was taken
    await render(["u1", "u2", "u3"]);
    expect(sweep).toHaveBeenCalledTimes(1);
  });

  it("sweeps an item that is already on screen the moment it is marked", async () => {
    await render(["n1"]);
    markFresh("user", "n1");
    expect(sweep).toHaveBeenCalledTimes(1);
    expect(sweep.mock.calls[0]![1]).toMatchObject({ delay: 0 });
    markFresh("user", "n1");
    expect(sweep).toHaveBeenCalledTimes(2); // a second creation event is a second sweep, a refetch is not an event
  });

  it("staggers several items that appear together", async () => {
    await render([]);
    for (const id of ["a", "b", "c"]) markFresh("user", id);
    await render(["a", "b", "c"]);
    expect(sweep.mock.calls.map((c) => (c[1] as { delay: number }).delay)).toEqual([0, 90, 180]);
  });

  it("leaves the mark to the twin that is visible (a table row and a card, one hidden)", async () => {
    markFresh("user", "u9");
    boxed = false;
    await render(["u9"]);
    expect(sweep).not.toHaveBeenCalled();
    boxed = true;
    await render(["u9", "u10"]);
    markFresh("user", "u9");
    expect(sweep).toHaveBeenCalledTimes(1);
  });

  it("waits for an open dialog that does not hold the item", async () => {
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    document.body.append(dialog);
    markFresh("user", "w1");
    await render(["w1"]);
    expect(sweep).toHaveBeenCalledTimes(1);
    expect(sweep.mock.calls[0]![1]).toMatchObject({ hold: expect.any(Promise), hideWhileHeld: true });
  });

  it("does nothing when the effects are off", async () => {
    setEffectsEnabled(false);
    markFresh("user", "off1");
    await render(["off1"]);
    expect(sweep).not.toHaveBeenCalled();
  });
});

describe("<Sweep>", () => {
  const Demo = ({ k }: { k: unknown }) => <Sweep play={k}>content</Sweep>;
  it("sweeps when play changes to something truthy, not on the first render", async () => {
    await act(async () => root!.render(<Demo k={0} />));
    await act(async () => root!.render(<Demo k={0} />));
    expect(sweep).not.toHaveBeenCalled();
    await act(async () => root!.render(<Demo k={1} />));
    expect(sweep).toHaveBeenCalledTimes(1);
    await act(async () => root!.render(<Demo k={1} />));
    expect(sweep).toHaveBeenCalledTimes(1);
    await act(async () => root!.render(<Demo k={2} />));
    expect(sweep).toHaveBeenCalledTimes(2);
    await act(async () => root!.render(<Demo k={false} />));
    expect(sweep).toHaveBeenCalledTimes(2);
  });

  it("passes its options through to the sweep", async () => {
    await act(async () =>
      root!.render(
        <>
          <Sweep play={false} palette="lagoon" duration={500} direction="ttb">
            x
          </Sweep>
        </>,
      ),
    );
    await act(async () =>
      root!.render(
        <Sweep play palette="lagoon" duration={500} direction="ttb">
          x
        </Sweep>,
      ),
    );
    expect(sweep).toHaveBeenCalledTimes(1);
    expect(sweep.mock.calls[0]![1]).toEqual({ palette: "lagoon", duration: 500, direction: "ttb" });
  });
});
