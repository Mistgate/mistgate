import { describe, expect, it } from "vitest";
import { OsRolloutStatus, OsStepState } from "@/gen/mistgate/admin/v1/update_pb";
import { dictionaries, fill, langs, pickForm, type Lang, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { blockKeys, blockOf, blockText, elapsedText, jobErrorText, osAlertReason, osAlertWhyKey, osRunOf, pauseCode, pauseText, phaseText, reasonLine, refreshText, resultLine, stepPill, stepReason, stepSince } from "./os-run";
import { osPollMs, type OsRun, type OsStepRow } from "./os-updates";

// The run of an OS update on one node: which text a state gets, and what the buttons may do.

const tOf = (lang: Lang): T => {
  const dict = dictionaries[lang];
  return Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(dict[key], vars), {
    n: (key: keyof typeof en, count: number, vars?: Record<string, string | number>) => fill(pickForm(dict[key], count, lang), { n: count, ...vars }),
  }) as unknown as T;
};
const t = tOf("en");
const tRu = tOf("ru");

const NOW = 1_800_000_000;
const step = (over: Partial<OsStepRow> = {}): OsStepRow => ({
  nodeId: "nod_de1", nodeName: "de1", state: OsStepState.SENT, jobId: "osj_1", phase: "", phaseUnix: 0, kernelFrom: "", kernelTo: "", sentUnix: NOW - 200, rebootUnix: 0, backUnix: 0, finishedUnix: 0,
  errorKey: "", params: {}, ...over,
});
const run = (steps: OsStepRow[], over: Partial<OsRun> = {}): OsRun => ({
  id: "osr_1", status: OsRolloutStatus.RUNNING, mode: 1, reboot: 2, createdUnix: NOW - 300, finishedUnix: 0, pauseKey: "", pauseParams: {}, steps, ...over,
});

describe("what to show for a node's run", () => {
  it("shows a step that is waiting or under way while the run goes on", () => {
    for (const state of [OsStepState.PENDING, OsStepState.SENT, OsStepState.REBOOTING, OsStepState.GATING]) {
      const s = step({ state });
      expect(osRunOf({ step: s, rollout: run([s]) }, NOW).kind, String(state)).toBe("running");
    }
  });
  it("shows nothing without a step, or for a step of a run that ended with it still open", () => {
    expect(osRunOf({ step: undefined, rollout: undefined }, NOW).kind).toBe("idle");
    const s = step({ state: OsStepState.SENT });
    expect(osRunOf({ step: s, rollout: run([s], { status: OsRolloutStatus.CANCELLED }) }, NOW).kind).toBe("idle");
  });
  it("keeps the outcome for a day, and a stopped run until it is closed", () => {
    const passed = step({ state: OsStepState.PASSED, finishedUnix: NOW - 600 });
    const done = run([passed], { status: OsRolloutStatus.DONE, finishedUnix: NOW - 600 });
    expect(osRunOf({ step: passed, rollout: done }, NOW)).toMatchObject({ kind: "result", stopped: false });
    expect(osRunOf({ step: passed, rollout: done }, NOW + 86_400).kind).toBe("idle");
    const failed = step({ state: OsStepState.FAILED, errorKey: "not_back", finishedUnix: NOW - 3 * 86_400 });
    expect(osRunOf({ step: failed, rollout: run([failed], { status: OsRolloutStatus.PAUSED }) }, NOW)).toMatchObject({ kind: "result", stopped: true });
    // closed three days ago: gone
    expect(osRunOf({ step: failed, rollout: run([failed], { status: OsRolloutStatus.CANCELLED, finishedUnix: NOW - 3 * 86_400 }) }, NOW).kind).toBe("idle");
  });
  it("counts a phase from when it began", () => {
    expect(stepSince(step({ phaseUnix: NOW - 180 }), run([]))).toBe(NOW - 180);
    expect(stepSince(step({ state: OsStepState.REBOOTING, rebootUnix: NOW - 240 }), run([]))).toBe(NOW - 240);
    expect(stepSince(step({ state: OsStepState.GATING, backUnix: NOW - 30 }), run([]))).toBe(NOW - 30);
    expect(elapsedText(t, NOW - 20, NOW)).toBe("under a minute");
    expect(elapsedText(t, NOW - 180, NOW)).toBe("3 min");
  });
  it("asks every few seconds while a run goes on, rarely when nothing does", () => {
    const d = (r?: OsRun) => ({ rollout: r }) as never;
    expect(osPollMs(d(), NOW * 1000)).toBe(60_000);
    expect(osPollMs(d(run([])), NOW * 1000)).toBe(5_000);
    expect(osPollMs(d(run([], { status: OsRolloutStatus.PAUSED })), NOW * 1000)).toBe(10_000);
    expect(osPollMs(d(run([], { status: OsRolloutStatus.DONE, finishedUnix: NOW - 30 })), NOW * 1000)).toBe(8_000);
    expect(osPollMs(d(run([], { status: OsRolloutStatus.DONE, finishedUnix: NOW - 3600 })), NOW * 1000)).toBe(60_000);
  });
});

describe("the words of a run", () => {
  it("names the phase the node is in, and the state when it has not said one", () => {
    expect(phaseText(t, { state: OsStepState.SENT, phase: "installing" })).toBe("Installing updates…");
    expect(phaseText(t, { state: OsStepState.SENT, phase: "waiting_lock" })).toBe("Waiting for another package manager…");
    expect(phaseText(t, { state: OsStepState.SENT, phase: "" })).toBe(en["os.state.sent"]);
    expect(phaseText(t, { state: OsStepState.SENT, phase: "from_the_future" })).toBe(en["os.state.sent"]);
    expect(phaseText(t, { state: OsStepState.REBOOTING, phase: "rebooting" })).toContain("up to 15 minutes");
    expect(phaseText(tRu, { state: OsStepState.GATING, phase: "", rebootUnix: NOW - 60 })).toBe("Нода вернулась, проверка профилей…");
    expect(phaseText(t, { state: OsStepState.PENDING, phase: "" })).toBe(en["os.state.pending"]);
  });
  it("does not say a node came back when the run had no reboot", () => {
    expect(phaseText(t, { state: OsStepState.GATING, phase: "", rebootUnix: NOW - 60 })).toBe("Back, checking profiles…");
    expect(phaseText(t, { state: OsStepState.GATING, phase: "", rebootUnix: 0 })).toBe("Checking profiles…");
    expect(phaseText(t, { state: OsStepState.GATING, phase: "" })).toBe(en["os.state.gating.plain"]);
    expect(phaseText(tRu, { state: OsStepState.GATING, phase: "", rebootUnix: 0 })).toBe("Проверка профилей…");
  });
  it("sums up a finished step: the kernel, the packages, the reboot", () => {
    const s = step({ state: OsStepState.PASSED, kernelFrom: "6.8.0-147-generic", kernelTo: "6.8.0-150-generic", params: { upgraded: "6", reboot: "done" } });
    expect(resultLine(t, s)).toBe("Done · kernel 6.8.0-147-generic → 6.8.0-150-generic · 6 packages installed · rebooted");
    expect(resultLine(t, s, false)).toBe("kernel 6.8.0-147-generic → 6.8.0-150-generic · 6 packages installed · rebooted");
    expect(resultLine(t, step({ state: OsStepState.PASSED, params: { upgraded: "1", reboot: "not_required", kernel: "6.8.0-150-generic" } }))).toBe("Done · kernel 6.8.0-150-generic · 1 package installed · no reboot needed");
    expect(resultLine(tRu, step({ state: OsStepState.PASSED, params: { upgraded: "6", reboot: "not_required" } }))).toBe("Готово · 6 пакетов · перезагрузка не потребовалась");
    expect(resultLine(t, step({ state: OsStepState.PASSED, params: { reboot: "not_asked" } }))).toBe("Done · not rebooted, as you chose");
  });
  it("says why a step did not go through, with the node's own reason for a refusal or a failed job", () => {
    expect(stepReason(t, "offline")).toEqual({ title: en["os.step.err.offline"], detail: "" });
    expect(stepReason(t, "os.step.err.not_back").title).toContain("did not come back within 15 minutes");
    const refused = stepReason(t, "agent_refused", { code: "disk_full" });
    expect(refused.title).toBe(en["os.step.err.agent_refused"]);
    expect(refused.detail).toBe(en["os.job.err.disk_full"]);
    expect(stepReason(t, "reboot_held", { code: "boot_default_custom" }).detail).toContain("GRUB_DEFAULT=0");
    expect(stepReason(t, "job_failed", { code: "install_failed", package: "nginx" }).detail).toContain("failed on nginx");
    expect(stepReason(t, "job_failed", { code: "install_failed" }).detail).toBe(en["os.job.err.install_failed"]);
    // a code the panel does not know reads as "unknown", never as the raw code or a hole
    expect(stepReason(t, "job_failed", { code: "from_the_future" }).detail).toBe(en["os.job.err.unknown"]);
    expect(stepReason(t, "brand_new_failure").title).toBe("The update did not finish (brand_new_failure).");
    expect(stepReason(t, "")).toEqual({ title: "", detail: "" });
    expect(reasonLine(t, "agent_refused", { code: "apt_lock" })).toBe(`${en["os.step.err.agent_refused"]} ${en["os.job.err.apt_lock"]}`);
    expect(resultLine(t, step({ state: OsStepState.FAILED, errorKey: "probe_failed" }))).toBe(en["os.step.err.probe_failed"]);
    expect(resultLine(t, step({ state: OsStepState.SKIPPED, errorKey: "up_to_date" }))).toBe(en["os.step.err.up_to_date"]);
  });
  it("tells a refused reboot of a reboot-only run from one that followed an install", () => {
    const only = { code: "too_soon", mode: "reboot_only" };
    expect(stepReason(t, "reboot_held", only)).toEqual({ title: "The server was not rebooted: the node refused.", detail: en["os.job.err.too_soon"] });
    expect(stepReason(tRu, "reboot_held", only).title).toBe("Сервер не перезагружен: нода отказалась.");
    // the install run, and a step from before the mode was sent, keep the words about installed updates
    expect(stepReason(t, "reboot_held", { code: "too_soon", mode: "security" }).title).toBe(en["os.step.err.reboot_held"]);
    expect(stepReason(t, "reboot_held", { code: "too_soon" }).title).toBe(en["os.step.err.reboot_held"]);
    for (const lang of langs) expect(stepReason(tOf(lang), "reboot_held", only).title, lang).not.toMatch(/установлен|installed|已安装|نصب شد|kuruldu/);
    expect(pauseText(t, "reboot_held", "fi1", only)).toBe("fi1: the server was not rebooted, the node refused.");
    expect(pauseText(tRu, "reboot_held", "fi1", only)).toBe("fi1: сервер не перезагружен, нода отказалась.");
    expect(pauseText(t, "reboot_held", "fi1", { mode: "security" })).toBe("fi1: updates are installed, but the server was not rebooted.");
    expect(reasonLine(t, "reboot_held", only)).toBe(`${en["os.step.err.reboot_held.only"]} ${en["os.job.err.too_soon"]}`);
    expect(resultLine(t, step({ state: OsStepState.FAILED, errorKey: "reboot_held", params: only }), false)).not.toMatch(/installed/);
  });
  it("words a node that dropped off during the check as lost, not as one that came back", () => {
    expect(stepReason(t, "node_lost").title).toMatch(/^The server dropped off the network while the panel was checking it after the update\./);
    expect(stepReason(tRu, "node_lost").title).toMatch(/^Сервер пропал из сети, пока панель проверяла его после обновления\./);
    // the pause key stays gate_failed; the step's reason picks the words
    expect(pauseCode("gate_failed", { reason: "node_lost" })).toBe("node_lost");
    expect(pauseCode("os.pause.gate_failed", { reason: "probe_failed" })).toBe("gate_failed");
    expect(pauseText(t, "gate_failed", "de1", { reason: "node_lost" })).not.toContain("came back");
    expect(pauseText(t, "gate_failed", "de1", { reason: "node_lost" })).toContain("dropped off the network");
    expect(pauseText(t, "gate_failed", "de1", { reason: "inbound_failed" })).toBe("de1 came back but failed the check after the update.");
  });
  it("says in the alert which of the sentences fits, and does not repeat the step's headline", () => {
    const why = "health.alert.os_update_failed.why.";
    expect(osAlertWhyKey(`${why}reboot_held`, { reason: "reboot_held", mode: "reboot_only" })).toBe(`${why}reboot_held.only`);
    expect(osAlertWhyKey(`${why}reboot_held`, { reason: "reboot_held", mode: "security" })).toBe(`${why}reboot_held`);
    expect(osAlertWhyKey(`${why}gate_failed`, { reason: "node_lost" })).toBe(`${why}node_lost`);
    expect(osAlertWhyKey(`${why}not_back`, { reason: "not_back" })).toBe(`${why}not_back`);
    expect(osAlertReason(t, { reason: "reboot_held", code: "boot_default_custom", mode: "reboot_only" })).toBe(en["os.job.err.boot_default_custom"]);
    expect(osAlertReason(t, { reason: "job_failed", code: "install_failed", package: "nginx" })).toContain("The update failed on the node. Installing the updates failed on nginx.");
  });
  it("says that nothing changed on the server when the node refused to start", () => {
    for (const lang of langs) {
      const s = stepReason(tOf(lang), "agent_refused", { code: "busy" });
      expect(s.title, lang).not.toBe(en["os.step.err.agent_refused"].split(". ")[0]);
      expect(s.title.split(/(?<=[.。])\s*/).length, lang).toBeGreaterThan(1);
    }
    expect(stepReason(t, "agent_refused", { code: "busy" }).title).toBe("The node did not start the update. Nothing on the server was changed.");
    expect(stepReason(tRu, "agent_refused", { code: "busy" }).title).toBe("Нода не начала обновление. На сервере ничего не менялось.");
    // the result line of a refused step: the sentence, then the node's own reason
    const s = step({ state: OsStepState.FAILED, errorKey: "agent_refused", params: { code: "busy" } });
    expect(resultLine(tRu, s, false)).toBe(`Нода не начала обновление. На сервере ничего не менялось. ${dictionaries.ru["os.job.err.busy"]}`);
  });
  it("words the banner of a stopped run with the node's name", () => {
    expect(pauseText(t, "step_failed", "de1")).toBe("The update of de1 stopped.");
    expect(pauseText(t, "os.pause.gate_failed", "de1")).toBe("de1 came back but failed the check after the update.");
    expect(pauseText(tRu, "reboot_held", "fi1")).toBe("fi1: обновления установлены, но сервер не перезагрузили.");
    expect(pauseText(t, "something_new", "de1")).toBe("The update of de1 stopped.");
  });
  it("colours a step: done green, stopped red, waiting grey, running busy", () => {
    expect(stepPill({ state: OsStepState.PASSED }).kind).toBe("ok");
    expect(stepPill({ state: OsStepState.FAILED }).kind).toBe("bad");
    expect(stepPill({ state: OsStepState.SKIPPED }).kind).toBe("off");
    expect(stepPill({ state: OsStepState.GATING }).kind).toBe("busy");
  });
  it("answers a refresh in words, and a refusal with the node's reason", () => {
    expect(refreshText(t, "de1", { outcome: "started", code: "" })).toBe("de1 is checking for updates. The list is refreshed in a minute.");
    expect(refreshText(t, "de1", { outcome: "recent", code: "" })).toContain("refreshed a moment ago");
    expect(refreshText(t, "de1", { outcome: "limit", code: "" })).toContain("Too many");
    expect(refreshText(t, "de1", { outcome: "refused", code: "apt_lock" })).toContain("held the lock");
    expect(refreshText(t, "de1", { outcome: "refused", code: "" })).toBe("de1 did not accept the request.");
    expect(refreshText(t, "de1", { outcome: "new_outcome", code: "" })).toBe("de1 did not accept the request.");
  });
});

describe("what the buttons may do", () => {
  const row = (over: Record<string, unknown> = {}) => ({ updateBlock: "", rebootBlock: "", rollout: undefined, step: undefined, ...over }) as never;
  it("follows the server's word for the node, install and reboot apart", () => {
    expect(blockOf(row({ updateBlock: "nothing_to_do" }), "install")).toBe("nothing_to_do");
    expect(blockOf(row({ updateBlock: "nothing_to_do" }), "reboot")).toBe("");
    expect(blockKeys(row({ updateBlock: "offline", rebootBlock: "offline" }))).toEqual(["offline"]);
    expect(blockKeys(row({ updateBlock: "nothing_to_do" }))).toEqual(["nothing_to_do"]);
    expect(blockKeys(row())).toEqual([]);
  });
  it("closes every button while a run is open (running or stopped), even if the server said nothing", () => {
    // a run that finished with a node's refusal is not open: the next one may start at once
    expect(blockOf(row({ rollout: run([], { status: OsRolloutStatus.FAILED }) }), "install")).toBe("");
    for (const status of [OsRolloutStatus.RUNNING, OsRolloutStatus.PAUSED]) {
      const r = row({ rollout: run([], { status }) });
      expect(blockOf(r, "install")).toBe("rollout_active");
      expect(blockOf(r, "reboot")).toBe("rollout_active");
    }
    expect(blockOf(row({ rollout: run([], { status: OsRolloutStatus.DONE }) }), "install")).toBe("");
  });
  it("lets a node be checked for updates while another one is being updated, but not while it is busy or cannot", () => {
    expect(blockOf(row({ rollout: run([]), updateBlock: "rollout_active" }), "refresh")).toBe("");
    for (const k of ["offline", "agent_old", "unsupported", "panel_host", "node_busy"]) expect(blockOf(row({ updateBlock: k }), "refresh"), k).toBe(k);
    expect(blockOf(row({ step: step({ state: OsStepState.SENT }) }), "refresh")).toBe("rollout_active");
  });
});

describe("every sentence has its words in every language", () => {
  const blocks = ["offline", "agent_old", "unsupported", "panel_host", "node_busy", "no_status", "nothing_to_do", "rollout_active", "agent_rollout", "panel_busy", "awg_build"];
  const jobCodes = [
    "unsupported_os", "no_systemd", "bad_request", "busy", "disk_full", "apt_lock", "dpkg_broken", "security_source_failed", "refresh_failed", "install_failed", "auto_reboot_conflict", "dpkg_interrupted",
    "awg_module_missing", "boot_full", "initramfs_missing", "reboot_failed", "timeout", "interrupted", "cancelled", "launch_failed", "kernel_not_switched", "awg_module_not_loaded", "inbound_failed",
    "kernel_unknown", "kernel_older", "awg_module_build_failed", "too_soon", "boot_default_custom", "reboot_limit", "unknown",
  ];
  const stepKeys = [
    "offline", "unsupported", "panel_host", "busy", "up_to_date", "not_required", "cancelled", "agent_refused", "job_failed", "reboot_held", "no_answer", "not_back", "kernel_not_switched", "state_not_applied",
    "inbounds_unreadable", "warp_down", "inbound_failed", "probe_failed", "job_not_settled", "reboot_held.only", "node_lost",
  ];
  const keys = [
    ...blocks.map((k) => `os.block.${k}`),
    ...jobCodes.map((k) => `os.job.err.${k}`),
    ...stepKeys.map((k) => `os.step.err.${k}`),
    ...["preflight", "waiting_lock", "repairing", "refreshing", "installing", "awg_module", "checking", "rebooting"].map((k) => `os.phase.${k}`),
    ...["step_failed", "reboot_held", "reboot_held.only", "not_back", "gate_failed", "node_lost"].flatMap((k) => [`os.pause.${k}`, `health.alert.os_update_failed.why.${k}`]),
    "os.state.gating.plain",
    "os.dlg.reboot.lead.online",
    "os.menu.label",
    "os.stopped.hint",
    "os.job.err.install_failed.pkg",
    "health.alert.os_update_failed.title",
    "approval.tool.os_rollout_start",
    "approval.tool.os_rollout_cancel",
    "approval.outcome.os_rollout_started",
    "approval.outcome.os_rollout_cancelled",
    "err.os_blocked",
    "err.os_cancel_inflight",
    "err.node_updating",
  ];
  it("writes each one in all five, none left as the English", () => {
    for (const k of keys) {
      for (const lang of langs) expect(dictionaries[lang][k as keyof typeof en], `${lang} ${k}`).toBeTruthy();
      for (const lang of langs.filter((l) => l !== "en")) expect(dictionaries[lang][k as keyof typeof en], `${lang} ${k}`).not.toBe(en[k as keyof typeof en]);
    }
  });
  it("leaves no bare placeholder for a code or a reason that lacks its value", () => {
    for (const code of jobCodes) for (const lang of langs) expect(jobErrorText(tOf(lang), code), `${lang} ${code}`).not.toMatch(/[{}]/);
    for (const k of blocks) expect(blockText(t, k)).not.toBe(k);
    expect(blockText(t, "from_the_future")).toBe("from_the_future");
  });
  it("keeps the plurals to the forms the language has", () => {
    for (const k of ["os.done.upgraded", "os.dlg.security", "os.dlg.others", "os.dlg.online", "os.dlg.reboot.lead.online"] as const) {
      expect(en[k].split("|")).toHaveLength(2);
      expect(dictionaries.ru[k].split("|")).toHaveLength(3);
      for (const lang of ["zh", "fa", "tr"] as const) expect(dictionaries[lang][k], `${lang} ${k}`).not.toContain("|");
    }
  });
  it("keeps the Russian phases in one voice: nouns, never \"I do\" or \"we wait\"", () => {
    const phases = ["preflight", "waiting_lock", "repairing", "refreshing", "installing", "awg_module", "checking", "rebooting"].map((k) => dictionaries.ru[`os.phase.${k}` as keyof typeof en]);
    const states = ["pending", "sent", "rebooting", "gating", "gating.plain"].map((k) => dictionaries.ru[`os.state.${k}` as keyof typeof en]);
    const all = [...phases, ...states, dictionaries.ru["os.btn.refreshing"]];
    for (const p of all) expect(p, p).not.toMatch(/(^|\s)(Ставлю|Устанавливаю|Проверяю|Жду|Чиню|Собираю|Перезагружаю|Запускаю|проверяю|проверяем|ждём)(\s|…)/);
    expect(dictionaries.ru["os.phase.installing"]).toBe("Установка обновлений…");
    expect(dictionaries.ru["os.phase.waiting_lock"]).toBe("Ожидание другого менеджера пакетов…");
    expect(dictionaries.ru["os.btn.refreshing"]).toBe("Проверка…");
    // the pills of a step agree with the update, not the node
    expect(dictionaries.ru["os.pill.skipped"]).toBe("Пропущено");
    expect(dictionaries.ru["os.pill.failed"]).toBe("Остановлено");
  });
  it("says the Russian words the owner asked for", () => {
    expect(dictionaries.ru["err.os_blocked"]).toBe("Нельзя начать обновление ОС: {reason}");
    expect(dictionaries.ru["approval.fact.pending"]).toBe("Ожидают установки");
    expect(dictionaries.ru["approval.value.os_old_kernels"]).toBe("Старые ядра остаются установленными: если нода не загрузится, выбери прежнее ядро в консоли хостера.");
    for (const [k, v] of Object.entries(dictionaries.ru)) if (k.startsWith("os.") || k.startsWith("approval.value.os_")) expect(v, k).not.toMatch(/провайдер|выберите/i);
  });
  it("tells the AmneziaWG module warning the same way everywhere, without promising a build fixes it", () => {
    expect(en["os.dlg.warn.awgMissing"]).toBe("The AmneziaWG module is not built for the new kernel. The node refuses to reboot until it is: people on AmneziaWG would lose their connection.");
    expect(dictionaries.ru["os.dlg.warn.awgMissing"]).toBe("Модуль AmneziaWG не собран под новое ядро. Нода откажется перезагружаться, пока модуля нет: люди на AmneziaWG потеряли бы связь.");
    for (const lang of langs) for (const k of ["os.dlg.warn.awgMissing", "os.job.err.awg_module_missing", "approval.value.os_awg_module_missing"] as const) {
      expect(dictionaries[lang][k], `${lang} ${k}`).not.toMatch(/Сначала собери|Build the module first|Build the module for the new kernel|Собери модуль/);
    }
  });
  it("never offers to reboot anyway", () => {
    for (const lang of langs) for (const [k, v] of Object.entries(dictionaries[lang])) if (k.startsWith("os.")) expect(v, `${lang} ${k}`).not.toMatch(/anyway|всё равно перезагр|force/i);
  });
});
