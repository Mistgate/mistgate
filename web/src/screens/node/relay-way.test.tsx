import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { relaysQuery, type RelayMsg } from "@/lib/relay";
import { useRelayRuns } from "./relays";
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
const editor = () => document.querySelector<HTMLElement>("[data-testid=relay-mode-editor]")!;
const way = (label: string) => buttonIn(editor(), label)!;

describe("the choice of the way, under the server's row", () => {
  it("offers auto, forwarding only and bridge only in the row; a click saves the mode", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    updateRelay.mockResolvedValue({ relay: relay({ mode: "dnat" }) });
    await mount(<Rows />);
    await menuItem(rowOf("VLESS · TCP"), "Method…");
    // no window: the editor opens in the row, with what a change does to people
    expect(document.querySelector("[role=dialog]")).toBeNull();
    expect(editor().textContent).toContain("Open connections of people will drop");
    const names = [...editor().querySelectorAll("[role=group] button")].map((b) => b.textContent);
    expect(names).toEqual(["Auto (recommended)", "Forwarding only", "Bridge only"]);
    expect(way("Auto (recommended)").getAttribute("aria-pressed")).toBe("true");
    expect(way("Bridge only").getAttribute("aria-disabled")).not.toBe("true");
    await click(way("Forwarding only"));
    await settle();
    expect(updateRelay).toHaveBeenCalledWith({ id: "rly_1", mode: "dnat" });
    expect(way("Forwarding only").getAttribute("aria-pressed")).toBe("true");
    await click(buttonIn(editor(), "Done"));
    expect(document.querySelector("[data-testid=relay-mode-editor]")).toBeNull();
  });

  it("a UDP relay cannot be put on the bridge: the choice is greyed with the honest reason, forwarding and auto stay", async () => {
    const udp = relay({ profileName: "Hysteria2", protocol: "hysteria2", network: "udp", reverseBlocked: "udp", modeReason: "udp" });
    listRelays.mockResolvedValue({ relays: [udp] });
    await mount(<Rows />);
    // the reason stays out of the row (it would repeat on every UDP relay), and the speed check is not offered
    expect(rowOf("Hysteria2").textContent).not.toContain("A bridge works only for TCP");
    expect(buttonIn(rowOf("Hysteria2"), "Speed check (8 MB)")).toBeUndefined();
    await menuItem(rowOf("Hysteria2"), "Method…");
    expect(way("Bridge only").getAttribute("aria-disabled")).toBe("true");
    expect(way("Auto (recommended)").getAttribute("aria-disabled")).not.toBe("true");
    expect(editor().textContent).toContain("A bridge works only for TCP for now: VLESS and MTProto. Hysteria2 and AmneziaWG stay direct.");
    expect(editor().textContent).not.toContain("DNAT");
    // a click on the greyed one asks the panel for nothing
    await click(way("Bridge only"));
    expect(updateRelay).not.toHaveBeenCalled();
    expect(way("Auto (recommended)").getAttribute("aria-pressed")).toBe("true");
  });

  it("an agent that is too old names the node to update", async () => {
    listRelays.mockResolvedValue({ relays: [relay({ reverseBlocked: "exit_agent_old", modeReason: "reverse_unsupported" })] });
    await mount(<Rows />);
    await menuItem(rowOf("VLESS · TCP"), "Method…");
    expect(way("Bridge only").getAttribute("aria-disabled")).toBe("true");
    expect(editor().textContent).toContain("A bridge is not available: update the agent of de1.");
  });

  it("a refused change says why, in words, and keeps the choice where it was", async () => {
    listRelays.mockResolvedValue({ relays: [relay()] });
    const { ConnectError, Code } = await import("@connectrpc/connect");
    updateRelay.mockRejectedValue(new ConnectError("reverse_unavailable: reason=entry_agent_old", Code.FailedPrecondition));
    await mount(<Rows />);
    await menuItem(rowOf("VLESS · TCP"), "Method…");
    await click(way("Bridge only"));
    await settle();
    expect(editor().querySelector("[role=alert]")?.textContent).toBe("A bridge is not possible for this relay: the agent of the entry is too old.");
    expect(way("Auto (recommended)").getAttribute("aria-pressed")).toBe("true");
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
