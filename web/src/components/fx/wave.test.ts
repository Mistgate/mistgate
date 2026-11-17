import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setEffectsEnabled } from "@/lib/effects";
import { originOf, playWave } from "./wave";

// jsdom has no WebGL: getContext answers null, so what plays here is the CSS ring that stands in for the shader
beforeEach(() => {
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
});
afterEach(() => {
  setEffectsEnabled(false);
  document.querySelectorAll(".mg-wave-css, canvas.mg-wave").forEach((e) => e.remove());
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const rings = () => document.querySelectorAll<HTMLElement>(".mg-wave-css");

describe("the wave", () => {
  it("plays nothing while the effects are off", () => {
    playWave({ x: 10, y: 20 }, { light: false });
    expect(rings()).toHaveLength(0);
    expect(document.querySelector("canvas.mg-wave")).toBeNull();
  });

  it("plays nothing with reduced motion, even with the effects on", () => {
    setEffectsEnabled(true);
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("reduce"), addEventListener() {}, removeEventListener() {} }));
    playWave({ x: 10, y: 20 }, { light: false });
    expect(rings()).toHaveLength(0);
  });

  it("without WebGL draws a ring of colour from the point, one at a time, light or dark", () => {
    setEffectsEnabled(true);
    playWave({ x: 10.4, y: 20 }, { light: false });
    const first = rings()[0]!;
    expect(first.style.getPropertyValue("--ox")).toBe("10px");
    expect(first.style.getPropertyValue("--oy")).toBe("20px");
    expect(first.dataset.light).toBe("");
    playWave({ x: 50, y: 60 }, { light: false, theme: true }); // a theme change always draws the field as a veil
    expect(rings()).toHaveLength(1);
    expect(rings()[0]!.dataset.light).toBe("1");
  });

  it("starts from the middle of the control that was pressed", () => {
    const el = document.createElement("button");
    el.getBoundingClientRect = () => ({ left: 100, top: 40, width: 40, height: 20, right: 140, bottom: 60, x: 100, y: 40, toJSON() {} });
    expect(originOf(el)).toEqual({ x: 120, y: 50 });
  });
});
