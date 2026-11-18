import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { CopyButton } from "@/components/copy-button";
import { setEffectsEnabled } from "@/lib/effects";
import { Button, type ButtonVariant } from "./button";
import { Modal } from "./modal";
import { Stepper } from "./stepper";
import { StatusPill } from "./status";
import { Switch } from "./switch";
import { ToastProvider } from "./toast";

// The base components against the redesign boards (pn.css): the sizes the boards measure, so a change that drifts from them fails here.

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  setEffectsEnabled(false);
});

function mount(ui: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(ui));
  return host;
}

describe("Button", () => {
  it("draws a 1px frame in every variant (see-through where it is not drawn), as the board's .btn.pri is as wide as .btn", () => {
    const variants: ButtonVariant[] = ["primary", "secondary", "outline", "ghost", "danger", "ghostDanger"];
    const el = mount(
      <>
        {variants.map((v) => (
          <Button key={v} variant={v}>
            {v}
          </Button>
        ))}
      </>,
    );
    for (const b of el.querySelectorAll("button")) expect(b.className.split(" ")).toContain("border");
    expect(el.querySelector("button")!.className).toContain("border-transparent");
  });
});

describe("Modal", () => {
  const open = (props: { fx: boolean }) => {
    setEffectsEnabled(props.fx);
    mount(
      <Modal open onOpenChange={() => {}} title="New group" closeLabel="Close" footer={<Button>Go</Button>}>
        body
      </Modal>,
    );
    return document.querySelector<HTMLElement>("[role=dialog]")!;
  };

  it("has the board's header: a 20px title on a 20px line, the x 40px wide, the row centred", () => {
    const dlg = open({ fx: false });
    const title = dlg.querySelector("h2")!;
    expect(title.className).toContain("text-xl");
    expect(title.className).toContain("leading-5");
    expect(dlg.querySelector("button[aria-label=Close]")!.className).toContain("size-10");
    expect(title.parentElement!.parentElement!.className).toContain("items-center");
  });

  it("grows from 90% on the spring with the effects on, and only fades with them off", () => {
    expect(open({ fx: true }).className).toContain("md:data-starting-style:scale-90");
    act(() => root?.unmount());
    host?.remove();
    expect(open({ fx: false }).className).not.toContain("scale-90");
  });
});

describe("Switch", () => {
  it("is the board's 36 x 20 track with a 16px thumb that travels 16 (the stretch while pressed is index.css)", () => {
    const el = mount(<Switch aria-label="on" />);
    const track = el.querySelector("[role=switch]")!;
    expect(track.className).toContain("w-9");
    expect(track.className).toContain("h-5");
    expect(track.firstElementChild!.className).toContain("size-4");
    expect(track.firstElementChild!.className).toContain("data-checked:translate-x-4");
  });
});

describe("Stepper", () => {
  it("is the board's 36px capsule: two 32px buttons round a value that is 56px at least", () => {
    const el = mount(
      <Stepper decrementLabel="less" incrementLabel="more" onDecrement={() => {}} onIncrement={() => {}}>
        3
      </Stepper>,
    );
    const group = el.querySelector("[role=group]")!;
    expect(group.className).toContain("h-9");
    expect(group.className).toContain("bg-canvas");
    for (const b of group.querySelectorAll("button")) expect(b.className).toContain("size-8");
    expect(group.querySelector("output")!.className).toContain("min-w-14");
  });
});

describe("StatusPill", () => {
  it("is a dot and the word in the same colour (the board's .pill); a busy one blinks its dot", () => {
    const el = mount(
      <>
        <StatusPill kind="ok" />
        <StatusPill kind="busy" />
      </>,
    );
    const [ok, busy] = [...el.children] as HTMLElement[];
    expect(ok!.textContent).toBe("Healthy");
    expect(ok!.className).toContain("h-8");
    expect(ok!.className).toContain("px-3");
    expect(ok!.querySelector("svg")).toBeNull();
    expect(ok!.firstElementChild!.className).toContain("size-2");
    expect(ok!.firstElementChild!.className).not.toContain("animate");
    expect(busy!.firstElementChild!.className).toContain("mg-ui-blink");
  });
});

describe("CopyButton", () => {
  it("holds the width of both words and shows one of them as text", async () => {
    vi.useFakeTimers();
    Object.defineProperty(navigator, "clipboard", { value: { writeText: () => Promise.resolve() }, configurable: true });
    const el = mount(
      <ToastProvider>
        <CopyButton value="x" label="Copy it" variant="secondary" />
      </ToastProvider>,
    );
    const b = el.querySelector("button")!;
    expect(b.textContent).toBe("Copy it");
    // the other word is the ::before of its own cell of the grid, so the button is as wide as the longer one
    expect([...b.querySelectorAll("[data-t]")].map((s) => s.getAttribute("data-t"))).toEqual(["Copy it", "Copied"]);
    expect(b.hasAttribute("data-copied")).toBe(false);
    await act(async () => {
      b.click();
    });
    expect(b.textContent).toBe("Copied");
    expect(b.hasAttribute("data-copied")).toBe(true);
    await act(async () => {
      vi.advanceTimersByTime(2100);
    });
    expect(b.textContent).toBe("Copy it");
    vi.useRealTimers();
  });
});
