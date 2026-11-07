import { act } from "react";
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

function mount(ui: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(ui));
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const port = (node: string, id: string) => host!.querySelector<HTMLElement>(`[data-gnode="${node}"] [data-port-id="${id}"]`)!;
const key = (el: Element, k: string, init: KeyboardEventInit = {}) => act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true, ...init })));
const click = (el: Element) => act(async () => void el.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const status = () => host!.querySelector('[role="status"]')!.textContent;

describe("Graph", () => {
  it("renders an application region, node groups, ports and wires", () => {
    const edges: GraphEdge[] = [{ id: "e1", from: { node: "de1", port: "relay" }, to: { node: "nl1", port: "hy2" } }];
    mount(<Graph nodes={nodes} edges={edges} typeColors={colors} ariaLabel="Ways" />);
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

  it("places nodes without a saved position and keeps new positions under the layout key", () => {
    const onPositionsChange = vi.fn();
    mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" layoutKey="t" onPositionsChange={onPositionsChange} />);
    expect(onPositionsChange).toHaveBeenCalled();
    const saved = JSON.parse(localStorage.getItem("mg.graph.t")!);
    expect(Object.keys(saved).sort()).toEqual(["de1", "fi1", "nl1"]);
    expect(saved.de1.x).toBeLessThan(saved.nl1.x);
    expect(saved.nl1.x).toBeLessThan(saved.fi1.x);
  });

  it("connects from the keyboard: Enter on a port, Tab to a target, Enter", async () => {
    const onConnect = vi.fn(async () => {});
    mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" onConnect={onConnect} />);
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
    mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" onConnect={onConnect} />);
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
    mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" onConnect={onConnect} />);
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
    mount(<Graph nodes={nodes} edges={edges} typeColors={colors} ariaLabel="g" onEdgeDelete={onEdgeDelete} />);
    const wire = host!.querySelector("[data-gedge]")!;
    await click(wire);
    await key(wire, "Delete");
    expect(onEdgeDelete).toHaveBeenCalledWith(edges[0]);
    await settle();
    expect(status()).toContain("Removed");
  });

  it("does not delete wires without onEdgeDelete", async () => {
    const edges: GraphEdge[] = [{ id: "e1", from: { node: "de1", port: "relay" }, to: { node: "nl1", port: "hy2" } }];
    mount(<Graph nodes={nodes} edges={edges} typeColors={colors} ariaLabel="g" />);
    const wire = host!.querySelector("[data-gedge]")!;
    await click(wire);
    await key(wire, "Delete");
    expect(status()).toBe("");
  });

  it("opens the wire menu on click and closes it", async () => {
    const edges: GraphEdge[] = [{ id: "e1", from: { node: "de1", port: "relay" }, to: { node: "nl1", port: "hy2" } }];
    mount(
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
    mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" onConnect={onConnect} readOnly />);
    const src = port("de1", "relay");
    await key(src, "Enter");
    expect(host!.querySelector('[data-role="target"]')).toBeNull();
    expect(onConnect).not.toHaveBeenCalled();
  });

  it("refuses a pair canConnect rejects: it is not offered as a target", async () => {
    const onConnect = vi.fn();
    mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" onConnect={onConnect} canConnect={(_, to) => (to.node === "fi1" ? "nope" : true)} />);
    await key(port("de1", "relay"), "Enter");
    expect(host!.querySelectorAll('[data-role="target"], [data-role="current"]')).toHaveLength(1);
    expect(port("fi1", "in").getAttribute("data-role")).toBe("dim");
  });

  it("announces when no port can take a wire", async () => {
    const lone: GraphNode[] = [{ id: "be", title: "be", ports: [{ id: "o", side: "out", type: "udp" }] }];
    mount(<Graph nodes={lone} edges={[]} typeColors={colors} ariaLabel="g" onConnect={() => {}} />);
    await key(port("be", "o"), "Enter");
    expect(status()).toContain("No port");
  });

  it("selects a node on focus and reports it", async () => {
    const onSelectionChange = vi.fn();
    mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" onSelectionChange={onSelectionChange} />);
    await act(async () => host!.querySelector<HTMLElement>('[data-gnode="nl1"]')!.focus());
    expect(onSelectionChange).toHaveBeenCalledWith(["nl1"]);
    expect(host!.querySelector('[data-gnode="nl1"]')!.hasAttribute("data-selected")).toBe(true);
  });

  it("nudges the selected node with the arrow keys", async () => {
    const onPositionsChange = vi.fn();
    mount(<Graph nodes={nodes} edges={[]} typeColors={colors} ariaLabel="g" positions={{ de1: { x: 0, y: 0 }, nl1: { x: 300, y: 0 }, fi1: { x: 600, y: 0 } }} onPositionsChange={onPositionsChange} />);
    const card = host!.querySelector<HTMLElement>('[data-gnode="nl1"]')!;
    await act(async () => card.focus());
    await key(card, "ArrowRight");
    expect(onPositionsChange).toHaveBeenLastCalledWith(expect.objectContaining({ nl1: { x: 308, y: 0 } }));
    await key(card, "ArrowDown", { shiftKey: true });
    expect(onPositionsChange).toHaveBeenLastCalledWith(expect.objectContaining({ nl1: { x: 300, y: 1 } }));
  });
});
