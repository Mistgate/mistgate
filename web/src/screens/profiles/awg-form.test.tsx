import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { awgHidden } from "./awg-panel";
import { allFields, getAt, makeSecret, MASK, parseSchema, parseSettings, withGeneratedSecrets } from "./schema";
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
  act(() => root!.render(<ToastProvider>{ui}</ToastProvider>));
  return host;
}

// the shape of the awg plugin's schema (internal/panel/protocols/awg/schema.go), trimmed
const schema = JSON.stringify({
  type: "object",
  properties: {
    version: { type: "string", enum: ["3.1", "2.0"], title: "Protocol version", "x-group": "basics", "x-order": 10, "x-critical": true },
    port: { type: "integer", title: "UDP port", "x-group": "basics", "x-order": 20, "x-critical": true },
    subnet4: { type: "string", title: "Client network (IPv4)", "x-group": "basics", "x-order": 50, "x-critical": true },
    obfuscation: {
      type: "object",
      title: "Obfuscation",
      "x-group": "obfuscation",
      "x-order": 70,
      properties: {
        preset: { type: "string", enum: ["quic", "dns"], title: "Mimicry", "x-order": 10 },
        domain: { type: "string", title: "Domain in the packets", "x-order": 15 },
        jc: { type: "integer", title: "Junk packets (Jc)", "x-order": 20 },
        per_device_signature: { type: "boolean", title: "A signature of its own for every device", "x-order": 135 },
        header_protection_key: { type: "string", title: "Header protection key", "x-secret": true, "x-widget": "generate", "x-group": "advanced", "x-order": 180, "x-critical": true },
      },
    },
  },
});
const settings = parseSettings('{"version":"3.1","port":23456,"subnet4":"10.66.4.0/22","obfuscation":{"preset":"quic","jc":6,"header_protection_key":"••••"}}');
const labels = (el: HTMLElement) => [...el.querySelectorAll("[aria-label]")].map((e) => e.getAttribute("aria-label"));

describe("SchemaForm for a protocol with its own cards", () => {
  it("leaves out what the protocol draws itself", () => {
    const el = mount(<SchemaForm protocol="awg" groups={parseSchema(schema)} settings={settings} base={settings} problems={[]} onChange={() => {}} hidden={awgHidden} />);
    expect(labels(el)).toContain("UDP port");
    expect(labels(el)).not.toContain("Protocol version");
    expect(labels(el)).not.toContain("Mimicry");
    expect(labels(el)).not.toContain("Domain in the packets");
    expect(labels(el)).not.toContain("A signature of its own for every device");
    expect(labels(el)).toContain("Junk packets (Jc)");
  });
  it("shows a locked field with its value and no way to type", () => {
    const el = mount(<SchemaForm protocol="awg" groups={parseSchema(schema)} settings={settings} base={settings} problems={[]} onChange={() => {}} readOnly={new Set(["subnet4"])} />);
    const input = el.querySelector<HTMLInputElement>('input[aria-label="Client network (IPv4)"]')!;
    expect(input.value).toBe("10.66.4.0/22");
    expect(input.disabled).toBe(true);
    expect(el.querySelector<HTMLInputElement>('input[aria-label="UDP port"]')!.disabled).toBe(false);
  });
  it("puts what the protocol adds under the field's hint", () => {
    const el = mount(<SchemaForm protocol="awg" groups={parseSchema(schema)} settings={settings} base={settings} problems={[]} onChange={() => {}} extra={(f) => (f.id === "port" ? <em data-testid="x">random</em> : null)} />);
    expect(el.querySelectorAll("em")).toHaveLength(1);
  });
});

describe("secrets of the awg schema", () => {
  it("the header protection key is made as 32 bytes of base64, a password as a plain secret", () => {
    const fields = allFields(parseSchema(schema));
    const out = withGeneratedSecrets(settings, fields, makeSecret);
    const key = String(getAt(out, ["obfuscation", "header_protection_key"]));
    expect(key).not.toBe(MASK);
    expect(atob(key)).toHaveLength(32);
  });
});
