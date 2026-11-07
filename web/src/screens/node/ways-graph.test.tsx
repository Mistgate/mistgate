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
  Link: ({ children, to, params, search: s, className }: { children?: ReactNode; to: string; params?: { id: string }; search?: Record<string, string>; className?: string }) => (
    <a href={`${to.replace("$id", params?.id ?? "")}${s ? "?" + new URLSearchParams(s).toString() : ""}`} className={className}>
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
const click = (el: Element | undefined | null) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
// an item of an open popover (menu or details)
const item = (label: string) => [...document.querySelectorAll<HTMLElement>("[data-base-ui-inert], body > div button, body > div a")].find((b) => b.textContent?.trim() === label);
const more = (title: string) => document.querySelector<HTMLElement>(`button[aria-label="${title}"]`);
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

const card = (id: string) => document.querySelector<HTMLElement>(`[data-gnode="${id}"]`);
const text = (id: string) => card(id)?.textContent ?? "";
const drop = (el: Element, type: string, id: string) =>
  act(async () => {
    const ev = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(ev, "dataTransfer", { value: { types: [type], getData: (t: string) => (t === type ? id : "") } });
    el.dispatchEvent(ev);
  });
const wayBody = (id: string) => card(id)!.querySelector<HTMLElement>(".p-3\\.5")!;

describe("the route map", () => {
  it("has the four labelled columns, people, a card per way, the node with its servers as chips, and where they exit", async () => {
    await mount();
    const cols = [...document.querySelectorAll("[data-variant=label]")].map((c) => c.textContent);
    expect(cols).toEqual(["Who", "How they get in", "Node", "Where they exit"]);
    expect(text("people")).toContain("People · Russia");
    expect(text("way:direct")).toContain("Direct");
    expect(text("way:via:nod_msk1")).toContain("Through msk1");
    expect(text("way:via:nod_msk1")).toContain("1 relay");
    expect(text("way:via:nod_msk1")).toContain("VLESS · TCP");
    expect(text("node")).toContain("de1");
    expect(text("node")).toContain("online");
    expect(text("node")).toContain("Hysteria2");
    expect(text("exit:internet")).toContain("directly from de1 · 2 servers");
    expect(text("ghost:way")).toContain("Add a way in");
    expect(text("ghost:exit")).toContain("WARP or through another node");
  });

  it("draws one wire per way, no rows of ports and no pills at wire ends", async () => {
    await mount();
    expect([...document.querySelectorAll("[data-gedge]")].map((e) => e.getAttribute("data-gedge")).sort()).toEqual(
      ["node:exit:internet", "people:way:direct", "people:way:via:nod_msk1", "way:direct:node", "way:via:nod_msk1:node"].sort(),
    );
    expect(document.querySelectorAll(".mg-gport:not(.mg-ganchor)")).toHaveLength(0);
    expect(document.querySelector(".mg-glabel[data-end]")).toBeNull();
    expect(document.querySelector(".mg-glabel")?.textContent).toBe("1 server");
  });

  it("selecting a way card opens that way through ?way=", async () => {
    await mount();
    await act(async () => card("way:via:nod_msk1")!.focus());
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: expect.objectContaining({ tab: "ways", way: "via:nod_msk1" }) }));
  });

  it("the People card links to where the country is set, for the owner", async () => {
    await mount();
    expect(card("people")!.querySelector("a")?.getAttribute("href")).toBe("/reachability");
  });
});

describe("the owner's edits", () => {
  it("the ⋯ of a through-card checks all, turns all off, adds a relay for another server (prefilled with the entry) and removes", async () => {
    updateRelay.mockResolvedValue({ relay: relay() });
    checkRelay.mockResolvedValue({ relay: relay() });
    deleteRelay.mockResolvedValue({});
    await mount();
    await click(more("Through msk1"));
    expect(["Check", "Turn off", "Add servers to this relay…", "Remove…"].every((l) => !!item(l))).toBe(true);
    await click(item("Check"));
    await settle();
    expect(checkRelay).toHaveBeenCalledWith({ id: "rly_1" });

    await click(more("Through msk1"));
    await click(item("Turn off"));
    await settle();
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_1", enabled: false });

    await click(more("Through msk1"));
    await click(item("Add servers to this relay…"));
    await settle();
    expect(listRelayCandidates).toHaveBeenCalledWith({ viaNodeId: "nod_msk1" }, expect.anything());
    expect(dialog()?.textContent).toContain("msk1");
    await click([...dialog()!.querySelectorAll("button")].find((b) => b.textContent === "Cancel" || b.textContent === "Close"));
    await settle();

    await click(more("Through msk1"));
    await click(item("Remove…"));
    expect(deleteRelay).not.toHaveBeenCalled();
    await click([...dialog()!.querySelectorAll("button")].find((b) => b.textContent === "Remove"));
    await settle();
    expect(deleteRelay).toHaveBeenCalledWith({ id: "rly_1" });
  });

  it("a relay chip opens its details and actions: change port and remove ask in their own dialogs", async () => {
    updateRelay.mockResolvedValue({ relay: relay() });
    await mount();
    const chip = [...card("way:via:nod_msk1")!.querySelectorAll("button")].find((b) => b.textContent?.includes("VLESS · TCP"))!;
    await click(chip);
    expect(document.body.textContent).toContain("msk1 → de1");
    await click(item("Change port…"));
    const input = dialog()!.querySelector<HTMLInputElement>("input")!;
    expect(input.value).toBe("443");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "8444");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click([...dialog()!.querySelectorAll("button")].find((b) => b.textContent === "Save"));
    await settle();
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_1", port: 8444 });
  });

  it("a server chip on the node opens its details with the In subscriptions switch, which asks first and offers Undo", async () => {
    updateInbound.mockResolvedValue({ warnings: [] });
    await mount();
    const chip = [...card("node")!.querySelectorAll("button")].find((b) => b.textContent?.includes("VLESS · TCP"))!;
    await click(chip);
    expect(document.body.textContent).toContain("Protocol");
    expect(document.body.textContent).toContain("tcp/443");
    // the popover is the last in the page (the detail card below the map has the list's own switches)
    const sw = [...document.querySelectorAll<HTMLElement>('[role="switch"][aria-label*="VLESS"]')].at(-1)!;
    await click(sw);
    await settle();
    expect(updateInbound).toHaveBeenCalledWith({ inboundId: "inb_v", directInSubscriptions: false, validateOnly: true });
    expect(updateInbound).toHaveBeenCalledWith({ inboundId: "inb_v", directInSubscriptions: false });
    expect(document.body.textContent).toContain("VLESS · TCP is out of subscriptions");
    await click([...document.querySelectorAll("button")].find((b) => b.textContent === "Undo"));
    await settle();
    expect(updateInbound).toHaveBeenLastCalledWith({ inboundId: "inb_v", directInSubscriptions: true });
  });

  it("the Direct card's ⋯ has a switch per server", async () => {
    await mount();
    await click(more("Direct"));
    expect(document.body.textContent).toContain("In subscriptions");
    expect(document.querySelectorAll('[role="switch"]').length).toBeGreaterThanOrEqual(2);
  });

  it("dragging a server onto a through-card opens the relay dialog prefilled; one the entry already relays is refused", async () => {
    await mount();
    await drop(wayBody("way:via:nod_msk1"), "application/x-mg-server", "inb_h");
    await settle();
    expect(listRelayCandidates).toHaveBeenCalledWith({ viaNodeId: "nod_msk1" }, expect.anything());
    expect(dialog()!.querySelector<HTMLInputElement>('input[aria-label="Relay Hysteria2 of de1"]')?.checked).toBe(true);
    await click([...dialog()!.querySelectorAll("button")].find((b) => b.textContent === "Cancel" || b.textContent === "Close"));
    await settle();
    await drop(wayBody("way:via:nod_msk1"), "application/x-mg-server", "inb_v");
    expect(document.body.textContent).toContain("Cannot relay this server here");
  });

  it("dragging a relay out onto the canvas asks to remove it, and removes it once confirmed", async () => {
    deleteRelay.mockResolvedValue({});
    await mount();
    await drop(document.querySelector('[role="application"]')!, "application/x-mg-relay", "rly_1");
    expect(deleteRelay).not.toHaveBeenCalled();
    await click([...dialog()!.querySelectorAll("button")].find((b) => b.className.includes("danger")) ?? [...dialog()!.querySelectorAll("button")].pop());
    await settle();
    expect(deleteRelay).toHaveBeenCalledWith({ id: "rly_1" });
  });

  it("the add-a-way card lists the other nodes; a node picked opens the relay dialog at once, prefilled, and cancelling changes nothing", async () => {
    await mount();
    expect(card("way:via:nod_spb1")).toBeNull();
    await click(more("Add a way in: through a node, CDN, IPv6"));
    expect(item("Through spb1")).toBeTruthy();
    await click(item("Through spb1"));
    await settle();
    expect(listRelayCandidates).toHaveBeenCalledWith({ viaNodeId: "nod_spb1" }, expect.anything());
    expect(dialog()?.textContent).toContain("spb1");
    await click([...dialog()!.querySelectorAll("button")].find((b) => b.textContent === "Cancel" || b.textContent === "Close"));
    await settle();
    expect(card("way:via:nod_spb1")).toBeNull();
  });

  it("the CDN item goes to the new-profile form of this node, not to an empty card", async () => {
    await mount();
    await click(more("Add a way in: through a node, CDN, IPv6"));
    expect(item("Through a CDN: add a profile")?.getAttribute("href")).toBe("/profiles/new?protocol=vless&node=nod_de1");
  });

  it("with only Direct a hint card says what a way in is for, with a button that opens the same menu", async () => {
    listRelays.mockResolvedValue({ relays: [] });
    await mount();
    expect(card("ghost:way")).toBeNull();
    expect(text("hint:way")).toContain("No other ways in yet");
    await click(more("Add a way in"));
    await click(item("Through msk1"));
    await settle();
    expect(dialog()?.textContent).toContain("msk1");
  });

  it("node names on the cards link to that node's Ways in graph", async () => {
    await mount();
    expect(card("way:via:nod_msk1")!.querySelector("a")?.getAttribute("href")).toBe("/nodes/nod_msk1?tab=ways&view=graph");
  });

  it("the other exit card offers WARP setup, and through another node is disabled with the reason", async () => {
    await mount();
    await click(more("WARP or through another node"));
    expect(item("Set up WARP")?.getAttribute("href") ?? "").toContain("/nodes/nod_de1");
    expect(item("Through another node")?.hasAttribute("disabled")).toBe(true);
    expect(item("Through another node")?.getAttribute("title")).toBe("Needs support in the system first");
  });
});

describe("another role", () => {
  it("a helper sees the map read-only: no menus, no add-a-way, no exit setup, no draggable chips", async () => {
    role = Role.HELPER;
    await mount();
    expect(app()).not.toBeNull();
    expect(text("way:direct")).toContain("Direct");
    expect(card("ghost:way")).toBeNull();
    expect(card("ghost:exit")).toBeNull();
    expect(document.querySelector('[data-gnode="way:direct"] button[aria-label]:not(.mg-ganchor)')).toBeNull();
    expect(card("people")!.querySelector("a")).toBeNull();
    expect(document.querySelector("[draggable=true]")).toBeNull();
    expect(listRelays).not.toHaveBeenCalled();
  });
});

describe("the legend and the shortcuts", () => {
  it("the legend shows the wires, the statuses and the cards; the editing line is the owner's", async () => {
    await mount();
    const btn = (label: string) => [...document.querySelectorAll<HTMLElement>(".mg-glegend-btn")].find((b) => b.textContent === label)!;
    await click(btn("Legend"));
    const help = document.querySelector("#graph-help")!.textContent!;
    expect(help).toContain("Traffic flows");
    expect(help).toContain("Blocked or off");
    expect(help).toContain("Relay failing");
    expect(help).toContain("Drag a server onto a way");
    act(() => root!.unmount());
    host!.remove();
    role = Role.HELPER;
    await mount();
    // the open card is remembered by the browser: still open on the second mount
    if (!document.querySelector("#graph-help")) await click(btn("Legend"));
    expect(document.querySelector("#graph-help")!.textContent).toContain("Traffic flows");
    expect(document.querySelector("#graph-help")!.textContent).not.toContain("Drag a server onto a way");
  });

  it("the shortcuts card has the route map's own group for the owner", async () => {
    await mount();
    const btn = [...document.querySelectorAll<HTMLElement>(".mg-glegend-btn")].find((b) => b.textContent === "Shortcuts")!;
    await click(btn);
    expect(document.querySelector("#graph-help")!.textContent).toContain("Relay a server");
  });
});

describe("full screen", () => {
  it("opens from the toolbar button into ?full=1", async () => {
    await mount();
    expect(document.querySelector('[role="dialog"][aria-modal="true"]')).toBeNull();
    await click(document.querySelector("[data-graph-expand]"));
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: expect.objectContaining({ tab: "ways", view: "graph", full: "1" }) }));
  });

  it("?full=1 restores the overlay: a top bar, the map, a drawer with the detail, and no page scroll behind", async () => {
    search = { tab: "ways", view: "graph", full: "1" };
    await mount();
    const overlay = document.querySelector<HTMLElement>('[role="dialog"][aria-modal="true"]')!;
    expect(overlay).not.toBeNull();
    expect(overlay.querySelector("header")?.textContent).toContain("Ways in to de1");
    expect(overlay.querySelector('[role="application"]')).not.toBeNull();
    expect(overlay.querySelector("aside #way-panel")).not.toBeNull();
    expect(document.body.style.overflow).toBe("hidden");
  });

  it("Esc and the close button leave it", async () => {
    search = { tab: "ways", view: "graph", full: "1" };
    await mount();
    const overlay = () => document.querySelector<HTMLElement>('[role="dialog"][aria-modal="true"]')!;
    await act(async () => void overlay().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: expect.not.objectContaining({ full: "1" }) }));
    navigate.mockClear();
    await click(overlay().querySelector('button[aria-label="Close"]'));
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: expect.not.objectContaining({ full: "1" }) }));
  });
});
