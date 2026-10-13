import { describe, expect, it } from "vitest";
import { clickSort, sortOf, validateSort } from "./sort";

describe("the people order in the URL", () => {
  it("keeps only a known sort and a true desc; name ascending is the bare URL", () => {
    expect(validateSort({})).toEqual({});
    expect(validateSort({ sort: "name" })).toEqual({});
    expect(validateSort({ sort: "traffic", desc: 1 })).toEqual({ sort: "traffic", desc: 1 });
    expect(validateSort({ sort: "seen", desc: "1" })).toEqual({ sort: "seen", desc: 1 });
    expect(validateSort({ sort: "seen", desc: true })).toEqual({ sort: "seen", desc: 1 });
    expect(validateSort({ sort: "colour", desc: "no" })).toEqual({});
  });
  it("reads name ascending by default, and name descending from desc alone", () => {
    expect(sortOf({})).toEqual({ sort: "name", desc: false });
    expect(sortOf({ desc: 1 })).toEqual({ sort: "name", desc: true });
    expect(sortOf({ sort: "expiry" })).toEqual({ sort: "expiry", desc: false });
  });
  it("starts a column in its natural direction and reverses it on the next click", () => {
    const name = sortOf({});
    expect(clickSort(name, "traffic")).toEqual({ sort: "traffic", desc: 1 });
    expect(clickSort(sortOf(clickSort(name, "traffic")), "traffic")).toEqual({ sort: "traffic" });
    expect(clickSort(name, "expiry")).toEqual({ sort: "expiry" });
    expect(clickSort(name, "name")).toEqual({ desc: 1 });
  });
});
