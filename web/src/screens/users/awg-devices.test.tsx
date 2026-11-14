import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import type { DeviceConfig } from "@/gen/mistgate/admin/v1/device_pb";
import { en } from "@/i18n/en";
import { DeviceConfigsModal, type DeviceRef } from "./awg-devices";

// fake key material only
const config = (over: Partial<DeviceConfig>) =>
  ({
    inboundId: "inb_awg", nodeId: "nod_de1", nodeName: "de1", countryCode: "DE", profileName: "AWG 3.1", awgVersion: "3.1", conf: "[Interface]\nPrivateKey = direct\n", vpnKey: "vpn://direct",
    stale: false, minClients: [], warnings: [], confFilename: "de1-31.conf", viaCountry: "", viaNodeId: "", ...over,
  }) as unknown as DeviceConfig;
const direct = config({});
const throughRu = config({ conf: "[Interface]\nPrivateKey = via\n", vpnKey: "vpn://via", confFilename: "de1-31-via-ru.conf", viaCountry: "RU", viaNodeId: "nod_msk1" });
const device: DeviceRef = { id: "dev_1", label: "Pixel", platform: "android", profileName: "AWG 3.1", version: "3.1", address: "10.66.4.5", stale: false };

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<object>()), devices: { getDeviceConfigs: vi.fn() }, users: { revokeDevice: vi.fn() } }));
vi.mock("@/components/qr-code", () => ({ QrCode: ({ value }: { value: string }) => <div data-testid="qr" data-value={value} /> }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(initial: DeviceConfig[]) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <DeviceConfigsModal device={device} initial={initial} onClose={() => {}} onChanged={() => {}} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
}
const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 20))));
};
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]")!;
const qr = () => dialog().querySelector<HTMLElement>("[data-testid=qr]")!.dataset.value;

describe("the configs of an AmneziaWG device", () => {
  it("the direct config alone is as before: no note about a path", async () => {
    await mount([direct]);
    expect(dialog().textContent).toContain("de1");
    expect(dialog().textContent).not.toContain("separate key");
    expect(qr()).toContain("direct");
  });

  it("a config through an entry shares the node of the direct one and is picked apart from it, with its own QR code, and says once what it is", async () => {
    await mount([direct, throughRu]);
    // the direct config first, with no note
    expect(qr()).toContain("direct");
    expect(dialog().textContent).not.toContain("separate key");
    const trigger = dialog().querySelector<HTMLElement>('button[aria-label="Node"]')!;
    expect(trigger.textContent).toContain("de1");
    expect(trigger.textContent).not.toContain("via RU");
    await act(async () => void trigger.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    await settle();
    const options = [...document.querySelectorAll<HTMLElement>("[role=option]")];
    expect(options.map((o) => o.textContent)).toEqual(["de1", "de1 · via RU"]);
    await act(async () => options[1]!.click());
    await settle();
    expect(qr()).toContain("via");
    const note = en["awg.cfg.viaNote"].replace("{via}", "via RU");
    expect(dialog().textContent?.split(note).length).toBe(2);
    expect(dialog().querySelector('button[aria-label="Node"]')!.textContent).toContain("de1 · via RU");
  });
});
