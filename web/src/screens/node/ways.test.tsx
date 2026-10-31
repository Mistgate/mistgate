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
import { useWays, WaysTab } from "./ways";

const listRelays = vi.fn();
const listRelayCandidates = vi.fn();
const createRelay = vi.fn();
const checkRelay = vi.fn();
const updateRelay = vi.fn();
const checkIpv6 = vi.fn();
const updateIpv6Entry = vi.fn();
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
  for (const m of [listRelays, listRelayCandidates, createRelay, checkRelay, updateRelay, checkIpv6, updateIpv6Entry, navigate]) m.mockReset();
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
  it("draws direct (half seen), through msk1 (works, seen where de1 is dark) and IPv6 (off); the add branch is amber and says why", async () => {
    await mount(de1());
    expect(lanes().map((l) => l.id)).toEqual(["way-tab-direct", "way-tab-via:nod_msk1", "way-tab-ipv6"]);
    expect(lanes().map((l) => l.dataset.ring)).toEqual(["half", "ok", "off"]);
    expect(lane("direct").textContent).toContain("203.0.113.7 · 1 port");
    expect(lane("direct").textContent).toContain("Dark on MTS");
    expect(lane("via:nod_msk1").textContent).toContain("People reach msk1 from MTS");
    expect(lane("ipv6").textContent).toBe("IPv6 entryOffoff");
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
    // de1 can take an IPv6 entry: the option is a choice like the other
    expect(dialog().querySelector("[data-testid=way-option-off]")).toBeNull();
    expect([...dialog().querySelectorAll("button")].some((b) => b.textContent?.startsWith("IPv6 entry"))).toBe(true);

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

  it("a node that cannot take one shows why, with no button, and the New way in dialog keeps the option greyed with the reason", async () => {
    search = { tab: "ways", way: "ipv6" };
    await mount(de1({ ipv6: v6({ entrySupported: false, unavailableReason: "agent_old" }) }));
    expect(lane("ipv6").textContent).toContain("not available");
    expect(panel().textContent).toContain("Update the node agent to use an IPv6 entry.");
    expect(panel().textContent).toContain("Not available");
    expect(buttonIn(panel(), "Add IPv6 entry…")).toBeUndefined();

    search = { tab: "ways", add: "1" };
    await act(async () => root!.render(ui(de1({ ipv6: v6({ entrySupported: false, unavailableReason: "agent_old" }) }))));
    const off = dialog().querySelector<HTMLElement>("[data-testid=way-option-off]")!;
    expect(off.tagName).toBe("DIV");
    expect(off.textContent).toContain("Update the node agent to use an IPv6 entry.");
    expect(off.textContent).toContain("Not available");
    expect(off.querySelector("button, [tabindex]")).toBeNull();
  });

  it("an entry that is on is not offered again in the New way in dialog", async () => {
    search = { tab: "ways", add: "1" };
    await mount(de1({ ipv6: v6({ entry: "2001:db8::7" }) }));
    const off = dialog().querySelector<HTMLElement>("[data-testid=way-option-off]")!;
    expect(off.textContent).toContain("Already on: 2001:db8::7");
    expect(off.textContent).toContain("On");
  });

  it("picking IPv6 in the New way in dialog hands over to the entry's dialog", async () => {
    search = { tab: "ways", add: "1" };
    await mount(de1());
    await click([...dialog().querySelectorAll("button")].find((b) => b.textContent?.startsWith("IPv6 entry")));
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: { tab: "ways", way: "ipv6", add: "ipv6" } }));
  });

  it("a helper sees the state and the last check, and nothing to press; ?add=ipv6 opens nothing", async () => {
    role = Role.HELPER;
    search = { tab: "ways", way: "ipv6", add: "ipv6" };
    await mount(de1({ ipv6: v6({ lastCheck: passed({ passed: false, verdicts: [{ network: "udp", port: 443, verdict: "broken" }] }) }) }));
    expect(panel().textContent).toContain("Off. Subscriptions connect to 203.0.113.7 only.");
    expect(panel().textContent).toContain("2001:db8::7 is not reachable from outside: udp/443 packets do not arrive. Nothing changed.");
    expect(panel().querySelectorAll("button")).toHaveLength(0);
    expect(document.querySelector("[role=dialog]")).toBeNull();
  });
});
