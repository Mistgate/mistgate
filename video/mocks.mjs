import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// The invented world of the videos: nodes de1, fi1, nl1, msk1 and the new sg1; people alice, bob, carol; documentation
// addresses only (RFC 5737) and example.com names. world(lang) returns the admin's /api answers as a table
// {"Service/Method": value | (body) => value} over a mutable state the takes change as they go.

/** carol's link: the address of her own page (the subscription and the page are one URL). */
export const LINK = "https://sub.example.com/s/7c2n9q4x1v8k3m6d5b0h";
export const NOW = Date.UTC(2026, 10, 3, 14, 20) / 1000; // the page's virtual clock starts here (UTC)
const M = 60;
const H = 3600;
const DAY = 86400;
const GB = 2 ** 30;

const places = {
  en: { DE: "Frankfurt", FI: "Helsinki", NL: "Amsterdam", RU: "Moscow", SG: "" },
  ru: { DE: "Франкфурт", FI: "Хельсинки", NL: "Амстердам", RU: "Москва", SG: "" },
  zh: { DE: "法兰克福", FI: "赫尔辛基", NL: "阿姆斯特丹", RU: "莫斯科", SG: "" },
  fa: { DE: "فرانکفورت", FI: "هلسینکی", NL: "آمستردام", RU: "مسکو", SG: "" },
  tr: { DE: "Frankfurt", FI: "Helsinki", NL: "Amsterdam", RU: "Moskova", SG: "" },
};
const groupNames = { en: ["Friends", "Family"], ru: ["Друзья", "Семья"], zh: ["朋友", "家人"], fa: ["دوستان", "خانواده"], tr: ["Arkadaşlar", "Aile"] };

// [id, name, country, address, online users by protocol, down Mbit/s, cpu]
const fleet = [
  ["nod_de1", "de1", "DE", "203.0.113.7", { "vless-reality": 2, hysteria2: 1 }, 64, 9],
  ["nod_fi1", "fi1", "FI", "192.0.2.15", { "vless-reality": 1 }, 21, 5],
  ["nod_nl1", "nl1", "NL", "203.0.113.41", { hysteria2: 1 }, 38, 7],
  ["nod_msk1", "msk1", "RU", "198.51.100.20", { "vless-reality": 1 }, 17, 4],
];
const SG = ["nod_sg1", "sg1", "SG", "203.0.113.50", {}, 0, 2];

// the protocols' settings schemas, read from this worktree's Go sources: the forms are the real ones
const schemaOf = (p) => /const settingsSchema = `([\s\S]*?)`/.exec(readFileSync(fileURLToPath(new URL(`../internal/panel/protocols/${p}/schema.go`, import.meta.url)), "utf8"))[1];
const protocols = () => [
  { id: "hysteria2", displayName: "Hysteria2", settingsSchemaJson: schemaOf("hysteria2"), defaultSettingsJson: JSON.stringify({ port: 443, tls_mode: "acme_domain", sni: "", obfs: { type: "salamander", password: "••••" }, masquerade: { type: "decoy" }, ignore_client_bandwidth: true, up_mbps: 0, down_mbps: 0, udp: true, egress: "direct" }), apps: ["APP_HAPP"] },
  { id: "vless", displayName: "VLESS", settingsSchemaJson: schemaOf("vless"), defaultSettingsJson: JSON.stringify({ port: 443, transport: "xhttp", security: "reality", reality: { target_kind: "self", target: "", server_names: [], short_ids: [], fingerprint: "chrome" }, xhttp: { path: "", host: "", mode: "auto", x_padding_bytes: "100-1000" }, egress: "direct" }), apps: ["APP_HAPP"] },
  { id: "awg", displayName: "AmneziaWG", settingsSchemaJson: schemaOf("awg"), defaultSettingsJson: JSON.stringify({ version: "3.1", port: 10819, mtu: 1280, subnet4: "10.66.4.0/22", subnet6: "fd66:66:0:1::/64", obfuscation: { preset: "dns" }, egress: "direct" }), apps: ["APP_AMNEZIA"] },
  { id: "mtproto", displayName: "MTProto", settingsSchemaJson: schemaOf("mtproto"), defaultSettingsJson: JSON.stringify({ port: 443, fake_tls: { kind: "own", domain: "" } }), apps: ["APP_TELEGRAM"] },
];
const F = ["SUB_FORMAT_BASE64_URIS", "SUB_FORMAT_MIHOMO_YAML", "SUB_FORMAT_XRAY_JSON", "SUB_FORMAT_SINGBOX_JSON"];

export function world(lang = "en") {
  const place = places[lang];
  const state = { sg1: "none", sg1Profile: false, provisionCalls: 0, alerts: false, journalFixed: false, carol: true, carolNew: false };

  const rows = () => (state.sg1 === "online" ? [...fleet, SG] : state.sg1 === "pending" ? [...fleet, SG] : fleet);
  const status = (id) => (id === "nod_sg1" && state.sg1 === "pending" ? "NODE_STATUS_PENDING" : "NODE_STATUS_ONLINE");
  const protos = (online, id) => (id === "nod_sg1" && !state.sg1Profile ? [] : id === "nod_sg1" ? ["vless"] : ["vless", "hysteria2"]);
  // a healthy node has no reason at all; the fresh sg1 runs but has no profile yet
  const reason = (id) => (id === "nod_sg1" && state.sg1 === "online" && !state.sg1Profile ? { reason: { code: "no_profiles", params: {} } } : {});
  const onlineList = (o) => Object.entries(o).map(([protocol, users]) => ({ protocol, users }));

  const node = ([id, name, cc, address, online, down, cpu]) => ({
    id, name, countryCode: cc, location: place[cc], provider: "Example Cloud", address, status: status(id), protocols: protos(online, id), online: onlineList(online),
    trafficTodayBytes: String(Math.round(down * 0.42 * GB)), hasMetrics: id !== "nod_sg1" || state.sg1 === "online", cpuPct: cpu, ramPct: 31, agentVersion: "v0.1.71",
    uptimeS: id === "nod_sg1" ? "40" : String(31 * DAY), lastSeenUnix: String(NOW - 4), awgBackend: "auto", bandwidthMbps: 1000, torrentBlockerEnabled: false,
    torrentBlockerSupported: true, decoyMode: "off", decoySupported: true, userRateLimitSupported: true, ...reason(id),
  });
  const card = (r) => {
    const n = node(r);
    const spark = Array.from({ length: 24 }, (_, i) => Math.round((r[5] || 0) * 1e5 * (1.2 + Math.sin(i / 3 + r[0].length))));
    return { id: n.id, name: n.name, countryCode: n.countryCode, location: n.location, provider: n.provider, status: n.status, ...(n.reason && { reason: n.reason }), online: n.online, downBps: r[5] * 1e6, upBps: r[5] * 0.3e6, hasMetrics: n.hasMetrics, cpuPct: n.cpuPct, sparkBytes: spark };
  };

  // 24 hourly points per protocol, a calm day curve
  const series = (scale) =>
    Array.from({ length: 24 }, (_, i) => {
      const start = NOW - NOW % H - (23 - i) * H;
      const hour = new Date(start * 1000).getUTCHours();
      const day = 0.35 + 0.65 * Math.max(0, Math.sin(((hour - 6) / 24) * 2 * Math.PI));
      return { startUnix: start, values: [{ protocol: "vless-reality", value: Math.round(scale * day * 0.62) }, { protocol: "hysteria2", value: Math.round(scale * day * 0.38) }] };
    });
  const ev = (id, ago, code, who, params = {}, severity = "EVENT_SEVERITY_INFO") => ({ id, timeUnix: NOW - ago, severity, code, params, nodeId: who.node ?? "", nodeName: who.nodeName ?? "", userId: who.user ?? "", userName: who.userName ?? "", inboundId: "", profileName: "", protocol: "", source: "" });
  const events = () => [
    ...(state.sg1 === "online" ? [ev(9, 20, "node_enrolled", { node: "nod_sg1", nodeName: "sg1" }, { agent_version: "v0.1.71" })] : []),
    ev(8, 42 * M, "user_created", { user: "usr_carol", userName: "carol" }),
    ev(7, 3 * H, "node_recovered", { node: "nod_nl1", nodeName: "nl1" }, { span: "2 min", minutes: "2" }),
    ev(6, 9 * H, "cert_renewed", { node: "nod_de1", nodeName: "de1" }),
  ];
  const overview = () => ({
    nowUnix: NOW, nodesTotal: rows().length, nodesProblem: 0, usersOnline: 6, nodes: rows().map(card), traffic: series(14 * GB), online: series(8).map((p) => ({ ...p, values: p.values.map((v) => ({ ...v, value: Math.max(1, Math.round(v.value)) })) })),
    events: events(), topConsumers: [
      { userId: "usr_alice", userName: "alice", nodeId: "nod_de1", nodeName: "de1", downBps: 31e6 },
      { userId: "usr_bob", userName: "bob", nodeId: "nod_nl1", nodeName: "nl1", downBps: 12e6 },
      { userId: "usr_carol", userName: "carol", nodeId: "nod_fi1", nodeName: "fi1", downBps: 4e6 },
    ], alertsActive: state.alerts ? alerts().active.length : 0, alertsCritical: 0,
  });

  // ---- the SSH install of sg1: the job walks its phases one poll at a time ----
  const phases = ["connecting", "preflight", "firewall", "transfer", "install", "waiting_node"];
  const job = () => {
    const i = state.provisionCalls++;
    const done = i >= phases.length;
    if (done) state.sg1 = "online";
    return { job: { id: "job_sg1", nodeId: "nod_sg1", name: "sg1", sshHost: "203.0.113.50", sshPort: 22, state: done ? "completed" : "running", phase: done ? "" : phases[i], errorCode: "", createdUnix: NOW, updatedUnix: NOW } };
  };

  // ---- Health (the alerts video): a few open alerts that fold by node and kind, one with a fix ----
  const A = (o) => ({
    severity: "ALERT_SEVERITY_WARNING", subject: "", params: {}, whyKey: "", lastSeenUnix: NOW - 10, resolvedAtUnix: 0, resolution: "", mutedUntilUnix: 0, actions: ["open_node", "mute"],
    ...o, nodeId: `nod_${o.nodeName}`, firstSeenUnix: o.openedUnix, openedUnix: o.openedUnix,
  });
  const lossy = (id, nodeName, port, profile, got, openedUnix) =>
    A({ id, kind: "ALERT_KIND_PORT_LOSSY", nodeName, subject: `inb_${id}`, titleKey: "health.alert.port_lossy.title", whyKey: "health.alert.port_lossy.why", params: { profile, port: String(port), sent: "300", got: String(got), sender: "fi1", checked_unix: String(NOW - 6 * M) }, actions: ["open_profiles", "open_node", "mute"], openedUnix });
  const journal = A({
    id: "alt_de1_journal", kind: "ALERT_KIND_DOCTOR_WARN", nodeName: "de1", subject: "journald_size", titleKey: "health.alert.doctor_warn.title", whyKey: "health.doctor.journald_size.why",
    params: { check: "journald_size", journal_mb: "3870", cap_mb: "500" }, actions: ["apply_fix:journald_vacuum", "open_node", "accept", "mute"], openedUnix: NOW - 2 * DAY - 4 * H,
  });
  const open = [
    A({ id: "alt_fi1_xhttp", kind: "ALERT_KIND_CHECK_FAILED", nodeName: "fi1", subject: "inb_fx", titleKey: "health.alert.check_failed.title", whyKey: "health.alert.check_failed.why.timeout", params: { inbound: "inb_fx", profile: "VLESS · XHTTP", port: "2053", error_code: "timeout", online: "2" }, actions: ["restart_inbound", "open_node", "mute"], openedUnix: NOW - 26 * M }),
    lossy("alt_nl1_8443", "nl1", 8443, "Hysteria2 · 8443", 207, NOW - 9 * H - 3 * M),
    lossy("alt_nl1_2083", "nl1", 2083, "Hysteria2 · 2083", 231, NOW - 9 * H - 3 * M),
    lossy("alt_nl1_4443", "nl1", 4443, "Hysteria2 · 4443", 264, NOW - 9 * H - 2 * M),
    lossy("alt_msk1_443", "msk1", 443, "Hysteria2 · 443", 243, NOW - 3 * H - 41 * M),
  ];
  const closed = (o, lasted, resolution = "cleared") => A({ ...o, resolution, resolvedAtUnix: o.openedUnix + lasted, actions: ["open_node"] });
  const blip = (id, nodeName, at, lasted) => closed({ id, kind: "ALERT_KIND_HOST_BLIP", severity: "ALERT_SEVERITY_INFO", nodeName, subject: "agent", titleKey: "health.alert.host_blip.title", whyKey: "health.alert.host_blip.why", openedUnix: at }, lasted, "node_returned");
  const past = [
    blip("alt_h1", "fi1", NOW - 4 * H - 10 * M, 95),
    closed({ id: "alt_h2", kind: "ALERT_KIND_NODE_DOWN", severity: "ALERT_SEVERITY_CRITICAL", nodeName: "nl1", subject: "agent", titleKey: "health.alert.node_down.title", whyKey: "health.alert.node_down.why", params: { minutes: "2" }, openedUnix: NOW - 3 * H - 2 * M }, 2 * M, "node_returned"),
    blip("alt_h3", "de1", NOW - DAY - 3 * H, 70),
    closed({ id: "alt_h4", kind: "ALERT_KIND_CERT_EXPIRY", nodeName: "nl1", subject: "inb_n3", titleKey: "health.alert.cert_expiry.title", whyKey: "health.alert.cert_expiry.why", params: { profile: "Hysteria2 · 8443", days_left: "6" }, openedUnix: NOW - 3 * DAY }, 20 * H),
  ];
  const alerts = () => ({
    nowUnix: NOW,
    active: state.journalFixed ? open : [journal, ...open],
    history: state.journalFixed ? [{ ...journal, resolution: "fix_applied", resolvedAtUnix: NOW + 8, actions: ["open_node"] }, ...past] : past,
  });
  const item = (id, status = "DOCTOR_STATUS_OK", extra = {}) => ({ id, status, titleKey: `doctor.${id}.title`, detail: "", params: {}, whyKey: "", fixId: "", measuredUnix: NOW - 5 * M, detailCode: "", acceptedUnix: 0, acceptedBy: "", acceptedByName: "", ...extra });
  const doctor = () => ({
    nowUnix: NOW,
    nodes: rows().map((r) => ({
      nodeId: r[0], nodeName: r[1], nodeStatus: "NODE_STATUS_ONLINE", agentSupported: true, hasReport: true, receivedUnix: NOW - 5 * M, ageS: 300, stale: false, agentVersion: "v0.1.71", lastSeenUnix: NOW - 4,
      items: [
        item("disk_space"), item("time_sync"), item("memory_pressure"), item("security_updates"),
        r[1] === "de1" && !state.journalFixed ? item("journald_size", "DOCTOR_STATUS_WARN", { params: { journal_mb: "3870", cap_mb: "500" }, whyKey: "health.doctor.journald_size.why", fixId: "journald_vacuum" }) : item("journald_size"),
      ],
    })),
  });

  const me = { admin: { id: "adm_1", displayName: "owner", role: "ROLE_OWNER" }, version: "v0.1.71", stepUpUntilUnix: String(NOW + DAY), sourceUrl: "", sourceStars: 0, edition: "", unavailable: [] };
  const groups = { groups: [
    { id: "grp_friends", name: groupNames[lang][0], profileIds: ["prf_vr", "prf_hy"], userCount: 2, color: "", happNodes: 4, amneziaNodes: 0, telegramNodes: 0, disabledNodeIds: [] },
    { id: "grp_family", name: groupNames[lang][1], profileIds: ["prf_vr", "prf_hy"], userCount: 1, color: "", happNodes: 4, amneziaNodes: 0, telegramNodes: 0, disabledNodeIds: [] },
  ] };
  const profile = (id, name, protocol, summary) => ({ id, name, protocol, summary, nodeCount: 4 + (id === "prf_vr" && state.sg1Profile ? 1 : 0), userCount: 3, warnings: [], version: 3, formats: [] });
  const profiles = () => [profile("prf_vr", "VLESS · Reality", "vless", "REALITY · 443"), profile("prf_hy", "Hysteria2", "hysteria2", "UDP · 8443"), ...(state.tgProfile ? [state.tgProfile] : [])];
  const inbound = (id, nodeId, nodeName) => ({ id, profileId: "prf_vr", profileName: "VLESS · Reality", protocol: "vless", nodeId, nodeName, port: 443, tlsServerName: "", state: "INBOUND_STATE_ACTIVE", lastError: "", certNotAfterUnix: "0", egress: "direct", portOverride: 0, tlsServerNameOverride: "", enabled: true });
  // [id, name, group, used GB, quota GB, online on, days left]
  const people = [
    ["usr_alice", "alice", 1, 38, 100, "nod_de1", 41],
    ["usr_bob", "bob", 0, 12, 50, "nod_nl1", 18],
    ["usr_carol", "carol", 0, 3, 50, "", 74],
  ];
  // a person in the lists; quotas and traffic in decimal GB, as the panel counts them; carol is new in the link video
  const user = ([id, name, g, used, quota, on, left]) => {
    const fresh = name === "carol" && state.carolNew;
    return {
      id, name, groupId: groups.groups[g].id, groupName: groups.groups[g].name, status: "USER_STATUS_ACTIVE", apps: { happ: true, amnezia: false, telegram: false }, via: fresh ? [] : ["APP_HAPP"],
      devicesUsed: fresh ? 0 : on ? 2 : 1, deviceLimit: fresh ? 5 : 3, usedBytes: fresh ? 0 : used * 1e9, quotaBytes: (fresh ? 100 : quota) * 1e9, quotaReset: "QUOTA_RESET_MONTH", nextResetUnix: NOW + 12 * DAY,
      expiresUnix: NOW + (fresh ? 30 : left) * DAY, lastSeenUnix: fresh ? 0 : on ? NOW - 30 : NOW - 5 * H, online: !fresh && !!on, currentNodeId: fresh ? "" : on, currentNodeName: on && !fresh ? on.slice(4) : "",
      speedLimitBps: 0, nodes: { all: true, nodeIds: [] }, createdUnix: fresh ? NOW : NOW - 60 * DAY, dnsPresetId: "", effectiveDnsPresetId: "", effectiveDnsPresetName: "", dnsSource: "DNS_SOURCE_DEFAULT",
      accessHapp: true, accessAmnezia: false, subscriptionName: "", groupColor: "", deviceSpeedLimitBps: 0, accessTelegram: false, telegramIssued: false,
    };
  };

  const table = {
    "AuthService/GetLoginInfo": { brandHead: "mist", brandTail: "gate", accent: "", language: lang, hasLogo: false, setupOpen: false, passwordLogin: true },
    "AuthService/Me": me,
    "FleetService/Overview": overview,
    "FleetService/ListEvents": () => ({ events: events(), hasMore: false }),
    "NodeService/ListNodes": () => ({ nodes: rows().map(node) }),
    "NodeService/GetNode": (b) => {
      const r = rows().find((x) => x[0] === b.nodeId) ?? SG;
      const fresh = r === SG;
      const inbounds = fresh ? (state.sg1Profile ? [inbound("inb_sg1", "nod_sg1", "sg1")] : []) : [inbound(`inb_${r[1]}_vr`, r[0], r[1]), { ...inbound(`inb_${r[1]}_hy`, r[0], r[1]), profileId: "prf_hy", profileName: "Hysteria2", protocol: "hysteria2", port: 8443 }];
      return {
        node: node(r), inbounds, onlineUsers: [], topToday: [], notes: "", dnsResolvers: [], enrollmentExpiresUnix: "0", createdUnix: String(NOW - (fresh ? 60 : 90 * DAY)), portChecks: [], tcpChecks: [],
        metrics: { atUnix: String(NOW - 3), cpuPct: r[6], softirqPct: 1, ramUsedBytes: String(Math.round(0.9 * GB)), ramTotalBytes: String(4 * GB), diskUsedBytes: String(4 * GB), diskTotalBytes: String(40 * GB), netRxBps: r[5] * 1e6, netTxBps: r[5] * 1.04e6 },
        facts: { hostname: r[1], os: "Debian 13", kernel: "6.12", arch: "amd64", cpuCount: 2, ramTotalBytes: String(4 * GB), diskTotalBytes: String(40 * GB), virt: "kvm", bootUnix: String(NOW - 31 * DAY), engines: ["xray 26.6.2", "hysteria 2.7.1"] },
      };
    },
    "ProfileService/CreateInbound": (b) => {
      if (b.profileId === "prf_tg") {
        const r = rows().find((x) => x[0] === b.nodeId);
        return { inbound: { ...inbound(`inb_tg_${r[1]}`, r[0], r[1]), profileId: "prf_tg", profileName: state.tgProfile.name, protocol: "mtproto" }, warnings: [], freePort: 0 };
      }
      if (!b.validateOnly) state.sg1Profile = true;
      return { inbound: inbound("inb_sg1", "nod_sg1", "sg1"), warnings: [], freePort: 0 };
    },
    "ProfileService/GetProfile": (b) => {
      const p = profiles().find((x) => x.id === b.profileId) ?? profiles()[0];
      const tg = p.protocol === "mtproto";
      const on = tg ? fleet : [...fleet, ...(state.sg1Profile && p.id === "prf_vr" ? [SG] : [])];
      return {
        profile: { ...p, nodeCount: on.length, userCount: 3 },
        settingsJson: tg ? JSON.stringify({ port: 443, fake_tls: { kind: "site", domain: "www.example.com" } }) : "{}",
        inbounds: on.map((r) => ({ ...inbound(`inb_${p.id}_${r[1]}`, r[0], r[1]), profileId: p.id, profileName: p.name, protocol: p.protocol })),
      };
    },
    "GroupService/UpdateGroup": (b) => {
      const g = groups.groups.find((x) => x.id === b.groupId);
      if (b.profileIds) g.profileIds = b.profileIds.values ?? [];
      if (g.profileIds.includes("prf_tg")) g.telegramNodes = 4;
      return { group: g };
    },
    "ProvisioningService/GetSSHFingerprint": (b) => ({ host: b.host, port: b.port, fingerprint: "SHA256:Qm4Vw7dJx2kR9tYc1fHn8sLp3aZe6uBg0iXo5NvK2Ws", algorithm: "ssh-ed25519" }),
    "ProvisioningService/CheckSSH": { preflight: { distribution: "Debian", version: "13", kernel: "6.12.48", architecture: "amd64", cpuCount: 2, memoryBytes: 4 * GB, diskAvailableBytes: 36 * GB, systemd: true, alreadyEnrolled: false, panelReachable: true } },
    "ProvisioningService/StartNodeProvision": () => ((state.sg1 = "pending"), { job: { id: "job_sg1", nodeId: "nod_sg1", name: "sg1", sshHost: "203.0.113.50", sshPort: 22, state: "queued", phase: "queued", errorCode: "", createdUnix: NOW, updatedUnix: NOW } }),
    "ProvisioningService/GetNodeProvision": job,
    "ProvisioningService/ListNodeProvisions": { jobs: [] },
    "ApprovalService/ListApprovals": { approvals: [], awaiting: 0, nowUnix: NOW },
    "UserService/ListUsers": () => {
      const list = people.filter((p) => state.carol || p[1] !== "carol").map(user);
      const sum = (f) => list.reduce((a, u) => a + f(u), 0);
      return { users: list, counts: { all: list.length, online: sum((u) => +u.online), expiring: 0, overQuota: 0, devicesUsed: sum((u) => u.devicesUsed), devicesOnline: sum((u) => +u.online), overDeviceLimit: 0, telegram: 0 } };
    },
    "UserService/CreateUser": (b) => {
      state.carol = true;
      state.carolNew = true;
      return { user: user(people[2]), subscriptionUrl: LINK, pagePassword: "" };
    },
    "UserService/GetSubscriptionLink": { url: LINK, pagePassword: "" },
    "ProfileService/ListProfiles": () => ({ profiles: profiles() }),
    "GroupService/ListGroups": groups,
    "SubscriptionService/GetSubscriptionSettings": { settings: { updateIntervalHours: 12, apps: [{ kind: "APP_HAPP", name: "Happ" }] }, effectiveTitle: "Mistgate", serverSamples: [] },
    "UpdateService/GetUpdates": {},
    "HealthService/ListAlerts": () => (state.alerts ? alerts() : { nowUnix: NOW, active: [], history: [] }),
    "HealthService/GetDoctor": () => (state.alerts ? doctor() : { nowUnix: NOW, nodes: [] }),
    "HealthService/ApplyFix": (b) => {
      if (b.dryRun) return { plan: { fixId: b.fixId, titleKey: "health.fix.journald_vacuum.plan", params: { before_mb: "3870", target_mb: "500" }, detail: "", disruptive: false }, planId: "pln_journal", planExpiresUnix: NOW + 600 };
      state.journalFixed = true;
      return { applied: true, affected: 1, error: "", resultParams: { freed_mb: "3370" } };
    },
    "HealthService/GetChecks": { nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks: [] },
    "CountryReachabilityService/GetCountryReachability": { settings: { country: "RU" } },
    "RelayService/ListRelays": { relays: [] },
    "ProfileService/ListProtocols": () => ({ protocols: protocols() }),
    "ProfileService/PreviewProfile": (b) => {
      const tg = b.protocol === "mtproto";
      const formats = F.map((format) => ({ format, ok: !tg, note: tg ? "unsupported" : "" }));
      return { errors: [], warnings: [], clientPreview: tg ? tgUrl("node.example.com") : `${b.protocol}://••••@node.example.com:443`, clientLabel: tg ? "Telegram · tg:// link" : "Subscription link · URI list", formats };
    },
    "ProfileService/CreateProfile": (b) => {
      state.tgProfile = { id: "prf_tg", name: b.name || "MTProto · 443", protocol: b.protocol, summary: "TCP 443 · Fake TLS · www.example.com", nodeCount: 4, userCount: 3, warnings: [], version: 1, formats: [] };
      return { profile: state.tgProfile };
    },
    "AwgService/ListMimicryPresets": { presets: [], domains: [] },
    "DnsService/ListDnsPresets": { presets: [], providers: [], clientSupport: [] },
    "NodeService/GetNodeConnections": { supported: true, inbounds: [] },
  };
  return { table, state };
}

/** A tg://proxy link with a Fake TLS secret for www.example.com. */
export const tgUrl = (host, port = 443) => `tg://proxy?server=${host}&port=${port}&secret=ee0123456789abcdef0123456789abcdef${Buffer.from("www.example.com").toString("hex")}`;

/**
 * A person's own page (the public page of their link) in a language: their servers (sg1 among them when `sg1` is set),
 * the Telegram proxy links when `telegram` is set. Built on the dev server's sample of a visit (apps, devices) with
 * everything the video shows replaced by the invented world; no DNS block, no announcement, no support link.
 */
export function person(lang, sample, { name = "alice", sg1 = false, telegram = false } = {}) {
  const country = new Intl.DisplayNames([lang === "zh" ? "zh-CN" : lang], { type: "region" });
  const city = places[lang];
  const label = (cc) => (city[cc] ? `${country.of(cc)} · ${city[cc]}` : country.of(cc));
  const server = ([id, , cc], load) => ({
    id, country_code: cc, place: city[cc], label: label(cc), app_names: [`${cc} · VLESS`], online: true, load, dns: null,
    connections: [{ way: "link", exit: "direct", app_name: `${cc} · VLESS`, profile_id: "" }],
  });
  const servers = [server(fleet[0], "medium"), server(fleet[1], "low"), server(fleet[2], "low"), server(fleet[3], "low"), ...(sg1 ? [server(SG, "low")] : [])];
  const tg = (cc, host, extra = {}) => ({ label: label(cc), country_code: cc, place: city[cc], ipv6: false, relay: false, via: "", url: tgUrl(host), ...extra });
  return {
    ...sample, lang, title: "Mistgate", subscription_url: "https://sub.example.com/s/3f9k2v8q1x7m4c6d9b2n5h8j", announcement: "", support_url: "",
    options: { ...sample.options, show_announcement: false, show_support: false },
    user: { ...sample.user, name },
    server_count: servers.length, servers, server_loads: servers.map((x) => ({ name: x.label, level: x.load })),
    dns: null, dns_presets: [],
    access: { ...sample.access, telegram },
    telegram: telegram ? { links: [tg("DE", "de1.example.com"), tg("DE", "[2001:db8::7]", { ipv6: true }), tg("FI", "fi1.example.com")] } : null,
    devices: telegram ? sample.devices : sample.devices.filter((d) => d.app !== "telegram"),
  };
}
