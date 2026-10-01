import { describe, expect, it } from "vitest";
import { generateSecret } from "./secret";

describe("generateSecret", () => {
  it("makes 32 URL-safe characters that differ every time", () => {
    const a = generateSecret();
    expect(a).toMatch(/^[A-Za-z0-9_-]{32}$/);
    expect(generateSecret()).not.toBe(a);
  });
});
