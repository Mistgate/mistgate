import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { BundleStatus, NodeUpdateState, RolloutStatus, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { SystemPage } from "@/screens/settings-pages/system";
import { setEffectsEnabled } from "@/lib/effects";
import { UpdatesScreen } from "./index";

const getUpdates = vi.fn();
const startRollout = vi.fn();
const scheduleNodeUpdate = vi.fn();
const cancelNodeUpdateSchedule = vi.fn();
const setUpdateTimezone = vi.fn();
const setUpdateDownloadSources = vi.fn();
const pauseRollout = vi.fn();
const resumeRollout = vi.fn();
const cancelRollout = vi.fn();
const rollbackNode = vi.fn();
const rescanBundle = vi.fn();
const checkPanelUpdate = vi.fn();
const installPanelUpdate = vi.fn();
let role = Role.OWNER;
/** The edition fields of Me: none on the VPS fixtures (an older panel says nothing either), the edge's in its own tests. */
let edition: { edition?: string; unavailable?: string[] } = {};
vi.mock("@/lib/api", () => ({
  updates: {
    getUpdates: (...a: unknown[]) => getUpdates(...a),
    startRollout: (...a: unknown[]) => startRollout(...a),
    scheduleNodeUpdate: (...a: unknown[]) => scheduleNodeUpdate(...a),
    cancelNodeUpdateSchedule: (...a: unknown[]) => cancelNodeUpdateSchedule(...a),
    setUpdateTimezone: (...a: unknown[]) => setUpdateTimezone(...a),
    setUpdateDownloadSources: (...a: unknown[]) => setUpdateDownloadSources(...a),
    pauseRollout: (...a: unknown[]) => pauseRollout(...a),
    resumeRollout: (...a: unknown[]) => resumeRollout(...a),
    cancelRollout: (...a: unknown[]) => cancelRollout(...a),
    rollbackNode: (...a: unknown[]) => rollbackNode(...a),
    rescanBundle: (...a: unknown[]) => rescanBundle(...a),
    checkPanelUpdate: (...a: unknown[]) => checkPanelUpdate(...a),
    installPanelUpdate: (...a: unknown[]) => installPanelUpdate(...a),
  },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role }, ...edition }) },
  isUnauthenticated: () => false,
  webauthnSupported: () => false,
}));
// the screen is tested without a router: a link is just an anchor here, with where it goes
const route = vi.hoisted(() => ({ search: {} as { tab?: string }, navigate: vi.fn() }));
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => route.navigate,
  useSearch: () => route.search,
  Link: ({ children, to, params, search, className, ...rest }: { children?: ReactNode; to: string; params?: object; search?: object; className?: string }) => (
    <a href={to} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(search ?? {})} className={className} aria-label={(rest as { "aria-label"?: string })["aria-label"]}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

// these read what the screen says: the numbers must not be mid-shuffle (the motion has its own tests)
beforeEach(() => setEffectsEnabled(false));

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  role = Role.OWNER;
  edition = {};
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  route.search = {};
  for (const m of [getUpdates, startRollout, scheduleNodeUpdate, cancelNodeUpdateSchedule, setUpdateTimezone, setUpdateDownloadSources, pauseRollout, resumeRollout, cancelRollout, rollbackNode, rescanBundle, checkPanelUpdate, installPanelUpdate]) m.mockReset();
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
  scheduledMissed: false,
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
  downloadSource: "",
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
  updateDownloadTimeoutSeconds: 1800,
  updateDownloadSources: ["github"],
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
/** The page's one button that updates the rest of the fleet, whatever the count. */
const bulk = () => buttons().find((b) => /^Update \d+ nodes?$/.test(b.textContent?.trim() ?? ""));
const click = (b: Element | null | undefined) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));

describe("panel self-update", () => {
  it("lets an owner check GitHub and exposes an available release", async () => {
    checkPanelUpdate.mockResolvedValue({ update: {} });
    await mount(page({ panel: { ...page().panel, update: { version: "v0.3.0", url: "https://github.com/Mistgate/mistgate/releases/tag/v0.3.0", publishedUnix: 2000, checkedUnix: 1000, available: true, supported: true, installable: true, installing: false, errorKey: "" } } }));
    expect(text()).toContain("Available");
    expect(document.querySelector(".uq-v.new")?.textContent).toContain("v0.3.0");
    expect(button("Update panel")).toBeTruthy();
    await click(button("Check GitHub"));
    expect(checkPanelUpdate).toHaveBeenCalledTimes(1);
  });

  it("installs exactly the signed release the owner sees", async () => {
    installPanelUpdate.mockResolvedValue({ update: {} });
    const sha = "ab".repeat(32);
    await mount(page({ panel: { ...page().panel, update: { version: "v0.3.0", url: "", publishedUnix: 2000, checkedUnix: 1000, built: 1500, sha256: sha, available: true, supported: true, installable: true, installing: false, errorKey: "" } } }));
    // the SHA-256 is a detail of the panel's card: folded until asked for
    expect(text()).not.toContain("abababababab");
    await click(document.querySelector("button[aria-controls='uq-det']"));
    expect(text()).toContain("abababababab");
    await click(button("Update panel"));
    await settle();
    // the window first: what happens and a backup; nothing is installed before Update in it
    expect(installPanelUpdate).not.toHaveBeenCalled();
    const dialog = document.querySelector<HTMLElement>("[role=dialog]")!;
    expect(dialog.textContent).toContain("Update the panel to v0.3.0");
    expect(dialog.textContent).toContain("If the new version does not start, the panel goes back to this one by itself.");
    expect(dialog.textContent).toContain("File on this computer");
    await click([...dialog.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Update"));
    await settle();
    expect(installPanelUpdate).toHaveBeenCalledWith({ expectedVersion: "v0.3.0", expectedSha256: sha });
  });

  it("says a release without a valid panel signature cannot be installed", async () => {
    await mount(page({ panel: { ...page().panel, update: { version: "v0.3.0", url: "", publishedUnix: 2000, checkedUnix: 1000, built: 0, sha256: "", available: false, supported: true, installable: false, installing: false, errorKey: "unsigned" } } }));
    expect(text()).toContain("Release not installed: the signature didn’t match");
    expect(text()).toContain("The manifest of v0.3.0 is signed with a different key than the one built into the panel. The panel stayed on 0.2.0-bbb, nothing changed");
    expect(button("Update panel")).toBeUndefined();
  });

  it("shows a channel the root update policy does not allow as a state with the command, and no install button", async () => {
    await mount(page({ updateChannel: "testing", panel: { ...page().panel, update: { version: "v0.3.0-testing.1", url: "", publishedUnix: 2000, checkedUnix: 1000, built: 0, sha256: "", available: true, supported: true, installable: false, installing: false, errorKey: "channel_not_allowed" } } }));
    expect(text()).toContain("Channel testing not allowed");
    expect(text()).toContain("/etc/mistgate/panel-update-channel");
    expect(text()).toContain('printf "%s\\n" testing');
    expect(button("Update panel")).toBeUndefined();
  });

  it("does not show the panel install action to a non-owner", async () => {
    role = Role.READONLY;
    await mount(page({ panel: { ...page().panel, update: { version: "v0.3.0", url: "https://github.com/Mistgate/mistgate/releases/tag/v0.3.0", publishedUnix: 2000, checkedUnix: 1000, available: true, supported: true, installable: true, installing: false, errorKey: "" } } }));
    expect(button("Update panel")).toBeUndefined();
    expect(text()).toContain("Available");
    expect(text()).toContain("Only the owner can update the panel");
  });

  it("shows the build, the latest release and a way to the Updates page, the About card and the time zone in Settings → System", async () => {
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
    expect(text()).toContain("This panel");
    expect(text()).toContain("Latest release");
    expect(text()).toContain("v0.3.0");
    // the update itself is on the Updates page: Settings only points to it
    expect(button("Update panel")).toBeUndefined();
    expect(document.querySelector('a[href="/updates"]')?.textContent).toBe("Open updates");
    expect(text()).toContain("Time zone for new schedules");
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

describe("Settings → System → download sources", () => {
  const sw = () => document.querySelector<HTMLElement>('[role=switch][aria-label="GitHub as a source"]');
  async function remount(data: unknown) {
    await act(async () => root!.unmount());
    host!.remove();
    await mount(data, <SystemPage />);
  }

  it("shows the panel as always first, without a switch, and GitHub on or off as the panel reports", async () => {
    await mount(page(), <SystemPage />);
    expect(text()).toContain("Download sources");
    expect(text()).toContain("Always first. It can’t be turned off");
    expect(text()).toContain("always on");
    expect(text()).toContain("Needed only if downloading from the panel failed or it’s busy.");
    expect(text()).toContain("GitHub will see the addresses of nodes that download the agent.");
    expect(text()).toContain("signed with this panel’s key");
    // one switch, for GitHub; the panel row has none
    expect(document.querySelectorAll("[role=switch]")).toHaveLength(1);
    expect(sw()?.getAttribute("aria-checked")).toBe("true");
    await remount(page({ updateDownloadSources: [] }));
    expect(sw()?.getAttribute("aria-checked")).toBe("false");
  });

  it("turns GitHub off and on by sending the whole list of optional sources", async () => {
    setUpdateDownloadSources.mockResolvedValue({ sources: [] });
    await mount(page(), <SystemPage />);
    await click(sw());
    expect(setUpdateDownloadSources).toHaveBeenLastCalledWith({ sources: [] });
    await remount(page({ updateDownloadSources: [] }));
    await click(sw());
    expect(setUpdateDownloadSources).toHaveBeenLastCalledWith({ sources: ["github"] });
  });

  it("gives a non-owner the switch as it is, held shut", async () => {
    role = Role.READONLY;
    await mount(page(), <SystemPage />);
    expect(text()).toContain("Download sources");
    expect(sw()?.getAttribute("aria-checked")).toBe("true");
    expect(sw()?.hasAttribute("data-disabled")).toBe(true);
    await click(sw());
    expect(setUpdateDownloadSources).not.toHaveBeenCalled();
  });

  it("is not drawn on the Cloudflare edition, where the call is not implemented", async () => {
    edition = { edition: "edge", unavailable: ["backups"] };
    await mount(page({ updateDownloadSources: [] }), <SystemPage />);
    expect(text()).toContain("Maximum download time");
    expect(text()).not.toContain("Download sources");
    expect(document.querySelector("[role=switch]")).toBeNull();
  });
});
/** Base UI opens a menu on the press, not on the click. */
async function press(el: Element | null | undefined) {
  expect(el).toBeTruthy();
  await act(async () => {
    for (const type of ["pointerdown", "mousedown"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
    for (const type of ["pointerup", "mouseup", "click"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
  });
  await settle();
}
const menuItem = (label: string) => [...document.querySelectorAll('[role="menuitem"]')].find((i) => i.textContent === label);
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
    expect(bulk()).toBeUndefined();
    // the bundle card does not repeat the hero's "no bundle" as an error
    expect(text()).not.toContain("No release bundle in the panel’s data directory");
    expect(button("Read the folder again")).toBeDefined();
  });

  it("says all is current when every node is", async () => {
    await mount(page({ nodes: [node({ state: NodeUpdateState.UP_TO_DATE })] }));
    expect(text()).toContain("1 of 1 node on 0.2.0-bbb");
    expect(text()).toContain("The node agent 0.2.0-bbb is on every node");
    expect(text()).toContain("All up to date");
    expect(bulk()).toBeUndefined();
    expect(text()).toContain("Signature verified");
  });

  it("counts a node as up to date only when it said so: offline nodes are named apart", async () => {
    const off = (id: string, name: string) => node({ nodeId: id, name, state: NodeUpdateState.OFFLINE });
    await mount(page({ nodes: [...outdated, off("nod_3", "fi1"), off("nod_4", "se1"), off("nod_5", "lv1")] }));
    expect(text()).toContain("0 of 5 nodes on 0.2.0-bbb");
    expect(text()).not.toContain("3 of 5 nodes on");
    // the offline ones are named apart, with what happens to them
    expect(text()).toContain("fi1, se1, lv1 are offline");
    expect(text()).toContain("A schedule runs when the nodes are back");
  });

  it("says how far the fleet is and leads with one button that updates the rest, plus one button per node", async () => {
    await mount(page({ nodes: [...outdated, node({ nodeId: "nod_3", name: "fi1" }), node({ nodeId: "nod_4", name: "se1" })] }));
    expect(text()).toContain("2 of 4 nodes on 0.2.0-bbb");
    expect(text()).toContain("Node agent 0.2.0-bbb. One node goes first (the canary)");
    expect(button("Update 2 nodes")).toBeDefined();
    expect(button("Update 2 nodes")!.className).toContain("bg-accent"); // the one primary button of the page
    expect(document.querySelector("button[aria-label='Update de1']")).not.toBeNull();
    expect(document.querySelector("button[aria-label='Update nl1']")).not.toBeNull();
    // the other rows are done: no update button
    expect(document.querySelector("button[aria-label='Update fi1']")).toBeNull();
    // the fleet in one look: a card per node, in the colour of where it stands; the canary (the quietest) leads
    const cards = [...document.querySelectorAll(".uq-ft")];
    expect(cards).toHaveLength(4);
    expect(cards.filter((c) => c.classList.contains("part"))).toHaveLength(2);
    expect(cards.filter((c) => c.classList.contains("ok"))).toHaveLength(2);
    expect(cards[0]!.classList.contains("can")).toBe(true);
    expect(cards[0]!.textContent).toContain("nl1");
    expect(cards[0]!.textContent).toContain("canary");
    expect(cards[0]!.textContent).toContain("can be updated to 0.2.0-bbb");
  });

  it("starts a rollout of every node that can be updated, the canary named, after a window that says what happens", async () => {
    startRollout.mockResolvedValue({});
    await mount(page({ nodes: outdated }));
    expect(startRollout).not.toHaveBeenCalled();
    await click(button("Update 2 nodes"));
    const d = dialog()!;
    expect(d.textContent).toContain("Update to 0.2.0-bbb");
    expect(d.textContent).toContain("The canary goes first and is checked for 5 minutes. Then the rest, 1 at a time.");
    expect(d.textContent).toContain("A node that fails its checks after the update gets its previous version back");
    // nl1 has the fewest people online: it is the canary
    const rows = [...d.querySelectorAll("li")];
    expect(rows.map((r) => r.textContent)).toEqual(["de15 online", "nl1Canary1 online"]);
    expect(startRollout).not.toHaveBeenCalled();
    await click(inDialog("Start rollout"));
    await settle();
    expect(startRollout).toHaveBeenCalledWith({ nodeIds: ["nod_1", "nod_2"], batchSize: 0, expectedVersion: "0.2.0-bbb", expectedBuilt: 200n });
    expect(dialog()).toBeNull();
  });

  it("lets the owner leave a node out of the rollout, and moves the canary to the quietest of the rest", async () => {
    startRollout.mockResolvedValue({});
    await mount(page({ nodes: outdated }));
    await click(button("Update 2 nodes"));
    const boxes = [...dialog()!.querySelectorAll<HTMLInputElement>("input[type=checkbox]")];
    await click(boxes[1]); // nl1 out
    expect([...dialog()!.querySelectorAll("li")].map((r) => r.textContent)).toEqual(["de1Canary5 online", "nl11 online"]);
    await click(inDialog("Start rollout"));
    await settle();
    expect(startRollout).toHaveBeenCalledWith({ nodeIds: ["nod_1"], batchSize: 0, expectedVersion: "0.2.0-bbb", expectedBuilt: 200n });
  });

  it("will not start a rollout of no node", async () => {
    await mount(page({ nodes: outdated }));
    await click(button("Update 2 nodes"));
    for (const box of dialog()!.querySelectorAll<HTMLInputElement>("input[type=checkbox]")) await click(box);
    expect(inDialog("Start rollout")!.disabled).toBe(true);
  });

  it("keeps the dialog open when the panel refuses a rollout of the fleet", async () => {
    startRollout.mockRejectedValue(new ConnectError("the update bundle changed; review the new version", Code.FailedPrecondition));
    await mount(page({ nodes: outdated }));
    await click(button("Update 2 nodes"));
    await click(inDialog("Start rollout"));
    await settle();
    expect(startRollout).toHaveBeenCalledTimes(1);
    expect(dialog()).not.toBeNull();
  });

  it("offers the rollout of the fleet only when one can start: not while one runs, not without a trusted bundle, not to a helper", async () => {
    await mount(page({ nodes: outdated, rollout: rollout({ steps: [step({ state: StepState.GATING })] }) }));
    expect(button("Update 2 nodes")).toBeUndefined();
    act(() => root?.unmount());
    host?.remove();
    await mount(page({ nodes: outdated, bundle: bundle({ status: BundleStatus.UNTRUSTED, errorKey: "updates.bundle.err.bad_signature" }) }));
    expect(button("Update 2 nodes")).toBeUndefined();
    act(() => root?.unmount());
    host?.remove();
    role = Role.HELPER;
    await mount(page({ nodes: outdated }));
    expect(button("Update 2 nodes")).toBeUndefined();
  });

  it("opens the window of one node from its Schedule button, with the two ways to update it", async () => {
    await mount(page({ nodes: outdated }));
    expect(menuItem("Schedule…")).toBeUndefined();
    // nothing to roll back yet: no menu on the row
    expect(document.querySelector("button[aria-label='More actions for de1']")).toBeNull();
    const row = document.querySelector("button[aria-label='Update de1']")!;
    expect(row.textContent).toBe("Schedule");
    await click(row);
    expect(dialog()!.textContent).toContain("Update de1");
    const choices = [...dialog()!.querySelectorAll("[role=radio]")];
    expect(choices.map((c) => c.getAttribute("aria-checked"))).toEqual(["true", "false"]);
    expect(choices[1]!.textContent).toContain("Choose a date and time");
  });

  it("keeps a row without a second action free of the menu", async () => {
    await mount(page({ nodes: [node()] }));
    expect(document.querySelector("button[aria-label='More actions for de1']")).toBeNull();
  });

  it("starts one node from its row with a dialog about that node only", async () => {
    startRollout.mockResolvedValue({});
    await mount(page({ nodes: outdated }));
    await click(document.querySelector("button[aria-label='Update de1']")!);
    expect(dialog()!.textContent).toContain("Update de1");
    expect(dialog()!.textContent).not.toContain("batches of");
    await click(inDialog("Update now"));
    await settle();
    expect(startRollout).toHaveBeenCalledWith({ nodeIds: ["nod_1"], batchSize: 0, expectedVersion: "0.2.0-bbb", expectedBuilt: 200n });
  });

  it("marks a schedule that missed its window", async () => {
    await mount(page({ nodes: [node({ state: NodeUpdateState.OFFLINE, scheduledUnix: 1_800_000_000, scheduledVersion: "0.2.0-bbb", scheduledBuilt: 200, scheduledMissed: true })] }));
    expect(text()).toContain("Missed 0.2.0-bbb · 2027-01-15 08:00 UTC+00:00");
    expect(text()).toContain("will not start by itself");
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
    expect(startRollout).toHaveBeenCalledWith({ nodeIds: ["nod_2"], batchSize: 0, expectedVersion: "0.2.0-bbb", expectedBuilt: 200n });
  });

  it("schedules one offline node in the configured UTC+3 offset", async () => {
    scheduleNodeUpdate.mockResolvedValue({});
    const offline = node({ state: NodeUpdateState.OFFLINE });
    await mount(page({ nodes: [offline] }));
    await click(document.querySelector("button[aria-label='Update de1']")!);
    const modal = dialog()!;
    expect(modal.textContent).toContain("This node is offline");
    const later = modal.querySelectorAll<HTMLElement>("[role=radio]")[1]!;
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
    expect(text()).toContain("Only the owner can update agents");
    expect(bulk()).toBeUndefined();
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
    // what the rollout does this moment, and how far it got
    expect(document.querySelector(".uq-now")?.textContent).toContain("Checking de1 for 5 minutes…");
    expect(document.querySelector(".uq-now")?.textContent).toContain("1 of 3 nodes updated");
    expect(text()).toContain("1 of 2 nodes on 0.2.0-bbb");
    expect(text()).toContain("Canary");
    expect(text()).toContain("Batch 1");
    expect(text()).toContain("checking");
    expect(text()).toContain("waiting");
    expect(bulk()).toBeUndefined();

    await click(button("Pause"));
    expect(pauseRollout).toHaveBeenCalledWith({ rolloutId: "rol_1" });
  });

  it("asks before cancelling a rollout", async () => {
    cancelRollout.mockResolvedValue({});
    await mount(page({ nodes: [node({ state: NodeUpdateState.UPDATING })], rollout: rollout({ steps: [step({ state: StepState.SENT })] }) }));
    await click(button("Stop"));
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
    expect(open.dataset.search).toBe(JSON.stringify({ tab: "history" }));
    expect(button("Resume")!.className).not.toContain("bg-accent");
    expect(document.querySelector(".rc-pb.bad")).not.toBeNull();
    // the node's name in the rollout steps leads to it too
    expect([...document.querySelectorAll("a")].some((a) => a.textContent === "de1" && a.dataset.params === JSON.stringify({ id: "nod_2" }))).toBe(true);
    await click(button("Resume"));
    expect(resumeRollout).toHaveBeenCalledWith({ rolloutId: "rol_1" });
  });

  it("offers Resume as the main button after the owner's own pause", async () => {
    await mount(page({ nodes: [node()], rollout: rollout({ status: RolloutStatus.PAUSED, pauseKey: "updates.pause.owner", steps: [step({ state: StepState.PASSED })] }) }));
    expect(button("Resume")).toBeTruthy();
    // the owner's own pause is amber, not the red of a node that failed
    expect(document.querySelector(".rc-pb")?.classList.contains("bad")).toBe(false);
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
    // a finished rollout folds to its header; its stages are one click away
    expect(document.querySelector(".uq-ls")).toBeNull();
    await click(button("Show stages"));
    expect(text()).toContain("Skipped");
    const rows = [...document.querySelectorAll(".uq-ls > div")].map((r) => r.textContent);
    // the node that was passed over says "skipped", never the "updated" of an update that did not happen
    expect(rows[0]).toBe("de1Canaryupdated · 0.2.0-bbb");
    expect(rows[1]).toContain("de2Skippedskipped");
    expect(text()).toContain("Skipped: the node was offline.");
  });

  it("keeps the last finished rollout on the page without making it the hero", async () => {
    await mount(page({ nodes: [node()], rollout: rollout({ status: RolloutStatus.DONE, finishedUnix: 50, steps: [step({ state: StepState.PASSED })] }) }));
    expect(text()).toContain("All up to date");
    expect(text()).toContain("The last rollout");
    expect(text()).toContain("Done");
    expect(button("Pause")).toBeUndefined();
  });

  it("rolls a node back after a confirmation that names the version it returns to", async () => {
    rollbackNode.mockResolvedValue({});
    await mount(page({ nodes: [node({ lastUpdate: { outcome: "ok", fromVersion: "0.0.9", toVersion: "0.1.0", atUnix: 5 } })] }));
    expect(text()).toContain("0.0.9 → 0.1.0");
    // it is not a button of the row: it sits behind the menu, and still asks before it does anything
    expect(document.querySelector("button[aria-label='Roll back de1']")).toBeNull();
    await press(document.querySelector("button[aria-label='More actions for de1']"));
    await click(menuItem("Roll back…"));
    expect(dialog()!.textContent).toContain("Roll back de1?");
    expect(dialog()!.textContent).toContain("(0.0.9)");
    expect(rollbackNode).not.toHaveBeenCalled();
    await click(inDialog("Roll back"));
    await settle();
    expect(rollbackNode).toHaveBeenCalledWith({ nodeId: "nod_1" });
  });

  it("marks nodes that must be updated by hand and nodes without the crash guard, by name", async () => {
    await mount(page({ nodes: [node({ nodeId: "nod_1", name: "old1", state: NodeUpdateState.UNSUPPORTED, supportsUpdate: false }), node({ nodeId: "nod_2", name: "de1", crashGuard: false })] }));
    expect(text()).toContain("Update by hand");
    expect(text()).toContain("No guard against repeated crashes: de1.");
    expect(document.querySelector("button[aria-label='Update old1']")).toBeNull();
  });

  it("says a node waits for a manual update instead of 'all up to date', and gives the two commands to copy", async () => {
    await mount(page({ nodes: [node(), node({ nodeId: "nod_9", name: "de2", state: NodeUpdateState.UNSUPPORTED, supportsUpdate: false, address: "de2.example.com", arch: "arm64" })], bundle: bundle({ files: [{ os: "linux", arch: "arm64", name: "mistgate-node-linux-arm64", size: 1, sha256: "ab" }] }) }));
    expect(text()).toContain("1 node waits for a manual update: de2");
    expect(text()).toContain("Update by hand: de2"); // the page's subtitle
    expect(text()).not.toContain("All up to date");
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
    expect(bulk()).toBeUndefined();
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

describe("the folds of the Updates screen", () => {
  const detailsOf = (id: string) => document.querySelector<HTMLButtonElement>(`button[aria-controls='${id}']`);

  it("keeps the release bundle's details folded while the panel trusts it, with the verdict and the dates in one line", async () => {
    await mount(page({ nodes: [node()], bundle: bundle({ expiresUnix: 1_800_000_000, scannedUnix: 990 }) }));
    expect(text()).toContain("Signature verified");
    expect(text()).toContain("0.2.0-bbb");
    expect(text()).toContain("valid until");
    expect(text()).not.toContain("mistgate-node-linux-amd64"); // the files
    expect(text()).not.toContain("The panel checks GitHub every 10 minutes"); // the long note
    expect(detailsOf("up-bundle-details")!.getAttribute("aria-expanded")).toBe("false");
    await click(detailsOf("up-bundle-details"));
    expect(detailsOf("up-bundle-details")!.getAttribute("aria-expanded")).toBe("true");
    expect(text()).toContain("mistgate-node-linux-amd64");
    expect(text()).toContain("The panel checks GitHub every 10 minutes");
  });

  it("opens the bundle's details by itself when it is not trusted: that is where the reason and the fix are", async () => {
    await mount(page({ nodes: outdated, bundle: bundle({ status: BundleStatus.UNTRUSTED, errorKey: "updates.bundle.err.file_missing", params: { file: "mistgate-node-linux-arm64" } }) }));
    expect(detailsOf("up-bundle-details")!.getAttribute("aria-expanded")).toBe("true");
    expect(text()).toContain("A file listed in the manifest is missing: mistgate-node-linux-arm64.");
  });

  it("folds a rollout that finished well to its header, and shows one that ended badly", async () => {
    await mount(page({ nodes: [node()], rollout: rollout({ status: RolloutStatus.DONE, steps: [step({ state: StepState.PASSED })] }) }));
    expect(button("Show stages")).toBeDefined();
    expect(text()).toContain("1 of 1 node updated");
    expect(text()).not.toContain("Canary");
    act(() => root?.unmount());
    host?.remove();
    await mount(page({ nodes: [node()], rollout: rollout({ status: RolloutStatus.FAILED, steps: [step({ state: StepState.FAILED, errorKey: "updates.step.err.probe_failed" })] }) }));
    expect(button("Hide stages")).toBeDefined();
    expect(text()).toContain("Canary");
    expect(text()).toContain("The client-eye check failed after the update.");
  });

  it("notes a node that passed because the panel's own path to it loses packets", async () => {
    const passed = step({ state: StepState.PASSED, params: { vantage: "nl2", panel_path: "lossy" } });
    await mount(page({ nodes: [node()], rollout: rollout({ status: RolloutStatus.DONE, steps: [passed] }) }));
    act(() => button("Show stages")!.click());
    expect(text()).toContain("Passed: de1 is fine. Its profiles answer from nl2");
    act(() => root?.unmount());
    host?.remove();
    const failed = step({ state: StepState.ROLLED_BACK, errorKey: "updates.step.err.probe_failed", params: { vantage: "nl2", vantage_verdict: "bad" } });
    await mount(page({ nodes: [node()], rollout: rollout({ status: RolloutStatus.FAILED, steps: [failed] }) }));
    expect(text()).toContain("The client-eye check failed after the update.");
    expect(text()).toContain("Failed: de1's profiles did not answer the panel, and not from nl2 either.");
  });

  it("shows a running rollout as rows, one per node, and as a card per node above", async () => {
    await mount(page({ nodes: [node({ state: NodeUpdateState.UPDATING })], rollout: rollout({ steps: [step({ nodeName: "de1", state: StepState.GATING })] }) }));
    const rows = [...document.querySelectorAll(".uq-ls > div")].map((r) => r.textContent);
    expect(rows).toEqual(["de1Canarychecking…"]);
    expect(document.querySelector(".uq-ft.now")?.textContent).toContain("checking for 5 minutes");
    expect(button("Hide stages")).toBeUndefined(); // a running rollout cannot be folded
  });

  it("opens the panel's steps by itself when it cannot update itself, and leaves the details folded otherwise", async () => {
    route.search = { tab: "panel" }; // the panel's own tab
    await mount(page({ nodes: [node()] }));
    expect(detailsOf("uq-det")!.getAttribute("aria-expanded")).toBe("false");
    expect(text()).not.toContain("Updating the panel itself");
    act(() => root?.unmount());
    host?.remove();
    await mount(page({ nodes: [node()], panel: { ...page().panel, update: { ...page().panel.update, supported: false } } }));
    expect(detailsOf("uq-det")!.getAttribute("aria-expanded")).toBe("true");
    expect(text()).toContain("Updating the panel itself");
  });
});

describe("the nodes of a rollout in progress", () => {
  const row = (name: string) => [...document.querySelectorAll<HTMLElement>(".uq-t > .uq-r:not(.uq-h)")].find((r) => r.textContent?.startsWith(name))!;
  // the server plans a canary and then one node per stage while fewer than five are to be updated
  const fleet = [
    node({ nodeId: "nod_1", name: "nl1", state: NodeUpdateState.UP_TO_DATE, lastUpdate: { outcome: "ok", fromVersion: "0.1.0", toVersion: "0.2.0", atUnix: 900 } }),
    node({ nodeId: "nod_2", name: "de1", state: NodeUpdateState.UPDATING }),
    node({ nodeId: "nod_3", name: "fi1", state: NodeUpdateState.OUTDATED, lastUpdate: { outcome: "ok", fromVersion: "0.0.9", toVersion: "0.1.0", atUnix: 5 } }),
    node({ nodeId: "nod_4", name: "se1", state: NodeUpdateState.OUTDATED }),
    node({ nodeId: "nod_5", name: "no1", state: NodeUpdateState.OUTDATED }),
  ];
  const steps = [
    step({ nodeId: "nod_1", nodeName: "nl1", stage: 0, state: StepState.PASSED }),
    step({ nodeId: "nod_2", nodeName: "de1", stage: 1, state: StepState.GATING }),
    step({ nodeId: "nod_3", nodeName: "fi1", stage: 2, state: StepState.PENDING }),
    step({ nodeId: "nod_4", nodeName: "se1", stage: 3, state: StepState.PENDING }),
  ];

  it("say where they stand in it, and offer no update of their own", async () => {
    await mount(page({ nodes: fleet, rollout: rollout({ steps }) }));
    expect(row("fi1").textContent).toContain("Queued · batch 2");
    expect(row("se1").textContent).toContain("Queued · batch 3");
    expect(row("de1").textContent).toContain("Checking…");
    expect(row("fi1").textContent).not.toContain("Update available");
    for (const n of ["fi1", "se1", "de1", "nl1"]) expect(document.querySelector(`button[aria-label='Update ${n}']`), n).toBeNull();
    // a node that is not in the rollout can still be added to it
    expect(row("no1").textContent).toContain("Update available");
    expect(document.querySelector("button[aria-label='Update no1']")).not.toBeNull();
  });

  it("name the canary while it waits, and the node being installed", async () => {
    await mount(
      page({
        nodes: [node({ nodeId: "nod_1", name: "nl1", state: NodeUpdateState.OUTDATED }), node({ nodeId: "nod_2", name: "de1", state: NodeUpdateState.UPDATING })],
        rollout: rollout({ steps: [step({ nodeId: "nod_1", nodeName: "nl1", stage: 0, state: StepState.PENDING }), step({ nodeId: "nod_2", nodeName: "de1", stage: 1, state: StepState.SENT })] }),
      }),
    );
    expect(row("nl1").textContent).toContain("Queued · canary");
    expect(row("de1").textContent).toContain("Updating…");
  });

  it("shows live download progress with accessible progress semantics", async () => {
    await mount(
      page({
        nodes: [node({ nodeId: "nod_1", name: "de1", state: NodeUpdateState.UPDATING })],
        rollout: rollout({
          steps: [step({ nodeId: "nod_1", nodeName: "de1", state: StepState.SENT, downloadPhase: "downloading", downloadedBytes: 512, totalBytes: 1024, bytesPerSecond: 128 })],
        }),
      }),
    );
    const progress = document.querySelector<HTMLElement>('[role="progressbar"][aria-valuenow="50"]');
    expect(progress?.getAttribute("aria-label")).toBe("Download progress: 50%");
    expect(progress?.textContent).toContain("50% · 128 B/s");
    expect(progress?.textContent).toContain("512 B / 1 KiB");
    // the panel is the usual source: it says nothing
    expect(progress?.textContent).not.toContain("GitHub");
  });

  it("says GitHub on the progress line only while the bytes come from there", async () => {
    const sent = (downloadSource: string) =>
      page({
        nodes: [node({ nodeId: "nod_1", name: "de1", state: NodeUpdateState.UPDATING })],
        rollout: rollout({ steps: [step({ nodeId: "nod_1", nodeName: "de1", state: StepState.SENT, downloadPhase: "downloading", downloadedBytes: 512, totalBytes: 1024, bytesPerSecond: 128, downloadSource })] }),
      });
    await mount(sent("github"));
    expect(document.querySelector('[role="progressbar"][aria-valuenow="50"]')?.textContent).toContain("50% · 128 B/s · from GitHub");
    // a name the node made up is not worded
    await act(async () => root!.unmount());
    host!.remove();
    await mount(sent("<b>evil</b>"));
    expect(document.querySelector('[role="progressbar"][aria-valuenow="50"]')?.textContent).toContain("50% · 128 B/s");
    expect(document.querySelector('[role="progressbar"][aria-valuenow="50"]')?.textContent).not.toContain("from");
  });

  it("lists what a failed download tried under the error, and keeps the node's odd text out of it", async () => {
    const failed = (sources_failed?: string) =>
      page({
        nodes: [node({ state: NodeUpdateState.FAILED })],
        rollout: rollout({ status: RolloutStatus.FAILED, steps: [step({ state: StepState.FAILED, errorKey: "updates.step.err.download_failed", params: sources_failed === undefined ? {} : { sources_failed } })] }),
      });
    await mount(failed("panel:stalled,github:http_404"));
    expect(text()).toContain("The node could not download the file.");
    expect(text()).toContain("Tried: panel (stalled), GitHub (HTTP 404)");
    for (const raw of [undefined, "", "<img src=x onerror=alert(1)>", "peer:stalled", "x".repeat(300)]) {
      await act(async () => root!.unmount());
      host!.remove();
      await mount(failed(raw));
      expect(text()).not.toContain("Tried:");
      expect(document.querySelector("img")).toBeNull();
    }
  });

  it("names the stage of every node: the canary first, then the batches, two nodes of one batch under the same name", async () => {
    await mount(page({ nodes: fleet, rollout: rollout({ steps: [...steps, step({ nodeId: "nod_6", nodeName: "x1", stage: 3, state: StepState.PENDING })] }) }));
    const stages = [...document.querySelectorAll(".uq-ls > div > em")].map((e) => e.textContent);
    expect(stages).toEqual(["Canary", "Batch 1", "Batch 2", "Batch 3", "Batch 3"]);
    expect(text()).not.toContain("next node");
  });
  it("keep Roll back where it is allowed, and lose Schedule", async () => {
    await mount(page({ nodes: fleet, rollout: rollout({ steps }) }));
    expect(row("fi1").querySelector("button[aria-label='More actions for fi1']")).not.toBeNull();
    await press(row("fi1").querySelector("button[aria-label='More actions for fi1']"));
    expect([...document.querySelectorAll('[role="menuitem"]')].map((i) => i.textContent)).toEqual(["Roll back…"]);
    // the others have nothing to offer: no menu at all
    expect(row("se1").querySelector("button[aria-label='More actions for se1']")).toBeNull();
    // a node that is not in the rollout keeps its Schedule
    await click(menuItem("Roll back…")); // close the menu
    await click(inDialog("Cancel"));
    await press(row("nl1").querySelector("button[aria-label='More actions for nl1']"));
    expect([...document.querySelectorAll('[role="menuitem"]')].map((i) => i.textContent)).toEqual(["Roll back…"]);
  });

  it("are the same while the rollout is paused", async () => {
    await mount(page({ nodes: fleet, rollout: rollout({ status: RolloutStatus.PAUSED, pauseKey: "updates.pause.owner", steps }) }));
    expect(row("fi1").textContent).toContain("Queued · batch 2");
    expect(document.querySelector("button[aria-label='Update fi1']")).toBeNull();
  });

  it("go back to their own state once the rollout is over", async () => {
    await mount(page({ nodes: fleet, rollout: rollout({ status: RolloutStatus.CANCELLED, steps }) }));
    expect(row("fi1").textContent).toContain("Update available");
    expect(document.querySelector("button[aria-label='Update fi1']")).not.toBeNull();
  });
});

describe("the fleet of the agents tab", () => {
  const fleetOf = (n: number, over: (i: number) => Record<string, unknown> = () => ({})) => Array.from({ length: n }, (_, i) => node({ nodeId: `nod_${i + 1}`, name: `n${i + 1}`, ...over(i) }));
  const wordOf = (cell: Element) => cell.querySelector(".sr-only")?.textContent;

  it("keeps a card per node up to six nodes, and draws no matrix", async () => {
    await mount(page({ nodes: fleetOf(6) }));
    expect(document.querySelectorAll(".uq-ft")).toHaveLength(6);
    expect(document.querySelector(".uq-fleet.many")).toBeNull();
    expect(document.querySelector(".uq-mc")).toBeNull();
  });

  it("gives the canary a card and every other node a cell from seven nodes, with the board's two headers", async () => {
    await mount(page({ nodes: fleetOf(12, (i) => (i === 3 ? { state: NodeUpdateState.OUTDATED } : {})) }));
    const fleet = document.querySelector(".uq-fleet.many")!;
    expect(fleet.classList.contains("all")).toBe(false);
    expect(fleet.querySelectorAll(".uq-ft")).toHaveLength(1);
    expect(fleet.querySelector(".uq-ft")!.textContent).toContain("n4");
    expect(fleet.querySelector(".uq-ft")!.classList.contains("can")).toBe(true);
    const heads = [...fleet.querySelectorAll(".uq-mh")].map((h) => h.textContent);
    expect(heads).toEqual(["Awaiting update1", "Up to date110.2.0-bbb"]);
    const cells = [...fleet.querySelectorAll(".uq-mc")];
    expect(cells).toHaveLength(11);
    expect(cells.every((c) => c.classList.contains("ok") && wordOf(c) === "up to date" && c.querySelector("svg.i"))).toBe(true);
    expect(cells[0]!.getAttribute("title")).toBe("n1: up to date");
    // the table below still lists every node
    expect(document.querySelectorAll(".uq-t .uq-r:not(.uq-h)")).toHaveLength(12);
  });

  it("lets the cells of the nodes that are not up to date speak in the card's colours, the ones that need a look first", async () => {
    const states = [NodeUpdateState.UP_TO_DATE, NodeUpdateState.UP_TO_DATE, NodeUpdateState.OUTDATED, NodeUpdateState.OFFLINE, NodeUpdateState.FAILED, NodeUpdateState.UPDATING, NodeUpdateState.ROLLED_BACK, NodeUpdateState.UNSUPPORTED];
    await mount(page({ nodes: fleetOf(8, (i) => ({ state: states[i], onlineUsers: 1 })) }));
    expect(document.querySelector(".uq-fleet.many .uq-ft")!.classList.contains("bad")).toBe(true);
    const look = (c: Element) => [[...c.classList].filter((x) => x !== "uq-mc")[0], c.querySelector(".mk") ? "mark" : c.querySelector("svg") ? "glyph" : ""];
    expect([...document.querySelectorAll(".uq-mc")].map(look)).toEqual([
      ["bad", "glyph"],
      ["now", "mark"],
      ["part", "mark"],
      ["part", "mark"],
      ["off", "mark"],
      ["ok", "glyph"],
      ["ok", "glyph"],
    ]);
    // the matrix is no longer only the nodes that are up to date: its header says "The rest"
    expect([...document.querySelectorAll(".uq-mh")].map((h) => h.textContent)).toEqual(["Awaiting update6", "The rest70.2.0-bbb"]);
    expect(wordOf(document.querySelectorAll(".uq-mc")[0]!)).toMatch(/rolled back|update failed/);
  });

  it("spreads the matrix over the card when every node is up to date, and flashes the node that has just finished", async () => {
    setEffectsEnabled(true);
    const done = rollout({ status: RolloutStatus.DONE, steps: [step({ nodeId: "nod_2", nodeName: "n2", state: StepState.PASSED, finishedUnix: 990 })] });
    await mount(page({ nowUnix: 1000, nodes: fleetOf(9), rollout: done }));
    expect(document.querySelector(".uq-fleet.many.all")).not.toBeNull();
    expect(document.querySelector(".uq-fleet .uq-ft")).toBeNull();
    expect(document.querySelector(".uq-mh1")).toBeNull();
    expect(document.querySelectorAll(".uq-mc")).toHaveLength(9);
    expect([...document.querySelectorAll(".uq-mc.just")].map((c) => c.textContent)).toEqual(["n2up to date"]);
    expect(document.querySelector(".uq")!.classList.contains("uq-fx")).toBe(true);
    expect(document.querySelector(".uq-mh")!.textContent).toBe("Up to date90.2.0-bbb");
  });

  it("folds a long matrix to eight rows and opens the rest on Show all", async () => {
    // jsdom has no layout: the grid says it is taller than eight rows of 44px
    const h = vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(1000);
    await mount(page({ nodes: fleetOf(40) }));
    const grid = document.querySelector(".uq-mcs")!;
    expect(grid.classList.contains("fold")).toBe(true);
    const more = button("Show all 40")!;
    expect(more.getAttribute("aria-expanded")).toBe("false");
    await click(more);
    expect(grid.classList.contains("fold")).toBe(false);
    expect(button("Show fewer")!.getAttribute("aria-expanded")).toBe("true");
    await click(button("Show fewer"));
    expect(grid.classList.contains("fold")).toBe(true);
    h.mockRestore();
  });

  it("offers no Show all when the rows fit", async () => {
    await mount(page({ nodes: fleetOf(12) }));
    expect(document.querySelector(".uq-mcs")!.classList.contains("fold")).toBe(false);
    expect(buttons().some((b) => /^Show (all|fewer)/.test(b.textContent ?? ""))).toBe(false);
  });
});

describe("the rollout window follows the server's batch plan", () => {
  const many = (n: number) => Array.from({ length: n }, (_, i) => node({ nodeId: `nod_${i + 1}`, name: `n${i + 1}`, state: NodeUpdateState.OUTDATED, onlineUsers: i }));

  it("goes one at a time after the canary while fewer than five nodes are to be updated", async () => {
    await mount(page({ nodes: many(4) }));
    await click(bulk());
    expect(dialog()!.textContent).toContain("Then the rest, 1 at a time.");
  });

  it("goes two at a time from five nodes, and counts only the nodes still ticked", async () => {
    await mount(page({ nodes: many(5) }));
    await click(bulk());
    expect(dialog()!.textContent).toContain("Then the rest, 2 at a time.");
    await click(dialog()!.querySelector("input[type=checkbox]"));
    expect(dialog()!.textContent).toContain("Then the rest, 1 at a time.");
  });

  it("does not talk of a rest when there is a single node", async () => {
    await mount(page({ nodes: many(1) }));
    await click(bulk());
    expect(dialog()!.textContent).toContain("This node is checked for 5 minutes after the update.");
    expect(dialog()!.textContent).not.toContain("at a time");
  });
});
describe("What's new", () => {
  const note = (version: string, over: Record<string, unknown> = {}) => ({
    version,
    url: `https://github.com/Mistgate/mistgate/releases/tag/${version}`,
    publishedUnix: 1_790_000_000,
    en: `## Fixes\n\n- Fix in ${version}.`,
    ru: `## Исправления\n\n- Исправление в ${version}.`,
    zh: "",
    security: false,
    ...over,
  });
  // the notes belong to the panel's own tab (the board has no "What's new" on the agents tab)
  const releaseUpdate = { version: "v0.1.40", url: "https://github.com/Mistgate/mistgate/releases/tag/v0.1.40", publishedUnix: 2000, checkedUnix: 1000, available: true, supported: true, installable: true, installing: false, errorKey: "" };
  const behind = (over: Record<string, unknown> = {}) => {
    route.search = { tab: "panel" };
    return page({
      bundle: bundle({ version: "v0.1.40" }),
      nodes: [node({ nodeId: "nod_1", name: "de1", version: "v0.1.37", state: NodeUpdateState.OUTDATED })],
      panel: { ...page().panel, update: releaseUpdate },
      panelNotes: [note("v0.1.40"), note("v0.1.39"), note("v0.1.38")],
      ...over,
    });
  };
  const remount = async (data: unknown) => {
    await act(async () => root!.unmount());
    host!.remove();
    await mount(data);
  };
  const article = (version: string) => [...document.querySelectorAll("article")].find((x) => x.getAttribute("aria-label") === version);

  it("shows the newest release in the panel card and folds the earlier ones", async () => {
    await mount(behind());
    expect(text()).toContain("What’s new");
    expect(text()).toContain("Fix in v0.1.40.");
    expect(text()).not.toContain("Fix in v0.1.39.");
    expect(text()).toContain("The text is not signed");
    // no pressure without a security section
    expect(text()).not.toContain("Security fixes");
    expect(text()).not.toContain("update soon");
    await click(button("and 2 earlier versions"));
    expect(text()).toContain("Fix in v0.1.39.");
    expect(text()).toContain("Fix in v0.1.38.");
    expect(button("Hide earlier versions")).toBeTruthy();
    await click(button("Hide earlier versions"));
    expect(text()).not.toContain("Fix in v0.1.38.");
    // the version links to its GitHub page, safely
    const a = document.querySelector('a[href="https://github.com/Mistgate/mistgate/releases/tag/v0.1.40"]')!;
    expect(a.getAttribute("rel")).toBe("noopener noreferrer");
  });

  it("badges a release with security fixes and says so plainly", async () => {
    await mount(behind({ panelNotes: [note("v0.1.40"), note("v0.1.39", { security: true, en: "## Security\n\n- Fixed a hole." })] }));
    expect(text()).toContain("There are security fixes — better update soon");
    // the fixes are in the release that is folded here: its line carries the word "Security"
    expect(text()).not.toContain("Fixed a hole.");
    await click(button("and 1 earlier version"));
    expect(text()).toContain("Fixed a hole.");
    expect(article("v0.1.39")?.textContent).toContain("Security");
    expect(article("v0.1.40")?.textContent).not.toContain("Security");
  });

  it("puts every bullet on a line of its own, with the word of its section in front, and a nested bullet under it", async () => {
    const en = ["## Features", "", "- **Node page:** keeps the filters.", "  - the choice is kept per device", "", "## Fixes", "", "- A fix.", "", "## Docs", "", "- A page."].join("\n");
    await mount(behind({ panelNotes: [note("v0.1.40", { en })] }));
    const lines = [...document.querySelectorAll("article li")].map((li) => li.textContent);
    expect(lines).toEqual(["NewNode page: keeps the filters.– the choice is kept per device", "FixedA fix.", "DocsA page."]);
    expect([...document.querySelectorAll("article .uq-tg")].map((e) => e.className)).toEqual(["uq-tg new", "uq-tg fix", "uq-tg"]);
    // a single release has no earlier versions to offer
    expect(text()).not.toContain("earlier version");
  });

  it("shows the panel's notes in the panel card, only for a verified release", async () => {
    const update = { version: "v0.3.0", url: "https://github.com/Mistgate/mistgate/releases/tag/v0.3.0", publishedUnix: 2000, checkedUnix: 1000, available: true, supported: true, installable: true, installing: false, errorKey: "" };
    await mount(page({ panel: { ...page().panel, update }, panelNotes: [note("v0.3.0", { en: "- Panel thing." }), note("v0.2.5")] }));
    expect(text()).toContain("Panel thing.");
    expect(text()).toContain("and 1 earlier version");
    await remount(page({ panel: { ...page().panel, update }, panelNotes: [] }));
    expect(text()).not.toContain("What’s new");
  });

  it("is absent without notes, and is not on the agents tab (the board has it on the panel's tab only)", async () => {
    await mount(behind({ panelNotes: [] }));
    expect(text()).not.toContain("What’s new");
    await act(async () => root!.unmount());
    host!.remove();
    route.search = { tab: "agents" };
    await mount(page({ bundle: bundle({ version: "v0.1.40" }), nodes: [node({ nodeId: "nod_1", name: "de1", version: "v0.1.37", state: NodeUpdateState.OUTDATED })], agentNotes: [note("v0.1.40")] }));
    expect(button("Update 1 node")).toBeTruthy();
    expect(text()).not.toContain("What’s new");
    expect(text()).not.toContain("Fix in v0.1.40.");
  });

  it("reads the notes in the interface language, with English as the fallback", async () => {
    const { setLang } = await import("@/i18n");
    try {
      act(() => setLang("ru"));
      await mount(behind({ panelNotes: [note("v0.1.40")] }));
      expect(text()).toContain("Что нового");
      expect(text()).toContain("Исправление в v0.1.40.");
      act(() => setLang("zh"));
      await remount(behind({ panelNotes: [note("v0.1.40")] }));
      expect(text()).toContain("更新内容");
      expect(text()).toContain("Fix in v0.1.40."); // the release has no Chinese text: English
    } finally {
      act(() => setLang("en"));
    }
  });
});

describe("the Cloudflare edition", () => {
  // Me after node updates were wired there: every other gap of the edition is still listed, agent_updates is not
  const edgeMe = { edition: "edge", unavailable: ["backups", "mcp", "health_checks", "agent_logs", "panel_update", "decoy_own", "tspu_probe"] };
  const base = "https://github.com/Mistgate/mistgate/releases/download/v0.2.0-bbb/";
  const sha = "9dd4c0" + "ab".repeat(29);
  // the panel's own signed release, offered for download; no dist folder; the panel cannot swap its own binary
  const edgePage = (over: Record<string, unknown> = {}) =>
    page({
      distDir: "",
      bundle: bundle({ downloadBase: base, files: [{ os: "linux", arch: "amd64", name: "mistgate-node-linux-amd64", size: 1048576, sha256: sha }] }),
      panel: { ...page().panel, update: { ...page().panel.update, supported: false, errorKey: "unsupported", checkedUnix: 0 } },
      ...over,
    });
  // a v0.1.47 agent: older than the release and without update-link/1
  const legacy = node({ nodeId: "nod_3", name: "nl1", version: "v0.1.47", state: NodeUpdateState.UNSUPPORTED, supportsUpdate: false, address: "nl1.example.com" });
  const howTo = () => buttons().find((b) => b.textContent === "How to update");
  const codes = () => [...document.querySelectorAll("code")].map((c) => c.textContent);
  const remount = async (data: unknown) => {
    await act(async () => root!.unmount());
    host!.remove();
    await mount(data);
  };
  const gateNote = "On the Cloudflare edition the client-eye check is skipped for now";
  beforeEach(() => {
    edition = edgeMe;
  });

  it("offers the same buttons as the VPS, with no notice that node updates are missing and no bundle card", async () => {
    await mount(edgePage({ nodes: outdated }));
    expect(bulk()?.textContent).toBe("Update 2 nodes");
    expect(document.querySelector("button[aria-label='Update de1']")).toBeTruthy();
    expect(text()).not.toContain("Updating nodes");
    expect(text()).not.toContain("Not available in the Cloudflare edition yet");
    // the bundle card is the VPS's dist folder: its signing and copying steps do not apply
    expect(text()).not.toContain("Release bundle");
    expect(button("Read the folder again")).toBeUndefined();
  });

  it("gives a v0.1.47 node commands run on the node: download, SHA-256 check, install", async () => {
    await mount(edgePage({ nodes: [node(), legacy] }));
    expect(text()).toContain("1 node waits for a manual update: nl1");
    expect(text()).toContain("Its agent was built before nodes could update on the Cloudflare edition.");
    expect(text()).not.toContain("Automatic updates need a supported system");
    await click(howTo());
    expect(howTo()!.getAttribute("aria-expanded")).toBe("true");
    expect(codes()).toContain(`curl -fL -o /root/mistgate-node.new ${base}mistgate-node-linux-amd64 && echo "${sha}  /root/mistgate-node.new" | sha256sum -c -`);
    expect(codes()).toContain("chmod +x /root/mistgate-node.new && mv /root/mistgate-node.new /root/mistgate-node && /root/mistgate-node install");
    expect(text()).toContain("1. On the node: download the new agent and check its SHA-256");
    expect(text()).toContain("If the node cannot reach GitHub, copy mistgate-node-linux-amd64 to /root/mistgate-node.new yourself.");
    expect(text()).not.toContain("panel’s server");
    expect(text()).not.toContain("The panel holds no");
    expect(text()).not.toContain("scp ");
  });

  it("keeps the panel's server out of the steps while its release cannot be offered", async () => {
    await mount(edgePage({ nodes: [legacy], bundle: bundle({ status: BundleStatus.UNTRUSTED, files: [], errorKey: "updates.bundle.err.release_unreachable" }) }));
    await click(howTo());
    expect(text()).toContain("There is no signed mistgate-node-linux-amd64 to download right now.");
    expect(text()).not.toContain("panel’s server");
    expect(text()).not.toContain("The panel holds no");
  });

  it("says the panel row is updated with mistgate edge deploy, without systemd, and gives the command", async () => {
    route.search = { tab: "panel" }; // the panel's own tab
    await mount(edgePage({ nodes: [node()] }));
    expect(text()).toContain("Updated with mistgate edge deploy");
    expect(text()).toContain("This panel runs on Cloudflare. Update it from your computer with this command; --rollback in place of --version returns the previous version.");
    expect(codes()).toContain(`mistgate edge deploy --host ${location.hostname} --version 0.2.0-bbb`);
    expect(text()).not.toContain("systemd");
    expect(text()).not.toContain("Not available in the Cloudflare edition yet");
    expect(button("Update panel")).toBeUndefined();
    // the details hold no steps for a panel that cannot update itself
    await click(document.querySelector("button[aria-controls='uq-det']"));
    expect(text()).not.toContain("Updating the panel itself");
  });

  it("puts a newer release in the command once GitHub was checked", async () => {
    const update = { ...page().panel.update, version: "v0.3.0", available: true, supported: false, installable: false, errorKey: "", checkedUnix: 1000 };
    await mount(edgePage({ nodes: [node()], panel: { ...page().panel, update } }));
    expect(text()).toContain("Updated with mistgate edge deploy");
    expect(codes()).toContain(`mistgate edge deploy --host ${location.hostname} --version v0.3.0`);
    expect(button("Update panel")).toBeUndefined();
  });

  it("notes the client-eye check is skipped, in the rollout window and on the rollout in progress", async () => {
    await mount(edgePage({ nodes: outdated }));
    await click(bulk());
    expect(dialog()?.textContent).toContain(gateNote);
    await remount(edgePage({ nodes: outdated, rollout: rollout({ steps: [step({ state: StepState.GATING }), step({ nodeId: "nod_2", nodeName: "nl1", stage: 1 })] }) }));
    expect(text()).toContain(gateNote);
  });

  it("names the release errors of the edition in the hero", async () => {
    await mount(edgePage({ nodes: outdated, bundle: bundle({ status: BundleStatus.UNTRUSTED, files: [], errorKey: "updates.bundle.err.release_unreachable" }) }));
    expect(text()).toContain("The panel could not fetch its signed node release from GitHub. It tries again within a minute.");
    await remount(edgePage({ nodes: outdated, bundle: bundle({ status: BundleStatus.MISSING, version: "", built: 0, files: [], errorKey: "updates.bundle.err.no_release" }) }));
    expect(text()).toContain("This panel is not a release build, so it has no signed node release to offer.");
    expect(text()).not.toContain("dist folder");
  });

  it("leaves the VPS page as it was: the bundle card, the scp step, no edition texts", async () => {
    edition = {};
    await mount(page({ nodes: [...outdated, legacy], rollout: rollout({ steps: [step({ state: StepState.GATING })] }) }));
    expect(text()).toContain("Release bundle");
    expect(text()).not.toContain(gateNote);
    expect(text()).not.toContain("mistgate edge deploy");
    await click(howTo());
    expect(text()).toContain("1. On the panel’s server: put the new agent on the node");
    expect(codes()).toContain("scp /var/lib/mistgate/dist/mistgate-node-linux-amd64 root@nl1.example.com:/root/mistgate-node");
  });
});

describe("the tabs of the Updates screen", () => {
  const release = { version: "v0.3.0", url: "https://github.com/Mistgate/mistgate/releases/tag/v0.3.0", publishedUnix: 2000, checkedUnix: 1000, available: true, supported: true, installable: true, installing: false, errorKey: "" };
  const withRelease = (over: Record<string, unknown> = {}) => page({ panel: { ...page().panel, update: release }, nodes: [node()], ...over });
  const tabs = () => [...document.querySelectorAll<HTMLElement>("[role=tab]")];
  const selected = () => document.querySelector("[role=tab][aria-selected=true]")?.id;

  it("opens on the panel when a release is out, and on the agents when it is not", async () => {
    await mount(withRelease());
    expect(selected()).toBe("uq-tab-panel");
    act(() => root?.unmount());
    host?.remove();
    await mount(page({ nodes: [node()] }));
    expect(selected()).toBe("uq-tab-agents");
  });

  it("opens on the panel for a release it cannot install, and for an update that went back", async () => {
    await mount(page({ panel: { ...page().panel, update: { ...release, available: false, installable: false, errorKey: "unsigned" } }, nodes: [node()] }));
    expect(selected()).toBe("uq-tab-panel");
    act(() => root?.unmount());
    host?.remove();
    await mount(page({ panel: { ...page().panel, update: { ...release, available: false, errorKey: "install_failed" } }, nodes: [node()] }));
    expect(selected()).toBe("uq-tab-panel");
  });

  it("opens on the tab the address names, and on the one a link's anchor names", async () => {
    route.search = { tab: "agents" };
    await mount(withRelease());
    expect(selected()).toBe("uq-tab-agents");
    act(() => root?.unmount());
    host?.remove();
    route.search = {};
    location.hash = "#os";
    try {
      await mount(withRelease());
      expect(selected()).toBe("uq-tab-os");
    } finally {
      location.hash = "";
    }
  });

  it("puts a number on a tab only when something waits there, and says what in the line under the title", async () => {
    await mount(withRelease({ nodes: outdated }));
    expect(tabs().map((t) => t.textContent)).toEqual(["This panel1", "Node agents2", "Server systems"]);
    expect(document.querySelector(".uq-h p")?.textContent).toBe("You can update the panel and 2 nodes");
    act(() => root?.unmount());
    host?.remove();
    await mount(page({ nodes: [node()] }));
    expect(tabs().map((t) => t.textContent)).toEqual(["This panel", "Node agents", "Server systems"]);
    expect(document.querySelector(".uq-h p")?.textContent).toBe("Panel and agents are up to date");
  });

  it("moves between the tabs with the arrow keys, through the address", async () => {
    await mount(page({ nodes: [node()] }));
    const key = (el: Element, k: string) => act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })));
    await key(tabs()[1]!, "ArrowRight");
    expect(route.navigate).toHaveBeenCalledWith({ to: "/updates", search: { tab: "os" }, replace: true });
    await key(tabs()[1]!, "ArrowLeft");
    expect(route.navigate).toHaveBeenLastCalledWith({ to: "/updates", search: { tab: "panel" }, replace: true });
  });

  it("keeps the tabs on the page while it loads", async () => {
    getUpdates.mockReturnValue(new Promise(() => {}));
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () =>
      root!.render(
        <QueryClientProvider client={qc}>
          <UpdatesScreen />
        </QueryClientProvider>,
      ),
    );
    expect(tabs()).toHaveLength(3);
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    expect(document.querySelector(".uq-h p")?.textContent).toBe("Loading…");
  });

  it("keeps the tabs on the page when it did not load, with a card that says nothing changed on the nodes and a way to try again", async () => {
    getUpdates.mockRejectedValue(new Error("down"));
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () =>
      root!.render(
        <QueryClientProvider client={qc}>
          <UpdatesScreen />
        </QueryClientProvider>,
      ),
    );
    for (let i = 0; i < 3; i++) await settle();
    expect(document.querySelector("[role=alert]")?.textContent).toContain("Updates failed to load");
    expect(document.querySelector("[role=alert]")?.textContent).toContain("Nothing changed on the nodes.");
    expect(tabs()).toHaveLength(3);
    expect(document.querySelector(".uq-h p")?.textContent).toBe("No data");
    await click(button("Retry"));
    expect(getUpdates).toHaveBeenCalledTimes(2);
  });
});

describe("the panel's own update, as the page follows it", () => {
  const release = { version: "v0.3.0", url: "", publishedUnix: 2000, checkedUnix: 1000, built: 1500, sha256: "ab".repeat(32), available: true, supported: true, installable: true, installing: false, errorKey: "" };
  const offer = (over: Record<string, unknown> = {}) => page({ panel: { ...page().panel, update: { ...release, ...over } }, nodes: [node()] });

  it("takes the whole window while the panel installs itself, and says what holds: the nodes and the people go on", async () => {
    installPanelUpdate.mockResolvedValue({ update: {} });
    await mount(offer());
    await click(button("Update panel"));
    await settle();
    await click(inDialog("Update"));
    await settle();
    const take = document.querySelector<HTMLElement>(".uq-take[role=alertdialog]")!;
    expect(take.textContent).toContain("The panel is updating");
    expect(take.textContent).toContain("0.2.0-bbb");
    expect(take.textContent).toContain("v0.3.0");
    expect(take.textContent).toContain("Nodes and people’s connections keep working. The page comes back by itself");
    // the panel reports no step: the page shows none
    expect(take.querySelector(".uq-ttrk")).toBeNull();
    expect(document.querySelector(".uq-now")?.textContent).toContain("Updating the panel to v0.3.0");
    expect(document.querySelector(".uq-ha")).toBeNull();
  });

  it("shows it from the panel's own word that it is installing, to whoever opens the page", async () => {
    await mount(offer({ installing: true }));
    expect(document.querySelector(".uq-take")).not.toBeNull();
    expect(document.querySelector(".uq-hero.run")).not.toBeNull();
  });

  it("tells a panel that went back: the old version, why, and a button to try again", async () => {
    await mount(offer({ errorKey: "install_failed" }));
    const bar = document.querySelector(".uq-hero.back .rc-pb.bad")!;
    expect(bar.textContent).toContain("The panel is back on 0.2.0-bbb");
    expect(bar.textContent).toContain("journalctl -u mistgate-panel-update");
    await click(button("Try again"));
    expect(dialog()!.textContent).toContain("Update the panel to v0.3.0");
  });

  it("asks GitHub again from the bar when it did not answer", async () => {
    checkPanelUpdate.mockResolvedValue({ update: {} });
    await mount(offer({ errorKey: "check_failed", checkedUnix: 1 }));
    expect(document.querySelector(".uq-hero .rc-pb")?.textContent).toContain("GitHub didn’t respond");
    await click(button("Check again"));
    expect(checkPanelUpdate).toHaveBeenCalledTimes(1);
  });

  it("explains a release that is out but cannot be installed here, and still lets the owner check", async () => {
    await mount(offer({ installable: false, supported: false }));
    expect(document.querySelector(".uq-hint .rc-nt")?.textContent).toContain("Automatic updates need a supported system");
    expect(button("Update panel")).toBeUndefined();
    expect(button("Check GitHub")).toBeDefined();
  });

  it("opens the channel window from the chip, for the owner only", async () => {
    await mount(offer());
    const chip = document.querySelector<HTMLElement>(".uq-ch")!;
    expect(chip.tagName).toBe("BUTTON");
    await click(chip);
    expect(dialog()!.textContent).toContain("Release channel");
    act(() => root?.unmount());
    host?.remove();
    role = Role.READONLY;
    await mount(offer());
    expect(document.querySelector(".uq-ch")?.tagName).toBe("SPAN");
  });

  it("folds the earlier versions of the release notes, and opens them", async () => {
    const notes = ["v0.3.0", "v0.2.9", "v0.2.8"].map((version) => ({ version, url: "", publishedUnix: 2000, en: `## Fixes\n\n- Fix in ${version}.`, ru: "", zh: "", security: false, channel: "stable" }));
    await mount(page({ panel: { ...page().panel, update: release }, nodes: [node()], panelNotes: notes }));
    expect(text()).toContain("Fix in v0.3.0.");
    expect(text()).not.toContain("Fix in v0.2.9.");
    await click(button("and 2 earlier versions"));
    expect(document.querySelectorAll(".uq-rel")).toHaveLength(3);
    expect(document.querySelectorAll(".uq-rel.top")).toHaveLength(1);
  });
});

describe("the agents tab", () => {
  it("shows the folder being read while the owner's request is on its way", async () => {
    rescanBundle.mockReturnValue(new Promise(() => {}));
    await mount(page({ nodes: [node()] }));
    await click(button("Read the folder again"));
    expect(button("Reading")?.disabled).toBe(true);
    expect(button("Reading")?.querySelector(".an-sp")).not.toBeNull();
  });

  it("points the observer to the owner, and shows the cards without a button", async () => {
    role = Role.READONLY;
    await mount(page({ nodes: outdated }));
    expect(document.querySelector(".uq-ro")?.textContent).toBe("Only the owner can update agents");
    expect(document.querySelectorAll(".uq-ft")).toHaveLength(2);
    expect(bulk()).toBeUndefined();
  });

  it("says there are no nodes, and where to add one", async () => {
    await mount(page({ nodes: [] }));
    const none = document.querySelector(".us-none")!;
    expect(none.textContent).toContain("No nodes yet");
    expect(none.textContent).toContain("Nothing to update.");
    expect(none.querySelector("a")?.getAttribute("href")).toBe("/nodes");
  });

  it("shows the tick for the nodes of a rollout window, one node too, with the canary named", async () => {
    await mount(page({ nodes: [outdated[0]!] }));
    await click(bulk());
    expect(dialog()!.querySelectorAll("input[type=checkbox]")).toHaveLength(1);
    expect(dialog()!.textContent).toContain("Canary");
  });

  it("names the canary on its card while the fleet waits for the rollout", async () => {
    await mount(page({ nodes: outdated }));
    const first = document.querySelector(".uq-ft")!;
    expect(first.textContent).toContain("nl1");
    expect(first.classList.contains("can")).toBe(true);
  });

  it("offers Stop and Pause while a rollout runs, and keeps both from an observer", async () => {
    const running = () => page({ nodes: [node({ state: NodeUpdateState.UPDATING })], rollout: rollout({ steps: [step({ state: StepState.SENT })] }) });
    await mount(running());
    expect(button("Pause")).toBeDefined();
    expect(button("Stop")).toBeDefined();
    act(() => root?.unmount());
    host?.remove();
    role = Role.READONLY;
    await mount(running());
    expect(button("Pause")).toBeUndefined();
    expect(button("Stop")).toBeUndefined();
    expect(document.querySelector(".uq-now")).not.toBeNull();
  });
});
