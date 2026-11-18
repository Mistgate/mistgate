import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { Check, Chip, SectionLabel } from "./bits";
import { PopFooter, PopRow, Popover } from "./popover";
import { ProtocolTag } from "./protocol-tag";
import { Row, RowHead } from "./row";
import { StatusPill } from "./status";

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

describe("Check", () => {
  function Demo() {
    const [on, setOn] = useState(false);
    return <Check label="Send mail" checked={on} onCheckedChange={setOn} />;
  }

  it("ticks when its words are clicked, and says so as a checkbox", () => {
    const el = mount(<Demo />);
    const box = el.querySelector<HTMLElement>('[role="checkbox"]')!;
    expect(box.getAttribute("aria-checked")).toBe("false");
    expect(el.querySelector("svg")).toBeNull();
    act(() => el.querySelector("label")!.click());
    expect(box.getAttribute("aria-checked")).toBe("true");
    expect(el.querySelector("svg")).not.toBeNull();
  });
});

describe("Row", () => {
  it("is a plain grid line by default and a whole-row button when asked", () => {
    const el = mount(
      <>
        <RowHead className="grid-cols-2">
          <span>Name</span>
          <span>State</span>
        </RowHead>
        <Row className="grid-cols-2">plain</Row>
        <Row as="button" className="grid-cols-2">
          target
        </Row>
      </>,
    );
    const [head, plain, target] = [...el.children] as HTMLElement[];
    expect(head!.getAttribute("role")).toBe("row");
    expect(plain!.tagName).toBe("DIV");
    expect(plain!.className).toContain("min-h-14");
    expect(plain!.className).not.toContain("cursor-pointer");
    expect(target!.tagName).toBe("BUTTON");
    expect(target!.getAttribute("type")).toBe("button");
    expect(target!.className).toContain("cursor-pointer");
    expect(target!.className).toContain("grid-cols-2");
  });
});

describe("ProtocolTag", () => {
  it("names the protocol, takes the short ids too, and keeps the name in a compact tag", () => {
    const el = mount(
      <>
        <ProtocolTag protocol="hysteria2" />
        <ProtocolTag protocol="amneziawg" />
        <ProtocolTag protocol="relay" label="Relay" compact />
        <ProtocolTag protocol="trusttunnel" />
      </>,
    );
    const tags = [...el.children] as HTMLElement[];
    expect(tags.map((t) => t.textContent)).toEqual(["Hysteria2", "AmneziaWG", "Relay", "TrustTunnel"]);
    expect(tags[0]!.className).toContain("bg-tone-mint-bg");
    expect(tags[1]!.className).toContain("bg-tone-lavender-bg");
    expect(tags[3]!.className).toContain("bg-tone-sage-bg");
    expect(tags[2]!.getAttribute("title")).toBe("Relay");
    expect(tags[2]!.getAttribute("tabindex")).toBe("0");
    // a tag in a list is the board's plain one: no glyph, regular weight; only the compact one draws the glyph
    expect(tags[0]!.querySelector("svg")).toBeNull();
    expect(tags[0]!.className).not.toContain("font-medium");
    expect(tags[2]!.querySelector("svg")!.getAttribute("stroke-width")).toBe("1.5");
  });

  it("opens a compact tag by itself with `expanded` (a phone has no pointer) and leaves the others folded", () => {
    const el = mount(
      <>
        <ProtocolTag protocol="vless" compact expanded />
        <ProtocolTag protocol="vless" compact />
      </>,
    );
    const label = (i: number) => (el.children[i] as HTMLElement).querySelector("span")!.className;
    expect(label(0)).toContain("max-w-[120px]");
    expect(label(0)).not.toContain("opacity-0");
    expect(label(1)).toContain("max-w-0");
  });

  it("falls back to a grey tag with the raw id for a protocol it does not know", () => {
    const el = mount(<ProtocolTag protocol="quic9" />);
    expect(el.textContent).toBe("quic9");
    expect((el.firstElementChild as HTMLElement).className).toContain("bg-step");
  });
});

describe("Popover", () => {
  it("opens on its trigger and shows its rows and footer, then closes on Escape", async () => {
    mount(
      <Popover trigger={<button type="button">Fleet</button>}>
        <PopRow>de1</PopRow>
        <PopFooter href="#all">All nodes</PopFooter>
      </Popover>,
    );
    expect(document.body.textContent).not.toContain("de1");
    await act(async () => host!.querySelector("button")!.click());
    expect(document.body.textContent).toContain("de1");
    expect(document.querySelector<HTMLAnchorElement>("a[href='#all']")).not.toBeNull();
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    await act(async () => {});
    expect(document.querySelector("a[href='#all']")).toBeNull();
  });
});

describe("tags and headings", () => {
  it("a tone gives a tag that tone's pair, and a heading is quiet-capitals only when asked", () => {
    const el = mount(
      <>
        <Chip tone="sky">tag</Chip>
        <Chip>plain</Chip>
        <SectionLabel count={3}>Nodes</SectionLabel>
        <SectionLabel caps>Section</SectionLabel>
        <StatusPill kind="ok" />
      </>,
    );
    const [sky, plain, head, caps, pill] = [...el.children] as HTMLElement[];
    expect(sky!.className).toContain("bg-tone-sky-bg");
    expect(plain!.className).toContain("bg-step");
    expect(head!.textContent).toBe("Nodes3");
    expect(head!.className).toContain("text-base");
    expect(head!.className).not.toContain("uppercase");
    expect(caps!.className).toContain("uppercase");
    expect(pill!.className).toContain("pill");
    expect(pill!.className).toContain("tone-ok");
  });
});
