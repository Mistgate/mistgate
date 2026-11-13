import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { OsRebootPolicy, OsRolloutMode, OsRolloutStatus, OsStepState, OsUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { setLang } from "@/i18n";
import { osRows, osUpdatesQuery, type OsUpdates } from "@/lib/os-updates";
import { meQuery } from "@/lib/session";
import { OsNodeActions } from "./os-actions";
import { OsSection } from "./os";

// Several nodes in one OS update: the ticks and the window (what, order, batch, who is left out), the queue of a run in each state
// with its buttons, the names of the security packages, who holds the package lock, and "All packages" for one node.

const updates = vi.hoisted(() => ({ getOsUpdates: vi.fn(), startOsRollout: vi.fn(), cancelOsRollout: vi.fn(), pauseOsRollout: vi.fn(), resumeOsRollout: vi.fn(), refreshOsUpdates: vi.fn() }));
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), updates }));
const guard = vi.hoisted(() => vi.fn((call: () => Promise<unknown>) => call()));
vi.mock("@/components/step-up", () => ({ useStepUp: () => guard, isStepUpCancelled: () => false }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  setLang("en");
  for (const m of Object.values(updates)) m.mockResolvedValue({});
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [...Object.values(updates), guard]) m.mockReset();
  guard.mockImplementation((call: () => Promise<unknown>) => call());
  setLang("en");
});

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const NOW = Math.floor(Date.now() / 1000);
const base = {
  measuredUnix: NOW - 60, distroId: "ubuntu", distroVersion: "24.04", pending: 0, security: 0, notable: [], kernelRunning: "6.8.0-147-generic", kernelNewest: "6.8.0-147-generic",
  rebootRequired: false, rebootReasons: [], held: [], apt: "ok", halfConfigured: [], listsUnix: NOW - 3600, dpkgUnix: NOW - 7200, autoUpdates: "on", autoReboot: false, awgModuleNext: "", container: "", bootIdHash: "", error: "",
  securityPending: [], securityStuck: [], aptLockHolder: "", aptLockSinceUnix: 0,
};
const node = (id: string, online: number, over: Record<string, unknown> = {}, status: Record<string, unknown> = {}) => ({
  nodeId: `nod_${id}`, name: id, connected: true, supported: true, onlineUsers: online, onPanelServer: false, state: OsUpdateState.UPDATES, status: { ...base, pending: 6, security: 2, ...status },
  updateCapable: true, updateBlock: "", rebootBlock: "", rebootPossibleUnix: 0, ...over,
});
const step = (id: string, state: OsStepState, stage: number, over: Record<string, unknown> = {}) => ({
  nodeId: `nod_${id}`, nodeName: id, state, jobId: "", phase: "", phaseUnix: 0, kernelFrom: "", kernelTo: "", sentUnix: 0, rebootUnix: 0, backUnix: 0, finishedUnix: 0, errorKey: "", params: {}, stage, retryAfterUnix: 0, ...over,
});
const rollout = (steps: unknown[], over: Record<string, unknown> = {}) => ({
  id: "osr_1", status: OsRolloutStatus.RUNNING, mode: OsRolloutMode.SECURITY, reboot: OsRebootPolicy.IF_REQUIRED, createdUnix: NOW - 300, finishedUnix: 0, pauseKey: "", pauseParams: {}, steps, batchSize: 2, ...over,
});
const data = (nodes: unknown[], run?: unknown): OsUpdates => ({ nowUnix: NOW, scheduleTimezoneOffsetMinutes: 0, panelHost: base, nodes, rollout: run }) as never;

// nl1 is the quietest, de1 and fi1 have security updates, sg1 only ordinary packages, msk1 an old agent
const nodes = () => [
  node("fi1", 5),
  node("de1", 3),
  node("nl1", 0),
  node("sg1", 2, { updateBlock: "nothing_to_do", state: OsUpdateState.UP_TO_DATE }, { security: 0, pending: 18 }),
  node("msk1", 0, { supported: false, updateCapable: false, updateBlock: "agent_old", rebootBlock: "agent_old", state: OsUpdateState.UNSUPPORTED, status: undefined }),
];

async function mount(ui: ReactNode, seed: OsUpdates, { owner = true }: { owner?: boolean } = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", login: "owner", role: owner ? Role.OWNER : Role.READONLY }, edition: "vps", unavailable: [] } as never);
  qc.setQueryData(osUpdatesQuery.queryKey, seed);
  // a call that changes something refetches the page's data: it gets the same answer back
  updates.getOsUpdates.mockResolvedValue(seed);
  await act(async () => root!.render(<QueryClientProvider client={qc}><ToastProvider>{ui}</ToastProvider></QueryClientProvider>));
  for (let i = 0; i < 3; i++) await settle();
}
const text = () => host!.textContent ?? "";
const button = (label: string, within: ParentNode = document) => [...within.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
const buttonStarting = (label: string, within: ParentNode = document) => [...within.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim().startsWith(label));
const disabled = (b: HTMLButtonElement | undefined) => !!b && (b.hasAttribute("data-disabled") || b.disabled);
const click = async (el: Element | null | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  for (let i = 0; i < 3; i++) await settle();
};
const tick = (name: string) => host!.querySelector<HTMLInputElement>(`input[type=checkbox][aria-label="Select ${name}"]`);
// the table and the phone's cards both hold a tick per node (one of them is hidden by the layout): one name once, the header's tick apart
const ticked = () => [...new Set([...host!.querySelectorAll<HTMLInputElement>("input[type=checkbox][aria-label^='Select ']")].filter((i) => i.checked && !i.getAttribute("aria-label")!.startsWith("Select all")).map((i) => i.getAttribute("aria-label")!.slice(7)))];
const dialog = () => document.querySelector<HTMLElement>("[data-shot=os-fleet-confirm]");
const radio = (label: string) => [...dialog()!.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent?.includes(label))!;

describe("ticking nodes in the table", () => {
  it("gives the owner a tick on every node and one button that ticks all that have something to install", async () => {
    await mount(<OsSection />, data(nodes()));
    expect(host!.querySelectorAll("input[type=checkbox][aria-label^='Select ']").length).toBeGreaterThanOrEqual(5);
    expect(text()).toContain("Tick nodes to update several at once.");
    await click(button("Select all with something to install (4)"));
    // nl1, de1, fi1 have security updates and sg1 has ordinary packages; msk1 has an old agent
    expect(ticked().sort()).toEqual(["de1", "fi1", "nl1", "sg1"]);
    expect(text()).toContain("4 nodes selected");
    expect(button("Update 4 nodes…")).toBeTruthy();
    await click(button("Clear"));
    expect(ticked()).toEqual([]);
  });

  it("says so when no node has anything to install, and offers no button", async () => {
    await mount(<OsSection />, data([node("de1", 1, { updateBlock: "nothing_to_do" }, { security: 0, pending: 0 }), node("nl1", 0, { updateBlock: "nothing_to_do" }, { security: 0, pending: 0 })]));
    expect(text()).toContain("No node has anything to install right now.");
    expect(buttonStarting("Select all")).toBeUndefined();
  });

  it("shows no ticks to someone who is not the owner, nor while a run holds the next one", async () => {
    await mount(<OsSection />, data(nodes()), { owner: false });
    expect(host!.querySelector("input[type=checkbox]")).toBeNull();
    act(() => root?.unmount());
    host!.remove();
    await mount(<OsSection />, data(nodes(), rollout([step("nl1", OsStepState.SENT, 0), step("de1", OsStepState.PENDING, 1)])));
    expect(host!.querySelector("input[type=checkbox]")).toBeNull();
    expect(host!.querySelector("[data-shot=os-selection-bar]")).toBeNull();
  });
});

describe("the window before the run", () => {
  async function open(...names: string[]) {
    await mount(<OsSection />, data(nodes()));
    for (const n of names) await click(tick(n));
    await click(buttonStarting("Update "));
  }

  it("names the canary, lists the order, the people online and who is left out for now", async () => {
    await open("fi1", "de1", "nl1", "msk1");
    const d = dialog()!.textContent!;
    expect(d).toContain("Update 3 nodes");
    expect(d).toContain("nl1 goes first: the fewest people are online there.");
    expect(d).toContain("nobody online");
    expect(d).toContain("3 online");
    expect(d).toContain("5 online");
    expect(d).toContain("8 people are online on these nodes now.");
    expect(d).toContain("Left out for now");
    expect(d).toContain("msk1");
    expect(d).toContain("The node agent is too old to run updates");
    // the first kind is chosen, and the reboot as recommended
    expect(radio("Security updates").getAttribute("aria-checked")).toBe("true");
    expect(radio("Reboot if required").getAttribute("aria-checked")).toBe("true");
    expect(d).toContain("Fits 3 of 4");
  });

  it("puts a small heading over each group of choices, so they do not run into each other", async () => {
    await open("fi1", "de1", "nl1");
    const heads = [...dialog()!.querySelectorAll("h3")].map((h) => h.textContent);
    expect(heads).toContain("What to install");
    expect(heads).toContain("Reboot");
    expect(heads.indexOf("What to install")).toBeLessThan(heads.indexOf("Reboot"));
    // a reboot-only run has no reboot choice, so no such group
    await click(radio("Only a reboot"));
    expect([...dialog()!.querySelectorAll("h3")].map((h) => h.textContent)).not.toContain("Reboot");
  });

  it("takes one node at a time by default, even for a big fleet, and sends more only when the owner picks it", async () => {
    const many = ["a1", "a2", "a3", "a4", "a5", "a6"].map((n, i) => node(n, i));
    await mount(<OsSection />, data(many));
    await click(button("Select all with something to install (6)"));
    await click(buttonStarting("Update "));
    const picked = () => [...dialog()!.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent === "1")!;
    expect(picked().getAttribute("aria-checked")).toBe("true");
    expect(dialog()!.textContent).toContain("A bigger batch finishes sooner, but more people lose their connection at the same time.");
    await click(buttonStarting("Start on 6 nodes", dialog()!));
    expect(updates.startOsRollout).toHaveBeenCalledWith(expect.objectContaining({ batchSize: 1 }));
  });

  it("starts one run for the nodes that can go, the canary first, after a fresh step-up", async () => {
    await open("fi1", "de1", "nl1", "msk1");
    await click(buttonStarting("Start on 3 nodes", dialog()!));
    expect(guard).toHaveBeenCalledTimes(1);
    // the nodes in the order the panel takes them, the one left out not sent
    expect(updates.startOsRollout).toHaveBeenCalledWith({ nodeIds: ["nod_nl1", "nod_de1", "nod_fi1"], mode: OsRolloutMode.SECURITY, reboot: OsRebootPolicy.IF_REQUIRED, batchSize: 1 });
    expect(dialog()).toBeNull();
    // the ticks are cleared once it has started
    expect(ticked()).toEqual([]);
  });

  it("takes the ordinary packages in with All packages, and sends the mode, the reboot choice and the batch size", async () => {
    await open("fi1", "de1", "nl1", "sg1");
    expect(dialog()!.textContent).toContain("Update 3 nodes");
    expect(dialog()!.textContent).toContain("sg1");
    expect(dialog()!.textContent).toContain("There are no security updates to install");
    await click(radio("All packages"));
    expect(dialog()!.textContent).toContain("Update 4 nodes");
    await click(radio("Do not reboot"));
    // four nodes: the canary and three more, in batches of 1 to 3
    const batch = [...dialog()!.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent === "2")!;
    await click(batch);
    await click(buttonStarting("Start on 4 nodes", dialog()!));
    expect(updates.startOsRollout).toHaveBeenCalledWith({ nodeIds: ["nod_nl1", "nod_sg1", "nod_de1", "nod_fi1"], mode: OsRolloutMode.ALL, reboot: OsRebootPolicy.NEVER, batchSize: 2 });
  });

  it("opens on All packages when the ticked nodes have only ordinary ones", async () => {
    await open("sg1", "msk1");
    expect(radio("All packages").getAttribute("aria-checked")).toBe("true");
  });

  it("makes a reboot-only run the red button, with no reboot choice", async () => {
    await open("fi1", "de1");
    await click(radio("Only a reboot"));
    expect(dialog()!.querySelector("[aria-label='After installing']")).toBeNull();
    const go = buttonStarting("Reboot 2 nodes", dialog()!)!;
    expect(go.className).toContain("bg-danger-soft");
    await click(go);
    expect(updates.startOsRollout).toHaveBeenCalledWith({ nodeIds: ["nod_de1", "nod_fi1"], mode: OsRolloutMode.REBOOT_ONLY, reboot: OsRebootPolicy.ALWAYS, batchSize: 1 });
  });

  it("cannot start when none of the ticked nodes can go, and says why for each", async () => {
    await open("msk1");
    expect(disabled(buttonStarting("Start on", dialog()!))).toBe(true);
    expect(dialog()!.textContent).toContain("None of the ticked nodes can be updated this way right now.");
    expect(dialog()!.textContent).toContain("The node agent is too old");
    expect(updates.startOsRollout).not.toHaveBeenCalled();
  });

  it("keeps the window open and does not clear the ticks when the call is refused", async () => {
    updates.startOsRollout.mockRejectedValue(new Error("boom"));
    await open("fi1", "de1");
    await click(buttonStarting("Start on 2 nodes", dialog()!));
    expect(dialog()).not.toBeNull();
    expect(ticked().sort()).toEqual(["de1", "fi1"]);
  });
});

describe("the queue of a run", () => {
  const passed = (id: string, stage = 0) => step(id, OsStepState.PASSED, stage, { finishedUnix: NOW - 60, params: { upgraded: "4", reboot: "done" } });
  const all = (run: unknown) => data(nodes().map((n) => ({ ...n, updateBlock: "rollout_active", rebootBlock: "rollout_active" })), run);
  const card = () => host!.querySelector<HTMLElement>("[data-shot=os-fleet-run]")!;

  it("shows the stages, the state of every node and the progress as 'N of M'", async () => {
    await mount(<OsSection />, all(rollout([
      passed("nl1"),
      step("de1", OsStepState.SENT, 1, { phase: "installing" }),
      step("fi1", OsStepState.REBOOTING, 1),
      step("sg1", OsStepState.PENDING, 2),
      step("msk1", OsStepState.SKIPPED, 2, { errorKey: "unsupported" }),
    ])));
    const c = card().textContent!;
    expect(c).toContain("Updating the systems of 5 nodes");
    expect(c).toContain("Canary");
    expect(c).toContain("Batch 1");
    expect(c).toContain("Batch 2");
    expect(c).toContain("2 of 5 nodes done");
    for (const w of ["Done", "Installing", "Rebooting", "In the queue", "Skipped"]) expect(c, w).toContain(w);
    expect(c).toContain("Security updates · reboot if required · then 2 at a time");
    expect(card().querySelector("[role=progressbar]")?.getAttribute("aria-valuenow")).toBe("2");
  });

  it("says a waiting node waits, calmly: the time, the reason and who holds the lock; no red", async () => {
    const nl1 = node("de1", 3, { updateBlock: "rollout_active", rebootBlock: "rollout_active" }, { apt: "locked", aptLockHolder: "unattended-upgr", aptLockSinceUnix: NOW - 14 * 60 });
    await mount(<OsSection />, data([nl1, node("fi1", 5)], rollout([
      step("de1", OsStepState.PENDING, 1, { phase: "waiting_retry", retryAfterUnix: NOW + 11 * 60 - 5, params: { retry_code: "apt_lock" } }),
      step("fi1", OsStepState.PENDING, 2),
    ])));
    const c = card().textContent!;
    expect(c).toContain("Can retry in 11 min");
    expect(c).toContain("another package manager holds the lock");
    expect(c).toContain("unattended-upgr has been installing packages on the node for 14 min");
    expect(card().querySelector("[class*=text-danger]")).toBeNull();
    expect(card().querySelector("[role=alert]")).toBeNull();
    // the row of the node says it too, in one line, without a moving bar
    expect(host!.textContent).toContain("Can retry in 11 min");
    expect(host!.querySelector("[role=progressbar][aria-label='The update is in progress']")).toBeNull();
  });

  it("offers Pause and Cancel to a running run, and Pause asks the panel after a step-up", async () => {
    await mount(<OsSection />, all(rollout([passed("nl1"), step("de1", OsStepState.PENDING, 1)])));
    expect(button("Pause", card())).toBeTruthy();
    expect(button("Cancel…", card())?.title).toContain("A job already running on a node is not interrupted");
    await click(button("Pause", card()));
    expect(guard).toHaveBeenCalled();
    expect(updates.pauseOsRollout).toHaveBeenCalledWith({ rolloutId: "osr_1" });
  });

  it("asks before cancelling, and says a job that is already running is not interrupted", async () => {
    await mount(<OsSection />, all(rollout([passed("nl1"), step("de1", OsStepState.SENT, 1, { phase: "installing" })])));
    await click(button("Cancel…", card()));
    const d = document.querySelector<HTMLElement>("[data-shot=os-fleet-cancel]")!;
    expect(d.textContent).toContain("A job already running on a node is never interrupted: it finishes and is still checked.");
    expect(updates.cancelOsRollout).not.toHaveBeenCalled();
    await click(button("Keep going", d));
    expect(document.querySelector("[data-shot=os-fleet-cancel]")).toBeNull();
    await click(button("Cancel…", card()));
    await click(button("Cancel the update", document.querySelector<HTMLElement>("[data-shot=os-fleet-cancel]")!));
    expect(updates.cancelOsRollout).toHaveBeenCalledWith({ rolloutId: "osr_1" });
  });

  it("says a pause by the owner in a quiet notice and offers Continue", async () => {
    await mount(<OsSection />, all(rollout([passed("nl1"), step("de1", OsStepState.PENDING, 1)], { status: OsRolloutStatus.PAUSED, pauseKey: "owner" })));
    expect(card().textContent).toContain("Paused. Nothing new starts until you continue.");
    expect(card().querySelector("[role=alert]")).toBeNull();
    await click(button("Continue", card()));
    expect(updates.resumeOsRollout).toHaveBeenCalledWith({ rolloutId: "osr_1", skipFailed: false });
  });

  const stopped = (over: Record<string, unknown> = {}) =>
    all(rollout(
      [passed("nl1"), step("de1", OsStepState.FAILED, 1, { errorKey: "reboot_held", params: { code: "boot_default_custom", mode: "security", ...over } }), step("fi1", OsStepState.PENDING, 1)],
      { status: OsRolloutStatus.PAUSED, pauseKey: "reboot_held", pauseParams: { node: "de1", reason: "reboot_held", code: "boot_default_custom", mode: "security" } },
    ));

  it("explains a stop by a failed node with the reason and gives Retry, Skip and continue, Close", async () => {
    await mount(<OsSection />, stopped());
    const banner = card().querySelector<HTMLElement>("[role=alert]")!;
    expect(banner.textContent).toContain("de1: updates are installed, but the server was not rebooted.");
    expect(banner.textContent).toContain("GRUB");
    expect(banner.textContent).toContain("Retry tries this node once more");
    // the accent is on Retry: it is the first thing to try
    expect(button("Retry", banner)!.className).toContain("bg-accent");
    expect(button("Skip and continue", banner)!.className).not.toContain("bg-accent");
    await click(button("Retry", banner));
    expect(updates.resumeOsRollout).toHaveBeenLastCalledWith({ rolloutId: "osr_1", skipFailed: false });
    await click(button("Skip and continue", banner));
    expect(updates.resumeOsRollout).toHaveBeenLastCalledWith({ rolloutId: "osr_1", skipFailed: true });
    await click(button("Close", banner));
    expect(updates.cancelOsRollout).toHaveBeenCalledWith({ rolloutId: "osr_1" });
    // the reason is said once, in the banner, not again under the stage
    expect(card().textContent!.match(/boot menu \(GRUB\)/g)).toHaveLength(1);
  });

  it("turns Retry off after a retry that failed again, and says why", async () => {
    await mount(<OsSection />, stopped({ retry_count: "1" }));
    const banner = card().querySelector<HTMLElement>("[role=alert]")!;
    expect(disabled(button("Retry", banner))).toBe(true);
    expect(banner.textContent).toContain("already retried once");
    expect(disabled(button("Skip and continue", banner))).toBeFalsy();
    // and the accent moves to the one that is left
    expect(button("Skip and continue", banner)!.className).toContain("bg-accent");
    expect(button("Retry", banner)!.className).not.toContain("bg-accent");
  });

  it("says a cancel that waits for a running job, with no buttons", async () => {
    await mount(<OsSection />, all(rollout([passed("nl1"), step("de1", OsStepState.SENT, 1, { phase: "installing" }), step("fi1", OsStepState.SKIPPED, 1, { errorKey: "cancelled" })], { status: OsRolloutStatus.PAUSED, pauseKey: "cancel_wait" })));
    expect(card().textContent).toContain("Cancelling. The nodes that have not started are skipped");
    expect(button("Pause", card())).toBeUndefined();
    expect(button("Continue", card())).toBeUndefined();
    // a node that was only cancelled needs no sentence of its own
    expect(card().textContent).not.toContain("cancelled before it started");
  });

  it("shows a finished run folded to its header, and opens it on a click", async () => {
    const done = rollout([passed("nl1"), passed("de1", 1), step("msk1", OsStepState.SKIPPED, 2, { errorKey: "unsupported" })], { status: OsRolloutStatus.DONE, finishedUnix: NOW - 100 });
    await mount(<OsSection />, data(nodes(), done));
    expect(card().textContent).toContain("The last update of 3 nodes");
    expect(card().textContent).toContain("Done");
    expect(card().textContent).not.toContain("Canary");
    await click(button("Show", card()) ?? [...card().querySelectorAll("button")].find((b) => b.getAttribute("aria-expanded") === "false"));
    expect(card().textContent).toContain("Canary");
  });

  it("gives someone who is not the owner the queue without its buttons", async () => {
    await mount(<OsSection />, stopped(), { owner: false });
    expect(card().textContent).toContain("Needs a look");
    expect(button("Retry", card())).toBeUndefined();
    expect(button("Pause", card())).toBeUndefined();
  });

  it("shows nothing of it for a run of one node: that stays on the node's own row", async () => {
    await mount(<OsSection />, data(nodes(), rollout([step("de1", OsStepState.SENT, 0, { phase: "installing" })])));
    expect(host!.querySelector("[data-shot=os-fleet-run]")).toBeNull();
    expect(text()).toContain("Installing updates…");
  });
});

describe("the names of the security packages", () => {
  const named = () => data([node("de1", 3, {}, { security: 4, securityPending: ["libfoo1", "libbar-common"], securityStuck: ["libfoo1"] }), node("nl1", 0)]);

  it("opens 'Security: 4' to the list of names, the stuck one marked and a calm note that says it will be installed from the panel", async () => {
    await mount(<OsSection />, named());
    expect(text()).not.toContain("libbar-common");
    const toggle = buttonStarting("Security: 4")!;
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    await click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    const list = host!.querySelector<HTMLElement>("[role=group][aria-label='Security updates waiting on de1']")!;
    expect(list.textContent).toContain("libfoo1");
    expect(list.textContent).toContain("libbar-common");
    // two of four are named by the agent here: the rest is counted, not invented
    expect(list.textContent).toContain("and 2 more");
    expect(list.querySelector("li[title='waits for a new package']")?.textContent).toBe("libfoo1");
    expect(list.textContent).toContain("Automatic updates do not install these on their own: they need a newer package. They will be installed the next time you run the security updates from the panel.");
    expect(list.querySelector("[role=alert]")).toBeNull();
    await click(toggle);
    expect(text()).not.toContain("libbar-common");
  });

  it("offers no list for an agent that does not name its packages", async () => {
    await mount(<OsSection />, data([node("de1", 3), node("nl1", 0)]));
    expect(buttonStarting("Security:")).toBeUndefined();
  });

  it("says who holds the package lock and for how long, as a quiet line", async () => {
    await mount(<OsSection />, data([node("de1", 3, {}, { apt: "locked", aptLockHolder: "unattended-upgr", aptLockSinceUnix: NOW - 14 * 60 }), node("nl1", 0)]));
    expect(text()).toContain("unattended-upgr has been installing packages on the node for 14 min.");
    expect(text()).not.toContain("The package manager is busy");
  });

  it("says the generic words for a lock whose holder is unknown", async () => {
    await mount(<OsSection />, data([node("de1", 3, {}, { apt: "locked" }), node("nl1", 0)]));
    expect(text()).not.toContain("has been installing packages");
  });

  it("says who held the lock when the node refused for the whole wait", async () => {
    const refused = rollout([step("de1", OsStepState.SKIPPED, 0, { errorKey: "try_later", finishedUnix: NOW - 60, params: { code: "apt_lock", mode: "security" } })], { status: OsRolloutStatus.DONE, finishedUnix: NOW - 60 });
    await mount(<OsSection />, data([node("de1", 3, {}, { job: { aptLockHolder: "unattended-upgr", aptLockMinutes: 30, code: "apt_lock", jobId: "osj_1" } }), node("nl1", 0)], refused));
    expect(text()).toContain("unattended-upgr held the package lock for the whole 30-minute wait, so nothing was changed.");
  });

  it("notes what an automatic update left behind", async () => {
    await mount(<OsSection />, data([node("de1", 3, {}, { job: { noteCode: "security_leftover_failed", note: "english from the node" } }), node("nl1", 0)]));
    expect(text()).toContain("After the automatic update some security packages are still waiting.");
    expect(text()).not.toContain("english from the node");
  });
});

describe("All packages for one node", () => {
  const one = (status: Record<string, unknown>, over: Record<string, unknown> = {}) => data([node("de1", 3, over, status)]);
  const actions = (d: OsUpdates) => <OsNodeActions row={osRows(d).find((r) => r.nodeId === "nod_de1")!} />;
  const win = () => document.querySelector<HTMLElement>("[data-shot=os-confirm]")!;
  const pick = (label: string) => [...win().querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent?.includes(label))!;

  it("offers the choice when ordinary packages wait beside the security updates, security first", async () => {
    const d = one({ security: 2, pending: 12 });
    await mount(actions(d), d);
    await click(button("Install updates…"));
    expect(pick("Security updates").getAttribute("aria-checked")).toBe("true");
    expect(win().textContent).toContain("2 updates are waiting. Ordinary packages are left for later.");
    expect([...win().querySelectorAll("h3")].map((h) => h.textContent)).toEqual(["What to install", "Reboot"]);
    await click(pick("All packages"));
    expect(win().textContent).toContain("Install all updates on de1");
    expect(win().textContent).toContain("12 updates will be installed, security and ordinary");
    await click(button("Install updates", win()));
    expect(updates.startOsRollout).toHaveBeenCalledWith({ nodeIds: ["nod_de1"], mode: OsRolloutMode.ALL, reboot: OsRebootPolicy.IF_REQUIRED });
  });

  it("starts the usual security run when the choice is left alone", async () => {
    const d = one({ security: 2, pending: 12 });
    await mount(actions(d), d);
    await click(button("Install updates…"));
    await click(button("Install updates", win()));
    expect(updates.startOsRollout).toHaveBeenCalledWith({ nodeIds: ["nod_de1"], mode: OsRolloutMode.SECURITY, reboot: OsRebootPolicy.IF_REQUIRED });
  });

  it("makes Install updates available for a node with only ordinary packages, and opens it on All packages", async () => {
    const d = one({ security: 0, pending: 9 }, { updateBlock: "nothing_to_do", state: OsUpdateState.UP_TO_DATE });
    await mount(actions(d), d);
    expect(disabled(button("Install updates…"))).toBeFalsy();
    await click(button("Install updates…"));
    expect(win().textContent).toContain("Install all updates on de1");
    // nothing to choose: only ordinary packages wait
    expect(win().querySelector("[aria-label='What to install']")).toBeNull();
    await click(button("Install updates", win()));
    expect(updates.startOsRollout).toHaveBeenCalledWith({ nodeIds: ["nod_de1"], mode: OsRolloutMode.ALL, reboot: OsRebootPolicy.IF_REQUIRED });
  });

  it("keeps Install updates off when nothing at all waits", async () => {
    const d = one({ security: 0, pending: 0 }, { updateBlock: "nothing_to_do", state: OsUpdateState.UP_TO_DATE });
    await mount(actions(d), d);
    expect(disabled(button("Install updates…"))).toBe(true);
  });
});
