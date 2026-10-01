import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { Flag, FlagText, hasFlag } from "./flag";

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

function mount(ui: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(ui));
  return host;
}

describe("Flag", () => {
  it("draws a known country and falls back to letters for an unknown one", () => {
    expect(hasFlag("de")).toBe(true);
    expect(hasFlag("xx")).toBe(false);
    const el = mount(
      <>
        <Flag code="DE" />
        <Flag code="xx" />
      </>,
    );
    expect(el.querySelectorAll("svg")).toHaveLength(1);
    expect(el.textContent).toBe("XX");
  });
});

describe("FlagText", () => {
  it("swaps flag emoji for drawn flags and keeps the rest of the text", () => {
    const el = mount(<FlagText text="🇩🇪 de1 · 🇳🇱 nl1" />);
    expect(el.querySelectorAll("svg")).toHaveLength(2);
    expect(el.textContent).toBe(" de1 ·  nl1");
  });
  it("leaves plain text alone", () => {
    expect(mount(<FlagText text="de1" />).textContent).toBe("de1");
  });
});
