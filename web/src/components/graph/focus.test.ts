import { describe, expect, it } from "vitest";
import { pathsThrough, wireFocus } from "./focus";

// The fleet map in miniature: people reach ro, ru and de directly; ro relays to de; every node leaves to the Internet, de also
// through WARP.
const w = (id: string, from: string, to: string) => ({ id, from: { node: from, port: "out" }, to: { node: to, port: "in" } });
const edges = [
  w("p-ro", "people", "ro"),
  w("p-ru", "people", "ru"),
  w("p-de", "people", "de"),
  w("ro-de", "ro", "de"),
  w("ro-net", "ro", "net"),
  w("ru-net", "ru", "net"),
  w("de-net", "de", "net"),
  w("de-warp", "de", "warp"),
];
const sorted = (s: Set<string>) => [...s].sort();

describe("pathsThrough", () => {
  it("an exit node keeps the people, the entry that relays to it, and its own exits", () => {
    const f = pathsThrough(["de"], edges)!;
    expect(sorted(f.nodes)).toEqual(["de", "net", "people", "ro", "warp"]);
    // people -> ro is on the way to de (through the relay); people -> ru and ro's own exit are not
    expect(sorted(f.edges)).toEqual(["de-net", "de-warp", "p-de", "p-ro", "ro-de"]);
  });

  it("an entry node keeps where it relays to and that node's exits, but not other ways into that node", () => {
    const f = pathsThrough(["ro"], edges)!;
    expect(sorted(f.nodes)).toEqual(["de", "net", "people", "ro", "warp"]);
    // people -> de reaches a node downstream of ro, but not through ro
    expect(f.edges.has("p-de")).toBe(false);
    expect(sorted(f.edges)).toEqual(["de-net", "de-warp", "p-ro", "ro-de", "ro-net"]);
  });

  it("a node with no relays keeps only its own way in and out", () => {
    const f = pathsThrough(["ru"], edges)!;
    expect(sorted(f.nodes)).toEqual(["net", "people", "ru"]);
    expect(sorted(f.edges)).toEqual(["p-ru", "ru-net"]);
  });

  it("several cards keep the union of their paths, and no focus is no dimming", () => {
    const f = pathsThrough(["ru", "de"], edges)!;
    expect(f.nodes.has("ro")).toBe(true);
    expect(f.edges.has("ro-net")).toBe(false);
    expect(pathsThrough([], edges)).toBeNull();
  });

  it("survives a cycle", () => {
    const f = pathsThrough(["a"], [w("ab", "a", "b"), w("ba", "b", "a")])!;
    expect(sorted(f.nodes)).toEqual(["a", "b"]);
    expect(sorted(f.edges)).toEqual(["ab", "ba"]);
  });

  it("a wire's focus is the wire and its two ends", () => {
    const f = wireFocus(edges[3]!);
    expect(sorted(f.nodes)).toEqual(["de", "ro"]);
    expect(sorted(f.edges)).toEqual(["ro-de"]);
  });
});
