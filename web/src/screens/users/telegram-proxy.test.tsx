import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { DeviceKind } from "@/gen/mistgate/admin/v1/user_pb";
import { WaysOfGroup } from "./groups";
import type { DeviceN, UserN } from "./model";
import { TelegramSection } from "./telegram-proxy";
import { DevicesPanel } from "./user-detail";

const getTelegramLinks = vi.fn();
const issueTelegramKey = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  users: {
    getTelegramLinks: (...a: unknown[]) => getTelegramLinks(...a),
    issueTelegramKey: (...a: unknown[]) => issueTelegramKey(...a),
  },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { apps: [] }, effectiveTitle: "" }) },
  profiles: { listProtocols: () => Promise.resolve({ protocols: [{ id: "hysteria2", apps: [App.HAPP] }, { id: "mtproto", apps: [App.TELEGRAM] }] }) },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children?: ReactNode; to: string }) => <a href={to}>{children}</a>,
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
  getTelegramLinks.mockReset();
  issueTelegramKey.mockReset();
});

async function mount(ui: ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>{ui}</ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};

const user = (telegram: boolean) => ({ id: "usr_alice", name: "alice", groupName: "Family", deviceLimit: 3, devicesUsed: 2, apps: { happ: true, amnezia: true, telegram } }) as unknown as UserN;
const link = (over: object) => ({ nodeId: "nod_de1", nodeName: "de1", profileId: "prf_tg", profileName: "Telegram · 443", url: "tg://proxy?server=de1.example.com&port=443&secret=ee00", ipv6: false, relay: false, via: "", ...over });
const proxy = {
  hasKey: true,
  keyDeviceId: "dev_tg",
  keyIssuedUnix: BigInt(1_790_000_000),
  links: [link({}), link({ ipv6: true, url: "tg://proxy?server=[2001:db8::7]&port=443&secret=ee00" }), link({ relay: true, via: "RU", url: "tg://proxy?server=198.51.100.20&port=8443&secret=ee00" })],
};
const rows = () => [...document.querySelectorAll("section li")];

describe("the Telegram proxy of a person", () => {
  it("off: one line and the switch, which turns it on", async () => {
    const onTurnOn = vi.fn();
    await mount(<TelegramSection user={user(false)} countryOf={() => "DE"} onTurnOn={onTurnOn} onChanged={vi.fn()} />);
    expect(text()).toContain("Off: the person gets no tg:// links.");
    expect(getTelegramLinks).not.toHaveBeenCalled();
    await click(document.querySelector("[role=switch]"));
    expect(onTurnOn).toHaveBeenCalled();
  });

  it("on with a key: the key as a device out of the limit, and a row per link with the twins marked", async () => {
    getTelegramLinks.mockResolvedValue(proxy);
    await mount(<TelegramSection user={user(true)} countryOf={() => "DE"} onTurnOn={vi.fn()} onChanged={vi.fn()} />);
    expect(rows()[0]!.textContent).toContain("Telegram key");
    expect(rows()[0]!.textContent).toContain("not counted in the device limit");
    const links = rows().slice(1);
    expect(links.map((r) => r.querySelector("b")!.parentElement!.textContent)).toEqual(["de1", "de1IPv6", "de1via RU"]);
    // the link holds the key: opened, copied or scanned, never printed
    expect(links[0]!.querySelector("a")!.getAttribute("href")).toBe(proxy.links[0]!.url);
    expect(text()).not.toContain("secret=");
    expect(links[0]!.querySelector("svg[role=img]")).toBeNull();
    await click(links[0]!.querySelector('button[aria-label="QR code"]'));
    expect(rows()[1]!.querySelector("svg[role=img]")?.getAttribute("aria-label")).toBe("QR code of the tg:// link to de1");
  });

  it("on without a key: issues one and shows its links", async () => {
    getTelegramLinks.mockResolvedValue({ hasKey: false, links: [] });
    issueTelegramKey.mockResolvedValue({ proxy });
    const onChanged = vi.fn();
    await mount(<TelegramSection user={user(true)} countryOf={() => "DE"} onTurnOn={vi.fn()} onChanged={onChanged} />);
    expect(text()).toContain("No Telegram key: the links need one.");
    await click(button("Issue a key"));
    await settle();
    expect(issueTelegramKey).toHaveBeenCalledWith({ userId: "usr_alice" });
    expect(onChanged).toHaveBeenCalled();
    expect(rows()).toHaveLength(4);
  });
});

describe("the devices of a person with a Telegram key", () => {
  it("count by the server's number (the key is not in the limit) and keep the key out of the shared apps", async () => {
    getTelegramLinks.mockResolvedValue(proxy);
    const device = (id: string, kind: DeviceKind) => ({ id, kind, model: "", platform: "", client: "", awgProfileId: "", nodeIds: [], firstSeenUnix: 0, lastSeenUnix: 0, lastHandshakeUnix: 0, usedBytes: 0 }) as unknown as DeviceN;
    const devices = [device("d1", DeviceKind.LINK), device("d2", DeviceKind.LINK), device("dev_tg", DeviceKind.TELEGRAM_KEY)];
    await mount(<DevicesPanel user={user(true)} devices={devices} profiles={[{ protocol: "mtproto" }] as never} actions={{ refresh: vi.fn() } as never} onEditGroup={vi.fn()} />);
    expect(text()).toContain("2 of 3");
    expect(text()).not.toContain("Apps without a device ID");
    expect(text()).toContain("Telegram key");
  });
});

describe("the ways a person's group gives", () => {
  it("lists MTProto in a Telegram row of its own, not in the subscription, which reads plainly with no app on the page", async () => {
    const profiles = [
      { id: "p_hy", name: "Main · 443", protocol: "hysteria2" },
      { id: "p_tg", name: "Telegram · 443", protocol: "mtproto" },
    ];
    await mount(<WaysOfGroup profiles={profiles} compact />);
    const rows = [...document.querySelectorAll(".items-start")].map((r) => r.textContent);
    expect(rows[0]).toBe("SubscriptionMain · 443");
    expect(rows.at(-1)).toBe("Telegram ProxyTelegram · 443");
  });
});
