import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// Base UI draws a radio, a switch and a checkbox as <span role="..."> that says its state in aria-checked and data-checked, not as a
// <button> that is pressed or an <input> that is checked or disabled. A rule of a screen that still names the button, [aria-pressed],
// :checked or :disabled inside one of these containers styles nothing (it happened to the App versions tiles and chips), so the
// text of every stylesheet is read here. The containers are the classes that hold Radio.Root, in Segmented, FilterChips and by hand.
// vitest does not load CSS, hence a read of the files and not computed styles.
const containers = [
  "sb-tiles", "sb-ch", "sb-lim3", "sb-seg", "pp-seg", "ai-seg", "us-seg", "an-seg", "an-wc", "uq-opts", "dq-cat", "fz-ch", "fz-seg", "nh-chips", "sx-as", "sx-af",
];
// the same class in the profile page (.pf) holds real buttons: only the subscriptions' (.sbp) rows are radios
const sbpOnly = ["pf-opt", "pp-cards"];

const files = (dir: string): string[] =>
  readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? files(join(dir, e.name)) : e.name.endsWith(".css") ? [join(dir, e.name)] : []));

const selectors = (css: string) =>
  [...css.replace(/\/\*[\s\S]*?\*\//g, "").matchAll(/([^{}]+)\{/g)]
    .map((m) => m[1]!.trim())
    .filter((s) => !s.startsWith("@"))
    .flatMap((s) => s.split(","))
    .map((s) => s.trim());

const wrong = /(^|[\s>+~])button(?![\w-])|\[aria-pressed|:checked|:disabled/;

describe("rules of the containers of radios", () => {
  it("name the role and aria-checked, not a button", () => {
    const bad: string[] = [];
    for (const f of files("src")) {
      for (const s of selectors(readFileSync(f, "utf8"))) {
        const classes = [...containers, ...(s.startsWith(".sbp") ? sbpOnly : [])];
        for (const c of classes) {
          const at = new RegExp(`\\.${c}(?![\\w-])`).exec(s);
          if (at && wrong.test(s.slice(at.index + at[0].length))) bad.push(`${f}: ${s}`);
        }
      }
    }
    expect(bad).toEqual([]);
  });

  it("find the classes they are about (a renamed class would leave the check above empty)", () => {
    const css = files("src").map((f) => readFileSync(f, "utf8")).join("\n");
    for (const c of [...containers, ...sbpOnly]) expect(css, c).toMatch(new RegExp(`\\.${c}(?![\\w-])`));
  });

  it("keep the pointer on a switch, a radio and a checkbox (a button had it from the base layer)", () => {
    const base = readFileSync("src/index.css", "utf8");
    for (const role of ["switch", "radio", "checkbox"]) expect(base).toMatch(new RegExp(`\\[role="${role}"\\][^{}]*\\{[^}]*cursor:\\s*pointer`));
  });
});
