import { describe, expect, it } from "vitest";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { fill, pickForm, type T } from "@/i18n";
import { capacityOf, roundMbps, runsLine, type RunInfo } from "./bandwidth";

const say = (lang: "en" | "ru"): T => {
  const d = lang === "en" ? en : ru;
  return Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(d[key], vars), {
    n: (key: keyof typeof en, count: number, vars?: Record<string, string | number>) => fill(pickForm(d[key], count, lang), { n: count, ...vars }),
  }) as T;
};
const info = (p: Partial<RunInfo>): RunInfo => ({ server: "speed.cloudflare.com", serverDetail: "", runs: 3, runsTotal: 3, runFailures: [], ...p });

describe("runsLine", () => {
  it("says what the runs were, with Russian plural forms", () => {
    const ruLine = (runs: number) => runsLine(say("ru"), info({ runs, runsTotal: runs }));
    expect(ruLine(1)).toBe("Через speed.cloudflare.com, лучший из 1 прогона");
    expect(ruLine(2)).toBe("Через speed.cloudflare.com, лучший из 2 прогонов");
    expect(ruLine(3)).toBe("Через speed.cloudflare.com, лучший из 3 прогонов");
    expect(ruLine(5)).toBe("Через speed.cloudflare.com, лучший из 5 прогонов");
    expect(ruLine(21)).toBe("Через speed.cloudflare.com, лучший из 21 прогона");
  });
  it("English: one run, three runs", () => {
    expect(runsLine(say("en"), info({ runs: 1, runsTotal: 1 }))).toBe("Through speed.cloudflare.com, the best of 1 run");
    expect(runsLine(say("en"), info({}))).toBe("Through speed.cloudflare.com, the best of 3 runs");
  });
  it("names the Ookla server", () => {
    expect(runsLine(say("ru"), info({ server: "Ookla", serverDetail: "МТС, Москва" }))).toBe("Через Ookla · МТС, Москва, лучший из 3 прогонов");
    expect(runsLine(say("en"), info({ server: "Ookla", serverDetail: "MTS, Moscow" }))).toBe("Through Ookla · MTS, Moscow, the best of 3 runs");
  });
  it("does not hide failed runs: how many worked and why the others did not", () => {
    const f = (runs: number, runFailures: string[]) => info({ runs, runFailures });
    expect(runsLine(say("ru"), f(1, ["rate_limited", "rate_limited"]))).toBe(
      "Через speed.cloudflare.com, удался 1 прогон из 3: остальные — сервер ограничил запросы",
    );
    expect(runsLine(say("ru"), f(2, ["timeout"]))).toBe("Через speed.cloudflare.com, удалось 2 прогона из 3: неудачный — сервер не ответил вовремя");
    expect(runsLine(say("en"), f(1, ["rate_limited", "http_503"]))).toBe(
      "Through speed.cloudflare.com, 1 of 3 runs worked: the others — the server limited the requests, the server answered with error 503",
    );
    expect(runsLine(say("en"), f(2, ["unreachable"]))).toBe("Through speed.cloudflare.com, 2 of 3 runs worked: the other one — the server did not answer");
    expect(runsLine(say("ru"), f(1, ["failed", "weird"]))).toContain("остальные — прогон не удался");
  });
  it("an agent that reports no totals: all runs it counted worked", () => {
    expect(runsLine(say("ru"), info({ runs: 1, runsTotal: 0 }))).toBe("Через speed.cloudflare.com, лучший из 1 прогона");
  });
  it("failed runs without reasons still say how many worked", () => {
    expect(runsLine(say("ru"), info({ runs: 2 }))).toBe("Через speed.cloudflare.com, удалось 2 прогона из 3");
  });
});

describe("capacityOf", () => {
  it("is the slower direction: a node relays every byte both ways", () => {
    expect(capacityOf(4984, 1080)).toBe(1080); // inbound free, outbound capped at the plan
    expect(capacityOf(870, 940)).toBe(870);
    expect(capacityOf(937, 0)).toBe(937); // no upload figure: the download
  });
});

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
