import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { InboundState, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity, DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { agentLinked } from "@/lib/node-status";
import { NodeDetail } from "./index";

const listAlerts = vi.fn();
const getDoctor = vi.fn();
const getChecks = vi.fn();
const runChecksNow = vi.fn();
const restartInbounds = vi.fn();
const streamLogs = vi.fn();
let search: Record<string, string> = {};
vi.mock("@/lib/api", () => ({
  health: {
    listAlerts: (...a: unknown[]) => listAlerts(...a),
    getDoctor: (...a: unknown[]) => getDoctor(...a),
    getChecks: (...a: unknown[]) => getChecks(...a),
    runChecksNow: (...a: unknown[]) => runChecksNow(...a),
  },
  nodes: { restartInbounds: (...a: unknown[]) => restartInbounds(...a), streamLogs: (...a: unknown[]) => streamLogs(...a) },
  fleet: { overview: () => Promise.resolve({ nodes: [], traffic: [], online: [], events: [], topConsumers: [], nowUnix: 0n }) },
  updates: { getUpdates: () => Promise.resolve({ nodes: [] }) },
  profiles: { listProfiles: () => Promise.resolve({ profiles: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: 1 } }) },
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
}));
vi.mock("@/components/add-node", () => ({ useAddNode: () => () => {} }));
// Base UI's tabs do not load under vitest: the same items as plain tab buttons, the chosen one's content under them
vi.mock("@/components/ui/tabs", () => ({
  Tabs: ({ items, value, onValueChange }: { items: { value: string; label: ReactNode; badge?: number; content: ReactNode }[]; value: string; onValueChange: (v: string) => void }) => (
    <div>
      {items.map((item) => (
        <button key={item.value} type="button" role="tab" aria-selected={item.value === value} onClick={() => onValueChange(item.value)}>
          {item.label}
          {!!item.badge && <span>{item.badge}</span>}
        </button>
      ))}
      {items.find((item) => item.value === value)?.content}
    </div>
  ),
}));
vi.mock("@/lib/updates", async (orig) => ({ ...(await orig<object>()), useOlderNodes: () => new Set<string>() }));
// no router in the test: a link is an anchor that carries its target, the search is what the test says
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, search: s, hash, className, title }: { children?: ReactNode; to: string; search?: object; hash?: string; className?: string; title?: string }) => (
    <a href={to + (hash ? `#${hash}` : "")} data-search={JSON.stringify(s ?? {})} className={className} title={title}>
      {children}
    </a>
  ),
  useNavigate: () => (o: { search: Record<string, string> }) => {
    search = o.search;
  },
  useSearch: () => search,
  useParams: () => ({}),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  search = {};
  listAlerts.mockResolvedValue({ nowUnix: 1000, active: [], history: [] });
  getDoctor.mockResolvedValue({ nowUnix: 1n, nodes: [] });
  getChecks.mockResolvedValue({ nowUnix: 1n, intervalS: 300, columns: [], rows: [] });
  runChecksNow.mockResolvedValue({ scheduled: 2, skipped: 0 });
  restartInbounds.mockResolvedValue({ restarted: 2 });
  streamLogs.mockReturnValue((async function* () {})());
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listAlerts, getDoctor, getChecks, runChecksNow, restartInbounds, streamLogs]) m.mockReset();
});

const inbound = (over: Record<string, unknown> = {}) => ({
  id: "inb_1",
  profileId: "prf_1",
  profileName: "hy2 · 443",
  protocol: "hysteria2",
  state: InboundState.ACTIVE,
  port: 443,
  tlsServerName: "de1.example.com",
  certNotAfterUnix: 0,
  certPinSha256: "",
  lastError: "",
  ...over,
});
const data = (node: Record<string, unknown> = {}, over: Record<string, unknown> = {}) =>
  ({
    node: { id: "nod_1", name: "de1", address: "de1.example.com", countryCode: "DE", status: NodeStatus.ONLINE, online: [], lastSeenUnix: 0, ...node },
    inbounds: [inbound()],
    facts: undefined,
    metrics: undefined,
    enrollmentExpiresUnix: 0,
    ...over,
  }) as never;

async function mount(d: never) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <NodeDetail data={d} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => document.body.textContent ?? "";
const buttons = (label: string) => [...document.querySelectorAll("button")].filter((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const disabled = (b: Element | undefined) => !!b && (b.hasAttribute("disabled") || b.hasAttribute("data-disabled"));

describe("the agent is on the line", () => {
  it("counts a node with no traffic as linked: its restart and its logs work", () => {
    expect(agentLinked(NodeStatus.ONLINE)).toBe(true);
    expect(agentLinked(NodeStatus.NO_TRAFFIC)).toBe(true);
    expect(agentLinked(NodeStatus.DOWN)).toBe(false);
    expect(agentLinked(NodeStatus.BLIP)).toBe(false);
    expect(agentLinked(NodeStatus.PENDING)).toBe(false);
  });

  it("no traffic: says the diagnosis of the open alert and offers a restart and a check now, not the doctor", async () => {
    listAlerts.mockResolvedValue({
      nowUnix: 1000,
      history: [],
      active: [
        {
          id: "alt_1",
          kind: AlertKind.NO_TRAFFIC,
          severity: AlertSeverity.CRITICAL,
          nodeId: "nod_1",
          nodeName: "de1",
          subject: "",
          titleKey: "health.alert.no_traffic.title",
          whyKey: "health.alert.no_traffic.why.udp_blocked",
          params: { failed: "2", total: "2" },
          firstSeenUnix: 900,
          lastSeenUnix: 1000,
          resolvedAtUnix: 0,
          resolution: "",
          mutedUntilUnix: 0,
          actions: [],
        },
      ],
    });
    await mount(data({ status: NodeStatus.NO_TRAFFIC, online: [{ protocol: "hysteria2", users: 5 }] }));
    expect(text()).toContain("Alive, but no traffic");
    expect(text()).toContain("UDP"); // the alert's own why, not the generic sentence
    expect(text()).not.toContain("the client-side check fails");
    const restart = buttons("Restart profiles");
    expect(restart.length).toBeGreaterThan(0);
    expect(restart.every((b) => !disabled(b))).toBe(true);
    await click(buttons("Check now")[0]);
    expect(runChecksNow).toHaveBeenCalledWith({ nodeId: "nod_1" });

    // the restart says who notices: the people connected now
    await click(restart[0]);
    expect(text()).toContain("Restart all profiles on de1?");
    expect(text()).toContain("5 people drop for a few seconds and reconnect by themselves.");
    await click([...document.querySelectorAll("[role=dialog] button")].find((b) => b.textContent === "Restart"));
    expect(restartInbounds).toHaveBeenCalledWith({ nodeId: "nod_1" });
  });

  it("keeps the logs on the line while traffic does not flow", async () => {
    streamLogs.mockImplementation(() =>
      (async function* () {
        yield { lines: [{ timeUnixMs: 1_000, level: 1, source: "hysteria2", message: "client connected" }], eof: false, dropped: 0, error: "" };
        await new Promise(() => {}); // the tail stays open
      })(),
    );
    search = { tab: "logs" };
    await mount(data({ status: NodeStatus.NO_TRAFFIC }));
    expect(streamLogs).toHaveBeenCalledWith(expect.objectContaining({ nodeId: "nod_1", follow: true }), expect.anything());
    expect(text()).toContain("Live stream");
    expect(text()).toContain("client connected");
    expect(text()).not.toContain("The node is not connected");
  });
});

describe("an unreachable node", () => {
  it("says for how long, when it was last heard from and what to do, with the command to copy", async () => {
    const lastSeen = Math.floor(Date.now() / 1000) - 47 * 60;
    await mount(data({ status: NodeStatus.DOWN, lastSeenUnix: lastSeen, reason: { code: "agent_silent", params: { minutes: "47" } } }));
    expect(text()).toContain("The node has not answered for 47 min");
    expect(text()).toContain("Most often it is the hoster");
    expect(text()).toContain("systemctl restart mistgate-node && journalctl -u mistgate-node -n 50 --no-pager");
    expect(buttons("Copy command")).toHaveLength(1);
    expect(buttons("Check again")).toHaveLength(0); // the doctor stays in the header, not in the banner
    // nothing to restart: the button says why
    expect(buttons("Restart profiles").every(disabled)).toBe(true);
    expect(text()).toContain("The node is not connected — nothing to restart");
    expect(text()).toContain("The node has been offline since");
    expect(text()).toContain("the node is offline");
  });
});

describe("an online node with a reason", () => {
  it("with no profile: says users get nothing from it and leads to the profiles", async () => {
    await mount(data({}, { inbounds: [] }));
    expect(text()).toContain("No profiles on de1 yet — users will not get it");
    await click(buttons("Add profile")[0]);
    expect(search).toEqual({ tab: "profiles" });
  });

  it("with a profile that did not start: names it and says why in words", async () => {
    const failed = inbound({ id: "inb_8", profileName: "hy2 · WARP · 8443", port: 8443, state: InboundState.FAILED, lastError: "listen udp :8443: bind: address already in use" });
    await mount(data({ reason: { code: "inbound_failed", params: { inbound: "inb_8", error: "listen udp :8443: bind: address already in use" } } }, { inbounds: [inbound(), failed] }));
    expect(text()).toContain("“hy2 · WARP · 8443” failed to start");
    expect(text()).toContain("Port udp/8443 is taken by another program");
    // the tabs say where the problems are
    const tab = [...document.querySelectorAll("[role=tab]")].find((x) => x.textContent?.startsWith("Profiles"));
    expect(tab?.textContent).toBe("Profiles1");
  });

  it("with several that did not start: counts them of the enabled profiles, as the server's reason does", async () => {
    const failed = (id: string, port: number) => inbound({ id, profileName: `hy2 · ${port}`, port, state: InboundState.FAILED, lastError: "x" });
    const off = inbound({ id: "inb_off", profileName: "hy2 · off", state: InboundState.DISABLED });
    const reason = { code: "inbound_failed", params: { inbound: "inb_a", profile: "hy2 · 8443", error: "x", failed: "2", total: "3" } };
    await mount(data({ reason }, { inbounds: [inbound(), failed("inb_a", 8443), failed("inb_b", 4443), off] }));
    expect(text()).toContain("2 of 3 profiles failed to start");
  });
});

describe("the header", () => {
  it("counts the doctor's problems on its tab and colours the WARP chip by its state, as a way to the WARP card", async () => {
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [{ nodeId: "nod_1", items: [{ id: "a", status: DoctorStatus.FAIL, params: {} }, { id: "b", status: DoctorStatus.WARN, params: {} }, { id: "c", status: DoctorStatus.OK, params: {} }] }],
    });
    await mount(data({ warp: { state: WarpState.DOWN, colo: "" } }));
    const doctor = [...document.querySelectorAll("[role=tab]")].find((x) => x.textContent?.startsWith("Doctor"));
    expect(doctor?.textContent).toBe("Doctor2");
    const chip = document.querySelector('a[href="/nodes/$id#warp"]');
    expect(chip?.textContent).toContain("WARP · Not working");
    expect(chip?.querySelector(".tone-bad")).not.toBeNull();
    expect(chip?.getAttribute("data-search")).toBe(JSON.stringify({ tab: "settings" }));
  });
});
