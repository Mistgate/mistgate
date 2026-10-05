import { describe, expect, it } from "vitest";
import { roundMbps } from "./bandwidth";

describe("roundMbps", () => {
  it("rounds the way a plan is written", () => {
    expect(roundMbps(7)).toBe(7);
    expect(roundMbps(93)).toBe(95);
    expect(roundMbps(937)).toBe(940);
    expect(roundMbps(940)).toBe(940);
    expect(roundMbps(1480)).toBe(1500);
    expect(roundMbps(9420)).toBe(9400);
  });
  it("never gives 0, which would switch the percentages off, nor more than the field takes", () => {
    expect(roundMbps(0.3)).toBe(1);
    expect(roundMbps(0)).toBe(1);
    expect(roundMbps(5_000_000)).toBe(1_000_000);
  });
});
