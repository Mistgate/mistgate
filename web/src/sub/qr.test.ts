import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { sampleQr } from "./dev-data";
import { qrOf } from "./logic";
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
    expect(svg.querySelector("path")!.getAttribute("d")).toMatch(/^M[-\d. MHVaz]+$/);
    const mark = c.querySelector<HTMLElement>(".u-qi > .u-qt")!;
    expect(mark.style.width).toBe("24.34%"); // 9.98 of 41 modules
    expect(mark.querySelector("img")!.getAttribute("src")).toBe("logo.png");
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
