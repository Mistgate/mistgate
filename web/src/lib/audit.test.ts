import { describe, expect, it } from "vitest";
import { fill } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { actorLabel, describeAudit, resultText } from "./audit";

const tIn = (d: Record<string, string>) =>
  Object.assign((key: string, vars?: Record<string, string | number>) => fill(d[key]!, vars), { n: () => "" }) as never;
const tEn = tIn(en);
const tRu = tIn(ru);

// Every action string the panel writes (internal/panel/*: auth, access, dns, fleet, health, update, warp, subs, mcp, the
// command line) with the params and the result it writes, so the dictionary and the server stay in step.
const written: [string, Record<string, unknown>, string?][] = [
  ["setup", { method: "password" }],
  ["login", { method: "passkey" }],
  ["login", { method: "password" }],
  ["login", { method: "password", login: "admin" }, "fail"],
  ["login", { method: "password", login: "?" }, "fail"],
  ["login", {}, "fail"],
  ["login_password", {}, "locked"],
  ["logout", {}],
  ["lockout", { login: "x", until: 1 }, "locked"],
  ["lockout", { source: "203.0.113.0", until: 1 }, "locked"],
  ["stepup", { method: "totp" }],
  ["stepup", { reason: "wrong or used code" }, "fail"],
  ["captcha", { for: "login", reason: "bad" }, "fail"],
  ["turnstile_off", {}],
  ["security_update", { turnstile_enabled: true }],
  ["security_update", { turnstile_enabled: true, reason: "turnstile_secret_rejected" }, "rejected"],
  ["passkey_add", { passkey: "pk_1" }],
  ["passkey_remove", { passkey: "pk_1" }],
  ["password_change", { login: "admin", sessions_ended: 2 }],
  ["password_change", { reason: "wrong current password" }, "fail"],
  ["password_add", { login: "admin" }],
  ["totp_rebind", { login: "admin", sessions_ended: 1 }],
  ["reset_login", { admin: "adm_1", login: "admin", created: false }],
  ["reset_login", { admin: "adm_1", login: "owner", created: true }],
  ["session_end", {}],
  ["sessions_end_others", { count: 2 }],
  ["instance_update", {}],
  ["subscription_settings_update", {}],
  ["page_unlock_lockout", { user: "usr_1", scope: "token" }, "locked"],
  ["node.create_enrollment", { node_id: "nod_1", name: "de1" }],
  ["node.update", { node_id: "nod_1" }],
  ["node.restart_inbounds", { node_id: "nod_1" }],
  ["node.retire", { node_id: "nod_1", name: "de1" }],
  ["node.awg_prepare", { node_id: "nod_1", node: "de1" }],
  ["user_create", { user: "usr_1", name: "Марина" }],
  ["user_update", { user: "usr_1", name: "Марина", fields: "quota" }],
  ["user_delete", { count: 1, names: "Марина" }],
  ["user_delete", { count: 3, names: "Марина, Олег, Лена" }],
  ["users_disable", { count: 1, names: "Марина" }],
  ["users_disable", { count: 2, names: "Марина, Олег" }],
  ["users_enable", { count: 1, names: "Марина" }],
  ["users_enable", { count: 2, names: "Марина, Олег" }],
  ["users_extend", { count: 1, names: "Марина", days: 30 }],
  ["users_extend", { count: 2, names: "Марина, Олег", days: 30 }],
  ["group_create", { group: "grp_1", name: "друзья" }],
  ["group_update", { group: "grp_1", name: "друзья" }],
  ["group_delete", { group: "grp_1", name: "друзья" }],
  ["profile_create", { profile: "prf_1", name: "hy2 · 443", protocol: "hysteria2" }],
  ["profile_update", { profile: "prf_1", name: "hy2 · 443", settings_changed: true, reissue: false }],
  ["profile_delete", { profile: "prf_1", name: "hy2 · 443", protocol: "hysteria2" }],
  ["inbound_add", { inbound: "inb_1", profile: "hy2 · 443", node: "de1", port: 443 }],
  ["inbound_update", { inbound: "inb_1", profile: "hy2 · 443", node: "de1", port: 8443, enabled: true }],
  ["inbound_update", { inbound: "inb_1", profile: "hy2 · 443", node: "de1", port: 443, enabled: false }],
  ["inbound_remove", { inbound: "inb_1", profile: "hy2 · 443", node: "de1" }],
  ["preset_create", { preset: "dns_1", name: "Дом" }],
  ["preset_update", { preset: "dns_1", name: "Дом" }],
  ["preset_delete", { preset: "dns_1", name: "Дом" }],
  ["preset_default", { preset: "dns_1", name: "Дом" }],
  ["preset_default", { preset: "", name: "" }],
  ["device_create", { user: "usr_1", device: "dev_1", profile: "prf_1" }],
  ["device_configs", {}],
  ["device_rotate", {}],
  ["device_revoke", {}],
  ["user_reset_traffic", { user: "usr_1", was_bytes: 5 }],
  ["health.mute_alert", { alert_id: "alr_1" }],
  ["health.fix_plan", { node_id: "nod_1" }],
  ["health.apply_fix", { node_id: "nod_1" }],
  ["update_rollout_start", { rollout_id: "rol_1", version: "v1.2", nodes: "3", batch_size: "1" }],
  ["update_rollout_pause", {}],
  ["update_rollout_resume", {}],
  ["update_rollout_cancel", {}],
  ["update_rollback_node", { node_id: "nod_1", node: "de1" }],
  ["update_rescan", { status: "ok" }],
  ["warp_params", {}],
  ["warp_register", { node: "de1" }],
  ["warp_import", { node: "de1" }],
  ["warp_enable", { node_id: "nod_1" }],
  ["warp_disable", { node_id: "nod_1" }],
  ["warp_refresh", { node_id: "nod_1" }],
  ["warp_delete", { node: "de1" }],
  ["warp_reregister", { node: "de1" }],
  ["token_create", { token_id: "tok_1", name: "ci", profile: "operator", ttl_days: 30 }],
  ["token_revoke", { token_id: "tok_1" }],
  ["approval_approve", { plan_id: "pln_1", tool: "node_fix", token_id: "tok_1" }],
  ["approval_reject", { plan_id: "pln_1", tool: "node_fix", token_id: "tok_1" }],
  ["mcp_plan", { plan_id: "pln_1", tool: "user_enable", needs_approval: false }],
  ["mcp_apply", { plan_id: "pln_1", tool: "user_enable", result: "applied" }],
  ["mcp_apply", { plan_id: "pln_1", tool: "user_enable", result: "failed: x" }, "fail"],
  ["call", { procedure: "/mistgate.admin.v1.UserService/ListUsers", status: 200 }],
];

const describe2 = (t: never, action: string, params: Record<string, unknown>, result = "ok") => describeAudit(t, { action, paramsJson: JSON.stringify(params), result });

describe("audit dictionary", () => {
  it("words every action the panel writes, in both languages, without a hole left by a missing param or an English result", () => {
    for (const [action, params, result] of written) {
      for (const [lang, t] of [["en", tEn], ["ru", tRu]] as const) {
        const s = describe2(t, action, params, result);
        expect(s, `${lang} ${action}`).not.toBe(action);
        expect(s, `${lang} ${action}`).not.toMatch(/[{}]|\(\s*\)|:\s*$|«»|“”|\s{2}/); // an empty {param} leaves "( )", empty quotes or a dangling colon
        if (lang === "ru") expect(s, `ru ${action} ${result ?? ""}`).not.toMatch(/\b(fail|locked|rejected|denied|limited|error)\b/);
      }
    }
  });
  it("tells a sign-in by its method and a failed one by what was tried, never as a passkey sign-in", () => {
    expect(describe2(tRu, "login", { method: "passkey" })).toBe("вошёл(а) по passkey");
    expect(describe2(tRu, "login", { method: "password" })).toBe("вошёл(а) по паролю");
    expect(describe2(tRu, "login", { method: "password", login: "admin" }, "fail")).toBe("неудачная попытка входа (пароль, логин «admin»)");
    expect(describe2(tRu, "login", { method: "password", login: "?" }, "fail")).toBe("неудачная попытка входа (пароль, такого логина нет)");
    expect(describe2(tRu, "login", {}, "fail")).toBe("неудачная попытка входа по passkey");
    expect(describe2(tRu, "lockout", { login: "admin", until: 1 }, "locked")).toBe("вход под логином «admin» заблокирован на 15 мин после неудачных попыток");
    expect(describe2(tRu, "lockout", { source: "203.0.113.0", until: 1 }, "locked")).toBe("вход с этого адреса заблокирован на 15 мин после неудачных попыток");
    expect(describe2(tRu, "page_unlock_lockout", { user: "usr_1" }, "locked")).toBe("страница подписки заблокирована после неверных паролей");
    expect(describe2(tRu, "node.awg_prepare", { node: "de1" })).toBe("запустил(а) сборку модуля AmneziaWG на de1");
    expect(describe2(tRu, "stepup", { method: "totp" })).toBe("повторно подтвердил(а) вход");
  });
  it("names the people and the things a change touched", () => {
    expect(describe2(tRu, "user_delete", { count: 1, names: "Марина" })).toBe("удалил(а) пользователя Марина");
    expect(describe2(tRu, "users_disable", { count: 2, names: "Марина, Олег" })).toBe("отключил(а) пользователей (2): Марина, Олег");
    expect(describe2(tRu, "inbound_add", { profile: "hy2 · 443", node: "de1", port: 443 })).toBe("поставил(а) профиль hy2 · 443 на ноду de1, порт 443");
    expect(describe2(tRu, "inbound_update", { profile: "hy2", node: "de1", port: 443, enabled: false })).toBe("выключил(а) профиль hy2 на ноде de1");
    expect(describe2(tRu, "preset_default", { preset: "", name: "" })).toBe("вернул(а) встроенный основной DNS-пресет");
    expect(describe2(tEn, "reset_login", { login: "owner", created: true })).toBe("gave an admin the password login “owner” on the panel server");
  });
  it("says what failed in the UI language, after the sentence, when the sentence itself does not", () => {
    expect(describe2(tRu, "mcp_apply", { tool: "user_enable" }, "fail")).toBe("выполнил(а) запланированное изменение: Включить пользователей — не удалось");
    expect(describe2(tRu, "security_update", { turnstile_enabled: true }, "rejected")).toBe("не включил(а) проверку Cloudflare: ключи не прошли проверку");
    expect(describe2(tRu, "brand_new", {}, "denied")).toBe("brand_new — отказано");
    expect(resultText(tRu, "locked")).toBe("заблокировано");
    expect(resultText(tRu, "weird")).toBe("weird");
  });
  it("puts the params and the tool's own title into the sentence", () => {
    expect(describeAudit(tEn, { action: "token_create", paramsJson: JSON.stringify({ name: "ci", profile: "operator" }) })).toBe("made the API token ci (operator)");
    expect(describeAudit(tEn, { action: "approval_approve", paramsJson: JSON.stringify({ tool: "user_disable" }) })).toBe("approved the change: Disable users");
    expect(describeAudit(tRu, { action: "mcp_apply", paramsJson: JSON.stringify({ tool: "user_enable" }) })).toBe("выполнил(а) запланированное изменение: Включить пользователей");
    expect(describeAudit(tEn, { action: "call", paramsJson: JSON.stringify({ procedure: "/mistgate.admin.v1.UserService/ListUsers" }) })).toBe("called UserService/ListUsers");
  });
  it("says when a plan waits for the owner, and shows an unknown tool or action as it came", () => {
    const plan = (needs: boolean, tool = "node_fix") => describeAudit(tEn, { action: "mcp_plan", paramsJson: JSON.stringify({ tool, needs_approval: needs }) });
    expect(plan(true)).toBe("planned a change that needs the owner: Fix a node");
    expect(plan(false)).toBe("planned a change: Fix a node");
    expect(plan(false, "brand_new_tool")).toBe("planned a change: brand_new_tool");
    expect(describeAudit(tEn, { action: "brand_new", paramsJson: "{}" })).toBe("brand_new");
  });
});

describe("audit actors", () => {
  const who = (t: never, actorId: string, actorName = "", action = "x", result = "ok") => actorLabel(t, { actorId, actorName, action, result });
  it("shows the token's name when the server gives it, the id when it does not", () => {
    expect(who(tEn, "token:tok_ab", "CI script")).toBe("API token CI script");
    expect(who(tEn, "mcp:tok_ab", "claude")).toBe("MCP token claude");
    expect(who(tEn, "mcp:tok_ab")).toBe("MCP token tok_ab");
    expect(who(tRu, "token:tok_ab", "CI")).toBe("API-токен CI");
  });
  it("keeps admins, anonymous, the command line and the panel itself readable", () => {
    expect(who(tEn, "adm_1", "Alice")).toBe("Alice");
    expect(who(tEn, "adm_1")).toBe("adm_1");
    expect(who(tEn, "anonymous")).toBe("anonymous");
    expect(who(tRu, "cli")).toBe("командная строка");
    expect(who(tEn, "system")).toBe("the panel itself");
    expect(who(tRu, "user:usr_1")).toBe("пользователь на своей странице");
  });
  it("does not pass a failed sign-in against an admin's login off as that admin, and a lockout is the panel's", () => {
    expect(who(tRu, "adm_1", "Эннан", "login", "fail")).toBe("аноним");
    expect(who(tRu, "adm_1", "Эннан", "login", "ok")).toBe("Эннан");
    expect(who(tRu, "adm_1", "Эннан", "lockout", "locked")).toBe("сама панель");
  });
});
