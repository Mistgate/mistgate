import { describe, expect, it } from "vitest";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import type { Fmt } from "@/lib/format";
import { devicesView, groupTone, groupTones, leastUsedTone, termView, trafficTone, warnOf } from "./format";
import type { Tx } from "./t";

describe("groupTone", () => {
  it("is the colour stored on the group, whatever its id", () => {
    for (const tone of groupTones) expect(groupTone("grp_ok", tone)).toBe(tone);
    expect(groupTone("grp_a", "mint")).toBe(groupTone("grp_b", "mint"));
  });

  it("falls back to a tone picked from the id while none is stored, the same one every time", () => {
    expect(groupTone("grp_8k2f9a")).toBe(groupTone("grp_8k2f9a", ""));
    expect(groupTone("grp_8k2f9a", "purple")).toBe(groupTone("grp_8k2f9a")); // a name outside the palette is none
    // pinned: a change here recolours every group that has no stored colour
    expect([groupTone("grp_ok"), groupTone("grp_empty"), groupTone("grp_all")]).toEqual(["lavender", "mint", "sand"]);
  });

  it("spreads ids over the palette and never leaves it", () => {
    const tones = new Set(Array.from({ length: 60 }, (_, i) => groupTone(`grp_${(i * 7919).toString(36)}`)));
    expect([...tones].every((t) => (groupTones as readonly string[]).includes(t))).toBe(true);
    expect(tones.size).toBeGreaterThanOrEqual(5);
  });

  it("keeps sky and mint last: they are also the Link and Keys chips", () => {
    expect(groupTones).toEqual(["lavender", "sand", "sage", "rose", "sky", "mint"]);
  });
});

describe("leastUsedTone", () => {
  const wear = (...colors: string[]) => colors.map((color) => ({ color }));
  it("takes the first tone of the palette for the first group", () => {
    expect(leastUsedTone([])).toBe("lavender");
  });
  it("takes the earliest tone nobody wears, then the least worn", () => {
    expect(leastUsedTone(wear("lavender", "sand"))).toBe("sage");
    expect(leastUsedTone(wear("lavender", "sand", "sage", "rose", "sky", "mint"))).toBe("lavender");
    expect(leastUsedTone(wear("lavender", "lavender", "sand", "sage", "rose", "sky"))).toBe("mint");
    expect(leastUsedTone(wear("", "lavender"))).toBe("sand");
  });
});
describe("the people table's cells", () => {
  const DAY = 86400;
  const now = 1_800_000_000;
  // a stand-in for the translator: a key, with its count or variables after it
  const t = Object.assign((key: string, vars?: Record<string, unknown>) => (vars ? `${key}:${JSON.stringify(vars)}` : key), {
    n: (key: string, n: number) => `${key}:${n}`,
    opt: () => undefined,
    lang: "en",
    locale: "en",
  }) as unknown as Tx;
  const fmt = { date: (u: number) => `d${u}` } as unknown as Fmt;
  const user = (over: Record<string, unknown> = {}) =>
    ({ status: UserStatus.ACTIVE, expiresUnix: 0, devicesUsed: 0, deviceLimit: 5, usedBytes: 0, quotaBytes: 0, nextResetUnix: 0, lastSeenUnix: now - 60, accessHapp: true, accessAmnezia: false, accessTelegram: false, ...over }) as never;

  it("says the term: days (amber in the last week), expired, no expiry, and off for a disabled person", () => {
    expect(termView(user({ expiresUnix: now + 41 * DAY - 10 }), t, now)).toEqual({ text: "ul.days:41", tone: "" });
    expect(termView(user({ expiresUnix: now + 7 * DAY - 10 }), t, now)).toEqual({ text: "ul.days:7", tone: "warn" });
    expect(termView(user({ expiresUnix: now - DAY, status: UserStatus.EXPIRED }), t, now)).toEqual({ text: "ul.term.expired", tone: "crit" });
    expect(termView(user({ expiresUnix: now - 5 }), t, now).tone).toBe("crit"); // the end passed before the server said so
    expect(termView(user(), t, now)).toEqual({ text: "ul.term.never", tone: "" });
    expect(termView(user({ status: UserStatus.DISABLED, expiresUnix: now + DAY }), t, now)).toEqual({ text: "ul.term.off", tone: "off" });
  });

  it("draws the devices: a dot a place up to six, a bar over six, nothing without a limit, and amber when the limit is reached", () => {
    expect(devicesView({ devicesUsed: 2, deviceLimit: 5 })).toEqual({ text: "2/5", full: false, dots: 5, pct: 40 });
    expect(devicesView({ devicesUsed: 3, deviceLimit: 3 })).toMatchObject({ full: true, dots: 3, pct: 100 });
    expect(devicesView({ devicesUsed: 9, deviceLimit: 10 })).toMatchObject({ dots: 0, pct: 90 });
    expect(devicesView({ devicesUsed: 7, deviceLimit: 5 })).toMatchObject({ full: true, pct: 100 }); // over the limit: the bar stays whole
    expect(devicesView({ devicesUsed: 4, deviceLimit: 0 })).toEqual({ text: "4/∞", full: false, dots: 0, pct: 0 });
  });

  it("tones the traffic bar: quiet, amber from 80 %, red once used up (or when the server says over quota), dashed with no quota", () => {
    expect(trafficTone({ usedBytes: 10, quotaBytes: 100, status: UserStatus.ACTIVE })).toBe("");
    expect(trafficTone({ usedBytes: 80, quotaBytes: 100, status: UserStatus.ACTIVE })).toBe("warn");
    expect(trafficTone({ usedBytes: 100, quotaBytes: 100, status: UserStatus.ACTIVE })).toBe("crit");
    expect(trafficTone({ usedBytes: 5, quotaBytes: 100, status: UserStatus.LIMITED })).toBe("crit");
    expect(trafficTone({ usedBytes: 5e9, quotaBytes: 0, status: UserStatus.ACTIVE })).toBe("inf");
  });

  it("leads an open row with what is wrong, worst first, and with what to do for a person who never connected", () => {
    const warn = (over: Record<string, unknown>, group = { happNodes: 1, amneziaNodes: 0 }) => warnOf(user(over), group, t, fmt, now);
    expect(warn({ status: UserStatus.DISABLED, devicesUsed: 5 })).toEqual({ tone: "crit", text: "ul.warn.off" });
    expect(warn({ status: UserStatus.EXPIRED, expiresUnix: now - 2 * DAY })).toEqual({ tone: "crit", text: "ul.warn.expired:2" });
    expect(warn({ status: UserStatus.LIMITED, nextResetUnix: now + DAY })).toEqual({ tone: "crit", text: `ul.warn.quota:{"date":"d${now + DAY}"}` });
    expect(warn({ status: UserStatus.LIMITED })).toEqual({ tone: "crit", text: "ul.warn.quotaNoReset" });
    expect(warn({ accessHapp: false })).toEqual({ tone: "warn", text: "users.noAccessApps" });
    expect(warn({ accessHapp: false }, { happNodes: 0, amneziaNodes: 0 })?.text).toBe("users.noAccessGroup");
    expect(warn({ expiresUnix: now + 3 * DAY - 10 })).toEqual({ tone: "warn", text: "ul.warn.soon:3" });
    expect(warn({ devicesUsed: 5, deviceLimit: 5 })).toEqual({ tone: "warn", text: `ul.warn.full:{"n":5}` });
    expect(warn({ lastSeenUnix: 0 })).toEqual({ tone: "info", text: "ul.warn.new" });
    expect(warn({})).toBeNull();
  });
});
