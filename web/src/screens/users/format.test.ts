import { describe, expect, it } from "vitest";
import { groupTone } from "./format";

describe("groupTone", () => {
  it("is the same tone for the same id, always", () => {
    expect(groupTone("grp_8k2f9a")).toBe(groupTone("grp_8k2f9a"));
    // pinned: a change here recolours every group of every panel
    expect([groupTone("grp_ok"), groupTone("grp_empty"), groupTone("grp_all")]).toEqual(["lavender", "rose", "sky"]);
  });

  it("spreads ids over the six tones and never leaves them", () => {
    const tones = new Set(Array.from({ length: 60 }, (_, i) => groupTone(`grp_${(i * 7919).toString(36)}`)));
    expect([...tones].every((t) => ["lavender", "sky", "sand", "sage", "mint", "rose"].includes(t))).toBe(true);
    expect(tones.size).toBeGreaterThanOrEqual(5);
  });
});
