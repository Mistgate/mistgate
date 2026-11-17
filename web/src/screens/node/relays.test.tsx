import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { en } from "@/i18n/en";
import type { RelayMsg } from "@/lib/relay";
import { AddRelayDialog, ChangeRelayPort } from "./relays";

const listRelays = vi.fn();
const listRelayCandidates = vi.fn();
const createRelay = vi.fn();
const checkRelay = vi.fn();
const updateRelay = vi.fn();
const deleteRelay = vi.fn();
vi.mock("@/lib/api", () => ({
  relays: {
    listRelays: (...a: unknown[]) => listRelays(...a),
    listRelayCandidates: (...a: unknown[]) => listRelayCandidates(...a),
    createRelay: (...a: unknown[]) => createRelay(...a),
    checkRelay: (...a: unknown[]) => checkRelay(...a),
    updateRelay: (...a: unknown[]) => updateRelay(...a),
    deleteRelay: (...a: unknown[]) => deleteRelay(...a),
  },
  nodes: { listNodes: () => Promise.resolve({ nodes: [{ id: "nod_de1", countryCode: "DE" }, { id: "nod_fi1", countryCode: "FI" }, { id: "nod_msk1", countryCode: "RU" }, { id: "nod_ru2", countryCode: "RU" }] }) },
  countryReach: { getCountryReachability: () => Promise.resolve({ settings: { country: "RU" } }) },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { updateIntervalHours: 12 }, effectiveTitle: "", serverSamples: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: 1 } }) },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, hash, className }: { children?: ReactNode; to: string; params?: object; hash?: string; className?: string }) => (
    <a href={to + (hash ? `#${hash}` : "")} data-params={JSON.stringify(params ?? {})} className={className}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const now = Math.floor(Date.now() / 1000);
const stage = (over: Record<string, unknown> = {}) => ({ status: "ok", code: "", senderId: "", senderName: "", verdict: "ours", sent: 0, got: 0, atUnix: now - 120, ...over });
const check = (over: Record<string, unknown> = {}) => ({ atUnix: now - 120, network: "tcp", port: 8443, to: "203.0.113.7:443", leg: stage(), path: stage({ status: "skipped", code: "off", verdict: "" }), ok: true, stage: "", verified: false, ...over });
const relay = (over: Record<string, unknown> = {}) => ({
  id: "rly_1", inboundId: "inb_v", exitNodeId: "nod_de1", exitNodeName: "de1", profileId: "prf_v", profileName: "VLESS · TCP", protocol: "vless", viaNodeId: "nod_msk1", viaNodeName: "msk1",
  network: "tcp", port: 8443, exitPort: 443, enabled: false, state: InboundState.PENDING, lastError: "", toIp: "", appliedUnix: 0, exitLive: true, agentSupported: true, createdUnix: now - 3600,
  ...over,
});
const on = relay({ enabled: true, state: InboundState.ACTIVE, toIp: "203.0.113.7", check: check({ path: stage({ senderId: "nod_nl1", senderName: "nl1" }), verified: true }) });
const fresh = relay({ id: "rly_3", inboundId: "inb_f", exitNodeId: "nod_fi1", exitNodeName: "fi1", profileName: "VLESS · Reality", check: check() });

const cut = { verdict: "partial", dark: ["MTS"], atUnix: now };
const cand = (over: Record<string, unknown>) => ({ inboundId: "inb_v", nodeId: "nod_de1", nodeName: "de1", profileId: "prf_v", profileName: "VLESS · TCP", protocol: "vless", network: "tcp", port: 443, unsupported: "", hop: false, relayId: "", country: cut, reach: { verdict: "reach", source: "tspu_row", atUnix: now }, ...over });
const candidates = {
  agentSupported: true,
  entryCountry: { verdict: "ok", dark: [], atUnix: now },
  candidates: [
    cand({}),
    cand({ inboundId: "inb_h", profileName: "Hysteria2", protocol: "hysteria2", network: "udp", port: 8443, hop: true }),
    cand({ inboundId: "inb_c", profileName: "VLESS · CDN", unsupported: "cdn" }),
    cand({ inboundId: "inb_x", nodeId: "nod_fi1", nodeName: "fi1", profileName: "VLESS · Reality", relayId: "rly_3", country: { verdict: "ok", dark: [], atUnix: now }, reach: { verdict: "no_data", source: "", atUnix: 0 } }),
    cand({ inboundId: "inb_y", nodeId: "nod_fi1", nodeName: "fi1", profileName: "Trojan", country: { verdict: "ok", dark: [], atUnix: now }, reach: { verdict: "no_data", source: "", atUnix: 0 } }),
  ],
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  listRelayCandidates.mockResolvedValue(candidates);
  deleteRelay.mockResolvedValue({});
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listRelays, listRelayCandidates, createRelay, checkRelay, updateRelay, deleteRelay]) m.mockReset();
});

const msk1 = { id: "nod_msk1", name: "msk1", cc: "RU" };

async function mount(ui: ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>{ui}</ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const buttonIn = (el: ParentNode, label: string) => [...el.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]")!;
const tick = (label: string) => click(dialog().querySelector(`input[aria-label="${label}"]`));

describe("the Add relay dialog", () => {
  it("an old agent on the entry gets the reason and a way to Updates instead of the list", async () => {
    listRelayCandidates.mockResolvedValue({ ...candidates, agentSupported: false });
    await open();
    expect(dialog().textContent).toContain("Update the node agent to add relays.");
    expect(dialog().querySelector("a[href='/updates']")).not.toBeNull();
    expect(dialog().querySelector("[data-testid=relay-candidate]")).toBeNull();
    expect(buttonIn(dialog(), "Check and turn on")?.disabled).toBe(true);
  });

  it("for one exit (a way in to it) lists only that node's servers, without the group titles", async () => {
    await open("nod_de1");
    expect([...dialog().querySelectorAll("[data-testid=relay-candidate-node]")].map((n) => n.textContent?.includes("de1"))).toEqual([true]);
    expect(dialog().querySelectorAll("section h3")).toHaveLength(0);
  });

  const open = async (exitId?: string) => {
    listRelays.mockResolvedValue({ relays: [fresh] });
    await mount(<AddRelayDialog open onOpenChange={() => {}} entry={msk1} exitId={exitId} />);
    await settle();
  };

  it("puts the exits cut off from RU first, with the three hints; a server that cannot be relayed or is relayed already cannot be picked", async () => {
    await open();
    expect(dialog().textContent).toContain("Relay through msk1");
    const groups = [...dialog().querySelectorAll("section h3")].map((h) => h.textContent);
    expect(groups).toEqual(["Suggested: unreachable from part of RU", "Other nodes"]);
    const [de1, fi1] = [...dialog().querySelectorAll<HTMLElement>("[data-testid=relay-candidate-node]")];
    expect([...de1!.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["From RU: blocked on MTS", "msk1 → de1 passes (TSPU check)", "People reach msk1 from MTS"]);
    expect([...fi1!.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["From RU: reachable", "No check data from msk1"]);

    const servers = [...dialog().querySelectorAll<HTMLElement>("[data-testid=relay-candidate]")];
    const cdn = servers.find((s) => s.textContent?.includes("VLESS · CDN"))!;
    expect(cdn.dataset.off).toBe("true");
    expect(cdn.querySelector("input")?.disabled).toBe(true);
    expect(cdn.textContent).toContain(en["relay.unsupported.cdn"]);
    expect(servers.find((s) => s.textContent?.includes("VLESS · Reality"))?.textContent).toContain("Already relayed through msk1");
    expect(servers.find((s) => s.textContent?.includes("Hysteria2"))?.textContent).toContain("Has a port range (hop): only port 8443 goes through the relay.");
    expect(buttonIn(dialog(), "Check and turn on")?.disabled).toBe(true); // nothing picked yet
  });

  it("'Check and turn on' makes the relay, checks the leg, switches it on and says when people get it", async () => {
    await open();
    createRelay.mockResolvedValue({ relay: relay({ id: "rly_9" }) });
    checkRelay.mockResolvedValue({ relay: relay({ id: "rly_9", check: check() }) });
    updateRelay.mockResolvedValue({ relay: on });
    await tick("Relay VLESS · TCP of de1");
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    expect(createRelay).toHaveBeenCalledWith({ inboundId: "inb_v", viaNodeId: "nod_msk1", port: 0 });
    expect(checkRelay).toHaveBeenCalledWith({ id: "rly_9" });
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_9", enabled: true });
    expect(dialog().querySelector("[data-testid=relay-runs]")?.textContent).toContain("Works. People get the server “via RU” with their next subscription update (every 12 h).");
  });

  it("a failed leg stops before switching on and says why; 'Only check' leaves it off", async () => {
    await open();
    createRelay.mockResolvedValue({ relay: relay({ id: "rly_9" }) });
    checkRelay.mockResolvedValue({ relay: relay({ id: "rly_9", check: check({ ok: false, stage: "leg", leg: stage({ status: "fail", code: "timeout", verdict: "timeout" }) }) }) });
    await tick("Relay VLESS · TCP of de1");
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    expect(updateRelay).not.toHaveBeenCalled();
    expect(dialog().textContent).toContain("Not turned on: msk1 cannot reach de1 on 443/tcp.");
  });

  it("a port that is taken offers the free one, and runs that server again on it", async () => {
    await open();
    createRelay.mockRejectedValueOnce(new ConnectError("port_taken: free=8444&node=msk1&port=443&profile=hy2", Code.AlreadyExists));
    createRelay.mockResolvedValueOnce({ relay: relay({ id: "rly_9", port: 8444 }) });
    checkRelay.mockResolvedValue({ relay: relay({ id: "rly_9", port: 8444, check: check() }) });
    await tick("Relay VLESS · TCP of de1");
    await click(buttonIn(dialog(), "Only check"));
    await settle();
    expect(dialog().textContent).toContain("Port 443 on msk1 is already taken by the profile “hy2”.");
    await click(buttonIn(dialog(), "Take 8444"));
    await settle();
    expect(createRelay).toHaveBeenLastCalledWith({ inboundId: "inb_v", viaNodeId: "nod_msk1", port: 8444 });
    expect(updateRelay).not.toHaveBeenCalled();
    expect(dialog().textContent).toContain(en["relay.dlg.result.checked"]);
  });
});

describe("AmneziaWG through a relay", () => {
  const awgDe = cand({ inboundId: "inb_awg", profileId: "prf_awg", profileName: "AWG 3.1", protocol: "awg", network: "udp", port: 51820 });
  // an exit in the entry's own country (RU, as msk1): cut from RU, but the route would not change
  const awgRu = cand({ inboundId: "inb_awg_ru", nodeId: "nod_ru2", nodeName: "ru2", profileId: "prf_awg", profileName: "AWG 3.1", protocol: "awg", network: "udp", port: 51820 });
  const open = async () => {
    listRelays.mockResolvedValue({ relays: [] });
    listRelayCandidates.mockResolvedValue({ ...candidates, candidates: [awgDe, awgRu] });
    await mount(<AddRelayDialog open onOpenChange={() => {}} entry={msk1} />);
    await settle();
  };

  it("an AmneziaWG server can be picked, and picking it says once what people get", async () => {
    await open();
    const row = [...dialog().querySelectorAll<HTMLElement>("[data-testid=relay-candidate]")].find((s) => s.textContent?.includes("AWG 3.1") && s.closest("[data-testid=relay-candidate-node]")?.textContent?.includes("de1"))!;
    expect(row.dataset.off).toBeUndefined();
    expect(row.querySelector("input")?.disabled).toBe(false);
    expect(row.textContent).not.toContain("later");
    expect(dialog().textContent).not.toContain(en["relay.dlg.awgNote"]);
    await tick("Relay AWG 3.1 of de1");
    expect(dialog().textContent).toContain(en["relay.dlg.awgNote"]);
    expect(dialog().textContent?.split(en["relay.dlg.awgNote"]).length).toBe(2);
    await tick("Relay AWG 3.1 of de1");
    expect(dialog().textContent).not.toContain(en["relay.dlg.awgNote"]);
  });

  it("an exit in the entry's own country is not suggested, stays pickable and says why", async () => {
    await open();
    const groups = [...dialog().querySelectorAll("section h3")].map((h) => h.textContent);
    expect(groups).toEqual(["Suggested: unreachable from part of RU", "Other nodes"]);
    const nodes = [...dialog().querySelectorAll<HTMLElement>("[data-testid=relay-candidate-node]")];
    const [suggested, others] = ["de1", "ru2"].map((n) => nodes.find((x) => x.textContent?.includes(n))!);
    expect(suggested!.closest("section")?.querySelector("h3")?.textContent).toBe("Suggested: unreachable from part of RU");
    expect(others!.closest("section")?.querySelector("h3")?.textContent).toBe("Other nodes");
    expect(others!.textContent).toContain("Same country as msk1");
    expect(others!.querySelector("input")?.disabled).toBe(false);
    expect(suggested!.textContent).not.toContain("Same country");
  });

  it("changing the port of an AmneziaWG relay warns that AmneziaVPN configs through the entry need downloading again", async () => {
    const awg = relay({ id: "rly_awg", profileName: "AWG 3.1", protocol: "awg", network: "udp", port: 51820, exitPort: 51820 }) as unknown as RelayMsg;
    await mount(<ChangeRelayPort r={awg} onClose={() => {}} />);
    expect(dialog().textContent).toContain(en["relay.dlg.portAwg"]);
    await act(async () => root!.unmount());
    root = null;
    host?.remove();
    await mount(<ChangeRelayPort r={on as unknown as RelayMsg} onClose={() => {}} />);
    expect(dialog().textContent).not.toContain(en["relay.dlg.portAwg"]);
  });
});
