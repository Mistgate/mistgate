import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { en } from "@/i18n/en";
import { setEffectsEnabled } from "@/lib/effects";
import { en as np } from "@/i18n/nodepage";
import { ServerTab } from "./server";
import type { ServerSection } from "./tabs";

// Node → Server: the node form saves only what changed, a new install command for an enrolled node, measuring the network
// capacity, and what retiring says when the node could not be told.

const retireNode = vi.fn();
const measureBandwidth = vi.fn();
const updateNode = vi.fn();
const me = vi.fn();
vi.mock("@/lib/api", () => ({
  nodes: { retireNode: (...a: unknown[]) => retireNode(...a), measureBandwidth: (...a: unknown[]) => measureBandwidth(...a), updateNode: (...a: unknown[]) => updateNode(...a) },
  auth: { me: (...a: unknown[]) => me(...a) },
}));
const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate, Link: ({ children }: { children?: unknown }) => children }));
vi.mock("@/lib/updates", async (orig) => ({ ...(await orig<object>()), useOlderNodes: () => new Set<string>() }));
const addNode = vi.fn();
vi.mock("@/components/add-node", () => ({ useAddNode: () => addNode }));
vi.mock("./awg-backend", () => ({ AwgBackendCard: () => null }));
vi.mock("./decoy", () => ({ DecoyCard: () => null }));
const serverAccess = vi.fn(() => ({ data: null as null | { passwordGenerated: boolean } }));
vi.mock("./ssh-access", () => ({ SSHAccessCard: () => null, useServerAccess: () => serverAccess() }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  me.mockResolvedValue({ admin: { id: "adm_1", role: 1 } });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [retireNode, measureBandwidth, updateNode, me, navigate, addNode]) m.mockReset();
  serverAccess.mockReset();
  serverAccess.mockReturnValue({ data: null });
});

const data = (status = NodeStatus.DOWN, node: Record<string, unknown> = {}) =>
  ({
    node: { id: "nod_1", name: "de1", address: "de1.example.com", countryCode: "DE", status, location: "", publicLabel: "", provider: "", ...node },
    notes: "",
    dnsResolvers: [],
    inbounds: [],
    facts: { hostname: "de1", os: "Debian 13", kernel: "6.12", arch: "amd64", cpuCount: 2, ramTotalBytes: 0, diskTotalBytes: 0, virt: "kvm", hasIpv6: true, bootUnix: 0, engines: [] },
  }) as never;

/** The tab as the page holds it: the section is the page's state, the menu changes it. */
function Harness({ status, node, first }: { status?: NodeStatus; node?: Record<string, unknown>; first: ServerSection }) {
  const [section, setSection] = useState(first);
  return <ServerTab data={data(status, node)} section={section} onSection={setSection} />;
}

async function mount(status?: NodeStatus, node?: Record<string, unknown>, first: ServerSection = "node") {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <Harness status={status} node={node} first={first} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
/** The menu's button of a section. */
const nav = (section: ServerSection) => [...document.querySelectorAll<HTMLButtonElement>(".sv-nav button")].find((b) => b.textContent?.trim().startsWith(section === "del" ? en["node.danger"] : np[`np.sv.nav.${section}`]));
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]:not([aria-modal=false])");
const inDialog = (label: string) => [...(dialog()?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === label);
const click = async (el: Element | null | undefined) => {
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 3; i++) await settle();
};
async function type(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function retire() {
  await click(button(en["node.retire"]));
  await type(dialog()!.querySelector<HTMLInputElement>('input[placeholder="de1"]')!, "de1");
  await click(inDialog(en["node.retire"]));
}

describe("a new install command", () => {
  it("is offered to the owner for a node that was enrolled before", async () => {
    await mount(NodeStatus.ONLINE, undefined, "ag");
    expect(text()).toContain(en["node.add.reenrollBody"]);
    await click(button(en["node.banner.newCommand"]));
    expect(addNode).toHaveBeenCalledWith({ id: "nod_1", name: "de1" });
  });

  it("is not offered to a helper, whom the API refuses", async () => {
    me.mockResolvedValue({ admin: { id: "adm_2", role: 2 } });
    await mount(NodeStatus.DOWN, undefined, "ag");
    expect(button(en["node.banner.newCommand"])).toBeUndefined();
  });

  it("is not offered for a retired node", async () => {
    await mount(NodeStatus.RETIRED, undefined, "ag");
    expect(button(en["node.banner.newCommand"])).toBeUndefined();
  });
});

describe("the node's address", () => {
  const address = () => [...document.querySelectorAll("input")].find((i) => i.value === "de1.example.com");

  it("is the owner's to change", async () => {
    await mount(NodeStatus.ONLINE);
    expect(address()?.disabled).toBe(false);
    expect(text()).toContain(en["node.settings.addressHint"]);
  });

  it("is locked for a helper, whom the API refuses, and says why", async () => {
    me.mockResolvedValue({ admin: { id: "adm_2", role: 2 } });
    await mount(NodeStatus.ONLINE);
    expect(address()?.disabled).toBe(true);
    expect(text()).toContain(en["node.settings.addressOwnerHint"]);
  });
});

describe("measuring the network capacity", () => {
  const field = () => document.querySelector<HTMLInputElement>('input[type="number"]')!;
  const measured = (over: Record<string, unknown> = {}) => ({ downMbps: 937, upMbps: 871, server: "speed.cloudflare.com", errorCode: "", seconds: 29, runs: 3, runsTotal: 3, runFailures: [], serverDetail: "", peopleDownMbps: 0, peopleUpMbps: 0, ...over });

  it("starts as a button with what it does, and a click shows it is busy until the node answers", async () => {
    let answer!: (v: unknown) => void;
    measureBandwidth.mockReturnValue(new Promise((r) => (answer = r)));
    await mount(NodeStatus.ONLINE, undefined, "ch");
    expect(text()).toContain(en["node.settings.bandwidthMeasureHint"]);
    await click(button(en["node.settings.bandwidthMeasure"]));
    expect(measureBandwidth).toHaveBeenCalledWith({ nodeId: "nod_1" });
    const busy = button(en["node.settings.bandwidthMeasuring"]);
    expect(busy).toBeDefined();
    expect(busy!.hasAttribute("disabled") || busy!.getAttribute("data-disabled") !== null).toBe(true);
    expect(text()).toContain(en["node.settings.bandwidthMeasureBusy"]);
    expect(text()).toContain("30 seconds");
    expect(text()).toContain("1.5 GB");
    await act(async () => answer(measured()));
    for (let i = 0; i < 3; i++) await settle();
    expect(button(en["node.settings.bandwidthMeasuring"])).toBeUndefined();
    expect(button(np["np.sv.ch.again"])).toBeDefined();
  });

  it("the answer of the measurement sends a wave and two glints over the card for three seconds (the board's sv-wave), with the reveal animation on", async () => {
    measureBandwidth.mockResolvedValue(measured());
    setEffectsEnabled(true);
    await mount(NodeStatus.ONLINE, undefined, "ch");
    expect(document.querySelector(".fl-wv")).toBeNull();
    await click(button(en["node.settings.bandwidthMeasure"]));
    for (let i = 0; i < 3; i++) await settle();
    const card = document.querySelector('[aria-label="' + np["np.sv.nav.ch"] + '"]')!;
    expect(card.querySelector(".fl-wv")).not.toBeNull();
    expect(card.querySelectorAll(".fl-eg")).toHaveLength(2);
    setEffectsEnabled(false);
  });

  it("shows the result and fills the field with the rounded download figure only when asked, leaving the saving to the form", async () => {
    measureBandwidth.mockResolvedValue(measured());
    await mount(NodeStatus.ONLINE, undefined, "ch");
    await click(button(en["node.settings.bandwidthMeasure"]));
    expect(text()).toContain("Measured: 937 Mbps ↓ · 871 ↑");
    expect(text()).toContain("speed.cloudflare.com");
    expect(field().value).toBe(""); // the measurement alone changes nothing
    const save = button(en["common.save"])!;
    expect(save.hasAttribute("disabled") || save.getAttribute("data-disabled") !== null).toBe(true);
    await click(button("Use 870"));
    expect(field().value).toBe("870"); // the slower direction
    const save2 = button(en["common.save"])!;
    expect(save2.hasAttribute("disabled") || save2.getAttribute("data-disabled") !== null).toBe(false);
    expect(button("Use 870")!.hasAttribute("disabled") || button("Use 870")!.getAttribute("data-disabled") !== null).toBe(true); // it is in the field now
  });

  it("says how much of the figure is the people already on the node, only when there is some", async () => {
    measureBandwidth.mockResolvedValue(measured());
    await mount(NodeStatus.ONLINE, undefined, "ch");
    await click(button(en["node.settings.bandwidthMeasure"]));
    expect(text()).not.toContain("people's traffic");
    expect(text()).toContain("best of 3 runs");

    for (const [over, want] of [
      [{ peopleDownMbps: 35 }, "Including people's traffic: 35 Mbps ↓"],
      [{ peopleUpMbps: 12 }, "Including people's traffic: 12 Mbps ↑"],
      [{ peopleDownMbps: 35, peopleUpMbps: 12 }, "Including people's traffic: 35 Mbps ↓ · 12 ↑"],
    ] as const) {
      act(() => root?.unmount());
      host?.remove();
      measureBandwidth.mockResolvedValue(measured(over));
      await mount(NodeStatus.ONLINE, undefined, "ch");
      await click(button(en["node.settings.bandwidthMeasure"]));
      expect(text()).toContain(want);
      expect(text()).toContain("Measured: 937 Mbps ↓ · 871 ↑"); // the figures already include them
    }
  });

  it("says which server answered, and how many of the runs worked and why the others did not", async () => {
    measureBandwidth.mockResolvedValue(measured({ server: "Ookla", serverDetail: "MTS, Moscow" }));
    await mount(NodeStatus.ONLINE, undefined, "ch");
    await click(button(en["node.settings.bandwidthMeasure"]));
    expect(text()).toContain("Through Ookla · MTS, Moscow, the best of 3 runs.");

    act(() => root?.unmount());
    host?.remove();
    measureBandwidth.mockResolvedValue(measured({ runs: 1, runFailures: ["rate_limited", "rate_limited"] }));
    await mount(NodeStatus.ONLINE, undefined, "ch");
    await click(button(en["node.settings.bandwidthMeasure"]));
    expect(text()).toContain("Through speed.cloudflare.com, 1 of 3 runs worked: the others — the server limited the requests.");
    expect(text()).not.toContain("best of 1");
  });

  it("says so when only the download could be measured", async () => {
    measureBandwidth.mockResolvedValue(measured({ upMbps: 0 }));
    await mount(NodeStatus.ONLINE, undefined, "ch");
    await click(button(en["node.settings.bandwidthMeasure"]));
    expect(text()).toContain("Measured: 937 Mbps ↓ · the upload could not be measured");
    expect(button("Use 940")).toBeDefined(); // no upload figure: the download
  });

  it("words what the node answered", async () => {
    for (const code of ["busy", "unreachable", "unsupported", "failed"] as const) {
      measureBandwidth.mockResolvedValue({ downMbps: 0, upMbps: 0, server: "", errorCode: code, seconds: 0 });
      await mount(NodeStatus.ONLINE, undefined, "ch");
      await click(button(en["node.settings.bandwidthMeasure"]));
      expect(document.querySelector("[role=alert]")?.textContent, code).toBe(en[`node.settings.bandwidthErr.${code}`]);
      expect(button("Use 0")).toBeUndefined();
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("words an offline node, an old agent and a node that did not answer", async () => {
    for (const [err, want] of [
      [new ConnectError("node_offline", Code.FailedPrecondition), en["err.node_offline"]],
      [new ConnectError("agent too old", Code.FailedPrecondition), en["err.agent_too_old"]],
      [new ConnectError("the node did not answer in time", Code.DeadlineExceeded), en["node.settings.bandwidthErr.noAnswer"]],
    ] as const) {
      measureBandwidth.mockRejectedValue(err);
      await mount(NodeStatus.ONLINE, undefined, "ch");
      await click(button(en["node.settings.bandwidthMeasure"]));
      expect(document.querySelector("[role=alert]")?.textContent).toBe(want);
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("is for the owner of a node that is not retired", async () => {
    me.mockResolvedValue({ admin: { id: "adm_2", role: 2 } });
    await mount(NodeStatus.ONLINE, undefined, "ch");
    expect(button(en["node.settings.bandwidthMeasure"])).toBeUndefined();
    act(() => root?.unmount());
    host?.remove();
    me.mockResolvedValue({ admin: { id: "adm_1", role: 1 } });
    await mount(NodeStatus.RETIRED, undefined, "ch");
    expect(button(en["node.settings.bandwidthMeasure"])).toBeUndefined();
  });
});

describe("the node form", () => {
  it("saves only the fields that changed, each form with its own bar, the timeouts as one", async () => {
    updateNode.mockResolvedValue({});
    await mount(NodeStatus.ONLINE);
    const save = button(en["common.save"])!;
    expect(save.hasAttribute("disabled") || save.getAttribute("data-disabled") !== null).toBe(true);
    await type(document.querySelector<HTMLInputElement>(`input[placeholder="${en["node.settings.notesPh"]}"], textarea[placeholder="${en["node.settings.notesPh"]}"]`)! as never, "paid until March");
    await click(button(en["common.save"]));
    expect(updateNode).toHaveBeenCalledTimes(1);
    expect(updateNode.mock.calls[0]![0]).toEqual({ nodeId: "nod_1", notes: "paid until March" });
    // the agent's link is another form: its bar sends the timeouts alone
    await click(nav("ag"));
    await click(document.querySelector(`[aria-label="${en["common.increase"].replace("{what}", en["node.settings.dial"])}"]`));
    await click(button(en["common.save"]));
    expect(updateNode).toHaveBeenCalledTimes(2);
    expect(updateNode.mock.calls[1]![0]).toEqual({ nodeId: "nod_1", timeouts: { livenessTimeoutS: 0, applyTimeoutS: 0, dialTimeoutS: 20 } });
  });

  it("keeps what was typed when another section is looked at, and puts it back on Cancel", async () => {
    await mount(NodeStatus.ONLINE);
    const notes = () => document.querySelector<HTMLTextAreaElement>("textarea")!;
    await type(notes() as never, "x");
    await click(nav("ssh"));
    await click(nav("node"));
    expect(notes().value).toBe("x");
    await click(button(np["np.sv.cancel"]));
    expect(notes().value).toBe("");
  });

  it("has a public label, says who sees it, and sends it trimmed and alone", async () => {
    updateNode.mockResolvedValue({});
    await mount(NodeStatus.ONLINE, { publicLabel: "Fast" });
    expect(text()).toContain(en["node.settings.publicLabel"]);
    expect(text()).toContain(en["node.settings.publicLabelHint"]);
    const field = [...document.querySelectorAll("input")].find((i) => i.value === "Fast")!;
    expect(field.maxLength).toBe(40);
    await type(field, "  Home route ");
    await click(button(en["common.save"]));
    expect(updateNode.mock.calls[0]![0]).toEqual({ nodeId: "nod_1", publicLabel: "Home route" });
  });

  it("clears the public label with an empty field, and leaves it alone when it is not touched", async () => {
    updateNode.mockResolvedValue({});
    await mount(NodeStatus.ONLINE, { publicLabel: "Fast" });
    await type([...document.querySelectorAll("input")].find((i) => i.value === "Fast")!, "");
    await click(button(en["common.save"]));
    expect(updateNode.mock.calls[0]![0]).toEqual({ nodeId: "nod_1", publicLabel: "" });
    await act(async () => root?.unmount());
    host?.remove();
    updateNode.mockClear();
    await mount(NodeStatus.ONLINE, { publicLabel: "Fast" });
    await type(document.querySelector<HTMLTextAreaElement>("textarea")! as never, "x");
    await click(button(en["common.save"]));
    expect(updateNode.mock.calls[0]![0]).toEqual({ nodeId: "nod_1", notes: "x" });
  });

  it("locks the public label of a retired node", async () => {
    await mount(NodeStatus.RETIRED, { publicLabel: "Fast" });
    expect([...document.querySelectorAll("input")].find((i) => i.value === "Fast")!.disabled).toBe(true);
  });

  it("puts the agent and the host in sections of their own, and hides what a retired node cannot have", async () => {
    await mount(NodeStatus.ONLINE, { agentVersion: "v0.1.71" }, "ag");
    expect(text()).toContain(en["node.server.agent"]);
    expect(text()).toContain("v0.1.71");
    await click(nav("sys"));
    expect(text()).toContain(en["node.server.host"]);
    expect(text()).toContain("Debian 13");
    act(() => root?.unmount());
    host?.remove();
    await mount(NodeStatus.RETIRED);
    expect([...document.querySelectorAll(".sv-nav button")].map((b) => b.textContent?.trim())).toEqual([np["np.sv.nav.node"], np["np.sv.nav.ag"], np["np.sv.nav.ssh"], np["np.sv.nav.sys"]]);
  });
});

describe("per-person speed limit", () => {
  it("says an agent that cannot share a bucket holds each device to the smaller limit", async () => {
    await mount(NodeStatus.ONLINE, { lastSeenUnix: 1000, userRateLimitSupported: false }, "ag");
    expect(text()).toContain(en["node.settings.userRateLimitUnsupported"]);
  });

  it("is quiet for an agent that applies it, and for a node that never connected", async () => {
    await mount(NodeStatus.ONLINE, { lastSeenUnix: 1000, userRateLimitSupported: true }, "ag");
    expect(text()).not.toContain(en["node.settings.userRateLimitUnsupported"]);
    act(() => root?.unmount());
    host?.remove();
    await mount(NodeStatus.ONLINE, { lastSeenUnix: 0, userRateLimitSupported: false }, "ag");
    expect(text()).not.toContain(en["node.settings.userRateLimitUnsupported"]);
  });
});

describe("retiring a node", () => {
  it("goes back to the list when the agent got the order", async () => {
    retireNode.mockResolvedValue({ agentNotified: true });
    await mount(NodeStatus.ONLINE, undefined, "del");
    await retire();
    expect(retireNode).toHaveBeenCalledWith({ nodeId: "nod_1", confirmName: "de1" });
    expect(navigate).toHaveBeenCalledWith({ to: "/nodes" });
    expect(text()).not.toContain("did not get the order");
  });

  it("warns when the panel generated the server password, which only the panel knows", async () => {
    await mount(NodeStatus.ONLINE, undefined, "del");
    await click(button(en["node.retire"]));
    expect(dialog()?.textContent).not.toContain(en["node.retireGeneratedPassword"]);
    await click(inDialog(en["common.cancel"]));

    serverAccess.mockReturnValue({ data: { passwordGenerated: true } });
    await click(button(en["node.retire"]));
    expect(dialog()?.textContent).toContain(en["node.retireGeneratedPassword"]);
  });

  it("says an offline node was not reached and gives the cleanup to run on the server", async () => {
    retireNode.mockResolvedValue({ agentNotified: false });
    await mount(undefined, undefined, "del");
    await retire();
    expect(navigate).not.toHaveBeenCalled();
    expect(dialog()?.textContent).toContain("de1 did not get the order");
    expect(dialog()?.textContent).toContain(en["node.retiredUnreachedBody"]);
    expect(dialog()?.textContent).toContain("systemctl disable --now mistgate-node");
    expect(dialog()?.textContent).toContain("rm -rf /var/lib/mistgate-node");
    await click(inDialog(en["common.done"]));
    expect(navigate).toHaveBeenCalledWith({ to: "/nodes" });
  });
});
