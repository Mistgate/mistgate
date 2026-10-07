import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import type { Alert } from "@/lib/health";
import { AlertCard } from "./alerts";
import { useFixFlow } from "./fix";

const muteAlert = vi.fn();
const acceptDoctorItem = vi.fn();
const restartInbounds = vi.fn();
let role = Role.OWNER;
vi.mock("@/lib/api", () => ({
  health: { muteAlert: (...a: unknown[]) => muteAlert(...a), acceptDoctorItem: (...a: unknown[]) => acceptDoctorItem(...a), applyFix: vi.fn() },
  nodes: { restartInbounds: (...a: unknown[]) => restartInbounds(...a) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
  fleet: {},
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
}));
// no router in the test: a link is an anchor that says where it goes
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search, hash, className }: { children?: ReactNode; to: string; params?: { id: string }; search?: { tab: string }; hash?: string; className?: string }) => (
    <a href={`${to.replace("$id", params?.id ?? "")}${search ? `?tab=${search.tab}` : ""}${hash ? `#${hash}` : ""}`} className={className}>
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
  for (const f of [muteAlert, acceptDoctorItem, restartInbounds]) f.mockReset();
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
async function mount(a: Alert) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <Card a={a} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
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

describe("an alert that is fixed elsewhere", () => {
  it("a UDP port the hoster cuts: change it in the node's profiles", async () => {
    await mount(alert({ whyKey: "health.alert.check_failed.why.udp_blocked", params: { inbound: "inb_1", profile: "hy2 · 8443", port: "8443" }, actions: ["open_profiles", "open_node", "mute"] }));
    expect(text()).toContain("the hoster cuts UDP port 8443. Change the port of this profile on this node.");
    expect(text()).not.toContain("443 usually helps");
    const open = link("Open the node’s profiles")!;
    expect(open.getAttribute("href")).toBe("/nodes/nod_1?tab=profiles");
    expect(open.className).toContain("bg-accent");
  });

  it("the whole of UDP cut: says so with the ports, no port advice", async () => {
    await mount(alert({ kind: AlertKind.NO_TRAFFIC, subject: "", whyKey: "health.alert.no_traffic.why.udp_all_blocked", params: { failed: "2", total: "2", ports: "443, 8443" } }));
    expect(text()).toContain("Every port times out (443, 8443): the hoster seems to cut incoming UDP altogether.");
  });

  it("a dead WARP exit opens the WARP card", async () => {
    await mount(alert({ whyKey: "health.alert.check_failed.why.warp_path", params: { inbound: "inb_2", profile: "hy2 · WARP" }, actions: ["open_warp", "open_node", "mute"] }));
    expect(link("Open WARP")?.getAttribute("href")).toBe("/nodes/nod_1?tab=settings#warp");
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
