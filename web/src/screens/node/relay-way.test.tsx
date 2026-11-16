import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { en } from "@/i18n/en";
import { relaysQuery, type RelayMsg } from "@/lib/relay";
import { AddRelayDialog, RelayRows, useRelayRuns } from "./relays";
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
  return <RelayRows list={(q.data ?? []) as RelayMsg[]} side="entry" runs={runs} readOnly={readOnly} />;
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
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]")!;
const radio = (value: string) => dialog().querySelector<HTMLInputElement>(`input[type=radio][value=${value}]`)!;

describe("the way of a relay in its row", () => {
  it("a relay in auto that is direct has its chip and the two buttons, and nothing else to say", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    await mount(<Rows />);
    const way = rowOf("VLESS · TCP").querySelector<HTMLElement>("[data-testid=relay-way]")!;
    expect(way.dataset.active).toBe("dnat");
    expect(way.textContent).toContain("Direct");
    expect(way.textContent).toContain("auto");
    expect(buttonIn(way, "Change way")).toBeDefined();
    expect(buttonIn(way, "Speed check (8 MB)")).toBeDefined();
    expect(way.querySelector("[data-testid=relay-bridge]")).toBeNull();
    expect(way.textContent).not.toContain("DNAT");
  });

  it("a relay on the bridge says so, why, how it connects, how many checks it has toward direct, and how the bridge holds", async () => {
    listRelays.mockResolvedValue({ relays: [bridged()] });
    await mount(<Rows />);
    const row = rowOf("VLESS · TCP");
    expect(row.dataset.state).toBe("on"); // the leg failing is the reason it is on the bridge, no outage
    const way = row.querySelector<HTMLElement>("[data-testid=relay-way]")!;
    expect(way.dataset.active).toBe("reverse");
    expect(way.textContent).toContain("Via bridge");
    expect(way.querySelector("[title]")?.getAttribute("title")).toBe("de1 connects to msk1 itself and keeps the line open.");
    expect(way.textContent).toContain("msk1 does not reach de1 directly, so the relay goes through a bridge.");
    expect(way.querySelector("[data-testid=relay-streak]")?.textContent).toBe("Returning to direct: 2 of 3 checks passed in a row.");
    const line = way.querySelector("[data-testid=relay-bridge]")!;
    expect(line.textContent).toContain("Bridge is up · 2 of 2 connections · 38 ms · up for 3 h · port 8443 of msk1");
    expect(way.querySelector("[data-testid=relay-load]")?.textContent).toBe("In use: 120 of ~230");
    expect(way.querySelector("[data-testid=relay-load]")?.textContent).not.toContain("nearly full");
    // the leg that fails is not a red mark on a relay that stands in the bridge
    expect(row.querySelector("[data-testid=relay-stages]")?.textContent).toContain("msk1 → de1fails");
    expect(row.querySelector("[data-testid=relay-stages]")?.textContent).toContain("de1 → msk1, for a bridgepasses");
    expect(row.querySelector("[data-testid=relay-stages] .text-danger-text")).toBeNull();
  });

  it("a bridge that is nearly full says so quietly; one at its limit says what happens to people, and at four connections offers a second entry", async () => {
    const near = bridged({ bridge: bridge({ clients: 190, nearFull: true }) });
    const full = bridged({ id: "rly_2", profileName: "At limit", bridge: bridge({ clients: 468, capacity: 470, conns: 4, pool: 4, error: "stream_unavailable" }) });
    listRelays.mockResolvedValue({ relays: [near, full] });
    await mount(<Rows />);
    const nearLoad = rowOf("VLESS · TCP").querySelector("[data-testid=relay-load]")!;
    expect(nearLoad.textContent).toBe("In use: 190 of ~230nearly full");
    expect(rowOf("VLESS · TCP").textContent).not.toContain("At its limit");
    const limit = rowOf("At limit");
    expect(limit.querySelector("[data-testid=relay-load]")?.textContent).toBe("In use: 468 of ~470At its limit");
    expect(limit.textContent).toContain("People who come now are turned away");
    expect(limit.textContent).toContain("The bridge cannot grow past 4 connections.");
    const another = [...limit.querySelectorAll("a")].find((a) => a.textContent?.includes("Another entry for de1"))!;
    expect(another.getAttribute("data-params")).toBe(JSON.stringify({ id: "nod_de1" }));
    expect(another.getAttribute("data-search")).toBe(JSON.stringify({ tab: "ways", add: "1" }));
    // at two connections the panel raises the pool by itself: no advice yet
    const small = bridged({ id: "rly_3", profileName: "Pool two", bridge: bridge({ clients: 190, nearFull: true, pool: 2 }) });
    listRelays.mockResolvedValue({ relays: [small] });
    await act(async () => root!.unmount());
    root = null;
    host?.remove();
    await mount(<Rows />);
    expect(rowOf("Pool two").textContent).not.toContain("Another entry");
  });

  it("a bridge that is not held says why in words; one that has not reported yet is on its way up", async () => {
    const down = bridged({ id: "rly_2", profileName: "Down", bridge: bridge({ connected: false, conns: 0, sinceUnix: 0, error: "connect" }), check: check({ ok: false, stage: "leg", leg: stage({ status: "fail", code: "timeout" }), bridge: stage({ status: "fail", code: "connect" }) }) });
    const coming = bridged({ id: "rly_3", profileName: "Coming", bridge: undefined, check: check({ ok: false, stage: "leg", leg: stage({ status: "fail", code: "timeout" }) }) });
    listRelays.mockResolvedValue({ relays: [down, coming] });
    await mount(<Rows />);
    expect(rowOf("Down").dataset.state).toBe("failed");
    // the stored check says it in the red line of the row; the way does not say it twice
    expect(rowOf("Down").querySelector("[data-testid=relay-down]")?.textContent).toContain("de1 cannot connect to msk1");
    expect(rowOf("Down").querySelector("[data-testid=relay-bridge]")?.textContent ?? "").not.toContain("Bridge is");
    // a bridge whose exit says it is down, before the stored check does, is the way's own line
    const early = bridged({ id: "rly_4", profileName: "Early", bridge: bridge({ connected: false, conns: 0, sinceUnix: 0, error: "connect" }) });
    listRelays.mockResolvedValue({ relays: [early, coming] });
    await act(async () => root!.unmount());
    root = null;
    host?.remove();
    await mount(<Rows />);
    expect(rowOf("Early").querySelector("[data-testid=relay-bridge]")?.textContent).toContain("Bridge is down: de1 cannot connect to msk1.");
    expect(rowOf("Coming").querySelector("[data-testid=relay-bridge]")?.textContent).toContain("Bridge is starting…");
  });

  it("a relay that went back to direct says why; the owner's choice says it was the owner's", async () => {
    listRelays.mockResolvedValue({ relays: [relay({ modeReason: "direct_back", modeChangedUnix: now - 600 }), relay({ id: "rly_2", profileName: "Chosen", mode: "reverse", activeMode: "reverse", modeReason: "owner", bridge: bridge() })] });
    await mount(<Rows />);
    expect(rowOf("VLESS · TCP").textContent).toContain("msk1 reaches de1 directly again, so the relay went back to the direct way.");
    expect(rowOf("Chosen").textContent).toContain("Bridge only: your choice.");
    expect(rowOf("Chosen").querySelector("[data-testid=relay-streak]")).toBeNull(); // only auto counts its way back
  });

  it("the speed results sit under the line, a failed one with its reason", async () => {
    const speeds = bridged({
      check: check({
        ok: false, stage: "leg", leg: stage({ status: "fail", code: "timeout" }), bridge: stage(),
        reverseSpeed: stage({ status: "fail", code: "timeout", mb: 8, bytes: 2_000_000, atUnix: now - 300 }),
        directSpeed: stage({ mb: 8, mbitS: 31, durationMs: 2100, atUnix: now - 1800 }),
      }),
    });
    listRelays.mockResolvedValue({ relays: [speeds] });
    await mount(<Rows />);
    const list = rowOf("VLESS · TCP").querySelector("[data-testid=relay-speeds]")!;
    expect(list.textContent).toContain("Speed de1 → msk1: the flow broke off: 2 of 8 MB arrived");
    expect(list.textContent).toContain("Direct flow msk1 → de1: 31 Mbit/s · 8 MB in 2.1 s");
  });
});

describe("the choice of the way", () => {
  it("offers auto, direct and via bridge, with the line of each; saving sends the mode", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    updateRelay.mockResolvedValue({ relay: relay({ mode: "dnat" }) });
    await mount(<Rows />);
    await click(buttonIn(rowOf("VLESS · TCP"), "Change way"));
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
    await click(buttonIn(rowOf("Hysteria2"), "Change way"));
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
    expect(rowOf("VLESS · TCP").textContent).toContain("A bridge is not available: update the agent of de1.");
    expect(rowOf("VLESS · TCP").querySelector("a[href='/updates']")).not.toBeNull();
    await click(buttonIn(rowOf("VLESS · TCP"), "Change way"));
    expect(radio("reverse").disabled).toBe(true);
    expect(dialog().textContent).toContain("A bridge is not available: update the agent of de1.");
  });

  it("a refused change says why, in words", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    const { ConnectError, Code } = await import("@connectrpc/connect");
    updateRelay.mockRejectedValue(new ConnectError("reverse_unavailable: reason=entry_agent_old", Code.FailedPrecondition));
    await mount(<Rows />);
    await click(buttonIn(rowOf("VLESS · TCP"), "Change way"));
    await click(radio("reverse"));
    await click(buttonIn(dialog(), "Change"));
    await settle();
    expect(dialog().querySelector("[role=alert]")?.textContent).toBe("A bridge is not possible for this relay: the agent of the entry is too old.");
  });
});

describe("the speed check of the bridge's line", () => {
  it("asks first, says it sends 8 MB and counts toward the daily limit, and starts only on 'Check'", async () => {
    listRelays.mockResolvedValue({ relays: [bridged()] });
    checkRelay.mockResolvedValue({ relay: bridged() });
    await mount(<Rows />);
    await click(buttonIn(rowOf("VLESS · TCP"), "Speed check (8 MB)"));
    expect(dialog().textContent).toContain("Check the speed de1 → msk1?");
    expect(dialog().textContent).toContain("de1 sends 8 MB to msk1.");
    expect(dialog().textContent).toContain("counts toward the daily limit of speed checks on both nodes");
    expect(dialog().textContent).toContain("not the bridge’s");
    expect(checkRelay).not.toHaveBeenCalled(); // a stray click starts nothing
    await click(buttonIn(dialog(), "Cancel"));
    await settle();
    expect(document.querySelector("[role=dialog]")).toBeNull();
    expect(checkRelay).not.toHaveBeenCalled();
    await click(buttonIn(rowOf("VLESS · TCP"), "Speed check (8 MB)"));
    await click(buttonIn(dialog(), "Check"));
    await settle();
    expect(checkRelay).toHaveBeenCalledTimes(1);
    expect(checkRelay).toHaveBeenCalledWith({ id: "rly_1", reverseSpeed: true });
    expect(document.querySelector("[role=dialog]")).toBeNull();
  });

  it("a refusal stays in the window", async () => {
    listRelays.mockResolvedValue({ relays: [bridged()] });
    const { ConnectError, Code } = await import("@connectrpc/connect");
    checkRelay.mockRejectedValue(new ConnectError("check_unavailable", Code.FailedPrecondition));
    await mount(<Rows />);
    await click(buttonIn(rowOf("VLESS · TCP"), "Speed check (8 MB)"));
    await click(buttonIn(dialog(), "Check"));
    await settle();
    expect(dialog().querySelector("[role=alert]")?.textContent).toBe(en["err.check_unavailable"]);
  });

  it("'Check now' asks for the exit's reach too when a bridge is possible, and only the leg for direct only", async () => {
    listRelays.mockResolvedValue({ relays: [relay(), relay({ id: "rly_2", profileName: "Direct only", mode: "dnat" })] });
    checkRelay.mockResolvedValue({ relay: relay() });
    await mount(<Rows />);
    await click(buttonIn(rowOf("VLESS · TCP"), "Check now"));
    await settle();
    expect(checkRelay).toHaveBeenLastCalledWith({ id: "rly_1", reverseReach: true });
    await click(buttonIn(rowOf("Direct only"), "Check now"));
    await settle();
    expect(checkRelay).toHaveBeenLastCalledWith({ id: "rly_2" });
  });
});

describe("a reader of the relays", () => {
  it("sees the state and its words and none of the buttons, with one line saying who changes them", async () => {
    listRelays.mockResolvedValue({ relays: [bridged()] });
    await mount(<Rows readOnly />);
    const row = rowOf("VLESS · TCP");
    expect(row.textContent).toContain("Via bridge");
    expect(row.textContent).toContain("Bridge is up");
    expect(row.textContent).toContain("In use: 120 of ~230");
    expect(row.querySelectorAll("button")).toHaveLength(0);
    expect(document.body.textContent).toContain(en["relay.way.readOnly"]);
    // the second entry is the owner's to add: the advice without its link
    expect(row.querySelector("a[href='/nodes/$id'][data-search*=add]")).toBeNull();
  });

  it("the owner's rows have no such line", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    await mount(<Rows />);
    expect(document.body.textContent).not.toContain(en["relay.way.readOnly"]);
    expect(buttonIn(rowOf("VLESS · TCP"), "Turn off")).toBeDefined();
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
