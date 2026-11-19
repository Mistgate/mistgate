import { describe, expect, it } from "vitest";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import type { Lane } from "@/lib/reachability";
import { daysOf } from "./nodes";

const now = Math.floor(new Date(2026, 10, 19, 18, 0, 0).getTime() / 1000);
const week = now - 7 * 86400;
const lane = (source: string, verdict: CountryVerdict): Lane => ({ source, untilUnix: now, points: [{ atUnix: week, verdict }] }) as unknown as Lane;

describe("the seven days of a node", () => {
  it("are not coloured by a block list that holds the address: the live sources decide", () => {
    const days = daysOf([lane("lists", CountryVerdict.LISTED), lane("globalping", CountryVerdict.OK)], now);
    expect(days).toHaveLength(7);
    expect(days.every((d) => d.verdict === CountryVerdict.OK)).toBe(true);
  });

  it("stay empty when a list hit is all there is", () => {
    expect(daysOf([lane("lists", CountryVerdict.LISTED)], now).every((d) => d.verdict == null)).toBe(true);
  });

  it("still take the worst of the live sources", () => {
    const days = daysOf([lane("globalping", CountryVerdict.OK), lane("tspu", CountryVerdict.BLOCKED)], now);
    expect(days.every((d) => d.verdict === CountryVerdict.BLOCKED)).toBe(true);
  });
});
