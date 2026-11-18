import { describe, expect, it } from "vitest";
import { tabOf, tabOfHash, validateReachSearch } from "./search";

describe("the address of the Reachability page", () => {
  it("keeps a known tab and a known source, drops the rest; Nodes has no key", () => {
    expect(validateReachSearch({ tab: "events" })).toEqual({ tab: "events" });
    expect(validateReachSearch({ tab: "sources", source: "bsbord" })).toEqual({ tab: "sources", source: "bsbord" });
    expect(validateReachSearch({ tab: "nodes", source: "nope" })).toEqual({});
    expect(validateReachSearch({ tab: 3 })).toEqual({});
  });

  it("opens the tab asked for, the Sources tab for a bare ?source=, else Nodes", () => {
    expect(tabOf({})).toBe("nodes");
    expect(tabOf({ tab: "events" })).toBe("events");
    expect(tabOf({ source: "tspu" })).toBe("sources");
    expect(tabOf({ tab: "events", source: "tspu" })).toBe("events");
  });

  it("leads each anchor of the one-page version to its tab", () => {
    expect(tabOfHash("#sources")).toBe("sources");
    expect(tabOfHash("#reach-sources")).toBe("sources");
    expect(tabOfHash("#reach-events")).toBe("events");
    expect(tabOfHash("reach-nodes")).toBe("nodes");
    expect(tabOfHash("#alert-x")).toBeUndefined();
    expect(tabOfHash("")).toBeUndefined();
  });
});
