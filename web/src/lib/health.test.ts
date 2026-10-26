import { describe, expect, it } from "vitest";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity, CheckStatus, CountryVerdict, DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { dictionaries, fill, langs, pickForm, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { zh } from "@/i18n/zh";
import { healthKind } from "./fleet";
import { factWords } from "./integrations";
import { makeFmt } from "./format";
import {
  alertFix,
  alertIsAbout,
  countryColumn,
  countryIn,
  countryLook,
  countryReason,
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
  isPeopleAlert,
  itemFix,
  itemTitle,
  itemWhy,
  listOf,
  lookup,
  manualSteps,
  muteChoices,
  peopleOrder,
  resolutionWord,
  restartConsequence,
  severityKind,
  type Alert,
  type CheckCell,
  type CountryCheck,
  type DoctorItem,
  type NodeDoctor,
} from "./health";

const t = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(en[key], vars), { n: () => "" });
const tRu = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(ru[key], vars), { n: () => "" });
// with plurals, as useT() makes it
const plural = (dict: Record<string, string>, lang: string) =>
  Object.assign((key: string, vars?: Record<string, string | number>) => fill(dict[key]!, vars), {
    n: (key: string, count: number, vars?: Record<string, string | number>) => fill(pickForm(dict[key]!, count, lang), { n: count, ...vars }),
  }) as unknown as T;
const tPlural = plural(en, "en");
const tRuPlural = plural(ru, "ru");
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
  openedUnix: 100,
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

const cell =(over: Partial<CheckCell> = {}): CheckCell => ({ deployed: true, inboundId: "inb_1", failStreak: 0, history: [], latencyHourMs: 0, responseHourMs: 0, connectHourMs: 0, udpSentHour: 0, udpLostHour: 0, nodeSentHour: 0, nodeLostHour: 0, ...over });
const at = (status: CheckStatus, latencyMs = 0, errorCode = "") => ({ status, atUnix: 1, latencyMs, exitIp: "", exitCountry: "", errorCode, errorDetail: "", connectMs: 0, responseMs: 0, udpSent: 0, udpLost: 0 });

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
    const probe = alert({ subject: "probe_path", nodeId: "", params: { nodes: "ee, ru", providers: "2", failed: "3", total: "15" } });
    expect(alertTitle(t, probe)).toBe(en["health.alert.check_failed.title.probe_path"]);
    expect(alertTitle(t, alert({ params: { profile: "hy2 · 443" } }))).toContain("hy2 · 443");
    const flapping = alert({ kind: AlertKind.HOST_BLIP, titleKey: "health.alert.host_blip.title", subject: "flapping", params: { count: "4" } });
    expect(alertTitle(t, flapping)).toBe(en["health.alert.host_blip.title.flapping"]);
    const link = alert({ kind: AlertKind.NODE_DOWN, titleKey: "health.alert.node_down.title", subject: "panel_link", nodeId: "", params: { nodes: "4", total: "4" } });
    expect(alertTitle(t, link)).toBe(en["health.alert.node_down.title.panel_link"]);
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
    const unread = alert({ kind: AlertKind.UPDATE_FAILED, whyKey: "health.alert.update_failed.why.gate_failed", params: { node: "de2", reason: "inbounds_unreadable" } });
    expect(alertWhy(tRu, fmtRu, unread)).toContain("de2 не прошла проверку после обновления: панель не смогла прочитать профили ноды. Где получилось");
  });
  it("says when access ended and names the protocol people dropped off", () => {
    const ended = alert({ kind: AlertKind.ACCESS_ENDED, whyKey: "health.alert.access_ended.why.expired", params: { user_name: "alice", since: "1700000000" } });
    expect(alertWhy(t, fmt, ended)).toContain(`alice’s subscription ended on ${fmt.stamp(1700000000)}`);
    const gone = alert({ kind: AlertKind.USERS_IMPACTED, subject: "hysteria2", whyKey: "health.alert.users_impacted.why.gone", params: { now: "0", usual: "5", users: "5" } });
    expect(alertWhy(tRu, fmtRu, gone)).toMatch(/^Людей на Hysteria2: сейчас 0, обычно около 5 в этот час, а нода на связи\./);
  });
  it("words a torrent alert with the attempts, the evidence of the last one and the ports, and works without them", () => {
    const torrent = (params: Record<string, string>) =>
      alert({ kind: AlertKind.TORRENT, nodeId: "", subject: "usr_1", whyKey: "health.alert.torrent.why", params: { user_id: "usr_1", user_name: "alice", nodes: "EE, DE", count: "7", ...params } });
    expect(alertWhy(tPlural, fmt, torrent({ evidence: "tracker_connect", ports: "6969" }))).toBe(
      "alice is trying to use torrents on EE, DE: 7 attempts in a day, blocked. Evidence: tracker connect request (the protocol’s magic number), port 6969. It closes after a day without attempts.",
    );
    expect(alertWhy(tPlural, fmt, torrent({ count: "1", evidence: "dht_query", ports: "6881, 6969" }))).toContain(
      "1 attempt in a day, blocked. Evidence: DHT query, ports 6881, 6969.",
    );
    expect(alertWhy(tRuPlural, fmtRu, torrent({ count: "1", evidence: "tcp_handshake", ports: "6881" }))).toBe(
      "alice пытается качать торренты на EE, DE: 1 попытка за сутки, торренты заблокированы. Доказательство: рукопожатие BitTorrent по TCP, порт 6881. Алерт закроется после суток без попыток.",
    );
    expect(alertWhy(tRuPlural, fmtRu, torrent({ count: "22", evidence: "utp_syn", ports: "1, 2, 3" }))).toContain("22 попытки за сутки");
    expect(alertWhy(tRuPlural, fmtRu, torrent({ count: "5", evidence: "utp_syn", ports: "1, 2, 3" }))).toContain("5 попыток за сутки");
    // an agent that predates the evidence, and a code that this panel has no words for
    expect(alertWhy(tPlural, fmt, torrent({}))).toBe("alice is trying to use torrents on EE, DE: 7 attempts in a day, blocked. It closes after a day without attempts.");
    expect(alertWhy(tPlural, fmt, torrent({ evidence: "new_code" }))).toContain("Evidence: new_code.");
    expect(alertTitle(tRu, alert({ kind: AlertKind.TORRENT, titleKey: "health.alert.torrent.title" }))).toBe("Попытки качать торренты");
  });
  it("words a device_limit alert with who is at the limit and what to do", () => {
    const limited = alert({ kind: AlertKind.DEVICE_LIMIT, nodeId: "", subject: "usr_1", titleKey: "health.alert.device_limit.title", whyKey: "health.alert.device_limit.why",
      params: { user_id: "usr_1", user_name: "alice", used: "5", limit: "5", count: "2", last_unix: "1790000000" } });
    expect(alertTitle(t, limited)).toBe("An app was turned away at the device limit");
    expect(alertWhy(t, fmt, limited)).toMatch(/^alice is at the device limit \(5 of 5\) and an app asking for a new place was turned away\./);
    expect(alertWhy(tRu, fmtRu, limited)).toMatch(/^У alice заняты все места для устройств \(5 из 5\)/);
  });
  it("words a port_lossy alert with the loss in percent and who sent the test packets", () => {
    const lossy = (sender: string) =>
      alert({ kind: AlertKind.PORT_LOSSY, subject: "inb_1", titleKey: "health.alert.port_lossy.title", whyKey: "health.alert.port_lossy.why",
        params: { profile: "hy2 · WARP", port: "8443", sent: "300", got: "189", sender, checked_unix: "1790000000" } });
    expect(alertTitle(t, lossy("de2"))).toBe("UDP to port 8443 loses packets");
    expect(alertWhy(t, fmt, lossy("de2"))).toMatch(/^The test packets to port 8443 of “hy2 · WARP” lost 37 % \(checked from de2\)\./);
    expect(alertWhy(tRu, fmtRu, lossy("panel"))).toMatch(/^Тестовые пакеты до порта 8443 профиля «hy2 · WARP» потерялись на 37 % \(проверка с панели\)\./);
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
  const look = (last: CheckCell["last"], status = NodeStatus.ONLINE) => cellLook(t, fmt, cell({ last }), status);
  it("shows a dash for a profile that is not deployed and waits for a first result", () => {
    expect(cellLook(t, fmt, cell({ deployed: false }), NodeStatus.ONLINE)).toMatchObject({ kind: "none", label: "—" });
    expect(look(undefined)).toMatchObject({ kind: "off", label: "…" });
  });
  it("shows latency for ok and degraded, the failure for a failed round", () => {
    expect(look(at(CheckStatus.OK, 42))).toMatchObject({ kind: "ok", label: "42 ms" });
    expect(look(at(CheckStatus.DEGRADED, 120, "http_status"))).toMatchObject({ kind: "warn", label: "120 ms" });
    const failed = look(at(CheckStatus.FAILED, 0, "timeout"));
    expect(failed).toMatchObject({ kind: "bad", label: "✕ fails" });
    expect(failed.state).toBe(en["health.check.err.timeout"]);
  });
  // a round that passed: cold time 2658 ms, as the old panels showed it
  const ok = (over: Partial<CheckCell>, last: Partial<NonNullable<CheckCell["last"]>> = {}, status = CheckStatus.OK) =>
    cellLook(t, fmt, cell({ ...over, last: { ...at(status, 2658), ...last } }), NodeStatus.ONLINE);
  it("shows the hour's response median, else the last round's response, else the old cold time", () => {
    expect(ok({ responseHourMs: 48, latencyHourMs: 2600 }, { responseMs: 51 })).toMatchObject({ kind: "ok", label: "48 ms" });
    expect(ok({ latencyHourMs: 2600 }, { responseMs: 51 })).toMatchObject({ label: "51 ms" });
    expect(ok({ latencyHourMs: 2600 })).toMatchObject({ label: "2600 ms" });
    expect(ok({})).toMatchObject({ label: "2658 ms" });
    // the tooltip says which of the three it is
    expect(ok({ responseHourMs: 48 }).tip[0]).toBe("Response 48 ms, median over the last hour");
    expect(ok({}, { responseMs: 51 }).tip[0]).toBe("Response 51 ms in the last round");
    expect(ok({ latencyHourMs: 2600 }).tip[0]).toBe("2600 ms from scratch: no response time yet");
    expect(ok({ responseHourMs: 48 }, {}, CheckStatus.DEGRADED)).toMatchObject({ kind: "warn", label: "48 ms" });
  });
  it("marks a connect median of 2 s or more on a second line, in the cell's calm tone, and says it to a screen reader", () => {
    expect(ok({ responseHourMs: 48, connectHourMs: 2600 })).toMatchObject({ kind: "ok", label: "48 ms", slow: "2.6 s" });
    expect(ok({ responseHourMs: 48, connectHourMs: 2000 }).slow).toBe("2 s");
    expect(ok({ responseHourMs: 48, connectHourMs: 1999 }).slow).toBe("");
    expect(ok({ responseHourMs: 48 }).slow).toBe("");
    expect(ok({ responseHourMs: 48, connectHourMs: 2600 }).state).toBe("Response 48 ms, median over the last hour · Connect 2.6 s, median over the last hour");
    expect(ok({ responseHourMs: 48, connectHourMs: 300 }).state).toBe("Response 48 ms, median over the last hour");
    expect(cellLook(tRu, fmtRu, cell({ responseHourMs: 48, connectHourMs: 2600, last: at(CheckStatus.OK) }), NodeStatus.ONLINE)).toMatchObject({ label: "48 мс", slow: "2,6 с" });
  });
  it("lists the hour's medians, the last round, the 24 h counts and where it is measured in the tooltip", () => {
    const history = [
      { startUnix: 0, ok: 10, failed: 0, latencyMs: 90 },
      { startUnix: 1800, ok: 5, failed: 2, latencyMs: 2600 },
    ];
    const atUnix = 1_760_000_000;
    expect(ok({ responseHourMs: 48, connectHourMs: 2600, history }, { atUnix, responseMs: 51, connectMs: 2700 }).tip).toEqual([
      "Response 48 ms, median over the last hour",
      "Connect 2.6 s, median over the last hour",
      `Last round ${fmt.clock(atUnix)}: response 51 ms, connect 2.7 s`,
      "24 h: 15 ok, 2 failed",
      "Measured from the panel's server",
    ]);
    // a round before the panel measured them has neither; a degraded round says what failed
    expect(ok({ latencyHourMs: 2600, history }, { atUnix, errorCode: "http_status" }, CheckStatus.DEGRADED).tip.slice(1, 3)).toEqual([
      `Last round ${fmt.clock(atUnix)}: response —, connect —`,
      en["health.check.err.http_status"],
    ]);
    // a failed round: why, the counts and where from, no numbers
    expect(cellLook(t, fmt, cell({ history, last: at(CheckStatus.FAILED, 0, "timeout") }), NodeStatus.ONLINE).tip).toEqual([
      en["health.check.err.timeout"],
      "24 h: 15 ok, 2 failed",
      "Measured from the panel's server",
    ]);
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

describe("listOf", () => {
  it("joins names the language's way, with half-width spaces around 和 in zh", () => {
    expect(listOf("en", ["de1", "nl1", "fi1"])).toBe("de1, nl1, and fi1");
    expect(listOf("ru", ["de1", "nl1", "fi1"])).toBe("de1, nl1 и fi1");
    expect(listOf("zh-CN", ["de1", "nl1", "fi1"])).toBe("de1、nl1 和 fi1");
    expect(listOf("ru", ["de1"])).toBe("de1");
  });
});

describe("the check texts", () => {
  it("exist in English, Russian and Chinese", () => {
    const keys = ["hl.legend.ms", "hl.legend.slow", "hl.cell.connect", "hl.tip.response", "hl.tip.responseLast", "hl.tip.cold", "hl.tip.connect", "hl.tip.last", "hl.tip.day", "hl.tip.where", "hl.udp.title", "hl.udp.body", "hl.udp.to", "hl.udp.lost", "hl.hist.sub", "hl.hist.bar"] as const;
    for (const key of keys) {
      expect(en[key], key).toMatch(/[a-z]/i);
      expect(ru[key], key).toMatch(/[а-яё]/i);
      expect(dictionaries.zh[key], key).toMatch(/\p{Script=Han}/u);
    }
    // the bars stay the old cold time, and say so
    expect(en["hl.hist.bar"]).toContain("from scratch");
    expect(ru["hl.hist.bar"]).toContain("с нуля");
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
  const checks = ["disk_space", "journald_size", "dstate_tasks", "time_sync", "resolver", "ipv6", "foreign_vpn", "foreign_nft", "port_conflicts", "net_baseline", "cert_expiry", "memory_pressure", "cpu_softirq", "kernel_headers", "awg_backend", "warp_path", "unit_generation"];
  const fixes = ["journald_vacuum", "apply_baseline", "restart_inbound", "set_resolver", "reconnect_warp", "refresh_unit"];
  const errors = ["timeout", "auth", "tls", "refused", "http_status", "exit_unreachable", "client_unsupported", "node_offline", "inbound_not_active", "inbound_failed", "inbound_disabled", "inbound_pending"];
  const kinds = ["node_down", "host_blip", "no_traffic", "check_failed", "doctor_warn", "doctor_fail", "state_drift", "cert_expiry", "quota", "subscription_shared_suspect",
    "access_ended", "user_connection", "users_impacted", "country_blocked"];
  const whyNoTraffic = ["udp_blocked", "udp_all_blocked", "mixed", "auth", "tls", "refused", "exit_unreachable", "http_status", "unknown"];
  const whyCheck = ["udp_blocked", "warp_path", "timeout", "auth", "tls", "refused", "exit_unreachable", "http_status", "unknown", "panel_egress", "probe_path", "via_cdn", "not_started"];
  const expected = [
    ...checks.flatMap((c) => [`doctor.${c}.title`, `health.doctor.${c}.why`]),
    "health.doctor.dstate_tasks.why.qxl_ttm",
    ...["no_tun", "unit_outdated", "no_module", "docker_forward_drop"].map((h) => `health.doctor.awg_backend.why.${h}`),
    ...["not_configured", "no_backend", "paused", "table_in_use", "rule_pref_in_use", "interface_in_use"].map((h) => `health.doctor.warp_path.why.${h}`),
    ...fixes.flatMap((f) => [`health.fix.${f}.label`, `health.fix.${f}.plan`]),
    "hl.fix.disruptiveWarp",
    "hl.fix.disruptiveUnit",
    ...errors.map((e) => `health.check.err.${e}`),
    ...kinds.map((k) => `health.alert.${k}.title`),
    "health.alert.check_failed.title.panel_egress",
    "health.alert.check_failed.title.probe_path",
    "health.alert.host_blip.title.flapping",
    "health.alert.node_down.title.panel_link",
    "health.alert.node_down.why.panel_link",
    "health.alert.host_blip.why.flapping",
    ...whyNoTraffic.map((v) => `health.alert.no_traffic.why.${v}`),
    ...whyCheck.map((v) => `health.alert.check_failed.why.${v}`),
    "health.alert.node_down.why",
    "health.alert.host_blip.why",
    "health.alert.state_drift.why",
    "health.alert.cert_expiry.why",
    "health.alert.cert_expiry.why.expired",
    "health.alert.cert_expiry.why.san_mismatch",
    "health.alert.access_ended.why.expired",
    "health.alert.access_ended.why.quota",
    ...["never_connected", "stale_key", "silent"].map((v) => `health.alert.user_connection.why.${v}`),
    "health.alert.users_impacted.why.gone",
    "health.alert.country_blocked.why.tcp_blocked",
    "health.alert.country_blocked.why.blocked",
    "health.alert.country_blocked.why.tspu",
    "health.alert.country_blocked.why.conflict.tcp_blocked",
    "health.alert.country_blocked.why.conflict.blocked",
    "health.alert.country_blocked.title.partial",
    "health.alert.country_blocked.why.partial",
    "hl.res.accepted",
    "hl.res.superseded",
    "hl.fix.err.unknown_fix",
    "hl.fix.err.bad_params",
    "hl.fix.err.not_applicable",
    "hl.fix.err.unsupported_host",
    "hl.fix.err.busy",
    "hl.fix.err.unit_refresh_invalid",
    "node.reason.no_traffic",
    "node.reason.doctor_fail",
  ];
  it("has a sentence in every language for each", () => {
    for (const k of expected) {
      expect(hasKey(k), k).toBe(true);
      for (const lang of langs) expect(dictionaries[lang][k as keyof typeof ru], `${lang} ${k}`).toBeTruthy();
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
      unit_generation: ["unit_gen", "current"],
    };
    // what itemParams makes from them (doctor-detail.ts): the profile names and the optional pieces of a sentence
    const derived = ["profile", "profiles", "profiles_part", "server_part", "via", "colo_part", "left"];
    for (const c of checks) {
      const text = en[`health.doctor.${c}.why` as keyof typeof en];
      for (const m of text.matchAll(/\{(\w+)\}/g)) expect([...sent[c]!, ...derived], `${c}: {${m[1]}}`).toContain(m[1]);
    }
  });
  it("fills every placeholder of the alert texts the panel words with the params it sends", () => {
    // eval.go: no_traffic {failed, total, ports, port}, check_failed {inbound, profile, port, error_code, error_detail, cdn_host for via_cdn},
    // cert_expiry {inbound|inbound_id, profile, server_name, days_left, reason}, node_down {minutes} worded as {duration}
    const sent: Record<string, string[]> = {
      no_traffic: ["failed", "total", "ports", "port"],
      check_failed: ["inbound", "profile", "port", "error_code", "error_detail", "failed", "total", "providers", "nodes", "cdn_host"],
      cert_expiry: ["inbound", "inbound_id", "profile", "server_name", "days_left", "reason", "server_part"],
      node_down: ["duration", "nodes", "total"],
      host_blip: ["count"],
      device_limit: ["user_name", "used", "limit"],
      country_blocked: ["country", "networks", "ports"],
    };
    for (const [kind, params] of Object.entries(sent)) {
      for (const key of Object.keys(en).filter((k) => k.startsWith(`health.alert.${kind}.why`))) {
        for (const m of en[key as keyof typeof en].matchAll(/\{(\w+)\}/g)) expect(params, `${key}: {${m[1]}}`).toContain(m[1]);
      }
    }
  });
});

describe("alerts about people", () => {
  const ended = alert({ kind: AlertKind.ACCESS_ENDED, nodeId: "", subject: "usr_1", params: { user_id: "usr_1", user_name: "alice" } });
  const device = alert({ kind: AlertKind.USER_CONNECTION, nodeId: "", severity: AlertSeverity.INFO, subject: "dev_9", params: { user_id: "usr_2", user_name: "bob" } });
  const silent = alert({ kind: AlertKind.USER_CONNECTION, nodeId: "", severity: AlertSeverity.INFO, subject: "usr_3", params: { user_id: "usr_3" } });
  const impacted = alert({ kind: AlertKind.USERS_IMPACTED, subject: "hysteria2" });
  const torrent = alert({ kind: AlertKind.TORRENT, nodeId: "", subject: "usr_4", params: { user_id: "usr_4", user_name: "alice" } });
  const limit = alert({ kind: AlertKind.DEVICE_LIMIT, nodeId: "", subject: "usr_5", params: { user_id: "usr_5", user_name: "bob" } });

  it("tells them from the alerts about nodes: the dropped connections of a node are a node alert", () => {
    expect([ended, device, silent, impacted, alert(), torrent, limit].map(isPeopleAlert)).toEqual([true, true, true, false, false, true, true]);
    expect(alertIsAbout(limit, "usr_5")).toBe(true);
    expect(alertIsAbout(torrent, "usr_4")).toBe(true);
    expect(alertIsAbout(torrent, "usr_1")).toBe(false);
  });
  it("puts warnings before info and leaves the node alerts out", () => {
    expect(peopleOrder([device, impacted, ended, silent]).map((a) => a.subject)).toEqual(["usr_1", "dev_9", "usr_3"]);
  });
  it("finds the alerts of one user by params.user_id or the user-keyed subject, never by a device id", () => {
    expect([ended, device, silent].map((a) => alertIsAbout(a, "usr_2"))).toEqual([false, true, false]);
    expect(alertIsAbout({ ...silent, params: {} }, "usr_3")).toBe(true);
    expect(alertIsAbout({ ...device, params: {} }, "usr_2")).toBe(false);
    expect(alertIsAbout(impacted, "hysteria2")).toBe(false);
  });
});
// ---------------------------------------------------------------------------------------------------
// Reachability from a country

describe("reachability from a country", () => {
  const tZhPlural = plural(zh, "zh");
  const fmtZh = makeFmt("zh", tZhPlural);
  const roundAt = Math.floor(Date.now() / 1000) - 240;
  const probe = (network: string, city: string, icmpMs: number, tcp: [number, boolean][], valid = true) => ({
    network,
    asn: 1,
    city,
    icmpOk: icmpMs > 0,
    icmpMs,
    tcp: tcp.map(([port, ok]) => ({ port, ok, ms: ok ? 40 : 0 })),
    valid,
    dark: false,
    eyeball: true,
  });
  const check = (over: Partial<CountryCheck> = {}): CountryCheck => ({
    nodeId: "nod_1",
    country: "RU",
    verdict: CountryVerdict.OK,
    atUnix: roundAt,
    eyeball: true,
    reason: "",
    confidence: "eyeball",
    noProbe: [],
    listedBy: [],
    cdnProvider: "",
    cdnRegion: "",
    sources: [],
    globalpingVerdict: CountryVerdict.UNSPECIFIED,
    tspuVerdict: CountryVerdict.UNSPECIFIED,
    tspuPorts: [],
    dpiHop: 0,
    tspuVantageId: "",
    tspuVantageName: "",
    tspuAtUnix: 0,
    probes: [probe("Rostelecom", "Moscow", 42, [[443, true], [8443, true]]), probe("Beeline", "Kazan", 47, [[443, true], [8443, true]])],
    ...over,
  });
  const tcpCut = check({
    verdict: CountryVerdict.TCP_BLOCKED,
    probes: [
      probe("Rostelecom", "Moscow", 51, [[443, false], [8443, false]]),
      probe("Beeline", "", 55, [[443, false], [8443, false]]),
      probe("MegaFon", "Kazan", 0, [[443, true], [8443, false]], false),
    ],
  });
  const cut = check({ verdict: CountryVerdict.BLOCKED, probes: [probe("Rostelecom", "Moscow", 0, [[443, false]]), probe("MTS", "Omsk", 0, [])] });
  const none = check({ verdict: CountryVerdict.UNKNOWN, reason: "too_few_probes", eyeball: false, confidence: "", probes: [probe("Selectel", "Moscow", 38, [[443, true]])] });

  it("says each verdict in a word, calm for OK and no data, in a problem tone only for the two blocks", () => {
    const looks = [check(), tcpCut, cut, none].map((c) => countryLook(tPlural, fmt, c));
    expect(looks.map((l) => [l.kind, l.label])).toEqual([
      ["ok", "reachable"],
      ["warn", "TCP blocked"],
      ["bad", "unreachable"],
      ["unknown", "no data"],
    ]);
    expect(looks.map((l) => l.head)).toEqual(["Reachable from Russia", "TCP is blocked, ICMP passes", "Neither ICMP nor TCP gets through", "No data: too few probes answered"]);
    // the quiet line of a row: who saw it and when; TCP blocked adds that ICMP passes; no data says why
    expect(looks.map((l) => l.line)).toEqual([
      "Rostelecom, Beeline · 4 min ago",
      "ICMP passes · Rostelecom, Beeline · 4 min ago",
      "Rostelecom, MTS · 4 min ago",
      "too few probes answered · 4 min ago",
    ]);
  });

  it("words the same in Russian, the country in the genitive, and in Chinese", () => {
    const inRu = [check(), tcpCut, cut, none].map((c) => countryLook(tRuPlural, fmtRu, c));
    expect(inRu.map((l) => l.label)).toEqual(["доступна", "TCP режут", "недоступна", "нет данных"]);
    expect(inRu.map((l) => l.head)).toEqual(["Доступна из России", "TCP режут, ICMP проходит", "Не проходят ни ICMP, ни TCP", "Нет данных: ответило слишком мало зондов"]);
    const inZh = [check(), tcpCut, cut, none].map((c) => countryLook(tZhPlural, fmtZh, c));
    expect(inZh.map((l) => l.label)).toEqual(["可访问", "TCP 被封锁", "无法连接", "无数据"]);
    expect(inZh.map((l) => l.head)).toEqual(["从俄罗斯可以访问", "TCP 被封锁，ICMP 可通", "ICMP 和 TCP 都不通", "无数据：响应的探测点太少"]);
  });

  it("fills the tooltip: each probe with its network and city, ICMP and TCP per port, the time, how the probes were picked", () => {
    const l = countryLook(tPlural, fmt, tcpCut);
    expect(l.probes).toEqual([
      { where: "Rostelecom, Moscow", result: "ICMP 51 ms · TCP 443, 8443 ✕", counted: true, dark: false },
      { where: "Beeline", result: "ICMP 55 ms · TCP 443, 8443 ✕", counted: true, dark: false },
      // ports that differ get a mark each; a probe thrown away for the round is shown, and says so
      { where: "MegaFon, Kazan (not counted)", result: "ICMP ✕ · TCP 443 ✓, 8443 ✕", counted: false, dark: false },
    ]);
    expect(l.at).toBe(`Last round: ${fmt.stamp(roundAt)}`);
    expect(l.via).toBe("Checked via Globalping from home and mobile networks");
    // a node without TCP ports has ICMP alone; probes from any network are said to be data centers
    expect(countryLook(tPlural, fmt, cut).probes[1]).toEqual({ where: "MTS, Omsk", result: "ICMP ✕", counted: true, dark: false });
    expect(countryLook(tPlural, fmt, none).via).toBe("Checked via Globalping from data centers");
    expect(countryLook(tRuPlural, fmtRu, none).via).toBe("Проверено через Globalping из датацентров");
    expect(countryLook(tRuPlural, fmtRu, check()).via).toBe("Проверено через Globalping из домашних и мобильных сетей");
    expect(countryLook(tRuPlural, fmtRu, tcpCut).probes[2]!.where).toBe("MegaFon, Kazan (не учтён)");
    expect(countryLook(tZhPlural, fmtZh, tcpCut).probes[2]!.where).toBe("MegaFon, Kazan（未计入）");
    expect(countryLook(tZhPlural, fmtZh, none).via).toBe("通过 Globalping 从数据中心检测");
    expect(countryLook(tZhPlural, fmtZh, check()).probes[0]!.result).toBe("ICMP 42 ms · TCP 443, 8443 ✓");
  });

  it("has no reason for a code it does not know, and names an unknown country by its code", () => {
    expect(countryReason(tPlural, "brand_new")).toBe("");
    expect(countryReason(tPlural, "")).toBe("");
    expect(countryLook(tPlural, fmt, check({ verdict: CountryVerdict.UNKNOWN, reason: "brand_new" })).head).toBe("No data");
    expect(["RU", "BY", "KZ", "CN", "IR", "TR"].map((c) => countryIn(tRuPlural, c))).toEqual(["России", "Беларуси", "Казахстана", "Китая", "Ирана", "Турции"]);
    expect(countryIn(tPlural, "KZ")).toBe("Kazakhstan");
    expect(countryIn(tZhPlural, "CN")).toBe("中国");
    expect(countryIn(tRuPlural, "UZ")).toBe("страны UZ");
    expect(countryIn(tPlural, "UZ")).toBe("UZ");
  });

  // the block lists: a node in the registry that nobody checked live, a row of the lists alone, a live verdict with their detail
  const listsOnly = (over: Partial<CountryCheck> = {}) =>
    check({ verdict: CountryVerdict.LISTED, confidence: "listed", eyeball: false, probes: [], listedBy: ["antifilter_ipsum", "antifilter_community"], cdnProvider: "Hetzner", cdnRegion: "DE", ...over });

  it("says LISTED in its own word: in the registry, not checked live, from the lists only, with the lists and the subnet", () => {
    const l = countryLook(tPlural, fmt, listsOnly());
    expect([l.kind, l.label, l.head, l.state, l.alsoListed]).toEqual(["listed", "listed", "In the RKN registry, not checked live", "In the RKN registry, not checked live", false]);
    expect(l.probes).toEqual([]);
    expect(l.at).toBe(""); // no live round
    expect(l.lists).toEqual(["Lists: RKN registry (antifilter ipsum), community list (antifilter)", "Hetzner subnet, DE", `Lists compared: ${fmt.stamp(roundAt)}`]);
    expect(l.via).toBe("From the block lists only");
    expect(l.line).toBe("RKN registry (antifilter ipsum), community list (antifilter) · Hetzner subnet, DE · 4 min ago");
    // a list the dictionary does not know keeps its code; a subnet without a region has none
    expect(countryLook(tPlural, fmt, listsOnly({ listedBy: ["brand_new"], cdnRegion: "" })).lists.slice(0, 2)).toEqual(["Lists: brand_new", "Hetzner subnet"]);
  });

  it("says LISTED in Russian and Chinese", () => {
    const ruL = countryLook(tRuPlural, fmtRu, listsOnly());
    expect([ruL.label, ruL.head, ruL.via]).toEqual(["в реестре", "В реестре РКН, живьём не проверено", "Только по спискам блокировок"]);
    expect(ruL.lists).toEqual(["Списки: реестр РКН (antifilter ipsum), список сообщества (antifilter)", "Подсеть Hetzner, DE", `Сверено со списками: ${fmtRu.stamp(roundAt)}`]);
    const zhL = countryLook(tZhPlural, fmtZh, listsOnly());
    expect([zhL.label, zhL.head, zhL.via]).toEqual(["在名录中", "在 RKN 封锁名录中，未经实测", "仅依据封锁名单"]);
    expect(zhL.lists.slice(0, 2)).toEqual(["名单：RKN 封锁名录（antifilter ipsum）、社区名单（antifilter）", "Hetzner 网段（DE）"]);
  });

  it("says why an unknown live round left the lists to decide", () => {
    const l = countryLook(tPlural, fmt, listsOnly({ reason: "too_few_probes", probes: [probe("Selectel", "Moscow", 38, [[443, true]])] }));
    expect(l.kind).toBe("listed");
    expect(l.at).toBe(`Last round: ${fmt.stamp(roundAt)}`);
    expect(l.lists[0]).toBe("No live verdict: too few probes answered");
    expect(l.lists).not.toContain(`Lists compared: ${fmt.stamp(roundAt)}`);
  });

  it("calls a row of the lists alone that holds no hit not listed, without a live verdict", () => {
    const l = countryLook(tPlural, fmt, listsOnly({ verdict: CountryVerdict.UNKNOWN, confidence: "", listedBy: [] }));
    expect([l.kind, l.label, l.head, l.via]).toEqual(["unknown", "no data", "Not in the block lists, not checked live", ""]);
    expect(l.line).toBe("not in the block lists · Hetzner subnet, DE · 4 min ago");
    expect(countryLook(tRuPlural, fmtRu, listsOnly({ verdict: CountryVerdict.UNKNOWN, confidence: "", listedBy: [] })).head).toBe("Нет в списках блокировок, живьём не проверено");
  });

  it("keeps a live verdict and adds what the lists say", () => {
    const l = countryLook(tPlural, fmt, { ...tcpCut, listedBy: ["antifilter_ipsum"], cdnProvider: "Hetzner", cdnRegion: "DE" });
    expect([l.kind, l.label, l.head, l.alsoListed]).toEqual(["warn", "TCP blocked", "TCP is blocked, ICMP passes", true]);
    expect(l.state).toBe("TCP is blocked, ICMP passes · in the RKN registry");
    expect(l.probes).toHaveLength(3);
    expect(l.lists).toEqual(["Lists: RKN registry (antifilter ipsum)", "Hetzner subnet, DE"]);
    expect(l.via).toBe("Checked via Globalping from home and mobile networks");
    expect(l.line).toBe("ICMP passes · Rostelecom, Beeline · in the RKN registry · Hetzner subnet, DE · 4 min ago");
    // the subnet alone rides along with any verdict, without the list mark
    const ok = countryLook(tPlural, fmt, check({ cdnProvider: "Hetzner", cdnRegion: "DE" }));
    expect([ok.alsoListed, ok.lists, ok.line]).toEqual([false, ["Hetzner subnet, DE"], "Rostelecom, Beeline · Hetzner subnet, DE · 4 min ago"]);
    expect(countryLook(tPlural, fmt, check({ confidence: "dc_only", eyeball: false })).via).toBe("Checked via Globalping from data centers");
  });

  // a block on some networks: MTS gets nothing from the node while it reaches the others, Beeline reaches it
  const dark = (network: string, asn: number, city: string) => ({ ...probe(network, city, 0, [[443, false], [8443, false]]), asn, dark: true });
  const some = check({
    verdict: CountryVerdict.PARTIAL,
    probes: [dark("MTS", 8359, "Novosibirsk"), { ...probe("Beeline", "Moscow", 44, [[443, true], [8443, true]]), asn: 3216 }],
    noProbe: ["MegaFon", "T2"],
  });

  it("says PARTIAL apart from TCP blocked: its own kind and word, the networks that get nothing, the operators nobody asked", () => {
    const l = countryLook(tPlural, fmt, some);
    expect([l.kind, l.label, l.note, l.head]).toEqual(["partial", "some networks", "MTS", "Reachable from Russia, but not from every network"]);
    expect(l.dark).toBe("Blocked on some networks: MTS (AS8359, Novosibirsk)");
    expect(l.state).toBe("Reachable from Russia, but not from every network · Blocked on some networks: MTS (AS8359, Novosibirsk)");
    expect(l.noProbe).toBe("No home/mobile probe this round: MegaFon, T2");
    // the dark probe says what it means; the one that reached the node is a plain row
    expect(l.probes).toEqual([
      { where: "MTS, Novosibirsk (reaches your other nodes, not this one)", result: "ICMP ✕ · TCP 443, 8443 ✕", counted: true, dark: true },
      { where: "Beeline, Moscow", result: "ICMP 44 ms · TCP 443, 8443 ✓", counted: true, dark: false },
    ]);
    // the phone's line names the networks in full instead of every network asked
    expect(l.line).toBe("Blocked on some networks: MTS (AS8359, Novosibirsk) · 4 min ago");
    expect(l.via).toBe("Checked via Globalping from home and mobile networks");
  });

  it("names each dark network once, with its AS and city when it has them, and skips a probe that did not count", () => {
    const l = countryLook(tPlural, fmt, {
      ...some,
      probes: [
        dark("MTS", 8359, "Novosibirsk"),
        dark("MTS", 8359, "Novosibirsk"),
        dark("Beeline", 3216, ""),
        { ...dark("Rostelecom", 12389, "Kazan"), valid: false },
        probe("Beeline", "Moscow", 44, [[443, true]]),
      ],
    });
    expect(l.dark).toBe("Blocked on some networks: MTS (AS8359, Novosibirsk), Beeline (AS3216)");
    expect(l.note).toBe("MTS, Beeline");
    expect(l.probes.map((p) => p.dark)).toEqual([true, true, true, false, false]);
    expect(l.probes[3]!.where).toBe("Rostelecom, Kazan (not counted)");
    // names from a third party stay text: nothing in them is read as markup or as a placeholder
    expect(countryLook(tPlural, fmt, { ...some, probes: [dark("<b>MTS</b> {country}", 8359, "Novosibirsk")] }).dark).toBe(
      "Blocked on some networks: <b>MTS</b> {country} (AS8359, Novosibirsk)",
    );
  });

  it("breaks a partial block down: the networks that do not see the node, the probes that do with their time, who was not asked", () => {
    expect(countryLook(tPlural, fmt, some).breakdown).toEqual({
      dark: [{ name: "MTS", tag: "AS8359 · Novosibirsk" }],
      seen: [{ name: "Beeline", tag: "AS3216 · Moscow · 40\u00a0ms" }],
      noProbe: "Not checked this round: MegaFon, T2",
      at: "Last round: 4 min ago",
    });
    // a dark network once; one without a city; a probe that did not count is in neither list; a node without TCP ports: the ping
    const b = countryLook(tPlural, fmt, {
      ...some,
      noProbe: [],
      probes: [
        dark("MTS", 8359, "Novosibirsk"),
        dark("MTS", 8359, "Novosibirsk"),
        dark("Beeline", 3216, ""),
        { ...dark("Rostelecom", 12389, "Kazan"), valid: false },
        { ...probe("Beeline", "Kazan", 47, []), asn: 3216 },
        { ...probe("MegaFon", "Omsk", 50, [], false), asn: 31133 },
      ],
    }).breakdown!;
    expect(b.dark).toEqual([
      { name: "MTS", tag: "AS8359 · Novosibirsk" },
      { name: "Beeline", tag: "AS3216" },
    ]);
    expect(b.seen).toEqual([{ name: "Beeline", tag: "AS3216 · Kazan · 47\u00a0ms" }]);
    expect(b.noProbe).toBe("");
    const ruB = countryLook(tRuPlural, fmtRu, some).breakdown!;
    expect([ruB.seen[0]!.tag, ruB.noProbe, ruB.at]).toEqual(["AS3216 · Moscow · 40\u00a0мс", "Не проверены в этом круге: MegaFon, T2", "Последний круг: 4 мин назад"]);
    expect(countryLook(tZhPlural, fmtZh, some).breakdown!.noProbe).toBe("本轮未检测：MegaFon、T2");
  });

  it("does not call a full block a block on some networks, though every probe of it is stored dark", () => {
    const l = countryLook(tPlural, fmt, check({ verdict: CountryVerdict.BLOCKED, probes: [dark("MTS", 8359, "Omsk"), dark("Beeline", 3216, "Moscow")] }));
    expect([l.kind, l.head, l.dark, l.note]).toEqual(["bad", "Neither ICMP nor TCP gets through", "", ""]);
    expect(l.probes.map((p) => [p.where, p.dark])).toEqual([
      ["MTS, Omsk", false],
      ["Beeline, Moscow", false],
    ]);
    expect(l.line).toBe("MTS, Beeline · 4 min ago");
    expect(l.state).not.toContain("some networks");
    expect(l.breakdown).toBeNull();
    expect(countryLook(tPlural, fmt, check()).breakdown).toBeNull();
  });

  it("says which named operators had no probe on any verdict, and nothing when every one had", () => {
    expect(countryLook(tPlural, fmt, check({ noProbe: ["MegaFon"] })).noProbe).toBe("No home/mobile probe this round: MegaFon");
    expect(countryLook(tPlural, fmt, check()).noProbe).toBe("");
    // two sources that disagree word a partial Globalping verdict in full
    const mixed = { ...some, confidence: "conflict", sources: ["globalping", "tspu"], globalpingVerdict: CountryVerdict.PARTIAL, tspuVerdict: CountryVerdict.OK };
    expect(countryLook(tPlural, fmt, mixed).conflict).toBe("Globalping: blocked on some networks · probe: reachable");
  });

  it("says PARTIAL in Russian and Chinese", () => {
    const ruL = countryLook(tRuPlural, fmtRu, some);
    expect([ruL.label, ruL.head, ruL.dark, ruL.noProbe]).toEqual([
      "часть сетей",
      "Доступна из России, но не из всех сетей",
      "Недоступна из части сетей: MTS (AS8359, Novosibirsk)",
      "В этом круге нет домашнего или мобильного зонда: MegaFon, T2",
    ]);
    expect(ruL.probes[0]!.where).toBe("MTS, Novosibirsk (до других нод доходит, до этой нет)");
    const zhL = countryLook(tZhPlural, fmtZh, some);
    expect([zhL.label, zhL.head, zhL.dark, zhL.noProbe]).toEqual([
      "部分网络",
      "从俄罗斯可以访问，但并非所有网络都能访问",
      "部分网络无法连接：MTS (AS8359, Novosibirsk)",
      "本轮没有家庭或移动网络的探测点：MegaFon、T2",
    ]);
    expect(zhL.probes[0]!.where).toBe("MTS, Novosibirsk（能到达你的其他节点，但到不了这个节点）");
  });

  it("makes a column only when there are checks, named by the country of the newest round", () => {
    expect(countryColumn(undefined)).toBeNull();
    expect(countryColumn([])).toBeNull();
    const col = countryColumn([check({ nodeId: "nod_1", country: "RU", atUnix: roundAt }), check({ nodeId: "nod_2", country: "KZ", atUnix: roundAt + 60 })])!;
    expect(col.code).toBe("KZ");
    expect(col.byNode.get("nod_1")?.country).toBe("RU");
    expect(col.byNode.has("nod_3")).toBe(false);
  });

  it("words the country_blocked alert with the country after “from” and the ports as a list", () => {
    const base = { kind: AlertKind.COUNTRY_BLOCKED, nodeName: "nl1", titleKey: "health.alert.country_blocked.title" };
    const tcp = alert({
      ...base,
      whyKey: "health.alert.country_blocked.why.tcp_blocked",
      params: { node: "nl1", country: "RU", verdict: "tcp_blocked", networks: "Beeline, Rostelecom", ports: "443, 8443" },
    });
    const one = { ...tcp, params: { ...tcp.params, ports: "443" } };
    const blocked = alert({ ...base, whyKey: "health.alert.country_blocked.why.blocked", params: { node: "fi1", country: "RU", verdict: "blocked", networks: "MTS", ports: "" } });
    expect(alertTitle(tPlural, tcp)).toBe("Unreachable from Russia");
    expect(alertTitle(tRuPlural, tcp)).toBe("Недоступна из России");
    expect(alertTitle(tZhPlural, tcp)).toBe("从俄罗斯无法连接");
    expect(alertWhy(tPlural, fmt, tcp)).toBe(
      "Probes inside Russia (Beeline, Rostelecom) reach the node’s address, but TCP to ports 443 and 8443 does not connect: the connection requests are dropped on the way. Two checks in a row saw it. It closes after two clean checks.",
    );
    expect(alertWhy(tPlural, fmt, one)).toContain("but TCP to port 443 does not connect");
    expect(alertWhy(tRuPlural, fmtRu, tcp)).toBe(
      "Зонды внутри России (Beeline, Rostelecom) достают до адреса ноды, но TCP на порты 443 и 8443 не соединяется: запросы на соединение режут по дороге. Так показали две проверки подряд. Алерт закроется после двух чистых проверок.",
    );
    expect(alertWhy(tRuPlural, fmtRu, one)).toContain("но TCP на порт 443 не соединяется");
    expect(alertWhy(tZhPlural, fmtZh, tcp)).toBe(
      "俄罗斯境内的探测点（Beeline, Rostelecom）能到达节点地址，但 TCP 端口 443 和 8443 连不上：连接请求在途中被丢弃。连续两次检测都是如此。连续两次正常后，告警会关闭。",
    );
    // a node with nothing but UDP profiles has no TCP port to name
    expect(alertWhy(tPlural, fmt, blocked)).toBe(
      "Probes inside Russia (MTS) cannot reach the node at all: no answer to ping, no TCP connection. Two checks in a row saw it. It closes after two clean checks.",
    );
    expect(alertWhy(tRuPlural, fmtRu, blocked)).toBe(
      "Зонды внутри России (MTS) совсем не достают до ноды: нет ответа ни на ping, ни по TCP. Так показали две проверки подряд. Алерт закроется после двух чистых проверок.",
    );
    expect(alertWhy(tZhPlural, fmtZh, blocked)).toBe("俄罗斯境内的探测点（MTS）完全无法到达节点：ping 和 TCP 都没有响应。连续两次检测都是如此。连续两次正常后，告警会关闭。");
  });

  it("words the partial alert by its subject: some networks in the title, the dark networks as the server named them", () => {
    const partial = alert({
      kind: AlertKind.COUNTRY_BLOCKED,
      nodeName: "de1",
      subject: "partial",
      titleKey: "health.alert.country_blocked.title",
      whyKey: "health.alert.country_blocked.why.partial",
      params: { node: "de1", country: "RU", verdict: "partial", confidence: "eyeball", sources: "globalping", networks: "MTS (AS8359, Novosibirsk)" },
    });
    expect(alertTitle(tPlural, partial)).toBe("Unreachable from some networks in Russia");
    expect(alertTitle(tRuPlural, partial)).toBe("Недоступна из части сетей России");
    expect(alertTitle(tZhPlural, partial)).toBe("从俄罗斯的部分网络无法连接");
    expect(alertWhy(tPlural, fmt, partial)).toBe(
      "Home or mobile probes on MTS (AS8359, Novosibirsk) get no answer from this node, though they reach your other nodes; other networks in Russia reach it. Two checks in a row saw it. It closes once these networks reach the node again.",
    );
    expect(alertWhy(tRuPlural, fmtRu, partial)).toBe(
      "Домашние и мобильные зонды в сетях MTS (AS8359, Novosibirsk) не получают ответа от этой ноды, хотя до других твоих нод доходят; из остальных сетей России она доступна. Так показали две проверки подряд. Алерт закроется, когда эти сети снова увидят ноду.",
    );
    expect(alertWhy(tZhPlural, fmtZh, partial)).toBe(
      "位于 MTS (AS8359, Novosibirsk) 的家庭或移动网络探测点收不到这个节点的任何响应，但能到达你的其他节点；俄罗斯境内的其他网络可以访问它。连续两次检测都是如此。这些网络重新能访问节点后，告警会关闭。",
    );
    // the full block keeps its own title; the network names stay text
    expect(alertTitle(tRuPlural, { ...partial, subject: "" })).toBe("Недоступна из России");
    expect(alertWhy(tPlural, fmt, { ...partial, params: { ...partial.params, networks: "<i>MTS</i> {country}" } })).toContain("probes on <i>MTS</i> {country} get no answer");
  });

  it("names the alert in an approval, where there is no country to fill in", () => {
    const fact = { key: "alert", value: "", untrusted: false, code: "alert", params: { kind: "country_blocked", severity: "warning" }, untrustedParams: [] };
    expect(factWords(tPlural, fmt, fact)).toEqual(["A node is unreachable from the chosen country · Warning"]);
    expect(factWords(tRuPlural, fmtRu, fact)).toEqual(["Нода недоступна из выбранной страны · Внимание"]);
    expect(factWords(tZhPlural, fmtZh, fact)).toEqual(["从所选国家无法连接节点 · 警告"]);
  });
  // the TSPU probe: one of the owner's nodes in the country; a verdict of its own, or one that agrees or disagrees with Globalping
  const runAt = roundAt - 600;
  const probeRow = {
    tspuVantageId: "nod_probe1",
    tspuVantageName: "probe1",
    tspuAtUnix: runAt,
    dpiHop: 6,
    tspuPorts: [
      { port: 443, verdict: "blocked" },
      { port: 8443, verdict: "blocked" },
    ],
  };
  // the probe alone: no Globalping round, no probes, the row's time is the run's
  const tspuOnly = (over: Partial<CountryCheck> = {}) =>
    check({ verdict: CountryVerdict.TCP_BLOCKED, confidence: "dc_only", eyeball: false, probes: [], atUnix: runAt, sources: ["tspu"], tspuVerdict: CountryVerdict.TCP_BLOCKED, ...probeRow, ...over });
  // Globalping's home networks connect, the probe node sees TCP die at the box: the worse is shown
  const conflict = check({
    verdict: CountryVerdict.TCP_BLOCKED,
    confidence: "conflict",
    sources: ["globalping", "tspu"],
    globalpingVerdict: CountryVerdict.OK,
    tspuVerdict: CountryVerdict.TCP_BLOCKED,
    ...probeRow,
  });

  it("says a block the probe alone saw without a word on ICMP, which it never looks at, and names the probe as the source", () => {
    const l = countryLook(tPlural, fmt, tspuOnly());
    expect([l.kind, l.label, l.note, l.head]).toEqual(["warn", "TCP blocked", "TSPU probe", "TCP is blocked"]);
    expect(l.state).toBe("TCP is blocked · TSPU probe");
    expect(l.probes).toEqual([]);
    expect(l.at).toBe("");
    expect(l.tspu).toEqual({ where: "Probe node probe1 · TSPU after hop 6", ports: "TCP 443, 8443: blocked", at: `Probe run: ${fmt.stamp(runAt)}` });
    expect(l.lists).toEqual([]); // no "Lists compared": this row is the probe's
    expect(l.via).toBe("Checked by your TSPU probe from a data center, TCP only");
    expect(l.line).toBe("TCP 443, 8443: blocked · probe node probe1 · 14 min ago");
    expect(l.line).not.toContain("ICMP");
    // the probe that sees the node through is quiet, as any OK
    const ok = countryLook(tPlural, fmt, tspuOnly({ verdict: CountryVerdict.OK, tspuVerdict: CountryVerdict.OK, tspuPorts: [{ port: 443, verdict: "open" }] }));
    expect([ok.kind, ok.note, ok.line]).toEqual(["ok", "", "TCP 443: open · probe node probe1 · 14 min ago"]);
  });

  it("says the same in Russian and Chinese", () => {
    const r = countryLook(tRuPlural, fmtRu, tspuOnly());
    expect([r.label, r.note, r.head, r.via]).toEqual(["TCP режут", "ТСПУ-зонд", "TCP режут", "Проверено твоим ТСПУ-зондом из датацентра, только TCP"]);
    expect(r.tspu?.where).toBe("Нода-зонд probe1 · ТСПУ за 6-м хопом");
    expect(r.line).toBe("TCP 443, 8443: режут · нода-зонд probe1 · 14 мин назад");
    const z = countryLook(tZhPlural, fmtZh, tspuOnly());
    expect([z.label, z.note, z.head]).toEqual(["TCP 被封锁", "TSPU 探测", "TCP 被封锁"]);
    expect(z.tspu?.where).toBe("探测节点 probe1 · TSPU 位于第 6 跳之后");
    expect(z.tspu?.ports).toBe("TCP 443, 8443：被封锁");
    expect(z.via).toBe("由你的 TSPU 探测从数据中心检测，仅测 TCP");
  });

  it("says plainly that the sources disagree, and what each of them said", () => {
    const l = countryLook(tPlural, fmt, conflict);
    expect([l.kind, l.label, l.note, l.head]).toEqual(["warn", "TCP blocked", "sources disagree", "TCP is blocked"]);
    expect(l.conflict).toBe("Globalping: reachable · probe: TCP blocked");
    expect(l.state).toBe("TCP is blocked · The sources disagree · Globalping: reachable · probe: TCP blocked");
    expect(l.probes).toHaveLength(2); // Globalping's probes stay in the tooltip
    expect(l.at).toBe(`Last round: ${fmt.stamp(roundAt)}`);
    expect(l.tspu?.ports).toBe("TCP 443, 8443: blocked");
    expect(l.via).toBe("Checked by Globalping (home and mobile networks) and your TSPU probe (a data center)");
    expect(l.line).toBe("sources disagree · Globalping: reachable · probe: TCP blocked · 4 min ago");
    expect(countryLook(tRuPlural, fmtRu, conflict).conflict).toBe("Globalping: доступна · зонд: TCP режут");
    expect(countryLook(tRuPlural, fmtRu, conflict).note).toBe("источники расходятся");
    expect(countryLook(tZhPlural, fmtZh, conflict).conflict).toBe("Globalping：可访问 · TSPU 探测：TCP 被封锁");
    // Globalping saw the TCP block and the probe went through: ICMP is still Globalping's word, the note says they disagree
    const other = countryLook(tPlural, fmt, { ...conflict, globalpingVerdict: CountryVerdict.TCP_BLOCKED, tspuVerdict: CountryVerdict.OK });
    expect([other.head, other.note, other.conflict]).toEqual(["TCP is blocked, ICMP passes", "sources disagree", "Globalping: TCP blocked · probe: reachable"]);
  });

  it("names both sources when they agree, and Globalping's reason when the probe decided alone", () => {
    const both = countryLook(tPlural, fmt, check({ sources: ["globalping", "tspu"], globalpingVerdict: CountryVerdict.OK, tspuVerdict: CountryVerdict.OK, ...probeRow, tspuPorts: [{ port: 443, verdict: "open" }] }));
    expect([both.kind, both.note, both.conflict]).toEqual(["ok", "", ""]);
    expect(both.line).toBe("Rostelecom, Beeline · probe node probe1 · 4 min ago");
    expect(countryLook(tPlural, fmt, check({ eyeball: false, sources: ["globalping", "tspu"], ...probeRow })).via).toBe(
      "Checked by Globalping (data centers) and your TSPU probe (a data center)",
    );
    // a Globalping round with too few probes, and the probe's verdict: the round's networks are not the verdict's
    const alone = countryLook(tPlural, fmt, tspuOnly({ reason: "too_few_probes", atUnix: roundAt, probes: [probe("Selectel", "Moscow", 38, [[443, true]])] }));
    expect(alone.gpNone).toBe("Globalping: no data, too few probes answered");
    expect(alone.line).toBe("TCP 443, 8443: blocked · probe node probe1 · 4 min ago");
    expect(alone.via).toBe("Checked by your TSPU probe from a data center, TCP only");
  });

  it("has no data when no port got a verdict in time, and does not call it the lists' row", () => {
    const l = countryLook(tPlural, fmt, tspuOnly({ verdict: CountryVerdict.UNKNOWN, confidence: "", sources: [], tspuVerdict: CountryVerdict.UNKNOWN, tspuPorts: [{ port: 443, verdict: "timeout" }] }));
    expect([l.kind, l.head]).toEqual(["unknown", "No data: no port got a verdict in time"]);
    expect(l.tspu?.ports).toBe("TCP 443: timed out");
    expect(l.lists).toEqual([]);
  });

  it("words a country_blocked alert of the probe alone: no networks, TCP only, a data-center view", () => {
    const base = { kind: AlertKind.COUNTRY_BLOCKED, nodeName: "se1", titleKey: "health.alert.country_blocked.title", whyKey: "health.alert.country_blocked.why.tcp_blocked" };
    const a = alert({ ...base, params: { node: "se1", country: "RU", verdict: "tcp_blocked", confidence: "dc_only", sources: "tspu", networks: "", ports: "443, 8443" } });
    expect(alertTitle(tPlural, a)).toBe("Unreachable from Russia");
    expect(alertWhy(tPlural, fmt, a)).toBe(
      "Your probe node inside Russia sees TCP to ports 443 and 8443 die at the filtering box (TSPU) on its path. That is a data-center view: home networks may differ. Two runs in a row saw it. It closes after two clean checks.",
    );
    expect(alertWhy(tRuPlural, fmtRu, a)).toBe(
      "Твоя нода-зонд внутри России видит, что TCP на порты 443 и 8443 обрывается на коробке фильтрации (ТСПУ) по пути. Это взгляд из датацентра: в домашних сетях может быть иначе. Так показали два запуска подряд. Алерт закроется после двух чистых проверок.",
    );
    expect(alertWhy(tZhPlural, fmtZh, a)).toBe(
      "你在俄罗斯境内的探测节点发现，到 TCP 端口 443 和 8443 的连接在途中的过滤设备（TSPU）处被切断。这是数据中心视角，家庭网络的情况可能不同。连续两次运行都是如此。连续两次正常后，告警会关闭。",
    );
    expect(alertWhy(tPlural, fmt, a)).not.toContain("()");
  });

  it("words a country_blocked alert of two sources that disagree", () => {
    const params = { node: "nl1", country: "RU", confidence: "conflict", sources: "globalping,tspu", networks: "Beeline, Rostelecom", ports: "443, 8443" };
    const tcp = alert({ kind: AlertKind.COUNTRY_BLOCKED, whyKey: "health.alert.country_blocked.why.tcp_blocked", params: { ...params, verdict: "tcp_blocked" } });
    const gone = alert({ kind: AlertKind.COUNTRY_BLOCKED, whyKey: "health.alert.country_blocked.why.blocked", params: { ...params, verdict: "blocked" } });
    expect(alertWhy(tPlural, fmt, tcp)).toBe(
      "The sources disagree: Globalping (probes inside Russia: Beeline, Rostelecom) and your probe node there see this node differently. One of them finds TCP to ports 443 and 8443 cut, so that is shown. Two checks in a row saw it. It closes after two clean checks.",
    );
    expect(alertWhy(tRuPlural, fmtRu, tcp)).toBe(
      "Источники расходятся: Globalping (зонды внутри России: Beeline, Rostelecom) и твоя нода-зонд там видят ноду по-разному. Один из них видит, что TCP на порты 443 и 8443 режут, поэтому показано это. Так показали две проверки подряд. Алерт закроется после двух чистых проверок.",
    );
    expect(alertWhy(tZhPlural, fmtZh, tcp)).toBe(
      "各来源的结果不一致：Globalping（俄罗斯境内的探测点：Beeline, Rostelecom）和你在那里的探测节点看到的情况不同。其中一方发现 TCP 端口 443 和 8443 被封锁，因此显示这一结果。连续两次检测都是如此。连续两次正常后，告警会关闭。",
    );
    expect(alertWhy(tPlural, fmt, gone)).toBe(
      "The sources disagree: Globalping’s probes inside Russia (Beeline, Rostelecom) cannot reach the node at all, no answer to ping, no TCP connection, while your probe node there sees it differently. The worse result is shown. Two checks in a row saw it. It closes after two clean checks.",
    );
    expect(alertWhy(tRuPlural, fmtRu, gone)).toBe(
      "Источники расходятся: зонды Globalping внутри России (Beeline, Rostelecom) совсем не достают до ноды, нет ответа ни на ping, ни по TCP, а твоя нода-зонд там видит иначе. Показан худший результат. Так показали две проверки подряд. Алерт закроется после двух чистых проверок.",
    );
    expect(alertWhy(tZhPlural, fmtZh, gone)).toBe(
      "各来源的结果不一致：俄罗斯境内的 Globalping 探测点（Beeline, Rostelecom）完全无法到达节点，ping 和 TCP 都没有响应，而你在那里的探测节点看到的情况不同。此处显示较差的结果。连续两次检测都是如此。连续两次正常后，告警会关闭。",
    );
    // both sources that agree keep Globalping's words: its probes saw it
    const agree = alert({ kind: AlertKind.COUNTRY_BLOCKED, whyKey: "health.alert.country_blocked.why.tcp_blocked", params: { ...params, confidence: "eyeball", verdict: "tcp_blocked" } });
    expect(alertWhy(tPlural, fmt, agree)).toMatch(/^Probes inside Russia \(Beeline, Rostelecom\) reach the node’s address/);
  });
});
