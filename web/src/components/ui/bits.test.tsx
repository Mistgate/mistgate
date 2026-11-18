import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { Avatar, avatarColors, avatarIndex, SectionLabel } from "./bits";

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

  it("is the board's .ava: a grid that centres the letter, weight 600, one of the 8 pastels, at every size", () => {
    for (const size of [24, 26, 28, 32, 40]) {
      const el = mount(<Avatar name="Б" size={size} index={9} />);
      expect(el.className).toContain("grid");
      expect(el.className).toContain("place-items-center");
      expect(el.className).toContain("leading-none");
      expect(el.className).toContain("font-[600]");
      expect(el.style.width).toBe(`${size}px`);
      expect(el.style.height).toBe(`${size}px`);
      // index 9 wraps around the eight colours to the second one
      expect(el.style.background).toBe("rgb(159, 214, 207)");
      expect(avatarColors).toHaveLength(8);
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("takes its colour slot from the name as the board does: the first letter's code plus the length", () => {
    expect(avatarIndex("carol")).toBe((99 + 5) % 8);
    expect(avatarIndex("")).toBe(0);
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
