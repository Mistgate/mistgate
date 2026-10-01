import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { GenerateMode } from "@/gen/mistgate/admin/v1/awg_pb";
import { en, ru } from "@/i18n/awg";
import { AwgPanel, handTyped, warnText, type AwgAdvice } from "./awg-panel";
import type { Settings } from "./schema";

const listMimicryPresets = vi.fn();
const generateObfuscation = vi.fn();
vi.mock("@/lib/api", () => ({
  awg: { listMimicryPresets: (...a: unknown[]) => listMimicryPresets(...a), generateObfuscation: (...a: unknown[]) => generateObfuscation(...a) },
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const preset = (id: string, over: Record<string, unknown> = {}) => ({ id, name: id, versions: ["3.1", "2.0"], usesDomain: false, naturalPorts: [], variesPerDevice: true, ...over });
beforeEach(() => {
  listMimicryPresets.mockResolvedValue({
    presets: [preset("quic", { usesDomain: true, naturalPorts: [443] }), preset("dns", { usesDomain: true, naturalPorts: [53] }), preset("stun", { variesPerDevice: false }), preset("custom", { variesPerDevice: false })],
    domains: ["example.com", "example.org"],
  });
  generateObfuscation.mockImplementation(async (req: { mode: GenerateMode; preset: string; domain: string }) => ({
    obfuscationJson: JSON.stringify(
      req.mode === GenerateMode.SIGNATURE
        ? { preset: req.preset, domain: req.domain || "picked.example.net", i1: `<b 0x${req.preset}>`, i2: "", i3: "", i4: "", i5: "" }
        : { preset: req.preset, domain: req.domain, jc: 9, h1: "1000-2000", s1: 40, i1: "<b 0xfull>", signature_seed: "new" },
    ),
  }));
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  listMimicryPresets.mockReset();
  generateObfuscation.mockReset();
});

const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const start: Settings = { version: "3.1", mtu: 1280, port: 23456, obfuscation: { preset: "quic", domain: "", per_device_signature: true, signature_seed: "seed", jc: 6, s1: 24, h1: "100-200", header_protection_key: "k", i1: "<b 0xold>" } };

async function mount(settings: Settings = start, advice?: AwgAdvice) {
  const onChange = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AwgPanel settings={settings} onChange={onChange} advice={advice} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
  return { el: host, onChange };
}
const radio = (el: HTMLElement, text: string) => [...el.querySelectorAll<HTMLElement>('[role="radio"]')].find((r) => r.textContent?.startsWith(text))!;
const button = (el: HTMLElement, text: string) => [...el.querySelectorAll("button")].find((b) => b.textContent === text)!;
const click = async (e: HTMLElement) => {
  await act(async () => e.click());
  await settle();
};

describe("AwgPanel mimicry cards", () => {
  it("picking a look fetches the signature packets only and leaves the critical fields alone", async () => {
    const { el, onChange } = await mount();
    await click(radio(el, "DNS"));
    expect(generateObfuscation).toHaveBeenCalledOnce();
    expect(generateObfuscation.mock.calls[0]![0]).toMatchObject({ version: "3.1", preset: "dns", mode: GenerateMode.SIGNATURE, mtu: 1280 });
    const next = onChange.mock.calls[0]![0] as Settings;
    expect(next.obfuscation).toEqual({ ...(start.obfuscation as object), preset: "dns", domain: "picked.example.net", i1: "<b 0xdns>", i2: "", i3: "", i4: "", i5: "" });
    expect(next).toMatchObject({ version: "3.1", mtu: 1280, port: 23456 });
  });
  it("the QUIC cards say the config will not fit a QR code, in both languages", async () => {
    for (const id of ["quic", "curl_quic"]) {
      expect(en[`awg.preset.${id}.hint` as keyof typeof en]).toMatch(/QR/);
      expect(ru[`awg.preset.${id}.hint` as keyof typeof ru]).toMatch(/QR/);
    }
    const { el } = await mount();
    expect(radio(el, "QUIC").textContent).toContain(en["awg.preset.quic.hint"]);
  });
  it("the custom look has nothing to generate: no call, the packets stay", async () => {
    const { el, onChange } = await mount();
    await click(radio(el, "Custom"));
    expect(generateObfuscation).not.toHaveBeenCalled();
    expect((onChange.mock.calls[0]![0] as Settings).obfuscation).toMatchObject({ preset: "custom", i1: "<b 0xold>", jc: 6 });
  });
  it("the big button replaces the block (mode FULL), keeps the per-device choice and warns about the reissue", async () => {
    const { el, onChange } = await mount({ ...start, obfuscation: { ...(start.obfuscation as object), per_device_signature: false } });
    expect(el.textContent).toContain(en["awg.generateWarn"]);
    await click(button(el, en["awg.generate"]));
    expect(generateObfuscation.mock.calls[0]![0]).toMatchObject({ mode: GenerateMode.FULL, preset: "quic" });
    expect((onChange.mock.calls[0]![0] as Settings).obfuscation).toMatchObject({ jc: 9, h1: "1000-2000", per_device_signature: false, preset: "quic" });
  });
  it("the big button leaves the packets of the custom look alone: the generator has none to give", async () => {
    generateObfuscation.mockImplementationOnce(async () => ({ obfuscationJson: JSON.stringify({ preset: "custom", jc: 9, h1: "1000-2000", i1: "", i2: "", i3: "", i4: "", i5: "" }) }));
    const { el, onChange } = await mount({ ...start, obfuscation: { ...(start.obfuscation as object), preset: "custom", i1: "<b 0xmine>", i2: "<r 20>" } });
    await click(button(el, en["awg.generate"]));
    expect((onChange.mock.calls[0]![0] as Settings).obfuscation).toMatchObject({ preset: "custom", jc: 9, h1: "1000-2000", i1: "<b 0xmine>", i2: "<r 20>" });
  });
});

describe("AwgPanel domain and per-device signature", () => {
  it("shows the domain for a look that carries one and re-fetches the packets with it", async () => {
    const { el, onChange } = await mount();
    const input = el.querySelector<HTMLInputElement>('input[list]')!;
    expect(input).toBeTruthy();
    expect([...el.querySelectorAll("datalist option")].map((o) => o.getAttribute("value"))).toEqual(["example.com", "example.org"]);
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "example.org");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await settle();
    expect(generateObfuscation.mock.calls[0]![0]).toMatchObject({ mode: GenerateMode.SIGNATURE, preset: "quic", domain: "example.org" });
    expect((onChange.mock.calls[0]![0] as Settings).obfuscation).toMatchObject({ domain: "example.org" });
  });
  it("shows the built-in list on demand and Random draws a concrete name from it", async () => {
    const { el, onChange } = await mount();
    expect(el.textContent).not.toContain("example.com example.org");
    await click(button(el, en["awg.domain.list"].replace("{n}", "2")));
    await click(button(el, "example.com"));
    expect((onChange.mock.calls.at(-1)![0] as Settings).obfuscation).toMatchObject({ domain: "example.com" });
    await click(button(el, en["awg.domain.random"]));
    expect(generateObfuscation.mock.calls.at(-1)![0]).toMatchObject({ mode: GenerateMode.SIGNATURE, domain: expect.stringMatching(/^example\.(com|org)$/) });
  });
  it("hides the domain for a look without one", async () => {
    const { el } = await mount({ ...start, obfuscation: { ...(start.obfuscation as object), preset: "stun" } });
    expect(el.querySelector("input[list]")).toBeNull();
  });
  it("the switch is off and explained where every device would get the same bytes", async () => {
    const { el } = await mount({ ...start, obfuscation: { ...(start.obfuscation as object), preset: "stun" } });
    const sw = el.querySelector<HTMLElement>(`[role="switch"][aria-label="${en["awg.perDevice.title"]}"]`)!;
    expect(sw.hasAttribute("data-disabled")).toBe(true);
    expect(el.textContent).toContain(en["awg.perDevice.same"]);
  });
  it("turning the switch on sends nothing to the server and writes the flag", async () => {
    const { el, onChange } = await mount({ ...start, obfuscation: { ...(start.obfuscation as object), per_device_signature: false } });
    await click(el.querySelector<HTMLElement>(`[role="switch"][aria-label="${en["awg.perDevice.title"]}"]`)!);
    expect(generateObfuscation).not.toHaveBeenCalled();
    expect((onChange.mock.calls[0]![0] as Settings).obfuscation).toMatchObject({ per_device_signature: true });
  });
});

const fe = (code: string, params: Record<string, string> = {}) => ({ pointer: "/x", code, message: `en ${code}`, params }) as never;
const score = { value: 82, base: 95, tier: "header_protection", items: [{ code: "cps_frozen", pointer: "/obfuscation/i1", delta: -5, params: {} }, { code: "preset_port", pointer: "/port", delta: -5, params: { preset: "quic", ports: "443" } }, { code: "per_device_signature", pointer: "/obfuscation/per_device_signature", delta: 3, params: {} }] };

describe("AwgPanel score and remarks", () => {
  it("shows the score chip and, on a click, the starting point and every reason with its sign", async () => {
    const { el } = await mount(start, { warnings: [], score: score as never });
    const chip = button(el, "Disguise 82/100");
    expect(chip).toBeTruthy();
    expect(el.textContent).not.toContain(en["awg.score.cps_frozen"]);
    await click(chip);
    expect(el.textContent).toContain("Starting point 95: 3.1 with a header protection key");
    expect(el.textContent).toContain(`−5${en["awg.score.cps_frozen"]}`);
    expect(el.textContent).toContain(`+3${en["awg.score.per_device_signature"]}`);
  });
  it("no chip without a preview (errors in the form)", async () => {
    const { el } = await mount(start, undefined);
    expect(el.textContent).not.toContain("/100");
  });
  it("draws the server's remarks by code, with numbers; the port remark names the port and is not an alarm", async () => {
    const { el } = await mount(start, { warnings: [fe("mtu_headroom", { s4: "30", suggested_mtu: "1390" }), fe("preset_port", { preset: "quic", ports: "443" }), fe("h_lt5")], score: undefined });
    expect(el.textContent).toContain("S4 = 30 does not fit under 1500 at this MTU: data packets fragment. MTU 1390 fits.");
    expect(el.textContent).toContain("QUIC traffic is normally on port 443.");
    expect(el.textContent).toContain(en["awg.warn.h_lt5"]);
    // two amber notices, the port line is plain text
    expect(el.querySelectorAll('[role="status"]')).toHaveLength(2);
  });
  it("every code of the server has a text in both languages", async () => {
    const warn = ["jc_high", "jmax_ge_mtu", "no_flood_protection", "trailers_unequal_s", "mtu_above_1280", "mtu_headroom", "h_default_v20", "h_small_range", "h_lt5", "preset_port", "keepalive_over_nat"];
    const items = ["h_default_v20", "h_lt5", "h_small_range", "jc_zero", "junk_narrow", "s_zero", "jc_high", "jmax_ge_mtu", "no_random_trailers", "trailers_unequal_s", "no_data_padding", "timers_default", "no_cps", "cps_frozen", "preset_port", "port_default", "keepalive_over_nat", "mtu_headroom", "per_device_signature"];
    const tiers = ["header_protection", "ranges", "cps", "headers", "wireguard"];
    const keys = [...warn.map((c) => `awg.warn.${c}`), ...items.map((c) => `awg.score.${c}`), ...tiers.map((c) => `awg.score.tier.${c}`)];
    for (const k of keys) {
      expect((en as Record<string, string>)[k], k).toBeTruthy();
      expect((ru as Record<string, string>)[k], k).toBeTruthy();
    }
    // an unknown code falls back to the server's English line
    expect(warnText(Object.assign((k: string) => k, { opt: () => undefined }) as never, fe("brand_new"))).toBe("en brand_new");
  });
});

describe("AwgPanel import", () => {
  const conf = "[Interface]\nPrivateKey = c2VjcmV0LWZvci10ZXN0LW9ubHk=\nAddress = 10.0.0.2/32\nMTU = 1380\nJc = 5\nJmin = 30\nJmax = 80\nS1 = 20\nS2 = 21\nS3 = 22\nS4 = 23\nH1 = 100-200\nH2 = 300-400\nH3 = 500-600\nH4 = 700-800\nStrange = 1\n";
  async function paste(el: HTMLElement) {
    await click(button(el, en["awg.import.btn"]));
    const ta = el.querySelector<HTMLTextAreaElement>("textarea")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(ta, conf);
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(button(el, en["awg.import.do"]));
    return ta;
  }
  it("fills version, MTU and the block from a 2.0 file, reports what it did, and forgets the text", async () => {
    const { el, onChange } = await mount();
    const ta = await paste(el);
    // another version than the form's: a valid block of it first, then the file over it
    expect(generateObfuscation.mock.calls[0]![0]).toMatchObject({ version: "2.0", mode: GenerateMode.FULL, mtu: 1380 });
    const next = onChange.mock.calls[0]![0] as Settings;
    expect(next).toMatchObject({ version: "2.0", mtu: 1380, port: 23456 });
    expect(next.obfuscation).toMatchObject({ jc: 5, jmin: 30, s3: 22, h4: "700-800", signature_seed: "new" });
    expect(el.textContent).toContain(en["awg.import.kind.2.0"]);
    expect(el.textContent).toContain("Filled: MTU, Jc, Jmin");
    expect(el.textContent).toContain("Unknown keys: Strange");
    expect(el.textContent).toContain("PrivateKey");
    expect(el.textContent).not.toContain("c2VjcmV0"); // the key material is not shown anywhere
    expect(ta.value).toBe("");
  });
  it("garbage changes nothing and says so", async () => {
    const { el, onChange } = await mount();
    await click(button(el, en["awg.import.btn"]));
    const ta = el.querySelector<HTMLTextAreaElement>("textarea")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(ta, "just some words");
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(button(el, en["awg.import.do"]));
    expect(onChange).not.toHaveBeenCalled();
    expect(generateObfuscation).not.toHaveBeenCalled();
    expect(el.textContent).toContain(en["awg.import.kind.none"]);
  });
});

describe("handTyped", () => {
  const ob = (o: Record<string, unknown>): Settings => ({ version: "3.1", obfuscation: { preset: "quic", i1: "<b 0xold>", i2: "", jc: 6, ...o } });
  it("typing into I1-I5 chooses the custom look", () => {
    expect(handTyped(ob({}), ob({ i1: "<b 0xmine>" })).obfuscation).toMatchObject({ preset: "custom", i1: "<b 0xmine>" });
    expect(handTyped(ob({}), ob({ i2: "<r 20>" })).obfuscation).toMatchObject({ preset: "custom" });
  });
  it("any other field, and the custom look itself, stay as they are", () => {
    const next = ob({ jc: 9 });
    expect(handTyped(ob({}), next)).toBe(next);
    const custom = ob({ preset: "custom", i1: "<b 0xmine>" });
    expect(handTyped(ob({ preset: "custom" }), custom)).toBe(custom);
  });
});