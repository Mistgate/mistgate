import { afterEach, describe, expect, it } from "vitest";
import { NodeShareMode } from "@/gen/mistgate/admin/v1/common_pb";
import { defaultShare, gets, modeOf, protoMode, receivingIds, rememberShare, shareOfIds, sharing, shareValid } from "./node-share";

const groups = [
  { id: "grp_a", disabledNodeIds: [] as string[] },
  { id: "grp_b", disabledNodeIds: ["nod_de1"] },
  { id: "grp_c", disabledNodeIds: ["nod_nl1"] },
];

afterEach(() => localStorage.clear());

describe("node sharing", () => {
  it("reads the three modes of the API both ways", () => {
    expect([NodeShareMode.ALL, NodeShareMode.LISTED, NodeShareMode.NONE].map(modeOf)).toEqual(["all", "listed", "none"]);
    // a mode the panel never answers with reads as nobody
    expect(modeOf(NodeShareMode.UNSPECIFIED)).toBe("none");
    expect((["all", "listed", "none"] as const).map(protoMode)).toEqual([NodeShareMode.ALL, NodeShareMode.LISTED, NodeShareMode.NONE]);
  });

  it("a group gets a node unless it is on the group's disabled list", () => {
    expect(gets(groups[0]!, "nod_de1")).toBe(true);
    expect(gets(groups[1]!, "nod_de1")).toBe(false);
    expect(receivingIds(groups, "nod_de1")).toEqual(["grp_a", "grp_c"]);
    expect(receivingIds(groups, "nod_nl1")).toEqual(["grp_a", "grp_b"]);
  });

  it("the groups switched on make a list; none of them is nobody", () => {
    expect(shareOfIds(["grp_a", "grp_b"])).toEqual({ mode: "listed", groupIds: ["grp_a", "grp_b"] });
    expect(shareOfIds([])).toEqual({ mode: "none", groupIds: [] });
  });

  it("the request names groups only with the mode LISTED", () => {
    expect(sharing({ mode: "listed", groupIds: ["grp_a"] })).toEqual({ mode: NodeShareMode.LISTED, groupIds: ["grp_a"] });
    expect(sharing({ mode: "all", groupIds: ["grp_a"] })).toEqual({ mode: NodeShareMode.ALL, groupIds: [] });
    expect(sharing({ mode: "none", groupIds: [] })).toEqual({ mode: NodeShareMode.NONE, groupIds: [] });
    expect(shareValid({ mode: "listed", groupIds: [] })).toBe(false);
    expect(shareValid({ mode: "listed", groupIds: ["grp_a"] })).toBe(true);
    expect(shareValid({ mode: "none", groupIds: [] })).toBe(true);
  });
});

describe("the choice the New node window remembers", () => {
  it("starts at nobody yet when nothing was chosen", () => {
    expect(defaultShare(groups)).toEqual({ mode: "none", groupIds: [] });
  });

  it("starts at the last choice", () => {
    rememberShare({ mode: "all", groupIds: [] });
    expect(defaultShare(groups)).toEqual({ mode: "all", groupIds: [] });
    rememberShare({ mode: "listed", groupIds: ["grp_a", "grp_c"] });
    expect(defaultShare(groups)).toEqual({ mode: "listed", groupIds: ["grp_a", "grp_c"] });
  });

  it("drops the groups that are gone, and a list left empty is nobody yet", () => {
    rememberShare({ mode: "listed", groupIds: ["grp_a", "grp_gone"] });
    expect(defaultShare(groups)).toEqual({ mode: "listed", groupIds: ["grp_a"] });
    rememberShare({ mode: "listed", groupIds: ["grp_gone"] });
    expect(defaultShare(groups)).toEqual({ mode: "none", groupIds: [] });
  });

  it("keeps the list while the groups are not loaded yet", () => {
    rememberShare({ mode: "listed", groupIds: ["grp_a"] });
    expect(defaultShare(undefined)).toEqual({ mode: "listed", groupIds: ["grp_a"] });
  });

  it("does not remember groups with a mode that names none, and ignores what it cannot read", () => {
    rememberShare({ mode: "all", groupIds: ["grp_a"] });
    expect(JSON.parse(localStorage.getItem("nodes.share")!)).toEqual({ mode: "all", groupIds: [] });
    localStorage.setItem("nodes.share", "not json");
    expect(defaultShare(groups)).toEqual({ mode: "none", groupIds: [] });
    localStorage.setItem("nodes.share", JSON.stringify({ mode: "everybody" }));
    expect(defaultShare(groups)).toEqual({ mode: "none", groupIds: [] });
  });
});
