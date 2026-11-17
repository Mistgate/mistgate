import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { en } from "@/i18n/en";
import { en as np } from "@/i18n/nodepage";
import { ExitTab } from "./exit";

// Node → Exit: WARP first, then the people's traffic form (the IPv6 exit, BitTorrent, the node's DNS) with one Save that
// sends only what changed.

const updateNode = vi.fn();
const me = vi.fn();
vi.mock("@/lib/api", () => ({
  nodes: { updateNode: (...a: unknown[]) => updateNode(...a) },
  auth: { me: (...a: unknown[]) => me(...a) },
}));
vi.mock("./warp", () => ({ WarpCard: () => <section id="warp">WARP card</section> }));
vi.mock("./dns-options", () => ({ DnsOptionsCard: () => null }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  me.mockResolvedValue({ admin: { id: "adm_1", role: 1 } });
  updateNode.mockResolvedValue({});
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [updateNode, me]) m.mockReset();
});

const data = (node: Record<string, unknown> = {}) =>
  ({
    node: { id: "nod_1", name: "de1", status: NodeStatus.ONLINE, torrentBlockerEnabled: false, torrentBlockerSupported: true, clientIpv6: true, clientIpv6Supported: true, ...node },
    dnsResolvers: [],
    inbounds: [],
  }) as never;

async function mount(node?: Record<string, unknown>) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <ExitTab data={data(node)} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = async (el: Element | null | undefined) => {
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 3; i++) await settle();
};
// the bars of the cards come in order: the traffic's, then the node's own DNS
const seg = (label: string) => [...document.querySelectorAll("[role=radio]")].find((b) => b.textContent?.trim() === label);
const saveOf = (i: number) => [...document.querySelectorAll("button")].filter((b) => b.textContent?.trim() === en["common.save"])[i];
const toggle = (label: string) => [...document.querySelectorAll("label")].find((l) => l.textContent?.includes(label))!.querySelector<HTMLElement>("[role=switch]")!;
const on = (el: HTMLElement) => el.getAttribute("aria-checked") === "true";
const off = (el: HTMLElement) => el.hasAttribute("data-disabled") || el.getAttribute("aria-disabled") === "true";

describe("the Exit tab", () => {
  it("puts WARP first, where every “Open WARP” lands", async () => {
    await mount();
    const warp = document.getElementById("warp")!;
    const traffic = document.querySelector(".nx-opt")!;
    expect(warp.compareDocumentPosition(traffic) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(text()).toContain(en["node.exit.traffic"]);
  });
});

describe("the IPv6 exit", () => {
  it("is on by default, and the owner turns it off and saves only that field", async () => {
    await mount();
    expect(on(toggle(en["node.settings.clientIpv6"]))).toBe(true);
    expect(off(toggle(en["node.settings.clientIpv6"]))).toBe(false);
    expect(text()).toContain(en["node.settings.clientIpv6Hint"]);
    expect(text()).not.toContain(en["node.settings.clientIpv6Unsupported"]);
    await click(toggle(en["node.settings.clientIpv6"]));
    expect(on(toggle(en["node.settings.clientIpv6"]))).toBe(false);
    await click(saveOf(0));
    expect(updateNode).toHaveBeenCalledTimes(1);
    expect(updateNode.mock.calls[0]![0]).toEqual({ nodeId: "nod_1", clientIpv6: false });
  });

  it("cannot be turned off for an agent that does not know it, and says to update it", async () => {
    await mount({ clientIpv6: true, clientIpv6Supported: false });
    expect(off(toggle(en["node.settings.clientIpv6"]))).toBe(true);
    expect(text()).toContain(en["node.settings.clientIpv6Unsupported"]);
  });

  it("can be turned back on when the agent lost the capability, and warns that it does not follow the setting", async () => {
    await mount({ clientIpv6: false, clientIpv6Supported: false });
    expect(on(toggle(en["node.settings.clientIpv6"]))).toBe(false);
    expect(off(toggle(en["node.settings.clientIpv6"]))).toBe(false);
    expect(text()).toContain(en["node.settings.clientIpv6Stale"]);
  });

  it("is read-only for a helper, whom the API refuses", async () => {
    me.mockResolvedValue({ admin: { id: "adm_2", role: 2 } });
    await mount();
    expect(off(toggle(en["node.settings.clientIpv6"]))).toBe(true);
    expect(text()).toContain(en["node.settings.clientIpv6Owner"]);
  });
});

describe("BitTorrent", () => {
  it("is switched on and saved alone", async () => {
    await mount();
    await click(toggle(en["node.settings.torrentBlocker"]));
    await click(saveOf(0));
    expect(updateNode.mock.calls[0]![0]).toEqual({ nodeId: "nod_1", torrentBlockerEnabled: true });
  });

  it("asks for an update where the agent cannot block it", async () => {
    await mount({ torrentBlockerSupported: false });
    expect(off(toggle(en["node.settings.torrentBlocker"]))).toBe(true);
    expect(text()).toContain(en["node.settings.torrentBlockerUnsupported"]);
  });
});

describe("DNS for user traffic", () => {
  it("offers custom servers to a node that uses the server's resolver, once Custom is picked, with a quick fill of the two presets", async () => {
    await mount();
    expect(text()).not.toContain(en["node.settings.dnsCustom"]);
    expect(text()).toContain(np["np.ex.dns.systemHint"]);
    await click(seg(np["np.ex.dns.own"]));
    expect(text()).toContain(en["node.settings.dnsCustom"]);
    await click(button(en["node.settings.dnsMode.yandex"]));
    expect(document.querySelector<HTMLInputElement>(`#nx-dns-nod_1`)!.value).toBe("77.88.8.8, 77.88.8.1");
    await click(saveOf(1));
    expect(updateNode.mock.calls[0]![0]).toEqual({ nodeId: "nod_1", dnsResolvers: { values: ["77.88.8.8", "77.88.8.1"] } });
  });

  it("goes back to the server's resolver with an empty list", async () => {
    await mount();
    await click(seg(np["np.ex.dns.own"]));
    await click(button(en["node.settings.dnsMode.cloudflareGoogle"]));
    await click(saveOf(1));
    await click(seg(np["np.ex.dns.system"]));
    await click(saveOf(1));
    expect(updateNode.mock.calls[1]![0]).toEqual({ nodeId: "nod_1", dnsResolvers: { values: [] } });
  });
});
