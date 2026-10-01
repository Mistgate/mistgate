import { afterEach, describe, expect, it } from "vitest";
import { cases } from "./dev-data";
import { lockView, type LockState } from "./lock";
import { logoMark } from "./mark";
import { normalize } from "./logic";

const data = (logo = "") => {
  const d = normalize(structuredClone(cases.locked!));
  d.brand.logo_svg = logo;
  return d;
};
afterEach(() => document.body.replaceChildren());

describe("logoMark", () => {
  it("the built-in badge is inlined so its parts can move; it plays the intro and is decorative", () => {
    const box = logoMark(data(), 72);
    expect(box.dataset).toMatchObject({ mode: "intro", kind: "builtin" });
    expect(box.getAttribute("aria-hidden")).toBe("true");
    expect(box.style.getPropertyValue("--mg-accent")).toBe("var(--accent)");
    const svg = box.querySelector("svg.mg-badge")!;
    expect(svg.getAttribute("width")).toBe("100%");
    for (const part of ["mg-deep", "mg-paper", "mg-acc", "mg-eye", "mg-wink", "mg-mist"]) expect(svg.querySelector(`.${part}`), part).not.toBeNull();
  });

  it("an uploaded logo stays an <img> (never markup) and only its container animates", () => {
    const box = logoMark(data('<svg viewBox="0 0 8 8"><path d="M0 0h8v8z"/></svg>'), 72);
    expect(box.dataset.kind).toBe("custom");
    expect(box.querySelector("svg")).toBeNull();
    expect(box.querySelector("img")!.getAttribute("src")).toMatch(/^data:image\/svg\+xml,/);
  });

  it("a redrawn mark rests: no replay", () => {
    expect(logoMark(data(), 72, false).dataset.mode).toBe("rest");
  });
});

describe("the password form's mark", () => {
  const st: LockState = { lang: "ru", pw: "", busy: false, error: "", note: "" };
  const a = { lang: () => {}, input: () => {}, submit: () => {} };

  it("sits on top of the card with the padlock on its corner; the intro plays on the first build only", () => {
    const first = lockView(data(), st, a);
    const mark = first.querySelector<HTMLElement>(".lock-mark .mg-motion")!;
    expect(mark.dataset.mode).toBe("intro");
    expect(first.querySelector(".lock-mark .lock-ico")).not.toBeNull();
    // the form is redrawn on an error or a language change: the same mark, at rest
    expect(lockView(data(), { ...st, error: "x" }, a).querySelector<HTMLElement>(".lock-mark .mg-motion")!.dataset.mode).toBe("rest");
  });
});
