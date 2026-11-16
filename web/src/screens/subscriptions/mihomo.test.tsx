import { readFileSync } from "node:fs";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { en } from "@/i18n/en";
import { dictionaries, fill } from "@/i18n";
import { meQuery } from "@/lib/session";
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

const GOOD = "mixed-port: 7890\nproxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\n";
const tpl = (name: string, groupIds: string[] = [], yaml = GOOD): MihomoTemplate => ({ name, yaml, groupIds });
const doc = (list: MihomoTemplate[], extra: object = {}) => ({ title: "", rules: [], apps: [], mihomoTemplates: list, ...extra }) as unknown as Settings;
const reply = (o: object = {}) => ({ yaml: "proxies:\n  - {name: de1, type: vless, server: de1.example.com, port: 443}\n" + GOOD, warnings: [], groupName: "ROUTER", notServed: "", ...o });

async function mount(list: MihomoTemplate[], opts: { role?: Role; initial?: number | null; settings?: object } = {}) {
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
          <MihomoTab data={{ settings }} initial={opts.initial} />
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
const rowButton = (name: string) => document.querySelector<HTMLButtonElement>(`button[aria-label="${fill(en["subs.mt.pick"], { name })}"]`)!;
const area = () => document.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${en["subs.mt.yamlAria"]}"]`)!;
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
  it("says so, and the first one is made from the starter text with the save bar on", async () => {
    await mount([]);
    expect(text()).toContain(en["subs.mt.noneT"]);
    expect(text()).not.toContain(en["subs.dirty"]);
    await click(buttonWith(en["subs.mt.addFirst"]));
    expect(area().value).toBe(starterTemplate(true));
    expect(text()).toContain(en["subs.dirty"]);
    expect(document.querySelector<HTMLInputElement>("#mt-name")!.value).toBe(fill(en["subs.mt.defaultName"], { n: 1 }));
  });
  it("starts without Auto when the switch of automatic selection is off", async () => {
    await mount([], { settings: { linkAutoSelect: false } });
    await click(buttonWith(en["subs.mt.addFirst"]));
    expect(area().value).toBe(starterTemplate(false));
  });
});

describe("the list", () => {
  it("shows each template with its groups, its size and the panel's word, and the counters", async () => {
    previewMihomoTemplate.mockImplementation(async (r: { yaml: string }) => reply({ warnings: r.yaml.includes("dns") ? [{ code: "own_dns", arg: "" }, { code: "unused", arg: "" }] : [] }));
    await mount([tpl("Home routers", ["grp_router"]), tpl("Desktop", ["grp_pc"], GOOD + "dns:\n  enable: true\n"), tpl("Draft")]);
    await debounced();
    const rows = [...document.querySelectorAll("button[aria-pressed]")].map((b) => b.textContent);
    expect(rows[0]).toContain("Home routers");
    expect(rows[0]).toContain("ROUTER");
    expect(rows[0]).toContain(en["subs.mt.ok"]);
    expect(rows[1]).toContain(fill(en["subs.mt.warn"].split("|")[1]!, { n: 2 }));
    expect(rows[2]).toContain(en["subs.mt.unassigned"]);
    expect(rows[0]).toContain(fill(en["subs.mt.size"], { n: kib(templateBytes(GOOD)) }));
    expect(text()).toContain(fill(en["subs.mt.count"], { n: 3, max: 20 }));
    expect(text()).toContain("in all");
    expect(rowButton("Home routers").getAttribute("aria-pressed")).toBe("true"); // the first is open
    expect(area().value).toBe(GOOD);
  });
  it("opens another template in the editor", async () => {
    await mount([tpl("A", ["grp_router"], "a: 1\n"), tpl("B", [], "b: 2\n")]);
    await click(rowButton("B"));
    expect(area().value).toBe("b: 2\n");
    expect(document.querySelector<HTMLInputElement>("#mt-name")!.value).toBe("B");
  });
  it("makes a copy without groups and a name of its own", async () => {
    await mount([tpl("A", ["grp_router"], "a: 1\n")]);
    await click(buttonWith(en["subs.mt.dup"]));
    expect(document.querySelector<HTMLInputElement>("#mt-name")!.value).toBe(fill(en["subs.mt.copyOf"], { name: "A" }));
    expect(area().value).toBe("a: 1\n");
    expect(document.querySelector("button[aria-pressed=true]")!.textContent).toContain(en["subs.mt.unassigned"]);
  });
  it("deletes only after the owner confirms, and says what that does to the groups", async () => {
    await mount([tpl("A", ["grp_router", "grp_pc"]), tpl("B")]);
    await click(buttonWith(en["subs.mt.del"]));
    expect(text()).toContain(fill(en["subs.mt.delT"], { name: "A" }));
    expect(text()).toContain(fill(en["subs.mt.delBody"].split("|")[1]!, { n: 2 }));
    expect(rowButton("A")).not.toBeNull();
    await click([...document.querySelectorAll("button")].filter((b) => b.textContent?.trim() === en["subs.mt.delDo"]).at(-1)); // the one in the question, not the one of the editor
    await settle();
    expect(rowButton("A")).toBeNull();
    expect(rowButton("B").getAttribute("aria-pressed")).toBe("true");
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(sent()?.mihomoTemplates).toEqual([tpl("B")]);
  });
});

describe("the editor", () => {
  it("takes a group away from the others: it is off and says which template has it", async () => {
    await mount([tpl("Home routers", ["grp_router"]), tpl("Desktop")], { initial: 1 });
    const chip = buttonWith("ROUTER" + fill(en["subs.mt.takenBy"], { name: "Home routers" }));
    expect(chip.hasAttribute("disabled")).toBe(true);
    expect(chip.title).toBe(fill(en["subs.mt.takenTitle"], { group: "ROUTER", name: "Home routers" }));
    await click(chip);
    expect(text()).not.toContain(en["subs.dirty"]);
    const pc = buttonWith("PC");
    expect(pc.hasAttribute("disabled")).toBe(false);
    await click(pc);
    expect(pc.getAttribute("aria-pressed")).toBe("true");
    expect(text()).toContain(en["subs.dirty"]);
  });
  it("numbers the lines of the text and marks the one the panel names", async () => {
    previewMihomoTemplate.mockRejectedValue(refuse("line 2: could not find expected ':'"));
    await mount([tpl("Draft", [], "a: 1\nmode rule\nb: 2\n")]);
    await debounced();
    const nums = [...document.querySelectorAll("[aria-hidden=true] > div")].filter((d) => /^\d+$/.test(d.textContent ?? ""));
    expect(nums.map((n) => n.textContent)).toEqual(["1", "2", "3", "4"]);
    expect(nums[1]!.className).toContain("text-danger-text");
    expect(nums[0]!.className).not.toContain("text-danger-text");
  });
  it("shows what the panel refuses with its line, keeps the save off, and the list says it has errors", async () => {
    previewMihomoTemplate.mockRejectedValue(refuse("line 2: could not find expected ':'"));
    await mount([tpl("Draft", [], "a: 1\nmode rule\n")]);
    await debounced();
    expect(text()).toContain(en["subs.mt.refused"]);
    expect(text()).toContain(fill(en["subs.mt.line"], { n: 2 }));
    expect(text()).toContain("line 2: could not find expected ':'");
    expect(rowButton("Draft").textContent).toContain(en["subs.mt.bad"]);
    await typeInto(area(), "a: 1\nmode: rule\n"); // a change makes it dirty, the refusal stands until the check says otherwise
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(true);
    expect(text()).toContain(en["subs.mt.pv.fix"]);
  });
  it("says early what is wrong with the name, and does not save it", async () => {
    await mount([tpl("A"), tpl("B")]);
    await typeInto(document.querySelector("#mt-name")!, "b");
    expect(text()).toContain(en["subs.mt.err.nameTaken"]);
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(true);
    await click(buttonWith(en["subs.save"]));
    expect(updateSubscriptionSettings).not.toHaveBeenCalled();
  });
  it("lists the warnings in the language of the code, apart from the refusal, and still lets the owner save", async () => {
    previewMihomoTemplate.mockResolvedValue(
      reply({
        warnings: [
          { code: "own_dns", arg: "" },
          { code: "unknown_rule_target", arg: "Direct" },
          { code: "preset_rules_skipped", arg: "" },
          { code: "from_a_newer_panel", arg: "" },
        ],
      }),
    );
    await mount([tpl("Desktop", ["grp_pc"])]);
    await debounced();
    expect(text()).toContain(en["subs.mt.warnT"]);
    expect(text()).toContain(en["subs.mt.warn.own_dns"]);
    expect(text()).toContain(fill(en["subs.mt.warn.unknown_rule_target"], { arg: "Direct" }));
    expect(text()).toContain(en["subs.mt.warn.preset_rules_skipped"]);
    expect(text()).toContain(fill(en["subs.mt.warn.other"], { code: "from_a_newer_panel" }));
    expect(text()).not.toContain(en["subs.mt.refused"]);
    expect(rowButton("Desktop").textContent).toContain(fill(en["subs.mt.warn"].split("|")[1]!, { n: 4 }));
    await typeInto(area(), GOOD + "# more\n");
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(false);
  });
  it("loads a file into the text, and refuses a file that is far too big", async () => {
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
    expect(text()).toContain(en["subs.mt.loadBig"]);
    expect(area().value).toBe("x: 1\n");
  });
});

describe("the preview", () => {
  it("asks the panel with the text as it stands in the form (saved or not) and the groups, and shows the profile read only", async () => {
    await mount([tpl("Home routers", ["grp_router"])]);
    await typeInto(area(), "mixed-port: 1\n");
    await debounced();
    const call = previewMihomoTemplate.mock.calls.at(-1)?.[0] as { userId: string; yaml: string; groupIds: string[] };
    expect(call).toEqual({ userId: "usr_alice", yaml: "mixed-port: 1\n", groupIds: ["grp_router"] }); // alice: the person of the template's group
    const out = document.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${en["subs.mt.pv.yaml"]}"]`)!;
    expect(out.readOnly).toBe(true);
    expect(out.value).toContain("de1.example.com");
  });
  it("outcome 1: the person of a group of the template gets the template", async () => {
    await mount([tpl("Home routers", ["grp_router"])]);
    await debounced();
    expect(text()).toContain(fill(en["subs.mt.pv.isThis"], { name: "alice", group: "ROUTER" }));
    expect(text()).toContain(en["subs.mt.pv.this"]);
  });
  it("outcome 2: a person of a group that no template names gets the panel's own profile", async () => {
    people = [{ id: "usr_carol", name: "carol", groupId: "grp_mobile" }];
    previewMihomoTemplate.mockResolvedValue(reply({ groupName: "MOBILE" }));
    await mount([tpl("Home routers", ["grp_router"])]);
    await debounced();
    expect(text()).toContain(fill(en["subs.mt.pv.none"], { name: "carol", group: "MOBILE" }));
    expect(text()).toContain(en["subs.mt.pv.own"]);
  });
  it("outcome 3: a person of the group of another template gets that one", async () => {
    people = [{ id: "usr_bob", name: "bob", groupId: "grp_pc" }];
    previewMihomoTemplate.mockResolvedValue(reply({ groupName: "PC" }));
    await mount([tpl("Home routers", ["grp_router"]), tpl("Desktop", ["grp_pc"])], { initial: 0 });
    await debounced();
    expect(text()).toContain(fill(en["subs.mt.pv.other"], { name: "bob", group: "PC", tpl: "Desktop" }));
  });
  it("outcome 4: no access or no servers: the panel's own profile, and the reason", async () => {
    previewMihomoTemplate.mockResolvedValue(reply({ notServed: "no_access", yaml: "port: 7890\n" }));
    await mount([tpl("Home routers", ["grp_router"])]);
    await debounced();
    expect(text()).toContain(fill(en["subs.mt.pv.noAccess"], { name: "alice" }));
    expect(text()).toContain(en["subs.mt.pv.own"]);
    previewMihomoTemplate.mockResolvedValue(reply({ notServed: "no_servers", yaml: "port: 7890\n" }));
    await typeInto(area(), GOOD + "# x\n");
    await debounced();
    expect(text()).toContain(fill(en["subs.mt.pv.noServers"], { name: "alice" }));
  });
  it("has nobody to preview for: says so and does not ask", async () => {
    people = [];
    await mount([tpl("A")]);
    await debounced();
    expect(text()).toContain(en["subs.mt.pv.noUsers"]);
    expect(previewMihomoTemplate).not.toHaveBeenCalled();
  });
});

describe("saving", () => {
  it("lays the templates on the document as the panel holds it now, trimmed, with only the groups that exist", async () => {
    await mount([tpl("A", ["grp_router"])]);
    getSubscriptionSettings.mockResolvedValue({ settings: doc([tpl("A", ["grp_router"])], { title: "Fresh" }), effectiveTitle: "", reservedHeaderNames: [] });
    await click(buttonWith(en["subs.mt.add"]));
    await typeInto(document.querySelector("#mt-name")!, "  B  ");
    await click(buttonWith("PC"));
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
    expect(rowButton("Desktop").getAttribute("aria-pressed")).toBe("true");
    expect(text()).toContain(en["subs.mt.refused"]);
    expect(text()).toContain(fill(en["subs.mt.line"], { n: 4 }));
    expect(text()).toContain("line 4: mapping values are not allowed in this context");
    expect(text()).not.toContain("mihomo_templates[1]");
    expect(text()).toContain(en["subs.dirty"]); // still unsaved
  });
  it("puts a refusal of the whole list under the list", async () => {
    await mount([tpl("A")]);
    updateSubscriptionSettings.mockRejectedValue(refuse("invalid subscription settings: mihomo_templates: 300000 bytes of text in all, at most 262144"));
    await typeInto(document.querySelector("#mt-name")!, "A2");
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(text()).toContain(en["subs.mt.refusedAll"]);
    expect(text()).toContain("mihomo_templates: 300000 bytes of text in all, at most 262144");
  });
  it("discards the changes", async () => {
    await mount([tpl("A")]);
    await typeInto(document.querySelector("#mt-name")!, "Changed");
    expect(text()).toContain(en["subs.dirty"]);
    await click(buttonWith(en["subs.discard"]));
    expect(document.querySelector<HTMLInputElement>("#mt-name")!.value).toBe("A");
    expect(text()).not.toContain(en["subs.dirty"]);
  });
});

describe("unsaved changes", () => {
  const away = { current: { pathname: "/subscriptions" }, next: { pathname: "/subscriptions", search: { tab: "page" } } };
  it("lets go of a clean tab and asks about a changed one, but not about staying on the tab", async () => {
    await mount([tpl("A")]);
    expect(blockerOpts!.shouldBlockFn(away)).toBe(false);
    await typeInto(document.querySelector("#mt-name")!, "A2");
    expect(blockerOpts!.shouldBlockFn(away)).toBe(true); // another tab
    expect(blockerOpts!.shouldBlockFn({ ...away, next: { pathname: "/users", search: {} } })).toBe(true); // another page
    expect(blockerOpts!.shouldBlockFn({ ...away, next: { pathname: "/subscriptions", search: { tab: "mihomo" } } })).toBe(false);
  });
  it("shows the question while the router holds the move, and goes on only when the owner says so", async () => {
    const proceed = vi.fn();
    const reset = vi.fn();
    blocker = { status: "blocked", proceed, reset };
    await mount([tpl("A")]);
    expect(text()).toContain(en["subs.mt.leaveT"]);
    await click(buttonWith(en["users.cancel"]));
    expect(reset).toHaveBeenCalled();
    expect(proceed).not.toHaveBeenCalled();
  });
  it("proceeds on Leave", async () => {
    const proceed = vi.fn();
    blocker = { status: "blocked", proceed, reset: vi.fn() };
    await mount([tpl("A")]);
    await click(buttonWith(en["subs.mt.leave"]));
    expect(proceed).toHaveBeenCalled();
  });
});

describe("who can change it", () => {
  it("a helper reads the templates and the preview, and changes nothing", async () => {
    await mount([tpl("A", ["grp_router"])], { role: Role.HELPER });
    await debounced();
    expect(text()).toContain(en["subs.mt.ownerOnly"]);
    expect(buttonWith(en["subs.mt.add"])).toBeUndefined();
    expect(buttonWith(en["subs.mt.del"])).toBeUndefined();
    expect(buttonWith(en["subs.mt.load"])).toBeUndefined();
    expect(area().readOnly).toBe(true);
    expect(document.querySelector<HTMLInputElement>("#mt-name")!.readOnly).toBe(true);
    expect(buttonWith("ROUTER").hasAttribute("disabled")).toBe(true);
    expect(text()).toContain(fill(en["subs.mt.pv.isThis"], { name: "alice", group: "ROUTER" }));
  });
  it("a helper sees no empty-state button", async () => {
    await mount([], { role: Role.HELPER });
    expect(text()).toContain(en["subs.mt.noneT"]);
    expect(buttonWith(en["subs.mt.addFirst"])).toBeUndefined();
  });
});

describe("the texts in every language", () => {
  it("carry the same placeholders as English, and only the plural ones carry a |", () => {
    const keys = Object.keys(en).filter((k) => k.startsWith("subs.mt.") || k === "subs.tab.mihomo");
    expect(keys.length).toBeGreaterThan(70);
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
