import { readFileSync } from "node:fs";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, useEffect, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { en } from "@/i18n/en";
import { dictionaries, fill } from "@/i18n";
import { meQuery } from "@/lib/session";
import { LeaveGuard } from "./guard";
import { MihomoTab } from "./mihomo";
import { kib, starterTemplate, templateBytes, templateProblems, templateRefusal, textKey, type MihomoTemplate, type Settings } from "./model";

// Subscriptions -> Mihomo templates: the list with its counters and states, the editor (the groups that are taken, the text with its
// line numbers), what the panel refuses and what it only points out (by the codes it sends), the preview for each outcome, a save
// that lands on the fresh document, and the ask before changes are lost.

const getSubscriptionSettings = vi.fn();
const updateSubscriptionSettings = vi.fn();
const previewMihomoTemplate = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  subscriptions: {
    getSubscriptionSettings: (...a: unknown[]) => getSubscriptionSettings(...a),
    updateSubscriptionSettings: (...a: unknown[]) => updateSubscriptionSettings(...a),
    previewMihomoTemplate: (...a: unknown[]) => previewMihomoTemplate(...a),
  },
  groups: { listGroups: async () => ({ groups: [{ id: "grp_router", name: "ROUTER" }, { id: "grp_pc", name: "PC" }, { id: "grp_mobile", name: "MOBILE" }] }) },
  users: { listUsers: async () => ({ users: users() }) },
}));

type Blocker = { status: "idle" | "blocked"; proceed?: () => void; reset?: () => void };
let blocker: Blocker = { status: "idle" };
let blockerOpts: { shouldBlockFn: (a: { current: { pathname: string }; next: { pathname: string; search: unknown } }) => boolean } | null = null;
vi.mock("@tanstack/react-router", async (orig) => ({
  ...(await orig<object>()),
  useBlocker: (o: typeof blockerOpts) => {
    blockerOpts = o;
    return blocker;
  },
}));

let people = [
  { id: "usr_alice", name: "alice", groupId: "grp_router" },
  { id: "usr_bob", name: "bob", groupId: "grp_pc" },
];
const users = () => people;

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [getSubscriptionSettings, updateSubscriptionSettings, previewMihomoTemplate]) m.mockReset();
  blocker = { status: "idle" };
  blockerOpts = null;
  people = [
    { id: "usr_alice", name: "alice", groupId: "grp_router" },
    { id: "usr_bob", name: "bob", groupId: "grp_pc" },
  ];
});

const GOOD = "mixed-port: 7890\ndns:\n  enable: true\nproxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\n";
const tpl = (name: string, groupIds: string[] = [], yaml = GOOD): MihomoTemplate => ({ name, yaml, groupIds });
const doc = (list: MihomoTemplate[], extra: object = {}) => ({ title: "", rules: [], apps: [], mihomoTemplates: list, ...extra }) as unknown as Settings;
const reply = (o: object = {}) => ({ yaml: "proxies:\n  - {name: de1, type: vless, server: de1.example.com, port: 443}\n" + GOOD, warnings: [], groupName: "ROUTER", notServed: "", ...o });

// the screen hands the tab the settings again once a save has made them new: this stands in for it
let renew: ((s: Settings) => void) | null = null;
function Renewed({ settings, initial }: { settings: Settings; initial?: number | null }) {
  const [now, setNow] = useState(settings);
  useEffect(() => {
    renew = setNow;
  }, []);
  return <MihomoTab data={{ settings: now }} initial={initial} />;
}

async function mount(list: MihomoTemplate[], opts: { role?: Role; initial?: number | null; settings?: object; guard?: (d: boolean) => void } = {}) {
  const settings = doc(list, opts.settings);
  getSubscriptionSettings.mockResolvedValue({ settings, effectiveTitle: "", reservedHeaderNames: [] });
  updateSubscriptionSettings.mockImplementation(async (r: { settings: Settings }) => ({ settings: r.settings }));
  if (!previewMihomoTemplate.getMockImplementation()) previewMihomoTemplate.mockResolvedValue(reply());
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role: opts.role ?? Role.OWNER } } as never);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <LeaveGuard.Provider value={opts.guard ?? (() => {})}>
            <Renewed settings={settings} initial={opts.initial} />
          </LeaveGuard.Provider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async (ms = 0) => {
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, ms))));
};
const debounced = () => settle(100).then(() => act(async () => void (await new Promise((r) => setTimeout(r, 450))))).then(() => settle());
const text = () => document.body.textContent ?? "";
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const buttonWith = (t: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === t)!;
const heads = () => [...document.querySelectorAll<HTMLButtonElement>("button.hx-rh")];
const head = (name: string) => heads().find((b) => b.querySelector("b")?.textContent === name)!;
const chipOf = (label: string) => [...document.querySelectorAll(`[role=group][aria-label="${en["sx.hx.m.groups"]}"] button`)].find((b) => b.textContent === label)!;
const kb = (n: number) => (n / 1024).toLocaleString("en", { minimumFractionDigits: 1, maximumFractionDigits: 1 });
const area = () => document.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${en["sx.hx.m.text"]}"]`)!;
const typeInto = (el: HTMLInputElement | HTMLTextAreaElement, value: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const sent = () => (updateSubscriptionSettings.mock.calls.at(-1)?.[0] as { settings: Settings } | undefined)?.settings;
const refuse = (msg: string) => new ConnectError(msg, Code.InvalidArgument);

describe("the pieces the form says early", () => {
  it("counts a text as the panel does, in KiB", () => {
    expect(templateBytes("héllo")).toBe(6);
    expect(kib(1536)).toBe("1.5");
    expect(textKey("a")).not.toBe(textKey("b"));
    expect(textKey("ab")).toBe(textKey("ab"));
  });
  it("tells a missing, a taken and a too long name, an empty and a too big text", () => {
    const rows = [tpl("ROUTER"), tpl(" router "), tpl("", [], ""), tpl("x".repeat(61), [], "a".repeat(65 * 1024))];
    expect(templateProblems(rows, 0).name?.key).toBe("subs.mt.err.nameTaken");
    expect(templateProblems(rows, 2)).toMatchObject({ name: { key: "subs.mt.err.noName" }, text: { key: "subs.mt.err.empty" } });
    expect(templateProblems(rows, 3)).toMatchObject({ name: { key: "subs.mt.err.nameLong" }, text: { key: "subs.mt.err.big" } });
    expect(templateProblems([tpl("a"), tpl("b")], 1)).toEqual({ name: null, text: null });
  });
  it("takes the line and the template out of the panel's refusal", () => {
    expect(templateRefusal('invalid subscription settings: mihomo_templates[1] ("PC").yaml: line 7: mapping values are not allowed')).toEqual({ text: "line 7: mapping values are not allowed", line: 7, index: 1 });
    expect(templateRefusal('mihomo_templates[0] ("a \\"b\\"").yaml: proxy-groups[0] "x": a type is required')).toEqual({ text: 'proxy-groups[0] "x": a type is required', index: 0 });
    expect(templateRefusal("invalid subscription settings: mihomo_templates[2].group_ids: the group \"g\" is already in the template \"A\"")).toMatchObject({ index: 2 });
    expect(templateRefusal("invalid subscription settings: mihomo_templates: 300000 bytes of text in all")).toEqual({ text: "mihomo_templates: 300000 bytes of text in all" });
  });
  it("starts a new template with a group over every server, and Auto only while the panel adds it", () => {
    expect(starterTemplate(true)).toContain("proxies: [Auto, DIRECT]");
    expect(starterTemplate(false)).toContain("proxies: [DIRECT]");
    expect(starterTemplate(false)).toContain("include-all-proxies: true");
  });
});

describe("no templates yet", () => {
  it("says so, and the first one is made from the starter text, open, with the save on", async () => {
    await mount([]);
    expect(text()).toContain(en["sx.hx.m.noneT"]);
    expect(text()).toContain(en["sx.hx.m.rNoTpl"]); // the preview says everybody gets the panel's own profile
    expect(text()).not.toContain(en["sx.hx.m.ftDirty"]);
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(true);
    await click(buttonWith(en["sx.hx.m.add"]));
    expect(area().value).toBe(starterTemplate(true));
    expect(text()).toContain(en["sx.hx.m.ftDirty"]);
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(false);
    expect(document.querySelector<HTMLInputElement>("#mt-name")!.value).toBe(fill(en["subs.mt.defaultName"], { n: 1 }));
  });
  it("starts without Auto when the switch of automatic selection is off", async () => {
    await mount([], { settings: { linkAutoSelect: false } });
    await click(buttonWith(en["sx.hx.m.add"]));
    expect(area().value).toBe(starterTemplate(false));
  });
});

describe("the list", () => {
  it("shows each template with its groups, its size and the word of its state, and the counters", async () => {
    previewMihomoTemplate.mockImplementation(async (r: { yaml: string }) => reply({ warnings: r.yaml.includes("secret") ? [{ code: "own_dns", arg: "" }, { code: "unknown_rule_target", arg: "X" }] : [] }));
    const list = [tpl("Home routers", ["grp_router"]), tpl("Desktop", ["grp_pc"], GOOD + "# secret\n"), tpl("Draft"), tpl("Bare", ["grp_mobile"], GOOD.replace(/dns:\n {2}enable: true\n/, ""))];
    const bytes = list.reduce((n, r) => n + templateBytes(r.yaml), 0);
    await mount(list);
    await debounced();
    const rows = heads().map((b) => b.textContent!);
    expect(rows[0]).toContain("Home routers");
    expect(rows[0]).toContain(fill(en["sx.hx.m.sub"], { groups: "ROUTER", kib: kib(templateBytes(GOOD)) }));
    expect(rows[0]).toContain(en["sx.hx.m.stOk"]);
    expect(rows[1]).toContain(fill(en["sx.hx.m.stWarn"], { n: 2 }));
    expect(rows[2]).toContain(en["sx.hx.m.stFree"]);
    expect(rows[2]).toContain(en["sx.hx.m.noGroups"]);
    expect(rows[3]).toContain(fill(en["sx.hx.m.stWarn"], { n: 1 })); // no dns of its own
    expect(text()).toContain(fill(en["sx.hx.m.sz"], { n: 4, max: 20, kib: kb(bytes), total: 256 }));
    expect(head("Home routers").getAttribute("aria-expanded")).toBe("true"); // the first is open
    expect(area().value).toBe(GOOD);
  });
  it("opens one template at a time, and folds the open one on a second click", async () => {
    await mount([tpl("A", ["grp_router"], "a: 1\n"), tpl("B", [], "b: 2\n")]);
    await click(head("B"));
    expect(area().value).toBe("b: 2\n");
    expect(document.querySelector<HTMLInputElement>("#mt-name")!.value).toBe("B");
    expect(head("A").getAttribute("aria-expanded")).toBe("false");
    await click(head("B"));
    expect(document.querySelector("textarea.mt-y")).toBeNull();
  });
  it("makes a copy after the template, without groups and with a name of its own", async () => {
    await mount([tpl("A", ["grp_router"], "a: 1\n"), tpl("Z", [], "z: 1\n")]);
    await click(buttonWith(en["sx.hx.m.dup"]));
    expect(document.querySelector<HTMLInputElement>("#mt-name")!.value).toBe(fill(en["sx.hx.m.copy"], { name: "A" }));
    expect(area().value).toBe("a: 1\n");
    expect(heads().map((b) => b.querySelector("b")!.textContent)).toEqual(["A", fill(en["sx.hx.m.copy"], { name: "A" }), "Z"]);
    expect(heads()[1]!.textContent).toContain(en["sx.hx.m.noGroups"]);
    await click(buttonWith(en["sx.hx.m.dup"]));
    expect(document.querySelector<HTMLInputElement>("#mt-name")!.value).toBe(`${fill(en["sx.hx.m.copy"], { name: fill(en["sx.hx.m.copy"], { name: "A" }) })}`);
  });
  it("asks before a delete, in the row, and says what that does to the groups", async () => {
    await mount([tpl("A", ["grp_router", "grp_pc"]), tpl("B")]);
    await click(buttonWith(en["sx.hx.m.del"]));
    expect(text()).toContain(fill(en["sx.hx.m.ask"], { name: "A" }));
    expect(head("A")).not.toBeNull();
    await click(buttonWith(en["sx.hx.m.cancel"]));
    expect(text()).not.toContain(fill(en["sx.hx.m.ask"], { name: "A" }));
    await click(buttonWith(en["sx.hx.m.del"]));
    await click([...document.querySelectorAll("button")].filter((b) => b.textContent?.trim() === en["sx.hx.m.del"]).at(-1)); // the one in the question
    await settle();
    expect(head("A")).toBeUndefined();
    expect(head("B").getAttribute("aria-expanded")).toBe("true");
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(sent()?.mihomoTemplates).toEqual([tpl("B")]);
  });
  it("says a template nobody has is deleted without anything going back", async () => {
    await mount([tpl("A")]);
    await click(buttonWith(en["sx.hx.m.del"]));
    expect(text()).toContain(fill(en["sx.hx.m.askFree"], { name: "A" }));
  });
  it("keeps to 20 templates", async () => {
    await mount(Array.from({ length: 20 }, (_, n) => tpl(`T${n}`)));
    expect(buttonWith(en["sx.hx.m.add"]).hasAttribute("disabled")).toBe(true);
    expect(buttonWith(en["sx.hx.m.dup"]).hasAttribute("disabled")).toBe(true);
    expect(text()).toContain(fill(en["sx.hx.m.ftMax"], { max: 20 }));
  });
});

describe("the editor", () => {
  it("takes a group away from the others: it is off, says which template has it, and does not change", async () => {
    await mount([tpl("Home routers", ["grp_router"]), tpl("Desktop")], { initial: 1 });
    const chip = chipOf("ROUTER");
    expect(chip.getAttribute("aria-disabled")).toBe("true");
    expect((chip as HTMLElement).title).toBe(fill(en["sx.hx.m.taken"], { tpl: "Home routers" }));
    expect(text()).toContain(fill(en["sx.hx.m.busy"], { group: "ROUTER", tpl: "Home routers" }));
    expect(text()).toContain(en["sx.hx.m.busyEnd"]);
    await click(chip);
    expect(text()).not.toContain(en["sx.hx.m.ftDirty"]);
    const pc = chipOf("PC");
    expect(pc.getAttribute("aria-disabled")).toBeNull();
    await click(pc);
    expect(pc.getAttribute("aria-pressed")).toBe("true");
    expect(text()).toContain(en["sx.hx.m.ftDirty"]);
  });
  it("shows what the panel refuses with its line, keeps the save off, and the list says it has errors", async () => {
    previewMihomoTemplate.mockRejectedValue(refuse("line 2: could not find expected ':'"));
    await mount([tpl("Draft", ["grp_pc"], "a: 1\nmode rule\n")]);
    await debounced();
    expect(text()).toContain(en["sx.hx.m.refused"]);
    expect(text()).toContain("line 2: could not find expected ':'");
    expect(document.querySelector("button.mt-ln")!.textContent).toBe(fill(en["sx.hx.m.line"], { n: 2 }));
    expect(heads()[0]!.textContent).toContain(en["sx.hx.m.stBad"]);
    expect(document.querySelector(".hx-r.bad")).not.toBeNull();
    await typeInto(area(), "a: 1\nmode: rule\n"); // a change makes it dirty, the refusal stands until the check says otherwise
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(true);
    expect(text()).toContain(en["sx.hx.m.ftBad"]);
    expect(text()).toContain(en["sx.hx.m.rBad"]);
  });
  it("takes the cursor to the line the panel names", async () => {
    previewMihomoTemplate.mockRejectedValue(refuse("line 2: could not find expected ':'"));
    await mount([tpl("Draft", ["grp_router"], "a: 1\nmode rule\nb: 2\n")]);
    await debounced();
    await click(document.querySelector("button.mt-ln"));
    const a = area();
    expect(a.value.slice(a.selectionStart, a.selectionEnd)).toBe("mode rule");
  });
  it("says early what is wrong with the name and the text, and does not save", async () => {
    await mount([tpl("A"), tpl("B")]);
    await typeInto(document.querySelector("#mt-name")!, "b");
    expect(text()).toContain(en["subs.mt.err.nameTaken"]);
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(true);
    await typeInto(document.querySelector("#mt-name")!, " ");
    expect(text()).toContain(en["sx.hx.m.eName"]);
    await typeInto(document.querySelector("#mt-name")!, "C");
    await typeInto(area(), "  \n");
    expect(text()).toContain(en["sx.hx.m.eEmpty"]);
    await typeInto(area(), "x".repeat(65 * 1024));
    expect(text()).toContain(fill(en["sx.hx.m.eBig"], { max: 64 }));
    await click(buttonWith(en["subs.save"]));
    expect(updateSubscriptionSettings).not.toHaveBeenCalled();
  });
  it("lists the warnings in the language of the code and the ones the text itself gives, and still lets the owner save", async () => {
    previewMihomoTemplate.mockResolvedValue(
      reply({
        warnings: [
          { code: "own_dns", arg: "" },
          { code: "unknown_rule_target", arg: "Direct" },
          { code: "preset_rules_skipped", arg: "" },
          { code: "from_a_newer_panel", arg: "" },
          { code: "unused", arg: "" },
        ],
      }),
    );
    await mount([tpl("Desktop", ["grp_pc"], GOOD.replace(/dns:\n {2}enable: true\n/, "# rules: https://rules.example.com/a.yaml, http://x.example.net/b\n"))]);
    await debounced();
    for (const w of [en["subs.mt.warn.own_dns"], fill(en["subs.mt.warn.unknown_rule_target"], { arg: "Direct" }), en["subs.mt.warn.preset_rules_skipped"], fill(en["subs.mt.warn.other"], { code: "from_a_newer_panel" }), en["sx.hx.m.wNoDns"], fill(en["sx.hx.m.wHosts"], { hosts: "rules.example.com, x.example.net" })]) expect(text()).toContain(w);
    expect(text()).not.toContain(en["subs.mt.warn.unused"]); // the form's own word for it stands, and only when no group is chosen
    expect(text()).not.toContain(en["sx.hx.m.refused"]);
    expect(heads()[0]!.textContent).toContain(fill(en["sx.hx.m.stWarn"], { n: 6 }));
    await typeInto(area(), GOOD + "# more\n");
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(false);
  });
  it("says nobody has a template that has no group", async () => {
    await mount([tpl("Draft")]);
    expect(text()).toContain(en["sx.hx.m.wFree"]);
    expect(heads()[0]!.textContent).toContain(en["sx.hx.m.stFree"]);
  });
  it("loads a file into the text, and says a file that is far too big is not loaded", async () => {
    await mount([tpl("A")]);
    const input = document.querySelector<HTMLInputElement>("input[type=file]")!;
    const pick = (f: File) =>
      act(async () => {
        Object.defineProperty(input, "files", { value: [f], configurable: true });
        input.dispatchEvent(new Event("change", { bubbles: true }));
        await new Promise((r) => setTimeout(r, 20));
      });
    await pick(new File(["x: 1\n"], "t.yaml", { type: "text/yaml" }));
    await settle();
    expect(area().value).toBe("x: 1\n");
    await pick(new File([new Uint8Array(1024 * 1024 + 1)], "big.yaml"));
    await settle();
    expect(text()).toContain(en["sx.hx.m.eFile"]);
    expect(area().value).toBe("x: 1\n");
    await typeInto(area(), "y: 1\n"); // a change of the text takes the message away
    expect(text()).not.toContain(en["sx.hx.m.eFile"]);
  });
  it("takes a file dropped on the text, and marks the text while one is over it", async () => {
    await mount([tpl("A")]);
    const drag = (type: string, files: File[] = []) => act(async () => void area().dispatchEvent(Object.assign(new Event(type, { bubbles: true, cancelable: true }), { dataTransfer: { types: ["Files"], files } })));
    await drag("dragover");
    expect(area().className).toContain("drag");
    await drag("dragleave");
    expect(area().className).not.toContain("drag");
    await drag("drop", [new File(["dropped: 1\n"], "d.yaml")]);
    await settle();
    expect(area().value).toBe("dropped: 1\n");
  });
});

describe("the preview", () => {
  it("asks the panel with the text as it stands in the form (saved or not) and the groups, and shows the profile read only", async () => {
    await mount([tpl("Home routers", ["grp_router"])]);
    await typeInto(area(), "mixed-port: 1\n");
    await debounced();
    const call = previewMihomoTemplate.mock.calls.at(-1)?.[0] as { userId: string; yaml: string; groupIds: string[] };
    expect(call).toEqual({ userId: "usr_alice", yaml: "mixed-port: 1\n", groupIds: ["grp_router"] }); // alice: the person of the template's group
    const out = document.querySelector<HTMLPreElement>("pre.mt-pre")!;
    expect(out.textContent).toContain("de1.example.com");
    expect(text()).toContain(en["sx.hx.m.pvKeys"]);
  });
  it("outcome 1: the person of a group of the template gets the template", async () => {
    await mount([tpl("Home routers", ["grp_router"])]);
    await debounced();
    expect(document.querySelector(".mt-res")!.textContent).toBe(en["sx.hx.m.rThis"] + fill(en["sx.hx.m.rThisS"], { group: "ROUTER" }));
    expect(buttonWith("alice").getAttribute("aria-pressed")).toBe("true");
  });
  it("outcome 2: a person of a group that no template names gets the panel's own profile, and can see what the text would give", async () => {
    people = [{ id: "usr_carol", name: "carol", groupId: "grp_mobile" }];
    previewMihomoTemplate.mockResolvedValue(reply({ groupName: "MOBILE" }));
    await mount([tpl("Home routers", ["grp_router"])]);
    await debounced();
    expect(document.querySelector(".mt-res")!.textContent).toBe(en["sx.hx.m.rBuilt"] + fill(en["sx.hx.m.rNoGrp"], { group: "MOBILE" }));
    expect(document.querySelector("pre.mt-pre")).toBeNull();
    await click(buttonWith(en["sx.hx.m.pvAnyway"]));
    expect(document.querySelector("pre.mt-pre")!.textContent).toContain("de1.example.com");
  });
  it("outcome 3: a person of the group of another template gets that one", async () => {
    people = [{ id: "usr_bob", name: "bob", groupId: "grp_pc" }];
    previewMihomoTemplate.mockResolvedValue(reply({ groupName: "PC" }));
    await mount([tpl("Home routers", ["grp_router"]), tpl("Desktop", ["grp_pc"])], { initial: 0 });
    await debounced();
    expect(document.querySelector(".mt-res")!.textContent).toBe(en["sx.hx.m.rOther"] + fill(en["sx.hx.m.rOtherS"], { group: "PC", tpl: "Desktop" }));
  });
  it("outcome 3, nothing open: says who gets which", async () => {
    await mount([tpl("Home routers", ["grp_router"])], { initial: null });
    expect(document.querySelector(".mt-res")!.textContent).toContain(en["sx.hx.m.rOther"]);
    expect(document.querySelector("pre.mt-pre")).toBeNull();
  });
  it("outcome 4: no access or no servers: the panel's own profile, and the reason", async () => {
    previewMihomoTemplate.mockResolvedValue(reply({ notServed: "no_access", yaml: "port: 7890\n" }));
    await mount([tpl("Home routers", ["grp_router"])]);
    await debounced();
    expect(document.querySelector(".mt-res")!.textContent).toBe(en["sx.hx.m.rOff"] + en["sx.hx.m.rOffS"]);
    expect(document.querySelector("pre.mt-pre")).toBeNull();
    previewMihomoTemplate.mockResolvedValue(reply({ notServed: "no_servers", yaml: "port: 7890\n" }));
    await typeInto(area(), GOOD + "# x\n");
    await debounced();
    expect(document.querySelector(".mt-res")!.textContent).toBe(en["sx.hx.m.rNoSrv"] + en["sx.hx.m.rNoSrvS"]);
  });
  it("another person: asks again for them", async () => {
    await mount([tpl("Home routers", ["grp_router"])]);
    await debounced();
    await click(buttonWith("bob"));
    await debounced();
    expect((previewMihomoTemplate.mock.calls.at(-1)![0] as { userId: string }).userId).toBe("usr_bob");
    expect(document.querySelector(".mt-res")!.textContent).toContain(en["sx.hx.m.rNoGrp"].split("{")[0]!);
  });
  it("has nobody to preview for: says so and does not ask", async () => {
    people = [];
    await mount([tpl("A")]);
    await debounced();
    expect(text()).toContain(en["sx.hx.m.pvNoUsers"]);
    expect(previewMihomoTemplate).not.toHaveBeenCalled();
  });
  it("many people: a list to pick from instead of a chip each", async () => {
    people = Array.from({ length: 12 }, (_, n) => ({ id: `usr_${n}`, name: `person${n}`, groupId: "grp_router" }));
    await mount([tpl("A", ["grp_router"])]);
    await debounced();
    expect(document.querySelector("[role=combobox]")).not.toBeNull();
    expect(document.querySelectorAll(`[role=group][aria-label="${en["sx.hx.m.pvWho"]}"] button`)).toHaveLength(0);
  });
});

describe("saving", () => {
  it("lays the templates on the document as the panel holds it now, trimmed, with only the groups that exist", async () => {
    await mount([tpl("A", ["grp_router"])]);
    getSubscriptionSettings.mockResolvedValue({ settings: doc([tpl("A", ["grp_router"])], { title: "Fresh" }), effectiveTitle: "", reservedHeaderNames: [] });
    await click(buttonWith(en["sx.hx.m.add"]));
    await typeInto(document.querySelector("#mt-name")!, "  B  ");
    await click(chipOf("PC"));
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(sent()).toMatchObject({ title: "Fresh" }); // the document as the panel holds it at the save, not the one the form started from
    expect(sent()?.mihomoTemplates).toEqual([tpl("A", ["grp_router"]), { name: "B", yaml: starterTemplate(true), groupIds: ["grp_pc"] }]);
    expect(text()).toContain(en["subs.saved"]);
  });
  it("puts a refusal at the template it names, opens that template, and says the line", async () => {
    updateSubscriptionSettings.mockReset();
    await mount([tpl("Home routers", ["grp_router"]), tpl("Desktop", ["grp_pc"])]);
    updateSubscriptionSettings.mockRejectedValue(refuse('invalid subscription settings: mihomo_templates[1] ("Desktop").yaml: line 4: mapping values are not allowed in this context'));
    await typeInto(document.querySelector("#mt-name")!, "Home routers 2");
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(head("Desktop").getAttribute("aria-expanded")).toBe("true");
    expect(text()).toContain(en["sx.hx.m.refused"]);
    expect(document.querySelector("button.mt-ln")!.textContent).toBe(fill(en["sx.hx.m.line"], { n: 4 }));
    expect(text()).toContain("line 4: mapping values are not allowed in this context");
    expect(text()).not.toContain("mihomo_templates[1]");
    expect(text()).toContain(en["subs.discard"]); // still unsaved
  });
  it("puts a refusal of the whole list under the list", async () => {
    await mount([tpl("A")]);
    updateSubscriptionSettings.mockRejectedValue(refuse("invalid subscription settings: mihomo_templates: 300000 bytes of text in all, at most 262144"));
    await typeInto(document.querySelector("#mt-name")!, "A2");
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(text()).toContain(en["sx.hx.m.refusedAll"]);
    expect(text()).toContain("mihomo_templates: 300000 bytes of text in all, at most 262144");
  });
  it("says the whole list is too big", async () => {
    await mount(Array.from({ length: 5 }, (_, n) => tpl(`T${n}`, [], "k: " + "v".repeat(60 * 1024) + "\n")));
    expect(text()).toContain(fill(en["sx.hx.m.ftOver"], { max: 256 }));
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(true);
  });
  it("discards the changes", async () => {
    await mount([tpl("A")]);
    await typeInto(document.querySelector("#mt-name")!, "Changed");
    expect(text()).toContain(en["subs.discard"]);
    await click(buttonWith(en["subs.discard"]));
    expect(document.querySelector<HTMLInputElement>("#mt-name")!.value).toBe("A");
    expect(text()).not.toContain(en["subs.discard"]);
  });
});

describe("after a save", () => {
  it("starts again from what is saved with the same template open, says so, and has nothing left to save", async () => {
    await mount([tpl("A", ["grp_router"]), tpl("B")], { initial: 1 });
    updateSubscriptionSettings.mockImplementation(async (r: { settings: Settings }) => {
      renew!(r.settings);
      return { settings: r.settings };
    });
    await typeInto(document.querySelector("#mt-name")!, "B2");
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(head("B2").getAttribute("aria-expanded")).toBe("true");
    expect(document.querySelector(".hx-fh")!.textContent).toBe(en["subs.saved"]);
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(true);
    expect(text()).not.toContain(en["subs.discard"]);
    await typeInto(document.querySelector("#mt-name")!, "B3"); // the next change takes the word away
    expect(document.querySelector(".hx-fh")!.textContent).toBe(en["sx.hx.m.ftDirty"]);
  });
});

describe("unsaved changes", () => {
  const away = { current: { pathname: "/subscriptions" }, next: { pathname: "/subscriptions", search: { tab: "page" } } };
  it("tells the screen while the form holds changes, so that another tab asks first", async () => {
    const guard = vi.fn();
    await mount([tpl("A")], { guard });
    expect(guard).toHaveBeenLastCalledWith(false);
    await typeInto(document.querySelector("#mt-name")!, "A2");
    expect(guard).toHaveBeenLastCalledWith(true);
    await click(buttonWith(en["subs.discard"]));
    expect(guard).toHaveBeenLastCalledWith(false);
  });
  it("leaves a helper's tab alone", async () => {
    const guard = vi.fn();
    await mount([tpl("A")], { role: Role.HELPER, guard });
    expect(guard).toHaveBeenLastCalledWith(false);
  });
  it("lets go of a clean page and asks about a changed one, but not about the tabs (the screen asks for those)", async () => {
    await mount([tpl("A")]);
    expect(blockerOpts!.shouldBlockFn({ ...away, next: { pathname: "/users", search: {} } })).toBe(false);
    await typeInto(document.querySelector("#mt-name")!, "A2");
    expect(blockerOpts!.shouldBlockFn({ ...away, next: { pathname: "/users", search: {} } })).toBe(true); // another page
    expect(blockerOpts!.shouldBlockFn(away)).toBe(false); // another tab
  });
  it("shows the question while the router holds the move, and goes on only when the owner says so", async () => {
    const proceed = vi.fn();
    const reset = vi.fn();
    blocker = { status: "blocked", proceed, reset };
    await mount([tpl("A")]);
    expect(text()).toContain(en["sx.leave.title"]);
    await click(buttonWith(en["sx.leave.stay"]));
    expect(reset).toHaveBeenCalled();
    expect(proceed).not.toHaveBeenCalled();
  });
  it("proceeds on Leave", async () => {
    const proceed = vi.fn();
    blocker = { status: "blocked", proceed, reset: vi.fn() };
    await mount([tpl("A")]);
    await click(buttonWith(en["sx.leave.go"]));
    expect(proceed).toHaveBeenCalled();
  });
});

describe("who can change it", () => {
  it("a helper reads the templates and the preview, and changes nothing", async () => {
    await mount([tpl("A", ["grp_router"])], { role: Role.HELPER });
    await debounced();
    expect(text()).toContain(en["sx.hx.m.ro"]);
    expect(buttonWith(en["sx.hx.m.add"])).toBeUndefined();
    expect(buttonWith(en["sx.hx.m.del"])).toBeUndefined();
    expect(buttonWith(en["sx.hx.m.load"])).toBeUndefined();
    expect(buttonWith(en["subs.save"])).toBeUndefined();
    expect(area().readOnly).toBe(true);
    expect(document.querySelector<HTMLInputElement>("#mt-name")!.readOnly).toBe(true);
    expect(chipOf("ROUTER").getAttribute("aria-disabled")).toBe("true");
    expect(document.querySelector(".mt-res")!.textContent).toContain(en["sx.hx.m.rThis"]);
  });
  it("a helper sees no add button on an empty list, and the note says why", async () => {
    await mount([], { role: Role.HELPER });
    expect(text()).toContain(en["sx.hx.m.noneT"]);
    expect(buttonWith(en["sx.hx.m.add"])).toBeUndefined();
    expect(text()).toContain(en["sx.hx.m.ro"]);
  });
});

describe("the texts in every language", () => {
  it("carry the same placeholders as English, and only the plural ones carry a |", () => {
    const keys = Object.keys(en).filter((k) => k.startsWith("subs.mt.") || k.startsWith("sx.hx.m.") || k === "subs.tab.mihomo");
    expect(keys.length).toBeGreaterThan(100);
    for (const lang of ["ru", "zh", "fa", "tr"] as const) {
      for (const k of keys) {
        const names = (m: string) => [...new Set([...m.matchAll(/\{(\w+)\}/g)].map((x) => x[1]))].sort();
        const want = names((en as Record<string, string>)[k]!);
        const got = names(dictionaries[lang][k as keyof typeof en]);
        expect(got, `${lang} ${k}`).toEqual(want);
        if (!["subs.mt.warn", "subs.mt.delBody"].includes(k)) expect(dictionaries[lang][k as keyof typeof en], `${lang} ${k}`).not.toContain("|");
      }
    }
  });
  it("have a sentence for every warning code the panel can send", () => {
    const go = readFileSync(`${process.cwd()}/../internal/panel/subsettings/mihomo.go`, "utf8");
    const codes = [...go.matchAll(/^\s*Warn\w+\s*=\s*"(\w+)"/gm)].map((m) => m[1]!);
    expect(codes.sort()).toEqual(["no_server_group", "own_auto", "own_dns", "preset_rules_skipped", "unknown_rule_target", "unused"]);
    for (const lang of ["en", "ru", "zh", "fa", "tr"] as const) for (const c of codes) expect(dictionaries[lang][`subs.mt.warn.${c}` as keyof typeof en], `${lang} ${c}`).toBeTruthy();
  });
});
