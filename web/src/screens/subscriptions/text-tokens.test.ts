import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// On the light theme the design's mint, amber and red are marks (a dot, a glyph, a line), not text: as text they are 2.3, 3.9 and 4.4 : 1
// on white. index.css has the text pair of each (--accent-text, --warning-text, --danger-text: 4.5 and more on the light theme, the same
// value as the mark on the dark one). Every rule of the Subscriptions CSS that colours TEXT takes that pair; the few rules that colour a
// glyph or a mark keep the design's value and are named here.
const marks = [
  ".sbp .nx-rad[aria-checked=\"true\"]",
  ".sbp .rw-gl.ok",
  ".sbp .rw-gl.part",
  ".sbp .rw-gl.bad",
  ".sbp .rw-chip.ok>i",
  ".sbp .pp-ic.mint",
  ".sbp .sb-y.y",
  ".sbp .sb-ap.drag .sb-grip",
  ".sbp .sb-cb.on i",
  ".sbp .dq-def:hover .nx-rad",
];

describe("the Subscriptions CSS on the light theme", () => {
  it.each(["subs.css", "subs-hx.css", "subs-dns.css"])("%s colours text with the text tokens, not the marks'", (file) => {
    const css = readFileSync(new URL(`./${file}`, import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
    const bad: string[] = [];
    for (const m of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      const selector = m[1]!.trim().replace(/\s+/g, " ");
      for (const d of m[2]!.matchAll(/(?:^|;|\s)color:\s*var\(--(accent-mark|warning|danger)\)/g)) {
        if (!marks.includes(selector)) bad.push(`${selector} { color: var(--${d[1]}) }`);
      }
    }
    expect(bad).toEqual([]);
  });
});
