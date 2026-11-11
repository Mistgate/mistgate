import { describe, expect, it } from "vitest";
import { fill, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { parseSourcesFailed, parseSourcesUsed, triedText, usedText } from "./update-sources";

const t = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(en[key], vars), { n: () => "" }) as unknown as T;

describe("sources_failed", () => {
  it("words each source with its code, in the order the node tried them", () => {
    expect(triedText(t, "panel:stalled,github:http_404")).toBe("Tried: panel (stalled), GitHub (HTTP 404)");
    expect(triedText(t, "github:not_found")).toBe("Tried: GitHub (not found)");
    for (const code of ["stalled", "slow", "busy", "refused", "timeout", "tls", "pin", "size", "not_found", "error"]) {
      expect(triedText(t, `panel:${code}`), code).not.toContain("up.downloadSource");
    }
  });

  it("turns a code it does not know into a plain error, and drops a source it does not know", () => {
    expect(triedText(t, "panel:made_up_code,github:http_999")).toBe("Tried: panel (error), GitHub (error)");
    expect(parseSourcesFailed("peer:stalled,github:tls")).toEqual([{ source: "github", code: "tls" }]);
    expect(parseSourcesFailed("panel:stalled:extra,github")).toEqual([]);
  });

  it("shows nothing for a value that is not a list of source:code pairs", () => {
    for (const raw of [undefined, "", ",", "<b>panel</b>:stalled", "panel: stalled", "PANEL:STALLED", "panel:stalled\n", "a".repeat(201)]) {
      expect(triedText(t, raw), String(raw)).toBe("");
    }
  });

  it("keeps a long list short", () => {
    expect(parseSourcesFailed(Array(12).fill("panel:stalled").join(","))).toHaveLength(8);
  });
});

describe("sources_used", () => {
  it("names the known sources once each, in order", () => {
    expect(usedText(t, "panel,github")).toBe("panel + GitHub");
    expect(parseSourcesUsed("github,github,peer,panel")).toEqual(["github", "panel"]);
    expect(usedText(t, "<script>")).toBe("");
    expect(usedText(t, undefined)).toBe("");
  });
});
