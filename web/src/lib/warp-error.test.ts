import { existsSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { fill, type Lang, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { attentionWords, describeWarpError, splitWarpError, warpCheckFailed, warpErrorLine } from "./warp-error";

const dict = { en, ru };
const tOf = (lang: Lang) => Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(dict[lang][key], vars), { n: () => "" }) as unknown as T;

// Every last_error the agent can produce (internal/node/warp/manager.go): reasons, then the recovery notes it appends.
const reasons = [
  "probe_cloudflare_failed",
  "probe_other_failed",
  "warp_flag_off",
  "handshake_stale",
  "handshake_never",
  "link_down",
  "stat_failed",
  "backend_unavailable",
  "up_failed: open /dev/net/tun: no such file or directory",
];
const notes = [
  "reassert",
  "reassert failed",
  "rotated to 162.159.192.1:500",
  "rotate 162.159.192.1:500 failed: operation not permitted",
  "refresh requested",
  "owner action needed",
  "every endpoint tried",
];
const attention = ["revoked", "down_after_ladder", "table_in_use", "rule_pref_in_use", "apply_failed"];

describe("WARP's last_error in words", () => {
  it("splits the reason from the ladder note, and a bare note has no reason", () => {
    expect(splitWarpError("probe_cloudflare_failed; ladder: reassert")).toEqual({ reason: "probe_cloudflare_failed", note: "reassert" });
    expect(splitWarpError("ladder: rotated to 1.2.3.4:500")).toEqual({ reason: "", note: "rotated to 1.2.3.4:500" });
    expect(splitWarpError("link_down")).toEqual({ reason: "link_down", note: "" });
    expect(warpCheckFailed("ladder: reassert")).toBe(false);
    expect(warpCheckFailed("link_down; ladder: reassert")).toBe(true);
    expect(warpCheckFailed("")).toBe(false);
  });

  it("gives every reason and every note of the agent a sentence in both languages, with nothing raw left over", () => {
    for (const lang of ["en", "ru"] as const) {
      const t = tOf(lang);
      for (const reason of reasons) {
        const w = describeWarpError(t, reason)!;
        expect(w.head, `${lang} ${reason}`).toBeTruthy();
        expect(w.raw, `${lang} ${reason}`).toBe("");
        expect(`${w.head} ${w.more}`, `${lang} ${reason}`).not.toMatch(/[{}]|undefined|probe_|handshake_|warp_flag/);
      }
      for (const note of notes) {
        const w = describeWarpError(t, `probe_other_failed; ladder: ${note}`)!;
        expect(w.ladder, `${lang} ${note}`).toBeTruthy();
        expect(w.raw, `${lang} ${note}`).toBe("");
        expect(w.ladder, `${lang} ${note}`).not.toMatch(/[{}]|undefined/);
      }
      for (const code of attention) expect(code === "down_after_ladder" || attentionWords(t, code), `${lang} ${code}`).toBeTruthy();
    }
  });

  it("words the owner's case: the Cloudflare probe timed out and the node reasserted", () => {
    const w = describeWarpError(tOf("ru"), "probe_cloudflare_failed; ladder: reassert")!;
    expect(w.head).toBe("Проверка через WARP до Cloudflare завершилась ошибкой.");
    expect(w.more).toContain("Туннель поднят");
    expect(w.ladder).toBe("Пробую восстановить: переподключение.");
    expect(warpErrorLine(tOf("ru"), "probe_cloudflare_failed; ladder: reassert")).toBe("Проверка через WARP до Cloudflare завершилась ошибкой. Пробую восстановить: переподключение.");
    expect(describeWarpError(tOf("en"), "warp_flag_off")!.head).toBe("Cloudflare answered, but the traffic does not go through WARP (warp=off).");
    expect(describeWarpError(tOf("ru"), "ladder: rotated to 162.159.192.1:500")!.ladder).toContain("162.159.192.1:500");
  });

  it("falls back to the raw code, small and untouched, for a code it does not know", () => {
    const t = tOf("ru");
    expect(describeWarpError(t, "")).toBeNull();
    expect(describeWarpError(t, "brand_new_failure")).toEqual({ head: "", more: "", ladder: "", raw: "brand_new_failure" });
    expect(describeWarpError(t, "link_down; ladder: dance")).toMatchObject({ ladder: "", raw: "dance" });
    expect(warpErrorLine(t, "brand_new_failure; ladder: reassert")).toBe("brand_new_failure Пробую восстановить: переподключение.");
  });

  it("knows every code the manager can set (read from the Go source, so a new code cannot be forgotten)", () => {
    const path = `${process.cwd()}/../internal/node/warp/manager.go`; // vitest runs in web/
    if (!existsSync(path)) return; // the web tree built on its own: nothing to compare with
    const go = readFileSync(path, "utf8");
    const codes = new Set([...go.matchAll(/(?:reason = |LastError = )"([a-z_]+)/g)].map((m) => m[1]!).filter((c) => c !== ""));
    expect(codes.size).toBeGreaterThan(5);
    for (const c of codes) {
      const key = c === "warp_flag_" ? "warp.e.warp_flag" : `warp.e.${c}`;
      expect(Object.hasOwn(en, key), `no wording for the agent's code ${c}`).toBe(true);
    }
  });
});
