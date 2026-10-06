import { describe, expect, it } from "vitest";
import { validateNodeSearch } from "./tabs";

describe("validateNodeSearch", () => {
  it("keeps a known tab other than the first", () => {
    expect(validateNodeSearch({ tab: "logs" })).toEqual({ tab: "logs" });
    expect(validateNodeSearch({ tab: "overview" })).toEqual({});
    expect(validateNodeSearch({ tab: "nope" })).toEqual({});
  });

  it("keeps the profile to add only on the profiles tab", () => {
    expect(validateNodeSearch({ tab: "profiles", add: "prf_1" })).toEqual({ tab: "profiles", add: "prf_1" });
    expect(validateNodeSearch({ tab: "profiles", add: "" })).toEqual({ tab: "profiles" });
    expect(validateNodeSearch({ tab: "logs", add: "prf_1" })).toEqual({ tab: "logs" });
    expect(validateNodeSearch({ add: "prf_1" })).toEqual({});
  });

  it("keeps where the events log is read from only on the events tab", () => {
    expect(validateNodeSearch({ tab: "events", before: 420, size: 25, page: 3 })).toEqual({ tab: "events", before: 420, size: 25 });
    expect(validateNodeSearch({ tab: "events", before: -1, size: "x" })).toEqual({ tab: "events" });
    expect(validateNodeSearch({ tab: "logs", before: 420 })).toEqual({ tab: "logs" });
  });
});
