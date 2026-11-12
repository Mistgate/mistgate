import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { DeviceKind } from "@/gen/mistgate/admin/v1/user_pb";
import { setLang } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { meQuery } from "@/lib/session";
import type { DeviceN } from "./model";
import { DevicesPanel } from "./user-detail";

// The person's page, Devices: for the owner on the VPS edition, a device of the Mistgate app can be asked for its logs, says so in a
// badge, and a list of what came sits under the devices with the viewer behind "Open".

const ops = vi.hoisted(() => ({
  listClientDebug: vi.fn(),
  requestClientDebug: vi.fn(),
  cancelClientDebug: vi.fn(),
  deleteClientDebug: vi.fn(),
  getClientDebug: vi.fn(),
  exportClientDebug: vi.fn(),
}));
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  ops,
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { apps: [] }, effectiveTitle: "" }) },
  profiles: { listProtocols: () => Promise.resolve({ protocols: [] }) },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children?: ReactNode; to: string }) => <a href={to}>{children}</a>,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  setLang("en");
  for (const f of Object.values(ops)) f.mockReset();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

const NOW = Math.floor(Date.now() / 1000);
const owner = { admin: { id: "adm_1", role: Role.OWNER } };

const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function mount(devices: DeviceN[], me: object = owner) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQuery.queryKey, me as never);
  const user = { id: "usr_alice", name: "alice-liddell", groupName: "all", deviceLimit: 5, devicesUsed: devices.length, apps: { happ: true, amnezia: false } } as never;
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <DevicesPanel user={user} devices={devices} profiles={[]} actions={{ refresh: vi.fn() } as never} onEditGroup={vi.fn()} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const device = (id: string, model: string, over: object = {}) =>
  ({ id, kind: DeviceKind.LINK, model, platform: "windows", client: "", osVersion: "", awgProfileId: "", onlineNodeIds: [], firstSeenUnix: 0, lastSeenUnix: 0, lastHandshakeUnix: 0, usedBytes: 0, enrolled: true, enrolLinkStale: false, ...over }) as unknown as DeviceN;
const logInfo = (over: Record<string, unknown>) => ({
  id: "dbr_1", userId: "usr_alice", userName: "alice-liddell", deviceId: "dev_pc", devicePlatform: "windows", deviceModel: "Office PC", state: "open", level: "info", note: "", unasked: false,
  actor: "adm_owner", requestedUnix: BigInt(NOW - 3600), expiresUnix: BigInt(NOW + 3600), closedUnix: 0n, records: 0, bytes: 0, droppedAttrs: 0, firstUnixMs: 0n, lastUnixMs: 0n, app: "", readUnix: 0n, ...over,
});
const rec = (seq: number, level: string, source: string, message: string, attrs: Record<string, string> = {}) => ({ seq: BigInt(seq), timeUnixMs: BigInt(1_700_000_000_000 + seq * 1000), level, source, message, attrs });

const text = () => document.body.textContent ?? "";
const row = (name: string) => [...document.querySelectorAll("li")].find((li) => li.textContent?.startsWith(name));
const logRow = (name: string, state: string) => [...document.querySelectorAll("[data-shot=client-logs-list] li")].find((li) => li.textContent?.startsWith(name) && li.textContent.includes(state));
const button = (label: string, within: ParentNode = document) => [...within.querySelectorAll<HTMLElement>("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
const dialog = () => [...document.querySelectorAll("[role=dialog]")].at(-1)!;
const click = async (el: Element | null | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await settle();
};
/** Base UI opens a menu on the press, not on the click. */
const press = async (el: Element | null | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => {
    for (const type of ["pointerdown", "mousedown"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
    for (const type of ["pointerup", "mouseup", "click"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
  });
  await settle();
};
const menu = (name: string) => press(document.querySelector(`button[aria-label='Actions for ${name}']`));
const items = () => [...document.querySelectorAll('[role="menuitem"]')].map((i) => i.textContent);
const item = (label: string) => click([...document.querySelectorAll('[role="menuitem"]')].find((i) => i.textContent === label));
const type = async (input: HTMLInputElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await settle();
};
const badgeOf = (name: string) => [...(row(name)?.querySelectorAll("[aria-label]") ?? [])].find((e) => /^Logs |^Declined/.test(e.textContent ?? ""));

describe("asking a device for its logs", () => {
  it("offers it in the menu of a device with a key only, and not at all to a helper or where the edition has none", async () => {
    ops.listClientDebug.mockResolvedValue({ items: [] });
    await mount([device("dev_pc", "Office PC"), device("dev_plain", "Plain PC", { enrolled: false })]);
    await menu("Office PC");
    expect(items()).toEqual(["Request logs", "Rename…", "Delete…", "Block…"]);
    await press(document.body); // closes it
    await menu("Plain PC");
    expect(items()).toEqual(["Rename…", "Delete…", "Block…"]);
  });

  it("does not ask the panel, or show anything, for a helper", async () => {
    ops.listClientDebug.mockResolvedValue({ items: [logInfo({})] });
    await mount([device("dev_pc", "Office PC")], { admin: { id: "adm_2", role: Role.HELPER } });
    expect(ops.listClientDebug).not.toHaveBeenCalled();
    expect(text()).not.toContain(en["clientlogs.title"]);
    expect(badgeOf("Office PC")).toBeUndefined();
    await menu("Office PC");
    expect(items()).not.toContain("Request logs");
  });

  it("draws nothing on the Cloudflare edition, where OpsService answers UNIMPLEMENTED", async () => {
    ops.listClientDebug.mockRejectedValue(new ConnectError("not available on this edition", Code.Unimplemented));
    await mount([device("dev_pc", "Office PC")]);
    expect(text()).not.toContain(en["clientlogs.title"]);
    await menu("Office PC");
    expect(items()).not.toContain("Request logs");
  });

  it("sends the note and the detailed switch (off by default) and says so", async () => {
    ops.listClientDebug.mockResolvedValue({ items: [] });
    ops.requestClientDebug.mockResolvedValue({ request: logInfo({}) });
    await mount([device("dev_pc", "Office PC")]);
    await menu("Office PC");
    await item("Request logs");
    expect(dialog().textContent).toContain("Request logs: Office PC");
    expect(dialog().textContent).toContain(en["clientlogs.ttl"]);
    expect(dialog().textContent).toContain(en["clientlogs.noteHint"]);
    const note = dialog().querySelector<HTMLInputElement>("input")!;
    expect(note.maxLength).toBe(200);
    const detailed = dialog().querySelector("[role=switch]")!;
    expect(detailed.getAttribute("aria-checked")).toBe("false");
    await type(note, "  The tunnel drops in the evening  ");
    await click(button(en["clientlogs.send"], dialog()));
    expect(ops.requestClientDebug).toHaveBeenCalledWith({ deviceId: "dev_pc", note: "The tunnel drops in the evening", level: "info" });
    expect(text()).toContain(en["clientlogs.asked"]);

    await menu("Office PC");
    await item("Request logs");
    await click(dialog().querySelector("[role=switch]"));
    await click(button(en["clientlogs.send"], dialog()));
    expect(ops.requestClientDebug).toHaveBeenLastCalledWith({ deviceId: "dev_pc", note: "", level: "debug" });
  });

  it("keeps the window open and says why when the panel refuses", async () => {
    ops.listClientDebug.mockResolvedValue({ items: [] });
    ops.requestClientDebug.mockRejectedValue(new ConnectError("this device already has an open request", Code.FailedPrecondition));
    await mount([device("dev_pc", "Office PC")]);
    await menu("Office PC");
    await item("Request logs");
    await click(button(en["clientlogs.send"], dialog()));
    expect(dialog().textContent).toContain("Request logs: Office PC");
  });
});

describe("the badge beside a device", () => {
  it("is yellow while a request is open, with who, when and until when, and the menu then withdraws it", async () => {
    ops.listClientDebug.mockResolvedValue({ items: [logInfo({})] });
    ops.cancelClientDebug.mockResolvedValue({ request: logInfo({ state: "cancelled" }) });
    await mount([device("dev_pc", "Office PC")]);
    const badge = badgeOf("Office PC")!;
    expect(badge.textContent).toBe("Logs requested");
    expect(badge.className).toContain("tone-warn");
    expect(badge.getAttribute("aria-label")).toMatch(/Asked by adm_owner, .*\. Open until /);
    await menu("Office PC");
    expect(items()).toEqual(["Cancel the request", "Rename…", "Delete…", "Block…"]);
    await item("Cancel the request");
    expect(ops.cancelClientDebug).toHaveBeenCalledWith({ id: "dbr_1" });
  });

  it("is green when the logs came, and opens the viewer when pressed; red when the person declined", async () => {
    ops.listClientDebug.mockResolvedValue({
      items: [
        logInfo({ id: "dbr_mac", deviceId: "dev_mac", deviceModel: "MacBook Air", devicePlatform: "macos", state: "received", records: 2, closedUnix: BigInt(NOW - 600), app: "Mistgate/0.3.1", firstUnixMs: 1_700_000_001_000n, lastUnixMs: 1_700_000_002_000n }),
        logInfo({ id: "dbr_phone", deviceId: "dev_phone", deviceModel: "iPhone 15", devicePlatform: "ios", state: "declined", closedUnix: BigInt(NOW - 60) }),
      ],
    });
    ops.getClientDebug.mockResolvedValue({ bundle: {}, records: [rec(1, "info", "service", "connecting"), rec(2, "warn", "tunnel", "handshake timed out")], nextCursor: 0n, sources: ["service", "tunnel"] });
    await mount([device("dev_mac", "MacBook Air", { platform: "macos" }), device("dev_phone", "iPhone 15", { platform: "ios" })]);
    expect(badgeOf("iPhone 15")!.textContent).toBe("Declined");
    expect(badgeOf("iPhone 15")!.className).toContain("tone-bad");
    const got = badgeOf("MacBook Air")!;
    expect(got.textContent).toBe("Logs received");
    expect(got.className).toContain("tone-ok");
    await click(got);
    expect(ops.getClientDebug).toHaveBeenCalledWith(expect.objectContaining({ id: "dbr_mac", minLevel: "", source: "", query: "", fromMs: 0n, toMs: 0n, afterSeq: 0n }));
    expect(dialog().textContent).toContain("Logs: MacBook Air");
    expect(dialog().textContent).toContain("connecting");
    expect(dialog().textContent).toContain("handshake timed out");
  });
});

describe("the list of logs for support", () => {
  const items4 = () => [
    logInfo({ id: "dbr_open", state: "open" }),
    logInfo({ id: "dbr_got", deviceId: "dev_mac", deviceModel: "MacBook Air", devicePlatform: "macos", state: "received", records: 40, closedUnix: BigInt(NOW - 600), app: "Mistgate/0.3.1", note: "Please <b>send</b> https://example.com/x" }),
    logInfo({ id: "dbr_own", deviceId: "dev_phone", deviceModel: "iPhone 15", devicePlatform: "ios", state: "received", unasked: true, actor: "", records: 22, closedUnix: BigInt(NOW - 86_400), app: "Mistgate/0.3.0" }),
    logInfo({ id: "dbr_no", state: "declined", closedUnix: BigInt(NOW - 3 * 86_400), requestedUnix: BigInt(NOW - 3 * 86_400) }),
  ];
  const devices = () => [device("dev_pc", "Office PC"), device("dev_mac", "MacBook Air", { platform: "macos" }), device("dev_phone", "iPhone 15", { platform: "ios" })];

  it("shows one row per request with a pill for what became of it, and only the actions that fit", async () => {
    ops.listClientDebug.mockResolvedValue({ items: items4() });
    await mount(devices());
    expect(text()).toContain(en["clientlogs.title"]);
    expect(logRow("Office PC", "Requested")!.textContent).toContain("Asked by adm_owner");
    expect(button("Cancel the request", logRow("Office PC", "Requested")!)).toBeTruthy();
    expect(button("Open", logRow("Office PC", "Requested")!)).toBeUndefined();
    const got = logRow("MacBook Air", "Received")!;
    expect(got.textContent).toContain("40 lines");
    expect(got.textContent).toContain("Mistgate 0.3.1");
    expect(button("Open", got)).toBeTruthy();
    const own = logRow("iPhone 15", "Sent on their own")!;
    expect(own.textContent).toContain("22 lines");
    expect(button("Open", own)).toBeTruthy();
    const declined = logRow("Office PC", "Declined")!;
    expect(button("Open", declined)).toBeUndefined();
    expect(button("Cancel the request", declined)).toBeUndefined();
  });

  it("scrolls to itself when the page was opened at #client-logs (the link of an event), and not otherwise", async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    ops.listClientDebug.mockResolvedValue({ items: items4() });
    await mount(devices());
    expect(document.getElementById("client-logs")).toBeTruthy();
    expect(scroll).not.toHaveBeenCalled();
    act(() => root?.unmount());
    host?.remove();
    window.location.hash = "#client-logs";
    await mount(devices());
    window.location.hash = "";
    expect(scroll).toHaveBeenCalledTimes(1);
    expect(scroll.mock.contexts[0]).toBe(document.getElementById("client-logs"));
  });

  it("draws nothing for a person with no requests and no logs, though the menu can still ask", async () => {
    ops.listClientDebug.mockResolvedValue({ items: [] });
    await mount(devices());
    expect(text()).not.toContain(en["clientlogs.title"]);
  });

  it("speaks Russian in the pills", async () => {
    setLang("ru");
    ops.listClientDebug.mockResolvedValue({ items: items4() });
    await mount(devices());
    expect(text()).toContain(ru["clientlogs.title"]);
    for (const k of ["clientlogs.state.open", "clientlogs.state.received", "clientlogs.state.unasked", "clientlogs.state.declined"] as const) expect(text()).toContain(ru[k]);
  });

  it("deletes a bundle only after asking, and a request that is open only by withdrawing it", async () => {
    ops.listClientDebug.mockResolvedValue({ items: items4() });
    ops.deleteClientDebug.mockResolvedValue({});
    await mount(devices());
    const got = logRow("MacBook Air", "Received")!;
    await press(got.querySelector("button[aria-label^='Actions for the logs']"));
    expect(items()).toEqual(["Download", "Delete"]);
    await item("Delete");
    expect(dialog().textContent).toContain(en["clientlogs.deleteT"]);
    expect(dialog().textContent).toContain(en["clientlogs.deleteBody"]);
    expect(ops.deleteClientDebug).not.toHaveBeenCalled();
    await click(button(en["clientlogs.delete"], dialog()));
    expect(ops.deleteClientDebug).toHaveBeenCalledWith({ id: "dbr_got" });
    // the open one has no menu with Delete
    expect(logRow("Office PC", "Requested")!.querySelector("button[aria-label^='Actions for the logs']")).toBeNull();
    // a refused request asks less: no mention of backups
    await press(logRow("Office PC", "Declined")!.querySelector("button[aria-label^='Actions for the logs']"));
    await item("Delete");
    expect(dialog().textContent).toContain(en["clientlogs.deleteEntryT"]);
  });

  it("opens the bundle, shows what the owner asked and what the person wrote as plain text, and downloads it", async () => {
    ops.listClientDebug.mockResolvedValue({ items: items4() });
    ops.getClientDebug.mockResolvedValue({ bundle: {}, records: [rec(1, "info", "service", "<script>alert(1)</script> **bold** https://example.com")], nextCursor: 0n, sources: ["service"] });
    ops.exportClientDebug.mockImplementation(async function* () {
      yield { data: new TextEncoder().encode('{"seq":1}\n') };
    });
    URL.createObjectURL = vi.fn(() => "blob:x");
    URL.revokeObjectURL = vi.fn();
    const saved: string[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      saved.push(this.download);
    });
    await mount(devices());
    await click(button("Open", logRow("MacBook Air", "Received")!));
    const d = dialog();
    expect(d.textContent).toContain("Asked by adm_owner");
    // the note and the records are text nodes: nothing in them became an element or a link
    expect(d.textContent).toContain("Please <b>send</b> https://example.com/x");
    expect(d.textContent).toContain("<script>alert(1)</script> **bold** https://example.com");
    expect(d.querySelector("script, b, strong, a")).toBeNull();
    await click(button(en["clientlogs.download"], d));
    expect(ops.exportClientDebug).toHaveBeenCalledWith({ id: "dbr_got" });
    expect(saved).toEqual(["mistgate-client-logs-dbr_got.ndjson"]);
  });

  it("says that a bundle was sent on its own, and how many fields were dropped", async () => {
    ops.listClientDebug.mockResolvedValue({ items: [items4()[2]!, { ...items4()[1]!, droppedAttrs: 3 }] });
    ops.getClientDebug.mockResolvedValue({ bundle: {}, records: [], nextCursor: 0n, sources: [] });
    await mount(devices());
    await click(button("Open", logRow("iPhone 15", "Sent on their own")!));
    expect(dialog().textContent).toContain(en["clientlogs.unaskedNote"]);
    expect(dialog().textContent).toContain(en["debug.empty"]);
  });
});

describe("the viewer's filters", () => {
  const bundle = logInfo({ id: "dbr_got", deviceId: "dev_mac", deviceModel: "MacBook Air", devicePlatform: "macos", state: "received", records: 3, closedUnix: BigInt(NOW - 600), firstUnixMs: 1_700_000_001_000n, lastUnixMs: 1_700_000_900_000n });
  const page = (records: ReturnType<typeof rec>[], next = 0n) => ({ bundle: {}, records, nextCursor: next, sources: ["service", "tunnel"] });
  async function open() {
    ops.listClientDebug.mockResolvedValue({ items: [bundle] });
    await mount([device("dev_mac", "MacBook Air", { platform: "macos" })]);
    await click(button("Open", logRow("MacBook Air", "Received")!));
  }
  const inputs = () => [...dialog().querySelectorAll<HTMLInputElement>("input[type=datetime-local]")];

  it("asks the panel for the level, the source and the text, and walks the cursor", async () => {
    ops.getClientDebug.mockResolvedValueOnce(page([rec(1, "info", "service", "one")], 1n)).mockResolvedValue(page([rec(2, "warn", "tunnel", "two")]));
    await open();
    await click(button("More records", dialog()));
    expect(ops.getClientDebug).toHaveBeenLastCalledWith(expect.objectContaining({ afterSeq: 1n }));
    expect(dialog().textContent).toContain("one");
    expect(dialog().textContent).toContain("two");
    await click([...dialog().querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent === en["debug.filter.warn"]));
    expect(ops.getClientDebug).toHaveBeenLastCalledWith(expect.objectContaining({ minLevel: "warn", afterSeq: 0n }));
  });

  it("has a from and a to bounded by the bundle, and sends them as milliseconds", async () => {
    ops.getClientDebug.mockResolvedValue(page([rec(1, "info", "service", "one")]));
    await open();
    const [from, to] = inputs();
    expect(from!.min).toBe(to!.min);
    expect(from!.max).toBe(to!.max);
    expect(new Date(from!.min).getTime()).toBe(1_700_000_001_000);
    expect(new Date(from!.max).getTime()).toBe(1_700_000_900_000);
    await type(from!, from!.min);
    expect(ops.getClientDebug).toHaveBeenLastCalledWith(expect.objectContaining({ fromMs: 1_700_000_001_000n, toMs: 0n }));
    await type(to!, to!.max);
    // the end of the window includes its last second
    expect(ops.getClientDebug).toHaveBeenLastCalledWith(expect.objectContaining({ fromMs: 1_700_000_001_000n, toMs: 1_700_000_900_999n }));
    await click(button(en["clientlogs.wholeBundle"], dialog()));
    expect(ops.getClientDebug).toHaveBeenLastCalledWith(expect.objectContaining({ fromMs: 0n, toMs: 0n }));
  });
});

describe("the words", () => {
  it("has every sentence of logs for support in all five languages with the same placeholders", async () => {
    const { dictionaries } = await import("@/i18n");
    const keys = Object.keys(en).filter((k) => k.startsWith("clientlogs.") || k.startsWith("event.client_logs_") || /^audit\.client_debug_(read|downloaded|deleted)$/.test(k));
    expect(keys.length).toBeGreaterThan(40);
    const holes = (s: string) => [...new Set(s.match(/\{\w+\}/g) ?? [])].sort().join();
    for (const lang of ["ru", "zh", "fa", "tr"] as const) {
      for (const k of keys) {
        const other = (dictionaries[lang] as Record<string, string>)[k];
        expect(other, `${lang} ${k}`).toBeTruthy();
        expect(holes(other!), `${lang} ${k}`).toBe(holes((en as Record<string, string>)[k]!));
      }
    }
  });
});
