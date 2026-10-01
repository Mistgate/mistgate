import { describe, expect, it } from "vitest";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { en as profilesEn, ru as profilesRu } from "@/i18n/profiles";
import { en as usersEn, ru as usersRu } from "@/i18n/users";
import { avatarIndex, daysLeft, shownStatus, termText, usage } from "./format";
import type { UserN } from "./model";
import { useTx } from "./t";

describe("dictionaries of the people screens", () => {
  const ph = (s: string) => [...new Set([...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]))].sort().join();
  const forms = (s: string) => s.split("|").length;
  for (const [name, en, ru] of [
    ["users", usersEn, usersRu],
    ["profiles", profilesEn, profilesRu],
  ] as const) {
    it(`${name}: Russian keeps every placeholder, and has three forms where English has two`, () => {
      for (const key of Object.keys(en) as (keyof typeof en)[]) {
        const e = en[key] as string;
        const r = (ru as Record<string, string>)[key]!;
        expect(ph(r), key).toBe(ph(e));
        if (e.includes("|")) expect(forms(r), key).toBe(3);
        else expect(forms(r), key).toBe(1);
      }
    });
  }
  it("do not shadow each other", () => {
    const keys = [...Object.keys(usersEn), ...Object.keys(profilesEn)];
    expect(new Set(keys).size).toBe(keys.length);
  });
  it("useTx is a function", () => {
    expect(typeof useTx).toBe("function");
  });
});

const user = (over: Partial<UserN>): UserN =>
  ({ status: UserStatus.ACTIVE, devicesUsed: 1, deviceLimit: 5, expiresUnix: 0, usedBytes: 0, quotaBytes: 0, ...over }) as UserN;

describe("user display helpers", () => {
  it("derives 'devices full' only for an otherwise active user", () => {
    expect(shownStatus(user({}))).toBe("active");
    expect(shownStatus(user({ devicesUsed: 5 }))).toBe("devices");
    expect(shownStatus(user({ devicesUsed: 5, status: UserStatus.LIMITED }))).toBe("quota");
    expect(shownStatus(user({ status: UserStatus.DISABLED }))).toBe("disabled");
    expect(shownStatus(user({ status: UserStatus.EXPIRED }))).toBe("expired");
  });

  it("counts whole days, rounding up, and treats 0 as never", () => {
    expect(daysLeft(0, 1000)).toBeNull();
    expect(daysLeft(1000 + 86400 * 2 + 5, 1000)).toBe(3);
    expect(daysLeft(1000 - 10, 1000)).toBeLessThanOrEqual(0);
  });

  it("gives a stable avatar slot per id", () => {
    expect(avatarIndex("usr_a")).toBe(avatarIndex("usr_a"));
    expect(avatarIndex("usr_a")).toBeLessThan(9);
  });

  it("formats usage against a quota", () => {
    const fmt = { num: (v: number, d = 1, fixed = false) => (fixed ? v.toFixed(d) : String(Math.round(v * 10) / 10)), bytes: (n: number) => `${n / 1e9} GB` } as never;
    expect(usage(12.3e9, 100e9, fmt)).toMatchObject({ text: "12.3 / 100 GB", tone: "ok" });
    expect(usage(85e9, 100e9, fmt).tone).toBe("high");
    expect(usage(101e9, 100e9, fmt).tone).toBe("over");
    expect(usage(5e9, 0, fmt)).toMatchObject({ text: "5 GB", pct: 0 });
  });

  it("shows the term as days, expired or unlimited", () => {
    const t = ((k: string, v?: Record<string, number>) => (k === "users.days" ? `${v!.n} d` : k)) as never;
    expect(termText(user({ expiresUnix: 0 }), t, 100).text).toBe("∞");
    expect(termText(user({ expiresUnix: 50 }), t, 100)).toMatchObject({ text: "0", tone: "bad" });
    expect(termText(user({ expiresUnix: 100 + 86400 * 3 }), t, 100)).toMatchObject({ text: "3 d", tone: "fg" });
  });
});
