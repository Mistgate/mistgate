import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { setLang } from "@/i18n";
import type { Alert } from "@/lib/health";
import { AlertCard, AlertsTab, Ipv6Tag } from "./alerts";
import { useFixFlow } from "./fix";

const muteAlert = vi.fn();
const acceptDoctorItem = vi.fn();
const restartInbounds = vi.fn();
const updateNode = vi.fn();
let role = Role.OWNER;
/** Node.ipv6 of the alerts' node nod_1: it can take an entry, off */
let node1V6: object = { entry: "", entrySupported: true, unavailableReason: "", addresses: [], src: "" };
vi.mock("@/lib/api", () => ({
  health: { muteAlert: (...a: unknown[]) => muteAlert(...a), acceptDoctorItem: (...a: unknown[]) => acceptDoctorItem(...a), applyFix: vi.fn() },
  nodes: {
    restartInbounds: (...a: unknown[]) => restartInbounds(...a),
    updateNode: (...a: unknown[]) => updateNode(...a),
    listNodes: () => Promise.resolve({ nodes: [{ id: "nod_msk1", name: "msk1" }, { id: "nod_de1", name: "de1" }, { id: "nod_1", name: "de9", ipv6: node1V6 }] }),
  },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
  fleet: {},
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
}));
// no router in the test: a link is an anchor that says where it goes
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search, hash, className }: { children?: ReactNode; to: string; params?: { id: string }; search?: { tab: string; way?: string; add?: string }; hash?: string; className?: string }) => (
    <a
      href={`${to.replace("$id", params?.id ?? "")}${search ? `?tab=${search.tab}` : ""}${search?.way ? `&way=${search.way}` : ""}${search?.add ? `&add=${search.add}` : ""}${hash ? `#${hash}` : ""}`}
      className={className}
    >
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  role = Role.OWNER;
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const f of [muteAlert, acceptDoctorItem, restartInbounds, updateNode]) f.mockReset();
});

const NOW = 2_000_000_000;
const alert = (over: Partial<Alert> = {}): Alert => ({
  id: "alt_1",
  severity: AlertSeverity.WARNING,
  kind: AlertKind.CHECK_FAILED,
  nodeId: "nod_1",
  nodeName: "de1",
  subject: "inb_1",
  titleKey: "health.alert.check_failed.title",
  params: {},
  whyKey: "",
  firstSeenUnix: NOW - 600,
  openedUnix: NOW - 600,
  lastSeenUnix: NOW,
  resolvedAtUnix: 0,
  resolution: "",
  mutedUntilUnix: 0,
  actions: ["open_node", "mute"],
  ...over,
});

function Card({ a }: { a: Alert }) {
  const flow = useFixFlow();
  return (
    <>
      <AlertCard alert={a} now={NOW} flow={flow} />
      {flow.modal}
    </>
  );
}

async function settle() {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
function Tab({ active, history }: { active: Alert[]; history: Alert[] }) {
  const flow = useFixFlow();
  return (
    <>
      <AlertsTab active={active} history={history} now={NOW} flow={flow} />
      {flow.modal}
    </>
  );
}

async function render(node: ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>{node}</ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const mount = (a: Alert) => render(<Card a={a} />);
const mountTab = (active: Alert[], history: Alert[] = []) => render(<Tab active={active} history={history} />);
const text = () => document.body.textContent ?? "";
const button = (label: string, scope: ParentNode = document) => [...scope.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const link = (label: string) => [...document.querySelectorAll("a")].find((a) => a.textContent === label);
async function click(el: Element | null | undefined) {
  expect(el).toBeTruthy();
  await act(async () => void el!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await settle();
}
/** Base UI opens a menu on the press, not on the click. */
async function press(el: Element | null | undefined) {
  expect(el).toBeTruthy();
  await act(async () => {
    for (const type of ["pointerdown", "mousedown"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
    for (const type of ["pointerup", "mouseup", "click"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
  });
  await settle();
}
const dialog = () => document.querySelector("[role=dialog]")!;

describe("an alert that a restart can fix", () => {
  const timeout = alert({
    whyKey: "health.alert.check_failed.why.timeout",
    params: { inbound: "inb_1", profile: "hy2 · WARP · 8443", port: "8443", error_code: "timeout", online: "3" },
    actions: ["restart_inbound", "open_node", "mute"],
  });

  it("offers «Restart “profile”» as the main action, asks first and says who drops", async () => {
    restartInbounds.mockResolvedValue({ restarted: 1 });
    await mount(timeout);
    const restart = button("Restart “hy2 · WARP · 8443”")!;
    expect(restart.className).toContain("bg-accent");
    await click(restart);
    expect(restartInbounds).not.toHaveBeenCalled();
    expect(dialog().textContent).toContain("Restart “hy2 · WARP · 8443” on de1?");
    expect(dialog().textContent).toContain("Connections through this profile (now 3) drop for a couple of seconds and come back by themselves.");
    await click(button("Restart", dialog()));
    expect(restartInbounds).toHaveBeenCalledWith({ nodeId: "nod_1", inboundId: "inb_1" });
    expect(text()).toContain("de1: Restarted 1 profile");
  });

  it("keeps a failed restart in the window, above its buttons", async () => {
    restartInbounds.mockRejectedValue(new ConnectError("node offline", Code.FailedPrecondition));
    await mount(timeout);
    await click(button("Restart “hy2 · WARP · 8443”"));
    await click(button("Restart", dialog()));
    expect(dialog().querySelector('[role="alert"]')?.textContent).toContain("The node is not connected. Try again when its agent is back.");
  });

  it("every profile of a node that lets nobody in: «Restart the profiles» restarts them all", async () => {
    restartInbounds.mockResolvedValue({ restarted: 2 });
    await mount(alert({ kind: AlertKind.NO_TRAFFIC, subject: "", whyKey: "health.alert.no_traffic.why.auth", params: { failed: "2", total: "2", online: "0" }, actions: ["restart_inbounds", "open_node", "mute"] }));
    await click(button("Restart the profiles"));
    expect(dialog().textContent).toContain("Restart every profile on de1?");
    expect(dialog().textContent).toContain("Nobody is connected to this node now");
    await click(button("Restart", dialog()));
    expect(restartInbounds).toHaveBeenCalledWith({ nodeId: "nod_1", inboundId: "" });
  });

  it("is not offered to a read-only admin, who still reads why", async () => {
    role = Role.READONLY;
    await mount(timeout);
    expect(button("Restart “hy2 · WARP · 8443”")).toBeUndefined();
    expect(text()).toContain("“hy2 · WARP · 8443” does not complete the handshake on port 8443");
  });
});

describe("outbound IPv6 that does not work", () => {
  const ipv6 = alert({
    kind: AlertKind.DOCTOR_WARN,
    subject: "ipv6",
    titleKey: "doctor.ipv6.title",
    whyKey: "health.doctor.ipv6.why",
    params: { check: "ipv6", ipv6: "yes", connect: "failed", suggest: "client_ipv6_off" },
    actions: ["open_node", "accept", "mute"],
  });

  it("offers to turn off IPv6 for clients, asks first and changes only that setting", async () => {
    updateNode.mockResolvedValue({});
    await mount(ipv6);
    const off = button("Turn off IPv6 for clients")!;
    expect(off.className).toContain("bg-accent");
    await click(off);
    expect(updateNode).not.toHaveBeenCalled();
    expect(dialog().textContent).toContain("Turn off IPv6 for clients on de1?");
    expect(dialog().textContent).toContain("Nothing changes on the server itself");
    await click(button("Turn off", dialog()));
    expect(updateNode).toHaveBeenCalledWith({ nodeId: "nod_1", clientIpv6: false });
    expect(text()).toContain("de1: IPv6 for clients is off");
  });

  it("is only for the owner", async () => {
    role = Role.HELPER;
    await mount(ipv6);
    expect(button("Turn off IPv6 for clients")).toBeUndefined();
  });
});

describe("an alert that is fixed elsewhere", () => {
  it("a UDP port the hoster cuts: change it in the node's profiles", async () => {
    await mount(alert({ whyKey: "health.alert.check_failed.why.udp_blocked", params: { inbound: "inb_1", profile: "hy2 · 8443", port: "8443" }, actions: ["open_profiles", "open_node", "mute"] }));
    expect(text()).toContain("the hoster cuts UDP port 8443. Change the port of this profile on this node.");
    expect(text()).not.toContain("443 usually helps");
    const open = link("Open the node’s profiles")!;
    expect(open.getAttribute("href")).toBe("/nodes/nod_1?tab=profiles");
    // a place to go is not the fix itself: a secondary button, the primary is kept for a fix
    expect(open.className).not.toContain("bg-accent");
  });

  it("the whole of UDP cut: says so with the ports, no port advice", async () => {
    await mount(alert({ kind: AlertKind.NO_TRAFFIC, subject: "", whyKey: "health.alert.no_traffic.why.udp_all_blocked", params: { failed: "2", total: "2", ports: "443, 8443" } }));
    expect(text()).toContain("Every port times out (443, 8443): the hoster seems to cut incoming UDP altogether.");
  });

  it("a relay that is down names both nodes and the stage, and opens the exit's way in through this entry (a secondary button)", async () => {
    const down = (stage: string) =>
      alert({ kind: AlertKind.RELAY_DOWN, nodeId: "nod_msk1", nodeName: "msk1", subject: "rly_1", titleKey: "health.alert.relay_down.title", whyKey: `health.alert.relay_down.why.${stage}`, params: { relay: "msk1", exit: "de1", exit_profile: "VLESS · TCP", port: "8443", stage } });
    await mount(down("freeze"));
    expect(text()).toContain("Relay de1 via msk1 is down");
    expect(text()).toContain("A transfer through msk1 on port 8443 stalls after the first kilobytes.");
    const open = link("Open the way in")!;
    expect(open.getAttribute("href")).toBe("/nodes/nod_de1?tab=ways&way=via:nod_msk1");
    expect(open.className).not.toContain("bg-accent");
    act(() => root?.unmount());
    host?.remove();
    await mount(down("leg"));
    expect(text()).toContain("msk1 does not reach de1: the server VLESS · TCP gets nothing from it.");
  });

  it("a dead WARP exit opens the WARP card", async () => {
    await mount(alert({ whyKey: "health.alert.check_failed.why.warp_path", params: { inbound: "inb_2", profile: "hy2 · WARP" }, actions: ["open_warp", "open_node", "mute"] }));
    expect(link("Open WARP")?.getAttribute("href")).toBe("/nodes/nod_1?tab=exit#warp");
    expect(text()).not.toContain("ForceIPv4");
  });

  it("a certificate names its profile and domain, and opens the profiles", async () => {
    await mount(
      alert({
        kind: AlertKind.CERT_EXPIRY,
        titleKey: "health.alert.cert_expiry.title",
        whyKey: "health.alert.cert_expiry.why",
        params: { inbound: "inb_1", profile: "hy2 · 443 · Salamander", server_name: "de2.example.com", days_left: "5" },
        actions: ["open_profiles", "open_node", "mute"],
      }),
    );
    expect(text()).toContain("The certificate of “hy2 · 443 · Salamander” (de2.example.com) expires in 5 d.");
    expect(link("Open the node’s profiles")).toBeTruthy();
  });
});

describe("muting and accepting", () => {
  it("«Mute ▾» offers an hour, the morning, a day and a week, and says what muting does", async () => {
    muteAlert.mockResolvedValue({ alert: { mutedUntilUnix: NOW + 86_400 } });
    await mount(alert({ whyKey: "health.alert.check_failed.why.timeout", params: { profile: "x", port: "443" } }));
    await press(button("Mute"));
    const items = [...document.querySelectorAll('[role="menuitem"]')].map((i) => i.textContent);
    expect(items).toEqual(["For 1 hour", "Until morning (08:00)", "For a day", "For 7 days"]);
    expect(text()).toContain("Not counted in the badges. If it gets worse, the alert opens again.");
    await click([...document.querySelectorAll('[role="menuitem"]')].find((i) => i.textContent === "For a day"));
    expect(muteAlert).toHaveBeenCalledWith({ alertId: "alt_1", durationS: 86_400 });
  });

  it("a muted alert says until when, and «Unmute» lifts it", async () => {
    muteAlert.mockResolvedValue({ alert: {} });
    await mount(alert({ mutedUntilUnix: NOW + 3600 }));
    expect(text()).toContain("Muted until");
    expect(button("Mute")).toBeUndefined();
    await click(button("Unmute"));
    expect(muteAlert).toHaveBeenCalledWith({ alertId: "alt_1", durationS: 0 });
  });

  it("a doctor warning can be accepted as normal for the node", async () => {
    acceptDoctorItem.mockResolvedValue({ doctor: {} });
    await mount(alert({ kind: AlertKind.DOCTOR_WARN, subject: "foreign_vpn", titleKey: "health.alert.doctor_warn.title", params: { check: "foreign_vpn" }, actions: ["open_node", "accept", "mute"] }));
    await click(button("This is normal for this node"));
    expect(acceptDoctorItem).toHaveBeenCalledWith({ nodeId: "nod_1", checkId: "foreign_vpn" });
  });

  it("names the node as a link", async () => {
    await mount(alert());
    expect(link("de1")?.getAttribute("href")).toBe("/nodes/nod_1");
  });
});

describe("a reopened alert", () => {
  it("is «for» since it opened again, not since its first episode an hour ago", async () => {
    await mount(alert({ firstSeenUnix: NOW - 3600, openedUnix: NOW - 120 }));
    expect(text()).toContain("for 2 min");
    expect(text()).not.toContain("for 1 h");
  });
});

describe("a country alert and IPv6", () => {
  const country = (params: Record<string, string>, over: Partial<Alert> = {}) =>
    alert({ kind: AlertKind.COUNTRY_BLOCKED, subject: "", titleKey: "health.alert.country_blocked.title", whyKey: "", params: { country: "RU", verdict: "blocked", ...params }, ...over });
  const open6 = () => document.querySelector("[data-testid=alert-v6-open]");

  it("says that the node is reachable over IPv6 when the IPv6 rounds reach it", async () => {
    await mount(country({ ipv6: "open" }));
    expect(open6()?.textContent).toBe("Over IPv6 the node is reachable from there.");
    expect(document.querySelector("h3")?.textContent).toBe("Unreachable from Russia");
  });

  it("says nothing of IPv6 when IPv6 is cut too or not known", async () => {
    for (const ipv6 of ["blocked", "partial", "unknown"]) {
      await mount(country({ ipv6 }));
      expect(open6()).toBeNull();
      expect(text()).not.toContain("IPv6");
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("tags the IPv6 alert itself after its title", async () => {
    await mount(country({ family: "6" }, { subject: "ipv6" }));
    expect(document.querySelector("h3")?.textContent).toBe("Unreachable from RussiaIPv6");
    expect(open6()).toBeNull();
  });

  it("speaks Russian", async () => {
    act(() => setLang("ru"));
    try {
      await mount(country({ ipv6: "open" }, { subject: "partial", params: { country: "RU", verdict: "partial", networks: "MTS", ipv6: "open" } }));
      expect(open6()?.textContent).toBe("По IPv6 нода оттуда доступна.");
    } finally {
      act(() => setLang("en"));
    }
  });

  describe("and the IPv6 entry", () => {
    const entry6 = () => document.querySelector("[data-testid=alert-v6-entry]");
    const offOn = { entry: "", entrySupported: true, unavailableReason: "", addresses: [], src: "" };
    afterEach(() => {
      node1V6 = offOn;
    });

    it("offers the owner «Add IPv6 entry…» where IPv6 gets through, as the main action, into the entry's dialog", async () => {
      await mount(country({ ipv6: "open" }));
      const add = link("Add IPv6 entry…")!;
      expect(add.getAttribute("href")).toBe("/nodes/nod_1?tab=ways&way=ipv6&add=ipv6");
      expect(add.className).toContain("bg-accent");
    });

    it("offers nothing to a helper, nor for a node that cannot take an entry", async () => {
      role = Role.HELPER;
      await mount(country({ ipv6: "open" }));
      expect(link("Add IPv6 entry…")).toBeUndefined();
      act(() => root?.unmount());
      host?.remove();
      role = Role.OWNER;
      node1V6 = { ...offOn, entrySupported: false, unavailableReason: "agent_old" };
      await mount(country({ ipv6: "open" }));
      expect(link("Add IPv6 entry…")).toBeUndefined();
    });

    it("with the entry on: people with IPv6 get through it, in the place of the IPv6 line, and nothing to add", async () => {
      await mount(country({ ipv6: "entry" }));
      expect(entry6()?.textContent).toBe("People with IPv6 connect through the IPv6 entry.");
      expect(open6()).toBeNull();
      expect(link("Add IPv6 entry…")).toBeUndefined();
    });

    it("speaks Russian and Chinese", async () => {
      try {
        act(() => setLang("ru"));
        await mount(country({ ipv6: "entry" }));
        expect(entry6()?.textContent).toBe("Люди с IPv6 подключаются через вход по IPv6.");
        act(() => root?.unmount());
        host?.remove();
        act(() => setLang("zh"));
        await mount(country({ ipv6: "entry" }));
        expect(entry6()?.textContent).toBe("有 IPv6 的用户通过 IPv6 入口连接。");
      } finally {
        act(() => setLang("en"));
      }
    });
  });
});

describe("the IPv6 tag", () => {
  it("names the entry on an IPv4 alert of a node whose entry is on", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const a = alert({ kind: AlertKind.COUNTRY_BLOCKED, params: { country: "RU", verdict: "blocked", ipv6: "entry" } });
    await act(async () => root!.render(<Ipv6Tag alert={a} />));
    expect(host.textContent).toBe("IPv6 entry on");
  });
});

describe("the Alerts tab", () => {
  const lossy = (id: string, nodeName: string, port: string, got: string) =>
    alert({
      id,
      kind: AlertKind.PORT_LOSSY,
      nodeId: `nod_${nodeName}`,
      nodeName,
      subject: `inb_${id}`,
      titleKey: "health.alert.port_lossy.title",
      whyKey: "health.alert.port_lossy.why",
      params: { port, profile: `Hysteria2 · ${port}`, sent: "300", got, sender: "be1" },
      actions: ["open_profiles", "open_node", "mute"],
    });
  const nl1 = () => [lossy("a", "nl1", "2083", "207"), lossy("b", "nl1", "33183", "264"), lossy("c", "nl1", "4443", "276")];
  const toggles = () => [...document.querySelectorAll<HTMLButtonElement>("[data-alert-toggle]")];
  const expanded = (el: Element | undefined | null) => el?.getAttribute("aria-expanded");
  afterEach(() => {
    history.replaceState(null, "", location.pathname);
  });

  it("folds each card to one line and puts the ports of one node that lose packets on one card", async () => {
    muteAlert.mockResolvedValue({ alert: { mutedUntilUnix: NOW + 3600 } });
    await mountTab([...nl1(), lossy("d", "msk1", "443", "243")]);
    expect(toggles()).toHaveLength(2);
    const [card] = toggles();
    expect(expanded(card)).toBe("false");
    expect(card!.textContent).toContain("Warningnl1UDP loses packets on 3 ports");
    expect(card!.textContent).toContain("up to 31 % lost");
    expect(toggles()[1]!.textContent).toContain("msk1UDP to port 443 loses packets");
    expect(text()).not.toContain("get lost on the way");
    await click(card);
    expect(expanded(card)).toBe("true");
    expect(text()).toContain("The test packets to these ports get lost on the way.");
    expect([...document.querySelectorAll("tbody tr")].map((r) => [...(r as HTMLTableRowElement).cells].map((c) => c.textContent))).toEqual([
      ["2083", "Hysteria2 · 2083", "31 %", "be1"],
      ["33183", "Hysteria2 · 33183", "12 %", "be1"],
      ["4443", "Hysteria2 · 4443", "8 %", "be1"],
    ]);
    // one place to change the ports, a secondary button; one "Mute" for the three
    const open = [...document.querySelectorAll("a")].filter((a) => a.textContent === "Open the node’s profiles");
    expect(open.map((a) => a.getAttribute("href"))).toEqual(["/nodes/nod_nl1?tab=profiles"]);
    expect(document.querySelectorAll(".bg-accent")).toHaveLength(0);
    await press(button("Mute"));
    await click([...document.querySelectorAll('[role="menuitem"]')].find((i) => i.textContent === "For 1 hour"));
    expect(muteAlert.mock.calls.map((c) => c[0])).toEqual(["a", "b", "c"].map((alertId) => ({ alertId, durationS: 3600 })));
    expect(text()).toContain("nl1: 3 alerts muted until");
  });

  it("opens a critical alert by itself and keeps the muted ones folded at the end", async () => {
    muteAlert.mockResolvedValue({ alert: {} });
    const down = alert({
      id: "alt_down",
      severity: AlertSeverity.CRITICAL,
      kind: AlertKind.NODE_DOWN,
      nodeName: "sg1",
      subject: "agent",
      titleKey: "health.alert.node_down.title",
      whyKey: "health.alert.node_down.why",
      params: { minutes: "14" },
    });
    const muted = alert({ id: "alt_m", mutedUntilUnix: NOW + 3600, whyKey: "health.alert.check_failed.why.timeout", params: { profile: "x", port: "443" } });
    await mountTab([down, muted]);
    expect(toggles()).toHaveLength(1);
    expect(expanded(toggles()[0])).toBe("true");
    expect(text()).toContain("The agent has not answered for 14 min");
    const fold = document.querySelector("[data-muted-toggle]");
    expect([fold?.textContent, expanded(fold)]).toEqual(["Muted · 1", "false"]);
    await click(fold);
    expect(toggles()[1]!.textContent).toContain("until");
    expect(button("Unmute")).toBeUndefined(); // folded: one line until opened
    await click(toggles()[1]);
    await click(button("Unmute"));
    expect(muteAlert).toHaveBeenCalledWith({ alertId: "alt_m", durationS: 0 });
  });

  it("names the first operators of a long network list and shows every network by operator on request", async () => {
    const networks = "Beeline (AS3216, Moscow), MTS (AS8359, Kazan, bsbord), MTS (AS8359, Moscow), MegaFon (AS31133, Samara), Rostelecom (AS12389, Moscow), Tele2 (AS15378, Moscow)";
    await mountTab([
      alert({
        id: "alt_c",
        kind: AlertKind.COUNTRY_BLOCKED,
        subject: "partial",
        titleKey: "health.alert.country_blocked.title",
        whyKey: "health.alert.country_blocked.why.partial",
        params: { country: "RU", verdict: "partial", networks },
      }),
    ]);
    expect(toggles()[0]!.textContent).toContain("Unreachable from some networks in Russia6 networks");
    await click(toggles()[0]);
    expect(text()).toContain("Home or mobile probes on MTS, Beeline, MegaFon and 2 more get no answer");
    const all = document.querySelector("[data-nets-toggle]");
    expect([all?.textContent, expanded(all)]).toEqual(["Show all 6", "false"]);
    await click(all);
    expect([...document.querySelectorAll("dt")].map((d) => d.textContent)).toEqual(["MTS", "Beeline", "MegaFon", "Rostelecom", "Tele2"]);
    // the sources once; a row: the operator, its AS numbers once, its regions, a source named where the operator has several
    expect(text()).toContain("Sources: Globalping 5 · bsbord 1");
    const rows = [...document.querySelectorAll("dl > div")].map((r) => [...r.children].map((c) => c.textContent));
    expect(rows[0]).toEqual(["MTS", "AS8359", "GlobalpingMoscowbsbordKazan"]);
    expect(rows[1]).toEqual(["Beeline", "AS3216", "Moscow"]);
    expect(text()).not.toContain("—");
  });

  it("lists a network without details by its operator alone, and keeps a region with brackets whole", async () => {
    const networks =
      "ER-Telecom Holding, MTS (AS30881, Республика Саха (Якутия), cheburcheck), MTS (AS8359, Kirov, cheburcheck), Rostelecom (AS12389, Республика Саха (Якутия), cheburcheck), TTK (AS15774, Республика Саха (Якутия), cheburcheck)";
    await mountTab([
      alert({
        id: "alt_c",
        kind: AlertKind.COUNTRY_BLOCKED,
        titleKey: "health.alert.country_blocked.title",
        whyKey: "health.alert.country_blocked.why.blocked",
        params: { country: "RU", verdict: "blocked", confidence: "conflict", sources: "globalping,tspu,cheburcheck", networks },
      }),
    ]);
    await click(toggles()[0]);
    expect(text()).toContain("the probes of cheburcheck and Globalping inside Russia (MTS, ER-Telecom Holding, Rostelecom and 1 more)");
    await click(document.querySelector("[data-nets-toggle]"));
    const rows = [...document.querySelectorAll("dl > div")].map((r) => [...r.children].map((c) => c.textContent));
    expect(rows).toEqual([
      ["MTS", "AS30881 · AS8359", "Kirov, Республика Саха (Якутия)"],
      ["ER-Telecom Holding", "Globalping"],
      ["Rostelecom", "AS12389", "Республика Саха (Якутия)"],
      ["TTK", "AS15774", "Республика Саха (Якутия)"],
    ]);
  });

  it("folds a path_lossy alert to its sender, the node it lost packets to and the loss; open, both senders with their regions", async () => {
    await mountTab([
      alert({
        id: "alt_p",
        kind: AlertKind.PATH_LOSSY,
        nodeId: "nod_minsk1",
        nodeName: "minsk1",
        subject: "nod_nl1",
        titleKey: "health.alert.path_lossy.title",
        whyKey: "health.alert.path_lossy.why",
        params: { node: "minsk1", target: "nl1", target_id: "nod_nl1", ports: "2053, 2083", sent: "300", got: "236", other: "de1", other_got: "299", region: "cis", other_region: "other" },
      }),
    ]);
    const [card] = toggles();
    expect(expanded(card)).toBe("false");
    expect(card!.textContent).toContain("Warningminsk1Loses UDP on the way to nl1");
    expect(card!.textContent).toContain("21 % lost");
    expect(document.querySelector("[data-testid=path-runs]")).toBeNull();
    await click(card);
    expect(text()).toContain("UDP from minsk1 (CIS) to nl1 loses 21 % on ports 2053, 2083, while 299/300 arrive from de1 (outside the CIS).");
    expect([...document.querySelectorAll("[data-testid=path-runs] tbody tr")].map((r) => [...(r as HTMLTableRowElement).cells].map((c) => c.textContent))).toEqual([
      ["minsk1CIS", "236/300", "21 %"],
      ["de1outside the CIS", "299/300", "0 %"],
    ]);
    expect(link("Open node")?.getAttribute("href")).toBe("/nodes/nod_minsk1");
    expect(button("Mute")).toBeDefined();
  });

  it("folds a filtered path_lossy alert to the ports that got through and opens to a filter: only 443 against everything", async () => {
    await mountTab([
      alert({
        id: "alt_f",
        kind: AlertKind.PATH_LOSSY,
        nodeId: "nod_de1",
        nodeName: "de1",
        subject: "nod_fi1",
        titleKey: "health.alert.path_lossy.title",
        whyKey: "health.alert.path_lossy.why.filtered",
        params: { node: "de1", target: "fi1", target_id: "nod_fi1", ports: "2053, 8443", open: "443", sent: "300", got: "0", other: "msk1", other_got: "300", region: "other", other_region: "cis" },
      }),
    ]);
    const [card] = toggles();
    expect(card!.textContent).toContain("Warningde1UDP filtered on the way to fi1");
    expect(card!.textContent).toContain("only 443 gets through");
    expect(card!.textContent).not.toContain("%");
    await click(card);
    expect(text()).toContain("UDP from de1 (outside the CIS) reaches fi1 only on 443; ports 2053, 8443 get nothing, while from msk1 (CIS) everything arrives.");
    // no share lost: what got through, in words
    expect([...document.querySelectorAll("[data-testid=path-runs] th")].map((c) => c.textContent)).toEqual(["Checked from", "Arrived"]);
    expect([...document.querySelectorAll("[data-testid=path-runs] tbody tr")].map((r) => [...(r as HTMLTableRowElement).cells].map((c) => c.textContent))).toEqual([
      ["de1outside the CIS", "only 443"],
      ["msk1CIS", "everything"],
    ]);
  });

  it("names both senders of the ports that lost packets from both sides", async () => {
    const both = (id: string, port: string, got: string, confirmGot: string) =>
      alert({
        id,
        kind: AlertKind.PORT_LOSSY,
        nodeId: "nod_nl1",
        nodeName: "nl1",
        subject: `inb_${id}`,
        titleKey: "health.alert.port_lossy.title",
        whyKey: "health.alert.port_lossy.why.confirmed",
        params: { port, profile: `TUIC · ${port}`, sent: "300", got, sender: "msk1", confirm_sender: "fi1", confirm_sent: "300", confirm_got: confirmGot },
        actions: ["open_profiles", "open_node", "mute"],
      });
    const cells = () => [...document.querySelectorAll("tbody tr")].map((r) => [...(r as HTMLTableRowElement).cells].map((c) => c.textContent));
    const remount = async (active: Alert[]) => {
      act(() => root?.unmount());
      host?.remove();
      await mountTab(active);
      await click(toggles()[0]);
    };
    await mountTab([both("a", "2083", "241", "238"), both("b", "4443", "262", "259")]);
    await click(toggles()[0]);
    expect(text()).toContain("The test packets to these ports got lost from both sides, so this node’s ports lose them.");
    // a line per sender in both cells: the shares next to who measured them
    const lines = (cell: number) => [...document.querySelectorAll("tbody tr")].map((r) => [...(r as HTMLTableRowElement).cells[cell]!.children].map((s) => s.textContent));
    expect(cells().map((r) => r.slice(0, 2))).toEqual([
      ["2083", "TUIC · 2083"],
      ["4443", "TUIC · 4443"],
    ]);
    expect(lines(2)).toEqual([
      ["20 %", "21 %"],
      ["13 %", "14 %"],
    ]);
    expect(lines(3)).toEqual([
      ["msk1", "fi1"],
      ["msk1", "fi1"],
    ]);
    // a port checked from one sender keeps the words of the way
    await remount([both("a", "2083", "241", "238"), lossy("b", "nl1", "4443", "276")]);
    expect(text()).toContain("The test packets to these ports get lost on the way.");
    expect(cells()[1]).toEqual(["4443", "Hysteria2 · 4443", "8 %", "be1"]);
    act(() => root?.unmount());
    host?.remove();
    await mountTab([both("c", "2083", "241", "238")]);
    await click(toggles()[0]);
    expect(text()).toContain("The test packets to port 2083 of “TUIC · 2083” got lost from both sides: 20 % from msk1, 21 % from fi1, so this node’s port loses them.");
  });

  // a day of the real calendar: the day headers count from today
  const noon = Math.floor(new Date().setHours(12, 0, 0, 0) / 1000);
  const episode = (id: string, nodeName: string, dt: number) =>
    alert({ id, nodeId: `nod_${nodeName}`, nodeName, openedUnix: noon - 360 + dt, resolvedAtUnix: noon + dt, resolution: "cleared", params: { profile: "MTProto · 443" }, actions: ["open_node"] });
  const mtproto = () => [episode("a", "sg1", 33), episode("b", "nl1", 21), episode("c", "fi1", 9), episode("d", "de1", 0)];
  const fixed = alert({
    id: "alt_j",
    kind: AlertKind.DOCTOR_WARN,
    subject: "journald_size",
    titleKey: "health.alert.doctor_warn.title",
    params: { check: "journald_size" },
    openedUnix: noon - 90_000,
    resolvedAtUnix: noon - 86_400,
    resolution: "fix_applied",
  });

  it("shows one history row per event under its day, its episodes under it, and how it ended as a glyph", async () => {
    await mountTab([], [...mtproto(), fixed]);
    expect([...document.querySelectorAll("section h3")].map((h) => h.textContent)).toEqual(["Today", "Yesterday"]);
    const row = document.querySelector("[data-history-toggle]")!;
    expect(row.textContent).toContain("Warningsg1nl1fi1de1“MTProto · 443” fails the client-eye checklasted 6 min");
    expect([...document.querySelectorAll('[role="img"][aria-label]')].map((g) => g.getAttribute("aria-label"))).toEqual(["cleared by itself", "fixed"]);
    // folded, every episode keeps its anchor for the links from Reachability
    expect(expanded(row)).toBe("false");
    expect(mtproto().every((e) => document.getElementById(`alert-${e.id}-${e.resolvedAtUnix}`))).toBe(true);
    await click(row);
    const episodes = [...document.getElementById(row.getAttribute("aria-controls")!)!.querySelectorAll("li")];
    expect(episodes.map((li) => li.id)).toEqual(mtproto().map((e) => `alert-${e.id}-${e.resolvedAtUnix}`));
    expect(episodes[0]!.textContent).not.toContain("MTProto"); // the row says it once
  });

  it("shows the latest 20 events and the rest on request", async () => {
    const blips = Array.from({ length: 23 }, (_, i) =>
      alert({
        id: `alt_${i}`,
        kind: AlertKind.HOST_BLIP,
        subject: "agent",
        titleKey: "health.alert.host_blip.title",
        openedUnix: NOW - i * 3600 - 60,
        resolvedAtUnix: NOW - i * 3600,
        resolution: "node_returned",
      }),
    );
    await mountTab([], blips);
    expect(document.querySelectorAll("section li")).toHaveLength(20);
    await click(button("Show 3 more"));
    expect(document.querySelectorAll("section li")).toHaveLength(23);
    expect(button("Show 3 more")).toBeUndefined();
  });

  it("opens the card and the history row that a link points at", async () => {
    const b = mtproto()[1]!;
    history.replaceState(null, "", `#alert-${b.id}-${b.resolvedAtUnix}`);
    await mountTab(nl1(), mtproto());
    expect(expanded(toggles()[0])).toBe("false");
    expect(expanded(document.querySelector("[data-history-toggle]"))).toBe("true");
    act(() => root?.unmount());
    host?.remove();
    history.replaceState(null, "", "#alert-c");
    await mountTab(nl1());
    expect(expanded(toggles()[0])).toBe("true");
    expect(document.getElementById("alert-c")).toBeTruthy();
  });
});
