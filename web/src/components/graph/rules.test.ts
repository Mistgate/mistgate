import { describe, expect, it } from "vitest";
import { checkConnection, orient, typesCompatible, validTargets } from "./rules";
import { currentTarget, idle, wiringStep } from "./wiring";
import type { GraphNode } from "./types";

const nodes: GraphNode[] = [
  { id: "a", title: "a", ports: [{ id: "o-udp", side: "out", type: "udp" }, { id: "o-tcp", side: "out", type: "tcp" }, { id: "o-one", side: "out", type: "udp", multi: false }] },
  {
    id: "b",
    title: "b",
    ports: [
      { id: "i-udp", side: "in", type: "udp" },
      { id: "i-tcp", side: "in", type: "tcp" },
      { id: "i-any", side: "in", type: "any" },
      { id: "i-multi", side: "in", type: "udp", multi: true },
      { id: "i-off", side: "in", type: "udp", disabled: true },
      { id: "o-b", side: "out", type: "udp" },
    ],
  },
];
const A = (port: string) => ({ node: "a", port });
const B = (port: string) => ({ node: "b", port });

describe("typesCompatible", () => {
  it("accepts equal types and any", () => {
    expect(typesCompatible("udp", "udp")).toBe(true);
    expect(typesCompatible("udp", "tcp")).toBe(false);
    expect(typesCompatible("any", "tcp")).toBe(true);
    expect(typesCompatible("udp", "any")).toBe(true);
  });
});

describe("checkConnection", () => {
  it("connects matching types", () => {
    expect(checkConnection(nodes, [], A("o-udp"), B("i-udp"))).toEqual({ ok: true });
    expect(checkConnection(nodes, [], A("o-tcp"), B("i-any"))).toEqual({ ok: true });
  });
  it("refuses other types, self-loops, wrong sides, unknown and disabled ports", () => {
    expect(checkConnection(nodes, [], A("o-udp"), B("i-tcp"))).toMatchObject({ ok: false, code: "type" });
    expect(checkConnection(nodes, [], B("o-b"), B("i-udp"))).toMatchObject({ ok: false, code: "self" });
    expect(checkConnection(nodes, [], B("i-udp"), A("o-udp"))).toMatchObject({ ok: false, code: "side" });
    expect(checkConnection(nodes, [], A("nope"), B("i-udp"))).toMatchObject({ ok: false, code: "unknown" });
    expect(checkConnection(nodes, [], A("o-udp"), B("i-off"))).toMatchObject({ ok: false, code: "disabled" });
  });
  it("refuses duplicates and a taken single port, allows many into a multi input and out of an output", () => {
    const e = [{ from: A("o-udp"), to: B("i-udp") }];
    expect(checkConnection(nodes, e, A("o-udp"), B("i-udp"))).toMatchObject({ ok: false, code: "duplicate" });
    expect(checkConnection(nodes, e, A("o-udp"), B("i-any"))).toEqual({ ok: true });
    expect(checkConnection(nodes, [{ from: A("o-tcp"), to: B("i-any") }], A("o-udp"), B("i-any"))).toMatchObject({ ok: false, code: "occupied" });
    expect(checkConnection(nodes, [{ from: A("o-tcp"), to: B("i-multi") }], A("o-udp"), B("i-multi"))).toEqual({ ok: true });
    expect(checkConnection(nodes, [{ from: A("o-one"), to: B("i-any") }], A("o-one"), B("i-multi"))).toMatchObject({ ok: false, code: "occupied" });
  });
  it("asks the caller's canConnect last and passes its reason on", () => {
    const deny = () => "no udp on Mondays";
    expect(checkConnection(nodes, [], A("o-udp"), B("i-udp"), deny)).toEqual({ ok: false, code: "custom", message: "no udp on Mondays" });
    expect(checkConnection(nodes, [], A("o-udp"), B("i-tcp"), deny)).toMatchObject({ code: "type" });
    expect(checkConnection(nodes, [], A("o-udp"), B("i-udp"), () => true)).toEqual({ ok: true });
  });
});

describe("validTargets and orient", () => {
  it("lists the ports that can take a wire from either end", () => {
    expect(validTargets(nodes, [], A("o-tcp")).map((r) => r.port)).toEqual(["i-tcp", "i-any"]);
    expect(validTargets(nodes, [], B("i-tcp")).map((r) => `${r.node}.${r.port}`)).toEqual(["a.o-tcp"]);
    expect(validTargets(nodes, [], B("i-off"))).toEqual([]);
  });
  it("orients a pair as out then in", () => {
    expect(orient(nodes, B("i-udp"), A("o-udp"))).toEqual({ from: A("o-udp"), to: B("i-udp") });
    expect(orient(nodes, A("o-udp"), A("o-tcp"))).toBeNull();
  });
});

describe("keyboard wiring state machine", () => {
  const cands = [B("i-udp"), B("i-any"), B("i-multi")];
  it("starts picking at the first candidate, or says there is none", () => {
    const s = wiringStep(idle, { type: "start", source: A("o-udp"), candidates: cands });
    expect(s.state).toMatchObject({ phase: "picking", index: 0 });
    expect(wiringStep(idle, { type: "start", source: A("o-udp"), candidates: [] })).toEqual({ state: idle, effect: { type: "empty" } });
  });
  it("cycles both ways, wrapping", () => {
    let st = wiringStep(idle, { type: "start", source: A("o-udp"), candidates: cands }).state;
    st = wiringStep(st, { type: "next" }).state;
    expect(currentTarget(st)).toEqual(B("i-any"));
    st = wiringStep(wiringStep(st, { type: "next" }).state, { type: "next" }).state;
    expect(currentTarget(st)).toEqual(B("i-udp"));
    st = wiringStep(st, { type: "prev" }).state;
    expect(currentTarget(st)).toEqual(B("i-multi"));
  });
  it("confirms into a connect effect and returns to idle; cancel just returns", () => {
    let st = wiringStep(idle, { type: "start", source: A("o-udp"), candidates: cands }).state;
    st = wiringStep(st, { type: "next" }).state;
    const c = wiringStep(st, { type: "confirm" });
    expect(c.state).toEqual(idle);
    expect(c.effect).toEqual({ type: "connect", source: A("o-udp"), target: B("i-any") });
    expect(wiringStep(st, { type: "cancel" })).toEqual({ state: idle, effect: null });
  });
  it("ignores keys while idle", () => {
    expect(wiringStep(idle, { type: "confirm" })).toEqual({ state: idle, effect: null });
    expect(wiringStep(idle, { type: "next" })).toEqual({ state: idle, effect: null });
  });
});
