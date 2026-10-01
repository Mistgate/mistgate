import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { hy2, other } from "./schema.fixtures";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/en";
import { MASK, parseSchema, parseSettings } from "./schema";
import { SchemaForm } from "./schema-form";

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

const labels = (el: HTMLElement) => [...el.querySelectorAll("[aria-label]")].map((e) => e.getAttribute("aria-label"));

describe("SchemaForm", () => {
  it("renders a protocol nobody wrote UI for, with the schema's own text", () => {
    const settings = parseSettings('{"mtu":1280,"dns":"1.1.1.1","preset":"b","junk":{"count":4,"min":40},"key":"••••","ratio":1.5}');
    const el = mount(<SchemaForm protocol="other" groups={parseSchema(other)} settings={settings} base={settings} problems={[]} onChange={() => {}} />);
    // basics and obfuscation are open, advanced is folded
    expect(labels(el)).toEqual(expect.arrayContaining(["Private key", "Jc", "Jmin"]));
    expect(labels(el)).not.toContain("MTU");
    const headers = [...el.querySelectorAll("button[aria-expanded]")].map((b) => b.getAttribute("aria-expanded"));
    expect(headers).toEqual(["true", "true", "false"]);
    const advanced = el.querySelectorAll("button[aria-expanded]")[2] as HTMLButtonElement;
    act(() => advanced.click());
    expect(labels(el)).toEqual(expect.arrayContaining(["DNS", "MTU", "Preset", "Ratio"]));
  });

  it("shows a change and its count, and reports edits by path", () => {
    const base = parseSettings('{"port":443}');
    const onChange = vi.fn();
    const schema = parseSchema(hy2);
    const el = mount(<SchemaForm protocol="hysteria2" groups={schema} settings={{ port: 8443 }} base={base} problems={[]} onChange={onChange} />);
    expect(el.textContent).toContain("1 changed");
    const port = el.querySelector('input[aria-label="Port"]') as HTMLInputElement;
    expect(port.value).toBe("8443");
    // the value setter React tracks, then a real input event
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    act(() => {
      set.call(port, "9443");
      port.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(onChange).toHaveBeenCalledWith({ port: 9443 });
  });

  describe("secret field", () => {
    const form = (value: string, onChange = vi.fn()) => {
      const settings = { obfs: { type: "salamander", password: value } };
      const el = mount(
        <ToastProvider>
          <SchemaForm protocol="hysteria2" groups={parseSchema(hy2)} settings={settings} base={settings} problems={[]} onChange={onChange} />
        </ToastProvider>,
      );
      return { el, onChange, input: el.querySelector('input[aria-label="Obfuscation password"]') as HTMLInputElement };
    };
    const button = (el: HTMLElement, name: string) => el.querySelector(`button[aria-label="${name}"]`) as HTMLButtonElement;

    it("shows a saved secret as dots with nothing to reveal or copy, and can replace it", () => {
      const { el, input, onChange } = form(MASK);
      expect(input.type).toBe("password");
      expect(input.value).toBe("");
      expect(input.placeholder).toBe("••••••••••••");
      expect(button(el, "Show").disabled).toBe(true);
      expect(button(el, "Copy").disabled).toBe(true);
      act(() => button(el, "Generate").click());
      expect(onChange).toHaveBeenCalledTimes(1);
      const next = onChange.mock.calls[0]![0] as { obfs: { password: string } };
      expect(next.obfs.password).toMatch(/^[A-Za-z0-9_-]{32}$/);
    });

    it("hides a local value behind dots until the eye is clicked", () => {
      const { el, input } = form("s3cret-local-value");
      expect(input.type).toBe("password");
      expect(input.value).toBe("s3cret-local-value");
      act(() => button(el, "Show").click());
      expect(input.type).toBe("text");
      expect(button(el, "Hide").getAttribute("aria-pressed")).toBe("true");
    });

    it("keeps the saved secret when the box is emptied (an empty value would mean generate)", () => {
      const { input, onChange } = form("typed");
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      act(() => {
        set.call(input, "");
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
      expect(onChange).toHaveBeenCalledWith({ obfs: { type: "salamander", password: MASK } });
    });
  });

  it("puts the server's message on the field and opens a folded group that has one", () => {
    const settings = parseSettings('{"port":443,"up_mbps":0}');
    const el = mount(
      <SchemaForm protocol="hysteria2" groups={parseSchema(hy2)} settings={settings} base={settings} problems={[{ pointer: "/up_mbps", message: "must be 0-100000" }]} onChange={() => {}} />,
    );
    expect(el.textContent).toContain("must be 0-100000");
    expect(el.textContent).toContain("1 error");
  });

  it("words a field error by its pointer and code when the dictionary has it, else shows the server's text", () => {
    const settings = parseSettings('{"port":25000,"hop":{"from":20000,"to":30000}}');
    const problems = [
      { pointer: "/hop/from", code: "invalid", message: "the port must be outside the hop range" },
      { pointer: "/hop/to", code: "brand_new", message: "something new" },
    ];
    const el = mount(<SchemaForm protocol="hysteria2" groups={parseSchema(hy2)} settings={settings} base={settings} problems={problems} onChange={() => {}} />);
    expect(el.textContent).toContain(en["field.hop.from.invalid"]);
    expect(el.textContent).not.toContain("the port must be outside the hop range");
    expect(el.textContent).toContain("something new");
  });
});
