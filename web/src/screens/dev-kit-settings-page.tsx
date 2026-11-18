import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { ChatButton } from "@/components/assistant/chat";
import { AddNodeProvider } from "@/components/add-node";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { BundleStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { instance as instanceApi, ops as opsApi, updates as updatesApi, auth as authApi, backups as backupsApi, warpReach as warpReachApi } from "@/lib/api";
import { sanitizeLogo } from "@/lib/brand";
import { setEffectsEnabled } from "@/lib/effects";
import { instanceQuery } from "@/lib/instance";
import { awaitingQuery } from "@/lib/integrations";
import { checksQuery } from "@/lib/health";
import { defaultLogFilter, logSettingsQuery, logsQuery, metricsQuery, retentionQuery } from "@/lib/ops";
import { nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { updatesQuery } from "@/lib/updates";
import { warpReachQuery } from "@/lib/warpreach";
import { NODES, NOW, nodeRows, checksOf } from "@/screens/dev-kit-profiles-data";
import { Shot } from "@/screens/dev-kit-redesign";
import { SettingsFrame, SettingsTab, isPage, type PageId } from "@/screens/settings";
import { AuditPage, type AuditDemo } from "@/screens/settings-pages/audit";
import { BackupsPage, backupsKey, settingsKey as backupSettingsKey, type BackupsDemo } from "@/screens/settings-pages/backups";
import { securityQuery } from "@/screens/settings-pages/cloudflare";
import { InterfacePage, type InterfaceDemo } from "@/screens/settings-pages/interface";
import { panelResourcesQuery } from "@/screens/settings-pages/panel-resources";
import { passwordLoginQuery } from "@/screens/settings-pages/password";
import { passkeysQuery, SecurityPage, type SecurityDemo } from "@/screens/settings-pages/security";
import { SessionsPage, sessionsQuery, type SessionsDemo } from "@/screens/settings-pages/sessions";
import { SystemPage } from "@/screens/settings-pages/system";
import type { ReachDemo } from "@/screens/settings-pages/warp-reach";
import type { AuditKind, AuditSource } from "@/gen/mistgate/admin/v1/auth_pb";
import { auditDemo, auditFilter, auditPage, backupList, backupSettings, backupsDemo, keptData, passkeys, passwordLogin, restoreResult, securityDemo, securitySettings, sessions, sessionsDemo } from "./dev-kit-settings-b";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=pn-settings: Settings of the redesign in every state of the board's switches (PnSettings: the section, the
// data, the logo, the server, the sign-in, the sessions, the audit, the backups, the buffer, the role) and the states of the product the
// board does not draw, each in a frame of its own width (an iframe, so the window's breakpoints answer to the frame), inside the real shell.
// Each frame is a page of its own:
//   /dev-kit?only=settings-page&tab=<if|sys|sec|ses|aud|adm|dom|bk>[&state=…][&role=owner|helper|readonly][&lang=en|ru|zh|fa|tr][&theme=light|dark][&fx=1][&copy=fail][&edge=1]
// The tab is the board's id: if (Interface), sys (System), sec (Security), ses (Sessions), aud (Audit), adm (Admins), dom (Domains), bk (Backups).
// The state is the tab's own. Interface: load | err | big | bad | busy | new | dirty | saving | saved. System: load | err | hot | none (a panel that
// is not Linux) | nologs (a Cloudflare panel) | token (the metrics window) | made (the new token) | warp-own | warp-off | warp-full | warp-bad |
// warp-many | warp-typed | warp-refused | warp-busy | warp-load | warp-err (the card of the sites WARP is checked against; the built-in list otherwise). Admins: load | err. Domains: load | err | nosub.
// Part B adds its states in `partB` below (Security: lock | nosec, Sessions: one, Audit: none, Backups: none | fail | r2bad, and the windows).
// Made-up data: a panel called owner, the documentation addresses, nodes de1 … spb1.

const TAB: Record<string, PageId> = { if: "interface", sys: "system", sec: "security", ses: "sessions", aud: "audit", adm: "admins", dom: "domains", bk: "backups" };
export const tabCode = (id: PageId) => Object.entries(TAB).find(([, v]) => v === id)![0];

const SAMPLE_LOGO = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="12" cy="12" r="11" fill="#ffffff"/><path d="M6 15l6-9 6 9z" fill="#cba5fa"/><path d="M9 15l3-4.5L15 15z" fill="#693fc2"/></svg>`;

export type Variant = { tab: PageId; state: string; role: Role; edge: boolean };

/** What a scene's page is told besides its data: the board's states that come from a click. */
export const demoOf = (tab: PageId, state: string): { interface?: InterfaceDemo; token?: "form" | "made"; reach?: ReachDemo; security?: SecurityDemo; sessions?: SessionsDemo; audit?: AuditDemo; backups?: BackupsDemo } => {
  if (tab === "interface") {
    const logo = state === "new" ? { svg: SAMPLE_LOGO, preview: sanitizeLogo(SAMPLE_LOGO)! } : state === "big" || state === "bad" || state === "busy" ? state : undefined;
    return { interface: { ...(logo && { logo }), ...(state === "dirty" && { tail: "gates" }), ...(state === "new" && { tail: "gate" }), ...(state === "saving" && { tail: "gates", saving: true }), ...(state === "saved" && { saved: true }) } };
  }
  if (tab === "system") return { token: state === "token" ? "form" : state === "made" ? "made" : undefined, reach: reachDemo(state) };
  if (tab === "security") return { security: securityDemo(state) };
  if (tab === "sessions") return { sessions: sessionsDemo(state) };
  if (tab === "audit") return { audit: auditDemo(state) };
  if (tab === "backups") return { backups: backupsDemo(state) };
  return {};
};

// ---- the made-up data

const at = (h: number, m: number) => {
  const d = new Date();
  d.setHours(h, m, 0, 0);
  return BigInt(d.getTime());
};
const LOGS = [
  { seq: 8n, timeUnixMs: at(14, 19), level: "info", component: "http", message: "request served", code: "", requestId: "", attrs: { method: "POST", path: "/api/FleetService/Overview", status: "200", ms: "14" } },
  { seq: 7n, timeUnixMs: at(14, 18), level: "info", component: "agent", message: "heartbeat", code: "", requestId: "", attrs: { node: "de1", rtt_ms: "13" } },
  { seq: 6n, timeUnixMs: at(14, 13), level: "warn", component: "checks", message: "round failed for inb_fi1_vr: timeout", code: "", requestId: "", attrs: { node: "fi1", fail_streak: "3" } },
  { seq: 5n, timeUnixMs: at(14, 9), level: "info", component: "updates", message: "release check done, v0.1.86 available", code: "", requestId: "", attrs: {} },
  { seq: 4n, timeUnixMs: at(14, 5), level: "info", component: "agent", message: "node connected", code: "", requestId: "", attrs: { node: "nl1" } },
  { seq: 3n, timeUnixMs: at(14, 1), level: "error", component: "agent", message: "node stream closed: i/o timeout", code: "stream_closed", requestId: "abc12345", attrs: { node: "nl1" } },
  { seq: 2n, timeUnixMs: at(13, 20), level: "info", component: "backup", message: "backup finished", code: "", requestId: "", attrs: { bytes: "9400210" } },
  { seq: 1n, timeUnixMs: at(12, 18), level: "info", component: "http", message: "login ok", code: "", requestId: "", attrs: { method: "password" } },
];
const RANK: Record<string, number> = { debug: 0, info: 1, warn: 2, error: 3 };
const MB = 1024 * 1024;
const GB = 1024 * MB;
const resources = (hot: boolean, none: boolean) => ({
  process: { cpuPct: hot ? 61 : 2.4, rssBytes: (hot ? 412 : 212) * MB, goHeapBytes: (hot ? 268 : 92) * MB, goTotalBytes: (hot ? 344 : 168) * MB, openFds: 311, fdLimit: 65536, goroutines: 388, threads: 24, uptimeS: 12 * 86400 },
  host: none
    ? { cpus: 0, cpuBusyPct: 0, stealPct: 0, iowaitPct: 0, load1: 0, ramTotalBytes: 0, ramUsedBytes: 0, swapTotalBytes: 0, swapUsedBytes: 0, diskTotalBytes: 0, diskUsedBytes: 0 }
    : { cpus: 2, cpuBusyPct: hot ? 94 : 12, stealPct: hot ? 6.1 : 0.3, iowaitPct: hot ? 3.2 : 0.4, load1: hot ? 3.84 : 0.42, ramTotalBytes: 4 * GB, ramUsedBytes: (hot ? 3.6 : 1.7) * GB, swapTotalBytes: 2 * GB, swapUsedBytes: (hot ? 1.4 * GB : 60 * MB), diskTotalBytes: 40 * GB, diskUsedBytes: 9.4 * GB },
  database: { fileBytes: 86 * MB, walBytes: 4 * MB, pageSize: 4096, pageCount: 21504, freelistCount: 1204, measuredUnix: NOW - 120 },
  rpc: { requests: 1840, serverErrors: hot ? 14 : 0, clientErrors: 6, p95Ms: hot ? 920 : 38 },
  agentsConnected: 4,
  nodes: 4,
});
const RETENTION = [
  { key: "host_history_days", value: 14, defaultValue: 14, min: 2, max: 90, unit: "days" },
  { key: "traffic_bucket_days", value: 90, defaultValue: 90, min: 7, max: 730, unit: "days" },
  { key: "node_traffic_hour_days", value: 30, defaultValue: 30, min: 2, max: 365, unit: "days" },
];
const updatesData = () => ({
  nowUnix: NOW,
  panel: {
    version: "v0.1.71",
    built: NOW - 17 * 86400,
    hasReleaseKey: true,
    releaseKeyFingerprint: "0f3a9c21",
    update: { version: "v0.1.86", url: "https://github.com/example/mistgate/releases/tag/v0.1.86", publishedUnix: NOW - 86400, checkedUnix: NOW - 600, available: true, supported: true, installable: true, installing: false, errorKey: "" },
  },
  bundle: { status: BundleStatus.TRUSTED, version: "v0.1.71", built: NOW - 17 * 86400 },
  nodes: [],
  scheduleTimezoneOffsetMinutes: 180,
  updateDownloadTimeoutSeconds: 1800,
  updateDownloadSources: ["github"],
  updateChannel: "stable",
});
const instanceData = (role: Role, nosub: boolean) => ({
  instance: {
    brandHead: "mist",
    brandTail: "gate",
    accent: "#7dd3a0",
    language: "ru",
    hasLogo: false,
    logoVersion: "",
    adminUrl: role === Role.OWNER ? "https://panel.example.com/k7q2vx9m/" : "",
    subscriptionBase: role === Role.OWNER && !nosub ? "https://sub.example.com" : "",
  },
});

const logPage = (req: { minLevel?: string; query?: string; component?: string; code?: string; requestId?: string }) => {
  const min = RANK[req.minLevel ?? "info"] ?? 1;
  const q = (req.query ?? "").toLowerCase();
  const entries = LOGS.filter((l) => (RANK[l.level] ?? 1) >= min && (!q || `${l.component} ${l.message}`.toLowerCase().includes(q)) && (!req.component || l.component.includes(req.component)) && (!req.code || l.code === req.code) && (!req.requestId || l.requestId === req.requestId));
  return { level: "info", buffered: 1840, capacity: 5000, nextCursor: 0n, entries };
};

// the sites WARP is checked against: made-up names (the real built-in list is the panel's)
const REACH_BUILTIN = ["example.com", "example.org", "example.net", "docs.example.com"];
const REACH_OWN = ["news.example.org", "video.example.net", "chat.example.com"];
const REACH_FULL = ["a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com", "f.example.com", "g.example.com", "h.example.com"];
/** The panel's answer of GetWarpReachSettings in a scene: the built-in list unless the scene's state says the owner changed it. */
export const reachData = (state: string) => {
  const own = state === "warp-own" || state === "warp-bad" || state === "warp-refused" || state === "warp-busy" || state === "warp-typed";
  return {
    settings: { domains: state === "warp-off" ? [] : state === "warp-full" ? REACH_FULL : own ? REACH_OWN : REACH_BUILTIN },
    defaults: { domains: REACH_BUILTIN },
    customised: own || state === "warp-off" || state === "warp-full",
  };
};
/** What the card is opened with besides its data (a list edited, a name typed, a refusal, a call under way). */
const reachDemo = (state: string): ReachDemo | undefined => {
  if (state === "warp-bad") return { draft: [...REACH_OWN, "https://example.com/x", "example"] };
  if (state === "warp-many") return { draft: [...REACH_FULL, "i.example.com"] };
  if (state === "warp-typed") return { typed: "mail.example.com" };
  if (state === "warp-refused") return { draft: [...REACH_OWN, "https://example.com/x"], refused: "reach_domain_invalid" };
  if (state === "warp-busy") return { draft: [...REACH_OWN, "mail.example.com"], busy: "save" };
  return undefined;
};

/** What the panel's calls of this screen answer in a scene (the scene is its own page, so patching the shared client is safe), and which ones never do. */
function fakeCalls(v: Variant, nosub: boolean, hot: boolean, none: boolean) {
  const set = (api: unknown, fns: Record<string, unknown>) => Object.assign(api as object, fns);
  const hang = () => new Promise<never>(() => {});
  const fail = () => Promise.reject(new Error("unavailable"));
  const stalls = (tab: PageId) => (v.tab === tab && v.state === "load" ? hang : v.tab === tab && v.state === "err" ? fail : null);
  set(instanceApi, {
    getInstance: stalls("interface") ?? stalls("domains") ?? (async () => instanceData(v.role, nosub)),
    updateInstance: async (r: Record<string, unknown>) => ({ instance: { ...instanceData(v.role, nosub).instance, ...Object.fromEntries(Object.entries(r).filter(([k, x]) => x !== undefined && k !== "logoSvg")), hasLogo: !!r.logoSvg } }),
  });
  const calm = async () => updatesData();
  set(updatesApi, {
    getUpdates: stalls("system") ?? calm,
    setUpdateTimezone: async () => ({}),
    setUpdateDownloadTimeout: async () => ({}),
    setUpdateDownloadSources: async () => ({}),
    checkPanelUpdate: async () => ({}),
  });
  set(opsApi, {
    getLogSettings: v.state === "nologs" ? () => Promise.reject(new ConnectError("not available on this edition", Code.Unimplemented)) : async () => ({ level: "info", defaultLevel: "info", format: "text", buffered: 1840, capacity: 5000 }),
    setLogLevel: async (r: { level: string }) => ({ level: r.level }),
    getPanelLogs: async (r: Parameters<typeof logPage>[0]) => logPage(r),
    getRetention: async () => ({ items: RETENTION }),
    setRetention: async () => ({ items: RETENTION }),
    getMetricsSettings: async () => ({ enabled: true, hasToken: true }),
    setMetricsSettings: async () => ({ enabled: true, hasToken: true, token: "mgm_example_3f9a1c7e5b2d4086a1f3c5e7" }),
    getPanelResources: async () => resources(hot, none),
    exportData: async function* () {
      yield { data: new TextEncoder().encode('{"event":"example"}\n') };
    },
  });
  set(warpReachApi, {
    getWarpReachSettings: v.state === "warp-load" ? hang : v.state === "warp-err" ? fail : async () => reachData(v.state),
    updateWarpReachSettings: async (r: { settings?: { domains: string[] }; useDefaults?: boolean }) => (r.useDefaults ? { settings: { domains: REACH_BUILTIN }, customised: false } : { settings: { domains: r.settings?.domains ?? [] }, customised: true }),
  });
  set(authApi, {
    listPasskeys: stalls("admins") ?? stalls("security") ?? (async () => ({ passkeys: passkeys(v.state) })),
    getPasswordLogin: async () => passwordLogin(v.state),
    getSecuritySettings: async () => securitySettings(v.state),
    // the made-up panel keeps nothing: the saves answer at once with what they were told
    updateSecuritySettings: async (r: Record<string, unknown>) => ({ ...securitySettings(v.state), ...Object.fromEntries(Object.entries(r).filter(([, x]) => x !== undefined && x !== "")), turnstileSecretSet: r.turnstileSecretKey !== "" }),
    beginAddPasskey: () => hang(),
    removePasskey: async () => ({}),
    changePassword: async () => ({ endedSessions: 2 }),
    beginTotpEnrollment: () => hang(),
    listSessions: stalls("sessions") ?? (async () => ({ sessions: sessions(v.state) })),
    endSession: async () => ({}),
    endOtherSessions: async () => ({}),
    listAudit:
      stalls("audit") ??
      (async (r: { source?: AuditSource; kind?: AuditKind }) => {
        if (v.role !== Role.OWNER) throw new ConnectError("owner only", Code.PermissionDenied);
        return auditPage(r, v.state);
      }),
  });
  set(backupsApi, {
    getBackupSettings: stalls("backups") ?? (async () => ({ settings: backupSettings(v.state) })),
    listBackups: async () => ({ backups: backupList(v.state) }),
    updateBackupSettings: async () => ({ settings: backupSettings(v.state) }),
    testBackupStorage: async () => ({ ok: true }),
    createBackup: async () => ({}),
    getRestoreResult: async () => restoreResult(v.state),
    listKeptData: async () => ({ directories: keptData(v.state) }),
    deleteKeptData: async () => ({}),
    prepareRestore: () => hang(),
    confirmRestore: () => hang(),
  });
}

/** The private query cache of a scene: the shell's facts, then every tab's data (the tab picked inside the scene finds its own). */
export function seeded(v: Variant) {
  const hot = v.state === "hot";
  const none = v.state === "none";
  const nosub = v.state === "nosub";
  fakeCalls(v, nosub, hot, none);
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, retryDelay: 0, refetchOnWindowFocus: false, refetchInterval: false } } });
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role: v.role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: 0, ...(v.edge && { edition: "edge", unavailable: ["backups", "panel_update", "health_checks"] }) } as never);
  c.setQueryData(nodesQuery.queryKey, { nodes: nodeRows(), expectedAgentVersion: "0.1.71" } as never);
  c.setQueryData(overviewQuery().queryKey, { nodesTotal: NODES.length, nodes: nodeRows().map((n) => ({ id: n.id, name: n.name, countryCode: n.countryCode, location: n.location, status: NodeStatus.ONLINE, online: [], sparkBytes: [] })), alertsActive: 0, alertsCritical: 0 } as never);
  c.setQueryData(userCountQuery.queryKey, { counts: { all: 140, online: 31, devicesUsed: 0, devicesOnline: 0 } } as never);
  c.setQueryData(awaitingQuery.queryKey, { awaiting: 0 } as never);
  c.setQueryData(checksQuery("").queryKey, checksOf({}) as never);
  const stalled = (tab: PageId) => v.tab === tab && (v.state === "load" || v.state === "err");
  if (!stalled("interface") && !stalled("domains")) c.setQueryData(instanceQuery.queryKey, instanceData(v.role, nosub) as never);
  if (!stalled("system")) c.setQueryData(updatesQuery.queryKey, updatesData() as never);
  if (v.state !== "nologs") c.setQueryData(logSettingsQuery.queryKey, { level: "info", defaultLevel: "info", format: "text", buffered: 1840, capacity: 5000 } as never);
  c.setQueryData(logsQuery(defaultLogFilter, false).queryKey, logPage({ minLevel: "info" }) as never);
  if (v.state !== "warp-load" && v.state !== "warp-err") c.setQueryData(warpReachQuery.queryKey, reachData(v.state) as never);
  c.setQueryData(retentionQuery.queryKey, { items: RETENTION } as never);
  c.setQueryData(metricsQuery.queryKey, { enabled: true, hasToken: true } as never);
  c.setQueryData(panelResourcesQuery.queryKey, resources(hot, none) as never);
  // Part B: Security, Sessions, Audit and Backups
  if (!stalled("security") && !stalled("admins")) c.setQueryData(passkeysQuery.queryKey, { passkeys: passkeys(v.state) } as never);
  if (!stalled("admins")) c.setQueryData(passwordLoginQuery.queryKey, passwordLogin(v.state) as never);
  c.setQueryData(securityQuery.queryKey, securitySettings(v.state) as never);
  if (!stalled("sessions")) c.setQueryData(sessionsQuery.queryKey, { sessions: sessions(v.state) } as never);
  if (!stalled("audit") && v.role === Role.OWNER) c.setQueryData(auditFilter(v.state).key, auditPage(auditFilter(v.state).req, v.state) as never);
  if (!stalled("backups") && v.role === Role.OWNER) {
    c.setQueryData(backupSettingsKey, backupSettings(v.state) as never);
    c.setQueryData(backupsKey, backupList(v.state) as never);
    c.setQueryData(["backups", "restore-result"], { ...restoreResult(v.state), recent: true } as never);
    c.setQueryData(["backups", "kept"], keptData(v.state) as never);
  }
  return c;
}

/** The frame and the tab of a scene (no shell): the tab picked inside the scene is drawn from the same private cache. */
export function SettingsSceneBody({ tab, state }: { tab: PageId; state: string }) {
  const [section, setSection] = useState<PageId>(tab);
  const demo = demoOf(section, section === tab ? state : "");
  return (
    <SettingsFrame section={section} onPick={(id) => isPage(id) && setSection(id)}>
      {section === "interface" ? (
        <InterfacePage demo={demo.interface} />
      ) : section === "system" ? (
        <SystemPage demo={{ token: demo.token, reach: demo.reach }} />
      ) : section === "security" ? (
        <SecurityPage demo={demo.security} />
      ) : section === "sessions" ? (
        <SessionsPage demo={demo.sessions} />
      ) : section === "audit" ? (
        <AuditPage demo={demo.audit} />
      ) : section === "backups" ? (
        <BackupsPage demo={demo.backups} />
      ) : (
        <SettingsTab id={section} />
      )}
    </SettingsFrame>
  );
}

/** The parts of a scene's address: the tab, its state and the role (the scene's own cache is made from them). */
export function variantOf(params: URLSearchParams): Variant {
  const role = params.get("role") === "helper" ? Role.HELPER : params.get("role") === "readonly" ? Role.READONLY : Role.OWNER;
  return { tab: TAB[params.get("tab") ?? "if"] ?? "interface", state: params.get("state") ?? "", role, edge: params.get("edge") === "1" };
}

/** One whole page of the scene: the screen in the real shell, at the window's own width. */
export function SettingsScenePage() {
  const params = new URLSearchParams(location.search);
  const v = variantOf(params);
  const [qc] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    setEffectsEnabled(params.get("fx") === "1", false);
    // the board's "buffer" switch: the clipboard refuses, and the page asks for a copy by hand
    if (params.get("copy") === "fail") Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: () => Promise.reject(new Error("denied")) } });
    return seeded(v);
  });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AddNodeProvider>
          <ShellLayout current="settings" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
            <SettingsSceneBody tab={v.tab} state={v.state} />
          </ShellLayout>
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

// ---- the gallery

export type Frame = { id: string; tab: string; state?: string; title: string; w: number; h: number; params?: string };
const f = (id: string, tab: string, title: string, h: number, state = "", w = 1440, params = ""): Frame => ({ id, tab, state, title, w, h, params });

/** The frames of Interface, System, Admins and Domains (the part A tabs). */
export const frames: Frame[] = [
  f("settings-if", "if", "Interface: the board's 'ready' (owner, 1440)", 1000),
  f("settings-if-light", "if", "The same on the light theme", 1000, "", 1440, "&theme=light"),
  f("settings-if-fa", "if", "Right to left (fa)", 1000, "", 1440, "&lang=fa"),
  f("settings-if-1000", "if", "A 1000 window", 1200, "", 1000),
  f("settings-if-phone", "if", "A phone, 400 wide", 1500, "", 400),
  f("settings-if-load", "if", "Data: loading", 640, "load"),
  f("settings-if-err", "if", "Data: failed", 520, "err"),
  f("settings-if-big", "if", "Logo: an SVG over 64 KB", 1100, "big"),
  f("settings-if-bad", "if", "Logo: not an SVG the panel can use", 1100, "bad"),
  f("settings-if-busy", "if", "Logo: being read", 1000, "busy"),
  f("settings-if-new", "if", "Logo: uploaded, not saved yet", 1000, "new"),
  f("settings-if-dirty", "if", "A change in the name, not saved yet", 1000, "dirty"),
  f("settings-if-saving", "if", "Saving", 1000, "saving"),
  f("settings-if-saved", "if", "Saved: the button says so for two seconds", 1000, "saved"),
  f("settings-if-helper", "if", "Role: a helper (the card for everyone is read-only)", 1000, "", 1440, "&role=helper"),
  f("settings-if-readonly", "if", "Role: an observer", 1000, "", 1440, "&role=readonly"),
  f("settings-if-fx", "if", "Reveal animation on: the moments under its switch", 1100, "", 1440, "&fx=1"),
  f("settings-if-edge", "if", "The Cloudflare edition: no reveal animation", 900, "", 1440, "&edge=1"),
  f("settings-sys", "sys", "System: the board's 'calm' (owner, 1440)", 2000),
  f("settings-sys-light", "sys", "The same on the light theme", 2000, "", 1440, "&theme=light"),
  f("settings-sys-fa", "sys", "Right to left (fa)", 2100, "", 1440, "&lang=fa"),
  f("settings-sys-1000", "sys", "A 1000 window", 2400, "", 1000),
  f("settings-sys-phone", "sys", "A phone, 400 wide", 3600, "", 400),
  f("settings-sys-hot", "sys", "Server: under load", 2000, "hot"),
  f("settings-sys-load", "sys", "Data: loading", 640, "load"),
  f("settings-sys-err", "sys", "Data: failed", 520, "err"),
  f("settings-sys-helper", "sys", "Role: a helper (no logs, no retention, no metrics; the settings are read-only)", 1400, "", 1440, "&role=helper"),
  f("settings-sys-readonly", "sys", "Role: an observer", 1400, "", 1440, "&role=readonly"),
  f("settings-sys-none", "sys", "A server that is not Linux: no CPU, memory, disk", 2000, "none"),
  f("settings-sys-nologs", "sys", "The Cloudflare edition: the diagnostics are not there", 1500, "nologs", 1440, "&edge=1"),
  f("settings-sys-warp-own", "sys", "WARP sites: the owner's own list", 2000, "warp-own"),
  f("settings-sys-warp-off", "sys", "WARP sites: an empty list (the check is off)", 2000, "warp-off"),
  f("settings-sys-warp-full", "sys", "WARP sites: eight names (the most)", 2000, "warp-full"),
  f("settings-sys-warp-bad", "sys", "WARP sites: names the panel would refuse (hints)", 2000, "warp-bad"),
  f("settings-sys-warp-many", "sys", "WARP sites: nine names (hint)", 2000, "warp-many"),
  f("settings-sys-warp-typed", "sys", "WARP sites: a name typed, not added yet", 2000, "warp-typed"),
  f("settings-sys-warp-refused", "sys", "WARP sites: the panel refused the list (by its code)", 2000, "warp-refused"),
  f("settings-sys-warp-busy", "sys", "WARP sites: saving", 2000, "warp-busy"),
  f("settings-sys-warp-load", "sys", "WARP sites: loading", 2000, "warp-load"),
  f("settings-sys-warp-err", "sys", "WARP sites: failed to load", 2000, "warp-err"),
  f("settings-sys-warp-edge", "sys", "WARP sites: the Cloudflare edition (not available there)", 1500, "nologs", 1440, "&edge=1"),
  f("settings-sys-warp-phone", "sys", "WARP sites on a phone, 400 wide", 3800, "warp-own", 400),
  f("settings-sys-warp-fa", "sys", "WARP sites, right to left (fa)", 2100, "warp-own", 1440, "&lang=fa"),
  f("settings-sys-warp-light", "sys", "WARP sites on the light theme", 2000, "warp-own", 1440, "&theme=light"),
  f("settings-sys-token", "sys", "Window: a new metrics token", 1000, "token"),
  f("settings-sys-made", "sys", "Window: the token made, shown once", 1000, "made"),
  f("settings-adm", "adm", "Admins (owner, 1440)", 640),
  f("settings-adm-light", "adm", "The same on the light theme", 640, "", 1440, "&theme=light"),
  f("settings-adm-phone", "adm", "A phone, 400 wide", 760, "", 400),
  f("settings-adm-load", "adm", "Data: loading", 640, "load"),
  f("settings-adm-err", "adm", "Data: failed", 520, "err"),
  f("settings-adm-helper", "adm", "Role: a helper", 640, "", 1440, "&role=helper"),
  f("settings-dom", "dom", "Domains (owner, 1440)", 640),
  f("settings-dom-light", "dom", "The same on the light theme", 640, "", 1440, "&theme=light"),
  f("settings-dom-fa", "dom", "Right to left (fa)", 640, "", 1440, "&lang=fa"),
  f("settings-dom-phone", "dom", "A phone, 400 wide", 800, "", 400),
  f("settings-dom-nosub", "dom", "No public address for the subscription links", 640, "nosub"),
  f("settings-dom-load", "dom", "Data: loading", 640, "load"),
  f("settings-dom-err", "dom", "Data: failed", 520, "err"),
  f("settings-dom-helper", "dom", "Role: a helper (the owner sees the addresses)", 520, "", 1440, "&role=helper"),
  f("settings-dom-copy", "dom", "Buffer: the copy did not work (press Copy)", 640, "", 1440, "&copy=fail"),
];

/** Part B: the frames of Security, Sessions, Audit and Backups, and their windows (every state of the board's switches, and the states of the product it does not draw). */
export const partB: Frame[] = [
  // Security (the board's "Вход": usual, the lock, no secret key; the windows of a passkey, the password, the secret key)
  f("settings-sec", "sec", "Security: the board's 'usual' (owner, 1440)", 900),
  f("settings-sec-light", "sec", "The same on the light theme", 900, "", 1440, "&theme=light"),
  f("settings-sec-fa", "sec", "Right to left (fa)", 900, "", 1440, "&lang=fa"),
  f("settings-sec-1000", "sec", "A 1000 window", 1200, "", 1000),
  f("settings-sec-phone", "sec", "A phone, 400 wide", 1500, "", 400),
  f("settings-sec-load", "sec", "Data: loading", 640, "load"),
  f("settings-sec-err", "sec", "Data: failed", 520, "err"),
  f("settings-sec-lock", "sec", "Sign-in: the password locked for 15 minutes", 900, "lock"),
  f("settings-sec-nosec", "sec", "Sign-in: the check is on, no secret key", 900, "nosec"),
  f("settings-sec-tsoff", "sec", "The Cloudflare check is off", 800, "tsoff"),
  f("settings-sec-proof", "sec", "New keys: the proof before they are saved", 1000, "ts-proof"),
  f("settings-sec-nopw", "sec", "A passkey admin: no password login yet", 800, "nopw"),
  f("settings-sec-nokey", "sec", "A server with no master key: a password login cannot be kept", 800, "nokey"),
  f("settings-sec-nopk", "sec", "No passkeys", 800, "nopk"),
  f("settings-sec-new", "sec", "A passkey just added: it glows once", 900, "pk-new"),
  f("settings-sec-helper", "sec", "Role: a helper (no Cloudflare card: the server refuses it)", 800, "", 1440, "&role=helper"),
  f("settings-sec-readonly", "sec", "Role: an observer (his own passkeys and password work)", 800, "", 1440, "&role=readonly"),
  f("settings-sec-copy", "sec", "Buffer: the copy did not work (press Copy)", 900, "", 1440, "&copy=fail"),
  f("settings-sec-pq", "sec", "Window: add a passkey", 800, "pq"),
  f("settings-sec-pq-err", "sec", "Window: add a passkey, the name is empty", 800, "pq-err"),
  f("settings-sec-pq-busy", "sec", "Window: waiting for the device", 800, "pq-busy"),
  f("settings-sec-pk-del", "sec", "Window: remove a passkey", 800, "pk-del"),
  f("settings-sec-pw", "sec", "Window: change the password", 800, "pw"),
  f("settings-sec-pw-err", "sec", "Window: change the password, both fields refused", 800, "pw-err"),
  f("settings-sec-pw-busy", "sec", "Window: changing the password", 800, "pw-busy"),
  f("settings-sec-pw-add", "sec", "Window: add the password login (login and password)", 800, "pw-add"),
  f("settings-sec-pw-add-qr", "sec", "Window: add the password login (the app's code)", 900, "pw-add-qr"),
  f("settings-sec-pw-rebind", "sec", "Window: re-bind the authenticator app", 900, "pw-rebind"),
  f("settings-sec-ts-del", "sec", "Window: remove the secret key", 800, "ts-del"),
  // Sessions
  f("settings-ses", "ses", "Sessions: the board's '3 sessions' (owner, 1440)", 560),
  f("settings-ses-light", "ses", "The same on the light theme", 560, "", 1440, "&theme=light"),
  f("settings-ses-fa", "ses", "Right to left (fa)", 560, "", 1440, "&lang=fa"),
  f("settings-ses-phone", "ses", "A phone, 400 wide", 720, "", 400),
  f("settings-ses-one", "ses", "Only this one", 400, "one"),
  f("settings-ses-end", "ses", "Window: end the other sessions", 700, "end"),
  f("settings-ses-load", "ses", "Data: loading", 640, "load"),
  f("settings-ses-err", "ses", "Data: failed", 520, "err"),
  f("settings-ses-helper", "ses", "Role: a helper (his own sessions)", 560, "", 1440, "&role=helper"),
  // Audit
  f("settings-aud", "aud", "Audit: the board's 'there are records' (owner, 1440)", 940),
  f("settings-aud-light", "aud", "The same on the light theme", 940, "", 1440, "&theme=light"),
  f("settings-aud-fa", "aud", "Right to left (fa)", 940, "", 1440, "&lang=fa"),
  f("settings-aud-1000", "aud", "A 1000 window", 960, "", 1000),
  f("settings-aud-phone", "aud", "A phone, 400 wide", 1500, "", 400),
  f("settings-aud-in", "aud", "Filter: sign-ins", 500, "in"),
  f("settings-aud-ch", "aud", "Filter: changes", 940, "ch"),
  f("settings-aud-fail", "aud", "Filter: failures", 460, "fail"),
  f("settings-aud-mcp", "aud", "Filter: the source MCP", 480, "mcp"),
  f("settings-aud-nomatch", "aud", "Filters that match nothing", 520, "nomatch"),
  f("settings-aud-none", "aud", "No records at all", 480, "none"),
  f("settings-aud-paged", "aud", "The log goes on: the pager under the rows", 1000, "paged"),
  f("settings-aud-load", "aud", "Data: loading", 640, "load"),
  f("settings-aud-err", "aud", "Data: failed", 520, "err"),
  f("settings-aud-helper", "aud", "Role: a helper (the log is the owner's)", 420, "", 1440, "&role=helper"),
  // Backups
  f("settings-bk", "bk", "Backups: the board's 'set up' (owner, 1440)", 2300),
  f("settings-bk-light", "bk", "The same on the light theme", 2300, "", 1440, "&theme=light"),
  f("settings-bk-fa", "bk", "Right to left (fa)", 2300, "", 1440, "&lang=fa"),
  f("settings-bk-1000", "bk", "A 1000 window", 2800, "", 1000),
  f("settings-bk-phone", "bk", "A phone, 400 wide", 4200, "", 400),
  f("settings-bk-none", "bk", "Backups: not set up", 1700, "none"),
  f("settings-bk-fail", "bk", "Backups: the last one failed", 2300, "fail"),
  f("settings-bk-manual", "bk", "Backups: the schedule is off", 2300, "manual"),
  f("settings-bk-r2bad", "bk", "Backups: R2 does not let the panel in", 2400, "r2bad"),
  f("settings-bk-r2busy", "bk", "R2 is being checked", 2300, "r2busy"),
  f("settings-bk-r2ok", "bk", "R2 works: the button says so", 2300, "r2ok"),
  f("settings-bk-running", "bk", "A backup is being made", 2300, "running"),
  f("settings-bk-new", "bk", "The new backup glows once in the list", 2300, "new"),
  f("settings-bk-nobks", "bk", "Set up, no backups yet", 2200, "nobks"),
  f("settings-bk-dl", "bk", "The file is being prepared", 2300, "dl"),
  f("settings-bk-secshown", "bk", "The secret typed is shown", 2300, "secshown"),
  f("settings-bk-lastres", "bk", "The last restore worked; the data kept from before it", 2500, "lastres"),
  f("settings-bk-lastfail", "bk", "The last restore failed", 2400, "lastfail"),
  f("settings-bk-kept", "bk", "Data kept from before a restore", 2400, "kept"),
  f("settings-bk-load", "bk", "Data: loading", 640, "load"),
  f("settings-bk-err", "bk", "Data: failed", 520, "err"),
  f("settings-bk-helper", "bk", "Role: a helper (only the owner)", 420, "", 1440, "&role=helper"),
  f("settings-bk-edge", "bk", "The Cloudflare edition: no backups", 480, "", 1440, "&edge=1"),
  f("settings-bk-copy", "bk", "Buffer: the copy did not work (press Copy)", 2300, "", 1440, "&copy=fail"),
  f("settings-bk-fx", "bk", "Reveal animation on", 2300, "new", 1440, "&fx=1"),
  f("settings-bk-pass", "bk", "Window: a passphrase for the file", 1000, "pass"),
  f("settings-bk-secret", "bk", "Window: delete the saved secret access key", 800, "secret"),
  f("settings-bk-rs-pick", "bk", "Window: restore, the key of a backup picked in the list", 900, "rs-pick"),
  f("settings-bk-rs-chk", "bk", "Window: restore, checking the backup", 800, "rs-chk"),
  f("settings-bk-rs-ok", "bk", "Window: restore, what is in the backup", 1000, "rs-ok"),
  f("settings-bk-rs-typed", "bk", "Window: restore, the name typed", 1000, "rs-typed"),
  f("settings-bk-rs-run", "bk", "Window: restore, running", 800, "rs-run"),
  f("settings-bk-rs-back", "bk", "Window: restore, the panel is back", 800, "rs-back"),
  f("settings-bk-rs-late", "bk", "Window: restore, the panel has not answered yet", 800, "rs-late"),
  f("settings-bk-rs-wrong", "bk", "Window: restore, the key did not open the backup", 900, "rs-wrong"),
  f("settings-bk-rs-file", "bk", "Window: restore from a file, a passphrase", 1000, "rs-file"),
  f("settings-bk-rs-upload", "bk", "Window: restore from a file, uploading", 800, "rs-upload"),
];

export const scenes: Frame[] = [...frames, ...partB];

/** The query of a frame's page: what `SettingsScenePage` reads. */
export const addressOf = (s: Frame) => `tab=${s.tab}${s.state ? `&state=${s.state}` : ""}${s.params ?? ""}`;

export function SettingsKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Settings</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {scenes.map((s) => (
        <Shot key={s.id} id={s.id} title={s.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={s.title} src={`${path}?only=settings-page&${addressOf(s)}`} width={s.w} height={s.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
