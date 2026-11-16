import { readFileSync } from "node:fs";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { en } from "@/i18n/en";
import { dictionaries, fill } from "@/i18n";
import { HeadersTab } from "./headers";
import { headerLimit, headerNameProblem, headerSize, headerToSave, headerValueProblem, reservedHeaderName, type ResponseHeader, type Settings } from "./model";

// Subscriptions -> Response headers: the form says what the panel's save says (one table for both), keeps a half-typed header from
// being saved, saves the list as the panel takes it, and asks the panel for the preview.

type Table = { reserved: string[]; cases: { name: string; value: string; base64?: boolean; ok: boolean }[] };
const table = JSON.parse(readFileSync(`${process.cwd()}/../internal/panel/subsettings/testdata/header_cases.json`, "utf8")) as Table;

const getSubscriptionSettings = vi.fn();
const updateSubscriptionSettings = vi.fn();
const previewResponseHeaders = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  subscriptions: {
    getSubscriptionSettings: (...a: unknown[]) => getSubscriptionSettings(...a),
    updateSubscriptionSettings: (...a: unknown[]) => updateSubscriptionSettings(...a),
    previewResponseHeaders: (...a: unknown[]) => previewResponseHeaders(...a),
  },
  groups: { listGroups: async () => ({ groups: [{ id: "grp_mobile", name: "MOBILE" }, { id: "grp_home", name: "HOME" }] }) },
  users: { listUsers: async () => ({ users: [{ id: "usr_alice", name: "alice" }] }) },
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [getSubscriptionSettings, updateSubscriptionSettings, previewResponseHeaders]) m.mockReset();
});

const hdr = (name: string, value: string, o: Partial<ResponseHeader> = {}): ResponseHeader => ({ name, value, base64: false, formats: [], onlyGroups: false, groupIds: [], ...o });
const settings = (list: ResponseHeader[]) => ({ title: "", rules: [], apps: [], responseHeaders: list }) as unknown as Settings;

async function mount(list: ResponseHeader[]) {
  getSubscriptionSettings.mockResolvedValue({ settings: settings(list), effectiveTitle: "", reservedHeaderNames: table.reserved });
  updateSubscriptionSettings.mockImplementation(async (r: { settings: Settings }) => ({ settings: r.settings }));
  previewResponseHeaders.mockResolvedValue({ format: SubFormat.MIHOMO_YAML, headers: [{ name: "flclashx-servicename", value: "My VPN", base64: false }], notServed: "", groupName: "MOBILE" });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <HeadersTab data={{ settings: settings(list), reservedHeaderNames: table.reserved }} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const click = (b: Element) => act(async () => void b.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const buttonWith = (text: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === text)!;
const typeInto = async (el: HTMLInputElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const input = (label: string) => document.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!;
const savedList = () => (updateSubscriptionSettings.mock.calls.at(-1)?.[0] as { settings: Settings } | undefined)?.settings.responseHeaders;

describe("the form says what the panel says", () => {
  for (const c of table.cases) {
    it(`${JSON.stringify(c.name)} = ${JSON.stringify(c.value)}${c.base64 ? " (base64)" : ""}: ${c.ok ? "accepted" : "refused"}`, () => {
      const problem = headerNameProblem(c.name, table.reserved) ?? headerValueProblem(c.value, !!c.base64);
      expect(problem === null, JSON.stringify(problem)).toBe(c.ok);
    });
  }
  it("a reserved name is matched without regard to case, and a prefix covers its family", () => {
    expect(reservedHeaderName("SET-COOKIE", table.reserved)).toBe(true);
    expect(reservedHeaderName("x-hwid-new-one", table.reserved)).toBe(true);
    expect(reservedHeaderName("flclashx-view", table.reserved)).toBe(false);
  });
});

describe("what goes to the panel", () => {
  it("trims, folds a list of every format into all and drops groups that are gone", () => {
    const all = [SubFormat.BASE64_URIS, SubFormat.MIHOMO_YAML, SubFormat.XRAY_JSON, SubFormat.SINGBOX_JSON];
    const h = hdr(" x-a ", " v ", { formats: all, onlyGroups: true, groupIds: ["grp_a", "grp_gone"] });
    expect(headerToSave(h, ["grp_a"])).toEqual({ ...h, name: "x-a", value: "v", formats: [], groupIds: ["grp_a"] });
    expect(headerToSave(h, null).groupIds).toEqual(["grp_a", "grp_gone"]); // groups not known yet: nothing dropped
    expect(headerToSave({ ...h, onlyGroups: false }, ["grp_a"]).groupIds).toEqual([]);
  });
  it("counts the bytes as the panel does", () => {
    expect(headerSize(hdr("x-a", "vv"))).toBe(3 + 2 + 4);
    expect(headerSize(hdr("x-a", "vvv", { base64: true }))).toBe(3 + 7 + 4 + 4);
    expect(headerLimit.total).toBe(4096);
  });
});

describe("Response headers tab", () => {
  it("says there are none and adds the first", async () => {
    await mount([]);
    expect(document.body.textContent).toContain(en["subs.hdr.noneT"]);
    await click(buttonWith(en["subs.hdr.addFirst"]));
    expect(input("Header 1: Name")).not.toBeNull();
    expect(document.body.textContent).toContain(en["subs.dirty"]);
  });

  it("points out a name the panel keeps and does not offer to save it", async () => {
    await mount([hdr("flclashx-servicename", "My VPN")]);
    await typeInto(input("Header 1: Name"), "Content-Type");
    expect(document.body.textContent).toContain(fill(en["subs.hdr.err.reserved"], { name: "Content-Type" }));
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(true);
    await click(buttonWith(en["subs.save"]));
    expect(updateSubscriptionSettings).not.toHaveBeenCalled();
  });

  it("asks for base64 when the text is not ASCII, and for a group when the list is empty", async () => {
    await mount([hdr("flclashx-servicename", "My VPN")]);
    await typeInto(input("Header 1: Value"), "Мой VPN");
    expect(document.body.textContent).toContain(en["subs.hdr.err.ascii"]);
    await click(document.querySelector("[role=switch]")!);
    expect(document.body.textContent).not.toContain(en["subs.hdr.err.ascii"]);
    await click([...document.querySelectorAll("[role=radio]")].find((r) => r.textContent === en["subs.hdr.groupsSome"])!);
    expect(document.body.textContent).toContain(en["subs.hdr.groupsPick"]);
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(true);
    await click(buttonWith("MOBILE"));
    expect(document.body.textContent).not.toContain(en["subs.hdr.groupsPick"]);
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(false);
  });

  it("saves the list as typed, trimmed, with its formats and groups", async () => {
    await mount([hdr("flclashx-servicename", "My VPN")]);
    await click(buttonWith(en["subs.hdr.add"]));
    await typeInto(input("Header 2: Name"), "  flclashx-special ");
    await typeInto(input("Header 2: Value"), "providermode");
    const second = input("Header 2: Name").closest("div.rounded-xl")!;
    const chip = (text: string) => [...second.querySelectorAll("button")].find((b) => b.textContent?.trim() === text)!;
    await click(chip(en["subs.fmt.mihomo"]));
    await click([...second.querySelectorAll("[role=radio]")].find((r) => r.textContent === en["subs.hdr.groupsSome"])!);
    await click(chip("HOME"));
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(savedList()).toEqual([hdr("flclashx-servicename", "My VPN"), hdr("flclashx-special", "providermode", { formats: [SubFormat.MIHOMO_YAML], onlyGroups: true, groupIds: ["grp_home"] })]);
  });

  it("moves a header up and removes one", async () => {
    await mount([hdr("x-first", "1"), hdr("x-second", "2")]);
    await click(document.querySelector("button[aria-label='Move header 2 up']")!);
    expect(input("Header 1: Name").value).toBe("x-second");
    await click(document.querySelector("button[aria-label='Remove header 1']")!);
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(savedList()).toEqual([hdr("x-first", "1")]);
  });

  it("asks the panel what the answer of the person carries, with the form as it stands, and shows it", async () => {
    await mount([hdr("flclashx-servicename", "My VPN")]);
    await act(async () => void (await new Promise((r) => setTimeout(r, 400))));
    await settle();
    const call = previewResponseHeaders.mock.calls.at(-1)?.[0] as { userId: string; userAgent: string; headers: ResponseHeader[] };
    expect(call.userId).toBe("usr_alice");
    expect(call.userAgent).toMatch(/mihomo/i);
    expect(call.headers).toEqual([hdr("flclashx-servicename", "My VPN")]);
    expect(document.body.textContent).toContain("flclashx-servicenameMy VPN"); // the name above its value
    expect(document.body.textContent).toContain(fill(en["subs.hdr.pv.group"], { group: "MOBILE" }));
  });

  it("does not preview a list the panel would refuse", async () => {
    await mount([hdr("Content-Type", "text/plain")]);
    await act(async () => void (await new Promise((r) => setTimeout(r, 400))));
    expect(previewResponseHeaders).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain(en["subs.hdr.pv.fix"]);
  });
});

describe("the texts in every language", () => {
  it("carry the same placeholders as English", () => {
    const keys = Object.keys(en).filter((k) => k.startsWith("subs.hdr."));
    expect(keys.length).toBeGreaterThan(40);
    for (const lang of ["ru", "zh", "fa", "tr"] as const) {
      for (const k of keys) {
        const want = [...(en as Record<string, string>)[k]!.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();
        const got = [...dictionaries[lang][k as keyof typeof en].matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();
        expect(got, `${lang} ${k}`).toEqual(want);
      }
    }
  });
});
