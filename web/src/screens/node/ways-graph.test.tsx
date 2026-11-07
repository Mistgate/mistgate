import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { useWays, WaysTab } from "./ways";

const listRelays = vi.fn();
const listRelayCandidates = vi.fn();
const createRelay = vi.fn();
const checkRelay = vi.fn();
const updateRelay = vi.fn();
const deleteRelay = vi.fn();
const updateInbound = vi.fn();
let role = Role.OWNER;
vi.mock("@/lib/api", () => ({
  relays: {
    listRelays: (...a: unknown[]) => listRelays(...a),
    listRelayCandidates: (...a: unknown[]) => listRelayCandidates(...a),
    createRelay: (...a: unknown[]) => createRelay(...a),
    checkRelay: (...a: unknown[]) => checkRelay(...a),
    updateRelay: (...a: unknown[]) => updateRelay(...a),
    deleteRelay: (...a: unknown[]) => deleteRelay(...a),
  },
  health: {
    getChecks: () => Promise.resolve({ nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: [] }),
    getDoctor: () => Promise.resolve({ nowUnix: now, nodes: [{ nodeId: "nod_de1", items: [{ id: "ipv6", status: 1, params: { ipv6: "no" } }] }] }),
  },
  nodes: { listNodes: () => Promise.resolve({ nodes }), checkPorts: vi.fn(), checkIpv6: vi.fn(), updateIpv6Entry: vi.fn() },
  profiles: { updateInbound: (...a: unknown[]) => updateInbound(...a), listProfiles: () => Promise.resolve({ profiles: [] }) },
  countryReach: { getCountryReachability: () => Promise.resolve({ settings: { country: "RU" } }) },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { updateIntervalHours: 12 }, effectiveTitle: "", serverSamples: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
}));
let search: Record<string, unknown> = {};
const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, className }: { children?: ReactNode; to: string; params?: { id: string }; className?: string }) => (
    <a href={to.replace("$id", params?.id ?? "")} className={className}>
      {children}
    </a>
  ),
  useSearch: () => search,
  useNavigate: () => navigate,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  Element.prototype.scrollIntoView ??= () => {};
});

const now = Math.floor(Date.now() / 1000);
const nodes = [
  { id: "nod_de1", name: "de1", countryCode: "DE", status: NodeStatus.ONLINE },
  { id: "nod_msk1", name: "msk1", countryCode: "RU", status: NodeStatus.ONLINE },
  { id: "nod_spb1", name: "spb1", countryCode: "RU", status: NodeStatus.ONLINE },
];
const relay = (over: Record<string, unknown> = {}) => ({
  id: "rly_1", inboundId: "inb_v", exitNodeId: "nod_de1", exitNodeName: "de1", profileId: "prf_v", profileName: "VLESS · TCP", protocol: "vless", viaNodeId: "nod_msk1", viaNodeName: "msk1",
  network: "tcp", port: 443, exitPort: 443, enabled: true, state: InboundState.ACTIVE, lastError: "", toIp: "203.0.113.7", appliedUnix: now - 3600, exitLive: true, agentSupported: true, createdUnix: now - 7200,
  bytes: 0, packets: 0, countedUnix: 0, ...over,
});
const ib = (id: string, protocol: string, port: number, name: string, over: Record<string, unknown> = {}) => ({
  id, profileId: `prf_${id}`, profileName: name, protocol, port, state: InboundState.ACTIVE, enabled: true, egress: "direct", cdn: false, directInSubscriptions: true, ...over,
});
const de1 = () =>
  ({
    node: { id: "nod_de1", name: "de1", countryCode: "DE", address: "203.0.113.7", status: NodeStatus.ONLINE, bandwidthMbps: 0, warp: { state: WarpState.NOT_CONFIGURED } },
    inbounds: [ib("inb_v", "vless", 443, "VLESS · TCP"), ib("inb_h", "hysteria2", 8443, "Hysteria2")],
    portChecks: [],
    tcpChecks: [],
    facts: { hasIpv6: false },
  }) as never;

function Tab({ data }: { data: never }) {
  const w = useWays(data);
  return <WaysTab data={data} ways={w} />;
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  role = Role.OWNER;
  search = { tab: "ways", view: "graph" };
  localStorage.clear();
  listRelays.mockResolvedValue({ relays: [relay()] });
  listRelayCandidates.mockResolvedValue({
    agentSupported: true,
    entryCountry: { verdict: "ok", dark: [], atUnix: now },
    candidates: [{ inboundId: "inb_h", nodeId: "nod_de1", nodeName: "de1", profileId: "prf_inb_h", profileName: "Hysteria2", protocol: "hysteria2", network: "udp", port: 8443, unsupported: "", hop: false, relayId: "", country: { verdict: "ok", dark: [], atUnix: now }, reach: { verdict: "reach", source: "tspu_row", atUnix: now } }],
  });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listRelays, listRelayCandidates, createRelay, checkRelay, updateRelay, deleteRelay, updateInbound, navigate]) m.mockReset();
  delete (window as { matchMedia?: unknown }).matchMedia;
});

const settle = async () => {
  for (let i = 0; i < 10; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function mount(data: never = de1()) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <Tab data={data} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const app = () => document.querySelector('[role="application"]');
const port = (card: string, id: string) => document.querySelector<HTMLElement>(`[data-gnode="${card}"] [data-port-id="${id}"]`)!;
const wire = (id: string) => document.querySelector<HTMLElement>(`[data-gedge="${id}"]`)!;
const click = (el: Element | undefined | null) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const key = (el: Element, k: string) => act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));
const menuItem = (label: string) => [...document.querySelectorAll<HTMLElement>(".mg-gmenu button, .mg-gmenu a")].find((b) => b.textContent?.trim() === label);
// the last one: a toast ("The relay is off") is a dialog too
const dialog = () => [...document.querySelectorAll<HTMLElement>("[role=dialog]")].at(-1) ?? null;

describe("the List | Graph switch", () => {
  it("shows the list by default, switches to the graph through the URL and remembers the choice", async () => {
    search = { tab: "ways" };
    await mount();
    expect(app()).toBeNull();
    const graph = [...document.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent === "Graph")!;
    await click(graph);
    expect(localStorage.getItem("ways.view")).toBe("graph");
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: expect.objectContaining({ tab: "ways", view: "graph" }) }));
  });

  it("opens the graph from ?view=graph, and from the browser's memory when the URL says nothing", async () => {
    await mount();
    expect(app()?.getAttribute("aria-label")).toBe("Ways in to de1");
    act(() => root!.unmount());
    host!.remove();
    search = { tab: "ways" };
    localStorage.setItem("ways.view", "graph");
    await mount();
    expect(app()).not.toBeNull();
    act(() => root!.unmount());
    host!.remove();
    search = { tab: "ways", view: "list" };
    await mount();
    expect(app()).toBeNull();
  });

  it("is the list only on a phone, with no switch", async () => {
    (window as { matchMedia?: unknown }).matchMedia = () => ({ matches: true, addEventListener: () => {}, removeEventListener: () => {} });
    await mount();
    expect(app()).toBeNull();
    expect(document.querySelector("[role=radiogroup]")).toBeNull();
  });
});

describe("the graph's cards and wires", () => {
  it("draws people, the entry, the node's servers and the exit, with a wire per way", async () => {
    await mount();
    const cards = [...document.querySelectorAll("[data-gnode]")].map((c) => c.getAttribute("data-gnode"));
    expect(cards).toEqual(expect.arrayContaining(["people", "entry:nod_msk1", "node", "internet"]));
    expect(wire("relay:rly_1")).toBeTruthy();
    expect(wire("direct:inb_v")).toBeTruthy();
    expect(wire("egress:inb_v")).toBeTruthy();
    expect(wire("reach:nod_msk1")).toBeTruthy();
    // the chip on each server's row, the IPv6 row for a host with none
    const nodeCard = document.querySelector('[data-gnode="node"]')!.textContent!;
    expect(nodeCard).toContain("VLESS · TCP");
    expect(nodeCard).toContain("no IPv6 here");
  });

  it("selects a card the way the list opens a way, through ?way=", async () => {
    await mount();
    const card = document.querySelector<HTMLElement>('[data-gnode="entry:nod_msk1"]')!;
    await act(async () => card.focus());
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: expect.objectContaining({ tab: "ways", way: "via:nod_msk1" }) }));
  });
});

describe("the owner's edits", () => {
  it("drawing a relay from an entry to a server opens the relay dialog with both filled in", async () => {
    await mount();
    const out = port("entry:nod_msk1", "relay:new");
    out.focus();
    // the server msk1 already relays is not offered; the other one is
    await key(out, "Enter");
    expect(document.querySelectorAll('[data-role="target"], [data-role="current"]')).toHaveLength(1);
    expect(port("node", "in:inb_h").getAttribute("data-role")).toBe("current");
    await key(document.activeElement!, "Enter");
    await settle();
    expect(dialog()?.textContent).toContain("msk1");
    expect(listRelayCandidates).toHaveBeenCalledWith({ viaNodeId: "nod_msk1" }, expect.anything());
    const box = dialog()!.querySelector<HTMLInputElement>('input[aria-label="Relay Hysteria2 of de1"]');
    expect(box?.checked).toBe(true);
  });

  it("explains why a server cannot take a relay: an existing one is not even offered as a target", async () => {
    await mount();
    const out = port("entry:nod_msk1", "relay:new");
    await key(out, "Enter");
    expect(port("node", "in:inb_v").getAttribute("data-role")).toBe("dim");
  });

  it("a relay wire's menu checks, turns off, changes the port and removes through the list's own calls", async () => {
    updateRelay.mockResolvedValue({ relay: relay() });
    checkRelay.mockResolvedValue({ relay: relay() });
    deleteRelay.mockResolvedValue({});
    await mount();

    await click(wire("relay:rly_1"));
    expect([...document.querySelectorAll(".mg-gmenu button")].map((b) => b.textContent)).toEqual(["Check", "Turn off", "Change port…", "Remove…"]);
    await click(menuItem("Check"));
    await settle();
    expect(checkRelay).toHaveBeenCalledWith({ id: "rly_1" });

    await click(wire("relay:rly_1"));
    await click(menuItem("Turn off"));
    await settle();
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_1", enabled: false });

    await click(wire("relay:rly_1"));
    await click(menuItem("Change port…"));
    const input = dialog()!.querySelector<HTMLInputElement>("input")!;
    expect(input.value).toBe("443");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "8444");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click([...dialog()!.querySelectorAll("button")].find((b) => b.textContent === "Save"));
    await settle();
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_1", port: 8444 });

    // removing asks first
    await click(wire("relay:rly_1"));
    await click(menuItem("Remove…"));
    expect(deleteRelay).not.toHaveBeenCalled();
    await click([...dialog()!.querySelectorAll("button")].find((b) => /remove/i.test(b.textContent ?? "") && b.className.includes("danger")) ?? [...dialog()!.querySelectorAll("button")].pop());
    await settle();
    expect(deleteRelay).toHaveBeenCalledWith({ id: "rly_1" });
  });

  it("Delete on a selected relay wire asks to remove it, and the people-to-server wire offers the In subscriptions switch", async () => {
    updateInbound.mockResolvedValue({ warnings: [] });
    await mount();
    const w = wire("relay:rly_1");
    await click(w);
    await key(w, "Delete");
    expect(dialog()).not.toBeNull();
    expect(deleteRelay).not.toHaveBeenCalled();
    await click([...dialog()!.querySelectorAll("button")].find((b) => b.textContent === "Cancel"));
    await settle();

    await click(wire("direct:inb_v"));
    expect([...document.querySelectorAll(".mg-gmenu button")].map((b) => b.textContent)).toEqual(["Keep out of subscriptions…"]);
    await click(menuItem("Keep out of subscriptions…"));
    await settle();
    // the server is asked first (validate only), then switched
    expect(updateInbound).toHaveBeenCalledWith({ inboundId: "inb_v", directInSubscriptions: false, validateOnly: true });
    expect(updateInbound).toHaveBeenCalledWith({ inboundId: "inb_v", directInSubscriptions: false });
  });

  it("the exit wire offers a twin with the other exit", async () => {
    await mount();
    await click(wire("egress:inb_v"));
    expect([...document.querySelectorAll(".mg-gmenu button")].map((b) => b.textContent)).toEqual(["Make a twin through WARP…"]);
  });

  it("adds an entry node from the list of the other nodes", async () => {
    await mount();
    expect(document.querySelector('[data-gnode="entry:nod_spb1"]')).toBeNull();
    expect(document.querySelector('button[aria-label="Add entry node"]')).not.toBeNull();
  });
});

describe("another role", () => {
  it("a helper sees the graph read-only: no way to add a relay, no wire menu, no add-entry control", async () => {
    role = Role.HELPER;
    await mount();
    expect(app()).not.toBeNull();
    expect(document.querySelector('button[aria-label="Add entry node"]')).toBeNull();
    // a helper is not given relays, so there is no entry at all, and no output to draw from
    expect(document.querySelector('[data-port-id="relay:new"]')).toBeNull();
    expect(wire("direct:inb_v")).toBeTruthy();
    await click(wire("direct:inb_v"));
    expect(document.querySelector(".mg-gmenu")).toBeNull();
    expect(listRelays).not.toHaveBeenCalled();
  });
});


describe("full screen", () => {
  it("opens from the toolbar button into ?full=1, with the way's detail in a drawer", async () => {
    await mount();
    expect(document.querySelector('[role="dialog"][aria-modal="true"]')).toBeNull();
    await click(document.querySelector("[data-graph-expand]"));
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: expect.objectContaining({ tab: "ways", view: "graph", full: "1" }) }));
  });

  it("?full=1 restores the overlay: a top bar, the graph, a drawer with the detail, and no page scroll behind", async () => {
    search = { tab: "ways", view: "graph", full: "1" };
    await mount();
    const overlay = document.querySelector<HTMLElement>('[role="dialog"][aria-modal="true"]')!;
    expect(overlay).not.toBeNull();
    expect(overlay.querySelector("header")?.textContent).toContain("Ways in to de1");
    expect(overlay.querySelector('[role="application"]')).not.toBeNull();
    expect(overlay.querySelector("aside #way-panel")).not.toBeNull();
    expect(document.body.style.overflow).toBe("hidden");
    // the in-tab list/graph switch is under the overlay, not in it
    expect(overlay.querySelector("[role=radiogroup]")).toBeNull();
  });

  it("Esc and the close button leave it; the page scrolls again", async () => {
    search = { tab: "ways", view: "graph", full: "1" };
    await mount();
    const overlay = () => document.querySelector<HTMLElement>('[role="dialog"][aria-modal="true"]')!;
    await act(async () => void overlay().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: expect.not.objectContaining({ full: "1" }) }));
    navigate.mockClear();
    await click(overlay().querySelector('button[aria-label="Close"]'));
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: expect.not.objectContaining({ full: "1" }) }));
  });

  it("the shortcuts card in full screen also lists the ways' own shortcuts for the owner", async () => {
    search = { tab: "ways", view: "graph", full: "1" };
    await mount();
    const overlay = document.querySelector<HTMLElement>('[role="dialog"][aria-modal="true"]')!;
    const shortcuts = [...overlay.querySelectorAll<HTMLElement>("header button")].find((b) => b.textContent === "Shortcuts")!;
    await click(shortcuts);
    const card = overlay.querySelector("#graph-help")!;
    expect(card.textContent).toContain("New relay");
    expect(card.textContent).toContain("Remove relay");
  });

  it("a helper's shortcuts have no ways group, and the legend leaves out the editing line", async () => {
    role = Role.HELPER;
    await mount();
    const btn = (label: string) => [...document.querySelectorAll<HTMLElement>(".mg-glegend-btn")].find((b) => b.textContent === label)!;
    await click(btn("Shortcuts"));
    expect(document.querySelector("#graph-help")?.textContent).not.toContain("New relay");
    await click(btn("Legend"));
    expect(document.querySelector("#graph-help")?.textContent).toContain("Traffic flows");
    expect(document.querySelector("#graph-help")?.textContent).not.toContain("Drag from");
  });
});
