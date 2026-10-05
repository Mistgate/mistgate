import type { AuditEntry } from "@/gen/mistgate/admin/v1/auth_pb";
import type { T } from "@/i18n";
import { en, type MessageKey } from "@/i18n/en";

// Audit actions are open-ended strings written by the panel ("login", "node.retire", ...). Known ones get a
// sentence in the UI language; an unknown one is shown as it is, so a new module never leaves a blank row.
const keys: Record<string, MessageKey> = {
  setup: "audit.setup",
  login: "audit.login",
  login_password: "audit.login_password",
  logout: "audit.logout",
  lockout: "audit.lockout",
  passkey_add: "audit.passkey_add",
  passkey_remove: "audit.passkey_remove",
  password_change: "audit.password_change",
  password_add: "audit.password_add",
  totp_rebind: "audit.totp_rebind",
  reset_login: "audit.reset_login",
  session_end: "audit.session_end",
  sessions_end_others: "audit.sessions_end_others",
  instance_update: "audit.instance_update",
  "node.create_enrollment": "audit.node.create_enrollment",
  "node.update": "audit.node.update",
  "node.restart_inbounds": "audit.node.restart_inbounds",
  "node.retire": "audit.node.retire",
  "node.awg_prepare": "audit.node.awg_prepare",
  "node.bandwidth_measure": "audit.node.bandwidth_measure",
  "node.bandwidth_auto": "audit.node.bandwidth_auto",
  stepup: "audit.stepup",
  captcha: "audit.captcha",
  turnstile_off: "audit.turnstile_off",
  security_update: "audit.security_update",
  page_unlock_lockout: "audit.page_unlock_lockout",
  subscription_settings_update: "audit.subscription_settings_update",
  user_create: "audit.user_create",
  user_update: "audit.user_update",
  user_delete: "audit.user_delete",
  users_disable: "audit.users_disable",
  users_enable: "audit.users_enable",
  users_extend: "audit.users_extend",
  group_create: "audit.group_create",
  group_update: "audit.group_update",
  group_delete: "audit.group_delete",
  profile_create: "audit.profile_create",
  profile_update: "audit.profile_update",
  profile_delete: "audit.profile_delete",
  inbound_add: "audit.inbound_add",
  inbound_update: "audit.inbound_update",
  inbound_remove: "audit.inbound_remove",
  preset_create: "audit.preset_create",
  preset_update: "audit.preset_update",
  preset_delete: "audit.preset_delete",
  preset_default: "audit.preset_default",
  node_dns_options: "audit.node_dns_options",
  page_dns_choice: "audit.page_dns_choice",
  user_dns_choices_reset: "audit.user_dns_choices_reset",
  device_create: "audit.device_create",
  device_configs: "audit.device_configs",
  device_rotate: "audit.device_rotate",
  device_revoke: "audit.device_revoke",
  user_reset_traffic: "audit.user_reset_traffic",
  "health.mute_alert": "audit.health.mute_alert",
  "health.fix_plan": "audit.health.fix_plan",
  "health.apply_fix": "audit.health.apply_fix",
  update_rollout_start: "audit.update_rollout_start",
  update_rollout_pause: "audit.update_rollout_pause",
  update_rollout_resume: "audit.update_rollout_resume",
  update_rollout_cancel: "audit.update_rollout_cancel",
  update_rollback_node: "audit.update_rollback_node",
  update_rescan: "audit.update_rescan",
  warp_params: "audit.warp_params",
  warp_register: "audit.warp_register",
  warp_import: "audit.warp_import",
  warp_enable: "audit.warp_enable",
  warp_disable: "audit.warp_disable",
  warp_refresh: "audit.warp_refresh",
  warp_delete: "audit.warp_delete",
  warp_reregister: "audit.warp_reregister",
  token_create: "audit.token_create",
  token_revoke: "audit.token_revoke",
  approval_approve: "audit.approval_approve",
  approval_reject: "audit.approval_reject",
  mcp_plan: "audit.mcp_plan",
  mcp_apply: "audit.mcp_apply",
  call: "audit.call",
};

const hasKey = (k: string): k is MessageKey => Object.hasOwn(en, k);

/** An MCP tool code ("user_enable") as the Integrations page names it; unknown codes stay as they are. */
function toolTitle(t: T, tool = ""): string {
  const key = `approval.tool.${tool}`;
  return hasKey(key) ? t(key) : tool;
}

function parseParams(json: string): Record<string, string> {
  try {
    const v: unknown = JSON.parse(json);
    if (!v || typeof v !== "object") return {};
    return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, typeof x === "string" ? x : JSON.stringify(x)]));
  } catch {
    return {};
  }
}

/** Results other than "ok" are worth a second look in the list. */
export const isFailure = (result: string) => result !== "" && result !== "ok";

/** A result word in the UI language ("не удалось", "заблокировано"); an unknown one stays as it came. */
export function resultText(t: T, result: string): string {
  const key = `audit.result.${result}`;
  return hasKey(key) ? t(key) : result;
}

/**
 * The key of the sentence for a row, which may depend on its params and result: a sign-in names how it went and whether
 * it failed, a bulk change of one person reads in the singular, a variant of an action has a key of its own
 * ("<key>.<variant>"). `worded` says the sentence already tells that the action failed (no result word needed).
 */
function sentenceKey(e: Pick<AuditEntry, "action" | "result">, p: Record<string, string>): { key: MessageKey; worded: boolean } | null {
  const base = keys[e.action];
  if (!base) return null;
  const variant = (v: string, worded = false) => (hasKey(`${base}.${v}`) ? { key: `${base}.${v}` as MessageKey, worded } : { key: base, worded: false });
  const failed = isFailure(e.result);
  switch (e.action) {
    case "login":
      if (!failed) return variant(p.method === "password" ? "password" : "passkey");
      if (p.method !== "password") return variant("failPasskey", true);
      return variant(p.login && p.login !== "?" ? "failPassword" : "failPasswordUnknown", true);
    case "lockout":
      return variant(p.login && p.login !== "?" ? "login" : "source", true);
    case "login_password":
    case "captcha":
    case "page_unlock_lockout":
      return { key: base, worded: true };
    case "stepup":
    case "password_change":
      return failed ? variant("fail", true) : { key: base, worded: false };
    case "security_update":
      return e.result === "rejected" ? variant("rejected", true) : { key: base, worded: false };
    case "user_delete":
    case "users_disable":
    case "users_enable":
    case "users_extend":
      return p.count === "1" ? variant("one") : { key: base, worded: false };
    case "inbound_update":
      return p.enabled === "false" ? variant("off") : { key: base, worded: false };
    case "preset_default":
      return p.name ? { key: base, worded: false } : variant("builtin");
    case "node_dns_options":
      return p.presets === "0" ? variant("off") : { key: base, worded: false };
    case "page_dns_choice":
      return p.preset ? { key: base, worded: false } : variant("default");
    case "reset_login":
      return p.created === "true" ? variant("added") : { key: base, worded: false };
    case "mcp_plan":
      return p.needs_approval === "true" ? { key: "audit.mcp_plan_owner", worded: false } : { key: base, worded: false };
  }
  return { key: base, worded: false };
}

/**
 * The sentence for an audit row, without who did it. A failed row says so in its own words, or ends with the result in
 * the UI language ("— отклонено"); the server's English never shows.
 */
export function describeAudit(t: T, e: Pick<AuditEntry, "action" | "paramsJson"> & Partial<Pick<AuditEntry, "result">>): string {
  const result = e.result ?? "";
  const p = parseParams(e.paramsJson);
  const k = sentenceKey({ action: e.action, result }, p);
  if (!k) return isFailure(result) ? `${e.action} — ${resultText(t, result)}` : e.action;
  if (p.tool !== undefined) p.tool = toolTitle(t, p.tool);
  if (p.procedure !== undefined) p.procedure = p.procedure.replace(/^\/mistgate\.admin\.v1\./, "");
  const text = t(k.key, p).replace(/\s+/g, " ").trim();
  return isFailure(result) && !k.worded ? `${text} — ${resultText(t, result)}` : text;
}

/**
 * Who did it. Tokens write as "token:<id>" (API) or "mcp:<id>" (agent); the server joins the token's name into
 * actorName, which is shown when it is there and the bare id when it is not (a deleted token). A failed sign-in names
 * the admin whose login was tried, but nobody knows who tried it, and a lockout is the panel's own doing: those rows
 * name no admin.
 */
export function actorLabel(t: T, e: Pick<AuditEntry, "actorId" | "actorName"> & Partial<Pick<AuditEntry, "action" | "result">>): string {
  if (e.actorId === "anonymous") return t("audit.anonymous");
  if ((e.action === "login" || e.action === "login_password") && isFailure(e.result ?? "")) return t("audit.anonymous");
  if (e.action === "lockout") return t("audit.actor.system");
  const [kind, ...rest] = e.actorId.split(":");
  if (kind === "token" || kind === "mcp") return t(`audit.actor.${kind}`, { name: e.actorName || rest.join(":") });
  if (kind === "user") return t("audit.actor.user");
  if (e.actorId === "cli" || e.actorId === "system") return t(`audit.actor.${e.actorId}`);
  return e.actorName || e.actorId;
}
