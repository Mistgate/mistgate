import { describe, expect, it } from "vitest";
import { groupTone, groupTones, leastUsedTone } from "./format";

describe("groupTone", () => {
  it("is the colour stored on the group, whatever its id", () => {
    for (const tone of groupTones) expect(groupTone("grp_ok", tone)).toBe(tone);
    expect(groupTone("grp_a", "mint")).toBe(groupTone("grp_b", "mint"));
  });

  it("falls back to a tone picked from the id while none is stored, the same one every time", () => {
    expect(groupTone("grp_8k2f9a")).toBe(groupTone("grp_8k2f9a", ""));
    expect(groupTone("grp_8k2f9a", "purple")).toBe(groupTone("grp_8k2f9a")); // a name outside the palette is none
    // pinned: a change here recolours every group that has no stored colour
    expect([groupTone("grp_ok"), groupTone("grp_empty"), groupTone("grp_all")]).toEqual(["lavender", "mint", "sand"]);
  });

  it("spreads ids over the palette and never leaves it", () => {
    const tones = new Set(Array.from({ length: 60 }, (_, i) => groupTone(`grp_${(i * 7919).toString(36)}`)));
    expect([...tones].every((t) => (groupTones as readonly string[]).includes(t))).toBe(true);
    expect(tones.size).toBeGreaterThanOrEqual(5);
  });

  it("keeps sky and mint last: they are also the Link and Keys chips", () => {
    expect(groupTones).toEqual(["lavender", "sand", "sage", "rose", "sky", "mint"]);
  });
});

describe("leastUsedTone", () => {
  const wear = (...colors: string[]) => colors.map((color) => ({ color }));
  it("takes the first tone of the palette for the first group", () => {
    expect(leastUsedTone([])).toBe("lavender");
  });
  it("takes the earliest tone nobody wears, then the least worn", () => {
    expect(leastUsedTone(wear("lavender", "sand"))).toBe("sage");
    expect(leastUsedTone(wear("lavender", "sand", "sage", "rose", "sky", "mint"))).toBe("lavender");
    expect(leastUsedTone(wear("lavender", "lavender", "sand", "sage", "rose", "sky"))).toBe("mint");
    expect(leastUsedTone(wear("", "lavender"))).toBe("sand");
  });
});