import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CheckStatus, CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { setLang } from "@/i18n";
import { NodeChecks } from "@/screens/node/checks";
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
  udpLoss: { nodeIds: ["nod_1", "nod_3"], sharePercent: 38 },
};

async function mount(data: object = checks) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}><ChecksTab data={data as never} /></QueryClientProvider>));
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const cellButton = (aria: string) => [...document.querySelectorAll<HTMLButtonElement>("button[aria-label]")].find((b) => b.getAttribute("aria-label")?.startsWith(aria));

describe("the checks matrix", () => {
  it("shows a profile that failed to start in red, apart from one switched off, and a node without link apart from both", async () => {
    await mount();
    const failed = cellButton("de1, hy2 · WARP · 8443");
    expect(failed?.textContent).toBe("✕ not started");
    expect(failed?.className).toContain("tone-bad");
    const off = cellButton("de2, hy2 · 443 · Salamander");
    expect(off?.textContent).toBe("off");
    expect(off?.className).not.toContain("tone-bad");
    const offline = cellButton("nl1, hy2 · 443 · Salamander");
    expect(offline?.textContent).toBe("no link");
    expect(offline?.className).toContain("tone-blip");
    expect(cellButton("de2, hy2 · WARP · 8443")?.textContent).toBe("✕ fails");
  });

  it("explains its words under the matrix, for a phone that has no hover", async () => {
    await mount();
    const legend = document.body.textContent ?? "";
    for (const words of ["ms — response through an open tunnel, hourly median, from the panel's server", "✕ — fails the check", "✕ not started — the node could not start it", "off — switched off by you", "no link — the node is offline", "not checked — no test client"]) {
      expect(legend).toContain(words);
    }
  });

  it("names the nodes of the UDP-loss notice above the matrix, and shows none without it", async () => {
    await mount(udp);
    const notice = [...document.querySelectorAll("[role=status]")].find((n) => n.textContent?.includes("inbound UDP"));
    expect(notice?.textContent).toBe(
      "The panel's server loses inbound UDP: Hysteria2 connections to de1 and nl1 often wait for a handshake retry (38% in the last hour), while VLESS is fine. People are not affected: they connect to the nodes directly. If this keeps happening, contact the hoster of the panel's server.",
    );
    // above the matrix, not in a corner
    expect(notice!.compareDocumentPosition(cellButton("de1, hy2 · 443 · Salamander")!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    act(() => root?.unmount());
    host?.remove();
    await mount();
    expect(document.body.textContent).not.toContain("inbound UDP");
  });

  it("puts a slow connect on a second line from 2 s on, keeps the cell calm, and explains the mark", async () => {
    await mount(udp);
    const slow = cellButton("de1, hy2 · 443 · Salamander")!;
    expect(slow.textContent).toBe("48 ms2.6 s");
    expect(slow.className).toContain("tone-ok");
    expect(slow.querySelector("svg")).not.toBeNull(); // a shape, not only a colour
    expect(slow.getAttribute("aria-label")).toBe("de1, hy2 · 443 · Salamander: Response 48 ms, median over the last hour · Connect 2.6 s, median over the last hour");
    expect(cellButton("de1, VLESS · 443")?.textContent).toBe("61 ms");
    expect(cellButton("nl1, VLESS · 443")?.textContent).toBe("90 ms2 s");
    expect(document.body.textContent).toContain("slow connect, 2 s or more; the tunnel works");
    // the phone row says it in words
    expect([...document.querySelectorAll("button[aria-expanded]")].map((b) => b.textContent)).toContain("hy2 · 443 · Salamander48 msconnect 2.6 s");
  });

  it("opens the tooltip of a cell on keyboard focus", async () => {
    await mount(udp);
    const slow = cellButton("de1, hy2 · 443 · Salamander")!;
    await act(async () => slow.focus());
    for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
    const text = document.body.textContent ?? "";
    for (const line of ["Response 48 ms, median over the last hour", "Connect 2.6 s, median over the last hour", "24 h: 48 ok, 0 failed", "Measured from the panel's server"]) expect(text).toContain(line);
  });

  it("gives a phone a card per node, the history opening right under the row it belongs to", async () => {
    await mount();
    const rows = [...document.querySelectorAll<HTMLButtonElement>("button[aria-expanded]")];
    expect(rows.map((r) => r.textContent)).toContain("hy2 · WARP · 8443✕ not startedThe node could not start the profile");
    const row = rows.find((r) => r.textContent?.startsWith("hy2 · 443 · Salamander42 ms"))!;
    await act(async () => void row.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(row.getAttribute("aria-expanded")).toBe("true");
    expect(row.nextElementSibling?.textContent).toContain("24 h"); // the history card follows its row
  });
});

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
});
const verdict = (nodeId: string, v: CountryVerdict, probes: ReturnType<typeof probe>[], over: object = {}) => ({ nodeId, country: "RU", verdict: v, atUnix: at, probes, reason: "", eyeball: true, ...over });
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
// zh writes "{node}，{profile}：{state}"
const countryCell = (node: string) => [...document.querySelectorAll<HTMLElement>("[role=img][aria-label]")].find((b) => new RegExp(`^${node}[,，]`).test(b.getAttribute("aria-label") ?? ""));
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));

describe("the country column", () => {
  afterEach(() => act(() => setLang("en")));

  it("is not there while the integration is off or before a round", async () => {
    await mount();
    expect(document.body.textContent).not.toContain("From Russia");
    act(() => root?.unmount());
    host?.remove();
    await mount({ ...checks, countryChecks: [] });
    expect(document.body.textContent).not.toContain("From Russia");
    expect(document.querySelector("[data-testid=country-row]")).toBeNull();
  });

  it("shows each verdict: OK and no data quiet, the two blocks in their tone with a glyph", async () => {
    await mount(four);
    expect(document.body.textContent).toContain("From Russia");
    const fine = countryCell("de1")!;
    expect(fine.textContent).toBe("reachable");
    expect(fine.getAttribute("aria-label")).toBe("de1, From Russia: Reachable from Russia");
    expect(fine.className).not.toMatch(/tone-/);
    const tcp = countryCell("de2")!;
    expect(tcp.textContent).toBe("!TCP blockedICMP passes");
    expect(tcp.className).toContain("tone-warn");
    expect(tcp.getAttribute("aria-label")).toBe("de2, From Russia: TCP is blocked, ICMP passes");
    const cut = countryCell("nl1")!;
    expect(cut.textContent).toBe("×unreachable");
    expect(cut.className).toContain("tone-bad");
    const none = countryCell("fi1")!;
    expect(none.textContent).toBe("no data");
    expect(none.className).not.toMatch(/tone-(warn|bad)/);
    expect(none.getAttribute("aria-label")).toBe("fi1, From Russia: No data: too few probes answered");
    // a node with no round yet has a dash that says so
    expect(document.body.textContent).toContain("fi2, From Russia: not checked yet");
  });

  it("opens the tooltip on keyboard focus: the probes, ICMP and TCP per port, the round, where it was checked from", async () => {
    await mount(four);
    await act(async () => countryCell("de2")!.focus());
    for (let i = 0; i < 3; i++) await settle();
    const text = document.body.textContent ?? "";
    for (const line of [
      "TCP is blocked, ICMP passes",
      "Rostelecom, Moscow",
      "ICMP 51 ms · TCP 443, 8443 ✕",
      "MegaFon, Kazan (not counted)",
      "ICMP ✕ · TCP 443, 8443 ✕",
      "Last round: ",
      "Checked via Globalping from home and mobile networks",
    ])
      expect(text).toContain(line);
  });

  it("gives every phone card a row in words, where there is no hover", async () => {
    await mount(four);
    const rows = [...document.querySelectorAll("[data-testid=country-row]")].map((r) => r.textContent);
    expect(rows).toEqual([
      "From RussiareachableRostelecom, Beeline · 4 min ago",
      "!From RussiaTCP blockedICMP passes · Rostelecom · 4 min ago",
      "×From RussiaunreachableMTS · 4 min ago",
      "From Russiano datatoo few probes answered · 4 min ago",
      "From Russia—not checked yet",
    ]);
  });

  it("speaks Russian, the country in the genitive", async () => {
    act(() => setLang("ru"));
    await mount(four);
    expect(document.body.textContent).toContain("Из России");
    expect(countryCell("de2")?.textContent).toBe("!TCP режутICMP проходит");
    expect(countryCell("nl1")?.textContent).toBe("×недоступна");
    expect(countryCell("de1")?.textContent).toBe("доступна");
    expect(countryCell("fi1")?.getAttribute("aria-label")).toBe("fi1, Из России: Нет данных: ответило слишком мало зондов");
  });

  it("speaks Chinese", async () => {
    act(() => setLang("zh"));
    await mount(four);
    expect(document.body.textContent).toContain("从俄罗斯访问");
    expect(countryCell("de2")?.textContent).toBe("!TCP 被封锁ICMP 可通");
    expect(countryCell("fi1")?.textContent).toBe("无数据");
  });
});

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

  it("comes after the checks with the same verdict and tooltip, and is not there while the integration is off", async () => {
    await mountNode({ ...own, countryChecks: [countryChecks[1]] });
    expect(getChecks).toHaveBeenCalledWith({ nodeId: "nod_2" }, expect.anything());
    const row = document.querySelector<HTMLElement>("[data-testid=country-row]")!;
    expect(row.textContent).toBe("!From RussiaTCP blockedICMP passes · Rostelecom · 4 min ago");
    expect(row.className).toContain("tone-warn");
    await act(async () => row.focus());
    for (let i = 0; i < 3; i++) await settle();
    expect(document.body.textContent).toContain("ICMP 51 ms · TCP 443, 8443 ✕");
    act(() => root?.unmount());
    host?.remove();
    await mountNode({ ...own, countryChecks: [] });
    expect(document.querySelector("[data-testid=country-row]")).toBeNull();
  });
});
