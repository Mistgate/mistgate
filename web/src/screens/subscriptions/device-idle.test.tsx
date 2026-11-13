import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { setLang } from "@/i18n";
import { DeviceIdleCard, idleDays, parseIdleDays } from "./device-idle";
import type { Settings } from "./model";

// "Inactive devices": the switch and the number of days beside the device limit mode, saved through the settings call.

const getSubscriptionSettings = vi.fn();
const updateSubscriptionSettings = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  subscriptions: { getSubscriptionSettings: (...a: unknown[]) => getSubscriptionSettings(...a), updateSubscriptionSettings: (...a: unknown[]) => updateSubscriptionSettings(...a) },
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  setLang("en");
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [getSubscriptionSettings, updateSubscriptionSettings]) m.mockReset();
});

const settings = (days: number | undefined) => ({ title: "", rules: [], apps: [], ...(days === undefined ? {} : { deviceIdleAfterDays: days }) }) as unknown as Settings;

async function mount(days: number | undefined) {
  getSubscriptionSettings.mockResolvedValue({ settings: settings(days), effectiveTitle: "", sharedLinkUsers: 0 });
  updateSubscriptionSettings.mockImplementation(async (r: { settings: Settings }) => ({ settings: r.settings }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <DeviceIdleCard settings={settings(days)} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => host!.textContent ?? "";
const input = () => host!.querySelector<HTMLInputElement>("input[type=number]");
const toggle = () => host!.querySelector<HTMLElement>("[role=switch]")!;
const click = (el: Element) => act(async () => void el.dispatchEvent(new MouseEvent("click", { bubbles: true })));
/** Types into a React-controlled input the way a person does. */
async function type(el: HTMLInputElement, value: string) {
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    set.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const leave = (el: HTMLElement) => act(async () => void el.dispatchEvent(new FocusEvent("focusout", { bubbles: true })));
const saved = () => (updateSubscriptionSettings.mock.calls.at(-1)?.[0] as { settings: Settings } | undefined)?.settings.deviceIdleAfterDays;

describe("the days of an inactive device", () => {
  it("reads 30 when the panel has not said (the default), 0 as off", () => {
    expect(idleDays({})).toBe(30);
    expect(idleDays({ deviceIdleAfterDays: 0 })).toBe(0);
    expect(idleDays({ deviceIdleAfterDays: 90 })).toBe(90);
  });

  it("accepts a whole number from 7 to 365 and nothing else", () => {
    for (const ok of ["7", "30", "365", " 45 "]) expect(parseIdleDays(ok), ok).not.toBeNull();
    for (const bad of ["", "6", "366", "0", "12.5", "abc", "-8"]) expect(parseIdleDays(bad), bad).toBeNull();
  });
});

describe("the inactive devices card", () => {
  it("says what the number does, and that nothing is deleted", async () => {
    await mount(30);
    expect(text()).toContain("Inactive devices");
    expect(text()).toContain("A device with no connections for 30 days does not take a place in the limit.");
    expect(text()).toContain("The device is not deleted and its keys keep working. If it comes back, it counts again, as long as there is room.");
    expect(toggle().getAttribute("aria-checked")).toBe("true");
    expect(input()?.value).toBe("30");
  });

  it("is on at 30 days for a panel that says nothing", async () => {
    await mount(undefined);
    expect(toggle().getAttribute("aria-checked")).toBe("true");
    expect(input()?.value).toBe("30");
  });

  it("says it is off with no field, and keeps the explanation", async () => {
    await mount(0);
    expect(toggle().getAttribute("aria-checked")).toBe("false");
    expect(input()).toBeNull();
    expect(text()).toContain("Off. A device that is no longer used keeps its place until you delete it.");
    expect(text()).toContain("its keys keep working");
  });

  it("saves 0 when switched off, and 30 when switched on again", async () => {
    await mount(45);
    await click(toggle());
    await settle();
    expect(saved()).toBe(0);
    act(() => root?.unmount());
    host!.remove();
    await mount(0);
    await click(toggle());
    await settle();
    expect(saved()).toBe(30);
  });

  it("saves a new number when the field is left, and says nothing is wrong", async () => {
    await mount(30);
    await type(input()!, "60");
    expect(updateSubscriptionSettings).not.toHaveBeenCalled();
    await leave(input()!);
    await settle();
    expect(saved()).toBe(60);
    expect(text()).not.toContain("Enter a whole number");
  });

  it("refuses a number the panel would refuse, with the range, and does not save it", async () => {
    await mount(30);
    for (const bad of ["3", "400", "", "2.5"]) {
      await type(input()!, bad);
      expect(text(), bad).toContain("Enter a whole number from 7 to 365.");
      await leave(input()!);
      await settle();
    }
    expect(updateSubscriptionSettings).not.toHaveBeenCalled();
  });

  it("does not save the number that is already saved", async () => {
    await mount(30);
    await type(input()!, "30");
    await leave(input()!);
    await settle();
    expect(updateSubscriptionSettings).not.toHaveBeenCalled();
  });

  it("is worded in Russian with the right number form", async () => {
    setLang("ru");
    await mount(21);
    expect(text()).toContain("Неактивные устройства");
    expect(text()).toContain("не подключалось 21 день");
    expect(text()).toContain("Устройство не удаляется, его ключи продолжают работать.");
  });
});
