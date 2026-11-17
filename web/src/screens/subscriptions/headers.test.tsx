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
import { headerLimit, headerNameProblem, headerShadowed, headerSize, headerToSave, headerValueProblem, reservedHeaderName, type ResponseHeader, type Settings } from "./model";

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
  if (!previewResponseHeaders.getMockImplementation()) previewResponseHeaders.mockResolvedValue({ format: SubFormat.MIHOMO_YAML, headers: [{ name: "flclashx-servicename", value: "My VPN", base64: false }], notServed: "", groupName: "MOBILE" });
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

const heads = () => [...document.querySelectorAll<HTMLButtonElement>("button.hx-rh")];
const openRow = (n: number) => click(heads()[n - 1]!);
const nameBox = () => document.querySelector<HTMLInputElement>("input.hx-n")!;
const valueBox = () => document.querySelector<HTMLInputElement>("input.hx-v")!;
const text = () => document.body.textContent ?? "";
const chipOf = (group: string, label: string) => [...document.querySelectorAll(`[role=group][aria-label="${group}"] button`)].find((b) => b.textContent === label)!;
const disabled = (b: Element) => b.hasAttribute("disabled");

describe("the shadowed entry", () => {
  it("is one that an entry above of the same name goes with wherever it goes", () => {
    const rows = [hdr("X-Tier", "a"), hdr("x-tier", "b"), hdr("X-Other", "c")];
    expect(headerShadowed(rows, 0, ["g1"])).toBe(false);
    expect(headerShadowed(rows, 1, ["g1"])).toBe(true);
    expect(headerShadowed(rows, 2, ["g1"])).toBe(false);
    const split = [hdr("X-Tier", "a", { formats: [SubFormat.MIHOMO_YAML] }), hdr("X-Tier", "b", { formats: [SubFormat.XRAY_JSON] })];
    expect(headerShadowed(split, 1, ["g1"])).toBe(false); // no format both go with
    const groups = [hdr("X-Tier", "a", { onlyGroups: true, groupIds: ["g1"] }), hdr("X-Tier", "b", { onlyGroups: true, groupIds: ["g2"] })];
    expect(headerShadowed(groups, 1, ["g1", "g2"])).toBe(false);
    expect(headerShadowed([...groups, hdr("X-Tier", "c")], 2, ["g1", "g2"])).toBe(true);
    expect(headerShadowed([hdr("X-Tier", "a", { onlyGroups: true, groupIds: [] }), hdr("X-Tier", "b")], 1, ["g1"])).toBe(false); // the one above goes to nobody
  });
});

describe("Response headers tab", () => {
  it("says there are none, adds the first one open, and says what is missing in it", async () => {
    await mount([]);
    expect(text()).toContain(en["sx.hx.h.noneT"]);
    expect(text()).toContain(fill(en["sx.hx.h.sz"], { n: 0, max: 20, bytes: 0, total: 4096 }));
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(false); // nothing to save is a list that can be saved (the board)
    await click(buttonWith(en["sx.hx.h.add"]));
    expect(nameBox()).not.toBeNull();
    expect(nameBox().placeholder).toBe("X-Example");
    expect(text()).toContain(en["sx.hx.h.eNoName"]);
    expect(text()).toContain(en["sx.hx.h.eNoValue"]);
    expect(text()).toContain(en["sx.hx.h.ftBad"]);
    expect(disabled(buttonWith(en["subs.save"]))).toBe(true);
  });

  it("lists the headers folded, each with its value and where it goes, and opens one at a time", async () => {
    await mount([hdr("flclashx-servicename", "My VPN", { base64: true }), hdr("x-b", "2", { formats: [SubFormat.MIHOMO_YAML], onlyGroups: true, groupIds: ["grp_home"] })]);
    expect(heads()).toHaveLength(2);
    expect(heads()[0]!.textContent).toContain("flclashx-servicename");
    expect(heads()[0]!.textContent).toContain(`${en["sx.hx.h.allFormats"]} · ${en["sx.hx.h.allGroups"]} · base64`);
    expect(heads()[1]!.textContent).toContain(`${en["sx.hx.h.fm.mihomo"]} · HOME`);
    expect(nameBox()).toBeNull();
    await openRow(1);
    expect(nameBox().value).toBe("flclashx-servicename");
    expect(heads()[0]!.getAttribute("aria-expanded")).toBe("true");
    await openRow(2);
    expect(nameBox().value).toBe("x-b");
    expect(document.querySelectorAll("input.hx-n")).toHaveLength(1);
    await openRow(2);
    expect(nameBox()).toBeNull();
  });

  it("points out a name the panel keeps, in the folded row too, and does not offer to save it", async () => {
    await mount([hdr("flclashx-servicename", "My VPN")]);
    await openRow(1);
    await typeInto(nameBox(), "Content-Type");
    expect(text()).toContain(fill(en["sx.hx.h.eFamily"], { name: "Content-*" }));
    expect(disabled(buttonWith(en["subs.save"]))).toBe(true);
    await click(buttonWith(en["subs.save"]));
    expect(updateSubscriptionSettings).not.toHaveBeenCalled();
    await openRow(1); // fold it: the first mistake stands in the row
    expect(heads()[0]!.querySelector("em.bad")?.textContent).toBe(fill(en["sx.hx.h.eFamily"], { name: "Content-*" }));
    expect(document.querySelector(".hx-r.bad")).not.toBeNull();
  });

  it("tells a name that is no header name from one that is too long", async () => {
    await mount([hdr("x-a", "1")]);
    await openRow(1);
    await typeInto(nameBox(), "x a");
    expect(text()).toContain(en["sx.hx.h.eName"]);
    await typeInto(nameBox(), "x".repeat(65));
    expect(text()).toContain(fill(en["sx.hx.h.eNameLong"], { max: 64 }));
  });

  it("asks for base64 when the text is not ASCII, and for a group when the list is empty", async () => {
    await mount([hdr("flclashx-servicename", "My VPN")]);
    await openRow(1);
    await typeInto(valueBox(), "Мой VPN");
    expect(text()).toContain(en["sx.hx.h.eAscii"]);
    await click(document.querySelector("[role=switch]")!);
    expect(text()).not.toContain(en["sx.hx.h.eAscii"]);
    expect(text()).toContain(fill(en["sx.hx.h.b64On"], { n: 7 + 4 * Math.ceil(new TextEncoder().encode("Мой VPN").length / 3) }));
    await click(chipOf(en["sx.hx.h.groups"], "MOBILE"));
    expect(chipOf(en["sx.hx.h.groups"], en["sx.hx.h.gAll"]).getAttribute("aria-pressed")).toBe("false");
    await click(chipOf(en["sx.hx.h.groups"], "MOBILE")); // chosen groups, none left
    expect(text()).toContain(en["sx.hx.h.eNoGroup"]);
    expect(disabled(buttonWith(en["subs.save"]))).toBe(true);
    await click(chipOf(en["sx.hx.h.groups"], "HOME"));
    expect(text()).not.toContain(en["sx.hx.h.eNoGroup"]);
    expect(disabled(buttonWith(en["subs.save"]))).toBe(false);
    await click(chipOf(en["sx.hx.h.groups"], en["sx.hx.h.gAll"]));
    expect(chipOf(en["sx.hx.h.groups"], "HOME").getAttribute("aria-pressed")).toBe("false");
  });

  it("says a group that is gone is gone", async () => {
    await mount([hdr("x-a", "1", { onlyGroups: true, groupIds: ["grp_gone"] })]);
    expect(text()).toContain(en["sx.hx.h.eGone"]);
    expect(disabled(buttonWith(en["subs.save"]))).toBe(true);
  });

  it("says a header is covered by one above it of the same name, and still lets the list go", async () => {
    await mount([hdr("X-Tier", "a"), hdr("x-tier", "b")]);
    expect(heads()[1]!.querySelector("em")?.textContent).toBe(en["sx.hx.h.shadow"]);
    expect(heads()[1]!.querySelector("em.bad")).toBeNull();
    await openRow(2);
    expect(text()).toContain(en["sx.hx.h.shadowNote"]);
    expect(disabled(buttonWith(en["subs.save"]))).toBe(false);
  });

  it("saves the list as typed, trimmed, with its formats and groups", async () => {
    await mount([hdr("flclashx-servicename", "My VPN")]);
    await click(buttonWith(en["sx.hx.h.add"]));
    await typeInto(nameBox(), "  flclashx-special ");
    await typeInto(valueBox(), "providermode");
    await click(chipOf(en["sx.hx.h.formats"], en["sx.hx.h.fs.mihomo"]));
    await click(chipOf(en["sx.hx.h.groups"], "HOME"));
    expect(text()).toContain(en["subs.discard"]);
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(savedList()).toEqual([hdr("flclashx-servicename", "My VPN"), hdr("flclashx-special", "providermode", { formats: [SubFormat.MIHOMO_YAML], onlyGroups: true, groupIds: ["grp_home"] })]);
    expect(text()).toContain(en["subs.saved"]);
  });

  it("folds a choice of every format into all", async () => {
    await mount([hdr("x-a", "1")]);
    await openRow(1);
    for (const f of [en["sx.hx.h.fs.base64"], en["sx.hx.h.fs.mihomo"], en["sx.hx.h.fs.xray"], en["sx.hx.h.fs.singbox"]]) await click(chipOf(en["sx.hx.h.formats"], f));
    expect(chipOf(en["sx.hx.h.formats"], en["sx.hx.h.fAll"]).getAttribute("aria-pressed")).toBe("true");
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(savedList()).toEqual([hdr("x-a", "1")]);
  });

  it("moves a header up or down with the row staying open, and removes one", async () => {
    await mount([hdr("x-first", "1"), hdr("x-second", "2")]);
    await openRow(2);
    expect(disabled(buttonWith(en["sx.hx.h.down"]))).toBe(true);
    await click(buttonWith(en["sx.hx.h.up"]));
    expect(nameBox().value).toBe("x-second");
    expect(heads()[0]!.getAttribute("aria-expanded")).toBe("true");
    expect(disabled(buttonWith(en["sx.hx.h.up"]))).toBe(true);
    await click(buttonWith(en["sx.hx.h.del"]));
    expect(heads()).toHaveLength(1);
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(savedList()).toEqual([hdr("x-first", "1")]);
  });

  it("keeps to 20 headers and 4096 bytes, and says so", async () => {
    await mount(Array.from({ length: 20 }, (_, i) => hdr(`x-h-${i}`, "v")));
    expect(disabled(buttonWith(en["sx.hx.h.add"]))).toBe(true);
    expect(text()).toContain(fill(en["sx.hx.h.ftMax"], { max: 20 }));
  });
  it("says the whole list is too big", async () => {
    await mount(Array.from({ length: 5 }, (_, i) => hdr(`x-h-${i}`, "v".repeat(900))));
    expect(text()).toContain(fill(en["sx.hx.h.ftOver"], { max: 4096 }));
    expect(disabled(buttonWith(en["subs.save"]))).toBe(true);
  });

  it("discards what was changed", async () => {
    await mount([hdr("x-a", "1")]);
    await openRow(1);
    await typeInto(valueBox(), "2");
    await click(buttonWith(en["subs.discard"]));
    expect(heads()[0]!.textContent).toContain("x-a1");
    expect(nameBox()).toBeNull();
    expect(text()).not.toContain(en["subs.discard"]);
  });

  it("names the panel's families first and unfolds the whole list on 'and others'", async () => {
    await mount([]);
    const note = document.querySelector(".hx-rs")!;
    expect(note.textContent).toContain(en["sx.hx.h.res"]);
    expect(note.textContent).toContain("Content-*");
    expect(note.querySelectorAll(".mono")).toHaveLength(4);
    await click(buttonWith(en["sx.hx.h.resMore"]));
    expect(note.querySelectorAll(".mono").length).toBe(4 + table.reserved.length);
    await click(buttonWith(en["sx.hx.h.resLess"]));
    expect(note.querySelectorAll(".mono")).toHaveLength(4);
  });

  it("asks the panel what the answer of the person carries, with the form as it stands, and shows it", async () => {
    await mount([hdr("flclashx-servicename", "My VPN")]);
    await act(async () => void (await new Promise((r) => setTimeout(r, 400))));
    await settle();
    const call = previewResponseHeaders.mock.calls.at(-1)?.[0] as { userId: string; userAgent: string; headers: ResponseHeader[] };
    expect(call.userId).toBe("usr_alice");
    expect(call.userAgent).toMatch(/FlClashX/);
    expect(call.headers).toEqual([hdr("flclashx-servicename", "My VPN")]);
    const out = document.querySelector(".hx-out")!;
    expect(out.textContent).toBe("flclashx-servicenameMy VPN"); // the name above its value
    expect(text()).toContain(fill(en["sx.hx.h.pvSum"], { format: en["sx.hx.h.fm.mihomo"], group: "MOBILE" }));
    expect(text()).toContain(fill(en["sx.hx.h.pvN"].split("|")[0]!, { n: 1 }));
  });

  it("asks again for another person and another app", async () => {
    await mount([hdr("x-a", "1")]);
    await act(async () => void (await new Promise((r) => setTimeout(r, 400))));
    await settle();
    await click(buttonWith("sing-box"));
    await act(async () => void (await new Promise((r) => setTimeout(r, 400))));
    await settle();
    expect((previewResponseHeaders.mock.calls.at(-1)![0] as { userAgent: string }).userAgent).toMatch(/SFA|sing-box/);
    expect(buttonWith("sing-box").getAttribute("aria-pressed")).toBe("true");
    expect(buttonWith("alice").getAttribute("aria-pressed")).toBe("true");
  });

  it("says why nothing is carried when the answer is the page, and when nobody has headers", async () => {
    previewResponseHeaders.mockResolvedValue({ format: SubFormat.USER_PAGE, headers: [], notServed: "page", groupName: "MOBILE" });
    await mount([hdr("x-a", "1")]);
    await act(async () => void (await new Promise((r) => setTimeout(r, 400))));
    await settle();
    expect(text()).toContain(en["subs.hdr.pv.page"]);
    expect(document.querySelector(".hx-out")).toBeNull();
  });
  it("says none of the headers goes with this answer", async () => {
    previewResponseHeaders.mockResolvedValue({ format: SubFormat.BASE64_URIS, headers: [], notServed: "", groupName: "MOBILE" });
    await mount([hdr("x-b", "1", { onlyGroups: true, groupIds: ["grp_home"] })]);
    await act(async () => void (await new Promise((r) => setTimeout(r, 400))));
    await settle();
    expect(text()).toContain(en["sx.hx.h.pvNone"]);
  });

  it("does not preview a list the panel would refuse", async () => {
    await mount([hdr("Content-Type", "text/plain")]);
    await act(async () => void (await new Promise((r) => setTimeout(r, 400))));
    expect(previewResponseHeaders).not.toHaveBeenCalled();
    expect(text()).toContain(en["sx.hx.h.pvFix"]);
  });

  it("says when the panel refuses the save", async () => {
    await mount([hdr("x-a", "1")]);
    updateSubscriptionSettings.mockRejectedValue(new Error("boom"));
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(document.querySelector(".hx-fh.bad")).not.toBeNull();
  });
});

describe("the texts in every language", () => {
  it("carry the same placeholders as English", () => {
    const keys = Object.keys(en).filter((k) => k.startsWith("subs.hdr.") || k.startsWith("sx.hx.h."));
    expect(keys.length).toBeGreaterThan(100);
    for (const lang of ["ru", "zh", "fa", "tr"] as const) {
      for (const k of keys) {
        const names = (m: string) => [...new Set([...m.matchAll(/\{(\w+)\}/g)].map((x) => x[1]))].sort();
        const want = names((en as Record<string, string>)[k]!);
        const got = names(dictionaries[lang][k as keyof typeof en]);
        expect(got, `${lang} ${k}`).toEqual(want);
      }
    }
  });
});
