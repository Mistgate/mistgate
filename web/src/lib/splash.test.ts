import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import splashJs from "../../public/splash.js?raw";
import { setBrand } from "./brand";
import { hideSplash } from "./splash";

// The splash markup as index.html has it (only the parts public/splash.js and lib/splash.ts touch).
const markup = `<div id="mg-splash" role="status"><div class="sp-in">
  <span class="sp-mark mg-motion" data-mode="loading" data-kind="builtin"><svg class="mg-badge"></svg></span>
  <span class="sp-wm"><span class="sp-a">mist</span><span class="sp-b">gate</span></span>
  <span class="sp-sr">Loading…</span>
  <div class="sp-fail"><p>Cannot load the panel. Reload the page.</p><button type="button">Reload</button></div>
</div></div>`;

// a classic script: runs in the global scope, like the <script src> does
const run = () => (0, globalThis.eval)(splashJs); // oxlint-disable-line no-eval
const splash = () => document.getElementById("mg-splash");

beforeEach(() => {
  document.body.innerHTML = markup;
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
  document.documentElement.removeAttribute("style");
});
afterEach(() => {
  vi.useRealTimers();
  setBrand({ wordmark: ["mist", "gate"], logoSvg: null });
  document.body.innerHTML = "";
  localStorage.clear();
});

describe("hideSplash", () => {
  it("does nothing without a splash, and twice is the same as once", () => {
    document.body.innerHTML = "";
    expect(() => hideSplash()).not.toThrow();
    document.body.innerHTML = markup;
    splash()!.style.opacity = "1";
    hideSplash();
    hideSplash();
    expect(document.querySelectorAll("#mg-splash")).toHaveLength(1);
  });

  it("removes a splash that has not shown yet at once: a fast load never flashes it", () => {
    splash()!.style.opacity = "0";
    hideSplash();
    expect(splash()).toBeNull();
  });

  it("fades a shown splash out in 200 ms, then removes it", () => {
    vi.useFakeTimers();
    splash()!.style.opacity = "1";
    hideSplash();
    expect(splash()!.style.opacity).toBe("0");
    expect(splash()!.style.transition).toContain("200ms");
    expect(splash()!.style.pointerEvents).toBe("none");
    vi.advanceTimersByTime(300);
    expect(splash()).toBeNull();
  });
});

describe("public/splash.js", () => {
  it("applies the saved theme, accent and language before anything else", () => {
    localStorage.setItem("theme", "light");
    localStorage.setItem("accent", "#7dd3a0");
    localStorage.setItem("lang", "ru");
    run();
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(document.documentElement.style.getPropertyValue("--accent")).toBe("#7dd3a0");
    expect(document.documentElement.lang).toBe("ru");
    expect(splash()!.querySelector(".sp-sr")!.textContent).toBe("Загрузка…");
    expect(splash()!.querySelector(".sp-fail button")!.textContent).toBe("Обновить");
  });

  it("falls back to dark, English and the instance accent; a device's own accent beats the instance's", () => {
    localStorage.setItem("instance-accent", "#8ec8ea");
    run();
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(document.documentElement.lang).toBe("en");
    expect(document.documentElement.style.getPropertyValue("--accent")).toBe("#8ec8ea");
    localStorage.setItem("accent", "#e6a6b0");
    run();
    expect(document.documentElement.style.getPropertyValue("--accent")).toBe("#e6a6b0");
    localStorage.setItem("accent", "javascript:1"); // not a colour: ignored
    run();
    expect(document.documentElement.style.getPropertyValue("--accent")).toBe("#e6a6b0");
  });

  it("shows the brand lib/brand.ts cached: wordmark parts, and an uploaded logo in place of the badge", () => {
    setBrand({
      wordmark: ["acme", "vpn"],
      logoSvg: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><path fill="#693fc2" d="M0 0h9v9z"/></svg>',
    });
    run();
    expect(splash()!.querySelector(".sp-a")!.textContent).toBe("acme");
    expect(splash()!.querySelector(".sp-b")!.textContent).toBe("vpn");
    const mark = splash()!.querySelector(".sp-mark")!;
    expect(mark.getAttribute("data-kind")).toBe("custom");
    expect(mark.querySelector("path")!.getAttribute("style")).toBe("fill:var(--logo-d)");
    expect(mark.querySelector(".mg-badge")).toBeNull();
  });

  it("keeps the built-in badge for a missing, broken or unsafe cache", () => {
    for (const cache of ["not json", '{"w":["a"],"l":1}', JSON.stringify({ w: ["a", "b"], l: '<svg onload="x()"></svg>' }), JSON.stringify({ w: ["a", "b"], l: "<img src=x>" })]) {
      document.body.innerHTML = markup;
      localStorage.setItem("brand", cache);
      expect(run).not.toThrow();
      expect(splash()!.querySelector(".sp-mark")!.getAttribute("data-kind")).toBe("builtin");
      expect(splash()!.querySelector(".mg-badge")).not.toBeNull();
    }
  });

  it("says the panel could not load after ~10 s, unless the app took the splash down", () => {
    vi.useFakeTimers();
    run();
    vi.advanceTimersByTime(9_000);
    expect(splash()!.dataset.state).toBeUndefined();
    vi.advanceTimersByTime(1_500);
    expect(splash()!.dataset.state).toBe("fail");

    document.body.innerHTML = markup;
    run();
    splash()!.remove();
    expect(() => vi.advanceTimersByTime(11_000)).not.toThrow();
  });
});
