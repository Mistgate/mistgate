import { describe, expect, it } from "vitest";
import { routeWire } from "./geometry";
import { decideDrop, grabIdle, grabStep, grabTargets, handleAt, handlePoint, type GrabState } from "./reconnect";
import type { GraphEdge, GraphNode } from "./types";

const nodes: GraphNode[] = [
  { id: "be", title: "be", ports: [{ id: "o1", side: "out", type: "egress" }, { id: "o2", side: "out", type: "egress" }] },
  { id: "net", title: "net", ports: [{ id: "in", side: "in", type: "egress", multi: true }] },
  { id: "warp", title: "warp", ports: [{ id: "in", side: "in", type: "egress", multi: true }, { id: "off", side: "in", type: "egress", disabled: true }] },
];
const edge: GraphEdge = { id: "e1", from: { node: "be", port: "o1" }, to: { node: "net", port: "in" } };

describe("handles", () => {
  const route = routeWire({ x: 0, y: 0 }, { x: 300, y: 100 });
  it("sits on the wire, 18px from the end it belongs to", () => {
    const to = handlePoint(route, "to");
    const from = handlePoint(route, "from");
    expect(Math.hypot(to.x - 300, to.y - 100)).toBeCloseTo(18, 0);
    expect(Math.hypot(from.x, from.y)).toBeCloseTo(18, 0);
    expect(to.x).toBeLessThan(300);
    expect(from.x).toBeGreaterThan(0);
  });
  it("is found within 12px, the nearest first, and not further away", () => {
    const hs = [
      { ...handlePoint(route, "from"), edge: "a", end: "from" as const },
      { ...handlePoint(route, "to"), edge: "a", end: "to" as const },
    ];
    expect(handleAt({ x: hs[1]!.x + 8, y: hs[1]!.y }, hs)).toMatchObject({ end: "to" });
    expect(handleAt({ x: hs[0]!.x - 5, y: hs[0]!.y + 5 }, hs)).toMatchObject({ end: "from" });
    expect(handleAt({ x: 150, y: 50 }, hs)).toBeNull();
    expect(handleAt({ x: hs[1]!.x + 13, y: hs[1]!.y }, hs)).toBeNull();
  });
});

describe("what a drop means", () => {
  const be = { node: "net", port: "in" };
  it("is nothing on its own port, the move on a port that is accepted, an unhook on empty canvas", () => {
    expect(decideDrop(be, be, true)).toEqual({ kind: "noop" });
    expect(decideDrop(be, { node: "warp", port: "in" }, true)).toEqual({ kind: "apply", target: { node: "warp", port: "in" } });
    expect(decideDrop(be, null, true)).toEqual({ kind: "apply", target: null });
  });
  it("is a refusal with the reason when the mapping says no, on a port or on empty canvas", () => {
    expect(decideDrop(be, null, "A server always leaves somewhere")).toEqual({ kind: "refuse", reason: "A server always leaves somewhere" });
    expect(decideDrop(be, { node: "warp", port: "in" }, "no")).toMatchObject({ kind: "refuse" });
  });
});

describe("the ports the moved end may land on", () => {
  it("lists the in ports for a `to` end, skipping disabled ones, and keeps the own port", () => {
    const t = grabTargets(nodes, [edge], edge, "to");
    expect(t.map((r) => `${r.node}.${r.port}`)).toEqual(["net.in", "warp.in"]);
  });
  it("lists the out ports for a `from` end", () => {
    expect(grabTargets(nodes, [edge], edge, "from").map((r) => r.port)).toEqual(["o1", "o2"]);
  });
  it("asks the mapping when it has an opinion", () => {
    const only = (_e: GraphEdge, _end: "from" | "to", t: { node: string } | null) => (t?.node === "warp" ? true : "no");
    expect(grabTargets(nodes, [edge], edge, "to", only as never).map((r) => r.node)).toEqual(["warp"]);
  });
  it("without an opinion ignores the wire's own duplicate and respects the types", () => {
    const typed: GraphNode[] = [...nodes, { id: "tcp", title: "t", ports: [{ id: "in", side: "in", type: "tcp" }] }];
    expect(grabTargets(typed, [edge], edge, "to").some((r) => r.node === "tcp")).toBe(false);
  });
});

describe("the grab state machine", () => {
  const handle = { x: 1, y: 2, edge: "e1", end: "to" as const };
  const snap = { node: "warp", port: "in" };
  it("hover shows a handle, leaving clears it, pressing without one does nothing", () => {
    expect(grabStep(grabIdle, { type: "hover", handle }).state).toEqual({ phase: "hover", handle });
    expect(grabStep({ phase: "hover", handle }, { type: "hover", handle: null }).state).toEqual(grabIdle);
    expect(grabStep(grabIdle, { type: "press" }).state).toEqual(grabIdle);
  });
  it("press on a handle starts the drag, moves carry the snap and the refusal, release drops it", () => {
    let st: GrabState = grabStep({ phase: "hover", handle }, { type: "press" }).state;
    expect(st).toMatchObject({ phase: "drag", edge: "e1", end: "to" });
    st = grabStep(st, { type: "move", snap, refused: null }).state;
    const r = grabStep(st, { type: "release" });
    expect(r.state).toEqual(grabIdle);
    expect(r.effect).toEqual({ type: "drop", edge: "e1", end: "to", target: snap, refused: null });
  });
  it("released on empty canvas drops with a null target", () => {
    const st = grabStep({ phase: "hover", handle }, { type: "press" }).state;
    expect(grabStep(st, { type: "release" }).effect).toMatchObject({ type: "drop", target: null });
  });
  it("hovering is ignored while dragging, and cancelling springs back", () => {
    const st = grabStep({ phase: "hover", handle }, { type: "press" }).state;
    expect(grabStep(st, { type: "hover", handle: null }).state).toBe(st);
    expect(grabStep(st, { type: "cancel" })).toEqual({ state: grabIdle, effect: { type: "springBack" } });
  });
  it("a refusal on the way is carried to the release", () => {
    let st: GrabState = grabStep({ phase: "hover", handle }, { type: "press" }).state;
    st = grabStep(st, { type: "move", snap: null, refused: "udp only" }).state;
    expect(grabStep(st, { type: "release" }).effect).toMatchObject({ refused: "udp only" });
  });
});
