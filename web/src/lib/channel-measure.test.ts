import { describe, expect, it, vi } from "vitest";
import { currentStep, errorCodeOf, floorMbps, minutesOf, offer, parseLimit, progressOf, slowerSide, type Step } from "./channel-measure";

vi.mock("@/lib/api", () => ({ probe: {} }));

const step = (state: string, over: Partial<Step> = {}): Step => ({ partner: "nl1", direction: "down", state, mbitS: 0, error: "", streams: 8, capped: false, ...over });

describe("the number offered for the channel field", () => {
  it("rounds DOWN by the step of a plan: under 10 as is, under 100 to 5, under 1000 to 10, above to 50", () => {
    expect(floorMbps(7.9)).toBe(7);
    expect(floorMbps(97.4)).toBe(95);
    expect(floorMbps(455.1)).toBe(450);
    expect(floorMbps(999.9)).toBe(990);
    expect(floorMbps(1049)).toBe(1000);
    expect(floorMbps(1480)).toBe(1450);
    expect(floorMbps(8589.9)).toBe(8550);
    expect(floorMbps(5_000_000)).toBe(1_000_000);
  });

  it("is never above what was measured", () => {
    for (const v of [3.2, 11, 99.99, 100, 250.5, 937, 1000.4, 1234.5, 9999]) expect(floorMbps(v)).toBeLessThanOrEqual(v);
  });

  it("takes the slower side, and nothing when a side did not work", () => {
    expect(slowerSide(812.4, 455.1)).toBe(455.1);
    expect(slowerSide(300, 900)).toBe(300);
    expect(slowerSide(812.4, 0)).toBe(0);
    expect(slowerSide(0, 0)).toBe(0);
    expect(offer(250, 812.4, 455.1)).toEqual({ value: 450, same: false });
    expect(offer(450, 812.4, 455.1)).toEqual({ value: 450, same: true });
    expect(offer(0, 812.4, 455.1)).toEqual({ value: 450, same: false });
    expect(offer(250, 812.4, 0)).toEqual({ value: 0, same: false });
  });

  it("reads the number the owner types: a whole number of 1 to 1000000", () => {
    expect(parseLimit("450")).toBe(450);
    expect(parseLimit(" 1000000 ")).toBe(1_000_000);
    for (const bad of ["", "0", "-5", "12.5", "1e3", "1000001", "abc", "00000001x", "12345678"]) expect(parseLimit(bad), bad).toBeNull();
  });
});

describe("the run as the screen reads it", () => {
  it("finds the step that goes, else the first that waits, none when it is over", () => {
    expect(currentStep([step("done"), step("running", { direction: "up" }), step("waiting")])?.direction).toBe("up");
    expect(currentStep([step("done"), step("waiting", { partner: "se1" })])?.partner).toBe("se1");
    expect(currentStep([step("done"), step("failed")])).toBeUndefined();
  });

  it("counts the steps that are over", () => {
    expect(progressOf([step("done"), step("failed"), step("running"), step("waiting")])).toEqual({ over: 2, total: 4 });
    expect(progressOf([])).toEqual({ over: 0, total: 0 });
  });

  it("words the minutes to wait as at least one, rounded up", () => {
    expect(minutesOf(0)).toBe(1);
    expect(minutesOf(59)).toBe(1);
    expect(minutesOf(61)).toBe(2);
    expect(minutesOf(421)).toBe(8);
  });

  it("knows the panel's codes and calls any other one a failure", () => {
    expect(errorCodeOf("too_soon")).toBe("too_soon");
    expect(errorCodeOf("no_partner")).toBe("no_partner");
    expect(errorCodeOf("whatever the agent said")).toBe("failed");
    expect(errorCodeOf("")).toBe("failed");
  });
});
