import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { qrLogoSrc } from "./brand";
import { cases, sampleQr } from "./dev-data";
import { normalize, qrOf } from "./logic";
import { qrCard } from "./qr";

describe("qrOf: a code the server drew is checked before it is drawn", () => {
  it("takes the code as it was sent", () => {
    expect(qrOf({ ...sampleQr })).toEqual(sampleQr);
  });

  it("is no code when it is not one", () => {
    for (const bad of [null, undefined, "x", 3, [], {}, { ...sampleQr, n: 22 }, { ...sampleQr, n: 41.5 }, { ...sampleQr, n: 181 }, { ...sampleQr, n: "41" }]) expect(qrOf(bad)).toBeNull();
    expect(qrOf({ ...sampleQr, bits: sampleQr.bits.slice(4) })).toBeNull(); // too few bytes
    expect(qrOf({ ...sampleQr, bits: sampleQr.bits + "AAAA" })).toBeNull(); // too many
    expect(qrOf({ ...sampleQr, bits: `<${sampleQr.bits.slice(1)}` })).toBeNull(); // not base64
    expect(qrOf({ ...sampleQr, bits: 5 })).toBeNull();
  });

  it("drops a window that is nonsense, and keeps the code", () => {
    for (const w of [-1, 0, NaN, Infinity, "9", null, 21]) expect(qrOf({ ...sampleQr, window: w }), String(w)).toEqual({ ...sampleQr, window: 0 });
  });
});

describe("qrCard", () => {
  const card = () => qrCard(sampleQr, "QR <img src=x onerror=alert(1)>", "logo.png")!;

  it("is the board's card: white square, one path in the viewBox of the code, the logo in the hole", () => {
    const c = card();
    expect(c.className).toBe("u-qr");
    const svg = c.querySelector(".u-qi > svg")!;
    expect(svg.getAttribute("viewBox")).toBe("0 0 41 41");
    expect(svg.querySelectorAll("path")).toHaveLength(1);
    expect(svg.querySelector("path")!.getAttribute("fill-rule")).toBe("evenodd");
    expect(svg.querySelector("path")!.getAttribute("d")).toMatch(/^M[-\d. MHVhvaz]+$/);
    const mark = c.querySelector<HTMLElement>(".u-qi > .u-qt")!;
    expect(mark.style.width).toBe("24.34%"); // 9.98 of 41 modules
    expect(mark.querySelector("img")!.getAttribute("src")).toBe("logo.png");
  });

  it("draws the three eyes as rounded squares, as the board does, and no data module in them", () => {
    const d = card().querySelector("path")!.getAttribute("d")!;
    // ring r=2, cut-out r=1.2, centre r=0.9, at the top left, the top right (x = n - 7 = 34) and the bottom left
    const eye = (x: number, y: number) =>
      `M${x + 2} ${y}h3a2 2 0 0 1 2 2v3a2 2 0 0 1 -2 2h-3a2 2 0 0 1 -2 -2v-3a2 2 0 0 1 2 -2z` +
      `M${x + 1 + 1.2} ${y + 1}h2.6a1.2 1.2 0 0 1 1.2 1.2v2.6a1.2 1.2 0 0 1 -1.2 1.2h-2.6a1.2 1.2 0 0 1 -1.2 -1.2v-2.6a1.2 1.2 0 0 1 1.2 -1.2z` +
      `M${x + 2 + 0.9} ${y + 2}h1.2a0.9 0.9 0 0 1 0.9 0.9v1.2a0.9 0.9 0 0 1 -0.9 0.9h-1.2a0.9 0.9 0 0 1 -0.9 -0.9v-1.2a0.9 0.9 0 0 1 0.9 -0.9z`;
    const eyes = eye(0, 0) + eye(34, 0) + eye(0, 34);
    expect(d.endsWith(eyes)).toBe(true);
    // the data before them: no figure starts inside an eye's square (the loops start at a corner of the figure)
    for (const m of d.slice(0, d.length - eyes.length).matchAll(/M([\d.]+) ([\d.]+)/g)) {
      const x = Math.floor(+m[1]!), y = Math.floor(+m[2]!);
      expect((x < 7 && y < 7) || (x >= 34 && y < 7) || (x < 7 && y >= 34), `${x},${y}`).toBe(false);
    }
  });

  it("draws the path the Go renderer draws for the same code: the bits, their order, the hole, the corners", () => {
    // sampleQr is Packed() of the "link" sample of internal/qr/testdata/path_golden.json, with its window
    const golden = JSON.parse(readFileSync(resolve(process.cwd(), "../internal/qr/testdata/path_golden.json"), "utf8")) as { name: string; window: number; d_sha256: string }[];
    const link = golden.find((g) => g.name === "link")!;
    expect(sampleQr.window).toBe(link.window);
    const d = card().querySelector("path")!.getAttribute("d")!;
    expect(createHash("sha256").update(d).digest("hex")).toBe(link.d_sha256);
  });

  it("takes the label as text of an attribute, never as markup", () => {
    const c = card();
    expect(c.querySelector("svg")!.getAttribute("aria-label")).toBe("QR <img src=x onerror=alert(1)>");
    expect([...c.querySelectorAll("*")].map((e) => e.tagName.toLowerCase()).sort()).toEqual(["img", "path", "span", "span", "svg"]);
  });

  it("leaves no module in the hole and no logo where the server cut none", () => {
    const withHole = card().querySelector("path")!.getAttribute("d")!;
    const none = qrCard({ ...sampleQr, window: 0 }, "x", "logo.png")!;
    expect(none.querySelector(".u-qt")).toBeNull();
    expect(none.querySelector("path")!.getAttribute("d")!.length).toBeGreaterThan(withHole.length);
  });

  it("is nothing without a code", () => {
    expect(qrCard(null, "x", "logo.png")).toBeNull();
  });
});

describe("qrLogoSrc: the mark in the middle of the code", () => {
  const page = (logo: string) => {
    const d = normalize(structuredClone(cases.locked!));
    d.brand.logo_svg = logo;
    return d;
  };

  it("is the built-in badge in tonal grey: the ink of the code, greys, a white eye; clip and mask kept, nothing worked out from the accent", () => {
    const svg = decodeURIComponent(qrLogoSrc(page("")).replace("data:image/svg+xml,", ""));
    for (const v of ["--mg-ink:#0c0c0e", "--mg-deep:#8a8a93", "--mg-a:#c9c9d0", "--mg-paper:#f4f4f6", "--mg-mist:#fff", "--mg-eye:#fff"]) expect(svg).toContain(v);
    expect(svg).toContain('clip-path="url(#mg-clip)"');
    expect(svg).toContain('mask="url(#mg-gap)"');
    expect(svg).not.toContain("oklch");
  });

  it("is the instance's own logo as it is", () => {
    const own = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><path d="M0 0h8v8z" fill="#123456"/></svg>';
    expect(decodeURIComponent(qrLogoSrc(page(own)).replace("data:image/svg+xml,", ""))).toBe(own);
  });
});
