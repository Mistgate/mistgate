import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { useWays, WaysTab } from "./ways";

const listRelays = vi.fn();
const listRelayCandidates = vi.fn();
const createRelay = vi.fn();
const checkRelay = vi.fn();
const updateRelay = vi.fn();
let role = Role.OWNER;
let countryChecks: object[] = [];
vi.mock("@/lib/api", () => ({
  relays: {
    listRelays: (...a: unknown[]) => listRelays(...a),
    listRelayCandidates: (...a: unknown[]) => listRelayCandidates(...a),
    createRelay: (...a: unknown[]) => createRelay(...a),
    checkRelay: (...a: unknown[]) => checkRelay(...a),
    updateRelay: (...a: unknown[]) => updateRelay(...a),
    deleteRelay: vi.fn(),
  },
  health: {
    getChecks: (req: { nodeId: string }) =>
      Promise.resolve({ nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: countryChecks.filter((c) => !req.nodeId || (c as { nodeId: string }).nodeId === req.nodeId) }),
    getDoctor: () => Promise.resolve({ nowUnix: now, nodes: [{ nodeId: "nod_de1", items: [{ id: "ipv6", status: 1, params: { ipv6: "yes", connect: "ok" } }] }] }),
  },
  nodes: { listNodes: () => Promise.resolve({ nodes }), checkPorts: vi.fn() },
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
  { id: "nod_fi1", name: "fi1", countryCode: "FI", status: NodeStatus.ONLINE },
];
// every field of CountryCheck the client reads (as in screens/health/checks.test.tsx)
const country = (nodeId: string, verdict: CountryVerdict, dark: string[] = []) => ({
  nodeId, country: "RU", verdict, atUnix: now - 600, probes: [], reason: "", eyeball: true, confidence: "eyeball", listedBy: [], cdnProvider: "", cdnRegion: "",
  sources: ["globalping"], globalpingVerdict: verdict, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [],
  vantages: [...dark.map((network) => ({ source: "globalping", key: network, network, region: "", asn: "", kind: "mobile", state: "dark", atUnix: now - 600, dpiHop: 0, icmpOk: false, tcpOk: false })),
    { source: "globalping", key: "Rostelecom", network: "Rostelecom", region: "", asn: "", kind: "home", state: "reach", atUnix: now - 600, dpiHop: 0, icmpOk: true, tcpOk: true }],
  sourceVerdicts: [],
});
const relay = (over: Record<string, unknown> = {}) => ({
  id: "rly_1", inboundId: "inb_v", exitNodeId: "nod_de1", exitNodeName: "de1", profileId: "prf_v", profileName: "VLESS · TCP", protocol: "vless", viaNodeId: "nod_msk1", viaNodeName: "msk1",
  network: "tcp", port: 443, exitPort: 443, enabled: true, state: InboundState.ACTIVE, lastError: "", toIp: "203.0.113.7", appliedUnix: now - 3600, exitLive: true, agentSupported: true, createdUnix: now - 7200,
  bytes: 0, packets: 0, countedUnix: 0,
  ...over,
});
const cand = (over: Record<string, unknown>) => ({ inboundId: "inb_x", nodeId: "nod_de1", nodeName: "de1", profileId: "prf_x", profileName: "VLESS · XHTTP", protocol: "vless", network: "tcp", port: 2053, unsupported: "", hop: false, relayId: "", country: { verdict: "partial", dark: ["MTS"], atUnix: now }, reach: { verdict: "reach", source: "tspu_row", atUnix: now }, ...over });

const de1 = (over: Record<string, unknown> = {}) =>
  ({
    node: { id: "nod_de1", name: "de1", countryCode: "DE", address: "203.0.113.7", status: NodeStatus.ONLINE, bandwidthMbps: 0, ...over },
    inbounds: [{ id: "inb_v", port: 443, state: InboundState.ACTIVE, protocol: "vless", profileName: "VLESS · TCP" }],
    portChecks: [],
    // the lane counts the rows of its port check: one checked TCP port here
    tcpChecks: [{ port: 443, verdict: "ours", role: "vless", profile: "VLESS · TCP", checkedUnix: now - 600, sender: "fi1", reason: "" }],
    facts: { hasIpv6: true },
  }) as never;

function Tab({ data }: { data: never }) {
  const w = useWays(data);
  return <WaysTab data={data} ways={w} />;
}

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  role = Role.OWNER;
  search = { tab: "ways" };
  countryChecks = [country("nod_de1", CountryVerdict.PARTIAL, ["MTS"]), country("nod_msk1", CountryVerdict.OK), country("nod_fi1", CountryVerdict.PARTIAL, ["MTS"])];
  listRelays.mockResolvedValue({ relays: [relay()] });
  listRelayCandidates.mockResolvedValue({ agentSupported: true, entryCountry: { verdict: "ok", dark: [], atUnix: now }, candidates: [cand({}), cand({ inboundId: "inb_f", nodeId: "nod_fi1", nodeName: "fi1", profileName: "Trojan" })] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listRelays, listRelayCandidates, createRelay, checkRelay, updateRelay, navigate]) m.mockReset();
  delete (window as { matchMedia?: unknown }).matchMedia;
});

let qc: QueryClient;
async function mount(data: never) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(ui(data)));
  await settle();
}
const ui = (data: never) => (
  <QueryClientProvider client={qc}>
    <ToastProvider>
      <Tab data={data} />
    </ToastProvider>
  </QueryClientProvider>
);
const settle = async () => {
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const lanes = () => [...document.querySelectorAll<HTMLElement>("[role=tab]")];
const lane = (id: string) => document.getElementById(`way-tab-${id}`)!;
const panel = () => document.getElementById("way-panel")!;
const text = () => document.body.textContent ?? "";
const buttonIn = (el: ParentNode, label: string) => [...el.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]")!;

describe("the line of a node dark on part of the country", () => {
  it("draws direct (half seen), through msk1 (works, seen where de1 is dark) and IPv6 (soon); the add branch is amber and says why", async () => {
    await mount(de1());
    expect(lanes().map((l) => l.id)).toEqual(["way-tab-direct", "way-tab-via:nod_msk1", "way-tab-ipv6"]);
    expect(lanes().map((l) => l.dataset.ring)).toEqual(["half", "ok", "off"]);
    expect(lane("direct").textContent).toContain("203.0.113.7 · 1 port");
    expect(lane("direct").textContent).toContain("Dark on MTS");
    expect(lane("via:nod_msk1").textContent).toContain("People reach msk1 from MTS");
    expect(lane("ipv6").textContent).toContain("soon");
    expect(text()).toContain("People · Russia");
    expect(text()).toContain("Dark on MTS — add a way around");
    // the first lane with a problem is open: direct, with the ports and the country row
    expect(lane("direct").getAttribute("aria-selected")).toBe("true");
    expect(panel().textContent).toContain("People connect to 203.0.113.7.");
    expect(panel().textContent).toContain("Is de1 seen from Russia");
  });

  it("opens the lane the URL names, and arrows walk the list", async () => {
    search = { tab: "ways", way: "via:nod_msk1" };
    await mount(de1());
    expect(panel().textContent).toContain("People connect to msk1; it passes their traffic to de1 unchanged.");
    expect(panel().querySelectorAll("[data-testid=relay-row]")).toHaveLength(1);
    expect(panel().textContent).toContain("Port on msk1");
    expect(panel().querySelector("a[href*='way=out']")?.getAttribute("href")).toBe("/nodes/nod_msk1?tab=ways&way=out%3Anod_de1");
    act(() => lane("via:nod_msk1").focus());
    await act(async () => void lane("via:nod_msk1").dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true })));
    expect(document.activeElement).toBe(lane("ipv6"));
    await act(async () => void lane("ipv6").dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true })));
    expect(document.activeElement).toBe(lane("direct"));
  });

  it("on a phone the list and the lane take turns: a tap pushes the lane, the way back goes back", async () => {
    (window as { matchMedia?: unknown }).matchMedia = () => ({ matches: true });
    search = { tab: "ways" };
    await mount(de1());
    const list = document.querySelector("[role=tablist]")!;
    expect(list.className).not.toContain("max-md:hidden");
    expect(panel().parentElement!.className).toContain("max-md:hidden");
    await click(lane("via:nod_msk1"));
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ search: { tab: "ways", way: "via:nod_msk1" }, replace: false }));
    search = { tab: "ways", way: "via:nod_msk1" };
    await act(async () => root!.render(ui(de1())));
    expect(document.querySelector("[role=tablist]")!.className).toContain("max-md:hidden");
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {});
    await click(document.getElementById("way-back"));
    expect(back).toHaveBeenCalled();
    back.mockRestore();
  });
});

describe("the New way in dialog", () => {
  it("offers the node people see where de1 is dark, then only de1's servers through it, and makes the relay through that entry", async () => {
    search = { tab: "ways", add: "1" };
    await mount(de1());
    expect(dialog().textContent).toContain("A new way in to de1");
    expect(dialog().textContent).toContain("From Russia, de1 does not open on MTS.");
    expect(dialog().textContent).toContain("Good fit: msk1, seen from MTS");
    // the IPv6 entry is not there yet: text that says why, not a button, out of the Tab order
    const off = dialog().querySelector<HTMLElement>("[data-testid=way-option-off]")!;
    expect(off.getAttribute("aria-disabled")).toBe("true");
    expect(off.tagName).toBe("DIV");
    expect(off.textContent).toContain("The IPv6 entry is not in Mistgate yet: it comes in a later version.");
    expect(off.querySelector("button, [tabindex]")).toBeNull();

    await click([...dialog().querySelectorAll("button")].find((b) => b.textContent?.startsWith("Through another node")));
    await settle();
    expect([...dialog().querySelectorAll("section h3")].map((h) => h.textContent)).toEqual(["Good fits: seen from MTS", "Other nodes"]);
    const entries = [...dialog().querySelectorAll<HTMLElement>("[data-testid=way-entry]")];
    expect(entries.map((e) => e.querySelector(".font-extrabold")?.textContent)).toEqual(["msk1", "fi1"]);
    expect(entries[0]!.textContent).toContain("msk1 → de1 passes (TSPU check)");
    expect(entries[1]!.textContent).toContain("fi1 is dark too from MTS");

    await click(entries[0]);
    await settle();
    expect(listRelayCandidates).toHaveBeenCalledWith({ viaNodeId: "nod_msk1" }, expect.anything());
    const servers = [...dialog().querySelectorAll("[data-testid=relay-candidate]")].map((c) => c.textContent);
    expect(servers.every((s) => !s?.includes("Trojan"))).toBe(true); // fi1's server is not a way in to de1
    createRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false }) });
    checkRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, check: { ok: true, leg: { status: "ok", atUnix: now } } }) });
    updateRelay.mockResolvedValue({ relay: relay({ id: "rly_9" }) });
    await click(dialog().querySelector('input[aria-label="Relay VLESS · XHTTP of de1"]'));
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    expect(createRelay).toHaveBeenCalledWith({ inboundId: "inb_x", viaNodeId: "nod_msk1", port: 0 });
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_9", enabled: true });
    await click(buttonIn(dialog(), "Done"));
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: { tab: "ways", way: "via:nod_msk1" } }));
  });

  it("?add=via:<entry> opens straight at that entry's servers, and Back returns to the choice of node", async () => {
    search = { tab: "ways", add: "via:nod_msk1" };
    await mount(de1());
    expect(dialog().textContent).toContain("A new way in to de1 · Through msk1");
    await click(buttonIn(dialog(), "Back"));
    await settle();
    expect(dialog().textContent).toContain("Through which node");
  });
});

describe("other nodes and roles", () => {
  it("a healthy node has only Direct, green, and a quiet add branch that says what it is for", async () => {
    countryChecks = [country("nod_de1", CountryVerdict.OK)];
    listRelays.mockResolvedValue({ relays: [] });
    await mount(de1({}));
    expect(lanes().map((l) => [l.id, l.dataset.ring])).toEqual([
      ["way-tab-direct", "ok"],
      ["way-tab-ipv6", "off"],
    ]);
    expect(lane("direct").textContent).toContain("From Russia: all networks");
    expect(text()).toContain("If people’s network does not see this node, give them another way in");
  });

  it("an entry node lists the nodes reached through it, with a way to relay another", async () => {
    listRelays.mockResolvedValue({ relays: [relay(), relay({ id: "rly_2", inboundId: "inb_h", profileName: "Hysteria2", enabled: false, state: InboundState.PENDING, check: { ok: false, stage: "leg", leg: { status: "fail", code: "timeout", atUnix: now } } })] });
    const msk1 = { ...(de1() as object), node: { id: "nod_msk1", name: "msk1", countryCode: "RU", address: "198.51.100.20", status: NodeStatus.ONLINE, bandwidthMbps: 0 }, facts: {} } as never;
    await mount(msk1);
    expect(text()).toContain("Reached through msk1");
    expect(lane("out:nod_de1").textContent).toContain("2 servers · Partly down");
    expect(lane("out:nod_de1").getAttribute("aria-selected")).toBe("true"); // the lane with a problem opens first
    expect(text()).toContain("Relay another node…");
  });

  it("a helper sees direct and IPv6 only: no relays asked for, no way to add one", async () => {
    role = Role.HELPER;
    await mount(de1());
    expect(lanes().map((l) => l.id)).toEqual(["way-tab-direct", "way-tab-ipv6"]);
    expect(text()).not.toContain("Add a way in…");
    expect(listRelays).not.toHaveBeenCalled();
    expect(listRelayCandidates).not.toHaveBeenCalled();
  });
});
