import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { en } from "@/i18n/en";
import { relaysQuery, type RelayMsg } from "@/lib/relay";
import { AddRelayDialog, RelayRows, useRelayRuns } from "./relays";

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
  nodes: { listNodes: () => Promise.resolve({ nodes: [{ id: "nod_de1", countryCode: "DE" }, { id: "nod_fi1", countryCode: "FI" }, { id: "nod_msk1", countryCode: "RU" }] }) },
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
const down = relay({ id: "rly_2", inboundId: "inb_h", profileName: "Hysteria2", protocol: "hysteria2", network: "udp", port: 8444, exitPort: 8444, check: check({ network: "udp", ok: false, stage: "path", path: stage({ status: "fail", code: "timeout", verdict: "" }) }) });
const fresh = relay({ id: "rly_3", inboundId: "inb_f", exitNodeId: "nod_fi1", exitNodeName: "fi1", profileName: "VLESS · Reality", check: check() });
const unchecked = relay({ id: "rly_4", inboundId: "inb_u", exitNodeId: "nod_fi1", exitNodeName: "fi1", profileName: "Trojan" });
const stale = relay({ id: "rly_5", inboundId: "inb_s", exitNodeId: "nod_fi1", exitNodeName: "fi1", profileName: "Old", check: check({ atUnix: now - 30 * 60, leg: stage({ atUnix: now - 30 * 60 }) }) });

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

/** The relays of a node as a lane of the ways in shows them: the rows and what runs on them. */
function Rows({ node = "nod_msk1", side = "entry" }: { node?: string; side?: "entry" | "exit" }) {
  const q = useQuery(relaysQuery(node));
  const runs = useRelayRuns();
  return <RelayRows list={(q.data ?? []) as RelayMsg[]} side={side} runs={runs} />;
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
const text = () => document.body.textContent ?? "";
const rows = () => [...document.querySelectorAll<HTMLElement>("[data-testid=relay-row]")];
const rowOf = (name: string) => rows().find((r) => r.textContent?.includes(name))!;
const buttonIn = (el: ParentNode, label: string) => [...el.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]")!;
const tick = (label: string) => click(dialog().querySelector(`input[aria-label="${label}"]`));

describe("the relay rows of a lane", () => {
  it("lists relays that work, are down and are off, each with its state, checks and why", async () => {
    listRelays.mockResolvedValue({ relays: [on, down, fresh, unchecked, stale] });
    await mount(<Rows />);
    expect(rows().map((r) => r.dataset.state)).toEqual(["on", "failed", "off", "off", "off"]);

    const works = rowOf("VLESS · TCP");
    expect(works.textContent).toContain("Works");
    expect(works.textContent).toContain("tcp/8443");
    expect(works.textContent).toContain("203.0.113.7:443");
    expect(works.querySelector("[data-testid=relay-stages]")?.textContent).toBe("msk1 → de1passesFrom nl1 through msk1passes");
    expect(works.textContent).toContain("Works, checked 2 min ago.");
    expect(buttonIn(works, "Turn off")).toBeDefined();

    const broken = rowOf("Hysteria2");
    expect(broken.textContent).toContain("Down");
    expect(broken.querySelector("[data-testid=relay-down]")?.textContent).toMatch(/^Down since .+: 8444\/udp on msk1 is not reachable from outside\.$/);
  });

  it("offers 'Turn on' only after a check that passed in the last 15 minutes, and says why not otherwise", async () => {
    listRelays.mockResolvedValue({ relays: [fresh, unchecked, stale] });
    await mount(<Rows />);
    const ready = buttonIn(rowOf("VLESS · Reality"), "Turn on")!;
    expect(ready.disabled).toBe(false);
    expect(rowOf("VLESS · Reality").textContent).toContain("The check passed 2 min ago: it can be turned on now.");
    const never = buttonIn(rowOf("Trojan"), "Turn on")!;
    expect(never.disabled).toBe(true);
    expect(never.parentElement?.title).toBe(en["relay.gate.unchecked"]);
    expect(buttonIn(rowOf("Trojan"), "Check now")?.className).toContain("bg-accent"); // the next step is to check
    expect(buttonIn(rowOf("Old"), "Turn on")?.parentElement?.title).toBe(en["relay.gate.stale"]);

    updateRelay.mockResolvedValue({ relay: { ...fresh, enabled: true, state: InboundState.ACTIVE } });
    await click(ready);
    await settle();
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_3", enabled: true });
    expect(text()).toContain("The relay works. People get the server “via RU” with their next subscription update (every 12 h).");
  });

  it("checks now: the row says what it checks while it runs, and a refused switch leaves its reason, not a second one", async () => {
    listRelays.mockResolvedValue({ relays: [on] });
    let finish: (v: unknown) => void = () => {};
    checkRelay.mockReturnValue(new Promise((r) => (finish = r)));
    await mount(<Rows />);
    await click(buttonIn(rowOf("VLESS · TCP"), "Check now"));
    expect(rowOf("VLESS · TCP").dataset.state).toBe("checking");
    expect(rowOf("VLESS · TCP").textContent).toContain("Checking msk1 → de1…");
    expect(rowOf("VLESS · TCP").querySelector("[data-testid=relay-stages]")?.textContent).toBe("msk1 → de1checking…From outside through msk1next");
    await act(async () => finish({ relay: on }));
    await settle();
    expect(checkRelay).toHaveBeenCalledWith({ id: "rly_1" });
    expect(rowOf("VLESS · TCP").dataset.state).toBe("on");
    expect(listRelays.mock.calls.length).toBeGreaterThan(1); // the list is read again
  });

  it("switching on that fails its outside check reads from the stored check; any other refusal is said under the row", async () => {
    listRelays.mockResolvedValue({ relays: [fresh] });
    await mount(<Rows />);
    let finish: (e: unknown) => void = () => {};
    updateRelay.mockReturnValue(new Promise((_, j) => (finish = j)));
    await click(buttonIn(rowOf("VLESS · Reality"), "Turn on"));
    expect(rowOf("VLESS · Reality").textContent).toContain("Checking from outside through msk1…");
    listRelays.mockResolvedValue({ relays: [{ ...fresh, check: check({ ok: false, stage: "path", path: stage({ status: "fail", code: "timeout" }) }) }] });
    await act(async () => finish(new ConnectError("relay_check_failed: code=timeout&stage=path", Code.FailedPrecondition)));
    await settle();
    const row = rowOf("VLESS · Reality");
    expect(row.dataset.state).toBe("failed");
    expect(row.querySelector("[data-testid=relay-down]")?.textContent).toContain("8443/tcp on msk1 is not reachable from outside");
    expect(row.querySelector("[role=alert]")).toBeNull(); // no second sentence

    listRelays.mockResolvedValue({ relays: [fresh] });
    updateRelay.mockRejectedValue(new ConnectError("relay_check_stale", Code.FailedPrecondition));
    await settle();
    await click(buttonIn(rowOf("VLESS · Reality"), "Turn on"));
    await settle();
    expect(rowOf("VLESS · Reality").querySelector("[role=alert]")?.textContent).toBe(en["err.relay_check_stale"]);
  });

  it("asks before removing, and says who loses what", async () => {
    listRelays.mockResolvedValue({ relays: [on] });
    await mount(<Rows />);
    await click(buttonIn(rowOf("VLESS · TCP"), "Remove"));
    expect(dialog().textContent).toContain("Remove the relay msk1 → de1?");
    expect(dialog().textContent).toContain("VLESS · TCP on de1 is no longer reachable through msk1.");
    await click(buttonIn(dialog(), "Remove"));
    await settle();
    expect(deleteRelay).toHaveBeenCalledWith({ id: "rly_1" });
  });

});

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

describe("the exit's side and the panel's watch", () => {
  it("on the exit a row leads with the profile and names the port on the entry", async () => {
    listRelays.mockResolvedValue({ relays: [on] });
    await mount(<Rows node="nod_de1" side="exit" />);
    const row = rowOf("VLESS · TCP");
    expect(row.textContent).toContain("Port on msk1");
    expect(row.textContent).not.toContain("de1 · ");
    expect(row.querySelector("a")?.getAttribute("href")).toBe("/profiles/$id");
  });

  it("a relay that is on says how the last round through it went, and what went through the forward", async () => {
    const watched = relay({ ...on, monitor: { atUnix: now - 240, ok: true, stage: "", code: "", responseMs: 182, failStreak: 0 }, bytes: 1_200_000_000, packets: 18_400, countedUnix: now - 180 });
    listRelays.mockResolvedValue({ relays: [watched] });
    await mount(<Rows />);
    const watch = rowOf("VLESS · TCP").querySelector("[data-testid=relay-watch]")!;
    expect(watch.textContent).toContain("Through the relay: works, 182 ms, 4 min ago");
    expect(watch.textContent).toContain("Through the forward: 1.2 GB, 18,400 packets, counted 3 min ago");
    expect(rowOf("VLESS · TCP").textContent).not.toContain("Works, checked"); // the round says it
  });

  it("two failed rounds make it down, with the stage in words; an agent without counters says nothing of bytes", async () => {
    const frozen = relay({ ...on, monitor: { atUnix: now - 60, ok: false, stage: "freeze", code: "", responseMs: 0, failStreak: 3 } });
    const once = relay({ ...on, id: "rly_8", profileName: "Once", monitor: { atUnix: now - 60, ok: false, stage: "path", code: "timeout", responseMs: 0, failStreak: 1 } });
    listRelays.mockResolvedValue({ relays: [frozen, once] });
    await mount(<Rows />);
    const row = rowOf("VLESS · TCP");
    expect(row.dataset.state).toBe("failed");
    expect(row.textContent).toContain("Through the relay: the transfer stalls after the first kilobytes, 1 min ago · 3 rounds in a row");
    expect(row.textContent).not.toContain("Through the forward");
    expect(rowOf("Once").dataset.state).toBe("on"); // one bad round is not an outage yet
    expect(rowOf("Once").textContent).toContain("nothing comes through msk1 from outside");
  });
});
