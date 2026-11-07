import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setEffectsEnabled } from "@/lib/effects";
import { FRESH_TTL_MS, isFresh, markFresh, resetFresh, subscribeFresh, takeFresh } from "./registry";

let now = 1_000_000;
beforeEach(() => {
  now = 1_000_000;
  resetFresh(() => now);
  setEffectsEnabled(true);
});
afterEach(() => {
  resetFresh();
  localStorage.clear();
  setEffectsEnabled(true);
});

describe("the fresh-items registry", () => {
  it("marks an item and lets exactly one taker have it", () => {
    markFresh("user", "u1");
    expect(isFresh("user", "u1")).toBe(true);
    expect(takeFresh("user", "u1")).not.toBeNull();
    expect(takeFresh("user", "u1")).toBeNull();
    expect(isFresh("user", "u1")).toBe(false);
  });

  it("keeps kinds apart and ignores what was never marked (a refetch of the list brings only unmarked ids)", () => {
    markFresh("user", "x1");
    expect(isFresh("node", "x1")).toBe(false);
    expect(takeFresh("node", "x1")).toBeNull();
    for (const id of ["a", "b", "c"]) expect(takeFresh("user", id)).toBeNull();
    expect(isFresh("user", "x1")).toBe(true); // the unrelated takes did not eat it
  });

  it("peeking does not consume", () => {
    markFresh("group", "g1");
    expect(isFresh("group", "g1")).toBe(true);
    expect(isFresh("group", "g1")).toBe(true);
    expect(takeFresh("group", "g1")).not.toBeNull();
  });

  it("lets a mark expire when its item never shows up", () => {
    markFresh("profile", "p1");
    now += FRESH_TTL_MS - 1;
    expect(isFresh("profile", "p1")).toBe(true);
    now += 2;
    expect(isFresh("profile", "p1")).toBe(false);
    expect(takeFresh("profile", "p1")).toBeNull();
  });

  it("numbers items taken together, for the stagger, and starts over after a pause", () => {
    for (const id of ["a", "b", "c"]) markFresh("user", id);
    expect(takeFresh("user", "a")!.order).toBe(0);
    expect(takeFresh("user", "b")!.order).toBe(1);
    expect(takeFresh("user", "c")!.order).toBe(2);
    now += 1000;
    markFresh("user", "d");
    expect(takeFresh("user", "d")!.order).toBe(0);
  });

  it("tells subscribers (an item already on screen may be the one marked)", () => {
    const seen = vi.fn();
    const off = subscribeFresh(seen);
    markFresh("node", "n1");
    expect(seen).toHaveBeenCalledWith("node", "n1");
    off();
    markFresh("node", "n2");
    expect(seen).toHaveBeenCalledTimes(1);
  });

  it("does nothing at all when the effects are off, and ignores an empty id", () => {
    const seen = vi.fn();
    subscribeFresh(seen);
    setEffectsEnabled(false);
    markFresh("user", "u1");
    expect(isFresh("user", "u1")).toBe(false);
    setEffectsEnabled(true);
    markFresh("user", "");
    expect(seen).not.toHaveBeenCalled();
  });
});
