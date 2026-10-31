import { describe, expect, it } from "vitest";
import { upgradeNodeLink, validateNodeSearch } from "./tabs";

describe("validateNodeSearch", () => {
  it("keeps a known tab other than the first", () => {
    for (const tab of ["ways", "profiles", "exit", "history", "doctor", "server"]) expect(validateNodeSearch({ tab })).toEqual({ tab });
    expect(validateNodeSearch({ tab: "overview" })).toEqual({});
    expect(validateNodeSearch({ tab: "nope" })).toEqual({});
  });

  it("keeps the open way and the new-way dialog only on the ways tab", () => {
    expect(validateNodeSearch({ tab: "ways", way: "via:nod_msk1" })).toEqual({ tab: "ways", way: "via:nod_msk1" });
    expect(validateNodeSearch({ tab: "ways", way: "direct", add: "1" })).toEqual({ tab: "ways", way: "direct", add: "1" });
    expect(validateNodeSearch({ tab: "ways", add: 1 })).toEqual({ tab: "ways", add: "1" });
    expect(validateNodeSearch({ tab: "ways", add: "via:nod_msk1" })).toEqual({ tab: "ways", add: "via:nod_msk1" });
    // the IPv6 entry's dialog: Reachability and the alerts link here
    expect(validateNodeSearch({ tab: "ways", way: "ipv6", add: "ipv6" })).toEqual({ tab: "ways", way: "ipv6", add: "ipv6" });
    expect(validateNodeSearch({ tab: "ways", way: "sideways", add: "prf_1" })).toEqual({ tab: "ways" });
    expect(validateNodeSearch({ tab: "exit", way: "direct" })).toEqual({ tab: "exit" });
  });

  it("keeps the profile to add only on the profiles tab", () => {
    expect(validateNodeSearch({ tab: "profiles", add: "prf_1" })).toEqual({ tab: "profiles", add: "prf_1" });
    expect(validateNodeSearch({ tab: "profiles", add: "" })).toEqual({ tab: "profiles" });
    expect(validateNodeSearch({ tab: "history", add: "prf_1" })).toEqual({ tab: "history" });
    expect(validateNodeSearch({ add: "prf_1" })).toEqual({});
  });

  it("keeps History's part, the events without one", () => {
    expect(validateNodeSearch({ tab: "history", view: "journal" })).toEqual({ tab: "history", view: "journal" });
    expect(validateNodeSearch({ tab: "history", view: "logs" })).toEqual({ tab: "history", view: "logs" });
    expect(validateNodeSearch({ tab: "history", view: "events" })).toEqual({ tab: "history" });
    expect(validateNodeSearch({ tab: "history", view: "nope" })).toEqual({ tab: "history" });
    expect(validateNodeSearch({ tab: "server", view: "logs" })).toEqual({ tab: "server" });
  });

  it("keeps where the events log is read from only on the events", () => {
    expect(validateNodeSearch({ tab: "history", before: 420, size: 25, page: 3 })).toEqual({ tab: "history", before: 420, size: 25 });
    expect(validateNodeSearch({ tab: "history", before: -1, size: "x" })).toEqual({ tab: "history" });
    expect(validateNodeSearch({ tab: "history", view: "journal", before: 420 })).toEqual({ tab: "history", view: "journal" });
    expect(validateNodeSearch({ tab: "exit", before: 420 })).toEqual({ tab: "exit" });
  });
});

describe("upgradeNodeLink", () => {
  it("sends every link made for the old tabs to its new place", () => {
    const cases: [Record<string, unknown>, string, ReturnType<typeof upgradeNodeLink>][] = [
      [{ tab: "settings" }, "", { search: { tab: "server" }, hash: "" }],
      [{ tab: "settings" }, "warp", { search: { tab: "exit" }, hash: "warp" }],
      [{ tab: "users" }, "", { search: {}, hash: "people" }],
      [{ tab: "logs" }, "", { search: { tab: "history", view: "logs" }, hash: "" }],
      [{ tab: "journal" }, "", { search: { tab: "history", view: "journal" }, hash: "" }],
      [{ tab: "events" }, "", { search: { tab: "history" }, hash: "" }],
      [{ tab: "events", before: 420, size: 25 }, "", { search: { tab: "history", before: 420, size: 25 }, hash: "" }],
      [{ tab: "profiles" }, "ports", { search: { tab: "ways", way: "direct" }, hash: "" }],
      [{ tab: "profiles" }, "relays", { search: { tab: "ways" }, hash: "" }],
    ];
    for (const [search, hash, want] of cases) expect(upgradeNodeLink(search, hash), JSON.stringify(search) + hash).toEqual(want);
  });

  it("leaves the new links and the bare page alone", () => {
    for (const s of [{}, { tab: "profiles", add: "prf_1" }, { tab: "profiles" }, { tab: "ways", way: "direct" }, { tab: "exit" }, { tab: "history", view: "journal" }, { tab: "server" }, { tab: "nope" }])
      expect(upgradeNodeLink(s, ""), JSON.stringify(s)).toBeUndefined();
    expect(upgradeNodeLink({ tab: "exit" }, "warp")).toBeUndefined();
  });
});
