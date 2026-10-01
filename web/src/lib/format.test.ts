import { describe, expect, it } from "vitest";
import { fill } from "@/i18n";
import { ru } from "@/i18n/ru";
import { agoOf, coarseDuration, dateTimeOf, dayOf, minutesText, scaleBytes, stampOf } from "./format";

describe("minutesText", () => {
  const t = Object.assign((key: keyof typeof ru, vars?: Record<string, string | number>) => fill(ru[key], vars), { n: () => "" });
  it("keeps minutes up to an hour and a half, then two units", () => {
    expect(minutesText(t as never, 47)).toBe("47 мин");
    expect(minutesText(t as never, 90)).toBe("90 мин");
    expect(minutesText(t as never, 125)).toBe("2 ч 5 мин");
    expect(minutesText(t as never, 180)).toBe("3 ч");
    expect(minutesText(t as never, 2880)).toBe("2 дн");
    expect(minutesText(t as never, 3065)).toBe("2 дн 3 ч");
  });
  it("never shows nonsense for a missing or negative value", () => {
    expect(minutesText(t as never, Number(undefined))).toBe("0 мин");
    expect(minutesText(t as never, -5)).toBe("0 мин");
  });
});

describe("scaleBytes", () => {
  it("scales by 1000 for traffic and by 1024 for RAM and disk", () => {
    expect(scaleBytes(999)).toEqual({ value: 999, unit: 0 });
    expect(scaleBytes(1_500_000)).toEqual({ value: 1.5, unit: 2 });
    expect(scaleBytes(2 * 1024 ** 3, 1024)).toEqual({ value: 2, unit: 3 });
    expect(scaleBytes(3 * 1000 ** 4)).toEqual({ value: 3, unit: 4 });
  });
  it("stops at terabytes and never goes negative", () => {
    expect(scaleBytes(5000 * 1000 ** 4).unit).toBe(4);
    expect(scaleBytes(-5)).toEqual({ value: 0, unit: 0 });
  });
});

describe("coarseDuration", () => {
  it("picks days, hours or minutes", () => {
    expect(coarseDuration(27 * 86400 + 5)).toEqual([27, "d"]);
    expect(coarseDuration(5 * 3600 + 59)).toEqual([5, "h"]);
    expect(coarseDuration(540)).toEqual([9, "m"]);
    expect(coarseDuration(10)).toEqual([0, "m"]);
  });
});

describe("stampOf", () => {
  const now = new Date(2026, 8, 30, 15, 0, 0);
  const at = (d: Date) => Math.floor(d.getTime() / 1000);
  it("shows only the time for today and the date with the time for other days", () => {
    expect(stampOf(at(new Date(2026, 8, 30, 14, 6)), "en", now)).toMatch(/^14:06$/);
    const other = stampOf(at(new Date(2026, 8, 27, 3, 0)), "en", now);
    expect(other).toMatch(/27/);
    expect(other).toMatch(/03:00$/);
  });
  it("writes the day the same way everywhere, the year only when it is not this one", () => {
    expect(dateTimeOf(at(new Date(2026, 8, 28, 18, 15)), "ru", now)).toBe("28 сент., 18:15");
    expect(dayOf(at(new Date(2025, 10, 30)), "ru", now)).toMatch(/^30 нояб\. 2025/);
  });
});

describe("agoOf", () => {
  const t = (key: keyof typeof ru, vars?: Record<string, string | number>) => fill(ru[key], vars);
  it("is short in every list", () => {
    expect(agoOf(t, 1000 - 20, 1000)).toBe("только что");
    expect(agoOf(t, 10_000 - 5 * 60, 10_000)).toBe("5 мин назад");
    expect(agoOf(t, 100_000 - 3 * 3600 - 59, 100_000)).toBe("3 ч назад");
    expect(agoOf(t, 1_000_000 - 2 * 86400, 1_000_000)).toBe("2 дн назад");
    expect(agoOf(t, 1010, 1000)).toBe("только что"); // a clock a little ahead is not "in 10 s"
  });
});
