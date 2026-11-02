import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { setLang } from "@/i18n";
import { Icon } from "./icons";
import { TextField } from "./text-field";

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  act(() => setLang("en"));
  localStorage.clear();
});

function render(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(node));
  return host;
}

describe("right to left", () => {
  it("a mono field (an IP, a port, a key) stays left to right inside a Persian page; a text field follows the page", () => {
    act(() => setLang("fa"));
    expect(document.documentElement.dir).toBe("rtl");
    const el = render(
      <>
        <TextField mono aria-label="ip" defaultValue="203.0.113.7" />
        <TextField aria-label="name" defaultValue="کاربر" />
      </>,
    );
    const [ip, name] = [...el.querySelectorAll("input")];
    expect(ip!.getAttribute("dir")).toBe("ltr");
    expect(name!.hasAttribute("dir")).toBe(false);
  });

  it("marks only the horizontal glyphs to mirror", () => {
    const el = render(
      <>
        <Icon name="back" />
        <Icon name="chevronRight" />
        <Icon name="chevronDown" />
      </>,
    );
    expect([...el.querySelectorAll("svg")].map((s) => s.hasAttribute("data-flip"))).toEqual([true, true, false]);
  });
});
