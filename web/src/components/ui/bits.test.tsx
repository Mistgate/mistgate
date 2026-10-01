import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { Avatar, SectionLabel } from "./bits";

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
  return host.firstElementChild as HTMLElement;
}

describe("Avatar", () => {
  it("shows the upper-case first letter of a Russian or English name", () => {
    expect(mount(<Avatar name="тест" />).textContent).toBe("Т");
    act(() => root!.render(<Avatar name="alice" />));
    expect(host!.textContent).toBe("A");
  });

  it("centres the letter with flex and a line height of one, at every size", () => {
    for (const size of [24, 26, 28, 32, 40]) {
      const el = mount(<Avatar name="Б" size={size} />);
      expect(el.className).toContain("flex");
      expect(el.className).toContain("items-center");
      expect(el.className).toContain("justify-center");
      expect(el.className).toContain("leading-none");
      expect(el.style.width).toBe(`${size}px`);
      expect(el.style.height).toBe(`${size}px`);
      expect(el.firstElementChild!.className).toContain("leading-none");
      act(() => root?.unmount());
      host?.remove();
    }
  });
});

describe("SectionLabel", () => {
  it("is a bare label without an icon", () => {
    const el = mount(<SectionLabel>Traffic</SectionLabel>);
    expect(el.textContent).toBe("Traffic");
    expect(el.querySelector("svg")).toBeNull();
  });

  it("puts a decorative tinted chip before the text when given an icon and a tone", () => {
    const el = mount(
      <SectionLabel icon="traffic" tone="sky" as="h2">
        Traffic
      </SectionLabel>,
    );
    expect(el.tagName).toBe("H2");
    expect(el.textContent).toBe("Traffic");
    const chip = el.querySelector<HTMLElement>("[data-tone]")!;
    expect(chip.dataset.tone).toBe("sky");
    expect(chip.getAttribute("aria-hidden")).toBe("true");
    expect(chip.querySelector("svg")).not.toBeNull();
  });
});
