import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { en } from "@/i18n/en";
import { OpsCards } from "./system-ops";

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
          <OpsCards />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await settle();
}
const text = () => document.body.textContent ?? "";
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
    expect(text()).toContain("dial_failed");
    expect(text()).toContain("abc12345");
    expect(text()).toContain("sweep done");
    expect(text()).toContain("2 of 5000 records held");
    expect(ops.getPanelLogs.mock.calls[0]?.[0]).toMatchObject({ minLevel: "info", limit: 100 });
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

  it("shows a new metrics token once", async () => {
    await mount();
    await click(document.querySelector<HTMLElement>('[role=switch][aria-label="Serve /metrics"]'));
    expect(ops.setMetricsSettings.mock.calls[0]?.[0]).toMatchObject({ enabled: true });
    expect(text()).toContain("mgm_tokenshownonce");
    expect(text()).toContain(en["obs.met.tokenOnce"]);
  });
});
