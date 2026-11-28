import { readFileSync } from "node:fs";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import * as ace from "ace-builds";
import { act, useEffect, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { GroupSchema } from "@/gen/mistgate/admin/v1/group_pb";
import { en } from "@/i18n/en";
import { dictionaries, fill } from "@/i18n";
import { meQuery } from "@/lib/session";
import { groupsQuery } from "@/screens/users/rpc";
import { LeaveGuard } from "./guard";
import { MihomoTab } from "./mihomo";
import { canonicalSource, condition, displaySource, editSource, mapTransfer, parseSource, readTransfer, sourceSelection, variants, writeTransfer } from "./mihomo-source";
import { pickerUsersQuery } from "./queries";
import { kib, starterTemplate, templateBytes, templateProblems, templateRefusal, textKey, type MihomoTemplate, type Settings } from "./model";

// Subscriptions -> Mihomo templates: the list with its counters and states, the editor (the groups that are taken, the text with its
// line numbers), what the panel refuses and what it only points out (by the codes it sends), the preview for each outcome, a save
// that lands on the fresh document, and the ask before changes are lost.

const getSubscriptionSettings = vi.fn();
const updateSubscriptionSettings = vi.fn();
const previewMihomoTemplate = vi.fn();
const downloadText = vi.fn();
vi.mock("@/lib/awg", async (orig) => ({ ...(await orig<object>()), downloadText: (...a: unknown[]) => downloadText(...a) }));

const defaultGroups = [{ id: "grp_router", name: "ROUTER" }, { id: "grp_pc", name: "PC" }, { id: "grp_mobile", name: "MOBILE" }];
let localGroups = defaultGroups;
let pendingGroups: Promise<typeof defaultGroups> | undefined;
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  subscriptions: {
    getSubscriptionSettings: (...a: unknown[]) => getSubscriptionSettings(...a),
    updateSubscriptionSettings: (...a: unknown[]) => updateSubscriptionSettings(...a),
    previewMihomoTemplate: (...a: unknown[]) => previewMihomoTemplate(...a),
  },
  groups: { listGroups: async () => ({ groups: pendingGroups ? await pendingGroups : localGroups }) },
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
  for (const m of [getSubscriptionSettings, updateSubscriptionSettings, previewMihomoTemplate, downloadText]) m.mockReset();
  localGroups = defaultGroups;
  pendingGroups = undefined;
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
  return qc;
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
const previewSelector = ".mt-preview .mt-source";
const preview = () => ace.edit(document.querySelector<HTMLElement>(previewSelector)!);
const area = () => {
  const editor = ace.edit(document.querySelector<HTMLElement>(".mt-source")!);
  const doc = editor.session.getDocument();
  return {
    editor,
    get value() { return editor.getValue(); },
    get readOnly() { return editor.getReadOnly(); },
    setSelectionRange(start: number, end: number) {
      const a = doc.indexToPosition(start);
      const b = doc.indexToPosition(end);
      editor.selection.setSelectionRange(ace.Range.fromPoints(a, b));
    },
  };
};
const typeInto = (el: HTMLInputElement | ReturnType<typeof area>, value: string) =>
  act(async () => {
    if ("editor" in el) {
      el.editor.setValue(value, -1);
    } else {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    }
  });
const sent = () => (updateSubscriptionSettings.mock.calls.at(-1)?.[0] as { settings: Settings } | undefined)?.settings;
const refuse = (msg: string) => new ConnectError(msg, Code.InvalidArgument);

const marker = (refs: string[]) => `# @mistgate-if ${JSON.stringify(refs)}`;
const endMarker = "# @mistgate-end";
const optionsFor = (name: string) => [...document.querySelectorAll<HTMLSelectElement>("select")].find((s) => s.labels?.[0]?.textContent?.startsWith(fill(en["sx.hx.m.importGroup"], { name })))!;
const chooseOption = (el: HTMLSelectElement, value: string) => act(async () => {
  el.value = value;
  el.dispatchEvent(new Event("change", { bubbles: true }));
});
const loadFile = async (contents: string) => {
  const input = document.querySelector<HTMLInputElement>("input[type=file]")!;
  await act(async () => {
    Object.defineProperty(input, "files", { value: [new File([contents], "mihomo-template.yaml")], configurable: true });
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await settle();
};

describe("Mihomo group source", () => {
  const nested = `rules:\n${marker(["grp_router", "grp_pc"])}\n  - DOMAIN,x.test,DIRECT\n  # keep this comment\n${endMarker}\n  - MATCH,DIRECT`;
  it.each([
    ["legacy and indented literals", "a: |\r\n  # @mistgate-if []\n  still a literal\n# not @mistgate-end", [], ["a: |\r\n  # @mistgate-if []\n  still a literal\n# not @mistgate-end"]],
    ["rules; identical active sets", nested, ["grp_router", "grp_pc", "grp_mobile"], [
      "rules:\n  - MATCH,DIRECT",
      "rules:\n  - DOMAIN,x.test,DIRECT\n  # keep this comment\n  - MATCH,DIRECT",
    ]],
    ["mixed line endings and EOF marker", `a: 1\r\n${marker(["grp_router"])}\t\r\nb: 2\n${endMarker}`, ["grp_router"], ["a: 1\r\n", "a: 1\r\nb: 2\n"]],
    ["unassigned references stay inactive", nested, ["grp_mobile"], ["rules:\n  - MATCH,DIRECT"]],
    ["separate blocks for one group", `mode: rule\n${marker(["grp_router"])}\nredir-port: 1182\n${endMarker}\nrules:\n${marker(["grp_router", "grp_pc"])}\n  - DOMAIN,x.test,DIRECT\n${endMarker}\n  - MATCH,DIRECT\n`, ["grp_router", "grp_pc"], [
      "mode: rule\nrules:\n  - MATCH,DIRECT\n",
      "mode: rule\nredir-port: 1182\nrules:\n  - DOMAIN,x.test,DIRECT\n  - MATCH,DIRECT\n",
      "mode: rule\nrules:\n  - DOMAIN,x.test,DIRECT\n  - MATCH,DIRECT\n",
    ]],
  ] as const)("%s", (_name, yaml, assigned, want) => {
    const parsed = parseSource(yaml, true);
    expect(parsed.issue).toBeUndefined();
    expect([...variants(yaml, parsed.value!, assigned)]).toEqual(want);
  });

  it.each(["\r", "\u0085", "\u2028", "\u2029"])("does not treat conditions after line separator %j as ordinary comments", (eol) => {
    const yaml = ["mode: rule", marker(["grp_router"]), "redir-port: 1182", endMarker].join(eol);
    expect(parseSource(yaml, true).issue).toEqual({ kind: "lineBreak", line: 1 });
  });

  it("does not convert BOM-prefixed group markers to ordinary comments", () => {
    const marked = `mode: rule\r\n\uFEFF${marker(["grp_router"])}\r\nredir-port: 1182\r\n\uFEFF${endMarker}`;
    const issue = { kind: "syntax", line: 2 };
    expect(parseSource(marked, true).issue).toEqual(issue);
    expect(readTransfer(marked).issue).toEqual(issue);
    expect(parseSource("\uFEFFmode: rule\n", true).value?.blocks).toHaveLength(0);
  });

  it.each([
    ["missing end", marker(["grp_router"]), 2],
    ["stray end", endMarker, 2],
    ["unclosed outer block", `${marker(["grp_router"])}\n${marker(["grp_pc"])}\n${endMarker}`, 2],
    ["unknown reserved keyword", "# @mistgate-typo", 2],
    ["trailing end text", `${marker(["grp_router"])}\nb: 2\n${endMarker} # oops`, 4],
    ["trailing if text", `${marker(["grp_router"])} more\n${endMarker}`, 2],
    ["malformed JSON", "# @mistgate-if [01]\n# @mistgate-end", 2],
    ["empty list", `${marker([])}\n${endMarker}`, 2],
    ["repeat", `${marker(["grp_pc", "grp_pc"])}\n${endMarker}`, 2],
    ["bad ID", `${marker(["grp\npc"])}\n${endMarker}`, 2],
    ["group limit", `${marker(Array.from({ length: 101 }, (_, i) => `grp_${i}`))}\n${endMarker}`, 2],
  ] as const)("rejects %s with source line", (_name, yaml, line) => {
    expect(parseSource(`a: 1\n${yaml}`, true).issue).toEqual({ kind: "syntax", line });
  });

  it.each([
    ["end at next line", "rules:\n  - alpha\n  - bravo\n", 9, 17, `rules:\n${marker(["ROUTER"])}\n  - alpha\n${endMarker}\n  - bravo\n`],
    ["collapsed with CRLF and no final newline", "mode: rule\r\nx: 1", 12, 12, `mode: rule\r\n${marker(["ROUTER"])}\r\nx: 1\r\n${endMarker}`],
    ["partial selection over lines", "one\ntwo\nthree", 2, 6, `${marker(["ROUTER"])}\none\ntwo\n${endMarker}\nthree`],
  ] as const)("wraps complete lines: %s", (_name, yaml, from, to, want) => {
    const selection = sourceSelection(yaml, from, to);
    expect(selection.issue).toBeUndefined();
    expect(condition(displaySource(yaml, defaultGroups), selection.value!, ["grp_router"], defaultGroups).text).toBe(want);
  });
  it("wraps inside a condition, exports nested names, and keeps only the intersection", () => {
    const outer = `${marker(["grp_router", "grp_pc"])}\n`;
    const yaml = `mode: rule\n${outer}ipv6: false\nmixed-port: 1080\n${endMarker}\n`;
    const draft = displaySource(yaml, defaultGroups);
    const start = draft.text.indexOf("mixed-port");
    const end = draft.text.indexOf(endMarker);
    expect(sourceSelection(draft.text, start, end + 1).issue?.kind).toBe("selection");
    const wrapped = condition(draft, sourceSelection(draft.text, start, end).value!, ["grp_pc", "grp_mobile"], defaultGroups);
    const canonical = canonicalSource(wrapped).value!;
    const parsed = parseSource(canonical, true).value!;
    const effective = [...variants(canonical, parsed, ["grp_router", "grp_pc", "grp_mobile"])].map((text) => text.split("\n").filter(Boolean));
    expect(effective).toEqual([
      ["mode: rule"],
      ["mode: rule", "ipv6: false"],
      ["mode: rule", "ipv6: false", "mixed-port: 1080"],
    ]);
    const transfer = writeTransfer("Shared", canonical, ["grp_router", "grp_pc", "grp_mobile"], defaultGroups, wrapped).value!;
    expect(readTransfer(transfer).value?.text).toBe(wrapped.text);
    expect(sourceSelection(wrapped.text, wrapped.text.indexOf("mixed-port"), wrapped.text.indexOf("mixed-port")).value?.block?.refs).toEqual(["PC", "MOBILE"]);
  });


  it("rejects a selection that cuts a block in two and leaves empty lines alone", () => {
    expect(sourceSelection(nested, 1, nested.indexOf(endMarker)).issue?.kind).toBe("selection");
    expect(sourceSelection("a: 1\n \nb: 2\n", 6, 6).issue).toEqual({ kind: "emptyLine", line: 2 });
  });

  it("remaps only operands, preserves escaping, and never assigns a stale alias to a new ID", () => {
    const groups = [{ id: "grp_router", name: 'Дом, "Роутер"' }, defaultGroups[1]!];
    const yaml = `label: grp_router\r\n# @mistgate-if\t[ "grp_router" ] \r\n  # @mistgate-keep grp_router\n${endMarker}`;
    const draft = displaySource(yaml, groups);
    expect(draft.text).toBe(`label: grp_router\r\n# @mistgate-if\t[ ${JSON.stringify(groups[0]!.name)} ] \r\n  # @mistgate-keep grp_router\n${endMarker}`);
    const changed = [{ id: "grp_new", name: groups[0]!.name }, { id: "grp_router", name: "Renamed" }];
    expect(canonicalSource(editSource(draft, draft.text, changed)).value).toBe(yaml);
    expect(canonicalSource(editSource(draft, draft.text, changed.slice(0, 1))).value).toBe(yaml);
    expect(writeTransfer("Дом", yaml, ["grp_new"], changed.slice(0, 1), draft).issue).toEqual({ kind: "inactive", line: 2, group: "grp_router" });
    const file = writeTransfer('Дом "1"', yaml, ["grp_router", "grp_pc"], groups, draft).value!;
    const transfer = readTransfer(file).value!;
    expect(transfer).toEqual({ name: 'Дом "1"', groups: [groups[0]!.name, "PC"], text: draft.text });
    expect(mapTransfer(transfer.text, new Map([[groups[0]!.name, "grp_other"]])).value).toBe(yaml.replace('[ "grp_router" ]', '[ "grp_other" ]'));
  });
});

describe("editing and transferring group blocks", () => {
  it("dims the other group's lines without changing the source, folding or the preview selection", async () => {
    const yaml = `${GOOD}rules:\n${marker(["grp_router"])}\n  - DOMAIN,example.com,Proxy\n${endMarker}\n  - MATCH,Proxy\n`;
    await mount([tpl("Shared", ["grp_router", "grp_pc"], yaml)]);
    const { editor, value } = area();
    const { session } = editor;
    const ruleRow = value.split("\n").findIndex((line) => line.includes("DOMAIN,"));
    const select = document.querySelector<HTMLSelectElement>(".mt-variant select")!;
    const dimmed = () => Object.values(session.getMarkers(true)).filter((marker) => marker.clazz === "mt-dropped");
    await act(async () => {
      editor.gotoLine(ruleRow - 1, 0);
      session.toggleFoldWidget();
    });
    expect(session.getAllFolds().map(({ range }) => [range.start.row, range.end.row])).toEqual([[ruleRow - 2, ruleRow + 2]]);
    await chooseOption(select, "grp_pc");
    expect(dimmed().map(({ range }) => [range?.start.row, range?.end.row])).toEqual([[ruleRow - 1, ruleRow + 1]]);
    expect(editor.getValue()).toBe(value);
    expect(session.getAllFolds()).toHaveLength(1);
    expect(buttonWith("alice").getAttribute("aria-pressed")).toBe("true");
    await chooseOption(select, "grp_router");
    expect(dimmed()).toHaveLength(0);
  });

  it("wraps the selected list item, previews with IDs, and saves the exact canonical source", async () => {
    const yaml = `${GOOD}rules:\n  - DOMAIN,router.test,Proxy\n  - MATCH,Proxy\n`;
    const inserted = (ref: string) => `${GOOD}rules:\n${marker([ref])}\n  - DOMAIN,router.test,Proxy\n${endMarker}\n  - MATCH,Proxy\n`;
    previewMihomoTemplate.mockImplementation(async ({ userId, yaml, groupIds }: { userId: string; yaml: string; groupIds: string[] }) => {
      const router = userId === "usr_alice";
      const source = parseSource(yaml, true).value!;
      return reply({ yaml: [...variants(yaml, source, groupIds.filter((g) => g === (router ? "grp_router" : "grp_pc")))].at(-1), groupName: router ? "ROUTER" : "PC" });
    });
    await mount([tpl("Shared", ["grp_router", "grp_pc"], yaml)]);
    area().setSelectionRange(yaml.indexOf("router.test"), yaml.indexOf("  - MATCH"));
    await click(buttonWith(en["sx.hx.m.onlyGroups"]));
    const from = yaml.slice(0, yaml.indexOf("  - DOMAIN")).split("\n").length;
    expect(text()).toContain(fill(en["sx.hx.m.conditionHint"], { from: String(from), to: String(from) }));
    const box = document.querySelector<HTMLInputElement>('[role=dialog] input[type=checkbox]')!;
    expect(box.parentElement?.textContent).toBe("ROUTER");
    expect(document.querySelectorAll('[role=dialog] input[type=checkbox]')).toHaveLength(2);
    await click(box);
    await click(buttonWith(en["sx.hx.m.conditionApply"]));
    expect(area().value).toBe(inserted("ROUTER"));
    expect(document.querySelector(".mt-blocks")).toBeNull();
    await click(buttonWith(fill(en["sx.hx.m.blocks"], { n: "1" })));
    const label = fill(en["sx.hx.m.conditionEntry"], { lines: String(from + 1), groups: "ROUTER" });
    await act(async () => {
      area().editor.gotoLine(from - 1, 0);
      area().editor.session.toggleFoldWidget();
    });
    expect(area().editor.session.getAllFolds()).toHaveLength(1);
    await click(buttonWith(label));
    expect(area().editor.session.getAllFolds()).toHaveLength(0);
    expect(area().editor.getSelectedText()).toBe("  - DOMAIN,router.test,Proxy");
    await debounced();
    expect(previewMihomoTemplate).toHaveBeenCalledWith({ userId: "usr_alice", yaml: inserted("grp_router"), groupIds: ["grp_router", "grp_pc"] }, expect.anything());
    expect(preview().getValue()).toContain("router.test");
    await click(buttonWith("bob"));
    await settle();
    expect(previewMihomoTemplate).toHaveBeenCalledWith({ userId: "usr_bob", yaml: inserted("grp_router"), groupIds: ["grp_router", "grp_pc"] }, expect.anything());
    expect(preview().getValue()).toContain("MATCH,Proxy");
    expect(preview().getValue()).not.toContain("router.test");
    expect(preview().getValue()).not.toContain("@mistgate");
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(sent()?.mihomoTemplates).toEqual([tpl("Shared", ["grp_router", "grp_pc"], inserted("grp_router"))]);
  });

  it("refuses overlapping blocks and explicitly confirms that removing a condition makes the lines common", async () => {
    const yaml = `${GOOD}${marker(["grp_router"])}\nmode: rule\n${endMarker}`;
    await mount([tpl("Shared", ["grp_router", "grp_pc"], yaml)]);
    const displayed = area().value;
    area().setSelectionRange(0, displayed.indexOf(endMarker));
    await click(buttonWith(en["sx.hx.m.onlyGroups"]));
    expect(text()).toContain(en["sx.hx.m.source.selection"]);
    expect(area().value).toBe(displayed);
    area().setSelectionRange(0, 0);
    await click(buttonWith(fill(en["sx.hx.m.blocks"], { n: "1" })));
    await click(buttonWith(en["sx.hx.m.conditionEdit"]));
    expect(text()).toContain(en["sx.hx.m.conditionCommon"]);
    await click(buttonWith(en["sx.hx.m.cancel"]));
    expect(area().value).toBe(displayed);
    await click(buttonWith(fill(en["sx.hx.m.blocks"], { n: "1" })));
    await click(buttonWith(en["sx.hx.m.conditionEdit"]));
    const boxes = [...document.querySelectorAll<HTMLInputElement>('[role=dialog] input[type=checkbox]')];
    expect(boxes.map((x) => x.checked)).toEqual([true, false]);
    await click(boxes[0]);
    await click(boxes[1]);
    await click(buttonWith(en["sx.hx.m.conditionApply"]));
    expect(area().value).toContain(marker(["PC"]));
    await click(buttonWith(fill(en["sx.hx.m.blocks"], { n: "1" })));
    await click(buttonWith(en["sx.hx.m.conditionEdit"]));
    await click(buttonWith(en["sx.hx.m.conditionRemove"]));
    expect(area().value).toBe(`${GOOD}mode: rule`);
  });

  it("looks for DNS in effective variants rather than treating conditional DNS as common", async () => {
    const conditional = GOOD.replace("dns:\n  enable: true\n", `${marker(["grp_router"])}\ndns:\n  enable: true\n${endMarker}\n`);
    await mount([tpl("Shared", ["grp_router", "grp_pc"], conditional)]);
    expect(text()).toContain(en["sx.hx.m.wVariantsNoDns"]);
    await typeInto(area(), GOOD);
    expect(text()).not.toContain(en["sx.hx.m.wVariantsNoDns"]);
  });

  it("distinguishes DNS in the selected preview from other variants' DNS", async () => {
    const yaml = `mode: rule\n${marker(["grp_router"])}\ndns:\n  enable: true\n${endMarker}\n`;
    previewMihomoTemplate.mockResolvedValue(reply({ warnings: [{ code: "own_dns", arg: "" }] }));
    await mount([tpl("Shared", ["grp_router", "grp_pc"], yaml)]);
    expect(text()).toContain(en["subs.mt.warn.own_dns"]);
    expect(text()).toContain(en["sx.hx.m.wVariantsNoDns"]);
    expect(text()).not.toContain(en["sx.hx.m.wNoDns"]);
  });

  it("keeps a draft's group identity across refresh, rename and recreation, until explicitly reassigned", async () => {
    const guard = vi.fn();
    const yaml = `${GOOD}${marker(["grp_router"])}\nmode: rule\n${endMarker}\n`;
    const qc = await mount([tpl("Shared", ["grp_router"], yaml)], { guard });
    expect(guard.mock.calls.at(-1)).toEqual([false]);
    await typeInto(area(), area().value + "# unsaved\n");
    await act(async () => void qc.setQueryData(groupsQuery.queryKey, [create(GroupSchema, { id: "grp_router", name: "Renamed" })]));
    await settle();
    expect(area().value).toContain(marker(["ROUTER"]));
    await act(async () => void qc.setQueryData(groupsQuery.queryKey, [create(GroupSchema, { id: "grp_new", name: "ROUTER" })]));
    await settle();
    expect(text()).toContain(fill(en["sx.hx.m.source.inactive"], { group: "grp_router" }));
    await click(chipOf("ROUTER"));
    await click(buttonWith(en["sx.hx.m.export"]));
    expect(downloadText).not.toHaveBeenCalled();
    expect(buttonWith(en["sx.hx.m.exportConfirm"])).toBeUndefined();
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(sent()?.mihomoTemplates).toEqual([tpl("Shared", ["grp_new"], yaml + "# unsaved\n")]);
    const savedYaml = sent()!.mihomoTemplates[0]!.yaml;
    expect([...variants(savedYaml, parseSource(savedYaml, true).value!, ["grp_new"])]).toHaveLength(1);
    await click(document.querySelector("button.mt-ln"));
    await click(buttonWith(en["sx.hx.m.onlyGroups"]));
    await click(document.querySelector('[role=dialog] input[type=checkbox]'));
    await click(buttonWith(en["sx.hx.m.conditionApply"]));
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(sent()?.mihomoTemplates).toEqual([tpl("Shared", ["grp_new"], yaml.replace(marker(["grp_router"]), marker(["grp_new"])) + "# unsaved\n")]);
  });

  it("binds a newly used group name to the recreated group, not the unused old assignment", async () => {
    const qc = await mount([tpl("Shared", ["grp_router"])]);
    await typeInto(area(), GOOD + "# draft\n");
    await act(async () => void qc.setQueryData(groupsQuery.queryKey, [create(GroupSchema, { id: "grp_new", name: "ROUTER" })]));
    await settle();
    await click(chipOf("ROUTER"));
    const block = (ref: string) => `${GOOD}# draft\n${marker([ref])}\nredir-port: 1182\n${endMarker}\n`;
    await typeInto(area(), block("ROUTER"));
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(sent()?.mihomoTemplates).toEqual([tpl("Shared", ["grp_new"], block("grp_new"))]);
  });

  it("keeps an opening marker bound when the group name is reused before the block closes", async () => {
    const qc = await mount([tpl("Shared", ["grp_router", "grp_pc"])]);
    const partial = `${GOOD}${marker(["ROUTER"])}\nredir-port: 1182\n`;
    await typeInto(area(), partial);
    await act(async () => void qc.setQueryData(groupsQuery.queryKey, [
      create(GroupSchema, { id: "grp_router", name: "Renamed" }),
      create(GroupSchema, { id: "grp_pc", name: "ROUTER" }),
    ]));
    await settle();
    await typeInto(area(), partial + endMarker);
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(sent()?.mihomoTemplates[0]?.yaml).toBe(partial.replace(marker(["ROUTER"]), marker(["grp_router"])) + endMarker);
  });

  it("keeps invalid typed edits discardable even when they look like the stored source IDs", async () => {
    const guard = vi.fn();
    const yaml = `${GOOD}${marker(["grp_router"])}\nmode: rule\n${endMarker}\n`;
    await mount([tpl("Shared", ["grp_router"], yaml)], { guard });
    await typeInto(area(), yaml);
    expect(text()).toContain(fill(en["sx.hx.m.source.unknown"], { group: "grp_router" }));
    expect(guard.mock.calls.at(-1)).toEqual([true]);
    await click(buttonWith(en["subs.discard"]));
    expect(area().value).toContain(marker(["ROUTER"]));
    expect(guard.mock.calls.at(-1)).toEqual([false]);
  });

  it("undoes a whole condition edit without losing earlier typing, then redoes the same group binding", async () => {
    await mount([tpl("Shared", ["grp_router"])]);
    const editor = area().editor;
    const typed = GOOD + "mode: rule\n";
    await act(async () => { editor.session.insert(editor.session.getDocument().indexToPosition(GOOD.length), "mode: rule\n"); });
    area().setSelectionRange(GOOD.length, GOOD.length);
    await click(buttonWith(en["sx.hx.m.onlyGroups"]));
    await click(document.querySelector('[role=dialog] input[type=checkbox]'));
    await click(buttonWith(en["sx.hx.m.conditionApply"]));
    expect(area().value).toContain(marker(["ROUTER"]));
    await act(async () => editor.undo());
    expect(area().value).toBe(typed);
    await act(async () => editor.undo());
    expect(area().value).toBe(GOOD);
    await act(async () => editor.redo());
    await act(async () => editor.redo());
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(sent()?.mihomoTemplates[0]?.yaml).toBe(`${GOOD}${marker(["grp_router"])}\nmode: rule\n${endMarker}\n`);
  });

  it("restores mixed line endings when undoing a condition and does not mark the source dirty", async () => {
    const saved = "mode: rule\r\nlog-level: info\nmixed-port: 7890\r\n";
    const guard = vi.fn();
    await mount([tpl("Shared", ["grp_router"], saved)], { guard });
    area().setSelectionRange(0, area().value.indexOf("mixed-port"));
    await click(buttonWith(en["sx.hx.m.onlyGroups"]));
    await click(document.querySelector('[role=dialog] input[type=checkbox]'));
    await click(buttonWith(en["sx.hx.m.conditionApply"]));
    expect(guard.mock.calls.at(-1)).toEqual([true]);
    await act(async () => area().editor.undo());
    expect(guard.mock.calls.at(-1)).toEqual([false]);
    await act(async () => area().editor.redo());
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(sent()?.mihomoTemplates[0]?.yaml).toBe(`${marker(["grp_router"])}\r\nmode: rule\r\nlog-level: info\n${endMarker}\r\nmixed-port: 7890\r\n`);
  });

  it("exports the draft and imports its assignments and variants on a panel with different IDs", async () => {
    const yaml = `${GOOD}${marker(["grp_router"])}\nredir-port: 1182\n${endMarker}\n`;
    await mount([tpl("Shared", ["grp_router", "grp_pc"], yaml)]);
    await typeInto(document.querySelector("#mt-name")!, 'Home, "Lab"');
    await typeInto(area(), area().value + "# unsaved\n");
    await click(buttonWith(en["sx.hx.m.export"]));
    expect(text()).toContain(en["sx.hx.m.exportHint"]);
    await click(buttonWith(en["sx.hx.m.exportConfirm"]));
    expect(downloadText).toHaveBeenCalledOnce();
    const [filename, payload] = downloadText.mock.calls[0]! as [string, string];
    expect(filename).toBe("mihomo-template.yaml");
    expect(payload).toBe(`# @mistgate-template "Home, \\"Lab\\""\n# @mistgate-groups ["ROUTER","PC"]\n${yaml.replace(marker(["grp_router"]), marker(["ROUTER"]))}# unsaved\n`);
    expect(updateSubscriptionSettings).not.toHaveBeenCalled();

    await act(async () => root?.unmount());
    host?.remove();
    localGroups = [{ id: "grp_route2", name: "ROUTER" }, { id: "grp_desktop2", name: "Desktop" }, { id: "grp_busy", name: "PC" }];
    const other = tpl("Other", ["grp_busy"]);
    await mount([tpl("Target"), other]);
    await loadFile(payload);
    expect(optionsFor("ROUTER").value).toBe("grp_route2");
    expect(optionsFor("PC").value).toBe("");
    expect([...optionsFor("PC").options].find((o) => o.value === "grp_busy")?.disabled).toBe(true);
    expect([...optionsFor("PC").options].find((o) => o.value === "grp_route2")?.disabled).toBe(true);
    expect(buttonWith(en["sx.hx.m.importApply"]).hasAttribute("disabled")).toBe(true);
    await click(buttonWith(en["sx.hx.m.cancel"]));
    expect(area().value).toBe(GOOD);
    expect(document.querySelector<HTMLInputElement>("#mt-name")?.value).toBe("Target");
    await loadFile(payload);
    await chooseOption(optionsFor("PC"), "grp_desktop2");
    await click(buttonWith(en["sx.hx.m.importApply"]));
    expect(area().value).toBe(yaml.replace(marker(["grp_router"]), marker(["ROUTER"])) + "# unsaved\n");
    expect(chipOf("Desktop").getAttribute("aria-pressed")).toBe("true");
    expect(updateSubscriptionSettings).not.toHaveBeenCalled();
    await click(buttonWith(en["subs.save"]));
    await settle();
    const mapped = yaml.replace(marker(["grp_router"]), marker(["grp_route2"])) + "# unsaved\n";
    expect(sent()?.mihomoTemplates).toEqual([tpl('Home, "Lab"', ["grp_route2", "grp_desktop2"], mapped), other]);
    const from = yaml + "# unsaved\n";
    expect([...variants(mapped, parseSource(mapped, true).value!, ["grp_route2", "grp_desktop2"])]).toEqual([...variants(from, parseSource(from, true).value!, ["grp_router", "grp_pc"])]);
  });

  it.each([
    ["malformed header", '# @mistgate-template "Shared"\n# @mistgate-groups ["ROUTER",]\nx: 1\n', en["sx.hx.m.source.header"]],
    ["misplaced header", '# comment\nx: 1\n# @mistgate-template "Shared"\n# @mistgate-groups []\n', en["sx.hx.m.source.header"]],
    ["duplicate manifest group", '# @mistgate-template "Shared"\n# @mistgate-groups ["ROUTER","ROUTER"]\nx: 1\n', en["sx.hx.m.source.header"]],
    ["unlisted operand", `# @mistgate-template "Shared"\n# @mistgate-groups ["ROUTER"]\n${marker(["PC"])}\nx: 1\n${endMarker}\n`, fill(en["sx.hx.m.source.unknown"], { group: "PC" })],
    ["unassigned operand in headerless file", `${marker(["PC"])}\nx: 1\n${endMarker}\n`, fill(en["sx.hx.m.source.unknown"], { group: "PC" })],
    ["invalid carriage return in an operand list", `${GOOD}# @mistgate-if ["ROUTER"\r]\nmode: rule\n${endMarker}\n`, en["sx.hx.m.source.lineBreak"]],
  ])("rejects %s without changing the draft", async (_name, file, issue) => {
    await mount([tpl("Original", ["grp_router"])]);
    await loadFile(file);
    expect(text()).toContain(issue);
    expect(area().value).toBe(GOOD);
    expect(chipOf("ROUTER").getAttribute("aria-pressed")).toBe("true");
    expect(document.querySelector<HTMLInputElement>("#mt-name")?.value).toBe("Original");
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(true);
  });

  it("waits for group assignments before mapping a valid headerless file", async () => {
    let resolve!: (groups: typeof defaultGroups) => void;
    pendingGroups = new Promise((done) => { resolve = done; });
    await mount([tpl("Original", ["grp_router"])]);
    const body = `${GOOD}${marker(["ROUTER"])}\nmode: rule\n${endMarker}\n`;
    await loadFile(body);
    expect(area().value).toBe(GOOD);
    await act(async () => resolve(defaultGroups));
    await settle();
    expect(area().value).toBe(body);
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(sent()?.mihomoTemplates[0]?.yaml).toBe(body.replace(marker(["ROUTER"]), marker(["grp_router"])));
  });

  it("does not apply a chooser built on YAML replaced by a pending import", async () => {
    const qc = await mount([tpl("Original", ["grp_router"])]);
    let resolve!: (groups: typeof defaultGroups) => void;
    pendingGroups = new Promise((done) => { resolve = done; });
    await act(async () => { void qc.invalidateQueries({ queryKey: groupsQuery.queryKey }); });
    const body = `mode: rule\n${marker(["ROUTER"])}\nredir-port: 1182\n${endMarker}\n`;
    await loadFile(body);
    area().setSelectionRange(0, 0);
    await click(buttonWith(en["sx.hx.m.onlyGroups"]));
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
    await act(async () => resolve(defaultGroups));
    await settle();
    expect(area().value).toBe(body);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("starts fresh undo history when an import or discard replaces identical visible text", async () => {
    await mount([tpl("Original", ["grp_router"])]);
    let editor = area().editor;
    await act(async () => { editor.session.insert({ row: 0, column: 0 }, "# edited\n"); });
    await loadFile(area().value);
    await act(async () => area().editor.undo());
    expect(area().value).toBe("# edited\n" + GOOD);
    editor = area().editor;
    await act(async () => { editor.session.remove(new ace.Range(0, 0, 1, 0)); });
    await click(chipOf("PC"));
    await click(buttonWith(en["subs.discard"]));
    await act(async () => area().editor.undo());
    expect(area().value).toBe(GOOD);
    expect(chipOf("PC").getAttribute("aria-pressed")).toBe("false");
  });

  it("does not apply an imported name that the server considers a duplicate", async () => {
    await mount([tpl("Original", ["grp_router"]), tpl("İ", ["grp_pc"])]);
    await loadFile('# @mistgate-template "i"\n# @mistgate-groups []\nmode: rule\n');
    expect(buttonWith(en["sx.hx.m.importApply"]).hasAttribute("disabled")).toBe(true);
    expect(area().value).toBe(GOOD);
    expect(document.querySelector<HTMLInputElement>("#mt-name")?.value).toBe("Original");
    expect(chipOf("ROUTER").getAttribute("aria-pressed")).toBe("true");
  });

  it("accepts an imported name with trailing whitespace and sends the trimmed name", async () => {
    await mount([tpl("Original", ["grp_router"])]);
    await loadFile('# @mistgate-template "Router\\t"\n# @mistgate-groups []\nmode: rule\n');
    expect(buttonWith(en["sx.hx.m.importApply"]).disabled).toBe(false);
    await click(buttonWith(en["sx.hx.m.importApply"]));
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(sent()?.mihomoTemplates).toEqual([tpl("Router", [], "mode: rule\n")]);
  });

  it("maps a valid headerless file with current assignments, and rejects a manually typed unknown name", async () => {
    await mount([tpl("Original", ["grp_router"])]);
    const body = `${GOOD}${marker(["ROUTER"])}\nmode: rule\n${endMarker}\n`;
    await loadFile(body);
    expect(area().value).toBe(body);
    expect(document.querySelector<HTMLInputElement>("#mt-name")?.value).toBe("Original");
    await typeInto(area(), body.replace(marker(["ROUTER"]), marker(["Unknown"])));
    expect(text()).toContain(fill(en["sx.hx.m.source.unknown"], { group: "Unknown" }));
    expect(buttonWith(en["subs.save"]).hasAttribute("disabled")).toBe(true);
    await typeInto(area(), body);
    await click(buttonWith(en["subs.save"]));
    await settle();
    expect(sent()?.mihomoTemplates).toEqual([tpl("Original", ["grp_router"], body.replace(marker(["ROUTER"]), marker(["grp_router"])))]);
  });
});

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
  it("ships an inert group example that can be enabled for an assigned group", () => {
    const starter = starterTemplate(true);
    expect(parseSource(starter).value?.blocks).toHaveLength(0);
    const enabled = starter.replace(/^# (# @mistgate-(?:if|end).*|redir-port: 1182)$/gm, "$1");
    const mapped = mapTransfer(enabled, new Map([["Router", "grp_router"]])).value!;
    const [common, router] = variants(mapped, parseSource(mapped, true).value!, ["grp_router", "grp_pc"]);
    expect(common).not.toMatch(/^redir-port:/m);
    expect(router).toMatch(/^redir-port: 1182$/m);
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
    expect(document.querySelector(".mt-source")).toBeNull();
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
    await debounced();
    expect(text()).toContain(en["sx.hx.m.rBad"]);
  });
  it("takes the cursor to the line the panel names", async () => {
    previewMihomoTemplate.mockRejectedValue(refuse("line 2: could not find expected ':'"));
    await mount([tpl("Draft", ["grp_router"], "a: 1\nmode rule\nb: 2\n")]);
    await debounced();
    await click(document.querySelector("button.mt-ln"));
    expect(area().editor.getSelectedText()).toBe("mode rule");
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
    const pick = (f: File) =>
      act(async () => {
        const input = document.querySelector<HTMLInputElement>("input[type=file]")!;
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
    const box = document.querySelector(".mt-editor")!;
    const drag = (type: string, files: File[] = []) => act(async () => void box.dispatchEvent(Object.assign(new Event(type, { bubbles: true, cancelable: true }), { dataTransfer: { types: ["Files"], files } })));
    await drag("dragover");
    expect(box.classList.contains("drag")).toBe(true);
    await drag("dragleave");
    expect(box.classList.contains("drag")).toBe(false);
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
    expect(preview().getValue()).toContain("de1.example.com");
    expect(preview().getReadOnly()).toBe(true);
    const editable = document.querySelector('.hx-ed .ace_scroller[tabindex="0"]');
    const readOnly = document.querySelector(`${previewSelector} .ace_scroller[tabindex="0"]`);
    expect(editable?.getAttribute("aria-label")).toBe(en["sx.hx.m.text"]);
    expect(readOnly?.getAttribute("aria-label")).toBe(en["sx.hx.m.pvYaml"]);
    expect(readOnly?.getAttribute("aria-describedby")).toBeTruthy();
    expect(text()).toContain(en["sx.hx.m.pvKeys"]);
  });
  it("copies the complete generated YAML even with a fold, and clears the old person's YAML while loading", async () => {
    const router = 'mixed-port: 1080\r\nprofile:\r\n  enable: true # router\r\nurl: "https://example.com/a#b"\r\nlabel: "<img src=x> \u2066name\u2069"\r\n';
    const pc = "mode: rule\n";
    let resolvePC!: () => void;
    const pendingPC = new Promise<void>((resolve) => { resolvePC = resolve; });
    previewMihomoTemplate.mockImplementation(async ({ userId }: { userId: string }) => {
      if (userId === "usr_alice") return reply({ yaml: router });
      await pendingPC;
      return reply({ yaml: pc });
    });
    await mount([tpl("Home", ["grp_router", "grp_pc"])]);
    await debounced();
    const editor = preview();
    expect(editor.getValue()).toBe(router.replace(/\r\n/g, "\n"));
    expect(document.querySelector(`${previewSelector} img`)).toBeNull();
    await act(async () => {
      editor.gotoLine(2);
      editor.session.toggleFoldWidget();
    });
    expect(editor.session.getAllFolds()).toHaveLength(1);
    const original = Object.getOwnPropertyDescriptor(navigator, "clipboard");
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    try {
      await click(buttonWith(en["sx.hx.m.pvCopy"]));
      expect(writeText).toHaveBeenCalledWith(router);
    } finally {
      if (original) Object.defineProperty(navigator, "clipboard", original);
      else Reflect.deleteProperty(navigator, "clipboard");
    }
    await click(buttonWith("bob"));
    expect(document.querySelector(previewSelector)).toBeNull();
    resolvePC();
    await debounced();
    expect(preview().getValue()).toBe(pc);
  });
  it("hides a cached common profile immediately when assignments change", async () => {
    people = [{ id: "usr_alice", name: "alice", groupId: "grp_router" }];
    const yaml = `${GOOD}${marker(["grp_router"])}\nredir-port: 1182\n${endMarker}\n`;
    previewMihomoTemplate.mockImplementation(async ({ groupIds }: { groupIds: string[] }) =>
      reply({ yaml: groupIds.includes("grp_router") ? GOOD + "redir-port: 1182\n" : GOOD }));
    await mount([tpl("Home", ["grp_pc"], yaml)]);
    await click(buttonWith(en["sx.hx.m.pvAnyway"]));
    expect(preview().getValue()).not.toContain("redir-port");
    await click(chipOf("ROUTER"));
    expect(document.querySelector(previewSelector)).toBeNull();
    expect(buttonWith(en["sx.hx.m.pvCopy"])).toBeUndefined();
    await debounced();
    expect(preview().getValue()).toContain("redir-port: 1182");
  });
  it("does not reuse a cached profile after the same person's primary group changes", async () => {
    people = [{ id: "usr_alice", name: "alice", groupId: "grp_router" }];
    const router = GOOD + "redir-port: 1182\n";
    const pc = GOOD + "mixed-port: 1181\n";
    previewMihomoTemplate.mockImplementation(async () => reply({ yaml: people[0]?.groupId === "grp_router" ? router : pc }));
    const qc = await mount([tpl("Home", ["grp_router", "grp_pc"])]);
    expect(preview().getValue()).toBe(router);
    people = [{ id: "usr_alice", name: "alice", groupId: "grp_pc" }];
    await act(async () => void qc.setQueryData(pickerUsersQuery.queryKey, people));
    await settle();
    expect(buttonWith("alice").getAttribute("aria-pressed")).toBe("true");
    expect(preview().getValue()).toBe(pc);
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
    expect(document.querySelector(previewSelector)).toBeNull();
    await click(buttonWith(en["sx.hx.m.pvAnyway"]));
    expect(preview().getValue()).toContain("de1.example.com");
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
    expect(document.querySelector(previewSelector)).toBeNull();
  });
  it("outcome 4: no access or no servers: the panel's own profile, and the reason", async () => {
    previewMihomoTemplate.mockResolvedValue(reply({ notServed: "no_access", yaml: "port: 7890\n" }));
    await mount([tpl("Home routers", ["grp_router"])]);
    await debounced();
    expect(document.querySelector(".mt-res")!.textContent).toBe(en["sx.hx.m.rOff"] + en["sx.hx.m.rOffS"]);
    expect(document.querySelector(previewSelector)).toBeNull();
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
  it("only the plural ones carry a |", () => {
    const keys = Object.keys(en).filter((k) => k.startsWith("subs.mt.") || k.startsWith("sx.hx.m.") || k === "subs.tab.mihomo");
    expect(keys.length).toBeGreaterThan(50);
    for (const lang of ["ru", "zh", "fa", "tr"] as const) {
      for (const k of keys) {
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
