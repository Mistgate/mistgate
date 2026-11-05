import { describe, expect, it } from "vitest";
import { countryCodes, isCountryCode } from "./countries";

describe("country options", () => {
  it("offers mainland China", () => {
    expect(countryCodes).toContain("CN");
  });

  it("keeps the list sorted, unique and in upper-case ISO 3166-1 alpha-2", () => {
    // the selects sort by the localized name; the source stays in code order, so a code added twice or out of place shows here
    expect([...countryCodes]).toEqual([...countryCodes].sort());
    expect(new Set(countryCodes).size).toBe(countryCodes.length);
    expect(countryCodes.every((c) => isCountryCode(c) && c === c.toUpperCase())).toBe(true);
  });
});
