import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CheckStatus, CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { setLang } from "@/i18n";
import { NodeChecks } from "@/screens/node/checks";
import { setEffectsEnabled } from "@/lib/effects";
import { ChecksTab } from "./checks";

const getChecks = vi.fn();
vi.mock("@/lib/api", () => ({
  health: { runChecksNow: () => Promise.resolve({ scheduled: 1, skipped: 0 }), getChecks: (...a: unknown[]) => getChecks(...a) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: 1 } }) },
  fleet: {},
  nodes: {},
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children?: ReactNode; to: string; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

// these read what the screen says: the numbers must not be mid-shuffle (the motion has its own tests)
beforeEach(() => setEffectsEnabled(false));

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

const history = Array.from({ length: 48 }, (_, i) => ({ startUnix: i * 1800, ok: 1, failed: 0, latencyMs: 40 }));
const cell = (status: CheckStatus, errorCode = "", latencyMs = 0) => ({
  deployed: true,
  inboundId: `inb_${errorCode || status}`,
  failStreak: 0,
  history,
  last: { status, atUnix: 100, latencyMs, exitIp: "", exitCountry: "", errorCode, errorDetail: "" },
});
const checks = {
  nowUnix: 200,
  intervalS: 300,
  columns: [
    { profileId: "prf_1", profileName: "hy2 · 443 · Salamander", protocol: "hysteria2" },
    { profileId: "prf_2", profileName: "hy2 · WARP · 8443", protocol: "hysteria2" },
  ],
  rows: [
    { nodeId: "nod_1", nodeName: "de1", countryCode: "DE", nodeStatus: NodeStatus.ONLINE, cells: [cell(CheckStatus.OK, "", 42), cell(CheckStatus.SKIPPED, "inbound_failed")] },
    { nodeId: "nod_2", nodeName: "de2", countryCode: "DE", nodeStatus: NodeStatus.ONLINE, cells: [cell(CheckStatus.SKIPPED, "inbound_disabled"), cell(CheckStatus.FAILED, "timeout")] },
    { nodeId: "nod_3", nodeName: "nl1", countryCode: "NL", nodeStatus: NodeStatus.DOWN, cells: [cell(CheckStatus.SKIPPED, "node_offline"), { deployed: false, inboundId: "", failStreak: 0, history: [] }] },
  ],
};

// the panel's server loses UDP: slow Hysteria2 connects on de2 and nl1, VLESS fine; de1's VLESS connects slowly too, just under the mark
const ok = (responseHourMs: number, connectHourMs: number) => ({ ...cell(CheckStatus.OK, "", 2658), responseHourMs, connectHourMs });
const udp = {
  ...checks,
  columns: [...checks.columns, { profileId: "prf_3", profileName: "VLESS · 443", protocol: "vless" }],
  rows: [
    { ...checks.rows[0]!, cells: [ok(48, 2600), checks.rows[0]!.cells[1]!, ok(61, 1999)] },
    { ...checks.rows[1]!, cells: [...checks.rows[1]!.cells, ok(70, 300)] },
    { ...checks.rows[2]!, cells: [...checks.rows[2]!.cells, ok(90, 2000)] },
  ],
  udpLoss: { nodeIds: ["nod_1", "nod_3"], sharePercent: 38, lostPermille: 31, nodeLostPermille: 55 },
};

async function mount(data: object = checks, first = false) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}><ChecksTab data={data as never} first={first} /></QueryClientProvider>));
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}


// ---------------------------------------------------------------------------------------------------
// Reachability from a country: the last column, the phone rows and the node page

const at = Math.floor(Date.now() / 1000) - 240;
const probe = (network: string, city: string, icmpMs: number, ports: [number, boolean][], valid = true) => ({
  network,
  asn: 1,
  city,
  icmpOk: icmpMs > 0,
  icmpMs,
  tcp: ports.map(([port, open]) => ({ port, ok: open, ms: open ? 40 : 0 })),
  valid,
  dark: false,
  eyeball: true,
});
// every field of CountryCheck, as the client gets them: the sources and the TSPU probe's detail are empty unless a case sets them
const verdict = (nodeId: string, v: CountryVerdict, probes: ReturnType<typeof probe>[], over: object = {}) => ({
  nodeId,
  country: "RU",
  verdict: v,
  atUnix: at,
  probes,
  reason: "",
  eyeball: true,
  confidence: "",
  listedBy: [] as string[],
  cdnProvider: "",
  cdnRegion: "",
  sources: [] as string[],
  globalpingVerdict: CountryVerdict.UNSPECIFIED,
  tspuVerdict: CountryVerdict.UNSPECIFIED,
  tspuPorts: [] as { port: number; verdict: string }[],
  dpiHop: 0,
  tspuVantageId: "",
  tspuVantageName: "",
  tspuAtUnix: 0,
  noProbe: [] as string[],
  vantages: [] as object[],
  sourceVerdicts: [] as object[],
  ...over,
});
const countryChecks = [
  verdict("nod_1", CountryVerdict.OK, [probe("Rostelecom", "Moscow", 42, [[443, true]]), probe("Beeline", "Kazan", 47, [[443, true]])]),
  verdict("nod_2", CountryVerdict.TCP_BLOCKED, [probe("Rostelecom", "Moscow", 51, [[443, false], [8443, false]]), probe("MegaFon", "Kazan", 0, [[443, false], [8443, false]], false)]),
  verdict("nod_3", CountryVerdict.BLOCKED, [probe("MTS", "Omsk", 0, [[443, false]])]),
];
const four = {
  ...checks,
  rows: [...checks.rows, { ...checks.rows[0]!, nodeId: "nod_4", nodeName: "fi1", countryCode: "FI" }, { ...checks.rows[0]!, nodeId: "nod_5", nodeName: "fi2", countryCode: "FI" }],
  countryChecks: [...countryChecks, verdict("nod_4", CountryVerdict.UNKNOWN, [probe("Selectel", "Moscow", 38, [[443, true]])], { reason: "too_few_probes", eyeball: false })],
};
/** The text of an element, the no-break space before a "·" as a plain one. */
const ruText = (e: Element) => e.textContent!.replaceAll(" ", " ");
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));


describe("the country row on the node page", () => {
  async function mountNode(data: object) {
    getChecks.mockResolvedValue(data);
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => root!.render(<QueryClientProvider client={qc}><NodeChecks nodeId="nod_2" /></QueryClientProvider>));
    for (let i = 0; i < 4; i++) await settle();
  }
  const own = { ...checks, rows: [checks.rows[1]!] };

  it("a round asked for: the rows are looked at (tint, glint over the strip, dots for the time) until a newer cell is there, then the strip slides and the new cell jumps out", async () => {
    const one = { ...checks, rows: [checks.rows[0]!], columns: [checks.columns[0]!] };
    getChecks.mockResolvedValue(one);
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => root!.render(<QueryClientProvider client={qc}><NodeChecks nodeId="nod_1" /></QueryClientProvider>));
    for (let i = 0; i < 4; i++) await settle();
    const row = () => host!.querySelector(".nd-pr")!;
    expect(row().classList.contains("now")).toBe(false);
    expect(host!.querySelector(".nd-tk.scan, .nd-ms.dots")).toBeNull();
    await act(async () => [...host!.querySelectorAll("button")].find((b) => b.textContent === "Check now")!.click());
    for (let i = 0; i < 4; i++) await settle();
    expect(row().classList.contains("now")).toBe(true);
    expect(row().querySelector(".nd-tk.scan")).not.toBeNull();
    expect(row().querySelector(".nd-ms.dots")?.textContent).toBe("");
    // the new cell comes
    const first = one.rows[0]!.cells[0] as ReturnType<typeof cell>;
    const newer = { ...one, rows: [{ ...one.rows[0]!, cells: [{ ...first, last: { ...first.last, atUnix: 400 } }] }] };
    getChecks.mockResolvedValue(newer);
    await act(async () => void (await qc.invalidateQueries({ queryKey: ["health", "checks"] })));
    for (let i = 0; i < 4; i++) await settle();
    expect(row().classList.contains("now")).toBe(false);
    expect(row().querySelector(".nd-tk.adv")).not.toBeNull();
    expect(row().querySelectorAll(".nd-tk i.new")).toHaveLength(1);
    expect(row().querySelector(".nd-ms.fresh")?.textContent).toContain("42");
  });

  it("comes after the checks with the same verdict and tooltip, and is not there while the integration is off", async () => {
    await mountNode({ ...own, countryChecks: [countryChecks[1]] });
    expect(getChecks).toHaveBeenCalledWith({ nodeId: "nod_2" }, expect.anything());
    const row = document.querySelector<HTMLElement>("[data-testid=node-ru]")!;
    expect(ruText(row)).toBe("From Russia the node’s TCP is blockedICMP passes · 1 network: Rostelecom · 4 min ago");
    expect(row.dataset.kind).toBe("warn");
    // its title leads to the Reachability section
    expect(row.querySelector('a[href="/reachability"]')?.textContent).toBe("From Russia the node’s TCP is blocked");
    await act(async () => row.querySelector<HTMLElement>("[tabindex]")!.focus());
    for (let i = 0; i < 3; i++) await settle();
    expect(document.body.textContent).toContain("ICMP 51 ms · TCP 443, 8443 ✕");
    act(() => root?.unmount());
    host?.remove();
    await mountNode({ ...own, countryChecks: [] });
    expect(document.querySelector("[data-testid=node-ru]")).toBeNull();
  });
});

// ---------------------------------------------------------------------------------------------------
// A block on some networks (PARTIAL) in the same column: de1 is dark on MTS for Globalping and on MTS in Crimea for bsbord, seen by
// Beeline and nine cheburcheck probes, with a Crimean unit under the white list; nl1 fully blocked with every probe stored dark

const dark = (network: string, asn: number, city: string) => ({ ...probe(network, city, 0, [[443, false]]), asn, dark: true });
const vantage = (source: string, network: string, state: string, over: object = {}) => ({
  source,
  key: `${source}:${network}`,
  network,
  region: "",
  asn: "",
  kind: source === "bsbord" ? "mobile" : "unknown",
  state,
  atUnix: at,
  dpiHop: 0,
  icmpOk: state === "reach",
  tcpOk: state === "reach",
  ...over,
});
const cities = ["Moscow", "Kazan", "Omsk", "Perm", "Tula", "Tver", "Ufa", "Samara", "Sochi"];
const someVantages = [
  vantage("globalping", "MTS", "dark", { key: "AS8359", asn: "8359", region: "Novosibirsk" }),
  vantage("globalping", "Beeline", "reach", { key: "AS3216", asn: "3216", region: "Moscow" }),
  ...cities.map((region, i) => vantage("cheburcheck", "Rostelecom", "reach", { key: `probe:${i}`, asn: "12389", region, atUnix: at - 2 * 3600 })),
  vantage("bsbord", "MTS", "dark", { key: "mts|krym|off", region: "КРЫМ", atUnix: at - 3 * 3600 }),
  vantage("bsbord", "Volna", "whitelist", { key: "volna|krym|on", region: "КРЫМ", atUnix: at - 3 * 3600 }),
];
const said = (source: string, v: CountryVerdict, ago: number) => ({ source, verdict: v, eyeball: true, atUnix: at - ago, reason: "" });
const some = {
  ...four,
  countryChecks: [
    verdict("nod_1", CountryVerdict.PARTIAL, [dark("MTS", 8359, "Novosibirsk"), { ...probe("Beeline", "Moscow", 44, [[443, true]]), asn: 3216 }], {
      confidence: "eyeball",
      noProbe: ["MegaFon", "T2"],
      sources: ["globalping", "cheburcheck", "bsbord"],
      vantages: someVantages,
      sourceVerdicts: [said("globalping", CountryVerdict.PARTIAL, 0), said("cheburcheck", CountryVerdict.OK, 2 * 3600), said("bsbord", CountryVerdict.BLOCKED, 3 * 3600)],
    }),
    countryChecks[1]!,
    verdict("nod_3", CountryVerdict.BLOCKED, [dark("MTS", 8359, "Omsk"), dark("Beeline", 3216, "Moscow")], {
      confidence: "eyeball",
      vantages: [vantage("globalping", "MTS", "dark", { asn: "8359", region: "Omsk" }), vantage("globalping", "Beeline", "dark", { asn: "3216", region: "Moscow" })],
    }),
  ],
};
const items = (ul: Element) => [...ul.querySelectorAll("li")].map((li) => li.textContent);


// ---------------------------------------------------------------------------------------------------
// The block lists in the same column: LISTED, a row of the lists alone, a live verdict with their detail

const hetzner = { cdnProvider: "Hetzner", cdnRegion: "DE" };
const withLists = {
  ...four,
  countryChecks: [
    verdict("nod_1", CountryVerdict.OK, [probe("Rostelecom", "Moscow", 42, [[443, true]]), probe("Beeline", "Kazan", 47, [[443, true]])], { confidence: "eyeball", ...hetzner }),
    verdict("nod_2", CountryVerdict.TCP_BLOCKED, [probe("Rostelecom", "Moscow", 51, [[443, false], [8443, false]]), probe("MegaFon", "Kazan", 0, [[443, false], [8443, false]], false)], {
      confidence: "eyeball",
      listedBy: ["antifilter_ipsum"],
      ...hetzner,
    }),
    // the lists alone: no probes, no reason
    verdict("nod_3", CountryVerdict.LISTED, [], { confidence: "listed", eyeball: false, listedBy: ["antifilter_ipsum", "antifilter_community"], ...hetzner }),
    verdict("nod_4", CountryVerdict.UNKNOWN, [], { eyeball: false }),
  ],
};


describe("the block lists on the node page", () => {
  it("shows LISTED in the country row, with its tooltip", async () => {
    getChecks.mockResolvedValue({ ...checks, rows: [{ ...checks.rows[2]!, nodeStatus: NodeStatus.ONLINE }], countryChecks: [withLists.countryChecks[2]] });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => root!.render(<QueryClientProvider client={qc}><NodeChecks nodeId="nod_3" /></QueryClientProvider>));
    for (let i = 0; i < 4; i++) await settle();
    const row = document.querySelector<HTMLElement>("[data-testid=node-ru]")!;
    expect(ruText(row)).toBe("The node is in a block list, not checked live from RussiaRKN registry (antifilter ipsum), community list (antifilter) · Hetzner subnet, DE · 4 min ago");
    expect(row.dataset.kind).toBe("listed");
    await act(async () => row.querySelector<HTMLElement>("[tabindex]")!.focus());
    for (let i = 0; i < 3; i++) await settle();
    expect(document.body.textContent).toContain("In the RKN registry, not checked live");
    expect(document.body.textContent).toContain("From the block lists only");
  });
});

// ---------------------------------------------------------------------------------------------------
// The TSPU probe in the same column: a conflict with Globalping, a verdict of the probe alone, the last run under the matrix

const runAt = at - 600;
const probeDetail = (ports: [number, string][]) => ({
  tspuVantageId: "nod_probe1",
  tspuVantageName: "probe1",
  tspuAtUnix: runAt,
  dpiHop: 6,
  tspuPorts: ports.map(([port, v]) => ({ port, verdict: v })),
});
const withProbe = {
  ...four,
  countryChecks: [
    // both agree
    verdict("nod_1", CountryVerdict.OK, [probe("Rostelecom", "Moscow", 42, [[443, true]]), probe("Beeline", "Kazan", 47, [[443, true]])], {
      confidence: "eyeball",
      sources: ["globalping", "tspu"],
      globalpingVerdict: CountryVerdict.OK,
      tspuVerdict: CountryVerdict.OK,
      ...probeDetail([[443, "open"]]),
    }),
    // Globalping's home networks connect, the probe node sees TCP die at the box
    verdict("nod_2", CountryVerdict.TCP_BLOCKED, [probe("Rostelecom", "Moscow", 51, [[443, true], [8443, true]]), probe("Beeline", "Kazan", 55, [[443, true], [8443, true]])], {
      confidence: "conflict",
      sources: ["globalping", "tspu"],
      globalpingVerdict: CountryVerdict.OK,
      tspuVerdict: CountryVerdict.TCP_BLOCKED,
      ...probeDetail([[443, "blocked"], [8443, "blocked"]]),
    }),
    verdict("nod_3", CountryVerdict.BLOCKED, [probe("MTS", "Omsk", 0, [[443, false]])], { confidence: "eyeball", sources: ["globalping"], globalpingVerdict: CountryVerdict.BLOCKED }),
    // the probe alone: no Globalping round yet
    verdict("nod_4", CountryVerdict.TCP_BLOCKED, [], {
      atUnix: runAt,
      eyeball: false,
      confidence: "dc_only",
      sources: ["tspu"],
      tspuVerdict: CountryVerdict.TCP_BLOCKED,
      ...probeDetail([[443, "blocked"], [8443, "blocked"]]),
    }),
  ],
  tspuRun: { status: "ok", atUnix: runAt, nodeId: "nod_probe1", dpiHop: 6, blockedRef: "31.13.72.36", nodesChecked: 3, detail: "" },
};


describe("the TSPU probe on the node page", () => {
  it("shows a verdict of the probe alone in the country row, with its tooltip", async () => {
    getChecks.mockResolvedValue({ ...checks, rows: [checks.rows[1]!], countryChecks: [withProbe.countryChecks[3]] });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => root!.render(<QueryClientProvider client={qc}><NodeChecks nodeId="nod_4" /></QueryClientProvider>));
    for (let i = 0; i < 4; i++) await settle();
    const row = document.querySelector<HTMLElement>("[data-testid=node-ru]")!;
    expect(ruText(row)).toBe("From Russia the node’s TCP is blockedTCP 443, 8443: blocked · probe node probe1 · 14 min ago");
    expect(row.dataset.kind).toBe("warn");
    await act(async () => row.querySelector<HTMLElement>("[tabindex]")!.focus());
    for (let i = 0; i < 3; i++) await settle();
    expect(document.body.textContent).toContain("Probe node probe1 · TSPU after hop 6");
    expect(document.body.textContent).toContain("Checked by your TSPU probe from a data center, TCP only");
  });
});


describe("a partial block on the node page", () => {
  it("shows it on the node page as well", async () => {
    getChecks.mockResolvedValue({ ...checks, rows: [checks.rows[1]!], countryChecks: [{ ...some.countryChecks[0]!, nodeId: "nod_2" }] });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => root!.render(<QueryClientProvider client={qc}><NodeChecks nodeId="nod_2" /></QueryClientProvider>));
    for (let i = 0; i < 4; i++) await settle();
    const row = document.querySelector<HTMLElement>("[data-testid=node-ru]")!;
    expect(row.textContent!.startsWith("From Russia the node is not visible from all networks")).toBe(true);
    expect(row.dataset.kind).toBe("partial");
    const head = row.querySelector<HTMLElement>("[tabindex]")!;
    const breakdown = row.querySelector("[data-testid=partial-breakdown]")!;
    expect(items(breakdown.querySelector("ul")!)).toEqual(["✕MTS AS8359 · Novosibirsk · Globalping", "✕MTS КРЫМ · mobile · bsbord"]);
    // the row's tooltip still opens on focus
    await act(async () => head.focus());
    for (let i = 0; i < 3; i++) await settle();
    expect(document.body.textContent).toContain("MTS, Novosibirsk (reaches your other nodes, not this one)");
  });
});
// ---------------------------------------------------------------------------------------------------
// The Checks tab in the board's table: a row a node, a cell a profile, a press opens the row into a box per profile

const rowOf = (node: string) => [...document.querySelectorAll<HTMLElement>(".hl-cw")].find((r) => r.querySelector(".hl-cnode b")?.textContent === node)!;
const words = (row: Element) => [...row.querySelectorAll(".hl-cv")].map((c) => c.textContent);
const press = (el: Element | null | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const rowButton = (node: string) => rowOf(node).querySelector<HTMLButtonElement>("button.hl-cr")!;

describe("the checks table", () => {
  afterEach(() => act(() => setLang("en")));

  it("says what each profile does on each node, a failed start in red apart from one switched off and a node without link", async () => {
    await mount();
    expect([...document.querySelectorAll(".hl-ch > span")].map((s) => s.textContent)).toEqual(["Node", "hy2 · 443 · Salamander", "hy2 · WARP · 8443", ""]);
    expect(words(rowOf("de1"))).toEqual(["42 ms", "not started"]);
    expect(words(rowOf("de2"))).toEqual(["off", "fails"]);
    expect(words(rowOf("nl1"))).toEqual(["no link", "—"]);
    const cells = [...rowOf("de1").querySelectorAll(".hl-cv")];
    expect(cells[1]!.className).toContain("bad");
    expect(cells[0]!.className).not.toMatch(/bad|part/);
    expect(cells[1]!.querySelector(".rw-gl")?.className).toContain("bad");
    expect(rowOf("de2").querySelector(".hl-cv")!.className).toContain("dim"); // off is quiet, not red
    // the phone puts the profile before each value
    expect(cells[0]!.getAttribute("data-l")).toBe("hy2 · 443 · Salamander");
  });

  it("opens the row that fails by itself, one at a time, into a box per profile with its last result and its day", async () => {
    await mount();
    expect(rowOf("de2").className).toContain("open");
    expect(rowButton("de2").getAttribute("aria-expanded")).toBe("true");
    expect(rowOf("de1").querySelector(".hl-cd")).toBeNull();
    await press(rowButton("de1"));
    expect([rowButton("de1"), rowButton("de2")].map((b) => b.getAttribute("aria-expanded"))).toEqual(["true", "false"]);
    const boxes = [...rowOf("de1").querySelectorAll(".hl-cd > div:not(.hl-cda)")].map((b) => b.textContent);
    expect(boxes[0]).toContain("hy2 · 443 · Salamander42 ms");
    expect(boxes[1]).toContain("hy2 · WARP · 8443not started");
    expect(boxes[1]).toContain("The node could not start the profile");
    expect(rowOf("de1").querySelector(".hl-cd")!.textContent).toContain("24 h");
    expect([...rowOf("de1").querySelectorAll("a")].map((a) => a.textContent)).toContain("Open node");
    await press(rowButton("de1"));
    expect(rowOf("de1").querySelector(".hl-cd")).toBeNull();
  });

  it("walks the rows on «Check now»: the one asked sweeps, the rest wait, then they flash once", async () => {
    await mount();
    vi.useFakeTimers();
    try {
      const go = [...document.querySelectorAll("button")].find((b) => b.textContent === "Check now")!;
      await press(go);
      expect(go.textContent).toBe("Checking");
      expect(go.getAttribute("aria-disabled")).toBe("true");
      // de1 and de2 are on line, nl1 is not: de1 is asked, de2 waits
      expect(rowOf("de1").className).toContain("cking");
      expect(rowOf("de2").className).toContain("ckw");
      expect(words(rowOf("de1"))).toEqual(["checking…", "checking…"]);
      expect(words(rowOf("de2"))).toEqual(["queued", "queued"]);
      expect(rowOf("nl1").className).not.toMatch(/cking|ckw/);
      expect(document.body.textContent).toContain("last round running now");
      await act(async () => void vi.advanceTimersByTime(650));
      expect(rowOf("de1").className).not.toContain("cking");
      expect(rowOf("de2").className).toContain("cking");
      await act(async () => void vi.advanceTimersByTime(650));
      // done: every row flashes (the tick pops), then quiet
      expect(go.textContent).toBe("Check now");
      expect([...document.querySelectorAll(".hl-cw")].every((r) => r.className.includes("ckn"))).toBe(true);
      await act(async () => void vi.advanceTimersByTime(1700));
      expect(document.querySelector(".hl-cw.ckn")).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("explains its words in a fold under the table, for a phone that has no hover", async () => {
    await mount();
    const fold = document.querySelector<HTMLElement>(".sb-mvh")!;
    expect(fold.getAttribute("aria-expanded")).toBe("false");
    expect(fold.textContent).toContain("What the values mean");
    expect(fold.textContent).toContain("ms — response through an open tunnel, hourly median, from the panel's server");
    expect(document.querySelector(".hl-leg")).toBeNull();
    await press(fold);
    const legend = document.querySelector(".hl-leg")!.textContent ?? "";
    for (const pair of ["✕ failingcheck failed", "✕ not runningthe node could not bring it up", "offturned off by you", "no linkthe node is offline", "not checkedthere is no test client for the protocol"]) expect(legend).toContain(pair);
    expect(legend).not.toContain("slow");
  });

  it("names the nodes of the UDP-loss notice above the table, and shows none without it", async () => {
    await mount(udp);
    const notice = [...document.querySelectorAll(".rc-nt")].find((n) => n.textContent?.includes("loses UDP"));
    expect(notice?.textContent).toBe(
      "The panel's server loses UDP: Hysteria2 checks to de1 and nl1 often take a second or more longer than VLESS to the same node (38% in the last hour); lost Hysteria2 packets: 3.1% on the way to the nodes, 5.5% on the way back. People are not affected: they connect to the nodes directly. If this keeps happening, contact the hoster of the panel's server.",
    );
    expect(notice!.compareDocumentPosition(document.querySelector(".hl-ct")!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    act(() => root?.unmount());
    host?.remove();
    await mount();
    expect(document.body.textContent).not.toContain("loses UDP");
  });

  it("puts a slow connect beside the figure from 2 s on, keeps the cell calm, and explains the mark", async () => {
    await mount(udp);
    const de1 = [...rowOf("de1").querySelectorAll(".hl-cv")];
    expect(de1[0]!.textContent).toBe("48 ms2.6 s");
    expect(de1[0]!.className).not.toMatch(/bad|part/);
    expect(de1[0]!.querySelector(".hl-slow svg")).not.toBeNull(); // a shape, not only a colour
    expect(de1[2]!.textContent).toBe("61 ms");
    expect([...rowOf("nl1").querySelectorAll(".hl-cv")][2]!.textContent).toBe("90 ms2 s");
    await press(document.querySelector(".sb-mvh"));
    expect(document.querySelector(".hl-leg")!.textContent).toContain("slow connect, 2 s or more; the tunnel works");
  });

  it("has no profile columns to show before any is deployed, and says so", async () => {
    await mount({ ...checks, columns: [], rows: [] });
    expect(document.body.textContent).toContain("No profile is deployed on any node yet.");
    expect(document.querySelector(".hl-ct")).toBeNull();
  });
});

describe("the country column of the table", () => {
  afterEach(() => act(() => setLang("en")));

  it("is not there while the integration is off or before a round", async () => {
    await mount();
    expect(document.body.textContent).not.toContain("From Russia");
    act(() => root?.unmount());
    host?.remove();
    await mount({ ...checks, countryChecks: [] });
    expect(document.body.textContent).not.toContain("From Russia");
  });

  it("says the verdict of each node in its tone and its dot: reachable and no data quiet, the blocks coloured", async () => {
    await mount(four);
    expect(document.querySelector(".hl-ch")!.textContent).toContain("From Russia");
    const verdictOf = (node: string) => [...rowOf(node).querySelectorAll(".hl-cv")].at(-1)!;
    expect(verdictOf("de1").textContent).toBe("reachable");
    expect(verdictOf("de1").querySelector(".rw-gl")!.className).toContain("ok");
    expect(verdictOf("de2").textContent).toBe("TCP blocked");
    expect(verdictOf("de2").className).toContain("part");
    expect(verdictOf("nl1").textContent).toBe("unreachable");
    expect(verdictOf("nl1").className).toContain("bad");
    expect(verdictOf("fi1").textContent).toBe("no data");
    expect(verdictOf("fi1").className).toContain("dim");
    // a node with no round yet has a dash, and its box says so
    expect(verdictOf("fi2").textContent).toBe("—");
    await press(rowButton("fi2"));
    expect(rowOf("fi2").querySelector(".hl-cd")!.textContent).toContain("not checked yet");
  });

  it("keeps every deployed cell in the queue while the first round has not come, and a profile that is not on a node as a dash", async () => {
    await mount(checks, true);
    const row = rowOf("de1");
    expect(row.className).toContain("ckw");
    expect(words(row)[0]).toBe("queued");
    expect(row.querySelector(".hl-cv .rw-gl")?.className).toContain("none");
    // the failing profile of the board is not shown as a verdict yet: no red, no cross in the text
    expect(document.querySelector(".hl-cw")?.textContent).not.toContain("✕");
  });

  it("opens into what the sources said and the way to Reachability", async () => {
    await mount(four);
    // de2 is the row that fails, so it is open already
    expect(rowButton("de2").getAttribute("aria-expanded")).toBe("true");
    const box = [...rowOf("de2").querySelectorAll(".hl-cd > div")].at(-2)!;
    expect(box.textContent).toContain("From RussiaTCP blocked");
    expect(box.textContent).toContain("Rostelecom");
    expect(box.querySelector('a[href="/reachability"]')).not.toBeNull();
  });

  it("speaks Russian, the country in the genitive", async () => {
    act(() => setLang("ru"));
    await mount(four);
    expect(document.querySelector(".hl-ch")!.textContent).toContain("Из России");
    expect([...rowOf("de2").querySelectorAll(".hl-cv")].at(-1)!.textContent).toBe("TCP режут");
    expect([...rowOf("nl1").querySelectorAll(".hl-cv")].at(-1)!.textContent).toBe("недоступна");
  });

  it("shows a block on some networks in the warn tone with the dark networks of every source in the open row", async () => {
    await mount(some);
    const last = [...rowOf("de1").querySelectorAll(".hl-cv")].at(-1)!;
    expect(last.textContent).toBe("some networks");
    expect(last.className).toContain("part");
    await press(rowButton("de1"));
    const breakdown = rowOf("de1").querySelector("[data-testid=partial-breakdown]")!;
    expect(items(breakdown.querySelector("ul")!)).toEqual(["✕MTS AS8359 · Novosibirsk · Globalping", "✕MTS КРЫМ · mobile · bsbord"]);
  });

  it("puts the TSPU probe's last run at the foot of the table: where it ran, when, where the box sits, or why it gave no verdicts", async () => {
    await mount(withProbe);
    expect(document.querySelector("[data-testid=tspu-run]")?.textContent).toBe("TSPU probe · probe1 · last run 14 min ago · TSPU after hop 6 · reference 31.13.72.36 · 3 nodes checked");
    expect(document.querySelector("[data-testid=tspu-run]")!.className).toContain("sb-vfoot");
    act(() => root?.unmount());
    host?.remove();
    await mount({ ...checks, rows: [...checks.rows, { ...checks.rows[0]!, nodeId: "nod_probe1", nodeName: "probe1", countryCode: "RU" }], countryChecks: [], tspuRun: { ...withProbe.tspuRun, status: "control_failed", dpiHop: 0, nodesChecked: 0 } });
    expect(document.querySelector("[data-testid=tspu-run]")?.textContent).toBe(
      "TSPU probe · probe1 · The method does not work on this network · even the control address is cut behind the TSPU, so there are no verdicts · last run 14 min ago",
    );
    act(() => root?.unmount());
    host?.remove();
    await mount(four);
    expect(document.querySelector("[data-testid=tspu-run]")).toBeNull();
  });

  it("a listed node says so in the warn tone and the open row names the lists", async () => {
    await mount(withLists);
    const last = [...rowOf("nl1").querySelectorAll(".hl-cv")].at(-1)!;
    expect(last.textContent).toBe("listed");
    expect(last.className).toContain("part");
    await press(rowButton("nl1"));
    expect(rowOf("nl1").querySelector(".hl-cd")!.textContent).toContain("RKN registry");
  });
});
