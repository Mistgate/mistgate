import { BundleStatus, NodeUpdateState, OsRebootPolicy, OsRolloutMode, OsRolloutStatus, OsStepState, OsUpdateState, RolloutStatus, StepState } from "@/gen/mistgate/admin/v1/update_pb";

// Development only: the made-up data of the Updates board (PnUpdates): the panel v0.1.71 and the release v0.1.86 that is out, the nodes
// de1, fi1, nl1 and msk1, the agent release v0.1.71 (fi1 is still on v0.1.70), the servers' systems. Documentation addresses.

export const NOW = Math.floor(Date.now() / 1000);
const H = 3600;
const T = (iso: string) => Math.floor(Date.parse(iso) / 1000);

export const CUR = "v0.1.71";
export const OLD = "v0.1.70";
export const NEW = "v0.1.86";
export const B_CUR = T("2026-10-31T09:00:00Z");
export const B_OLD = T("2026-10-01T09:00:00Z");
export const B_NEW = T("2026-11-12T02:20:00Z");
export const SHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";
export const KEY = "SHA256:Kf2Pq9TnWc4Xe7JrBu1Ly5ZmHa0Dg8SoVi3Nt6QbR2w";
/** The schedule of fi1 on the board: 2026-11-16 06:20, UTC+03:00. */
export const SCHEDULED = T("2026-11-16T03:20:00Z");

export const FLAGS: Record<string, string> = { nod_de1: "DE", nod_fi1: "FI", nod_nl1: "NL", nod_msk1: "RU" };

// ---- the node agents
export type Variant = Record<string, unknown>;
const lastOk = (at: number) => ({ outcome: "ok", fromVersion: OLD, fromBuilt: B_OLD, toVersion: CUR, toBuilt: B_CUR, reason: "", atUnix: at });
export const upNode = (id: string, name: string, over: Variant = {}) => ({
  nodeId: `nod_${id}`,
  name,
  version: CUR,
  built: B_CUR,
  supportsUpdate: true,
  crashGuard: true,
  state: NodeUpdateState.UP_TO_DATE,
  scheduledUnix: 0,
  scheduledVersion: "",
  scheduledBuilt: 0,
  scheduledTimezoneOffsetMinutes: 0,
  scheduledMissed: false,
  inbounds: 2,
  onlineUsers: 1,
  address: `${name}.example.com`,
  arch: "amd64",
  lastUpdate: lastOk(NOW - 4 * H),
  ...over,
});
export const fi1Old = (over: Variant = {}) =>
  upNode("fi1", "fi1", {
    version: OLD,
    built: B_OLD,
    state: NodeUpdateState.OUTDATED,
    onlineUsers: 0,
    scheduledUnix: SCHEDULED,
    scheduledVersion: CUR,
    scheduledBuilt: B_CUR,
    scheduledTimezoneOffsetMinutes: 180,
    lastUpdate: undefined,
    ...over,
  });
export const fleetNodes = (fi1: Variant = fi1Old()) => [upNode("de1", "de1", { onlineUsers: 3 }), fi1, upNode("nl1", "nl1"), upNode("msk1", "msk1")];

/** The country of a made-up node by the letters of its name ("de2" is German, "msk3" Russian); the pool of the big fleets. */
const COUNTRY: Record<string, string> = { de: "DE", nl: "NL", fi: "FI", msk: "RU", fr: "FR", se: "SE", pl: "PL", no: "NO", lv: "LV", lt: "LT", at: "AT" };
export const flagOf = (name: string) => COUNTRY[name.replace(/\d+$/, "")] ?? "";

/**
 * More nodes for the big fleets of the matrix: `count` of them named de2, nl2 … (the names `taken` are skipped). "up": all up to date (the
 * board's twelve); "wait": every third waits for the update; "mix": one of each state a node can be in, to see every cell.
 */
export function extraNodes(count: number, taken: ReadonlySet<string>, how: "up" | "wait" | "mix"): unknown[] {
  const prefixes = Object.keys(COUNTRY);
  const out: unknown[] = [];
  const mix: Variant[] = [
    {},
    { version: OLD, built: B_OLD, state: NodeUpdateState.OUTDATED, lastUpdate: undefined },
    { version: OLD, built: B_OLD, state: NodeUpdateState.UPDATING },
    { version: OLD, built: B_OLD, state: NodeUpdateState.ROLLED_BACK },
    { state: NodeUpdateState.OFFLINE },
    { version: "v0.1.12", built: 0, state: NodeUpdateState.UNSUPPORTED, supportsUpdate: false, lastUpdate: undefined },
    { version: OLD, built: B_OLD, state: NodeUpdateState.FAILED },
  ];
  for (let i = 0; out.length < count; i++) {
    const name = `${prefixes[i % prefixes.length]}${Math.floor(i / prefixes.length) + 1}`;
    if (taken.has(name)) continue;
    const k = out.length;
    const over = how === "mix" ? mix[k % mix.length]! : how === "wait" && k % 3 === 0 ? mix[1]! : {};
    out.push(upNode(name, name, over));
  }
  return out;
}

export const step = (id: string, name: string, stage: number, state: StepState, over: Variant = {}) => ({
  nodeId: `nod_${id}`,
  nodeName: name,
  stage,
  state,
  fromVersion: "",
  fromBuilt: 0,
  startedUnix: NOW - 4 * H,
  finishedUnix: state === StepState.PASSED || state === StepState.FAILED || state === StepState.ROLLED_BACK ? NOW - 4 * H + 300 : 0,
  errorKey: "",
  params: {},
  downloadSource: "",
  downloadPhase: "",
  downloadedBytes: 0,
  totalBytes: 0,
  bytesPerSecond: 0,
  ...over,
});
export const rollout = (status: RolloutStatus, steps: unknown[], over: Variant = {}) => ({
  id: "rol_1",
  status,
  toVersion: CUR,
  toBuilt: B_CUR,
  batchSize: 1,
  createdUnix: NOW - 4 * H,
  finishedUnix: status === RolloutStatus.DONE ? NOW - 4 * H + 900 : 0,
  pauseKey: "",
  pauseParams: {},
  steps,
  ...over,
});
/** The last finished rollout of the board: the canary nl1, then de1 and msk1, four hours ago. */
export const lastRollout = () =>
  rollout(RolloutStatus.DONE, [step("nl1", "nl1", 0, StepState.PASSED), step("de1", "de1", 1, StepState.PASSED), step("msk1", "msk1", 1, StepState.PASSED)]);

export const bundle = (over: Variant = {}) => ({
  status: BundleStatus.TRUSTED,
  version: CUR,
  built: B_CUR,
  expiresUnix: T("2027-02-28T00:00:00Z"),
  files: [{ os: "linux", arch: "amd64", name: "mistgate-node-linux-amd64", size: 18_874_368, sha256: SHA }],
  errorKey: "",
  params: {},
  scannedUnix: NOW - 60,
  downloadBase: "",
  ...over,
});

// ---- the release notes (the board's three versions)
const NL = String.fromCharCode(10);
const note = (version: string, published: string, en: string[], ru: string[], security = false) => ({
  version,
  url: `https://github.com/example/mistgate/releases/tag/${version}`,
  publishedUnix: T(published),
  channel: "stable",
  security,
  en: en.join(NL),
  ru: ru.join(NL),
  zh: "",
});
export const panelNotes = [
  note(
    NEW,
    "2026-11-12T02:20:00Z",
    ["## Features", "", "- the fleet map shows the paths between nodes", "", "## Fixes", "", "- Hysteria2 certificate renewal on nodes behind NAT", "- the list of devices lost its order after a deletion"],
    ["## Возможности", "", "- карта флота показывает пути между нодами", "", "## Исправления", "", "- продление сертификата Hysteria2 на нодах за NAT", "- список устройств терял порядок после удаления"],
    true,
  ),
  note("v0.1.85", "2026-11-09T09:00:00Z", ["## Fixes", "", "- Hysteria2 certificate renewal on nodes behind NAT"], ["## Исправления", "", "- продление сертификата Hysteria2 на нодах за NAT"]),
  note("v0.1.84", "2026-11-05T09:00:00Z", ["## Fixes", "", "- the list of devices lost its order after a deletion"], ["## Исправления", "", "- список устройств терял порядок после удаления"]),
];
/** What the node agent brings (the agents tab): the board has no such card; a short made-up release. */
export const agentNotes = [
  note(
    CUR,
    "2026-10-31T09:00:00Z",
    ["## Features", "", "- the agent reports the crash guard of its service file", "", "## Fixes", "", "- a node that rolls back says why in the panel"],
    ["## Возможности", "", "- агент сообщает, есть ли защита от падений в его unit-файле", "", "## Исправления", "", "- нода, откатившаяся после обновления, объясняет причину в панели"],
  ),
];

// ---- the panel
export type PanelKind = "new" | "cur" | "gh" | "sig" | "back" | "done";
export const panelUpdate = (kind: PanelKind) => {
  const newer = { version: NEW, url: `https://github.com/example/mistgate/releases/tag/${NEW}`, publishedUnix: B_NEW, built: B_NEW, sha256: SHA };
  const base = { checkedUnix: NOW - 18 * 60, available: true, supported: true, installable: true, installing: false, errorKey: "", ...newer };
  switch (kind) {
    case "cur":
    case "done":
      return { ...base, available: false, version: CUR, url: `https://github.com/example/mistgate/releases/tag/${CUR}`, publishedUnix: B_CUR, built: B_CUR, checkedUnix: NOW - (kind === "done" ? 20 : 18 * 60) };
    case "gh":
      return { ...base, errorKey: "check_failed", checkedUnix: NOW - 2 * H };
    case "sig":
      return { ...base, available: false, installable: false, errorKey: "unsigned" };
    case "back":
      return { ...base, errorKey: "install_failed" };
    default:
      return base;
  }
};

export function updatesData(opts: { panel?: PanelKind; nodes?: unknown[]; rollout?: unknown; bundle?: unknown; running?: string } = {}) {
  const kind = opts.panel ?? "new";
  const done = kind === "done";
  return {
    nowUnix: NOW,
    scheduleTimezoneOffsetMinutes: 180,
    updateChannel: "stable",
    runningVersionChannel: "stable",
    offeredReleaseChannel: "stable",
    runningNewerThanChannelOffers: false,
    panel: { version: opts.running ?? (done ? NEW : CUR), built: done ? B_NEW : B_CUR, hasReleaseKey: true, releaseKeyFingerprint: KEY, update: panelUpdate(kind) },
    panelNotes: kind === "new" || kind === "gh" || kind === "back" || kind === "sig" ? panelNotes : [],
    agentNotes: [],
    bundle: opts.bundle ?? bundle(),
    nodes: opts.nodes ?? fleetNodes(),
    rollout: opts.rollout === undefined ? lastRollout() : opts.rollout,
    distDir: "/var/lib/mistgate/dist",
    updateDownloadTimeoutSeconds: 1800,
    updateDownloadSources: ["github"],
  };
}

// ---- the servers' systems
const osBase = {
  measuredUnix: NOW - 60,
  distroId: "debian",
  distroVersion: "13",
  pending: 0,
  security: 0,
  notable: [],
  securityPending: [],
  securityStuck: [],
  kernelRunning: "6.12.48+deb13-amd64",
  kernelNewest: "6.12.48+deb13-amd64",
  rebootRequired: false,
  rebootReasons: [],
  held: [],
  apt: "ok",
  halfConfigured: [],
  listsUnix: NOW - H,
  dpkgUnix: NOW - 2 * H,
  autoUpdates: "on",
  autoReboot: false,
  awgModuleNext: "",
  container: "",
  bootIdHash: "",
  error: "",
};
const osNode = (id: string, name: string, online: number, state: OsUpdateState, status: Variant, over: Variant = {}) => ({
  nodeId: `nod_${id}`,
  name,
  connected: true,
  supported: true,
  onlineUsers: online,
  onPanelServer: false,
  state,
  status: { ...osBase, ...status },
  updateCapable: true,
  updateBlock: "",
  updateBlockAll: "",
  rebootBlock: "",
  rebootPossibleUnix: 0,
  inbounds: 2,
  ...over,
});
export type OsKind = "new" | "calm" | "fail" | "off" | "busy" | "just";
export const osStep = (id: string, name: string, state: OsStepState, over: Variant = {}) => ({
  nodeId: `nod_${id}`,
  nodeName: name,
  state,
  jobId: "osj_1",
  phase: "",
  phaseUnix: 0,
  kernelFrom: "",
  kernelTo: "",
  sentUnix: NOW - 240,
  rebootUnix: 0,
  backUnix: 0,
  finishedUnix: 0,
  errorKey: "",
  params: {},
  stage: 0,
  retryAfterUnix: 0,
  retryCount: 0,
  retryCode: "",
  ...over,
});
const osRun = (status: OsRolloutStatus, steps: unknown[], over: Variant = {}) => ({
  id: "osr_1",
  status,
  mode: OsRolloutMode.SECURITY,
  reboot: OsRebootPolicy.IF_REQUIRED,
  createdUnix: NOW - 300,
  finishedUnix: status === OsRolloutStatus.DONE ? NOW - 20 : 0,
  pauseKey: "",
  pauseParams: {},
  steps,
  batchSize: 1,
  ...over,
});

export function osData(kind: OsKind = "new") {
  const calm = kind === "calm";
  const de1 = calm
    ? osNode("de1", "de1", 3, OsUpdateState.UP_TO_DATE, {})
    : osNode("de1", "de1", 3, OsUpdateState.REBOOT_REQUIRED, { pending: 12, security: 4, kernelNewest: "6.12.57+deb13-amd64", rebootRequired: true, rebootReasons: ["kernel"], securityPending: ["linux-image-amd64", "libssl3", "openssh-server", "tzdata"] });
  const nl1 =
    kind === "off"
      ? osNode("nl1", "nl1", 1, OsUpdateState.OFFLINE, {}, { connected: false, status: undefined, updateBlock: "offline", rebootBlock: "offline" })
      : calm
        ? osNode("nl1", "nl1", 1, OsUpdateState.UP_TO_DATE, {})
        : osNode("nl1", "nl1", 1, OsUpdateState.UPDATES, { pending: 5, security: 2 });
  let run: unknown;
  if (kind === "fail") {
    run = osRun(OsRolloutStatus.PAUSED, [osStep("de1", "de1", OsStepState.FAILED, { errorKey: "job_failed", finishedUnix: NOW - 60, params: { code: "apt_failed", mode: "security" } })], { pauseKey: "step_failed", pauseParams: { node: "de1" } });
  } else if (kind === "busy") {
    run = osRun(OsRolloutStatus.RUNNING, [osStep("de1", "de1", OsStepState.SENT, { phase: "installing", phaseUnix: NOW - 30 })]);
  } else if (kind === "just") {
    run = osRun(OsRolloutStatus.DONE, [osStep("de1", "de1", OsStepState.PASSED, { finishedUnix: NOW - 20, kernelFrom: "6.12.48+deb13-amd64", kernelTo: "6.12.57+deb13-amd64", params: { upgraded: "4", reboot: "done", kernel: "6.12.57+deb13-amd64" } })]);
  }
  const nodes = [
    kind === "busy" ? { ...de1, state: OsUpdateState.BUSY } : kind === "just" ? osNode("de1", "de1", 3, OsUpdateState.UP_TO_DATE, { kernelRunning: "6.12.57+deb13-amd64", kernelNewest: "6.12.57+deb13-amd64" }) : de1,
    osNode("fi1", "fi1", 0, OsUpdateState.UP_TO_DATE, {}),
    nl1,
    osNode("msk1", "msk1", 1, OsUpdateState.UP_TO_DATE, { distroId: "ubuntu", distroVersion: "24.04", kernelRunning: "6.8.0-147-generic", kernelNewest: "6.8.0-147-generic" }),
  ];
  return {
    nowUnix: NOW,
    scheduleTimezoneOffsetMinutes: 180,
    panelHost: { ...osBase },
    nodes,
    rollout: run,
  };
}
