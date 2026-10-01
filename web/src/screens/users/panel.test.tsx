import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { Panel } from "./ui";

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

describe("Panel", () => {
  it("renders the tinted icon chip before the title when given an icon and a tone", () => {
    const el = mount(
      <Panel title="Devices" icon="phone" tone="mint">
        body
      </Panel>,
    );
    const chip = el.querySelector<HTMLElement>("[data-tone]");
    expect(chip?.dataset.tone).toBe("mint");
    expect(chip?.getAttribute("aria-hidden")).toBe("true");
    expect(el.textContent).toBe("Devicesbody");
  });

  it("has no chip without an icon", () => {
    expect(mount(<Panel title="Devices">body</Panel>).querySelector("[data-tone]")).toBeNull();
  });
});
