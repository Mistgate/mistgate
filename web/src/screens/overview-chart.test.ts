import { beforeAll, describe, expect, it } from "vitest";
import { fill, setLang } from "@/i18n";
import { ru } from "@/i18n/ru";
import { toSeries } from "@/lib/series";
import { gridStep, linePath, stack, xLabels } from "./overview-chart";

// The geometry of the Overview's chart: the board's grid steps, its stepped and smooth lines, the stack of protocols and the marks under it.

beforeAll(() => setLang("ru"));
const t = Object.assign((key: keyof typeof ru, vars?: Record<string, string | number>) => fill(ru[key], vars), { n: () => "" });

describe("the chart", () => {
  it("takes the board's steps for the four grid lines, and a round one past its list", () => {
    expect([0, 3, 5.4, 9, 12, 76, 190].map(gridStep)).toEqual([1, 1, 2, 4, 4, 20, 50]);
    expect(gridStep(900)).toBe(250);
  });

  it("draws people as steps that hold until the next reading, and traffic as a smooth curve ending at the right edge", () => {
    expect(linePath([0, 5, 10], 10, true)).toBe("M0 240.0H180.0V120.0H540.0V0.0H720");
    const smooth = linePath([1, 3, 2, 4], 4, false);
    expect(smooth.startsWith("M0 180.0C")).toBe(true);
    expect(smooth.endsWith("720.0 0.0")).toBe(true);
    expect(linePath([2], 4, false)).toBe("M0 120.0H720");
    expect(linePath([], 4, false)).toBe("");
  });

  it("stacks the protocols bottom to top, and keeps a line for a day with no traffic at all", () => {
    const s = toSeries([
      { startUnix: 0, values: [{ protocol: "vless", value: 5 }, { protocol: "hysteria2", value: 10 }] },
      { startUnix: 3600, values: [{ protocol: "hysteria2", value: 2 }] },
    ]);
    const layers = stack(s);
    expect(layers.map((l) => [l.protocol, l.v, l.cum])).toEqual([
      ["hysteria2", "--pl-a", [10, 2]],
      ["vless", "--pl-b", [15, 2]],
    ]);
    const none = stack(toSeries([{ startUnix: 0, values: [] }]));
    expect(none).toHaveLength(1);
    expect(none[0]!.cum).toEqual([0]);
  });

  it("marks every sixth hour of a day and every other day of a week, and ends with now or today", () => {
    const hours = toSeries(Array.from({ length: 24 }, (_, i) => ({ startUnix: 1_790_000_000 + i * 3600, values: [] })));
    const day = xLabels(hours, false, t as never, "ru-RU");
    expect(day).toHaveLength(5);
    expect(day.at(-1)).toBe("сейчас");
    expect(day.slice(0, 4).every((x) => /^\d\d:00$/.test(x))).toBe(true);
    const week = toSeries(Array.from({ length: 7 }, (_, i) => ({ startUnix: 1_790_000_000 + i * 86_400, values: [] })));
    const days = xLabels(week, true, t as never, "ru-RU");
    expect(days).toHaveLength(4);
    expect(days.at(-1)).toBe("сегодня");
    expect(xLabels(toSeries([]), false, t as never, "ru-RU")).toEqual([]);
  });
});
