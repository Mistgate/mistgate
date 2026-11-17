import { AuditKind, AuditSource } from "@/gen/mistgate/admin/v1/auth_pb";
import type { RestoreDemo } from "@/screens/settings-pages/restore";
import type { AuditDemo } from "@/screens/settings-pages/audit";
import type { BackupsDemo } from "@/screens/settings-pages/backups";
import type { SecurityDemo } from "@/screens/settings-pages/security";
import type { SessionsDemo } from "@/screens/settings-pages/sessions";
import { NOW } from "./dev-kit-profiles-data";

// Development only. The made-up data and the states of the four tabs of Part B in /dev-kit?only=pn-settings (Security, Sessions, Audit and
// Backups; PnSettings: sxIs_sec, sxIs_ses, sxIs_aud, sxIs_bk): a panel called owner, the documentation addresses, nodes de1 … nl1.

const H = 3600;
const D = 86400;
const MiB = 1024 * 1024;

// ---- security

export const passkeys = (state: string) =>
  state === "nopk"
    ? []
    : [
        { id: "pk1", name: "Laptop (Windows Hello)", createdAtUnix: NOW - 90 * D, lastUsedAtUnix: NOW - 6 * H, backedUp: false },
        ...(state === "onepk" ? [] : [{ id: "pk2", name: "Phone", createdAtUnix: NOW - 40 * D, lastUsedAtUnix: NOW - 3 * D, backedUp: true }]),
      ];

/** GetPasswordLogin: a password login with an authenticator app, none yet (a passkey admin), or a server with no master key. */
export const passwordLogin = (state: string) =>
  state === "nopw" || state === "pw-add" || state === "pw-add-qr" ? { enabled: false, login: "", available: true } : state === "nokey" ? { enabled: false, login: "", available: false } : { enabled: true, login: "owner", available: true };

/** GetSecuritySettings: the Cloudflare check on with its keys (the board's calm), off, or on with no secret key yet. */
export const securitySettings = (state: string) => ({
  turnstileEnabled: state !== "tsoff" && state !== "ts-off",
  turnstileSiteKey: "0x4AAAAAAAexampleSiteKey",
  turnstileSecretSet: state !== "nosec",
});

export const securityDemo = (state: string): SecurityDemo => ({
  ...(state === "pq" && { add: {} }),
  ...(state === "pq-err" && { add: { error: true } }),
  ...(state === "pq-busy" && { add: { name: "Tablet", busy: true } }),
  ...(state === "pk-del" && { remove: "pk1" }),
  ...(state === "pk-new" && { fresh: "pk2" }),
  password: {
    ...(state === "lock" && { lock: true }),
    ...(state === "pw" && { window: "change" as const }),
    ...(state === "pw-err" && { window: "change-err" as const }),
    ...(state === "pw-busy" && { window: "change-busy" as const }),
    ...(state === "pw-add" && { window: "add" as const }),
    ...(state === "pw-add-qr" && { window: "add-qr" as const }),
    ...(state === "pw-rebind" && { window: "rebind" as const }),
  },
  cloudflare: { ...(state === "ts-del" && { removing: true }), ...(state === "ts-proof" && { proving: true }) },
});

// ---- sessions

const UA = {
  chrome: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36",
  safari: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
  firefox: "Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0",
};
export const sessions = (state: string) => [
  { id: "s1", current: true, userAgent: UA.chrome, ip: "203.0.113.120", createdAtUnix: NOW - 5 * H, lastSeenAtUnix: NOW - 20 },
  ...(state === "one" ? [] : [
    { id: "s2", current: false, userAgent: UA.safari, ip: "198.51.100.45", createdAtUnix: NOW - 2 * D, lastSeenAtUnix: NOW - 3 * H },
    { id: "s3", current: false, userAgent: UA.firefox, ip: "192.0.2.61", createdAtUnix: NOW - 9 * D, lastSeenAtUnix: NOW - 5 * D },
  ]),
];
export const sessionsDemo = (state: string): SessionsDemo => ({ end: state === "end" });

// ---- audit: [source, actor, action, params, ip, ago in seconds, result, id]

type Row = [AuditSource, string, string, Record<string, unknown>, string, number, string];
const ROWS: Row[] = [
  [AuditSource.PANEL, "owner", "users_extend", { count: 1, days: 30, names: "alice" }, "203.0.113.120", 3 * 60, "ok"],
  [AuditSource.PANEL, "owner", "user_create", { name: "carol" }, "203.0.113.120", 40 * 60, "ok"],
  [AuditSource.MCP, "claude-ops", "node.restart_inbounds", { node: "de1" }, "198.51.100.77", 2 * H, "ok"],
  [AuditSource.PANEL, "owner", "node.update", { node: "de1" }, "203.0.113.120", 4 * H, "ok"],
  [AuditSource.PANEL, "owner", "login", { method: "passkey" }, "203.0.113.120", 5 * H, "ok"],
  [AuditSource.PANEL, "owner", "subscription_settings_update", {}, "203.0.113.120", 9 * H, "ok"],
  [AuditSource.BOT, "owner", "users_disable", { count: 1, names: "bob" }, "", 1 * D + 2 * H, "ok"],
  [AuditSource.PANEL, "owner", "group_update", { name: "Friends" }, "203.0.113.120", 2 * D, "ok"],
  [AuditSource.PANEL, "", "login", { method: "password", login: "?" }, "192.0.2.200", 2 * D + 1 * H, "fail"],
  [AuditSource.ASSISTANT, "owner", "node.ports_check", { node: "nl1" }, "203.0.113.120", 3 * D, "ok"],
  [AuditSource.PANEL, "owner", "passkey_add", {}, "203.0.113.120", 8 * D, "ok"],
];
const FAILS = new Set(["fail"]);
const SIGN = new Set(["login", "passkey_add", "login_password", "lockout"]);

/** The audit page of a request: the rows that fit the kind and the source, newest first (the board's AUD, its filters and its empty state). */
export function auditPage(req: { source?: AuditSource; kind?: AuditKind }, state: string) {
  const rows = state === "none" ? [] : ROWS;
  const entries = rows
    .map((r, i) => ({ id: 400 - i, timeUnix: NOW - r[5], source: r[0], actorId: r[1] ? "adm_1" : "anonymous", actorName: r[1], action: r[2], paramsJson: JSON.stringify(r[3]), result: r[6], ip: r[4] }))
    .filter((e) => (!req.source || e.source === req.source) && (!req.kind || (req.kind === AuditKind.FAILURES ? FAILS.has(e.result) : req.kind === AuditKind.SIGN_IN ? SIGN.has(e.action) : !SIGN.has(e.action) && !FAILS.has(e.result))));
  return { entries, nextBeforeId: state === "paged" && entries.length > 0 ? entries.at(-1)!.id : 0 };
}
export const auditDemo = (state: string): AuditDemo => ({
  ...(state === "in" && { kind: "signin" as const }),
  ...(state === "ch" && { kind: "changes" as const }),
  ...(state === "fail" && { kind: "failures" as const }),
  ...(state === "mcp" && { source: "mcp" as const }),
  ...(state === "nomatch" && { kind: "failures" as const, source: "mcp" as const }),
});
/** The filters of a state of the audit page, as the query keys them. */
export const auditFilter = (state: string) => {
  const d = auditDemo(state);
  const src = { all: AuditSource.UNSPECIFIED, panel: AuditSource.PANEL, bot: AuditSource.BOT, mcp: AuditSource.MCP, api: AuditSource.API, assistant: AuditSource.ASSISTANT }[d.source ?? "all"];
  const kind = { all: AuditKind.UNSPECIFIED, signin: AuditKind.SIGN_IN, changes: AuditKind.CHANGES, failures: AuditKind.FAILURES }[d.kind ?? "all"];
  return { key: ["audit", d.source ?? "all", d.kind ?? "all", 0, 50] as const, req: { source: src, kind } };
};

// ---- backups

const AT = (days: number) => BigInt(NOW - days * D);
export const backupSettings = (state: string) =>
  state === "none"
    ? { accountId: "", jurisdiction: "default", bucket: "", accessKeyId: "", hasSecret: false, ageRecipient: "", enabled: false, intervalHours: 24, retentionDays: 14, lastSuccessUnix: 0, lastErrorCode: "", running: false }
    : {
        accountId: "4f1e2d3c4b5a69788796a5b4c3d2e1f0",
        jurisdiction: "eu",
        bucket: "mistgate-backups",
        accessKeyId: "9a8b7c6d5e4f30211203f4e5d6c7b8a9",
        hasSecret: true,
        ageRecipient: "age1qx8example7recovery2public9key4for6mistgate3backups5only0aa",
        enabled: state !== "manual",
        intervalHours: 24,
        retentionDays: 14,
        lastSuccessUnix: NOW - 11 * H,
        lastErrorCode: state === "fail" ? "backup_create_failed" : "",
        running: state === "running",
      };
const OLD = [
  { key: "mistgate-2026-11-03T03-00.db.age", mib: 9.1, ago: 11 * H },
  { key: "mistgate-2026-11-02T03-00.db.age", mib: 9, ago: D + 11 * H },
  { key: "mistgate-2026-11-01T03-00.db.age", mib: 8.9, ago: 2 * D + 11 * H },
  { key: "mistgate-2026-10-31T03-00.db.age", mib: 8.9, ago: 3 * D + 11 * H },
];
const FRESH = "mistgate-2026-11-04T03-00.db.age";
export const backupList = (state: string) =>
  state === "none" || state === "nobks"
    ? []
    : [...(state === "new" ? [{ key: FRESH, sizeBytes: Math.round(9.2 * MiB), createdUnix: NOW - 60 }] : []), ...OLD.map((b) => ({ key: b.key, sizeBytes: Math.round(b.mib * MiB), createdUnix: NOW - b.ago }))];
export const restoreResult = (state: string) =>
  state === "lastres"
    ? { outcome: "restored", source: "r2", fileName: OLD[0]!.key, madeByVersion: "v0.1.79", completedUnix: BigInt(NOW - 3 * H) }
    : state === "lastfail"
      ? { outcome: "failed", source: "upload", fileName: "mistgate-backup-panel-example-com.tar.gz.age", madeByVersion: "", completedUnix: BigInt(NOW - 3 * H) }
      : { outcome: "", source: "", fileName: "", madeByVersion: "", completedUnix: 0n };
export const keptData = (state: string) => (state === "kept" || state === "lastres" ? [{ name: "mistgate.before-restore-20261103T062012Z", sizeBytes: 2_621_440n, modifiedUnix: AT(0.125) }] : []);

const SUMMARY = { panelName: "Mistgate", madeByVersion: "v0.1.79", createdUnix: AT(1), backupNodes: 4n, backupUsers: 118n, backupProfiles: 7n, currentNodes: 5n, currentUsers: 131n, currentProfiles: 7n } as unknown as NonNullable<RestoreDemo["summary"]>;
const PICK = "mistgate-2026-11-02T03-00-00.db.age";
const restoreOf = (state: string): BackupsDemo["restore"] | undefined => {
  const open = { pick: PICK, source: "r2" as const, keyKind: "identity" as const };
  switch (state) {
    case "rs-pick":
      return { ...open };
    case "rs-chk":
      return { ...open, step: "busy" };
    case "rs-ok":
      return { ...open, step: "confirm", summary: SUMMARY };
    case "rs-typed":
      return { ...open, step: "confirm", summary: SUMMARY, typed: "Mistgate" };
    case "rs-run":
      return { ...open, step: "restarting" };
    case "rs-back":
      return { ...open, step: "back" };
    case "rs-late":
      return { ...open, step: "timeout" };
    case "rs-wrong":
      return { ...open, error: "set.backups.rs.err.open" };
    case "rs-file":
      return { manual: true, source: "file", keyKind: "passphrase", fileName: "mistgate-backup-panel-example-com-v0.1.79.tar.gz.age" };
    case "rs-upload":
      return { manual: true, step: "busy", source: "file", keyKind: "passphrase", fileName: "mistgate-backup-panel-example-com-v0.1.79.tar.gz.age", progress: 0.64 };
  }
};
export const backupsDemo = (state: string): BackupsDemo => ({
  ...(state === "r2busy" && { r2: "busy" as const }),
  ...(state === "r2ok" && { r2: "ok" as const }),
  ...(state === "r2bad" && { r2: "bad" as const }),
  ...(state === "running" && { running: true }),
  ...(state === "dl" && { download: "busy" as const }),
  ...(state === "pass" && { windows: "pass" as const }),
  ...(state === "secret" && { windows: "secret" as const }),
  ...(state === "secshown" && { secret: true }),
  ...(state === "new" && { fresh: FRESH }),
  restore: restoreOf(state),
});
