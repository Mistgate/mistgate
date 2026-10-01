import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { setBrand } from "@/lib/brand";
import { readFileSync } from "node:fs";
import { AnimatedMark, Brand, Logo } from "./brand";

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  act(() => setBrand({ logoSvg: null }));
});

function mount(ui: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(ui));
  return host;
}

describe("Logo", () => {
  it("inlines the built-in badge at the asked size, tinted by the accent, with ids unique per instance", () => {
    const el = mount(
      <>
        <Logo size={32} />
        <Logo size={32} />
      </>,
    );
    const svgs = [...el.querySelectorAll<SVGSVGElement>("svg.mg-badge")];
    expect(svgs).toHaveLength(2);
    const [first] = svgs as [SVGSVGElement, SVGSVGElement];
    expect(first.getAttribute("width")).toBe("32");
    expect((first.parentElement as HTMLElement).style.getPropertyValue("--mg-accent")).toBe("var(--accent)");
    const [a, b] = svgs.map((s) => s.querySelector("clipPath")!.id);
    expect(a).toMatch(/^mg-clip-/);
    expect(a).not.toBe(b);
    expect(first.querySelector("g")!.getAttribute("clip-path")).toBe(`url(#${a})`);
  });

  it("inlines an instance-uploaded logo and goes back to the built-in badge when it is removed", () => {
    act(() => setBrand({ logoSvg: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><path fill="#693fc2" d="M0 0h9v9z"/></svg>' }));
    const el = mount(<Logo size={41} />);
    expect(el.querySelector("svg.mg-badge")).toBeNull();
    expect(el.querySelector("svg path")?.getAttribute("style")).toBe("fill:var(--logo-d)");
    act(() => setBrand({ logoSvg: null }));
    expect(el.querySelector("svg.mg-badge")).not.toBeNull();
  });
});

describe("AnimatedMark", () => {
  const custom = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><path fill="#693fc2" d="M0 0h9v9z"/></svg>';

  it("is the very same badge as Logo, only wrapped: the parts the motion drives are all there, and it is decorative", () => {
    const el = mount(
      <>
        <Logo size={41} />
        <AnimatedMark size={41} mode="intro" />
      </>,
    );
    const [still, moving] = [...el.querySelectorAll<SVGSVGElement>("svg.mg-badge")] as [SVGSVGElement, SVGSVGElement];
    const parts = (s: SVGSVGElement) => [...s.querySelectorAll("[class]")].map((p) => p.getAttribute("class")).join(" ");
    expect(parts(moving)).toBe(parts(still));
    for (const part of ["mg-deep", "mg-paper", "mg-acc", "mg-eye", "mg-wink", "mg-mist"]) expect(moving.querySelector(`.${part}`)).not.toBeNull();
    const box = moving.closest<HTMLElement>(".mg-motion")!;
    expect(box.dataset).toMatchObject({ mode: "intro", kind: "builtin" });
    expect(box.getAttribute("aria-hidden")).toBe("true");
    expect(moving.getAttribute("width")).toBe("41");
  });

  it("an uploaded logo gets the container only: the logo inside is untouched", () => {
    act(() => setBrand({ logoSvg: custom }));
    const el = mount(<AnimatedMark size={56} mode="loading" />);
    const box = el.querySelector<HTMLElement>(".mg-motion")!;
    expect(box.dataset).toMatchObject({ mode: "loading", kind: "custom" });
    expect(box.querySelector("svg.mg-badge")).toBeNull();
    expect(box.querySelector("path")!.getAttribute("style")).toBe("fill:var(--logo-d)");
    expect(box.querySelectorAll("svg [class]")).toHaveLength(0); // nothing inside for the CSS motion to grab
  });

  it("Brand plays the intro only when asked: the sidebar's stays still", () => {
    const el = mount(
      <>
        <Brand />
        <Brand motion="intro" />
      </>,
    );
    expect(el.querySelectorAll(".mg-motion")).toHaveLength(1);
  });
});

describe("mark-motion.css", () => {
  // read as a file: vitest turns a CSS import (even ?raw) into an empty string
  const motionCss = readFileSync("src/assets/mark-motion.css", "utf8") // vitest runs in web/;
  const keyframes = (name: string) => motionCss.match(new RegExp(`@keyframes ${name} \\{([\\s\\S]*?)\\n\\}`))?.[1] ?? "";

  it("the intro only ever states where a part starts: it ends on the static badge by construction", () => {
    for (const name of ["mg-pop", "mg-mist", "mg-rise", "mg-pool", "mg-cat", "mg-eye"]) {
      const body = keyframes(name);
      expect(body, name).toMatch(/from \{/);
      expect(body, name).not.toMatch(/\bto \{|100%/);
    }
  });

  it("reduced motion switches every animation off, whatever the mode", () => {
    const reduced = motionCss.slice(motionCss.indexOf("@media (prefers-reduced-motion: reduce)"));
    expect(reduced).toContain(".mg-motion *");
    expect(reduced).toMatch(/animation: none !important/);
  });

  it("never animates `transform` (the eye has a rotate() attribute that would be overridden)", () => {
    expect(motionCss.replace(/\/\*[\s\S]*?\*\//g, "")).not.toMatch(/\btransform:/);
  });
});