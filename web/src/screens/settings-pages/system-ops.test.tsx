import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { en } from "@/i18n/en";
import { OpsLogs, OpsSide } from "./system-ops";

// Settings -> System: the panel's logs, retention, export and metrics cards.

const auth = vi.hoisted(() => ({ me: vi.fn() }));
const ops = vi.hoisted(() => ({
  getLogSettings: vi.fn(),
  setLogLevel: vi.fn(),
  getPanelLogs: vi.fn(),
  getRetention: vi.fn(),
  setRetention: vi.fn(),
  getMetricsSettings: vi.fn(),
  setMetricsSettings: vi.fn(),
  exportData: vi.fn(),
}));
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), auth, ops }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let role = Role.OWNER;
let root: Root | null = null;
let host: HTMLDivElement | null = null;
const items = [
  { key: "journal_days", value: 7, defaultValue: 7, min: 1, max: 7, unit: "days" },
  { key: "audit_days", value: 400, defaultValue: 400, min: 30, max: 1095, unit: "days" },
  { key: "check_samples_hours", value: 25, defaultValue: 25, min: 25, max: 168, unit: "hours" },
];
beforeEach(() => {
  role = Role.OWNER;
  auth.me.mockImplementation(async () => ({ admin: { id: "adm_1", displayName: "Alice", role } }));
  ops.getLogSettings.mockResolvedValue({ level: "info", defaultLevel: "info", format: "text", buffered: 2, capacity: 5000 });
  ops.getPanelLogs.mockResolvedValue({
    level: "info",
    buffered: 2,
    capacity: 5000,
    nextCursor: 0n,
    entries: [
      { seq: 2n, timeUnixMs: 1_700_000_100_000n, level: "warn", component: "fleet", message: "dial failed to <ip>:port", code: "dial_failed", requestId: "abc12345", attrs: { node: "de1", code: "dial_failed" } },
      { seq: 1n, timeUnixMs: 1_700_000_000_000n, level: "info", component: "health", message: "sweep done", code: "", requestId: "", attrs: {} },
    ],
  });
  ops.getRetention.mockResolvedValue({ items });
  ops.setRetention.mockResolvedValue({ items });
  ops.getMetricsSettings.mockResolvedValue({ enabled: false, hasToken: false });
  ops.setMetricsSettings.mockResolvedValue({ enabled: true, hasToken: true, token: "mgm_tokenshownonce" });
  ops.setLogLevel.mockResolvedValue({ level: "debug" });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  auth.me.mockReset();
  Object.values(ops).forEach((m) => m.mockReset());
});

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <OpsLogs />
          <OpsSide />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await settle();
}
const text = () => document.body.textContent ?? "";
// a toast is a dialog too, a modeless one
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]:not([aria-modal=false])");
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label) as HTMLButtonElement | undefined;
const click = async (b: Element | undefined | null) => {
  await act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 3; i++) await settle();
};
async function type(el: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("Settings → System → the panel's diagnostics", () => {
  it("shows the logs with their code and request code, and the four cards, to the owner", async () => {
    await mount();
    for (const title of [en["obs.logs.title"], en["obs.ret.title"], en["obs.exp.title"], en["obs.met.title"]]) expect(text()).toContain(title);
    expect(text()).toContain("dial failed to <ip>:port");
    expect(text()).toContain("code=dial_failed");
    expect(text()).toContain("request=abc12345");
    expect(text()).toContain("sweep done");
    expect(text()).toContain("Records held: 2 of 5000");
    expect(text()).toContain("Log level: “Info” until the panel restarts, then it goes back to “Info”");
    expect(ops.getPanelLogs.mock.calls[0]?.[0]).toMatchObject({ minLevel: "info", limit: 100 });
  });

  it("gives the log level its own row, the four choices sharing it (the board's .us-seg), and the search below", async () => {
    await mount();
    const levels = document.querySelector<HTMLElement>(`[role=radiogroup][aria-label="${en["obs.logs.show"]}"]`)!;
    expect(levels.className).toContain("w-full");
    expect(levels.className).toContain("[&>*]:flex-1");
    expect(levels.querySelectorAll("[role=radio]")).toHaveLength(4);
  });

  it("shows nothing to a helper: the procedures are the owner's", async () => {
    role = Role.HELPER;
    await mount();
    expect(text()).not.toContain(en["obs.logs.title"]);
    expect(ops.getLogSettings).not.toHaveBeenCalled();
  });

  it("says once that the Cloudflare edition has none of it", async () => {
    ops.getLogSettings.mockRejectedValue(new ConnectError("not available on this edition", Code.Unimplemented));
    await mount();
    expect(text()).toContain(en["obs.unavailable"]);
    expect(text()).not.toContain(en["obs.ret.title"]);
  });

  it("filters ask the panel, with the text cut to 100 characters", async () => {
    await mount();
    const search = [...document.querySelectorAll<HTMLInputElement>("input")].find((i) => i.maxLength === 100)!;
    await type(search, "dial");
    for (let i = 0; i < 3; i++) await settle();
    expect(ops.getPanelLogs.mock.calls.at(-1)?.[0]).toMatchObject({ query: "dial" });
  });

  it("saves only the retention fields that changed, and keeps Save off for a value outside its limits", async () => {
    await mount();
    // the retention fields are the numeric inputs, in the order the panel lists them: journal, audit, raw rounds
    const field = (label: string) => document.querySelectorAll<HTMLInputElement>('input[inputmode="numeric"]')[label === en["obs.ret.f.audit_days"] ? 1 : 0]!;
    const save = () => button(en["obs.ret.save"])!;
    expect(save().disabled).toBe(true);
    await type(field(en["obs.ret.f.audit_days"]), "10"); // below 30
    expect(save().disabled).toBe(true);
    await type(field(en["obs.ret.f.audit_days"]), "100");
    await click(save());
    expect(ops.setRetention).toHaveBeenCalledTimes(1);
    expect(ops.setRetention.mock.calls[0]?.[0]).toEqual({ values: { audit_days: 100 } });
  });

  it("shows a new metrics token once, in a window the Escape key does not close", async () => {
    await mount();
    await click(document.querySelector<HTMLElement>('[role=switch][aria-label="Serve /metrics"]'));
    expect(ops.setMetricsSettings.mock.calls[0]?.[0]).toMatchObject({ enabled: true });
    expect(dialog()?.textContent).toContain("mgm_tokenshownonce");
    expect(text()).toContain(en["sx.sys.met.made"]);
    await act(async () => void document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(text()).toContain("mgm_tokenshownonce");
    await click(button(en["sx.sys.met.done"]));
    expect(dialog()).toBeNull();
  });

  it("asks before a new token replaces the old one, then makes it", async () => {
    ops.getMetricsSettings.mockResolvedValue({ enabled: true, hasToken: true });
    ops.setMetricsSettings.mockResolvedValue({ enabled: true, hasToken: true, token: "mgm_second" });
    await mount();
    expect(text()).toContain(en["sx.sys.met.tok"]);
    await click(button(en["obs.met.rotate"]));
    expect(dialog()?.textContent).toContain(en["sx.sys.met.dlg.body"]);
    expect(ops.setMetricsSettings).not.toHaveBeenCalled();
    await click(button(en["sx.sys.met.make"]));
    expect(ops.setMetricsSettings.mock.calls[0]?.[0]).toMatchObject({ rotateToken: true });
    expect(dialog()?.textContent).toContain("mgm_second");
  });

  it("warns that a shorter time deletes rows for good, and only then", async () => {
    await mount();
    const audit = document.querySelectorAll<HTMLInputElement>('input[inputmode="numeric"]')[1]!;
    await type(audit, "500");
    expect(text()).not.toContain(en["sx.sys.ret.warn"]);
    await type(audit, "100");
    expect(text()).toContain(en["sx.sys.ret.warn"]);
  });

  it("downloads the period chosen, 30 days being 720 hours", async () => {
    const urls = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:export");
    ops.exportData.mockImplementation(async function* () {
      yield { data: new Uint8Array([123, 125, 10]) };
    });
    await mount();
    await click([...document.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent === en["sx.sys.exp.30"]));
    await click(button(en["obs.exp.download"]));
    const req = ops.exportData.mock.calls[0]?.[0] as { sinceUnix: bigint; untilUnix: bigint };
    expect(Number(req.untilUnix - req.sinceUnix)).toBe(720 * 3600);
    expect(urls).toHaveBeenCalled();
    urls.mockRestore();
  });

  it("filters the logs by level and writes the level the panel logs at", async () => {
    await mount();
    await click([...document.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent === "Errors"));
    expect(ops.getPanelLogs.mock.calls.at(-1)?.[0]).toMatchObject({ minLevel: "error" });
    await click(button(en["sx.sys.logs.more"]));
    expect(text()).toContain(en["obs.logs.level"]);
    expect(text()).toContain(en["obs.logs.window"]);
  });
});
