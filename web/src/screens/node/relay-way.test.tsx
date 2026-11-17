import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { relaysQuery, type RelayMsg } from "@/lib/relay";
import { AddRelayDialog, useRelayRuns } from "./relays";
import { RelayRows } from "./relay-rows";
import { LinkViaLine } from "./relay-way";

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
  nodes: { listNodes: () => Promise.resolve({ nodes: [{ id: "nod_de1", name: "de1", countryCode: "DE" }, { id: "nod_msk1", name: "msk1", countryCode: "RU" }] }) },
  countryReach: { getCountryReachability: () => Promise.resolve({ settings: { country: "RU" } }) },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { updateIntervalHours: 12 }, effectiveTitle: "", serverSamples: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: 1 } }) },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search, className }: { children?: ReactNode; to: string; params?: object; search?: object; className?: string }) => (
    <a href={to} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(search ?? {})} className={className}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const now = Math.floor(Date.now() / 1000);
const stage = (over: Record<string, unknown> = {}) => ({ status: "ok", code: "", senderId: "", senderName: "", verdict: "ours", sent: 0, got: 0, atUnix: now - 120, mb: 0, mbitS: 0, bytes: 0, connectMs: 0, firstByteMs: 0, durationMs: 0, port: 0, ...over });
const check = (over: Record<string, unknown> = {}) => ({ atUnix: now - 120, network: "tcp", port: 8443, to: "203.0.113.7:443", leg: stage(), path: stage({ status: "skipped", code: "off", verdict: "" }), ok: true, stage: "", verified: false, ...over });
const relay = (over: Record<string, unknown> = {}) => ({
  id: "rly_1", inboundId: "inb_v", exitNodeId: "nod_de1", exitNodeName: "de1", profileId: "prf_v", profileName: "VLESS · TCP", protocol: "vless", viaNodeId: "nod_msk1", viaNodeName: "msk1",
  network: "tcp", port: 8443, exitPort: 443, enabled: true, state: InboundState.ACTIVE, lastError: "", toIp: "203.0.113.7", appliedUnix: now - 600, exitLive: true, agentSupported: true, createdUnix: now - 3600,
  mode: "auto", activeMode: "dnat", modeChangedUnix: 0, modeReason: "", directStreak: 0, reverseBlocked: "", check: check({ path: stage({ senderId: "nod_nl1", senderName: "nl1" }), verified: true }),
  ...over,
});
const bridge = (over: Record<string, unknown> = {}) => ({ connected: true, conns: 2, rttMs: 38, sinceUnix: now - 3 * 3600, error: "", clients: 120, pool: 2, capacity: 230, nearFull: false, ...over });
/** A relay that stands in the bridge: the leg fails, the exit holds the bridge. */
const bridged = (over: Record<string, unknown> = {}) =>
  relay({
    activeMode: "reverse", modeReason: "leg_failed", modeChangedUnix: now - 3 * 3600, directStreak: 2, bridge: bridge(),
    check: check({ ok: false, stage: "leg", leg: stage({ status: "fail", code: "timeout", verdict: "timeout" }), reverseReach: stage({ port: 8443 }), bridge: stage(), path: stage({ senderId: "nod_nl1", senderName: "nl1" }) }),
    ...over,
  });

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  listRelayCandidates.mockResolvedValue({ agentSupported: true, entryCountry: { verdict: "ok", dark: [], atUnix: now }, candidates: [] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listRelays, listRelayCandidates, createRelay, checkRelay, updateRelay, deleteRelay]) m.mockReset();
});

function Rows({ readOnly }: { readOnly?: boolean }) {
  const q = useQuery(relaysQuery("nod_msk1"));
  const runs = useRelayRuns();
  return <RelayRows list={(q.data ?? []) as RelayMsg[]} runs={runs} readOnly={readOnly} people="RU" />;
}
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
const rows = () => [...document.querySelectorAll<HTMLElement>("[data-testid=relay-row]")];
const rowOf = (name: string) => rows().find((r) => r.textContent?.includes(name))!;
const buttonIn = (el: ParentNode, label: string) => [...el.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
/** A row's menu, opened, and the item of it clicked. */
const menuItem = async (row: HTMLElement, label: string) => {
  await click(row.querySelector("button[aria-haspopup=menu]"));
  await click([...document.querySelectorAll<HTMLElement>("[role=menuitem]")].find((b) => b.textContent === label));
};
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]")!;
const radio = (value: string) => dialog().querySelector<HTMLInputElement>(`input[type=radio][value=${value}]`)!;

describe("the choice of the way", () => {
  it("offers auto, direct and via bridge, with the line of each; saving sends the mode", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    updateRelay.mockResolvedValue({ relay: relay({ mode: "dnat" }) });
    await mount(<Rows />);
    await menuItem(rowOf("VLESS · TCP"), "Method…");
    expect(dialog().textContent).toContain("Way of the relay msk1 → de1");
    const names = [...dialog().querySelectorAll("[data-testid=way-choice] label > span > span:first-child")].map((s) => s.textContent);
    expect(names).toEqual(["Auto (recommended)", "Direct", "Via bridge"]);
    expect(radio("auto").checked).toBe(true);
    expect(radio("reverse").disabled).toBe(false);
    expect(buttonIn(dialog(), "Change")?.disabled).toBe(true); // nothing changed yet
    await click(radio("dnat"));
    expect(dialog().textContent).toContain("open connections of people drop");
    await click(buttonIn(dialog(), "Change"));
    await settle();
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_1", mode: "dnat" });
  });

  it("a UDP relay cannot be put on the bridge: the choice is greyed with the honest reason, direct and auto stay", async () => {
    const udp = relay({ profileName: "Hysteria2", protocol: "hysteria2", network: "udp", reverseBlocked: "udp", modeReason: "udp" });
    listRelays.mockResolvedValue({ relays: [udp] });
    await mount(<Rows />);
    // the reason stays out of the row (it would repeat on every UDP relay), and the speed check is not offered
    expect(rowOf("Hysteria2").textContent).not.toContain("A bridge works only for TCP");
    expect(buttonIn(rowOf("Hysteria2"), "Speed check (8 MB)")).toBeUndefined();
    await menuItem(rowOf("Hysteria2"), "Method…");
    expect(radio("reverse").disabled).toBe(true);
    expect(radio("auto").disabled).toBe(false);
    expect(radio("dnat").disabled).toBe(false);
    expect(dialog().textContent).toContain("A bridge works only for TCP for now: VLESS and MTProto. Hysteria2 and AmneziaWG stay direct.");
    expect(dialog().textContent).not.toContain("DNAT");
    expect(radio("auto").checked).toBe(true);
  });

  it("an agent that is too old names the node to update", async () => {
    listRelays.mockResolvedValue({ relays: [relay({ reverseBlocked: "exit_agent_old", modeReason: "reverse_unsupported" })] });
    await mount(<Rows />);
    await menuItem(rowOf("VLESS · TCP"), "Method…");
    expect(radio("reverse").disabled).toBe(true);
    expect(dialog().textContent).toContain("A bridge is not available: update the agent of de1.");
  });

  it("a refused change says why, in words", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    const { ConnectError, Code } = await import("@connectrpc/connect");
    updateRelay.mockRejectedValue(new ConnectError("reverse_unavailable: reason=entry_agent_old", Code.FailedPrecondition));
    await mount(<Rows />);
    await menuItem(rowOf("VLESS · TCP"), "Method…");
    await click(radio("reverse"));
    await click(buttonIn(dialog(), "Change"));
    await settle();
    expect(dialog().querySelector("[role=alert]")?.textContent).toBe("A bridge is not possible for this relay: the agent of the entry is too old.");
  });
});

describe("the exit's card, when its agent reaches the panel through an entry", () => {
  it("says through which entry, that it is a fallback and that the agent returns by itself", async () => {
    await mount(<LinkViaLine viaNodeId="nod_msk1" />);
    expect(document.body.textContent).toContain("Link to the panel: through msk1");
    expect(document.body.textContent).toContain("A fallback path");
    expect(document.body.textContent).toContain("The agent returns to the direct connection by itself.");
  });
  it("says nothing for a direct connection", async () => {
    await mount(<LinkViaLine viaNodeId="" />);
    expect(document.body.textContent).toBe("");
  });
});

describe("the Add relay dialog and the bridge", () => {
  const cut = { verdict: "blocked", dark: ["MTS"], atUnix: now };
  const cand = (over: Record<string, unknown>) => ({
    inboundId: "inb_v", nodeId: "nod_de1", nodeName: "de1", profileId: "prf_v", profileName: "VLESS · TCP", protocol: "vless", network: "tcp", port: 443, unsupported: "", hop: false, relayId: "",
    country: cut, reach: { verdict: "blocked", source: "tspu_row", atUnix: now }, exitReachesEntry: { verdict: "reach", source: "exit_probe", atUnix: now }, reverseBlocked: "", ...over,
  });
  const open = async (candidates: unknown[]) => {
    listRelayCandidates.mockResolvedValue({ agentSupported: true, entryCountry: { verdict: "ok", dark: [], atUnix: now }, candidates });
    listRelays.mockResolvedValue({ relays: [] });
    await mount(<AddRelayDialog open onOpenChange={() => {}} entry={msk1} />);
    await settle();
  };
  const tick = (label: string) => click(dialog().querySelector(`input[aria-label="${label}"]`));
  const noteOf = (profile: string) => [...dialog().querySelectorAll<HTMLElement>("[data-testid=relay-candidate]")].find((s) => s.textContent?.includes(profile))?.querySelector("[data-testid=relay-bridge-note]")?.textContent ?? null;

  it("marks a server that direct cannot reach but a bridge can; a UDP one is never offered the bridge", async () => {
    await open([
      cand({}),
      cand({ inboundId: "inb_h", profileName: "Hysteria2", protocol: "hysteria2", network: "udp", port: 8443, reverseBlocked: "udp" }),
      cand({ inboundId: "inb_n", profileName: "Neither", exitReachesEntry: { verdict: "blocked", source: "exit_probe", atUnix: now } }),
      cand({ inboundId: "inb_o", profileName: "Old agent", reverseBlocked: "exit_agent_old" }),
      cand({ inboundId: "inb_p", profileName: "Passes", reach: { verdict: "reach", source: "tspu_row", atUnix: now } }),
    ]);
    expect(noteOf("VLESS · TCP")).toBe("Direct does not pass, but a bridge is possible: de1 reaches msk1");
    expect(noteOf("Hysteria2")).toBe("Direct only: UDP does not go over a bridge yet");
    expect(noteOf("Neither")).toBe("Neither direct nor a bridge passes: msk1 and de1 do not reach each other");
    expect(noteOf("Old agent")).toBe("A bridge is not available: update the agent of de1.");
    expect(noteOf("Passes")).toBeNull();
  });

  it("the way is auto, folded away, and opens to the three; a UDP server picked greys the bridge with its reason", async () => {
    await open([cand({}), cand({ inboundId: "inb_h", profileName: "Hysteria2", protocol: "hysteria2", network: "udp", port: 8443, reverseBlocked: "udp" })]);
    expect(dialog().textContent).toContain("Way: Auto (recommended)");
    expect(dialog().querySelector("[data-testid=way-choice]")).toBeNull();
    await click(buttonIn(dialog(), "Way: Auto (recommended)"));
    await tick("Relay VLESS · TCP of de1");
    expect(radio("reverse").disabled).toBe(false);
    await tick("Relay Hysteria2 of de1");
    expect(radio("reverse").disabled).toBe(true);
    expect(dialog().textContent).toContain("A bridge works only for TCP for now");
  });

  it("direct only is sent as the mode; auto sends none", async () => {
    await open([cand({})]);
    createRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, mode: "dnat", check: undefined }) });
    checkRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, mode: "dnat", check: check() }) });
    await tick("Relay VLESS · TCP of de1");
    await click(buttonIn(dialog(), "Way: Auto (recommended)"));
    await click(radio("dnat"));
    await click(buttonIn(dialog(), "Only check"));
    await settle();
    expect(createRelay).toHaveBeenCalledWith({ inboundId: "inb_v", viaNodeId: "nod_msk1", port: 0, mode: "dnat" });
    expect(checkRelay).toHaveBeenCalledWith({ id: "rly_9" }); // direct only: the bridge is not asked about
  });

  it("auto with a leg that fails and a bridge that passes turns the relay on as a bridge, stage by stage", async () => {
    await open([cand({})]);
    const found = check({ ok: false, stage: "leg", leg: stage({ status: "fail", code: "timeout", verdict: "timeout" }), reverseReach: stage({ port: 443 }) });
    createRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, check: undefined }) });
    checkRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, check: found }) });
    updateRelay.mockResolvedValue({ relay: bridged({ id: "rly_9" }) });
    await tick("Relay VLESS · TCP of de1");
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    expect(checkRelay).toHaveBeenCalledWith({ id: "rly_9", reverseReach: true });
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_9", enabled: true });
    const runs = dialog().querySelector("[data-testid=relay-runs]")!;
    expect(runs.textContent).toContain("msk1 → de1fails");
    expect(runs.textContent).toContain("de1 → msk1, for a bridgepasses");
    expect(runs.textContent).toContain("Bridge held by de1passes");
    expect(runs.textContent).toContain("From outside through msk1passes");
    expect(runs.textContent).toContain("Works through a bridge. People get the server “via RU” with their next subscription update (every 12 h).");
    expect(runs.querySelector(".text-danger-text")).toBeNull();
  });

  it("'Only check' on a pair that only the bridge reaches says so and leaves the relay off", async () => {
    await open([cand({})]);
    const found = check({ ok: false, stage: "leg", leg: stage({ status: "fail", code: "timeout" }), reverseReach: stage({ port: 443 }) });
    createRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, check: undefined }) });
    checkRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, check: found }) });
    await tick("Relay VLESS · TCP of de1");
    await click(buttonIn(dialog(), "Only check"));
    await settle();
    expect(updateRelay).not.toHaveBeenCalled();
    expect(dialog().textContent).toContain("The check passed, but only through a bridge: msk1 does not reach de1, while de1 reaches msk1.");
  });

  it("neither direct nor the bridge passing says both reasons", async () => {
    await open([cand({})]);
    const found = check({ ok: false, stage: "leg", leg: stage({ status: "fail", code: "timeout" }), reverseReach: stage({ status: "fail", code: "timeout", port: 443 }) });
    createRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, check: undefined }) });
    checkRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, check: found }) });
    await tick("Relay VLESS · TCP of de1");
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    expect(updateRelay).not.toHaveBeenCalled();
    expect(dialog().textContent).toContain("Not turned on: msk1 cannot reach de1 on 443/tcp; a bridge does not help either: de1 does not reach the site of msk1 on 443: a bridge will not help here.");
  });

  it("a bridge that does not come up when switched on says which stage failed, not a general refusal", async () => {
    await open([cand({})]);
    const { ConnectError, Code } = await import("@connectrpc/connect");
    const found = check({ ok: false, stage: "leg", leg: stage({ status: "fail", code: "timeout" }), reverseReach: stage({ port: 443 }) });
    createRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, check: undefined }) });
    checkRelay.mockResolvedValue({ relay: relay({ id: "rly_9", enabled: false, check: found }) });
    updateRelay.mockRejectedValue(new ConnectError("relay_check_failed: code=pin_mismatch&stage=bridge", Code.FailedPrecondition));
    await tick("Relay VLESS · TCP of de1");
    await click(buttonIn(dialog(), "Check and turn on"));
    await settle();
    const runs = dialog().querySelector("[data-testid=relay-runs]")!;
    expect(runs.textContent).toContain("Bridge held by de1fails");
    expect(runs.textContent).toContain("From outside through msk1not checked");
    expect(runs.textContent).toContain("Not turned on: the certificate of the site of msk1 changed, or someone intercepts the connection");
  });
});
