import { describe, expect, it } from "vitest";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity, CheckStatus, DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { fill, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { healthKind } from "./fleet";
import { makeFmt } from "./format";
import {
  alertFix,
  alertTitle,
  alertWhy,
  barsOf,
  cellLook,
  checkError,
  doctorTotals,
  fixErrorText,
  fixLabel,
  fleetIssues,
  groupDoctor,
  hasKey,
  isIssue,
  isLoud,
  itemFix,
  itemTitle,
  itemWhy,
  lookup,
  manualSteps,
  muteChoices,
  resolutionWord,
  restartConsequence,
  severityKind,
  type Alert,
  type CheckCell,
  type DoctorItem,
  type NodeDoctor,
} from "./health";

const t = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(en[key], vars), { n: () => "" });
const tRu = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(ru[key], vars), { n: () => "" });
const fmt = makeFmt("en", t as unknown as T);
const fmtRu = makeFmt("ru", tRu as unknown as T);

const alert = (over: Partial<Alert> = {}): Alert => ({
  id: "alt_1",
  severity: AlertSeverity.WARNING,
  kind: AlertKind.CHECK_FAILED,
  nodeId: "nod_1",
  nodeName: "de1",
  subject: "",
  titleKey: "health.alert.check_failed.title",
  params: {},
  whyKey: "",
  firstSeenUnix: 100,
  lastSeenUnix: 100,
  resolvedAtUnix: 0,
  resolution: "",
  mutedUntilUnix: 0,
  actions: [],
  ...over,
});

const item = (over: Partial<DoctorItem> = {}): DoctorItem => ({
  id: "disk_space",
  status: DoctorStatus.OK,
  titleKey: "doctor.disk_space.title",
  detail: "",
  detailCode: "",
  params: {},
  whyKey: "",
  fixId: "",
  measuredUnix: 1,
  acceptedUnix: 0,
  acceptedBy: "",
  acceptedByName: "",
  ...over,
});

const cell =(over: Partial<CheckCell> = {}): CheckCell => ({ deployed: true, inboundId: "inb_1", failStreak: 0, history: [], ...over });
const at = (status: CheckStatus, latencyMs = 0, errorCode = "") => ({ status, atUnix: 1, latencyMs, exitIp: "", exitCountry: "", errorCode, errorDetail: "" });

describe("lookup", () => {
  it("fills the placeholders of a known key", () => {
    expect(lookup(t, "health.alert.check_failed.why.timeout", { profile: "hy2 · 443", port: 443 })).toContain("“hy2 · 443” does not complete the handshake on port 443");
  });
  it("falls back from an unknown variant to .unknown, then to the plain key, then to null", () => {
    expect(lookup(t, "health.alert.no_traffic.why.brand_new_code")).toBe(en["health.alert.no_traffic.why.unknown"]);
    expect(lookup(t, "health.doctor.dstate_tasks.why.some_hint")).toBe(fill(en["health.doctor.dstate_tasks.why"], {}));
    expect(lookup(t, "health.doctor.future_check.why")).toBeNull();
    expect(lookup(t, "")).toBeNull();
  });
});

describe("alerts", () => {
  it("names a doctor alert after its check and a blind-panel alert without a profile", () => {
    const warn = alert({ kind: AlertKind.DOCTOR_WARN, titleKey: "health.alert.doctor_warn.title", params: { check: "disk_space" } });
    expect(alertTitle(t, warn)).toBe("Disk space");
    const egress = alert({ subject: "panel_egress", nodeId: "", params: { failed: "5", total: "6", providers: "3" } });
    expect(alertTitle(t, egress)).toBe(en["health.alert.check_failed.title.panel_egress"]);
    expect(alertTitle(t, alert({ params: { profile: "hy2 · 443" } }))).toContain("hy2 · 443");
  });
  it("shows an unknown title key as it is and no reason for an unknown why key", () => {
    const a = alert({ titleKey: "health.alert.brand_new.title", whyKey: "health.alert.brand_new.why" });
    expect(alertTitle(t, a)).toBe("health.alert.brand_new.title");
    expect(alertWhy(t, fmt, a)).toBe("");
  });
  it("reads the fix action, and restart_inbound names its inbound (inbound_id of a doctor row, else inbound of a check)", () => {
    expect(alertFix(alert({ actions: ["open_node", "mute"] }))).toBeNull();
    expect(alertFix(alert({ actions: ["apply_fix:journald_vacuum", "open_node"] }))).toEqual({ fixId: "journald_vacuum", params: {} });
    expect(alertFix(alert({ actions: ["apply_fix:restart_inbound"], params: { inbound_id: "inb_7" } }))).toEqual({ fixId: "restart_inbound", params: { inbound_id: "inb_7" } });
    expect(alertFix(alert({ actions: ["apply_fix:restart_inbound"], params: { inbound: "inb_8" } }))).toEqual({ fixId: "restart_inbound", params: { inbound_id: "inb_8" } });
    expect(itemFix(item({ fixId: "restart_inbound", params: { inbound_id: "inb_2" } }))).toEqual({ fixId: "restart_inbound", params: { inbound_id: "inb_2" } });
    expect(itemFix(item())).toBeNull();
  });
  it("names the profile a restart restarts", () => {
    expect(fixLabel(t, "restart_inbound", "hy2 · WARP · 8443")).toBe("Restart “hy2 · WARP · 8443”");
    expect(fixLabel(tRu, "restart_inbound", "hy2 · WARP · 8443")).toBe("Перезапустить «hy2 · WARP · 8443»");
    expect(fixLabel(t, "restart_inbound")).toBe(en["health.fix.restart_inbound.label"]);
    expect(fixLabel(t, "journald_vacuum", "x")).toBe(en["health.fix.journald_vacuum.label"]);
  });
  it("says who drops on a restart, and nothing made up when the panel did not count", () => {
    expect(restartConsequence(tRu, "one", "3")).toBe("Подключения через этот профиль (сейчас 3) оборвутся на пару секунд и восстановятся сами.");
    expect(restartConsequence(tRu, "one", "0")).toContain("никто не подключён");
    expect(restartConsequence(t, "all", "12")).toContain("(now 12)");
    expect(restartConsequence(t, "one", undefined)).toBeNull();
  });
  it("words a doctor alert like the doctor row: «не работает», the WARP profiles by name", () => {
    const warp = alert({
      kind: AlertKind.DOCTOR_FAIL,
      whyKey: "health.doctor.warp_path.why",
      params: { check: "warp_path", state: "down", profiles: "hy2 · WARP · 8443" },
    });
    const why = alertWhy(tRu, fmtRu, warp);
    expect(why).toContain("WARP на этой ноде: не работает.");
    expect(why).toContain("Через этот выход работают: «hy2 · WARP · 8443».");
    expect(why).not.toMatch(/[{}]/);
  });
  it("names the profile and the domain of a certificate", () => {
    const cert = alert({ kind: AlertKind.CERT_EXPIRY, whyKey: "health.alert.cert_expiry.why", params: { inbound: "inb_1", profile: "hy2 · 443 · Salamander", server_name: "de2.example.com", days_left: "5" } });
    expect(alertWhy(tRu, fmtRu, cert)).toMatch(/^Сертификат «hy2 · 443 · Salamander» \(de2\.example\.com\) истекает через 5 дн\./);
    // an inbound the panel could not name is still said, by its id
    expect(alertWhy(t, fmt, alert({ kind: AlertKind.CERT_EXPIRY, whyKey: "health.alert.cert_expiry.why.expired", params: { inbound_id: "inb_9", reason: "expired" } }))).toContain("“inb_9” has expired");
  });
  it("says how long a node has been down in the coarsest unit, and a paused rollout's reason once", () => {
    expect(alertWhy(t, fmt, alert({ kind: AlertKind.NODE_DOWN, whyKey: "health.alert.node_down.why", params: { minutes: "185" } }))).toContain("for 3 h and");
    const paused = alert({ kind: AlertKind.UPDATE_FAILED, whyKey: "health.alert.update_failed.why.gate_failed", params: { node: "de2", reason: "probe_failed" } });
    expect(alertWhy(t, fmt, paused)).toContain("de2 did not pass the check after updating: the client-eye check failed. Where possible");
  });
  it("offers the mute choices up to the panel's week, «until morning» to the next 08:00", () => {
    const clock = (h: number, m = 0) => new Date(2026, 9, 1, h, m);
    const secs = (d: Date) => muteChoices(d).map((c) => c.seconds);
    expect(secs(clock(22))).toEqual([3600, 10 * 3600, 86_400, 604_800]);
    expect(secs(clock(6, 30))[1]).toBe(90 * 60); // before 08:00: this morning
    expect(secs(clock(8))[1]).toBe(24 * 3600); // at 08:00 sharp: tomorrow's
    expect(muteChoices(clock(12)).map((c) => c.key)).toEqual(["hl.mute.hour", "hl.mute.morning", "hl.mute.day", "hl.mute.week"]);
  });
  it("maps severity to the status kinds; info is never red", () => {
    expect(severityKind(AlertSeverity.CRITICAL)).toBe("bad");
    expect(severityKind(AlertSeverity.WARNING)).toBe("warn");
    expect(severityKind(AlertSeverity.INFO)).toBe("blip");
  });
  it("counts an alert as loud only when it is a warning or worse and not muted", () => {
    expect(isLoud(alert(), 1000)).toBe(true);
    expect(isLoud(alert({ severity: AlertSeverity.INFO }), 1000)).toBe(false);
    expect(isLoud(alert({ mutedUntilUnix: 2000 }), 1000)).toBe(false);
    expect(isLoud(alert({ mutedUntilUnix: 900 }), 1000)).toBe(true);
  });
  it("words a resolution, and an unknown one as plain 'closed'", () => {
    expect(resolutionWord("fix_applied")).toBe("hl.res.fix_applied");
    expect(resolutionWord("something_new")).toBe("hl.res.unknown");
  });
});

describe("healthKind", () => {
  it("is red for a broken node or a critical alert, amber for warnings only, green when quiet", () => {
    expect(healthKind(0, 0, 0)).toBe("ok");
    expect(healthKind(1, 0, 0)).toBe("bad");
    expect(healthKind(0, 2, 1)).toBe("bad");
    expect(healthKind(0, 2, 0)).toBe("warn");
  });
});

describe("cellLook", () => {
  const look = (last: CheckCell["last"], status = NodeStatus.ONLINE) => cellLook(t, cell({ last }), status);
  it("shows a dash for a profile that is not deployed and waits for a first result", () => {
    expect(cellLook(t, cell({ deployed: false }), NodeStatus.ONLINE)).toMatchObject({ kind: "none", label: "—" });
    expect(look(undefined)).toMatchObject({ kind: "off", label: "…" });
  });
  it("shows latency for ok and degraded, the failure for a failed round", () => {
    expect(look(at(CheckStatus.OK, 42))).toMatchObject({ kind: "ok", label: "42 ms" });
    expect(look(at(CheckStatus.DEGRADED, 120, "http_status"))).toMatchObject({ kind: "warn", label: "120 ms" });
    const failed = look(at(CheckStatus.FAILED, 0, "timeout"));
    expect(failed).toMatchObject({ kind: "bad", label: "✕ fails" });
    expect(failed.state).toBe(en["health.check.err.timeout"]);
  });
  it("shows a skipped cell as updating while the node updates, else by why it was skipped", () => {
    expect(look(at(CheckStatus.SKIPPED, 0, "node_offline"), NodeStatus.UPDATING)).toMatchObject({ kind: "busy", label: "upd." });
    expect(look(at(CheckStatus.SKIPPED, 0, "node_offline"), NodeStatus.DOWN)).toMatchObject({ kind: "blip", label: "no link" });
    expect(look(at(CheckStatus.SKIPPED, 0, "client_unsupported"))).toMatchObject({ kind: "off", label: "not checked" });
    expect(look(at(CheckStatus.SKIPPED, 0, "inbound_disabled"))).toMatchObject({ kind: "off", label: "off" });
    expect(look(at(CheckStatus.SKIPPED, 0, "inbound_pending"))).toMatchObject({ kind: "busy", label: "starting" });
  });
  it("shows a profile that failed to start in red, never as a calm 'off'", () => {
    const failed = look(at(CheckStatus.SKIPPED, 0, "inbound_failed"));
    expect(failed).toMatchObject({ kind: "bad", label: "✕ not started" });
    expect(failed.state).toBe(en["health.check.err.inbound_failed"]);
    // an older panel says only "not active": it stays the calm "off" it always was
    expect(look(at(CheckStatus.SKIPPED, 0, "inbound_not_active"))).toMatchObject({ kind: "off", label: "off" });
  });
  it("shows an error code it has no sentence for as it is", () => {
    expect(checkError(t, "weird")).toBe("weird");
    expect(checkError(t, "")).toBe("");
  });
});

describe("barsOf", () => {
  const b = (ok: number, failed: number, latencyMs = 0) => ({ startUnix: 0, ok, failed, latencyMs });
  it("makes a failed bucket full-height red, an empty one a faint stub, the rest as tall as their latency", () => {
    const bars = barsOf([b(0, 0), b(0, 2), b(3, 0, 100), b(3, 0, 50), b(2, 1, 50)]);
    expect(bars.map((x) => x.tone)).toEqual(["none", "bad", "ok", "ok", "warn"]);
    expect(bars[1]!.height).toBe(100);
    expect(bars[0]!.height).toBe(8);
    expect(bars[2]!.height).toBe(100);
    expect(bars[3]!.height).toBe(60);
  });
  it("keeps 48 buckets as 48 bars", () => {
    expect(barsOf(Array.from({ length: 48 }, () => b(1, 0, 30)))).toHaveLength(48);
  });
});

describe("doctor", () => {
  const n = (name: string, items: DoctorItem[], over: Partial<NodeDoctor> = {}): NodeDoctor => ({
    nodeId: `nod_${name}`,
    nodeName: name,
    nodeStatus: NodeStatus.ONLINE,
    agentSupported: true,
    hasReport: true,
    receivedUnix: 1,
    ageS: 10,
    stale: false,
    items,
    agentVersion: "0.3.0",
    lastSeenUnix: 1,
    ...over,
  });
  const warn = item({ id: "time_sync", status: DoctorStatus.WARN });
  const fail = item({ id: "resolver", status: DoctorStatus.FAIL });
  const ok = item({ id: "ipv6", status: DoctorStatus.OK });
  const skip = item({ id: "foreign_nft", status: DoctorStatus.SKIP });

  it("groups a node's items: problems first (fail before warn), then fine, then skipped", () => {
    const g = groupDoctor([ok, warn, skip, fail]);
    expect(g.issues.map((i) => i.id)).toEqual(["resolver", "time_sync"]);
    expect(g.ok.map((i) => i.id)).toEqual(["ipv6"]);
    expect(g.skipped.map((i) => i.id)).toEqual(["foreign_nft"]);
    expect(g.accepted).toEqual([]);
  });
  it("puts a warning the owner accepted aside: not a problem, not counted, in a group of its own", () => {
    const accepted = item({ id: "foreign_vpn", status: DoctorStatus.WARN, acceptedUnix: 1_790_000_000, acceptedBy: "adm_1" });
    expect(isIssue(accepted)).toBe(false);
    const g = groupDoctor([warn, accepted, ok]);
    expect(g.issues.map((i) => i.id)).toEqual(["time_sync"]);
    expect(g.accepted.map((i) => i.id)).toEqual(["foreign_vpn"]);
    const nodes = [n("a", [warn, accepted])];
    expect(fleetIssues(nodes).map((x) => x.item.id)).toEqual(["time_sync"]);
    expect(doctorTotals(nodes).issues).toBe(1);
  });
  it("gives the steps by hand for the checks the panel cannot fix itself", () => {
    expect(manualSteps(item({ id: "time_sync", status: DoctorStatus.WARN }))).toEqual([{ text: "hl.manual.time_sync", command: "timedatectl set-ntp true" }]);
    const port = manualSteps(item({ id: "port_conflicts", status: DoctorStatus.FAIL, params: { network: "udp", port: "8443", inbound_id: "inb_1" } }));
    expect(port.map((s) => s.command)).toEqual(["ss -lupn 'sport = :8443'", undefined]);
    expect(port[1]!.open).toEqual({ tab: "profiles", label: "hl.manual.port_moveBtn" });
    expect(manualSteps(item({ id: "port_conflicts", status: DoctorStatus.FAIL, params: { network: "tcp", port: "443" } }))[0]!.command).toBe("ss -ltpn 'sport = :443'");
    expect(manualSteps(item({ id: "foreign_nft", status: DoctorStatus.WARN }))[0]!.command).toBe("nft list ruleset");
    expect(manualSteps(item({ id: "dstate_tasks", status: DoctorStatus.FAIL }))).toEqual([{ text: "hl.manual.reboot" }]);
    expect(manualSteps(item({ id: "memory_pressure", status: DoctorStatus.WARN }))).toEqual([]);
  });
  it("lists the fleet's problems worst first and counts what was checked", () => {
    const nodes = [n("a", [ok, warn, skip]), n("b", [fail, ok]), n("c", [], { hasReport: false })];
    expect(fleetIssues(nodes).map((x) => `${x.nodeName}/${x.item.id}`)).toEqual(["b/resolver", "a/time_sync"]);
    expect(doctorTotals(nodes)).toEqual({ nodes: 2, items: 4, issues: 2 });
  });
  it("names an item from its title key, falling back to the check id, and explains only what it has words for", () => {
    expect(itemTitle(t, item())).toBe("Disk space");
    expect(itemTitle(t, item({ id: "x_new", titleKey: "doctor.x_new.title" }))).toBe("doctor.x_new.title");
    const why = itemWhy(t, item({ whyKey: "health.doctor.disk_space.why", params: { mount: "/", used_pct: "91", free_mb: "800" } }));
    expect(why).toContain("91%");
    expect(itemWhy(t, item({ whyKey: "health.doctor.x_new.why" }))).toBe("");
  });
});

describe("fixErrorText", () => {
  it("words the agent's vocabulary and passes a failure message through", () => {
    expect(fixErrorText(t, "busy")).toBe(en["hl.fix.err.busy"]);
    expect(fixErrorText(t, "failed: journalctl exited 1")).toBe("journalctl exited 1");
    expect(fixErrorText(t, "")).toBe(en["hl.fix.err.generic"]);
    expect(fixErrorText(t, "brand_new")).toBe("brand_new");
  });
});

// Every key the panel and the agent send has a sentence in both languages (agent.proto "CHECK IDS" and "FIX IDS").
describe("the server's vocabulary is covered", () => {
  const checks = ["disk_space", "journald_size", "dstate_tasks", "time_sync", "resolver", "ipv6", "foreign_vpn", "foreign_nft", "port_conflicts", "net_baseline", "cert_expiry", "memory_pressure", "cpu_softirq", "kernel_headers", "awg_backend", "warp_path"];
  const fixes = ["journald_vacuum", "apply_baseline", "restart_inbound", "set_resolver"];
  const errors = ["timeout", "auth", "tls", "refused", "http_status", "exit_unreachable", "client_unsupported", "node_offline", "inbound_not_active", "inbound_failed", "inbound_disabled", "inbound_pending"];
  const kinds = ["node_down", "host_blip", "no_traffic", "check_failed", "doctor_warn", "doctor_fail", "state_drift", "cert_expiry", "quota", "subscription_shared_suspect"];
  const whyNoTraffic = ["udp_blocked", "udp_all_blocked", "mixed", "auth", "tls", "refused", "exit_unreachable", "http_status", "unknown"];
  const whyCheck = ["udp_blocked", "warp_path", "timeout", "auth", "tls", "refused", "exit_unreachable", "http_status", "unknown", "panel_egress"];
  const expected = [
    ...checks.flatMap((c) => [`doctor.${c}.title`, `health.doctor.${c}.why`]),
    "health.doctor.dstate_tasks.why.qxl_ttm",
    ...["no_tun", "unit_outdated", "no_module", "docker_forward_drop"].map((h) => `health.doctor.awg_backend.why.${h}`),
    ...["not_configured", "no_backend", "paused", "table_in_use", "rule_pref_in_use", "interface_in_use"].map((h) => `health.doctor.warp_path.why.${h}`),
    ...fixes.flatMap((f) => [`health.fix.${f}.label`, `health.fix.${f}.plan`]),
    ...errors.map((e) => `health.check.err.${e}`),
    ...kinds.map((k) => `health.alert.${k}.title`),
    "health.alert.check_failed.title.panel_egress",
    ...whyNoTraffic.map((v) => `health.alert.no_traffic.why.${v}`),
    ...whyCheck.map((v) => `health.alert.check_failed.why.${v}`),
    "health.alert.node_down.why",
    "health.alert.host_blip.why",
    "health.alert.state_drift.why",
    "health.alert.cert_expiry.why",
    "health.alert.cert_expiry.why.expired",
    "health.alert.cert_expiry.why.san_mismatch",
    "hl.res.accepted",
    "hl.res.superseded",
    "hl.fix.err.unknown_fix",
    "hl.fix.err.bad_params",
    "hl.fix.err.not_applicable",
    "hl.fix.err.unsupported_host",
    "hl.fix.err.busy",
    "node.reason.no_traffic",
    "node.reason.doctor_fail",
  ];
  it("has an English and a Russian sentence for each", () => {
    for (const k of expected) {
      expect(hasKey(k), k).toBe(true);
      expect(ru[k as keyof typeof ru], k).toBeTruthy();
    }
  });
  it("has every placeholder of the why texts among the params the node or the panel sends", () => {
    const sent: Record<string, string[]> = {
      disk_space: ["mount", "used_pct", "free_mb", "inode_pct", "journal_mb", "btmp_mb"],
      journald_size: ["journal_mb", "cap_mb"],
      dstate_tasks: ["stuck", "tasks", "hint", "load_idle"],
      time_sync: ["offset_s", "ntp_synced"],
      resolver: ["failed", "median_ms", "resolver"],
      ipv6: ["ipv6", "connect", "warp_inbounds"],
      foreign_vpn: ["names", "count", "on_our_ports"],
      foreign_nft: ["tables", "nat_tables", "ports"],
      port_conflicts: ["inbound_id", "port", "network", "process", "hop_holders"],
      net_baseline: ["differs"],
      cert_expiry: ["reason", "days_left", "inbound_id", "server_name", "subject"],
      memory_pressure: ["avail_pct", "swap_pct", "oom_kills", "psi_some", "psi_full", "oom_victims"],
      cpu_softirq: ["softirq_pct", "cpu_pct"],
      kernel_headers: ["kernel", "missing"],
      awg_backend: ["backend", "version", "mode", "reason", "hint", "unit_gen"],
      warp_path: ["state", "backend", "colo", "hint", "error", "inbounds"],
    };
    // what itemParams makes from them (doctor-detail.ts): the profile names and the optional pieces of a sentence
    const derived = ["profile", "profiles", "profiles_part", "server_part", "via", "colo_part", "left"];
    for (const c of checks) {
      const text = en[`health.doctor.${c}.why` as keyof typeof en];
      for (const m of text.matchAll(/\{(\w+)\}/g)) expect([...sent[c]!, ...derived], `${c}: {${m[1]}}`).toContain(m[1]);
    }
  });
  it("fills every placeholder of the alert texts the panel words with the params it sends", () => {
    // eval.go: no_traffic {failed, total, ports, port}, check_failed {inbound, profile, port, error_code, error_detail},
    // cert_expiry {inbound|inbound_id, profile, server_name, days_left, reason}, node_down {minutes} worded as {duration}
    const sent: Record<string, string[]> = {
      no_traffic: ["failed", "total", "ports", "port"],
      check_failed: ["inbound", "profile", "port", "error_code", "error_detail", "failed", "total", "providers"],
      cert_expiry: ["inbound", "inbound_id", "profile", "server_name", "days_left", "reason", "server_part"],
      node_down: ["duration"],
    };
    for (const [kind, params] of Object.entries(sent)) {
      for (const key of Object.keys(en).filter((k) => k.startsWith(`health.alert.${kind}.why`))) {
        for (const m of en[key as keyof typeof en].matchAll(/\{(\w+)\}/g)) expect(params, `${key}: {${m[1]}}`).toContain(m[1]);
      }
    }
  });
});
