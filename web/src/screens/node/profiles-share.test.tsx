import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { ProfilesTab } from "./profiles";

// Services that share a TCP port by the name in the connection, in the node's dialogs and on its Profiles tab: the refusal is shown before
// the click with the way out, the name put in makes the check go again, a check that passes says who restarts and which DNS record the
// name needs, and the card of the port tells what the node counted. The calls are answered here, as the panel answers them.

let role = 1;
const listProfiles = vi.fn();
const createInbound = vi.fn();
const updateInbound = vi.fn();
const getDoctor = vi.fn();
const getNodeConnections = vi.fn();
vi.mock("@/lib/api", () => ({
  profiles: { listProfiles: (...a: unknown[]) => listProfiles(...a), createInbound: (...a: unknown[]) => createInbound(...a), updateInbound: (...a: unknown[]) => updateInbound(...a) },
  nodes: { getNodeConnections: (...a: unknown[]) => getNodeConnections(...a) },
  groups: { listGroups: () => Promise.resolve({ groups: [] }) },
  health: { getDoctor: (...a: unknown[]) => getDoctor(...a) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
  fleet: {},
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className, onClick }: { children?: ReactNode; to: string; className?: string; onClick?: () => void }) => (
    <a href={to} className={className} onClick={onClick}>
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
  role = 1;
  getDoctor.mockResolvedValue({ nowUnix: 1n, nodes: [] });
  getNodeConnections.mockResolvedValue({ supported: false, inbounds: [] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listProfiles, createInbound, updateInbound, getDoctor, getNodeConnections]) m.mockReset();
});

const node = { id: "nod_1", name: "de1", address: "de1.example.com", status: NodeStatus.ONLINE, awgBackend: "auto", online: [] };
const inb = (over: Record<string, unknown>) => ({ id: "inb_a", profileId: "prf_a", profileName: "Reality main", protocol: "vless", nodeId: "nod_1", nodeName: "de1", state: InboundState.ACTIVE, port: 443, tlsServerName: "de1.example.com", lastError: "", egress: "direct", ...over });
const data = (over: Record<string, unknown> = {}) => ({ node, inbounds: [inb({})], portChecks: [], ...over }) as never;

async function mount(d: never) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <ProfilesTab data={d} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const checked = async () => {
  await act(async () => void (await new Promise((r) => setTimeout(r, 350))));
  await settle();
};
const text = () => document.querySelector("[role=dialog]")?.textContent ?? document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button, a")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
/** "Add profile" opens the list of what can be put; the form of the first profile is one "Set up…" away. */
const openAdd = async () => {
  await click(button("Add profile"));
  await settle();
  await click(button("Set up…"));
};
const saveButton = () => document.querySelector<HTMLButtonElement>("[role=dialog] button[type=submit]");
const disabled = (b: HTMLButtonElement | null) => !!b && (b.disabled || b.hasAttribute("data-disabled"));
async function type(el: HTMLInputElement | undefined | null, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el!.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const field = (label: string) => [...document.querySelectorAll<HTMLInputElement>("[role=dialog] input")].find((i) => i.labels?.[0]?.textContent === label);

const taken = "port=443&node=de1&profile=Reality+main&name=de1.example.com";
const sharedWarning = { code: "tcp_port_shared", params: { node: "de1", port: "443", profiles: "Reality main, Telegram", default: "Reality main", restarts: "Reality main" } };
const refuse = (msg: string, code = Code.AlreadyExists) => new ConnectError(msg, code);

/** The panel's answer to a Telegram profile on de1, whose REALITY holds 443 under the node's own name. */
function panel(req: { validateOnly?: boolean; portOverride?: number; tlsServerNameOverride?: string }) {
  const port = req.portOverride || 443;
  const name = (req.tlsServerNameOverride ?? "").trim() || "de1.example.com";
  if (port === 443 && name === "de1.example.com") throw refuse(`sni_taken: ${taken}&free=8443&share=1&suggest=cdn.de1.example.com`);
  return { inbound: { port, tlsServerName: name }, warnings: port === 443 ? [sharedWarning] : [], freePort: 0 };
}

describe("adding a profile whose name the node's port already holds", () => {
  beforeEach(() => {
    listProfiles.mockResolvedValue({ profiles: [{ id: "prf_tg", name: "Telegram", protocol: "mtproto", summary: "TCP 443 · Fake TLS" }] });
    createInbound.mockImplementation(async (req: { validateOnly?: boolean }) => {
      const a = panel(req);
      return req.validateOnly ? a : { inbound: { id: "inb_new" }, warnings: a.warnings };
    });
  });

  it("shows the refusal and the name to take before the click, then puts the profile beside the other and says what that means", async () => {
    await mount(data());
    await openAdd();
    await checked();

    // the button is off, and the dialog says why and what to do
    expect(disabled(saveButton())).toBe(true);
    expect(text()).toContain("Put it beside “Reality main” on port 443");
    expect(text()).toContain("de1.example.com"); // the name that is held
    expect(field("Name of this profile on de1")?.value).toBe("cdn.de1.example.com"); // the panel's suggestion, editable
    expect(button("Take 8443")).toBeTruthy();

    await click(button("Put beside on 443"));
    await checked();
    // the check went again with the name, and passed: the sentences and the DNS record show before the click on Add
    const asked = createInbound.mock.calls.map(([r]) => r as { validateOnly?: boolean; tlsServerNameOverride?: string });
    expect(asked.at(-1)).toMatchObject({ validateOnly: true, tlsServerNameOverride: "cdn.de1.example.com" });
    expect(text()).toContain("TCP 443 on de1 is shared by Reality main, Telegram");
    expect(text()).toContain("“Reality main” restarts");
    expect(text()).toContain("cdn.de1.example.com");
    expect(text()).toContain("CNAME");
    expect(text()).toContain("de1.example.com"); // the record leads to the node
    expect(document.querySelector("[data-testid=dns-hint]")).toBeTruthy();
    expect(disabled(saveButton())).toBe(false);

    await click(saveButton());
    await settle();
    const real = createInbound.mock.calls.map(([r]) => r as { validateOnly?: boolean; tlsServerNameOverride?: string }).filter((r) => !r.validateOnly);
    expect(real).toHaveLength(1);
    expect(real[0]!.tlsServerNameOverride).toBe("cdn.de1.example.com");
  });

  it("puts the name the admin wrote, not the suggestion, and refuses a name that is not a host name", async () => {
    await mount(data());
    await openAdd();
    await checked();
    await type(field("Name of this profile on de1"), "203.0.113.9");
    expect(text()).toContain("Not a host name");
    expect(disabled(button("Put beside on 443") as HTMLButtonElement)).toBe(true);
    await type(field("Name of this profile on de1"), "Media.Example.com");
    await click(button("Put beside on 443"));
    await checked();
    expect(createInbound.mock.calls.map(([r]) => r as { tlsServerNameOverride?: string }).at(-1)?.tlsServerNameOverride).toBe("media.example.com");
  });

  it("takes the free port instead when the admin prefers it", async () => {
    await mount(data());
    await openAdd();
    await checked();
    await click(button("Take 8443"));
    await checked();
    expect(createInbound.mock.calls.map(([r]) => r as { portOverride?: number }).at(-1)?.portOverride).toBe(8443);
    expect(text()).not.toContain("Put it beside");
    expect(disabled(saveButton())).toBe(false);
  });

  it("says, for each of the other refusals, what is wrong and what can be done, and keeps the button off", async () => {
    const cases: [string, Code, string[]][] = [
      [`sni_taken: port=443&node=de1&profile=Telegram&name=www.example.net&free=8443`, Code.AlreadyExists, ["www.example.net", "Take 8443"]],
      [`share_needs_site: port=443&node=de1&profile=Reality+main&free=8443`, Code.FailedPrecondition, ["node’s own site", "Take 8443"]],
      [`share_unsupported: port=443&node=de1&profile=Reality+main&protocol=vless`, Code.FailedPrecondition, ["cannot share port 443", "vless"]],
      [`share_agent_old: port=443&node=de1&profile=Reality+main`, Code.FailedPrecondition, ["needs a newer agent", "Open updates"]],
    ];
    for (const [msg, code, words] of cases) {
      createInbound.mockReset();
      createInbound.mockRejectedValue(refuse(msg, code));
      await mount(data());
      await click(button("Add profile"));
      await checked(); // the list asks first; a refused profile "needs an action": its form is the action
      // an old agent is told in the list itself (the way out is the updates); the others open their form
      if (code !== Code.FailedPrecondition || !msg.startsWith("share_agent_old")) {
        await click(button("Set up…"));
        await checked();
        expect(disabled(saveButton()), msg).toBe(true);
      } else expect(button("Set up…")).toBeUndefined();
      for (const w of words) expect(text(), msg).toContain(w);
      expect(text(), msg).not.toMatch(/[{}]/);
      act(() => root?.unmount());
      host?.remove();
    }
  });
});

describe("editing the port of a profile that moves onto a port another holds", () => {
  it("rejects invalid public ports without rewriting them and clears only the external override", async () => {
    updateInbound.mockResolvedValue({ warnings: [] });
    await mount(data({ inbounds: [inb({ port: 19443, publicPort: 443 })] }));
    await click(button("Edit"));
    const input = field("Public TCP port (optional)");
    await type(input, "100000");
    expect(input?.value).toBe("100000");
    expect(disabled(saveButton())).toBe(true);
    await type(input, "80");
    expect(text()).toContain("Port 80 is kept for");
    expect(disabled(saveButton())).toBe(true);
    await type(input, "");
    await checked();
    await click(saveButton());
    await settle();
    const writes = updateInbound.mock.calls.map(([r]) => r).filter((r) => !r.validateOnly);
    expect(writes).toHaveLength(1);
    expect(writes[0].publicPort).toBe(0);
    expect(writes[0].portOverride).toBeUndefined();
  });

  it("offers the name in the dialog, keeps Save off with the reason in view, and saves once the name is put", async () => {
    updateInbound.mockImplementation(async (req: { validateOnly?: boolean; portOverride?: number; tlsServerNameOverride?: string }) => {
      const a = panel({ portOverride: req.portOverride ?? 8443, tlsServerNameOverride: req.tlsServerNameOverride ?? "de1.example.com" });
      return req.validateOnly ? a : { inbound: {}, warnings: a.warnings };
    });
    await mount(data({ inbounds: [inb({}), inb({ id: "inb_t", profileId: "prf_tg", profileName: "Telegram", protocol: "mtproto", port: 8443 })] }));
    const edits = [...document.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "Edit");
    await click(edits[1]);
    await type(field("Port"), "443");
    await checked();
    expect(disabled(saveButton())).toBe(true);
    expect(text()).toContain("Put it beside “Reality main” on port 443");
    await click(button("Put beside on 443"));
    await checked();
    expect(text()).toContain("is shared by Reality main, Telegram");
    expect(disabled(saveButton())).toBe(false);
    await click(saveButton());
    await settle();
    const real = updateInbound.mock.calls.map(([r]) => r as { validateOnly?: boolean; portOverride?: number; tlsServerNameOverride?: string }).filter((r) => !r.validateOnly);
    expect(real).toHaveLength(1);
    expect(real[0]).toMatchObject({ portOverride: 443, tlsServerNameOverride: "cdn.de1.example.com" });
  });

  it("never leaves the Save button off in silence: a refusal no field words is said at the foot of the dialog", async () => {
    updateInbound.mockRejectedValue(refuse("node_retired", Code.FailedPrecondition));
    await mount(data({ inbounds: [inb({ id: "inb_t", profileName: "Telegram", protocol: "mtproto", port: 8443 })] }));
    await click(button("Edit"));
    await type(field("Port"), "9443");
    await checked();
    expect(disabled(saveButton())).toBe(true);
    expect(document.querySelector("[role=dialog] [role=alert]")?.textContent).toBeTruthy();
  });
});

describe("the card of a shared port", () => {
  const inbounds = [
    inb({}),
    inb({ id: "inb_b", profileId: "prf_b", profileName: "Telegram", protocol: "mtproto", tlsServerName: "cdn.de1.example.com" }),
    inb({ id: "inb_c", profileId: "prf_c", profileName: "TrustTunnel", protocol: "trusttunnel", tlsServerName: "static.de1.example.com" }),
  ];
  const snap = (over: Record<string, unknown> = {}) => ({
    port: 443,
    defaultInboundId: "inb_a",
    accepted: 1000,
    routes: [{ inboundId: "inb_a", delivered: 600 }, { inboundId: "inb_b", delivered: 300 }, { inboundId: "inb_c", delivered: 90 }],
    defaultNoSni: 7,
    defaultUnknownName: 3,
    defaultNotTls: 0,
    defaultBadHello: 0,
    defaultHelloTooLarge: 0,
    defaultRouteDown: 0,
    timeouts: 4,
    evicted: 0,
    refused: 0,
    noRoute: 0,
    unknownNames: [{ name: "www.example.org", count: 12 }],
    bindError: "",
    ...over,
  });

  it("lists who shares the port, who is the default, and what the node counted", async () => {
    await mount(data({ inbounds, sharedPorts: [snap()], sharedPortsUnix: Math.floor(Date.now() / 1000) - 120 }));
    const card = document.querySelector("[data-testid=shared-port-443]")!;
    expect(card.textContent).toContain("TCP 443 is shared");
    expect(card.textContent).toContain("3 services answer on this port");
    for (const w of ["Reality main", "Telegram", "TrustTunnel", "cdn.de1.example.com", "static.de1.example.com"]) expect(card.textContent).toContain(w);
    expect(card.querySelectorAll("li")).toHaveLength(3);
    expect(card.textContent).toContain("Default");
    expect(card.textContent).toContain("www.example.org");
    expect(card.textContent).toContain("2 min ago");
    // counters: accepted, to the default (7 + 3), refused, down, silent, dropped
    // (the figures shuffle into place; the value is the label of each)
    const shown = [...card.querySelectorAll("[role=img]")].map((e) => e.getAttribute("aria-label"));
    expect(shown).toEqual(["600", "300", "90", "1,000", "10", "0", "0", "4", "0"]);
    expect(card.textContent).toContain("Accepted");
    expect(card.textContent).toContain("To the default");
    expect(card.textContent).toContain("Silent 10 s");
    expect(card.textContent).not.toContain("cannot share a port yet");
  });

  it("is amber where handshakes were dropped and red where the node could not take the port", async () => {
    await mount(data({ inbounds, sharedPorts: [snap({ evicted: 1480, refused: 3 })], sharedPortsUnix: 1 }));
    expect(document.querySelector("[data-testid=shared-port-443] .text-warn-text")).toBeTruthy();
    act(() => root?.unmount());
    host?.remove();
    await mount(data({ inbounds, sharedPorts: [snap({ bindError: "listen tcp :443: bind: address already in use" })], sharedPortsUnix: 1 }));
    expect(document.querySelector("[data-testid=shared-port-443] [role=alert]")?.textContent).toContain("could not take port 443");
  });

  it("says the agent cannot share yet, names who waits, and links to the updates", async () => {
    const held = "share_agent_old: this node's agent cannot share a TCP port between services by SNI";
    await mount(data({ inbounds: [inbounds[0], { ...inbounds[1], state: InboundState.FAILED, lastError: held }, { ...inbounds[2], state: InboundState.FAILED, lastError: held }], sharedPorts: [] }));
    const card = document.querySelector("[data-testid=shared-port-443]")!;
    expect(card.textContent).toContain("This node cannot share a port yet");
    expect(card.textContent).toContain("Only “Reality main” runs on port 443");
    expect(card.textContent).toContain("Telegram, TrustTunnel");
    expect(card.querySelector("a[href='/updates']")).toBeTruthy();
    expect(card.textContent).not.toContain("has not reported");
    // the held profile's own row says why, with the way out
    expect(document.body.textContent).toContain("cannot share a TCP port yet, so this profile is not sent to it");
    expect([...document.querySelectorAll("a[href='/updates']")].length).toBeGreaterThanOrEqual(3);
  });

  it("waits for the first report, and is not there for a port one service holds or two that are not both on", async () => {
    await mount(data({ inbounds, sharedPorts: [] }));
    expect(document.querySelector("[data-testid=shared-port-443]")?.textContent).toContain("has not reported on this port yet");
    act(() => root?.unmount());
    host?.remove();
    await mount(data({ inbounds: [inbounds[0], { ...inbounds[1], state: InboundState.DISABLED }], sharedPorts: [] }));
    expect(document.querySelector("[data-testid=shared-ports]")).toBeNull();
  });

  it("says why a profile does not run when its name is held by another (sni_shadowed), and offers the domain", async () => {
    const shadowed = { ...inbounds[1], state: InboundState.FAILED, lastError: "sni_shadowed: the name de1.example.com is served by inbound inb_a" };
    await mount(data({ inbounds: [inbounds[0], shadowed] }));
    expect(document.body.textContent).toContain("holds the name this one answers to");
    expect(button("Set the domain")).toBeTruthy();
  });
});
