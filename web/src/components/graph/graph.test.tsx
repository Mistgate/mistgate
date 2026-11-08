import { act } from "react";
import { createPortal } from "react-dom";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { Graph, GraphPortRow, type GraphEdge, type GraphNode } from ".";

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  localStorage.clear();
});

const colors = { udp: "var(--accent-sky)", tcp: "var(--accent-lavender)", any: "var(--muted)" };
const nodes: GraphNode[] = [
  {
    id: "de1",
    title: "de1",
    rank: 0,
    ports: [
      { id: "relay", side: "out", type: "udp", label: "Relay" },
      { id: "tcp", side: "out", type: "tcp", label: "TCP" },
    ],
  },
  {
    id: "nl1",
    title: "nl1",
    rank: 1,
    ports: [
      { id: "hy2", side: "in", type: "udp", label: "HY2" },
      { id: "vless", side: "in", type: "tcp", label: "VLESS" },
      { id: "out", side: "out", type: "udp", label: "Out" },
    ],
    body: (
      <GraphPortRow port="hy2">
        <span>HY2 row</span>
      </GraphPortRow>
    ),
  },
  { id: "fi1", title: "fi1", rank: 2, ports: [{ id: "in", side: "in", type: "any", label: "In" }] },
];

const settle = async () => {
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function mount(ui: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(ui));
  await settle(); // the cards are placed a tick after they are measured
}
const port = (node: string, id: string) => host!.querySelector<HTMLElement>(`[data-gnode="${node}"] [data-port-id="${id}"]`)!;
const key = (el: Element, k: string, init: KeyboardEventInit = {}) => act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true, ...init })));
const click = (el: Element) => act(async () => void el.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const status = () => host!.querySelector('[role="status"]')!.textContent;

describe("Graph", () => {
  it("renders an application region, node groups, ports and wires", async () => {
    const edges: GraphEdge[] = [{ id: "e1", from: { node: "de1", port: "relay" }, to: { node: "nl1", port: "hy2" } }];
    await mount(<Graph nodes={nodes} edges={edges} typeColors={colors} ariaLabel="Ways" />);
    const app = host!.querySelector('[role="application"]')!;
    expect(app.getAttribute("aria-label")).toBe("Ways");
    expect(app.getAttribute("aria-describedby")).toBeTruthy();
    expect(host!.querySelectorAll('[role="group"][data-gnode]')).toHaveLength(3);
    expect(host!.querySelector('[data-gnode="nl1"]')!.getAttribute("aria-label")).toBe("nl1");
    // the claimed port sits in its row, not in a second auto row
    expect(host!.querySelectorAll('[data-gnode="nl1"] [data-port-id="hy2"]')).toHaveLength(1);
    expect(host!.querySelectorAll("[data-gedge]")).toHaveLength(1);
    // hollow = free, filled = connected
    expect(port("de1", "relay").hasAttribute("data-connected")).toBe(true);
    expect(port("de1", "tcp").hasAttribute("data-connected")).toBe(false);
    expect(port("nl1", "hy2").getAttribute("aria-label")).toContain("input HY2");
    expect(host!.querySelector('[role="toolbar"]')).toBeTruthy();
  });

  it("places nodes without a saved position and keeps new positions under the layout key", async () => {
    const onPositionsChange = vi.fn();
    await mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" layoutKey="t" onPositionsChange={onPositionsChange} />);
    expect(onPositionsChange).toHaveBeenCalled();
    const saved = JSON.parse(localStorage.getItem("mg.graph.t")!);
    expect(Object.keys(saved).sort()).toEqual(["de1", "fi1", "nl1"]);
    expect(saved.de1.x).toBeLessThan(saved.nl1.x);
    expect(saved.nl1.x).toBeLessThan(saved.fi1.x);
  });

  it("connects from the keyboard: Enter on a port, Tab to a target, Enter", async () => {
    const onConnect = vi.fn(async () => {});
    await mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" onConnect={onConnect} />);
    const src = port("de1", "relay");
    src.focus();
    await key(src, "Enter");
    // valid udp targets: nl1.hy2 and fi1.in (any); the first is highlighted
    expect(host!.querySelectorAll('[data-role="target"], [data-role="current"]')).toHaveLength(2);
    expect(port("nl1", "vless").getAttribute("data-role")).toBe("dim");
    expect(document.activeElement).toBe(port("nl1", "hy2"));
    await key(document.activeElement!, "Tab");
    expect(document.activeElement).toBe(port("fi1", "in"));
    await key(document.activeElement!, "Enter");
    await settle();
    expect(onConnect).toHaveBeenCalledWith({ node: "de1", port: "relay" }, { node: "fi1", port: "in" });
    expect(status()).toContain("Connected");
    expect(host!.querySelector('[data-role="target"]')).toBeNull();
  });

  it("cancels keyboard wiring with Escape", async () => {
    const onConnect = vi.fn();
    await mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" onConnect={onConnect} />);
    const src = port("de1", "tcp");
    src.focus();
    await key(src, "Enter");
    expect(port("nl1", "vless").getAttribute("data-role")).toBe("current");
    await key(document.activeElement!, "Escape");
    expect(port("nl1", "vless").hasAttribute("data-role")).toBe(false);
    expect(onConnect).not.toHaveBeenCalled();
  });

  it("shows a refused connect's error and drops the pending wire", async () => {
    const onConnect = vi.fn(async () => {
      throw new Error("node is busy");
    });
    await mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" onConnect={onConnect} />);
    const src = port("de1", "relay");
    await key(src, "Enter");
    await key(document.activeElement!, "Enter");
    await settle();
    expect(status()).toContain("node is busy");
    expect(host!.querySelector(".mg-glabel[data-error]")?.textContent).toBe("node is busy");
  });

  it("removes a selected wire with Delete when onEdgeDelete exists", async () => {
    const onEdgeDelete = vi.fn();
    const edges: GraphEdge[] = [{ id: "e1", from: { node: "de1", port: "relay" }, to: { node: "nl1", port: "hy2" } }];
    await mount(<Graph nodes={nodes} edges={edges} typeColors={colors} ariaLabel="g" onEdgeDelete={onEdgeDelete} />);
    const wire = host!.querySelector("[data-gedge]")!;
    await click(wire);
    await key(wire, "Delete");
    expect(onEdgeDelete).toHaveBeenCalledWith(edges[0]);
    await settle();
    expect(status()).toContain("Removed");
  });

  it("does not delete wires without onEdgeDelete", async () => {
    const edges: GraphEdge[] = [{ id: "e1", from: { node: "de1", port: "relay" }, to: { node: "nl1", port: "hy2" } }];
    await mount(<Graph nodes={nodes} edges={edges} typeColors={colors} ariaLabel="g" />);
    const wire = host!.querySelector("[data-gedge]")!;
    await click(wire);
    await key(wire, "Delete");
    expect(status()).toBe("");
  });

  it("opens the wire menu on click and closes it", async () => {
    const edges: GraphEdge[] = [{ id: "e1", from: { node: "de1", port: "relay" }, to: { node: "nl1", port: "hy2" } }];
    await mount(
      <Graph
        nodes={nodes}
        edges={edges}
        typeColors={colors}
        ariaLabel="g"
        renderEdgeMenu={(e, close) => (
          <button type="button" onClick={close}>
            Menu for {e.id}
          </button>
        )}
      />,
    );
    await click(host!.querySelector("[data-gedge]")!);
    expect(host!.querySelector(".mg-gmenu")?.textContent).toBe("Menu for e1");
    await click(host!.querySelector(".mg-gmenu button")!);
    expect(host!.querySelector(".mg-gmenu")).toBeNull();
  });

  it("blocks connecting in read-only mode", async () => {
    const onConnect = vi.fn();
    await mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" onConnect={onConnect} readOnly />);
    const src = port("de1", "relay");
    await key(src, "Enter");
    expect(host!.querySelector('[data-role="target"]')).toBeNull();
    expect(onConnect).not.toHaveBeenCalled();
  });

  it("refuses a pair canConnect rejects: it is not offered as a target", async () => {
    const onConnect = vi.fn();
    await mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" onConnect={onConnect} canConnect={(_, to) => (to.node === "fi1" ? "nope" : true)} />);
    await key(port("de1", "relay"), "Enter");
    expect(host!.querySelectorAll('[data-role="target"], [data-role="current"]')).toHaveLength(1);
    expect(port("fi1", "in").getAttribute("data-role")).toBe("dim");
  });

  it("announces when no port can take a wire", async () => {
    const lone: GraphNode[] = [{ id: "be", title: "be", ports: [{ id: "o", side: "out", type: "udp" }] }];
    await mount(<Graph nodes={lone} edges={[]} typeColors={colors} ariaLabel="g" onConnect={() => {}} />);
    await key(port("be", "o"), "Enter");
    expect(status()).toContain("No port");
  });

  it("selects a node on focus and reports it", async () => {
    const onSelectionChange = vi.fn();
    await mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" onSelectionChange={onSelectionChange} />);
    await act(async () => host!.querySelector<HTMLElement>('[data-gnode="nl1"]')!.focus());
    expect(onSelectionChange).toHaveBeenCalledWith(["nl1"]);
    expect(host!.querySelector('[data-gnode="nl1"]')!.hasAttribute("data-selected")).toBe(true);
  });

  it("nudges the selected node with the arrow keys", async () => {
    const onPositionsChange = vi.fn();
    await mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" positions={{ de1: { x: 0, y: 0 }, nl1: { x: 300, y: 0 }, fi1: { x: 600, y: 0 } }} onPositionsChange={onPositionsChange} />);
    const card = host!.querySelector<HTMLElement>('[data-gnode="nl1"]')!;
    await act(async () => card.focus());
    await key(card, "ArrowRight");
    expect(onPositionsChange).toHaveBeenLastCalledWith(expect.objectContaining({ nl1: { x: 308, y: 0 } }));
    await key(card, "ArrowDown", { shiftKey: true });
    expect(onPositionsChange).toHaveBeenLastCalledWith(expect.objectContaining({ nl1: { x: 300, y: 1 } }));
  });
});

describe("help and shortcuts", () => {
  it("? opens the shortcuts card with a key column, Esc closes it, and the choice is remembered", async () => {
    await mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" legend={<p>legend text</p>} />);
    const app = host!.querySelector<HTMLElement>('[role="application"]')!;
    app.focus();
    await key(app, "?");
    const card = host!.querySelector("#graph-help")!;
    expect(card.getAttribute("aria-label")).toBe("Shortcuts");
    expect(card.querySelectorAll("kbd").length).toBeGreaterThan(10);
    expect(card.textContent).toContain("Box select");
    expect(localStorage.getItem("mg.graph.help")).toBe("shortcuts");
    await key(app, "Escape");
    expect(host!.querySelector("#graph-help")).toBeNull();
    expect(localStorage.getItem("mg.graph.help")).toBeNull();
  });

  it("the Legend button shows what the screen gives it, and the other button swaps the card", async () => {
    await mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" legend={<p>legend text</p>} />);
    const btn = (label: string) => [...host!.querySelectorAll<HTMLElement>(".mg-glegend-btn")].find((b) => b.textContent === label)!;
    await click(btn("Legend"));
    expect(host!.querySelector("#graph-help")?.textContent).toBe("legend text");
    await click(btn("Shortcuts"));
    expect(host!.querySelector("#graph-help")?.getAttribute("aria-label")).toBe("Shortcuts");
  });

  it("Shift+F asks to expand (and F only fits); the expand button does the same", async () => {
    const onExpand = vi.fn();
    await mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" onExpand={onExpand} />);
    const app = host!.querySelector<HTMLElement>('[role="application"]')!;
    await key(app, "f");
    expect(onExpand).not.toHaveBeenCalled();
    await key(app, "F", { shiftKey: true });
    expect(onExpand).toHaveBeenCalledTimes(1);
    await click(host!.querySelector("[data-graph-expand]")!);
    expect(onExpand).toHaveBeenCalledTimes(2);
  });

  it("a plain wheel is left to the page; Ctrl + wheel is taken for zoom; full screen takes any wheel", async () => {
    await mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" />);
    const app = host!.querySelector<HTMLElement>('[role="application"]')!;
    const wheel = (init: WheelEventInit) => {
      const e = new WheelEvent("wheel", { bubbles: true, cancelable: true, ...init });
      app.dispatchEvent(e);
      return e.defaultPrevented;
    };
    expect(wheel({ deltaY: 100 })).toBe(false);
    expect(wheel({ deltaY: -100, ctrlKey: true })).toBe(true);
    expect(wheel({ deltaX: 40, deltaY: 2 })).toBe(true);
  });

  it("full screen: a plain wheel pans", async () => {
    await mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" wheelPans />);
    const app = host!.querySelector<HTMLElement>('[role="application"]')!;
    const e = new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaY: 80 });
    app.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(true);
  });
});

describe("grabbing a wire's end", () => {
  const edges: GraphEdge[] = [
    { id: "e1", from: { node: "de1", port: "relay" }, to: { node: "nl1", port: "hy2" } },
    { id: "e2", from: { node: "de1", port: "tcp" }, to: { node: "nl1", port: "vless" }, grabbable: false },
  ];
  // the cards sit apart on one line, so the wire from de1 to nl1 is straight and its `from` handle is 18px from de1's port
  const spread = { de1: { x: 0, y: 0 }, nl1: { x: 400, y: 0 }, fi1: { x: 800, y: 0 } };
  const move = (el: Element) =>
    act(async () => {
      const ev = new MouseEvent("pointermove", { bubbles: true, clientX: 18, clientY: 0 });
      Object.defineProperty(ev, "pointerType", { value: "mouse" });
      el.dispatchEvent(ev);
    });

  it("shows a handle on a wire's end near the pointer, with a grab cursor", async () => {
    await mount(<Graph nodes={nodes} edges={edges.slice(0, 1)} typeColors={colors} ariaLabel="g" positions={spread} onReconnect={() => {}} />);
    const app = host!.querySelector<HTMLElement>('[role="application"]')!;
    expect(host!.querySelector(".mg-ghandle")).toBeNull();
    await move(app);
    expect(host!.querySelector(".mg-ghandle")).not.toBeNull();
    expect(app.hasAttribute("data-handle")).toBe(true);
  });

  it("gives no handle to a wire that is a fact, to a read-only graph, or without onReconnect", async () => {
    await mount(<Graph nodes={nodes} edges={edges.slice(1)} typeColors={colors} ariaLabel="g" positions={spread} onReconnect={() => {}} />);
    await move(host!.querySelector('[role="application"]')!);
    expect(host!.querySelector(".mg-ghandle")).toBeNull();
    act(() => root!.unmount());
    host!.remove();
    await mount(<Graph nodes={nodes} edges={edges.slice(0, 1)} typeColors={colors} ariaLabel="g" positions={spread} onReconnect={() => {}} readOnly />);
    await move(host!.querySelector('[role="application"]')!);
    expect(host!.querySelector(".mg-ghandle")).toBeNull();
    act(() => root!.unmount());
    host!.remove();
    await mount(<Graph nodes={nodes} edges={edges.slice(0, 1)} typeColors={colors} ariaLabel="g" positions={spread} onEdgeDelete={() => {}} />);
    await move(host!.querySelector('[role="application"]')!);
    expect(host!.querySelector(".mg-ghandle")).toBeNull();
  });

  it("Delete on a selected wire drops its `to` end on empty canvas: onReconnect(edge, 'to', null)", async () => {
    const onReconnect = vi.fn();
    await mount(<Graph nodes={nodes} edges={edges.slice(0, 1)} typeColors={colors} ariaLabel="g" onReconnect={onReconnect} />);
    const wire = host!.querySelector("[data-gedge]")!;
    await click(wire);
    await key(wire, "Delete");
    expect(onReconnect).toHaveBeenCalledWith(edges[0], "to", null);
  });

  it("Delete is refused with the reason when canReconnect says so, and a wire that is a fact is left alone", async () => {
    const onReconnect = vi.fn();
    await mount(<Graph nodes={nodes} edges={edges.slice(0, 1)} typeColors={colors} ariaLabel="g" onReconnect={onReconnect} canReconnect={() => "A server always leaves somewhere"} />);
    let wire = host!.querySelector("[data-gedge]")!;
    await click(wire);
    await key(wire, "Delete");
    expect(onReconnect).not.toHaveBeenCalled();
    expect(status()).toBe("A server always leaves somewhere");
    act(() => root!.unmount());
    host!.remove();
    await mount(<Graph nodes={nodes} edges={edges.slice(1)} typeColors={colors} ariaLabel="g" onReconnect={onReconnect} />);
    wire = host!.querySelector("[data-gedge]")!;
    await click(wire);
    await key(wire, "Delete");
    expect(onReconnect).not.toHaveBeenCalled();
  });

  it("a rejected onReconnect is announced, not thrown", async () => {
    await mount(<Graph nodes={nodes} edges={edges.slice(0, 1)} typeColors={colors} ariaLabel="g" onReconnect={async () => Promise.reject(new Error("busy"))} />);
    const wire = host!.querySelector("[data-gedge]")!;
    await click(wire);
    await key(wire, "Delete");
    await settle();
    expect(status()).toBe("busy");
  });
});

describe("cards that move, and popovers that are not the canvas", () => {
  const pointer = (type: string, x: number, y: number) => {
    const ev = new MouseEvent(type, { bubbles: true, cancelable: true, clientX: x, clientY: y, button: 0 });
    Object.defineProperty(ev, "pointerId", { value: 1 });
    Object.defineProperty(ev, "pointerType", { value: "mouse" });
    return ev;
  };
  const stubs = () => {
    Element.prototype.setPointerCapture = () => {};
    Element.prototype.releasePointerCapture = () => {};
    Element.prototype.hasPointerCapture = () => false;
  };

  it("a card that cannot be selected still moves with the pointer, snapped, alone", async () => {
    stubs();
    const onPositionsChange = vi.fn();
    const fixed: GraphNode[] = nodes.map((n) => ({ ...n, selectable: false }));
    await mount(<Graph nodes={fixed} edges={[]} typeColors={colors} ariaLabel="g" positions={{ de1: { x: 0, y: 0 }, nl1: { x: 300, y: 0 }, fi1: { x: 600, y: 0 } }} onPositionsChange={onPositionsChange} />);
    const card = host!.querySelector<HTMLElement>('[data-gnode="nl1"] .mg-ghead')!;
    const app = host!.querySelector<HTMLElement>('[role="application"]')!;
    await act(async () => void card.dispatchEvent(pointer("pointerdown", 100, 100)));
    await act(async () => void app.dispatchEvent(pointer("pointermove", 140, 124)));
    await act(async () => void app.dispatchEvent(pointer("pointerup", 140, 124)));
    expect(onPositionsChange).toHaveBeenLastCalledWith({ de1: { x: 0, y: 0 }, nl1: { x: 344, y: 24 }, fi1: { x: 600, y: 0 } });
  });

  it("a pointerdown in a popover portaled out of a card does not start a pan (it would swallow the click)", async () => {
    stubs();
    const capture = vi.spyOn(Element.prototype, "setPointerCapture");
    const withPortal: GraphNode[] = [{ ...nodes[0]!, body: createPortal(<button type="button">in a popover</button>, document.body) }];
    await mount(<Graph nodes={withPortal} edges={[]} typeColors={colors} ariaLabel="g" />);
    const button = [...document.querySelectorAll("button")].find((b) => b.textContent === "in a popover")!;
    await act(async () => void button.dispatchEvent(pointer("pointerdown", 5, 5)));
    expect(capture).not.toHaveBeenCalled();
    expect(host!.querySelector('[role="application"]')!.getAttribute("data-mode")).toBeNull();
    capture.mockRestore();
  });
});

describe("Graph focusPaths", () => {
  const focusNodes: GraphNode[] = [...nodes, { id: "x", title: "x", rank: 1, ports: [{ id: "in", side: "in", type: "udp", label: "In" }] }];
  const focusEdges: GraphEdge[] = [
    { id: "e1", from: { node: "de1", port: "relay" }, to: { node: "nl1", port: "hy2" } },
    { id: "e2", from: { node: "nl1", port: "out" }, to: { node: "fi1", port: "in" } },
    { id: "e3", from: { node: "de1", port: "relay" }, to: { node: "x", port: "in" } },
  ];
  const dimmed = () => [...host!.querySelectorAll("[data-gnode][data-dim]")].map((e) => e.getAttribute("data-gnode")).sort();
  const dimWires = () => [...host!.querySelectorAll("g.mg-gedge[data-dim] [data-gedge]")].map((e) => e.getAttribute("data-gedge")).sort();

  it("a selected card keeps the paths through it and fades the rest, and says how to show everything", async () => {
    let selected = ["fi1"];
    const onSel = (ids: string[]) => (selected = ids);
    await mount(<Graph nodes={focusNodes} edges={focusEdges} typeColors={colors} ariaLabel="Map" focusPaths selected={selected} onSelectionChange={onSel} />);
    expect(dimmed()).toEqual(["x"]);
    expect(dimWires()).toEqual(["e3"]);
    const caption = host!.querySelector(".mg-gfocus")!;
    expect(caption.textContent).toContain("Paths through fi1");
    await click(caption.querySelector("button")!);
    expect(selected).toEqual([]);
  });

  it("without focusPaths, or with nothing selected, nothing fades", async () => {
    await mount(<Graph nodes={focusNodes} edges={focusEdges} typeColors={colors} ariaLabel="Map" selected={["fi1"]} />);
    expect(dimmed()).toEqual([]);
    expect(host!.querySelector(".mg-gfocus")).toBeNull();
    act(() => root?.unmount());
    host?.remove();
    await mount(<Graph nodes={focusNodes} edges={focusEdges} typeColors={colors} ariaLabel="Map" focusPaths selected={[]} />);
    expect(dimmed()).toEqual([]);
    expect(dimWires()).toEqual([]);
  });
});
