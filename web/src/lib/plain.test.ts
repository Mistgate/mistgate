import { describe, expect, it } from "vitest";
import { plain } from "./plain";

describe("plain", () => {
  it("turns bigints into numbers at any depth and drops the message bookkeeping", () => {
    const msg = {
      $typeName: "x.Node",
      $unknown: undefined,
      id: "nod_1",
      bytes: 1_234_567_890_123n,
      online: [{ $typeName: "x.C", protocol: "hysteria2", users: 3 }],
      series: [{ startUnix: 1_800_000_000n, values: [{ value: 5n }] }],
      params: { minutes: "4" },
      reason: undefined,
    };
    expect(plain(msg)).toEqual({
      id: "nod_1",
      bytes: 1_234_567_890_123,
      online: [{ protocol: "hysteria2", users: 3 }],
      series: [{ startUnix: 1_800_000_000, values: [{ value: 5 }] }],
      params: { minutes: "4" },
      reason: undefined,
    });
  });
  it("leaves scalars alone", () => {
    expect(plain("a")).toBe("a");
    expect(plain(7)).toBe(7);
    expect(plain(null)).toBeNull();
  });
});
