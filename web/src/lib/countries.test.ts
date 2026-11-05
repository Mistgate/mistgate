import { describe, expect, it } from "vitest";
import { countryCodes } from "./countries";

describe("country options", () => {
  it("offers mainland China", () => {
    expect(countryCodes).toContain("CN");
  });
});
