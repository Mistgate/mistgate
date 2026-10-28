import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { ApprovalState, TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";
import { dictionaries, fill, langs, pickForm, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { makeFmt } from "./format";
import {
  approvalStateInfo,
  callErrorText,
  clockText,
  dangerText,
  factLabel,
  factText,
  factWords,
  outcomeText,
  parseRate,
  profileInfo,
  secondsLeft,
  snippet,
  splitTokens,
  toolTitle,
  tokenState,
  validName,
  type Token,
} from "./integrations";
import { relayFacts } from "@/test/relay-plan";

const t = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(en[key], vars), {
  n: (key: keyof typeof en, n: number, vars?: Record<string, string | number>) => fill(pickForm(en[key], n, "en"), { n, ...vars }),
}) as T;

const token = (over: Partial<Token> = {}): Token => ({
  id: "tok_1",
  name: "claude-ops",
  profile: TokenProfile.OPERATOR,
  createdUnix: 100,
  expiresUnix: 5000,
  lastUsedUnix: 0,
  lastUsedIp: "",
  lastUsedVia: 0,
  revokedUnix: 0,
  createdByName: "Owner",
  rateLimitPerMin: 120,
  hint: "ab12",
  ...over,
});

describe("token state", () => {
  it("is revoked before anything else, then expired by the server's clock, else active", () => {
    expect(tokenState(token(), 1000)).toBe("active");
    expect(tokenState(token({ expiresUnix: 1000 }), 1000)).toBe("expired");
    expect(tokenState(token({ expiresUnix: 999 }), 1000)).toBe("expired");
    expect(tokenState(token({ revokedUnix: 50, expiresUnix: 1 }), 1000)).toBe("revoked");
  });
  it("keeps live tokens in the server's order and sets the rest apart", () => {
    const list = [token({ id: "a" }), token({ id: "b", revokedUnix: 1 }), token({ id: "c" }), token({ id: "d", expiresUnix: 10 })];
    const { live, old } = splitTokens(list, 1000);
    expect(live.map((x) => x.id)).toEqual(["a", "c"]);
    expect(old.map((x) => x.id)).toEqual(["b", "d"]);
  });
});

describe("the create form's rules", () => {
  it("takes a name of 1..64 characters after trimming", () => {
    expect(validName("")).toBe(false);
    expect(validName("   ")).toBe(false);
    expect(validName(" grafana ")).toBe(true);
    expect(validName("x".repeat(64))).toBe(true);
    expect(validName("x".repeat(65))).toBe(false);
  });
  it("takes a whole rate of 1..600 and nothing else", () => {
    expect(parseRate("120")).toBe(120);
    expect(parseRate(" 600 ")).toBe(600);
    expect(parseRate("1")).toBe(1);
    for (const bad of ["0", "601", "", "-5", "1.5", "12a", "1e2", "00000"]) expect(parseRate(bad), bad).toBeNull();
  });
  it("describes every profile the panel offers, in both languages", () => {
    for (const p of [TokenProfile.READONLY, TokenProfile.OPERATOR, TokenProfile.ADMIN]) {
      const info = profileInfo(p);
      expect(en[info.key]).toBeTruthy();
      for (const lang of langs) expect(dictionaries[lang][info.desc], lang).toBeTruthy();
    }
  });
});

describe("MCP snippets", () => {
  const mcp = "https://panel.example.com/secret/mcp";
  const admin = "https://panel.example.com/secret/";
  it("never carry a real secret: the token is a placeholder or a file", () => {
    for (const c of ["code", "desktop", "other", "stdio"] as const) {
      const s = snippet(c, mcp, admin);
      expect(s, c).not.toMatch(/tk1_/);
      expect(s, c).toMatch(/<token>|<path-to-token-file>/);
    }
  });
  it("point Claude Code and a generic client at the endpoint, Claude Desktop and stdio at the proxy", () => {
    expect(snippet("code", mcp, admin)).toBe(`claude mcp add --transport http mistgate ${mcp} --header "Authorization: Bearer <token>"`);
    expect(JSON.parse(snippet("other", mcp, admin))).toEqual({ mcpServers: { mistgate: { type: "http", url: mcp, headers: { Authorization: "Bearer <token>" } } } });
    expect(JSON.parse(snippet("desktop", mcp, admin))).toEqual({ mcpServers: { mistgate: { command: "mistgate", args: ["mcp", "--url", admin, "--token-file", "<path-to-token-file>"] } } });
    expect(snippet("stdio", mcp, admin)).toBe(`mistgate mcp --url ${admin} --token-file <path-to-token-file>`);
  });
});

describe("the countdown", () => {
  it("counts from the server's clock plus the time since the answer, and stops at zero", () => {
    const data = { nowUnix: 1000, receivedMs: 50_000 };
    expect(secondsLeft({ expiresUnix: 1600 }, data, 50_000)).toBe(600);
    expect(secondsLeft({ expiresUnix: 1600 }, data, 110_000)).toBe(540);
    expect(secondsLeft({ expiresUnix: 1600 }, data, 50_500)).toBe(600); // half a second in: rounds up, never early
    expect(secondsLeft({ expiresUnix: 1600 }, data, 9_999_999)).toBe(0);
    expect(secondsLeft({ expiresUnix: 1600 }, data, 1_000)).toBe(600); // an idle clock from before the answer: no time has passed
  });
  it("writes minutes and two-digit seconds", () => {
    expect(clockText(600)).toBe("10:00");
    expect(clockText(541)).toBe("9:01");
    expect(clockText(59)).toBe("0:59");
    expect(clockText(0)).toBe("0:00");
  });
});

describe("words of an approval", () => {
  it("titles every tool that can plan a change, in both languages", () => {
    const tools = [
      "user_create", "user_update", "user_disable", "user_enable", "user_reset_traffic", "device_revoke", "alert_mute", "node_fix", "rollout_start", "rollout_pause", "rollout_resume",
      "rollout_cancel", "node_update_schedule", "node_update_schedule_cancel", "update_timezone", "node_rollback", "node_install", "node_server_password_rotate",
    ];
    for (const tool of tools) {
      expect(toolTitle(t, tool), tool).not.toBe(tool);
      for (const lang of langs) expect(dictionaries[lang][`approval.tool.${tool}` as keyof typeof ru], `${lang} ${tool}`).toBeTruthy();
    }
    expect(toolTitle(t, "something_new")).toBe("something_new");
  });
  it("labels every fact key the panel writes, and shows an unknown key as it came", () => {
    const keys = [
      "name", "subscription_name", "quota", "term", "apps", "nodes", "count", "users", "effect", "user", "device", "last_seen", "alert", "duration", "node", "fix", "detail",
      "drops_sessions", "version", "rollout", "progress", "action", "current_version", "previous_version", "group", "quota_reset", "expires", "device_limit", "speed_limit", "device_speed_limit",
      "dns_preset", "used", "scheduled_at", "scheduled_version", "timezone", "existing_schedule", "from", "to",
      "ssh", "username", "host_key", "host_key_algorithm", "address", "installation", "recovery",
    ];
    for (const k of keys) {
      expect(factLabel(t, k), k).not.toBe(k);
      for (const lang of langs) expect(dictionaries[lang][`approval.fact.${k}` as keyof typeof ru], `${lang} ${k}`).toBeTruthy();
    }
    expect(factLabel(t, "brand_new")).toBe("brand_new");
    // rollout_start updates one node now: no batches, no canary, so no words for them
    expect(factLabel(t, "batch_size")).toBe("batch_size");
    expect(Object.keys(en).filter((k) => /^approval\.(fact\.batch_size|value\.canary|value\.default)$/.test(k))).toEqual([]);
  });
  // internal/panel/mcp/tools_profiles.go: everything profile_create, profile_update and profile_deploy send by key
  it("has words for every key of the profile tools, in every language", () => {
    const facts = [
      "profile_name", "profile_summary", "format", "node", "port", "warnings", "group", "users", "name", "inbounds_restarted", "users_online", "devices_need_reissue",
      "critical_field", "user_count", "add_node", "remove_node", "add_group", "remove_group", "users_losing_server",
    ];
    const keys = [
      ...["profile_create", "profile_update", "profile_deploy"].map((k) => `approval.tool.${k}`),
      ...facts.map((k) => `approval.fact.${k}`),
      "approval.danger.fleet",
      ...["profile_created", "profile_updated", "profile_deployed"].map((k) => `approval.outcome.${k}`),
      ...["profile_deploy_failed", "changed_since_plan", "plan_unreadable"].map((k) => `approval.fail.${k}`),
    ];
    for (const k of keys) for (const lang of langs) expect(dictionaries[lang][k as keyof typeof ru], `${lang} ${k}`).toBeTruthy();
    expect(factLabel(t, "setting:/obfs/type")).toBe("Setting /obfs/type");
  });
  it("knows the three danger codes", () => {
    for (const c of ["step_up", "fleet", "bulk"]) expect(dangerText(t, c)).not.toBe(c);
    expect(dangerText(t, "x")).toBe("x");
  });
  it("words the panel's own values, and never touches text from data", () => {
    expect(factText(t, { key: "drops_sessions", value: "true", untrusted: false })).toBe("yes");
    expect(factText(t, { key: "progress", value: "2 of 5 nodes finished", untrusted: false })).toBe("2 of 5 nodes finished");
    expect(factText(t, { key: "action", value: "pause", untrusted: false })).toBe("Pause");
    expect(factText(t, { key: "effect", value: "the node restarts its agent", untrusted: false })).toBe("The node restarts its agent.");
    expect(factText(t, { key: "version", value: "0.2.0-bbb", untrusted: false })).toBe("0.2.0-bbb");
    // a node or user named like a panel sentence stays exactly as written
    expect(factText(t, { key: "node", value: "pause", untrusted: true })).toBe("pause");
    expect(factText(t, { key: "users", value: "they can connect again", untrusted: true })).toBe("they can connect again");
  });
  it("gives every state a look", () => {
    for (const s of [ApprovalState.AWAITING, ApprovalState.APPROVED, ApprovalState.REJECTED, ApprovalState.EXPIRED, ApprovalState.APPLYING, ApprovalState.APPLIED, ApprovalState.FAILED, ApprovalState.CANCELLED]) {
      expect(en[approvalStateInfo(s).key]).toBeTruthy();
    }
    expect(approvalStateInfo(ApprovalState.FAILED).kind).toBe("bad");
    expect(approvalStateInfo(ApprovalState.AWAITING).kind).toBe("warn");
  });
});

describe("coded facts and outcomes, in Russian", () => {
  const tr = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(ru[key], vars), {
    n: (key: keyof typeof en, n: number, vars?: Record<string, string | number>) => fill(pickForm(ru[key], n, "ru"), { n, ...vars }),
  }) as T;
  const fmt = makeFmt("ru", tr);
  const fact = (key: string, code: string, params: Record<string, string> = {}, untrustedParams: string[] = []) => ({ key, value: "the panel's English", untrusted: false, code, params, untrustedParams });
  const said = (f: ReturnType<typeof fact>) => factWords(tr, fmt, f).map((p) => (typeof p === "string" ? p : `[${p.data}]`)).join("");

  it("reads the plan's facts as the owner's sentences", () => {
    expect(said(fact("effect", "traffic_reset"))).toBe("Израсходованный трафик обнулится, остановленные квотой снова смогут подключаться.");
    expect(said(fact("duration", "seconds", { n: "3600" }))).toBe("1 ч");
    expect(said(fact("quota", "quota", { bytes: "100000000000", reset: "month" }))).toBe("100 ГБ, сброс 1-го числа");
    expect(said(fact("quota", "quota", { bytes: "0", reset: "month" }))).toBe("без лимита");
    expect(said(fact("term", "never"))).toBe("без срока");
    expect(said(fact("term", "days", { n: "30" }))).toBe("30 дней");
    expect(said(fact("nodes", "some", { n: "3" }))).toBe("3 ноды");
    expect(said(fact("nodes", "all"))).toBe("все ноды");
    expect(said(fact("apps", "happ"))).toBe("только ссылка подписки");
    expect(said(fact("quota", "change", { from: "0", to: "50000000000" }))).toBe("без лимита → 50 ГБ");
    expect(said(fact("quota_reset", "change", { from: "month", to: "rolling_month" }))).toBe("сброс 1-го числа → сброс каждые 30 дней");
    expect(said(fact("action", "pause"))).toBe("Пауза");
    expect(said(fact("effect", "restart"))).toBe("Нода перезапустит свой агент.");
    expect(said(fact("existing_schedule", "replace"))).toBe("Заменит текущее расписание ноды.");
    expect(said({ ...fact("timezone", "utc_offset", { minutes: "180" }), value: "UTC+03:00" })).toBe("UTC+03:00");
    expect(said(fact("scheduled_at", "time", { unix: "1700009000" }))).toBe(fmt.dateTime(1700009000));
    expect(said(fact("alert", "alert", { kind: "node_down", severity: "critical" }))).toBe("Нода недоступна · Критично");
    expect(said(fact("alert", "alert", { kind: "check_failed", severity: "warning" }))).toBe("Профиль не проходит проверку глазами клиента · Внимание");
    expect(said(fact("progress", "progress", { done: "2", total: "5" }))).toBe("завершено 2 из 5 нод");
  });

  it("keeps a name from data on its own plate inside the sentence, even when the server forgot to say it is data", () => {
    const fix = fact("fix", "restart_inbound", { profile: "hy2 · 443", port: "443" }, ["profile"]);
    expect(factWords(tr, fmt, fix)).toEqual(["Перезапустить профиль ", { data: "hy2 · 443" }, " (порт 443)"]);
    expect(factWords(tr, fmt, { ...fix, untrustedParams: [] })).toEqual(["Перезапустить профиль ", { data: "hy2 · 443" }, " (порт 443)"]);
    expect(said(fact("fix", "restart_inbound"))).toBe("Перезапустить профиль"); // no profile named: the doctor's label
    expect(factWords(tr, fmt, { key: "node", value: "pause", untrusted: true, code: "", params: {}, untrustedParams: [] })).toEqual([{ data: "pause" }]);
    expect(said(fact("effect", "brand_new_code"))).toBe("the panel's English"); // an unknown code reads as the panel wrote it
  });

  it("reads a profile plan's formats, warnings and secrets, and what came of it", () => {
    const plain = (key: string, value: string) => said({ key, value, untrusted: false, code: "", params: {}, untrustedParams: [] });
    expect(plain("format", "mihomo_yaml: full")).toBe("Mihomo: да");
    expect(plain("format", "singbox_json: xhttp")).toBe("sing-box: нет (XHTTP)");
    expect(plain("format", "base64_uris: unavailable")).toBe("Список: нет");
    expect(plain("format", "new_format: full")).toBe("new_format: да");
    expect(plain("warnings", "none")).toBe("нет");
    expect(plain("warnings", "port_lossy, brand_new")).toBe("порт теряет UDP-пакеты; brand_new");
    expect(plain("setting:/password", "masked secret -> generated secret")).toBe("Секрет сгенерируется заново.");
    expect(factLabel(tr, "setting:/port")).toBe("Настройка /port");
    // the profile tools mark the whole change as data: our arrow, a plate for each side, never the panel's "->"
    const change = { key: "setting:/port", value: "443 -> 8443", untrusted: true, code: "change", params: { from: "443", to: "8443" }, untrustedParams: ["from"] };
    expect(factWords(tr, fmt, change)).toEqual([{ data: "443" }, " → ", { data: "8443" }]);
    const out = (state: ApprovalState, outcomeCode: string, outcomeParams: Record<string, string> = {}) => outcomeText(tr, fmt, { state, outcomeCode, outcomeParams });
    expect(out(ApprovalState.APPLIED, "profile_deployed", { added_nodes: "1", removed_nodes: "1" })).toBe("Ноды и группы профиля изменены");
    expect(out(ApprovalState.FAILED, "profile_deploy_failed", { step: "remove_node", completed_steps: "2" })).toBe("Ошибка: шаг не прошёл, предыдущие шаги остались применены");
  });

  it("words what came of a decision, and leaves an unknown outcome to the panel's own line", () => {
    const out = (state: ApprovalState, outcomeCode: string, outcomeParams: Record<string, string> = {}) => outcomeText(tr, fmt, { state, outcomeCode, outcomeParams });
    expect(out(ApprovalState.APPLIED, "users_disabled", { n: "3" })).toBe("Отключено 3 пользователя");
    expect(out(ApprovalState.APPLIED, "users_disabled", { n: "1" })).toBe("Отключён 1 пользователь");
    expect(out(ApprovalState.APPLIED, "alert_muted", { seconds: "3600" })).toBe("Алерт заглушён на 1 ч");
    expect(out(ApprovalState.APPLIED, "rollback_started")).toBe("Откат запущен");
    expect(out(ApprovalState.APPLIED, "node_update_scheduled", { node_id: "nod_1", version: "v2" })).toBe("Обновление ноды запланировано");
    expect(out(ApprovalState.APPLIED, "node_update_schedule_cancelled", { node_id: "nod_1" })).toBe("Запланированное обновление отменено");
    expect(out(ApprovalState.APPLIED, "update_timezone_changed", { offset_minutes: "180" })).toBe("Часовой пояс расписания изменён");
    expect(out(ApprovalState.APPLIED, "node_install_queued", { job_id: "job_1" })).toBe("Установка поставлена в очередь");
    expect(out(ApprovalState.APPLIED, "node_ssh_password_rotated")).toBe("SSH-пароль сменён");
    expect(out(ApprovalState.FAILED, "no_trusted_bundle", { detail: "x" })).toBe("Ошибка: нет проверенного пакета обновлений");
    expect(out(ApprovalState.FAILED, "something_else")).toBeNull();
    expect(out(ApprovalState.APPLIED, "")).toBeNull();
    expect(ru["approval.fact.duration"]).toBe("На сколько");
    expect(ru["approval.fact.progress"]).toBe("Прогресс");
    expect(ru["approval.state.failed"]).toBe("Ошибка");
  });
});

describe("failed calls", () => {
  it("replaces the panel's English with a sentence of ours for the codes that need one", () => {
    expect(callErrorText(new ConnectError("too many tokens", Code.ResourceExhausted), t)).toBe(en["int.err.limit"]);
    expect(callErrorText(new ConnectError("exists", Code.AlreadyExists), t)).toBe(en["int.err.nameTaken"]);
    expect(callErrorText(new ConnectError("no such approval", Code.NotFound), t)).toBe(en["int.err.gone"]);
    expect(callErrorText(new ConnectError("this approval is not waiting any more", Code.FailedPrecondition), t)).toBe(en["int.err.notWaiting"]);
    expect(callErrorText(new ConnectError("nope", Code.PermissionDenied), t)).toBe(en["err.permissionDenied"]);
  });
});

describe("relay_plan on the approval card, in Russian", () => {
  const tr = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(ru[key], vars), {
    n: (key: keyof typeof en, n: number, vars?: Record<string, string | number>) => fill(pickForm(ru[key], n, "ru"), { n, ...vars }),
  }) as T;
  const fmt = makeFmt("ru", tr);
  const said = (f: { key: string; value: string; untrusted: boolean }) => factWords(tr, fmt, { code: "", params: {}, untrustedParams: [], ...f }).map((p) => (typeof p === "string" ? p : `[${p.data}]`)).join("");

  it("names the tool and every fact the plan writes", () => {
    expect(toolTitle(tr, "relay")).toBe("Изменить ретрансляцию");
    for (const f of [...relayFacts.create, ...relayFacts.move, { key: "action" }]) expect(factLabel(tr, f.key), f.key).not.toBe(f.key);
  });

  it("words the panel's sentences and verdicts; names from data stay on plates, the networks too", () => {
    const [entry, , server, port, exitSeen, entrySeen, reach, warning, checks, effect] = relayFacts.create;
    expect(said(entry!)).toBe("[msk1]");
    expect(said(server!)).toBe("[VLESS · XHTTP (vless, tcp 2053)]");
    expect(said(port!)).toBe("выберет панель: 2053, как на выходе, если он свободен на входе, иначе свободный");
    expect(said(exitSeen!)).toBe("частично заблокирован; не виден из [MTS, Tele2]");
    expect(said(entrySeen!)).toBe("доступен");
    expect(said(reach!)).toBe("да (проверка ТСПУ)");
    expect(said({ key: "entry_reaches_exit", value: "reach (refs_pass)", untrusted: false })).toBe("на пути входа ТСПУ не видно");
    expect(said({ key: "entry_reaches_exit", value: "no_data", untrusted: false })).toBe("нет данных проверки");
    expect(said(warning!)).toBe(ru["approval.value.relay.hop"]);
    expect(said(checks!)).toBe(ru["approval.value.relay.checks"]);
    expect(said(effect!)).toBe(`${ru["approval.value.relay.entryNote"]} ${ru["approval.value.relay.onNote"]}`);
  });

  it("words a change of a relay: the port moved, switched on, the last check, the effect; an action when the plan names one", () => {
    const [port, on, last, effect] = relayFacts.move;
    expect(said(port!)).toBe("8443 → 9443");
    expect(said(on!)).toBe(ru["int.ap.yes"]);
    expect(said(last!)).toBe(`${fmt.dateTime(Date.parse("2026-10-28T20:05Z") / 1000)}: от входа до выхода — проходит; снаружи через вход — не проходит (timeout)`);
    expect(said({ key: "last_check", value: "never checked", untrusted: false })).toBe("ещё не проверялась");
    expect(said(effect!)).toBe(`${ru["approval.value.relay.portNote"]} ${ru["approval.value.relay.portOnNote"]}`);
    expect(said({ key: "action", value: "create", untrusted: false })).toBe("Создать и включить, если проверки пройдут");
    expect(said({ key: "action", value: "delete", untrusted: false })).toBe("Удалить");
  });

  it("reads anything it does not know as the panel wrote it, and never words a value from data", () => {
    expect(said({ key: "effect", value: "Something new the panel says.", untrusted: false })).toBe("Something new the panel says.");
    const longer = "the server has a hop range; the relay passes its one port only and more";
    expect(said({ key: "warning", value: longer, untrusted: false })).toBe(longer);
    expect(said({ key: "exit_seen_from_inside", value: "weird text", untrusted: true })).toBe("[weird text]");
    expect(said({ key: "entry_port", value: "8443 -> 9443", untrusted: true })).toBe("[8443 -> 9443]");
    expect(outcomeText(tr, fmt, { state: ApprovalState.APPLIED, outcomeCode: "relay_created", outcomeParams: {} })).toBe("Ретрансляция создана и включена");
    expect(outcomeText(tr, fmt, { state: ApprovalState.FAILED, outcomeCode: "relay_check_failed", outcomeParams: { stage: "leg", code: "timeout" } })).toContain("проверка ретрансляции не прошла");
  });
});
