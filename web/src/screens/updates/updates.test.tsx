import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { BundleStatus, NodeUpdateState, RolloutStatus, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { SystemPage } from "@/screens/settings-pages/system";
import { UpdatesScreen } from "./index";

const getUpdates = vi.fn();
const startRollout = vi.fn();
const scheduleNodeUpdate = vi.fn();
const cancelNodeUpdateSchedule = vi.fn();
const setUpdateTimezone = vi.fn();
const pauseRollout = vi.fn();
const resumeRollout = vi.fn();
const cancelRollout = vi.fn();
const rollbackNode = vi.fn();
const rescanBundle = vi.fn();
const checkPanelUpdate = vi.fn();
const installPanelUpdate = vi.fn();
let role = Role.OWNER;
vi.mock("@/lib/api", () => ({
  updates: {
    getUpdates: (...a: unknown[]) => getUpdates(...a),
    startRollout: (...a: unknown[]) => startRollout(...a),
    scheduleNodeUpdate: (...a: unknown[]) => scheduleNodeUpdate(...a),
    cancelNodeUpdateSchedule: (...a: unknown[]) => cancelNodeUpdateSchedule(...a),
    setUpdateTimezone: (...a: unknown[]) => setUpdateTimezone(...a),
    pauseRollout: (...a: unknown[]) => pauseRollout(...a),
    resumeRollout: (...a: unknown[]) => resumeRollout(...a),
    cancelRollout: (...a: unknown[]) => cancelRollout(...a),
    rollbackNode: (...a: unknown[]) => rollbackNode(...a),
    rescanBundle: (...a: unknown[]) => rescanBundle(...a),
    checkPanelUpdate: (...a: unknown[]) => checkPanelUpdate(...a),
    installPanelUpdate: (...a: unknown[]) => installPanelUpdate(...a),
  },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
  isUnauthenticated: () => false,
  webauthnSupported: () => false,
}));
// the screen is tested without a router: a link is just an anchor here, with where it goes
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search, className, ...rest }: { children?: ReactNode; to: string; params?: object; search?: object; className?: string }) => (
    <a href={to} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(search ?? {})} className={className} aria-label={(rest as { "aria-label"?: string })["aria-label"]}>
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
  role = Role.OWNER;
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [getUpdates, startRollout, scheduleNodeUpdate, cancelNodeUpdateSchedule, setUpdateTimezone, pauseRollout, resumeRollout, cancelRollout, rollbackNode, rescanBundle, checkPanelUpdate, installPanelUpdate]) m.mockReset();
});

const node = (over: Record<string, unknown> = {}) => ({
  nodeId: "nod_1",
  name: "de1",
  version: "0.1.0-aaa",
  built: 100,
  supportsUpdate: true,
  crashGuard: true,
  state: NodeUpdateState.UP_TO_DATE,
  scheduledUnix: 0,
  scheduledVersion: "",
  scheduledBuilt: 0,
  scheduledTimezoneOffsetMinutes: 0,
  inbounds: 1,
  onlineUsers: 0,
  address: "de1.example.com",
  arch: "amd64",
  ...over,
});
const bundle = (over: Record<string, unknown> = {}) => ({
  status: BundleStatus.TRUSTED,
  version: "0.2.0-bbb",
  built: 200,
  expiresUnix: 0,
  files: [{ os: "linux", arch: "amd64", name: "mistgate-node-linux-amd64", size: 1048576, sha256: "abcdef0123456789abcdef" }],
  errorKey: "",
  params: {},
  scannedUnix: 0,
  ...over,
});
const step = (over: Record<string, unknown> = {}) => ({
  nodeId: "nod_1",
  nodeName: "de1",
  stage: 0,
  state: StepState.PENDING,
  fromVersion: "",
  fromBuilt: 0,
  startedUnix: 0,
  finishedUnix: 0,
  errorKey: "",
  params: {},
  ...over,
});
const rollout = (over: Record<string, unknown> = {}) => ({
  id: "rol_1",
  status: RolloutStatus.RUNNING,
  toVersion: "0.2.0-bbb",
  toBuilt: 200,
  batchSize: 1,
  createdUnix: 10,
  finishedUnix: 0,
  pauseKey: "",
  pauseParams: {},
  steps: [],
  ...over,
});
const page = (over: Record<string, unknown> = {}) => ({
  nowUnix: 1000,
  scheduleTimezoneOffsetMinutes: 180,
  panel: {
    version: "0.2.0-bbb", built: 200, hasReleaseKey: true, releaseKeyFingerprint: "abcd1234abcd1234",
    update: { version: "0.2.0-bbb", url: "https://github.com/Mistgate/mistgate/releases/tag/v0.2.0-bbb", publishedUnix: 0, checkedUnix: 1000, available: false, supported: true, installable: false, installing: false, errorKey: "" },
  },
  bundle: bundle(),
  nodes: [],
  distDir: "/var/lib/mistgate/dist",
  ...over,
});
const outdated = [
  node({ nodeId: "nod_1", name: "de1", state: NodeUpdateState.OUTDATED, onlineUsers: 5 }),
  node({ nodeId: "nod_2", name: "nl1", state: NodeUpdateState.OUTDATED, onlineUsers: 1 }),
];

async function mount(data: unknown, screen: ReactNode = <UpdatesScreen />) {
  getUpdates.mockResolvedValue(data);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        {screen}
      </QueryClientProvider>,
    ),
  );
  // the role and the page queries resolve
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => document.body.textContent ?? "";
const buttons = () => [...document.querySelectorAll("button")];
const button = (label: string) => buttons().find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));

describe("panel self-update", () => {
  it("lets an owner check GitHub and exposes an available release", async () => {
    checkPanelUpdate.mockResolvedValue({ update: {} });
    await mount(page({ panel: { ...page().panel, update: { version: "v0.3.0", url: "https://github.com/Mistgate/mistgate/releases/tag/v0.3.0", publishedUnix: 2000, checkedUnix: 1000, available: true, supported: true, installable: true, installing: false, errorKey: "" } } }));
    expect(text()).toContain("Version v0.3.0 is available");
    expect(button("Update panel")).toBeTruthy();
    await click(button("Check GitHub"));
    expect(checkPanelUpdate).toHaveBeenCalledTimes(1);
  });

  it("does not show the panel install action to a non-owner", async () => {
    role = Role.READONLY;
    await mount(page({ panel: { ...page().panel, update: { version: "v0.3.0", url: "https://github.com/Mistgate/mistgate/releases/tag/v0.3.0", publishedUnix: 2000, checkedUnix: 1000, available: true, supported: true, installable: true, installing: false, errorKey: "" } } }));
    expect(button("Update panel")).toBeUndefined();
    expect(text()).toContain("Version v0.3.0 is available");
  });

  it("shows the same update controls and an About card in Settings → System", async () => {
    await mount(
      page({
        panel: {
          ...page().panel,
          update: { version: "v0.3.0", url: "https://github.com/Mistgate/mistgate/releases/tag/v0.3.0", publishedUnix: 2000, checkedUnix: 1000, available: true, supported: true, installable: true, installing: false, errorKey: "" },
        },
      }),
      <SystemPage />,
    );
    expect(text()).toContain("About Mistgate");
    expect(text()).toContain("Panel version");
    expect(text()).toContain("Build date");
    expect(button("Update panel")).toBeTruthy();
    expect(text()).toContain("Node update schedule time zone");
    const timezone = document.querySelector<HTMLSelectElement>("select")!;
    expect(timezone.value).toBe("180");
    setUpdateTimezone.mockResolvedValue(true);
    await act(async () => {
      timezone.value = "240";
      timezone.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(setUpdateTimezone).toHaveBeenCalledWith({ timezoneOffsetMinutes: 240 });
    expect(document.querySelector('a[href="https://mistgate.app/"]')).toBeTruthy();
    expect(document.querySelector('a[href="https://github.com/Mistgate/mistgate"]')).toBeTruthy();
    expect(document.querySelector('a[href="https://github.com/Mistgate/mistgate/blob/main/LICENSE"]')).toBeTruthy();
  });
});
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");
const inDialog = (label: string) => [...(dialog()?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === label);
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));

describe("the Updates screen", () => {
  it("explains how to make a bundle while there is none, and offers no rollout", async () => {
    await mount(page({ bundle: bundle({ status: BundleStatus.MISSING, version: "", built: 0, files: [], errorKey: "updates.bundle.err.no_manifest" }), nodes: outdated }));
    expect(text()).toContain("Nothing to roll out yet");
    expect(text()).toContain("2 nodes are older than this panel");
    expect(text()).toContain("mistgate release sign");
    expect(text()).toContain("<data-dir>/dist");
    expect(button("Update all (canary first)")).toBeUndefined();
    // the bundle card does not repeat the hero's "no bundle" as an error
    expect(text()).not.toContain("No release bundle in the panel’s data directory");
    expect(button("Read the folder again")).toBeDefined();
  });

  it("says all is current when every node is", async () => {
    await mount(page({ nodes: [node({ state: NodeUpdateState.UP_TO_DATE })] }));
    expect(text()).toContain("All nodes are up to date");
    expect(text()).toContain("Every node runs the newest agent");
    expect(button("Update all (canary first)")).toBeUndefined();
    expect(text()).toContain("Signature verified");
  });

  it("shows update actions per node and never starts a bulk update", async () => {
    await mount(page({ nodes: outdated }));
    expect(text()).toContain("Node agent 0.2.0-bbb");
    expect(button("Update all (canary first)")).toBeUndefined();
    expect(document.querySelector("button[aria-label='Update de1']")).toBeDefined();
    expect(document.querySelector("button[aria-label='Update nl1']")).toBeDefined();
  });

  it("starts one node from its row with a dialog about that node only", async () => {
    startRollout.mockResolvedValue({});
    await mount(page({ nodes: outdated }));
    await click(document.querySelector("button[aria-label='Update de1']")!);
    expect(dialog()!.textContent).toContain("Update de1");
    expect(dialog()!.textContent).not.toContain("batches of");
    await click(inDialog("Update now"));
    await settle();
    expect(startRollout).toHaveBeenCalledWith({ nodeIds: ["nod_1"], batchSize: 0 });
  });

  it("adds a different node after the active rollout stage instead of blocking it", async () => {
    startRollout.mockResolvedValue({});
    const active = rollout({ steps: [step({ state: StepState.GATING })] });
    await mount(page({ nodes: outdated, rollout: active }));
    await click(document.querySelector("button[aria-label='Update nl1']")!);
    expect(dialog()!.textContent).toContain("added as the next stage");
    expect(inDialog("Add to rollout")?.disabled).toBe(false);
    await click(inDialog("Add to rollout"));
    await settle();
    expect(startRollout).toHaveBeenCalledWith({ nodeIds: ["nod_2"], batchSize: 0 });
  });

  it("explains that a node already queued in the active rollout will update in its stage", async () => {
    await mount(page({ nodes: outdated, rollout: rollout({ steps: [step({ nodeId: "nod_2", nodeName: "nl1" })] }) }));
    await click(document.querySelector("button[aria-label='Update nl1']")!);
    expect(dialog()!.textContent).toContain("already included in the active rollout");
    expect(inDialog("Update now")?.disabled).toBe(true);
  });

  it("schedules one offline node in the configured UTC+3 offset", async () => {
    scheduleNodeUpdate.mockResolvedValue({});
    const offline = node({ state: NodeUpdateState.OFFLINE });
    await mount(page({ nodes: [offline] }));
    await click(document.querySelector("button[aria-label='Update de1']")!);
    const modal = dialog()!;
    expect(modal.textContent).toContain("This node is offline");
    const later = modal.querySelector<HTMLInputElement>('input[name="node-update-mode"][value="schedule"]')!;
    await act(async () => later.click());
    const date = modal.querySelector<HTMLInputElement>("#node-update-at")!;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(date.constructor.prototype, "value")?.set;
      setter?.call(date, "3000-01-01T12:00");
      date.dispatchEvent(new Event("input", { bubbles: true }));
      date.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(modal.textContent).toContain("UTC+03:00");
    await click(inDialog("Save schedule"));
    await settle();
    expect(scheduleNodeUpdate).toHaveBeenCalledWith({
      nodeId: "nod_1",
      localDatetime: "3000-01-01T12:00",
      timezoneOffsetMinutes: 180,
      expectedVersion: "0.2.0-bbb",
      expectedBuilt: 200n,
    });
    expect(dialog()).toBeNull();
    expect(startRollout).not.toHaveBeenCalled();
  });

  it("keeps the dialog open when the panel refuses", async () => {
    startRollout.mockRejectedValue(new ConnectError("a rollout is already active", Code.FailedPrecondition));
    await mount(page({ nodes: outdated }));
    await click(document.querySelector("button[aria-label='Update de1']")!);
    await click(inDialog("Update now"));
    await settle();
    expect(startRollout).toHaveBeenCalledTimes(1);
    expect(dialog()).not.toBeNull();
  });

  it("hides every button from a helper and says why", async () => {
    role = Role.HELPER;
    await mount(page({ nodes: [...outdated, node({ nodeId: "nod_3", name: "fi1", lastUpdate: { outcome: "ok", fromVersion: "0.0.9", toVersion: "0.1.0", atUnix: 5 } })] }));
    expect(text()).toContain("Node agent 0.2.0-bbb");
    expect(text()).toContain("Only the owner can update nodes, set schedules, and manage rollouts");
    expect(button("Update all (canary first)")).toBeUndefined();
    expect(button("Update")).toBeUndefined();
    expect(button("Roll back")).toBeUndefined();
    expect(button("Read the folder again")).toBeUndefined();
  });

  it("shows a running rollout by stages with its progress, and pauses it", async () => {
    pauseRollout.mockResolvedValue({});
    await mount(
      page({
        nodes: [node({ nodeId: "nod_1", name: "nl1", state: NodeUpdateState.UP_TO_DATE }), node({ nodeId: "nod_2", name: "de1", state: NodeUpdateState.UPDATING })],
        rollout: rollout({
          steps: [
            step({ nodeId: "nod_1", nodeName: "nl1", stage: 0, state: StepState.PASSED }),
            step({ nodeId: "nod_2", nodeName: "de1", stage: 1, state: StepState.GATING }),
            step({ nodeId: "nod_3", nodeName: "fi1", stage: 1, state: StepState.PENDING }),
          ],
        }),
      }),
    );
    expect(text()).toContain("Updating the fleet to 0.2.0-bbb");
    expect(text()).toContain("1 of 3 nodes updated");
    expect(text()).toContain("Canary");
    expect(text()).toContain("Batch 1");
    expect(text()).toContain("2 nodes together");
    expect(text()).toContain("checking");
    expect(text()).toContain("waiting");
    expect(button("Update all (canary first)")).toBeUndefined();
    expect(document.querySelector("[role=progressbar]")?.getAttribute("aria-valuenow")).toBe("1");

    await click(button("Pause"));
    expect(pauseRollout).toHaveBeenCalledWith({ rolloutId: "rol_1" });
  });

  it("asks before cancelling a rollout", async () => {
    cancelRollout.mockResolvedValue({});
    await mount(page({ nodes: [node({ state: NodeUpdateState.UPDATING })], rollout: rollout({ steps: [step({ state: StepState.SENT })] }) }));
    await click(button("Cancel rollout"));
    expect(dialog()!.textContent).toContain("Cancel the rollout?");
    expect(cancelRollout).not.toHaveBeenCalled();
    await click(inDialog("Cancel rollout"));
    await settle();
    expect(cancelRollout).toHaveBeenCalledWith({ rolloutId: "rol_1" });
  });

  it("tells why a rollout paused and which node failed, leads to that node, and does not push to resume", async () => {
    resumeRollout.mockResolvedValue({});
    await mount(
      page({
        nodes: [node({ nodeId: "nod_2", name: "de1", state: NodeUpdateState.ROLLED_BACK, lastUpdate: { outcome: "rolled_back", fromVersion: "0.1.0", toVersion: "0.2.0", reason: "gate_probe_failed", atUnix: 5 } })],
        rollout: rollout({
          status: RolloutStatus.PAUSED,
          pauseKey: "updates.pause.gate_failed",
          pauseParams: { node: "de1", reason: "probe_failed" },
          steps: [step({ nodeId: "nod_2", nodeName: "de1", state: StepState.ROLLED_BACK, errorKey: "updates.step.err.probe_failed" })],
        }),
      }),
    );
    expect(text()).toContain("The update is paused");
    expect(text()).toContain("de1 did not pass the check after updating: the client-eye check failed. Where possible");
    expect(text()).toContain("rolled back");
    expect(text()).toContain("Rolled back from 0.2.0");
    // the panel's own rollback is not "rolled back by you"
    expect(text()).toContain("The panel put the previous version back: the client-eye check failed after the update.");
    expect(text()).not.toContain("Rolled back by you");
    // first: look at the node (its events); "Resume" is there but not the main button
    const open = [...document.querySelectorAll("a")].find((a) => a.textContent === "Open de1")!;
    expect(open.dataset.params).toBe(JSON.stringify({ id: "nod_2" }));
    expect(open.dataset.search).toBe(JSON.stringify({ tab: "events" }));
    expect(button("Resume")!.className).not.toContain("bg-accent");
    // the node's name in the rollout steps leads to it too
    expect([...document.querySelectorAll("a")].some((a) => a.textContent === "de1" && a.dataset.params === JSON.stringify({ id: "nod_2" }))).toBe(true);
    await click(button("Resume"));
    expect(resumeRollout).toHaveBeenCalledWith({ rolloutId: "rol_1" });
  });

  it("offers Resume as the main button after the owner's own pause", async () => {
    await mount(page({ nodes: [node()], rollout: rollout({ status: RolloutStatus.PAUSED, pauseKey: "updates.pause.owner", steps: [step({ state: StepState.PASSED })] }) }));
    expect(button("Resume")!.className).toContain("bg-accent");
    expect([...document.querySelectorAll("a")].some((a) => a.textContent?.startsWith("Open "))).toBe(false);
  });

  it("marks the stage of nodes that were passed over with a dash, not a tick", async () => {
    await mount(
      page({
        nodes: [node()],
        rollout: rollout({
          status: RolloutStatus.DONE,
          steps: [step({ state: StepState.PASSED }), step({ nodeId: "nod_2", nodeName: "de2", stage: 1, state: StepState.SKIPPED, errorKey: "updates.step.err.offline" })],
        }),
      }),
    );
    const marks = [...document.querySelectorAll("section span.rounded-full.size-7")].map((s) => s.textContent);
    expect(marks).toEqual(["✓", "–"]);
  });

  it("keeps the last finished rollout on the page without making it the hero", async () => {
    await mount(page({ nodes: [node()], rollout: rollout({ status: RolloutStatus.DONE, finishedUnix: 50, steps: [step({ state: StepState.PASSED })] }) }));
    expect(text()).toContain("All nodes are up to date");
    expect(text()).toContain("The last rollout");
    expect(text()).toContain("Done");
    expect(button("Pause")).toBeUndefined();
  });

  it("rolls a node back after a confirmation that names the version it returns to", async () => {
    rollbackNode.mockResolvedValue({});
    await mount(page({ nodes: [node({ lastUpdate: { outcome: "ok", fromVersion: "0.0.9", toVersion: "0.1.0", atUnix: 5 } })] }));
    expect(text()).toContain("0.0.9 → 0.1.0");
    await click(document.querySelector("button[aria-label='Roll back de1']")!);
    expect(dialog()!.textContent).toContain("Roll back de1?");
    expect(dialog()!.textContent).toContain("(0.0.9)");
    await click(inDialog("Roll back"));
    await settle();
    expect(rollbackNode).toHaveBeenCalledWith({ nodeId: "nod_1" });
  });

  it("marks nodes that must be updated by hand and nodes without the crash guard, by name", async () => {
    await mount(page({ nodes: [node({ nodeId: "nod_1", name: "old1", state: NodeUpdateState.UNSUPPORTED, supportsUpdate: false }), node({ nodeId: "nod_2", name: "de1", crashGuard: false })] }));
    expect(text()).toContain("Update by hand");
    expect(text()).toContain("Marked “Update by hand”: old1.");
    expect(text()).toContain("No guard against repeated crashes: de1.");
    expect(document.querySelector("button[aria-label='Update old1']")).toBeNull();
  });

  it("says a node waits for a manual update instead of 'all up to date', and gives the two commands to copy", async () => {
    await mount(page({ nodes: [node(), node({ nodeId: "nod_9", name: "de2", state: NodeUpdateState.UNSUPPORTED, supportsUpdate: false, address: "de2.example.com", arch: "arm64" })], bundle: bundle({ files: [{ os: "linux", arch: "arm64", name: "mistgate-node-linux-arm64", size: 1, sha256: "ab" }] }) }));
    expect(text()).toContain("MANUAL STEP NEEDED");
    expect(text()).toContain("1 node waits for a manual update: de2");
    expect(text()).toContain("Update by hand: de2"); // the page's subtitle
    expect(text()).not.toContain("All nodes are up to date");
    const toggles = buttons().filter((b) => b.textContent === "How to update");
    expect(toggles.length).toBeGreaterThan(0);
    expect(document.querySelector("code")).toBeNull();
    await click(toggles[0]);
    const codes = [...document.querySelectorAll("code")].map((c) => c.textContent);
    expect(codes).toContain("scp /var/lib/mistgate/dist/mistgate-node-linux-arm64 root@de2.example.com:/root/mistgate-node");
    expect(codes).toContain("ssh root@de2.example.com 'chmod +x /root/mistgate-node && /root/mistgate-node install'");
    expect(toggles[0]!.getAttribute("aria-expanded")).toBe("true");
    expect(buttons().filter((b) => b.textContent === "Copy").length).toBeGreaterThanOrEqual(2);
  });

  it("shows a bundle the panel does not trust, with the reason and the file", async () => {
    await mount(
      page({
        bundle: bundle({ status: BundleStatus.UNTRUSTED, errorKey: "updates.bundle.err.file_mismatch", params: { file: "mistgate-node-linux-amd64" } }),
        nodes: outdated,
      }),
    );
    expect(text()).toContain("The release bundle cannot be used");
    expect(text()).toContain("mistgate-node-linux-amd64 differs from the manifest");
    expect(text()).toContain("Failed the check");
    expect(button("Update all (canary first)")).toBeUndefined();
    // There is no node action until a trusted newer bundle is available.
    expect(document.querySelector("button[aria-label='Update de1']")).toBeNull();
  });

  it("names a bad signature in the bundle's pill", async () => {
    await mount(page({ bundle: bundle({ status: BundleStatus.UNTRUSTED, errorKey: "updates.bundle.err.bad_signature" }), nodes: outdated }));
    expect(text()).toContain("Signature does not match");
  });

  it("rescans the bundle on the owner's request", async () => {
    rescanBundle.mockResolvedValue({});
    await mount(page({ nodes: [node()] }));
    await click(button("Read the folder again"));
    await settle();
    expect(rescanBundle).toHaveBeenCalledTimes(1);
  });
});
