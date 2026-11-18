import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { en } from "@/i18n/en";
import { en as cm } from "@/i18n/channel";
import { setEffectsEnabled } from "@/lib/effects";
import { ServerTab } from "./server";
import type { ServerSection } from "./tabs";

// Node → Server → Channel: "Measure the channel". The window says what it will do before anything runs, the run is the panel's (the
// screen only starts, reads and stops it), the result offers a number, and the number is written only by pressing "Set".

const plan = vi.fn();
const start = vi.fn();
const status = vi.fn();
const cancel = vi.fn();
const updateNode = vi.fn();
const me = vi.fn();
vi.mock("@/lib/api", () => ({
  nodes: { retireNode: vi.fn(), measureBandwidth: vi.fn(), updateNode: (...a: unknown[]) => updateNode(...a) },
  probe: {
    channelMeasurePlan: (...a: unknown[]) => plan(...a),
    channelMeasureStart: (...a: unknown[]) => start(...a),
    channelMeasureStatus: (...a: unknown[]) => status(...a),
    channelMeasureCancel: (...a: unknown[]) => cancel(...a),
  },
  auth: { me: (...a: unknown[]) => me(...a) },
}));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn(), Link: ({ children }: { children?: unknown }) => children }));
vi.mock("@/lib/updates", async (orig) => ({ ...(await orig<object>()), useOlderNodes: () => new Set<string>() }));
vi.mock("@/components/add-node", () => ({ useAddNode: () => vi.fn() }));
vi.mock("./awg-backend", () => ({ AwgBackendCard: () => null }));
vi.mock("./decoy", () => ({ DecoyCard: () => null }));
vi.mock("./ssh-access", () => ({ SSHAccessCard: () => null, useServerAccess: () => ({ data: null }) }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  setEffectsEnabled(false);
  me.mockResolvedValue({ admin: { id: "adm_1", role: 1 }, edition: "vps" });
  plan.mockResolvedValue(planOk());
  status.mockResolvedValue(idle());
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [plan, start, status, cancel, updateNode, me]) m.mockReset();
});

const planOk = (over: Record<string, unknown> = {}) => ({
  nodeName: "de1",
  error: "",
  partners: [
    { name: "nl1", knownMbps: 2000 },
    { name: "se1", knownMbps: 0 },
  ],
  configuredMbps: 250,
  maxMb: 2048,
  maxGb: 8,
  estSeconds: 48,
  retryAfterS: 0,
  last: undefined,
  ...over,
});
const idle = (over: Record<string, unknown> = {}) => ({ nodeName: "de1", state: "idle", error: "", retryAfterS: 0, startedUnix: 0n, steps: [], result: undefined, last: undefined, configuredMbps: 250, ...over });
const step = (partner: string, direction: string, state: string, mbitS = 0, over: Record<string, unknown> = {}) => ({ partner, direction, state, mbitS, error: "", streams: 8, capped: false, ...over });
const dir = (mbitS: number, partner: string, capped = false) => ({ mbitS, partner, streams: 8, capped });
const nowSec = () => BigInt(Math.floor(Date.now() / 1000));
const running = () =>
  idle({ state: "running", startedUnix: nowSec(), steps: [step("nl1", "down", "done", 812.4), step("nl1", "up", "running"), step("se1", "down", "waiting"), step("se1", "up", "waiting")] });
const done = (down = 812.4, up = 455.1) =>
  idle({
    state: "done",
    startedUnix: nowSec(),
    steps: [step("nl1", "down", "done", down), step("nl1", "up", "done", up)],
    result: { atUnix: nowSec(), down: dir(down, "nl1"), up: dir(up, "nl1"), partners: 2 },
    last: { atUnix: nowSec(), down: dir(down, "nl1"), up: dir(up, "nl1"), partners: 2 },
  });

const data = (nodeStatus = NodeStatus.ONLINE, node: Record<string, unknown> = {}) =>
  ({
    node: { id: "nod_1", name: "de1", address: "de1.example.com", countryCode: "DE", status: nodeStatus, location: "", publicLabel: "", provider: "", bandwidthMbps: 250, ...node },
    notes: "",
    dnsResolvers: [],
    inbounds: [],
    facts: { hostname: "de1", os: "Debian 13", kernel: "6.12", arch: "amd64", cpuCount: 2, ramTotalBytes: 0, diskTotalBytes: 0, virt: "kvm", hasIpv6: true, bootUnix: 0, engines: [] },
  }) as never;

function Harness({ st, node }: { st?: NodeStatus; node?: Record<string, unknown> }) {
  const [section, setSection] = useState<ServerSection>("ch");
  return <ServerTab data={data(st, node)} section={section} onSection={setSection} />;
}
async function mount(st?: NodeStatus, node?: Record<string, unknown>) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <Harness st={st} node={node} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]:not([aria-modal=false])");
const inDialog = (label: string) => [...(dialog()?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === label);
const disabled = (b?: Element | null) => !!b && (b.hasAttribute("disabled") || b.getAttribute("data-disabled") !== null);
const click = async (el: Element | null | undefined) => {
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 3; i++) await settle();
};
async function type(el: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const openWindow = async () => click(button(cm["cm.open"]));

describe("the button", () => {
  it("is the owner's, next to the old one, and opens a window that has asked for the plan and started nothing", async () => {
    await mount();
    expect(button(en["node.settings.bandwidthMeasure"])).toBeDefined();
    expect(button(cm["cm.open"])).toBeDefined();
    await openWindow();
    expect(plan).toHaveBeenCalledWith({ nodeId: "nod_1" });
    expect(start).not.toHaveBeenCalled();
    expect(dialog()).not.toBeNull();
  });

  it("is not there for a helper, for the Cloudflare edition, or for a retired node", async () => {
    me.mockResolvedValue({ admin: { id: "adm_2", role: 2 }, edition: "vps" });
    await mount();
    expect(button(cm["cm.open"])).toBeUndefined();
    expect(plan).not.toHaveBeenCalled();
    expect(status).not.toHaveBeenCalled();
    act(() => root?.unmount());
    host?.remove();

    me.mockResolvedValue({ admin: { id: "adm_1", role: 1 }, edition: "edge" });
    await mount();
    expect(button(cm["cm.open"])).toBeUndefined();
    expect(status).not.toHaveBeenCalled();
    act(() => root?.unmount());
    host?.remove();

    me.mockResolvedValue({ admin: { id: "adm_1", role: 1 }, edition: "vps" });
    await mount(NodeStatus.RETIRED);
    expect(button(cm["cm.open"])).toBeUndefined();
  });
});

describe("the window before the run", () => {
  it("says what it will do: who it measures against, how much data, how long, who is slower, what is paid", async () => {
    await mount();
    await openWindow();
    const t = dialog()!.textContent!;
    expect(t).toContain("Measure the channel of de1");
    expect(t).toContain("nl1, 2000 Mbps known");
    expect(t).toContain("se1, channel not known");
    expect(t).toContain("up to 8 GB");
    expect(t).toContain("about 48 s");
    expect(t).toContain("people on these nodes will be slower");
    expect(t).toContain("paid traffic");
    expect(disabled(inDialog(cm["cm.start"]))).toBe(false);
    expect(start).not.toHaveBeenCalled();
  });

  it("refuses with the reason when there is no partner, the agent is old, or the node was measured a moment ago", async () => {
    for (const [error, want, retryAfterS] of [
      ["no_partner", cm["cm.err.no_partner"], 0],
      ["agent_too_old", cm["cm.err.agent_too_old"], 0],
      ["node_offline", cm["cm.err.node_offline"], 0],
      ["busy", cm["cm.err.busy"], 0],
      ["too_soon", "Try again in 8 min.", 421],
    ] as const) {
      plan.mockResolvedValue(planOk({ error, retryAfterS, partners: error === "too_soon" ? [{ name: "nl1", knownMbps: 2000 }] : [] }));
      await mount();
      await openWindow();
      expect(dialog()!.textContent, error).toContain(want);
      expect(disabled(inDialog(cm["cm.start"])), error).toBe(true);
      expect(start).not.toHaveBeenCalled();
      act(() => root?.unmount());
      host?.remove();
      document.querySelectorAll("[role=dialog]").forEach((d) => d.remove());
    }
  });

  it("starts the run only when asked, once", async () => {
    start.mockResolvedValue({ status: running() });
    await mount();
    await openWindow();
    expect(start).not.toHaveBeenCalled();
    status.mockResolvedValue(running());
    await click(inDialog(cm["cm.start"]));
    expect(start).toHaveBeenCalledTimes(1);
    expect(start).toHaveBeenCalledWith({ nodeId: "nod_1" });
  });

  it("shows the panel's refusal of the start (the pause, a busy node) and runs nothing", async () => {
    start.mockResolvedValue({ status: idle({ error: "too_soon", retryAfterS: 300 }) });
    await mount();
    await openWindow();
    await click(inDialog(cm["cm.start"]));
    expect(dialog()!.textContent).toContain("Try again in 5 min.");
    expect(disabled(inDialog(cm["cm.start"]))).toBe(true);
  });
});

describe("the run", () => {
  it("says which direction, with whom, which step of how many, and what each step gave; Stop stops it", async () => {
    start.mockResolvedValue({ status: running() });
    cancel.mockResolvedValue({ status: idle({ state: "canceled" }) });
    await mount();
    await openWindow();
    status.mockResolvedValue(running());
    await click(inDialog(cm["cm.start"]));
    const t = dialog()!.textContent!;
    expect(t).toContain("Sending to nl1");
    expect(t).toContain("Step 2 of 4");
    expect(t).toContain("812 Mbps"); // the figure of the step that is over
    expect(t).toContain("Receiving · se1");
    expect(t).toContain(cm["cm.waiting"]);
    expect(t).toContain(cm["cm.stopNote"]);
    await click(inDialog(cm["cm.stop"]));
    expect(cancel).toHaveBeenCalledWith({ nodeId: "nod_1" });
    expect(dialog()!.textContent).toContain(cm["cm.canceled"]);
  });

  it("goes on when the window is closed: the card shows it, and the button opens the window again", async () => {
    status.mockResolvedValue(running());
    await mount();
    expect(button(cm["cm.running"])).toBeDefined();
    await click(button(cm["cm.running"]));
    expect(dialog()!.textContent).toContain("Sending to nl1");
    expect(cancel).not.toHaveBeenCalled();
    expect(start).not.toHaveBeenCalled();
    expect(plan).not.toHaveBeenCalled(); // a run that is going is read, not planned again
  });
});

describe("the result", () => {
  const finish = async (result: unknown) => {
    start.mockResolvedValue({ status: running() });
    status.mockResolvedValue(result);
    await mount();
    await openWindow();
    status.mockResolvedValue(running());
    await click(inDialog(cm["cm.start"]));
    status.mockResolvedValue(result);
    // the run is read again every second while it goes
    await act(async () => void (await new Promise((r) => setTimeout(r, 1100))));
    for (let i = 0; i < 3; i++) await settle();
  };

  it("shows both directions with the partner and offers the slower side rounded DOWN; nothing is written until Set is pressed", async () => {
    await finish(done(812.4, 455.1));
    const t = dialog()!.textContent!;
    expect(t).toContain("455 Mbps");
    expect(t).toContain("812 Mbps");
    expect(t).toContain("with nl1");
    expect(t).toContain("You wrote 250 Mbps, measured 455 Mbps. Set 450 Mbps?");
    expect(t).toContain(cm["cm.note"]);
    expect(updateNode).not.toHaveBeenCalled();
    updateNode.mockResolvedValue({});
    await click(inDialog("Set 450 Mbps"));
    expect(updateNode).toHaveBeenCalledTimes(1);
    expect(updateNode).toHaveBeenCalledWith({ nodeId: "nod_1", bandwidthMbps: 450 });
  });

  it("lets the owner write their own number instead", async () => {
    await finish(done(812.4, 455.1));
    const input = dialog()!.querySelector<HTMLInputElement>('input[type="number"]')!;
    expect(input.value).toBe("450");
    await type(input, "400");
    expect(updateNode).not.toHaveBeenCalled();
    updateNode.mockResolvedValue({});
    await click(inDialog("Set 400 Mbps"));
    expect(updateNode).toHaveBeenCalledWith({ nodeId: "nod_1", bandwidthMbps: 400 });
    await type(input, "0");
    expect(dialog()!.textContent).toContain(cm["cm.offer.bad"]);
    await type(input, "12.5");
    expect(dialog()!.textContent).toContain(cm["cm.offer.bad"]);
  });

  it("says nothing needs setting when the number written is the measure", async () => {
    plan.mockResolvedValue(planOk());
    await mount(NodeStatus.ONLINE, { bandwidthMbps: 450 });
    start.mockResolvedValue({ status: running() });
    await openWindow();
    status.mockResolvedValue(running());
    await click(inDialog(cm["cm.start"]));
    status.mockResolvedValue(done(812.4, 455.1));
    await act(async () => void (await new Promise((r) => setTimeout(r, 1100))));
    for (let i = 0; i < 3; i++) await settle();
    expect(dialog()!.textContent).toContain("The number you wrote, 450 Mbps, matches the measure.");
    expect(inDialog("Set 450 Mbps")).toBeUndefined();
  });

  it("offers no number when only one direction worked", async () => {
    await finish({ ...done(), result: { atUnix: nowSec(), down: dir(812.4, "nl1"), up: dir(0, ""), partners: 2 } });
    expect(dialog()!.textContent).toContain(cm["cm.offer.partial"]);
    expect([...dialog()!.querySelectorAll("button")].some((b) => b.textContent?.startsWith("Set "))).toBe(false);
    expect(dialog()!.querySelector('input[type="number"]')).toBeNull();
  });

  it("marks a figure that the volume cap cut short as a minimum", async () => {
    await finish({ ...done(), result: { atUnix: nowSec(), down: dir(8589.9, "nl1", true), up: dir(900, "nl1"), partners: 1 } });
    expect(dialog()!.textContent).toContain(cm["cm.capped"]);
  });

  it("says what went wrong when nothing was measured, with the reason", async () => {
    await finish(idle({ state: "failed", error: "unreachable", steps: [step("nl1", "down", "failed", 0, { error: "unreachable" }), step("nl1", "up", "failed", 0, { error: "unreachable" })] }));
    const t = dialog()!.textContent!;
    expect(t).toContain("Nothing was measured.");
    expect(t).toContain(cm["cm.err.unreachable"]);
    expect(updateNode).not.toHaveBeenCalled();
  });

  it("is kept on the card as a line: when, both figures, against whom", async () => {
    status.mockResolvedValue(done(812.4, 455.1));
    await mount();
    const line = document.querySelector('[data-testid="channel-last"]')!;
    expect(line.textContent).toContain("receiving 812 Mbps, sending 455 Mbps");
    expect(line.textContent).toContain("against nl1");
  });
});
