import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { relaysQuery, type RelayMsg } from "@/lib/relay";
import { Code, ConnectError } from "@connectrpc/connect";
import { en } from "@/i18n/en";
import { PairWay } from "./relay-pair";
import { RelayRows } from "./relay-rows";
import { useRelayRuns } from "./relays";

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

/** The way and the measurements of the pair of the relays on the list. */
function Pair({ readOnly, onOther }: { readOnly?: boolean; onOther?: () => void }) {
  const q = useQuery(relaysQuery("nod_msk1"));
  const runs = useRelayRuns();
  const rs = (q.data ?? []) as RelayMsg[];
  if (rs.length === 0) return null;
  return <PairWay rs={rs} entry="msk1" exit="de1" cc="RU" runs={runs} readOnly={readOnly} nowS={now} onOther={onOther} />;
}
const text = () => document.body.textContent ?? "";
const section = (id: string) => document.querySelector<HTMLElement>(`[data-testid=${id}]`)!;

describe("the servers of a pair", () => {
  it("lists relays that work, are down and are off, each with its state, its port on the entry and why", async () => {
    const down = relay({ id: "rly_2", inboundId: "inb_h", profileName: "Hysteria2", protocol: "hysteria2", network: "udp", port: 8444, exitPort: 8444, check: check({ network: "udp", ok: false, stage: "path", path: stage({ status: "fail", code: "timeout", verdict: "" }) }) });
    const off = relay({ id: "rly_3", inboundId: "inb_f", profileName: "VLESS · Reality", enabled: false, state: InboundState.PENDING, check: check() });
    listRelays.mockResolvedValue({ relays: [relay(), down, off] });
    await mount(<Rows />);
    expect(rows().map((r) => r.dataset.state)).toEqual(["on", "failed", "off"]);
    const works = rowOf("VLESS · TCP");
    expect(works.textContent).toContain("Works");
    expect(works.textContent).toContain("tcp/8443 on msk1");
    expect(rowOf("Hysteria2").textContent).toContain("Down");
    expect(rowOf("Hysteria2").querySelector("[data-testid=relay-down]")?.textContent).toMatch(/Down since .+: 8444\/udp on msk1 is not reachable from outside\./);
    expect(rowOf("VLESS · Reality").textContent).toContain("The check passed 2 min ago: it can be turned on now.");
  });

  it("a click on the name opens the line from people through the entry to the exit, with what went through the forward", async () => {
    listRelays.mockResolvedValue({ relays: [relay({ monitor: { atUnix: now - 240, ok: true, stage: "", code: "", responseMs: 182, failStreak: 0 }, bytes: 1_200_000_000, packets: 18_400, countedUnix: now - 180 })] });
    await mount(<Rows />);
    expect(rowOf("VLESS · TCP").textContent).toContain("As a client: 182 ms · 4 min ago");
    expect(document.querySelector("[data-testid=relay-chain]")).toBeNull();
    await click(rowOf("VLESS · TCP").querySelector("button[aria-expanded]"));
    const chain = document.querySelector("[data-testid=relay-chain]")!;
    expect(chain.textContent).toContain("msk1");
    expect(chain.textContent).toContain("203.0.113.7:443");
    expect(chain.textContent).toContain("passes");
    expect(rowOf("VLESS · TCP").textContent).toContain("Through the forward: 1.2 GB, 18,400 packets, counted 3 min ago");
  });

  it("two failed rounds make the server down", async () => {
    const frozen = relay({ monitor: { atUnix: now - 60, ok: false, stage: "freeze", code: "", responseMs: 0, failStreak: 3 } });
    listRelays.mockResolvedValue({ relays: [frozen] });
    await mount(<Rows />);
    expect(rowOf("VLESS · TCP").dataset.state).toBe("failed");
  });

  it("the menu checks now, and the row says what it checks while it runs", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    let finish: (v: unknown) => void = () => {};
    checkRelay.mockReturnValue(new Promise((r) => (finish = r)));
    await mount(<Rows />);
    await menuItem(rowOf("VLESS · TCP"), "Check now");
    expect(rowOf("VLESS · TCP").dataset.state).toBe("checking");
    expect(rowOf("VLESS · TCP").textContent).toContain("Checking msk1 → de1…");
    await act(async () => finish({ relay: relay() }));
    await settle();
    expect(checkRelay).toHaveBeenCalledWith({ id: "rly_1", reverseReach: true });
    expect(rowOf("VLESS · TCP").dataset.state).toBe("on");
    expect(listRelays.mock.calls.length).toBeGreaterThan(1);
  });

  it("turns on only after a check that passed in the last 15 minutes, and says why not otherwise", async () => {
    const fresh = relay({ id: "rly_3", enabled: false, state: InboundState.PENDING, check: check() });
    const never = relay({ id: "rly_4", profileName: "Trojan", enabled: false, state: InboundState.PENDING, check: undefined });
    listRelays.mockResolvedValue({ relays: [fresh, never] });
    await mount(<Rows />);
    await click(rowOf("VLESS · TCP").querySelector("button[aria-haspopup=menu]"));
    const turnOn = [...document.querySelectorAll<HTMLButtonElement>("[role=menuitem]")].find((b) => b.textContent === "Turn on")!;
    expect(turnOn.disabled).toBe(false);
    await click(document.querySelector(".wy-mnb"));
    await click(rowOf("Trojan").querySelector("button[aria-haspopup=menu]"));
    const locked = [...document.querySelectorAll<HTMLButtonElement>("[role=menuitem]")].find((b) => b.textContent === "Turn on")!;
    expect(locked.disabled).toBe(true);
    expect(locked.title).toBe(en["relay.gate.unchecked"]);
  });

  it("switching on that fails its outside check reads from the stored check", async () => {
    const fresh = relay({ enabled: false, state: InboundState.PENDING, check: check() });
    listRelays.mockResolvedValue({ relays: [fresh] });
    await mount(<Rows />);
    let finish: (e: unknown) => void = () => {};
    updateRelay.mockReturnValue(new Promise((_, j) => (finish = j)));
    await menuItem(rowOf("VLESS · TCP"), "Turn on");
    expect(rowOf("VLESS · TCP").textContent).toContain("Checking from outside through msk1…");
    listRelays.mockResolvedValue({ relays: [{ ...fresh, check: check({ ok: false, stage: "path", path: stage({ status: "fail", code: "timeout" }) }) }] });
    await act(async () => finish(new ConnectError("relay_check_failed: code=timeout&stage=path", Code.FailedPrecondition)));
    await settle();
    expect(rowOf("VLESS · TCP").dataset.state).toBe("failed");
    expect(rowOf("VLESS · TCP").querySelector("[data-testid=relay-down]")?.textContent).toContain("8443/tcp on msk1 is not reachable from outside");
    expect(rowOf("VLESS · TCP").querySelector("[role=alert]")).toBeNull();
  });

  it("the menu asks “Really remove?” in itself, with no window; the second click removes, closing the menu takes the question back", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    await mount(<Rows />);
    await menuItem(rowOf("VLESS · TCP"), "Remove…");
    expect(document.querySelector("[role=dialog]")).toBeNull();
    const ask = [...document.querySelectorAll<HTMLElement>("[role=menuitem]")].find((b) => b.textContent === "Really remove?")!;
    expect(ask.className).toBe("ask");
    expect(deleteRelay).not.toHaveBeenCalled();
    await click(ask);
    await settle();
    expect(deleteRelay).toHaveBeenCalledWith({ id: "rly_1" });
    expect(document.querySelector("[role=menu]")).toBeNull();
    // the question does not outlive the menu: asked, the menu closed on its scrim, opened again
    await menuItem(rowOf("VLESS · TCP"), "Remove…");
    await click(document.querySelector(".wy-mnb"));
    await click(rowOf("VLESS · TCP").querySelector("button[aria-haspopup=menu]"));
    expect([...document.querySelectorAll<HTMLElement>("[role=menuitem]")].some((b) => b.textContent === "Really remove?")).toBe(false);
  });

  it("a failed server dropping forwarded traffic points to the Doctor of the entry", async () => {
    listRelays.mockResolvedValue({ relays: [relay({ state: InboundState.FAILED, lastError: "forward_drop" })] });
    await mount(<Rows />);
    expect(rowOf("VLESS · TCP").querySelector("[data-testid=relay-down]")?.textContent).toContain("Allow forwarding");
    expect(rowOf("VLESS · TCP").querySelector("[data-testid=relay-down] a")?.getAttribute("data-search")).toBe('{"tab":"doctor"}');
  });

  it("a reader sees the state and one line saying who changes it, and no menu", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    await mount(<Rows readOnly />);
    expect(text()).toContain("Only the owner changes how a relay works.");
    expect(document.querySelector("button[aria-haspopup=menu]")).toBeNull();
    expect(rowOf("VLESS · TCP").textContent).toContain("Works");
  });
});

describe("the way and the measurements of a pair", () => {
  it("forwarding in auto: the chip, who connects to whom, the check that switches nothing, the measurements", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    await mount(<Pair />);
    const way = section("pair-way");
    expect(way.textContent).toContain("Forwarding");
    expect(way.textContent).toContain("auto");
    expect(way.textContent).toContain("msk1 connects to de1 itself");
    expect(buttonIn(way, "Check only")).toBeDefined();
    expect(way.querySelector(".wy-occ")).toBeNull();
    expect(section("pair-checks").textContent).toContain("msk1 → de1: passes");
  });

  it("the bridge: why, how it holds, how full it is; at the limit what people get and a second entry", async () => {
    const other = vi.fn();
    listRelays.mockResolvedValue({ relays: [bridged({ bridge: bridge({ clients: 468, capacity: 470, nearFull: true, pool: 4, error: "stream_limit" }) })] });
    await mount(<Pair onOther={other} />);
    const way = section("pair-way");
    expect(way.textContent).toContain("Bridge");
    expect(way.textContent).toContain("de1 connects to msk1 itself and keeps the line open");
    expect(way.textContent).toContain("msk1 does not reach de1 directly");
    expect(way.querySelector(".wy-occ.lim")?.textContent).toContain("468 of ~470");
    expect(way.querySelectorAll(".wy-sg u.on")).toHaveLength(10);
    expect(way.querySelector(".wy-plt")?.textContent).toContain("New people are refused over the bridge");
    await click(buttonIn(way, "Another entry for de1…"));
    expect(other).toHaveBeenCalled();
  });

  it("a bridge that is not held says why in words, with the code and a button to check", async () => {
    listRelays.mockResolvedValue({ relays: [bridged({ bridge: bridge({ connected: false, error: "connect" }), check: check({ ok: false, stage: "bridge", leg: stage({ status: "fail", code: "timeout" }), reverseReach: stage({ port: 8443 }), bridge: stage({ status: "fail", code: "connect" }) }) })] });
    await mount(<Pair />);
    const note = section("pair-way").querySelector<HTMLElement>(".wy-plt.crit")!;
    expect(note.textContent).toContain("The bridge is down");
    expect(buttonIn(note, "Check de1 → msk1")).toBeDefined();
  });

  it("an agent that is too old names the node to update, with the way to Updates", async () => {
    listRelays.mockResolvedValue({ relays: [relay({ reverseBlocked: "exit_agent_old", modeReason: "reverse_unsupported" })] });
    await mount(<Pair />);
    expect(section("pair-way").textContent).toContain("A bridge is not available: update the agent of de1.");
    expect(section("pair-way").querySelector("a[href='/updates']")).not.toBeNull();
  });

  it("the choice of the way sends the mode to every relay of the pair that can take it", async () => {
    updateRelay.mockResolvedValue({ relay: relay() });
    listRelays.mockResolvedValue({ relays: [relay(), relay({ id: "rly_2", inboundId: "inb_h", profileName: "Hysteria2", protocol: "hysteria2", network: "udp", reverseBlocked: "udp", modeReason: "udp" })] });
    await mount(<Pair />);
    await click(buttonIn(section("pair-way"), "Method: auto"));
    await click(buttonIn(section("pair-way"), "Bridge only"));
    await settle();
    expect(updateRelay).toHaveBeenCalledTimes(1);
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_1", mode: "reverse" });
  });

  it("the speed check asks nothing, runs, says what it did and what it does not prove", async () => {
    listRelays.mockResolvedValue({ relays: [bridged()] });
    checkRelay.mockResolvedValue({ relay: bridged() });
    await mount(<Pair />);
    expect(section("pair-checks").textContent).toContain("de1 sends 8 MB to msk1. Catches a link that cuts a stream after the first kilobytes");
    await click(buttonIn(section("pair-checks"), "Check by speed (8 MB)"));
    await settle();
    expect(checkRelay).toHaveBeenCalledWith({ id: "rly_1", reverseSpeed: true });
    expect(section("pair-checks").textContent).toContain("The measurement goes to a separate port on msk1");
  });

  it("a reader sees the way and the measurements without a button", async () => {
    listRelays.mockResolvedValue({ relays: [bridged()] });
    await mount(<Pair readOnly />);
    expect(section("pair-way").textContent).toContain("Bridge");
    expect(document.querySelectorAll("button")).toHaveLength(0);
  });
});

describe("the bridge's problem, by what the agent said", () => {
  const down = (code: string) => bridged({ bridge: bridge({ connected: false, error: code }), check: check({ ok: false, stage: "bridge", leg: stage({ status: "fail", code: "timeout" }), reverseReach: stage({ port: 8443 }), bridge: stage({ status: "fail", code }) }) });
  // code -> tone class, the button or the link, where the link goes
  const table: [string, "crit" | "mut" | "warn", string | null, string?][] = [
    ["connect", "crit", "Check de1 → msk1"],
    ["pin_mismatch", "crit", "Check again"],
    ["tls_protocol", "crit", "Open the profiles of msk1", "nod_msk1:profiles"],
    ["pair_timeout", "crit", "Check the speed"],
    ["target_connect", "crit", "Open the profiles of de1", "nod_de1:profiles"],
    ["identity_unavailable", "crit", "Open “Server”", "nod_de1:server"],
    ["site_listen_failed", "crit", "Open the Doctor of msk1", "nod_msk1:doctor"],
    ["bad_config", "crit", "Check again"],
  ];
  it.each(table)("%s: a %s line with %s", async (code, tone, action, to) => {
    listRelays.mockResolvedValue({ relays: [down(code)] });
    await mount(<Pair />);
    const note = section("pair-way").querySelector<HTMLElement>(".wy-plt")!;
    expect(note.classList.contains("crit")).toBe(tone === "crit");
    expect(note.querySelector("small")?.textContent).toBe(code);
    const target = [...note.querySelectorAll("button, a")].find((x) => x.textContent === action);
    expect(target).toBeDefined();
    if (to) {
      const [id, tab] = to.split(":");
      expect(JSON.parse(target!.getAttribute("data-params")!)).toEqual({ id });
      expect(JSON.parse(target!.getAttribute("data-search")!)).toEqual({ tab });
    }
  });

  it("a word that is no failure (a pin to wait for, a limit of connections, a restart) is a quiet line without a button, even when the bridge is held", async () => {
    for (const [code, mut, said] of [["no_verification", true, "Passes by itself"] as const, ["connection_limit", false, "Passes by itself within minutes"] as const, ["closed", true, "The bridge is restarting"] as const]) {
      listRelays.mockResolvedValue({ relays: [bridged({ bridge: bridge({ error: code }) })] });
      await mount(<Pair />);
      const note = section("pair-way").querySelector<HTMLElement>(".wy-plt")!;
      expect(note.classList.contains("mut"), code).toBe(mut);
      expect(note.classList.contains("crit"), code).toBe(false);
      expect(note.textContent?.toLowerCase(), code).toContain(said.toLowerCase());
      expect(note.querySelector("button, a"), code).toBeNull();
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("when the panel could not read the relays the block keeps one line and a way to ask again", async () => {
    const retry = vi.fn();
    listRelays.mockResolvedValue({ relays: [bridged()] });
    await mount(<ReadFailed onRetry={retry} />);
    const way = section("pair-way");
    expect(way.querySelectorAll(".wy-plt")).toHaveLength(1);
    expect(way.textContent).toContain("Could not read the state of the bridge");
    expect(way.querySelector(".wy-kv")).toBeNull();
    await click(buttonIn(way, "Try again"));
    expect(retry).toHaveBeenCalled();
  });
});

function ReadFailed({ onRetry }: { onRetry: () => void }) {
  const q = useQuery(relaysQuery("nod_msk1"));
  const runs = useRelayRuns();
  const rs = (q.data ?? []) as RelayMsg[];
  if (rs.length === 0) return null;
  return <PairWay rs={rs} entry="msk1" exit="de1" cc="RU" runs={runs} nowS={now} readError onRetry={onRetry} />;
}

describe("the editors under a server's row", () => {
  const type = async (input: HTMLInputElement, value: string) => {
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };
  const editor = () => document.querySelector<HTMLElement>("[data-testid=relay-port-editor]")!;

  it("Change port… opens a field in the row, not a window, with what it does to people", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    await mount(<Rows />);
    await menuItem(rowOf("VLESS · TCP"), "Change port…");
    expect(document.querySelector("[role=dialog]")).toBeNull();
    expect(editor().textContent).toContain("Port on msk1");
    expect(editor().textContent).toContain("People get the new port with the next subscription update");
    expect(editor().querySelector<HTMLInputElement>("input")!.value).toBe("8443");
    // the port it has is no change
    expect(buttonIn(editor(), "Change")?.disabled).toBe(true);
    // typing the port it has says it is the current one
    await type(editor().querySelector<HTMLInputElement>("input")!, "844");
    await type(editor().querySelector<HTMLInputElement>("input")!, "8443");
    expect(editor().textContent).toContain("This is the current port");
  });

  it("a number that is not a port says so in the hint's place, and a good one is sent", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    updateRelay.mockResolvedValue({ relay: relay({ port: 8444 }) });
    await mount(<Rows />);
    await menuItem(rowOf("VLESS · TCP"), "Change port…");
    const input = () => editor().querySelector<HTMLInputElement>("input")!;
    await type(input(), "70000");
    expect(editor().textContent).toContain("A number from 10 to 65535");
    expect(editor().textContent).not.toContain("People get the new port");
    expect(buttonIn(editor(), "Change")?.disabled).toBe(true);
    await type(input(), "8444");
    expect(buttonIn(editor(), "Change")?.disabled).toBe(false);
    await click(buttonIn(editor(), "Change"));
    await settle();
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_1", port: 8444 });
    expect(document.querySelector("[data-testid=relay-port-editor]")).toBeNull();
  });

  it("a port the panel refuses says why, in words, and the field stays", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    updateRelay.mockRejectedValue(new ConnectError("port_taken: free=8445", Code.AlreadyExists));
    await mount(<Rows />);
    await menuItem(rowOf("VLESS · TCP"), "Change port…");
    await type(editor().querySelector<HTMLInputElement>("input")!, "8444");
    await click(buttonIn(editor(), "Change"));
    await settle();
    expect(editor().querySelector("[role=alert]")).not.toBeNull();
    await click(buttonIn(editor(), "Cancel"));
    expect(document.querySelector("[data-testid=relay-port-editor]")).toBeNull();
  });
});

describe("a relay that was just made", () => {
  it("grows into its row when the effects are on, and appears as any row without them", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    await mount(<Rows />);
    expect(rowOf("VLESS · TCP").classList.contains("wy-new")).toBe(false);
    act(() => root?.unmount());
    host?.remove();
    const { setEffectsEnabled } = await import("@/lib/effects");
    const { markFresh, resetFresh } = await import("@/components/fx/registry");
    setEffectsEnabled(true);
    try {
      markFresh("relay", "rly_1");
      await mount(<Rows />);
      expect(rowOf("VLESS · TCP").classList.contains("wy-new")).toBe(true);
    } finally {
      resetFresh();
      setEffectsEnabled(false);
    }
  });
});
