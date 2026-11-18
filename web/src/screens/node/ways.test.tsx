import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { Ipv6EntryAction } from "@/gen/mistgate/admin/v1/node_pb";
import { en } from "@/i18n/en";
import { setJob } from "./relay-job";
import { useWays, WaysStrip, WaysTab } from "./ways";

const listRelays = vi.fn();
const listRelayCandidates = vi.fn();
const createRelay = vi.fn();
const checkRelay = vi.fn();
const updateRelay = vi.fn();
const checkIpv6 = vi.fn();
const updateIpv6Entry = vi.fn();
const updateInbound = vi.fn();
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
  nodes: {
    listNodes: () => Promise.resolve({ nodes }),
    checkPorts: vi.fn(),
    checkIpv6: (...a: unknown[]) => checkIpv6(...a),
    updateIpv6Entry: (...a: unknown[]) => updateIpv6Entry(...a),
  },
  profiles: { updateInbound: (...a: unknown[]) => updateInbound(...a) },
  countryReach: { getCountryReachability: () => Promise.resolve({ settings: { country: "RU" } }) },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { updateIntervalHours: 12 }, effectiveTitle: "", serverSamples: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
}));
let search: Record<string, unknown> = {};
const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search: s, className, title }: { children?: ReactNode; to: string; params?: { id: string }; search?: Record<string, string>; className?: string; title?: string }) => (
    <a href={`${to.replace("$id", params?.id ?? "")}${s ? "?" + new URLSearchParams(s).toString() : ""}`} className={className} title={title}>
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

// Node.ipv6 of a node that can take the entry: two addresses of its uplink, the second the one it sends from
const v6 = (over: Record<string, unknown> = {}) => ({
  entry: "",
  entrySupported: true,
  unavailableReason: "",
  addresses: [
    { address: "2001:db8::5", prefixLen: 64 },
    { address: "2001:db8::7", prefixLen: 64 },
  ],
  src: "2001:db8::7",
  ...over,
});
const passed = (over: Record<string, unknown> = {}) => ({
  address: "2001:db8::7",
  passed: true,
  errorCode: "",
  verdicts: [
    { network: "tcp", port: 443, verdict: "ours" },
    { network: "udp", port: 443, verdict: "ok" },
  ],
  sender: "fi1",
  checkedUnix: now - 600,
  ...over,
});

const de1 = (over: Record<string, unknown> = {}) =>
  ({
    node: { id: "nod_de1", name: "de1", countryCode: "DE", address: "203.0.113.7", status: NodeStatus.ONLINE, bandwidthMbps: 0, ipv6: v6(), ...over },
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
  for (const m of [listRelays, listRelayCandidates, createRelay, checkRelay, updateRelay, checkIpv6, updateIpv6Entry, updateInbound, navigate]) m.mockReset();
  delete (window as { matchMedia?: unknown }).matchMedia;
  setJob(undefined);
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
  it("draws direct (half seen), through msk1 (works, seen where de1 is dark) and IPv6 (off); the add branch is amber and says why", async () => {
    await mount(de1());
    expect(lanes().map((l) => l.id)).toEqual(["way-tab-direct", "way-tab-via:nod_msk1", "way-tab-ipv6"]);
    expect(lanes().map((l) => l.dataset.ring)).toEqual(["half", "ok", "off"]);
    expect(lane("direct").textContent).toContain("203.0.113.7 · 1 port");
    expect(lane("direct").textContent).toContain("Dark on MTS");
    expect(lane("via:nod_msk1").textContent).toContain("People reach msk1 from MTS");
    expect(lane("ipv6").textContent).toBe("IPv6 entryoffOff");
    expect(text()).toContain("People · Russia");
    expect(text()).toContain("Dark on MTS — add a way around");
    // the first lane with a problem is open: direct, with the ports and the country row
    expect(lane("direct").getAttribute("aria-selected")).toBe("true");
    expect(panel().textContent).toContain("People connect to 203.0.113.7.");
    expect(panel().textContent).toContain("From Russia it does not open for everyone");
  });

  it("opens the lane the URL names, and arrows walk the list", async () => {
    search = { tab: "ways", way: "via:nod_msk1" };
    await mount(de1());
    expect(panel().textContent).toContain("People connect to msk1; it passes their traffic to de1 unchanged.");
    expect(panel().querySelectorAll("[data-testid=relay-row]")).toHaveLength(1);
    expect(panel().textContent).toContain("tcp/443 on msk1");
    expect(panel().querySelector("a[href*='way=out']")?.getAttribute("href")).toBe("/nodes/nod_msk1?tab=ways&way=out%3Anod_de1");
    act(() => lane("via:nod_msk1").focus());
    await act(async () => void lane("via:nod_msk1").dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true })));
    expect(document.activeElement).toBe(lane("ipv6"));
    await act(async () => void lane("ipv6").dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true })));
    expect(document.activeElement).toBe(lane("direct"));
  });

  it("on a phone the list and the open way stand one above the other: a tap opens the way in place", async () => {
    (window as { matchMedia?: unknown }).matchMedia = () => ({ matches: true });
    search = { tab: "ways" };
    await mount(de1());
    const list = document.querySelector("[role=tablist]")!;
    expect(list.className).not.toContain("max-md:hidden");
    expect(panel().className).not.toContain("max-md:hidden");
    await click(lane("via:nod_msk1"));
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ search: { tab: "ways", way: "via:nod_msk1" }, replace: true }));
  });
});

describe("the wizard Make reachable", () => {
  const step = () => dialog().querySelector<HTMLElement>("[data-testid=relay-wizard]")!.dataset.step;
  const entries = () => [...dialog().querySelectorAll<HTMLElement>("[data-testid=way-entry]")];
  const finishOk = () => {
    createRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false }) });
    checkRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, check: { ok: true, leg: { status: "ok", atUnix: now } } }) });
    updateRelay.mockResolvedValue({ relay: relay({ id: "rly_9" }) });
  };

  it("opens at the entries: the node people see where de1 is dark comes first, with what is known of it and a tag, and the steps say where we are", async () => {
    listRelays.mockResolvedValue({ relays: [] });
    search = { tab: "ways", add: "1" };
    await mount(de1());
    expect(dialog().textContent).toContain("Make reachable from Russia");
    expect(step()).toBe("2");
    expect([...dialog().querySelectorAll(".wy-steps li")].map((l) => l.className)).toEqual(["done", "cur", "", ""]);
    expect(dialog().textContent).toContain("Through which node should people go to de1");
    expect(entries().map((e) => e.querySelector("b .mono")?.textContent)).toEqual(["msk1", "fi1"]);
    expect(entries()[0]!.textContent).toContain("Fits");
    expect(entries()[0]!.textContent).toContain("msk1 → de1 passes (TSPU check)");
    expect(entries()[1]!.textContent).not.toContain("Fits");
    expect(entries()[1]!.textContent).toContain("fi1 is dark too from MTS");
    // the first is chosen, as the board does, and the footer names it
    expect(entries()[0]!.getAttribute("aria-checked")).toBe("true");
    expect(dialog().textContent).toContain("Entry: msk1");
  });

  it("an entry in the exit's own country is no good fit: no tag, and its reason", async () => {
    // de2 is seen from every network, as msk1 is, but it sits in Germany like de1: the people's route to de1 would not change
    nodes.push({ id: "nod_de2", name: "de2", countryCode: "DE", status: NodeStatus.ONLINE });
    countryChecks = [...countryChecks, country("nod_de2", CountryVerdict.OK)];
    listRelays.mockResolvedValue({ relays: [] });
    try {
      search = { tab: "ways", add: "1" };
      await mount(de1());
      const de2 = entries().find((e) => e.textContent?.includes("de2"))!;
      expect(de2.textContent).not.toContain("Fits");
      expect(de2.textContent).toContain("Same country as the exit");
      await click(de2);
      await click(buttonIn(dialog(), "Next"));
      await settle();
      expect(listRelayCandidates).toHaveBeenCalledWith({ viaNodeId: "nod_de2" }, expect.anything());
    } finally {
      nodes.pop();
    }
  });

  it("Back from the entries shows the node, chosen already, and what is the matter with it", async () => {
    listRelays.mockResolvedValue({ relays: [] });
    search = { tab: "ways", add: "1" };
    await mount(de1());
    await click(buttonIn(dialog(), "Back"));
    expect(step()).toBe("1");
    expect(dialog().textContent).toContain("The wizard was opened from the node’s page, so the node is already chosen");
    expect(dialog().querySelector("[role=checkbox]")!.textContent).toContain("de1");
    expect(dialog().querySelector("[role=checkbox]")!.textContent).toContain("partly");
    await click(buttonIn(dialog(), "Next"));
    expect(step()).toBe("2");
  });

  it("an entry that carries de1 already is not offered again", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    search = { tab: "ways", add: "1" };
    await mount(de1());
    expect(entries().map((e) => e.querySelector("b .mono")?.textContent)).toEqual(["fi1"]);
  });

  it("the servers of de1 through the entry are ticked, what people get is said, and the run makes the relays, turns them on and ends in the way it made", async () => {
    listRelays.mockResolvedValue({ relays: [] });
    listRelayCandidates.mockResolvedValue({
      agentSupported: true,
      entryCountry: { verdict: "ok", dark: [], atUnix: now },
      candidates: [cand({ audience: { people: 14, staticAwgDevices: 0 } }), cand({ inboundId: "inb_f", nodeId: "nod_fi1", nodeName: "fi1", profileName: "Trojan" })],
    });
    search = { tab: "ways", add: "1" };
    await mount(de1());
    await click(buttonIn(dialog(), "Next"));
    await settle();
    expect(step()).toBe("3");
    const servers = [...dialog().querySelectorAll("[data-testid=wizard-server]")];
    expect(servers.map((s) => s.textContent)).toEqual(["VLESS · XHTTPtcp/2053 on msk1"]); // fi1's server is not a way in to de1
    expect(servers[0]!.getAttribute("aria-checked")).toBe("true");
    expect(dialog().textContent).toContain("1 of 1 servers");
    expect(dialog().textContent).toContain("14 people get “DE · via RU”: 1 more server of de1 through msk1, beside the old ones");
    expect(dialog().textContent).toContain("The old ways stay, the app picks a working one itself");
    expect(dialog().textContent).toContain("msk1 sees people’s addresses and volumes, but not the content");
    expect(dialog().textContent).toContain("1 relay through msk1");
    finishOk();
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    expect(createRelay).toHaveBeenCalledWith({ inboundId: "inb_x", viaNodeId: "nod_msk1", port: 0 });
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_9", enabled: true });
    expect(step()).toBe("4");
    expect(dialog().textContent).toContain("Done · 1 of 1");
    expect(dialog().querySelector("[data-testid=wizard-run-row]")!.getAttribute("data-state")).toBe("ok");
    expect(dialog().textContent).toContain("de1 is reachable through msk1");
    expect(dialog().textContent).toContain("People get the new servers with the next subscription update");
    expect([...dialog().querySelectorAll(".wy-steps li")].map((l) => l.className)).toEqual(["done", "done", "done", "done"]);
    await click(buttonIn(dialog(), "Done"));
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: { tab: "ways", way: "via:nod_msk1" } }));
  });

  it("a server can be taken out of the run: the count and the call follow", async () => {
    listRelays.mockResolvedValue({ relays: [] });
    listRelayCandidates.mockResolvedValue({
      agentSupported: true,
      entryCountry: { verdict: "ok", dark: [], atUnix: now },
      candidates: [cand({}), cand({ inboundId: "inb_y", profileName: "Hysteria2", protocol: "hysteria2", network: "udp", port: 443 })],
    });
    search = { tab: "ways", add: "via:nod_msk1" };
    await mount(de1());
    expect(step()).toBe("3");
    expect(dialog().textContent).toContain("2 of 2 servers");
    await click([...dialog().querySelectorAll("[data-testid=wizard-server]")].find((s) => s.textContent?.includes("Hysteria2")));
    expect(dialog().textContent).toContain("1 of 2 servers");
    expect(dialog().textContent).toContain("1 relay through msk1");
    finishOk();
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    expect(createRelay).toHaveBeenCalledTimes(1);
    expect(createRelay).toHaveBeenCalledWith(expect.objectContaining({ inboundId: "inb_x" }));
  });

  it("a server dragged onto the entry on the graph opens the wizard with that server alone ticked: via:<entry>/<server>", async () => {
    listRelays.mockResolvedValue({ relays: [] });
    listRelayCandidates.mockResolvedValue({
      agentSupported: true,
      entryCountry: { verdict: "ok", dark: [], atUnix: now },
      candidates: [cand({}), cand({ inboundId: "inb_y", profileName: "Hysteria2", protocol: "hysteria2", network: "udp", port: 443 })],
    });
    search = { tab: "ways", add: "via:nod_msk1/inb_y" };
    await mount(de1());
    expect(step()).toBe("3");
    expect(dialog().textContent).toContain("1 of 2 servers");
    const on = [...dialog().querySelectorAll("[data-testid=wizard-server]")].filter((s) => s.getAttribute("aria-checked") === "true");
    expect(on.map((s) => s.textContent)).toEqual([expect.stringContaining("Hysteria2")]);
    // the others can still be ticked
    await click([...dialog().querySelectorAll("[data-testid=wizard-server]")].find((s) => !s.textContent?.includes("Hysteria2")));
    expect(dialog().textContent).toContain("2 of 2 servers");
  });

  it("a pair that the entry reaches only over a bridge says so: the bridge chip, the note, and the UDP server that cannot go", async () => {
    listRelays.mockResolvedValue({ relays: [] });
    const cut = { verdict: "blocked", source: "tspu_row", atUnix: now };
    const back = { verdict: "reach", source: "exit_probe", atUnix: now };
    listRelayCandidates.mockResolvedValue({
      agentSupported: true,
      entryCountry: { verdict: "ok", dark: [], atUnix: now },
      candidates: [cand({ reach: cut, exitReachesEntry: back }), cand({ inboundId: "inb_y", profileName: "Hysteria2", protocol: "hysteria2", network: "udp", port: 443, reach: cut, exitReachesEntry: back, reverseBlocked: "udp" })],
    });
    search = { tab: "ways", add: "via:nod_msk1" };
    await mount(de1());
    expect(dialog().textContent).toContain("Will go through the bridge: msk1 does not reach de1 directly, de1 will connect to msk1 itself");
    expect(dialog().textContent).toContain("Through the bridge de1 does not see the addresses of people");
    const udp = [...dialog().querySelectorAll("[data-testid=wizard-server]")].find((s) => s.textContent?.includes("Hysteria2"))!;
    expect(udp.getAttribute("aria-checked")).toBe("false");
    expect(udp.textContent).toContain("does not go over the bridge yet");
    expect(dialog().textContent).toContain("1 of 2 servers");
  });

  it("?add=via:<entry> opens straight at that entry's servers, and Back returns to the choice of node", async () => {
    listRelays.mockResolvedValue({ relays: [] });
    search = { tab: "ways", add: "via:nod_msk1" };
    await mount(de1());
    expect(step()).toBe("3");
    expect(dialog().textContent).toContain("1 relay through msk1");
    await click(buttonIn(dialog(), "Back"));
    await settle();
    expect(step()).toBe("2");
    expect(dialog().textContent).toContain("Through which node");
    // the one that was in hand stays in the list
    expect(entries().map((e) => e.querySelector("b .mono")?.textContent)).toContain("msk1");
  });

  it("a refusal of the panel says why in the row and offers another entry", async () => {
    listRelays.mockResolvedValue({ relays: [] });
    const { ConnectError, Code } = await import("@connectrpc/connect");
    createRelay.mockRejectedValue(new ConnectError("port_taken: free=8444", Code.AlreadyExists));
    search = { tab: "ways", add: "via:nod_msk1" };
    await mount(de1());
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    const row = dialog().querySelector("[data-testid=wizard-run-row]")!;
    expect(row.getAttribute("data-state")).toBe("bad");
    expect(dialog().textContent).toContain("de1 — did not work out");
    // the port the panel names is one click away: that server runs again on it, and the result is counted
    expect(buttonIn(dialog(), "Take 8444")).toBeDefined();
    createRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false }) });
    checkRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, check: { ok: true, leg: { status: "ok", atUnix: now } } }) });
    updateRelay.mockResolvedValue({ relay: relay({ id: "rly_9" }) });
    await click(buttonIn(dialog(), "Take 8444"));
    await settle();
    expect(createRelay).toHaveBeenLastCalledWith(expect.objectContaining({ port: 8444 }));
    expect(dialog().querySelector("[data-testid=wizard-run-row]")!.getAttribute("data-state")).toBe("ok");
    expect(dialog().textContent).toContain("Done · 1 of 1");
    expect(dialog().textContent).toContain("de1 is reachable through msk1");
  });

  it("another entry after a failure goes back to the choice of node", async () => {
    listRelays.mockResolvedValue({ relays: [] });
    const { ConnectError, Code } = await import("@connectrpc/connect");
    createRelay.mockRejectedValue(new ConnectError("timeout", Code.Unavailable));
    search = { tab: "ways", add: "via:nod_msk1" };
    await mount(de1());
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    await click(buttonIn(dialog(), "Another entry…"));
    expect(step()).toBe("2");
  });

  it("the window can be closed over a run: the run goes on, the strip says how far it is and opens the window again", async () => {
    listRelays.mockResolvedValue({ relays: [] });
    createRelay.mockReturnValue(new Promise(() => {})); // the first server never comes back: the run stays at 0 of 1
    search = { tab: "ways", add: "via:nod_msk1" };
    await mount(de1());
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    expect(step()).toBe("4");
    expect(dialog().textContent).toContain("Checking and turning on · 0 of 1");
    expect(dialog().textContent).toContain("You can close the window: the run goes on");
    expect(buttonIn(dialog(), "Stop")).toBeDefined();
    await click(buttonIn(dialog(), "Close"));
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: { tab: "ways" } }));
    // the page goes on without the window: the strip over the ways
    search = { tab: "ways" };
    await act(async () => root!.render(ui(de1())));
    await settle();
    expect(document.querySelector("[role=dialog]")).toBeNull();
    const strip = document.querySelector<HTMLElement>("[data-testid=relay-run-strip]")!;
    expect(strip.textContent).toContain("Turning on relays through msk1 · 0 of 1");
    expect(buttonIn(strip, "Stop")).toBeDefined();
    await click(buttonIn(strip, "Open"));
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: expect.objectContaining({ add: "via:nod_msk1" }) }));
    // and the window, asked for again, opens at the end of the run
    search = { tab: "ways", add: "via:nod_msk1" };
    await act(async () => root!.render(ui(de1())));
    await settle();
    expect(step()).toBe("4");
    expect(dialog().textContent).toContain("Checking and turning on · 0 of 1");
  });

  it("Stop leaves what is on, and says that the rest is not touched", async () => {
    listRelays.mockResolvedValue({ relays: [] });
    listRelayCandidates.mockResolvedValue({
      agentSupported: true,
      entryCountry: { verdict: "ok", dark: [], atUnix: now },
      candidates: [cand({}), cand({ inboundId: "inb_z", profileName: "VLESS · gRPC", port: 2054 })],
    });
    let release: (v: unknown) => void = () => {};
    createRelay.mockReturnValueOnce(new Promise((r) => (release = r)));
    checkRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, check: { ok: true, leg: { status: "ok", atUnix: now } } }) });
    updateRelay.mockResolvedValue({ relay: relay({ id: "rly_9" }) });
    search = { tab: "ways", add: "via:nod_msk1" };
    await mount(de1());
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    await click(buttonIn(dialog(), "Stop"));
    expect(buttonIn(dialog(), "Stopping after this one…")).toBeDefined();
    await act(async () => release({ relay: relay({ id: "rly_9", enabled: false }) }));
    await settle();
    expect(createRelay).toHaveBeenCalledTimes(1);
    expect(dialog().textContent).toContain("Stopped · 1 of 2");
    expect(dialog().textContent).toContain("What is on stays, the rest is not touched");
    const rows = [...dialog().querySelectorAll("[data-testid=wizard-run-row]")].map((r) => r.textContent);
    expect(rows[1]).toContain("not started");
  });

  it("relays that a run left (made, checked, never turned on) are one strip with Finish it; the wizard then has them ticked and makes nothing twice", async () => {
    const left = relay({ id: "rly_off", enabled: false, appliedUnix: 0, state: InboundState.PENDING, check: { ok: true, leg: { status: "ok", atUnix: now - 60 } } });
    listRelays.mockResolvedValue({ relays: [left] });
    await mount(de1());
    const strip = document.querySelector<HTMLElement>("[data-testid=relay-unfinished]")!;
    expect(strip.textContent).toContain("Not finished");
    expect(strip.textContent).toContain("1 relay through msk1 was made but not turned on.");
    await click(buttonIn(strip, "Finish it"));
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: expect.objectContaining({ add: "via:nod_msk1" }) }));
    // the wizard, at the servers of msk1: the relay that is there is ticked and says what it is
    listRelayCandidates.mockResolvedValue({ agentSupported: true, entryCountry: { verdict: "ok", dark: [], atUnix: now }, candidates: [cand({ inboundId: "inb_v", relayId: "rly_off", profileName: "VLESS · TCP", port: 443 })] });
    search = { tab: "ways", add: "via:nod_msk1" };
    await act(async () => root!.render(ui(de1())));
    await settle();
    const row = dialog().querySelector("[data-testid=wizard-server]")!;
    expect(row.getAttribute("aria-checked")).toBe("true");
    expect(row.textContent).toContain("made, not turned on");
    checkRelay.mockResolvedValue({ relay: { ...left, check: { ok: true, leg: { status: "ok", atUnix: now } } } });
    updateRelay.mockResolvedValue({ relay: { ...left, enabled: true } });
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    expect(createRelay).not.toHaveBeenCalled();
    expect(checkRelay).toHaveBeenCalledWith(expect.objectContaining({ id: "rly_off" }));
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_off", enabled: true });
  });

  it("a relay that was turned off by hand has been on once: it is no strip", async () => {
    listRelays.mockResolvedValue({ relays: [relay({ enabled: false })] });
    await mount(de1());
    expect(document.querySelector("[data-testid=relay-unfinished]")).toBeNull();
  });

  it("a reader gets no wizard and no strip", async () => {
    role = Role.HELPER;
    search = { tab: "ways", add: "1" };
    await mount(de1());
    expect(document.querySelector("[role=dialog]")).toBeNull();
    expect(document.querySelector("[data-testid=relay-strip]")).toBeNull();
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
    expect(text()).toContain("through another node or over IPv6, if people’s network does not see this node");
  });

  it("an entry node lists the nodes reached through it, with a way to relay another", async () => {
    listRelays.mockResolvedValue({ relays: [relay(), relay({ id: "rly_2", inboundId: "inb_h", profileName: "Hysteria2", enabled: false, state: InboundState.PENDING, check: { ok: false, stage: "leg", leg: { status: "fail", code: "timeout", atUnix: now } } })] });
    const msk1 = { ...(de1() as object), node: { id: "nod_msk1", name: "msk1", countryCode: "RU", address: "198.51.100.20", status: NodeStatus.ONLINE, bandwidthMbps: 0 }, facts: {} } as never;
    await mount(msk1);
    expect(text()).toContain("Whom it carries");
    expect(lane("out:nod_de1").textContent).toContain("2 servers · forwarding");
    expect(lane("out:nod_de1").textContent).toContain("Partly down");
    expect(lane("out:nod_de1").getAttribute("aria-selected")).toBe("true"); // the lane with a problem opens first
    expect(text()).toContain("Carry more nodes…");
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

describe("the IPv6 entry", () => {
  const v6check = (over: Record<string, unknown> = {}) => ({ ...country("nod_de1", CountryVerdict.OK), ...over });
  const withV6 = (v4: CountryVerdict, v6v: CountryVerdict) => ({ ...country("nod_de1", v4, v4 === CountryVerdict.OK ? [] : ["MTS"]), ipv6: v6check({ verdict: v6v, globalpingVerdict: v6v }) });
  const radios = () => [...dialog().querySelectorAll<HTMLInputElement>("input[type=radio]")];

  it("off: the card says where subscriptions connect, that IPv4 is cut where IPv6 gets through, and offers the entry to the owner", async () => {
    countryChecks = [withV6(CountryVerdict.BLOCKED, CountryVerdict.OK)];
    search = { tab: "ways", way: "ipv6" };
    await mount(de1());
    expect(panel().textContent).toContain("Off. Subscriptions connect to 203.0.113.7 only.");
    expect(panel().textContent).toContain("IPv4 is blocked from Russia, IPv6 gets through.");
    expect(panel().querySelector("[role=status], [role=img]")?.textContent ?? "").not.toContain("Not available");
    await click(buttonIn(panel(), "Add IPv6 entry…"));
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: { tab: "ways", way: "ipv6", add: "ipv6" } }));
  });

  it("off on a node every network sees: the entry would be a spare way in", async () => {
    countryChecks = [withV6(CountryVerdict.OK, CountryVerdict.OK)];
    search = { tab: "ways", way: "ipv6" };
    await mount(de1());
    expect(panel().textContent).toContain("IPv4 is not blocked from Russia now: this would be a spare way in.");
  });

  it("the dialog picks the address the server sends from, marks it, warns about static AmneziaWG keys, and turns the entry on", async () => {
    search = { tab: "ways", way: "ipv6", add: "ipv6" };
    const data = { ...(de1() as object), inbounds: [{ id: "inb_a", port: 51820, state: InboundState.ACTIVE, protocol: "awg", profileName: "AWG" }] } as never;
    await mount(data);
    expect(dialog().textContent).toContain("IPv6 entry on de1");
    expect(dialog().textContent).toContain("People get one more server, “de1 · IPv6”, next to each of this node’s servers.");
    expect(radios().map((r) => [r.value, r.checked])).toEqual([
      ["2001:db8::5", false],
      ["2001:db8::7", true],
    ]);
    expect(radios()[1]!.closest("label")!.textContent).toBe("2001:db8::7(the server’s address)");
    expect(dialog().textContent).toContain("Static AmneziaWG keys (.conf, vpn://) get no IPv6 server: they keep connecting to 203.0.113.7.");

    updateIpv6Entry.mockResolvedValue({ node: {}, errorCode: "", check: passed() });
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    expect(updateIpv6Entry).toHaveBeenCalledWith({ nodeId: "nod_de1", action: Ipv6EntryAction.ENABLE, address: "2001:db8::7" });
    expect(text()).toContain("The IPv6 entry on de1 is on: 2001:db8::7");
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: { tab: "ways", way: "ipv6" } }));
  });

  it("while the check runs the window says so and waits; a check that fails keeps the window with the ports and changes nothing", async () => {
    search = { tab: "ways", way: "ipv6", add: "ipv6" };
    await mount(de1());
    await click(radios()[0]);
    let answer: (v: unknown) => void = () => {};
    updateIpv6Entry.mockReturnValue(new Promise((r) => (answer = r)));
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    expect(dialog().querySelector("[data-testid=ipv6-checking]")?.textContent).toContain("Checking 2001:db8::5 from outside…");
    expect(buttonIn(dialog(), "Only check")?.disabled).toBe(true);
    expect(radios().every((r) => r.disabled)).toBe(true);

    const verdicts = [
      { network: "tcp", port: 443, verdict: "timeout" },
      { network: "udp", port: 8443, verdict: "ok" },
    ];
    await act(async () => answer({ node: {}, errorCode: "check_failed", check: passed({ address: "2001:db8::5", passed: false, verdicts }) }));
    await settle();
    const result = dialog().querySelector<HTMLElement>("[data-testid=ipv6-check]")!;
    expect(result.dataset.tone).toBe("bad");
    expect(result.textContent).toContain("2001:db8::5 is not reachable from outside: tcp/443 no answer (filtered). Nothing changed.");
    expect([...result.querySelectorAll("li")].map((l) => l.textContent)).toEqual(["tcp/443No answer (filtered)", "udp/8443OK"]);
    expect(result.lastElementChild?.textContent).toBe("10 min ago · from fi1");
    expect(updateIpv6Entry).toHaveBeenCalledWith({ nodeId: "nod_de1", action: Ipv6EntryAction.ENABLE, address: "2001:db8::5" });
  });

  it("Only check answers in the window: it passes, or nothing can check it", async () => {
    search = { tab: "ways", way: "ipv6", add: "ipv6" };
    await mount(de1());
    checkIpv6.mockResolvedValueOnce(passed({ checkedUnix: now }));
    await click(buttonIn(dialog(), "Only check"));
    await settle();
    expect(checkIpv6).toHaveBeenCalledWith({ nodeId: "nod_de1", address: "2001:db8::7" });
    expect(updateIpv6Entry).not.toHaveBeenCalled();
    expect(dialog().querySelector("[data-testid=ipv6-check]")?.textContent).toContain("2001:db8::7 answers from outside: checked just now from fi1.");

    checkIpv6.mockResolvedValueOnce(passed({ passed: false, errorCode: "no_ipv6_verifier", verdicts: [], sender: "" }));
    await click(buttonIn(dialog(), "Only check"));
    await settle();
    expect(dialog().querySelector("[data-testid=ipv6-check]")?.textContent).toBe(`2001:db8::7 was not checked. ${en["ways.ipv6.why.no_ipv6_verifier"]}10 min ago`);
  });

  it("on: the address, when and by whom it was checked (Globalping said as a weaker proof), and Turn off behind a confirmation", async () => {
    search = { tab: "ways", way: "ipv6" };
    await mount(de1({ ipv6: v6({ entry: "2001:db8::7", lastCheck: passed({ sender: "globalping", verdicts: [{ network: "tcp", port: 443, verdict: "reachable" }] }) }) }));
    expect(lane("ipv6").dataset.ring).toBe("ok");
    expect(lane("ipv6").textContent).toContain("2001:db8::7");
    expect(panel().textContent).toContain("On: 2001:db8::7. Checked 10 min ago from Globalping. Subscriptions show “de1 · IPv6”.");
    expect(panel().textContent).toContain(en["ways.ipv6.globalping"]);
    expect(buttonIn(panel(), "Add IPv6 entry…")).toBeUndefined();

    updateIpv6Entry.mockResolvedValue({ node: {}, errorCode: "" });
    await click(buttonIn(panel(), "Turn off"));
    expect(dialog().textContent).toContain("Turn off the IPv6 entry on de1?");
    expect(dialog().textContent).toContain("“de1 · IPv6” leaves the subscriptions; apps drop it at their next refresh.");
    await click(buttonIn(dialog(), "Turn off"));
    await settle();
    expect(updateIpv6Entry).toHaveBeenCalledWith({ nodeId: "nod_de1", action: Ipv6EntryAction.DISABLE });
    expect(text()).toContain("The IPv6 entry on de1 is off");
  });

  it("on while the server's IPv6 is down: the lane is down and the card asks to turn it off", async () => {
    search = { tab: "ways", way: "ipv6" };
    await mount(de1({ ipv6: v6({ entry: "2001:db8::7", entrySupported: false, unavailableReason: "ipv6_broken" }) }));
    expect(lane("ipv6").dataset.ring).toBe("bad");
    expect(lane("ipv6").textContent).toContain("IPv6 does not work on this server at the hoster.");
    expect(panel().textContent).toContain("On: 2001:db8::7. Subscriptions show “de1 · IPv6”.");
    expect(panel().textContent).toContain("Turn the entry off until it works again.");
    expect(buttonIn(panel(), "Turn off")).toBeDefined();
  });

  it("a node that cannot take one shows why, with no button", async () => {
    search = { tab: "ways", way: "ipv6" };
    await mount(de1({ ipv6: v6({ entrySupported: false, unavailableReason: "agent_old" }) }));
    expect(lane("ipv6").textContent).toContain("not available");
    expect(panel().textContent).toContain("Update the node agent to use an IPv6 entry.");
    expect(panel().textContent).toContain("Not available");
    expect(buttonIn(panel(), "Add IPv6 entry…")).toBeUndefined();
  });

  it("a helper sees the state and the last check, and nothing to press; ?add=ipv6 opens nothing", async () => {
    role = Role.HELPER;
    search = { tab: "ways", way: "ipv6", add: "ipv6" };
    await mount(de1({ ipv6: v6({ lastCheck: passed({ passed: false, verdicts: [{ network: "udp", port: 443, verdict: "broken" }] }) }) }));
    expect(panel().textContent).toContain("Off. Subscriptions connect to 203.0.113.7 only.");
    expect(panel().textContent).toContain("2001:db8::7 is not reachable from outside: udp/443 packets do not arrive. Nothing changed.");
    expect(panel().querySelectorAll("button")).toHaveLength(0);
    expect(document.querySelector("[role=dialog]")?.textContent ?? null).toBeNull();
  });
});

describe("servers in subscriptions", () => {
  const vless = { id: "inb_v", port: 443, state: InboundState.ACTIVE, protocol: "vless", profileName: "VLESS · TCP", directInSubscriptions: true, cdn: false };
  const cdn = { id: "inb_c", port: 443, state: InboundState.ACTIVE, protocol: "vless", profileName: "VLESS · CDN", directInSubscriptions: true, cdn: true };
  const withInbounds = (inbounds: object[]) => ({ ...(de1() as object), inbounds }) as never;

  it("Direct has a switch per server; off asks the server first and, when no way would be left, confirms with the reason", async () => {
    listRelays.mockResolvedValue({ relays: [] });
    updateInbound.mockImplementation((req: { validateOnly?: boolean }) =>
      Promise.resolve({ warnings: req.validateOnly ? [{ code: "inbound_no_way_in", params: { profile: "VLESS · TCP", node: "de1", reason: "no_usable_relay" } }] : [] }),
    );
    await mount(withInbounds([vless]));
    expect(panel().textContent).toContain(en["ways.subs.title"]);
    const sw = panel().querySelector<HTMLElement>("[role=switch]")!;
    expect(sw.getAttribute("aria-label")).toBe("VLESS · TCP in subscriptions");
    await click(sw);
    await settle();
    expect(updateInbound).toHaveBeenCalledWith({ inboundId: "inb_v", directInSubscriptions: false, validateOnly: true });
    expect(updateInbound).toHaveBeenCalledTimes(1); // nothing saved before the owner confirms
    expect(dialog().textContent).toContain("Take VLESS · TCP out of subscriptions?");
    expect(dialog().textContent).toContain(en["ways.subs.reason.no_usable_relay"]);
    await click(buttonIn(dialog(), en["ways.subs.hide"]));
    await settle();
    expect(updateInbound).toHaveBeenLastCalledWith({ inboundId: "inb_v", directInSubscriptions: false });
  });

  it("off with a way left (a relay through msk1) saves at once, without asking", async () => {
    updateInbound.mockResolvedValue({ warnings: [] });
    await mount(withInbounds([vless]));
    await click(panel().querySelector("[role=switch]"));
    await settle();
    expect(text()).not.toContain("Take VLESS · TCP out of subscriptions?");
    expect(updateInbound).toHaveBeenCalledTimes(2); // the check, then the save
    expect(updateInbound).toHaveBeenLastCalledWith({ inboundId: "inb_v", directInSubscriptions: false });
    expect(text()).toContain(en["common.saved"]);
  });

  it("a CDN profile is a lane of its own; Direct with every server out of subscriptions says so", async () => {
    search = { tab: "ways", way: "cdn" };
    await mount(withInbounds([{ ...vless, directInSubscriptions: false }, cdn]));
    expect(lanes().map((l) => l.id)).toEqual(["way-tab-direct", "way-tab-cdn", "way-tab-via:nod_msk1", "way-tab-ipv6"]);
    expect(lane("direct").dataset.ring).toBe("off");
    expect(lane("direct").textContent).toContain(en["ways.pill.hidden"]);
    expect(lane("cdn").textContent).toContain(en["ways.cdn"]);
    expect(lane("cdn").textContent).toContain(en["ways.cdn.seen"]);
    // the CDN lane's card lists only the CDN server
    expect(panel().textContent).toContain(en["ways.cdn.sub"]);
    expect([...panel().querySelectorAll("[role=switch]")].map((e) => e.getAttribute("aria-label"))).toEqual(["VLESS · CDN in subscriptions"]);
  });

  it("a helper sees the switches but cannot flip them", async () => {
    role = Role.HELPER;
    await mount(withInbounds([vless]));
    const sw = panel().querySelector<HTMLElement>("[role=switch]")!;
    expect(sw.hasAttribute("data-disabled") || sw.getAttribute("aria-disabled") === "true").toBe(true);
  });
});

function Strip({ data }: { data: never }) {
  const w = useWays(data);
  return <WaysStrip data={data} w={w} />;
}

describe("the Overview's strip", () => {
  it("names two dark networks and counts the rest, with the whole list in the tooltip, so a long list stays inside the card", async () => {
    countryChecks = [
      country("nod_de1", CountryVerdict.PARTIAL, ["MTS", "Beeline", "MegaFon", "Tele2", "Rostelecom"]),
      country("nod_msk1", CountryVerdict.OK),
      country("nod_fi1", CountryVerdict.PARTIAL, ["MTS"]),
    ];
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () =>
      root!.render(
        <QueryClientProvider client={qc}>
          <ToastProvider>
            <Strip data={de1()} />
          </ToastProvider>
        </QueryClientProvider>,
      ),
    );
    await settle();
    const direct = [...document.querySelectorAll("a")].find((a) => a.textContent?.startsWith("Direct"))!;
    expect(direct.textContent).toContain("Beeline, MTS +3");
    expect(direct.textContent).not.toContain("Rostelecom");
    const title = direct.getAttribute("title")!;
    expect(title.startsWith("Direct · not seen on: ")).toBe(true);
    for (const n of ["MTS", "Beeline", "MegaFon", "Tele2", "Rostelecom"]) expect(title).toContain(n);
    expect(direct.className).toContain("max-w-full");
  });

  it("an entry node has a second line, Carries: a chip per exit it passes on, the ones on the bridge say so, its load at the end", async () => {
    listRelays.mockResolvedValue({ relays: [relay(), relay({ id: "rly_2", exitNodeId: "nod_fi1", exitNodeName: "fi1", inboundId: "inb_f", profileName: "Trojan", activeMode: "reverse" })] });
    const msk1 = { ...(de1() as object), node: { id: "nod_msk1", name: "msk1", countryCode: "RU", address: "198.51.100.20", status: NodeStatus.ONLINE, bandwidthMbps: 1000 }, metrics: { cpuPct: 20, netRxBps: 340_000_000, netTxBps: 100_000_000, atUnix: Math.floor(Date.now() / 1000) }, facts: {} } as never;
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () =>
      root!.render(
        <QueryClientProvider client={qc}>
          <ToastProvider>
            <Strip data={msk1} />
          </ToastProvider>
        </QueryClientProvider>,
      ),
    );
    await settle();
    const line = document.querySelector<HTMLElement>('section[aria-label="Carries"]')!;
    expect(line).not.toBeNull();
    expect(line.textContent).toContain("msk1");
    expect([...line.querySelectorAll("a")].map((a) => a.textContent)).toEqual(["de1", "fi1· bridge"]);
    expect(line.textContent).toContain("load 34 %");
    // the exit has no such line
    act(() => root!.unmount());
    host.remove();
  });
});
