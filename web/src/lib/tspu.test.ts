import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { fill, pickForm, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { zh } from "@/i18n/zh";
import { makeFmt } from "./format";
import { tspuPortsText } from "./health";
import { probeNodeOptions, runLook, tspuErrorText, tspuState, type TspuNode, type TspuRun, type TspuSettings, type TspuTest } from "./tspu";

// with plurals, as useT() makes it
const tOf = (dict: Record<string, string>, lang: string) =>
  Object.assign((key: string, vars?: Record<string, string | number>) => fill(dict[key]!, vars), {
    n: (key: string, count: number, vars?: Record<string, string | number>) => fill(pickForm(dict[key]!, count, lang), { n: count, ...vars }),
  }) as unknown as T;
const t = tOf(en, "en");
const tRu = tOf(ru, "ru");
const tZh = tOf(zh, "zh");
const fmt = makeFmt("en", t);
const fmtRu = makeFmt("ru", tRu);
const fmtZh = makeFmt("zh", tZh);

const ago12 = Math.floor(Date.now() / 1000) - 12 * 60;
const run = (over: Partial<TspuRun> = {}): TspuRun => ({ status: "ok", atUnix: ago12, nodeId: "nod_probe1", dpiHop: 6, blockedRef: "31.13.72.36", nodesChecked: 3, detail: "", ...over });
const node = (over: Partial<TspuNode> = {}): TspuNode => ({ id: "nod_probe1", name: "probe1", countryCode: "RU", inCountry: true, declares: true, online: true, ...over });
const s = (over: Partial<TspuSettings> = {}): TspuSettings => ({
  enabled: true,
  available: true,
  unavailableReason: "",
  probeNodeId: "nod_probe1",
  intervalMin: 120,
  nodes: [node()],
  ...over,
});
const tested: TspuTest = { run: run({ atUnix: ago12 + 300 }), nodes: [{ nodeId: "nod_nl1", name: "nl1", verdict: "blocked", ports: [{ port: 443, verdict: "blocked" }] }] };

describe("the state of the TSPU probe card", () => {
  it("is not there on the Cloudflare edition, on or off; off is off for any country; another country than Russia says so", () => {
    expect(tspuState(s({ enabled: false, available: false, unavailableReason: "edition" }), null, false)).toEqual({ kind: "unavailable", reason: "edition" });
    expect(tspuState(s({ enabled: false, available: false, unavailableReason: "country" }), null, false)).toEqual({ kind: "off" });
    expect(tspuState(s({ available: false, unavailableReason: "country", lastRun: run() }), { at: ago12 + 600, ok: tested }, true)).toEqual({ kind: "unavailable", reason: "country" });
  });

  it("spins while a test runs, and asks for a probe node before anything else", () => {
    expect(tspuState(s({ lastRun: run() }), null, true)).toEqual({ kind: "running" });
    expect(tspuState(s({ probeNodeId: "" }), null, false)).toEqual({ kind: "noNode" });
    expect(tspuState(s({ probeNodeId: "", lastRun: run() }), null, false)).toEqual({ kind: "noNode" });
  });

  it("has not run yet, then shows the stored run, then the newer of the last test and the stored run", () => {
    expect(tspuState(s(), null, false)).toEqual({ kind: "untested" });
    expect(tspuState(s({ lastRun: run() }), null, false)).toEqual({ kind: "run", run: run() });
    // a test stores its run: the same time, and the card keeps the verdict per node
    expect(tspuState(s({ lastRun: tested.run }), { at: tested.run!.atUnix, ok: tested }, false)).toEqual({ kind: "run", run: tested.run, nodes: tested.nodes });
    // a scheduled run after the test wins, and has no verdicts per node to show
    const later = run({ atUnix: ago12 + 900, status: "busy" });
    expect(tspuState(s({ lastRun: later }), { at: tested.run!.atUnix, ok: tested }, false)).toEqual({ kind: "run", run: later });
  });

  it("fails on a test that failed, until a run stored after the test began: an answer lost on the way is not the last word", () => {
    const error = new ConnectError("tspu_probe_node_unfit", Code.FailedPrecondition);
    expect(tspuState(s({ lastRun: run() }), { at: ago12 + 60, error }, false)).toEqual({ kind: "fails", error });
    const stored = run({ atUnix: ago12 + 200 });
    expect(tspuState(s({ lastRun: stored }), { at: ago12 + 60, error }, false)).toEqual({ kind: "run", run: stored });
  });
});

describe("a run in words", () => {
  it("says the time, the hop of the box, the reference that placed it and how many nodes it checked", () => {
    expect(runLook(t, fmt, run())).toEqual({ kind: "ok", word: "Works", detail: "last run 12 min ago · TSPU after hop 6 · reference 31.13.72.36 · 3 nodes checked" });
    expect(runLook(tRu, fmtRu, run())).toEqual({ kind: "ok", word: "Работает", detail: "последний запуск 12 мин назад · ТСПУ за 6-м хопом · опорный адрес 31.13.72.36 · проверены 3 ноды" });
    expect(runLook(tZh, fmtZh, run()).detail).toBe("上次运行 12 分钟前 · TSPU 位于第 6 跳之后 · 参照地址 31.13.72.36 · 已检测 3 个节点");
    expect(runLook(tRu, fmtRu, run({ nodesChecked: 1 })).detail).toContain("проверена 1 нода");
  });

  it("names the limits of the method: the control cut too, the box not found, a busy probe node, a failure with its reason", () => {
    expect(runLook(t, fmt, run({ status: "control_failed", dpiHop: 0, nodesChecked: 0 }))).toEqual({
      kind: "warn",
      word: "The method does not work on this network",
      detail: "even the control address is cut behind the TSPU, so there are no verdicts · last run 12 min ago",
    });
    expect(runLook(tRu, fmtRu, run({ status: "control_failed" })).word).toBe("Метод не работает в этой сети");
    expect(runLook(tRu, fmtRu, run({ status: "no_dpi_hop" })).word).toBe("Не удалось найти ТСПУ на маршруте");
    expect(runLook(t, fmt, run({ status: "no_dpi_hop" })).detail).toBe(
      "no reference address placed it: they pass on this path, or the probe node’s kernel does not report router answers · last run 12 min ago",
    );
    expect(runLook(tZh, fmtZh, run({ status: "no_dpi_hop" })).word).toBe("未能在路径上找到 TSPU");
    expect(runLook(t, fmt, run({ status: "busy" }))).toEqual({ kind: "blip", word: "The probe node was busy", detail: "the panel asks again 5 min later · last run 12 min ago" });
    expect(runLook(t, fmt, run({ status: "failed", detail: "agent_too_old" }))).toEqual({ kind: "bad", word: "Does not work", detail: "the probe node’s agent is too old: update it · last run 12 min ago" });
    expect(runLook(tRu, fmtRu, run({ status: "failed", detail: "unreachable" })).detail).toBe("нода-зонд не ответила · последний запуск 12 мин назад");
    expect(runLook(tZh, fmtZh, run({ status: "failed", detail: "agent_unsupported" })).detail).toBe("探测节点的系统无法运行此探测 · 上次运行 12 分钟前");
    // a reason this page does not know is shown as sent
    expect(runLook(t, fmt, run({ status: "failed", detail: "brand_new" })).detail).toBe("brand_new · last run 12 min ago");
  });

  it("puts the ports of a verdict together: blocked first, then open, refused, timed out", () => {
    const ports = [
      { port: 8443, verdict: "open" },
      { port: 443, verdict: "blocked" },
      { port: 2053, verdict: "blocked" },
      { port: 9443, verdict: "refused" },
      { port: 80, verdict: "timeout" },
    ];
    expect(tspuPortsText(t, ports)).toBe("TCP 443, 2053: blocked · 8443: open · 9443: refused · 80: timed out");
    expect(tspuPortsText(tRu, ports)).toBe("TCP 443, 2053: режут · 8443: проходит · 9443: отказ · 80: не успели");
    expect(tspuPortsText(tZh, ports)).toBe("TCP 443, 2053：被封锁 · 8443：可通 · 9443：被拒绝 · 80：超时");
    expect(tspuPortsText(t, [])).toBe("");
  });
});

describe("the probe-node picker", () => {
  const nodes = [
    node(),
    node({ id: "nod_ru2", name: "ru2", declares: false }),
    node({ id: "nod_ru3", name: "ru3", online: false }),
    node({ id: "nod_de1", name: "de1", countryCode: "DE", inCountry: false }),
  ];

  it("keeps the server's order, in-country first, and greys out an old agent and an offline node, saying why", () => {
    expect(probeNodeOptions(t, fmt, nodes, "nod_probe1")).toEqual([
      { value: "nod_probe1", label: "probe1", hint: "Russia", disabled: false },
      { value: "nod_ru2", label: "ru2", hint: "update the agent · Russia", disabled: true },
      { value: "nod_ru3", label: "ru3", hint: "offline · Russia", disabled: true },
      { value: "nod_de1", label: "de1", hint: "Germany", disabled: false },
    ]);
    expect(probeNodeOptions(tRu, fmtRu, nodes, "nod_probe1")[1]!.hint).toBe("обнови агент · Россия");
    expect(probeNodeOptions(tZh, fmtZh, nodes, "nod_probe1")[2]!.hint).toBe("离线 · 俄罗斯");
  });

  it("starts with “not chosen” while there is none, and keeps a chosen node the server no longer lists, greyed", () => {
    expect(probeNodeOptions(t, fmt, nodes, "")[0]).toEqual({ value: "", label: "not chosen" });
    expect(probeNodeOptions(t, fmt, nodes, "nod_gone")[0]).toEqual({ value: "nod_gone", label: "nod_gone", disabled: true });
  });
});

describe("the errors of the card", () => {
  it("says how long to wait after a second test within the minute", () => {
    expect(tspuErrorText(new ConnectError("tested a moment ago: try again in 42 s", Code.ResourceExhausted), t)).toBe("One test a minute. Try again in 42 s.");
    expect(tspuErrorText(new ConnectError("too soon", Code.ResourceExhausted), tRu)).toBe("Не чаще раза в минуту. Попробуй через 60 с.");
    expect(tspuErrorText(new ConnectError("tested a moment ago: try again in 7 s", Code.ResourceExhausted), tZh)).toBe("每分钟最多测试一次。请 7 秒后再试。");
  });

  it("words the server's codes in three languages", () => {
    const codes: [string, Code, string, string, string][] = [
      ["tspu_probe_node_unfit", Code.FailedPrecondition, "The probe node is offline or its agent is too old.", "Нода-зонд не в сети, или её агент слишком старый.", "探测节点离线，或其 Agent 太旧。"],
      ["tspu_no_probe_node", Code.FailedPrecondition, "Pick a probe node first.", "Сначала выбери ноду-зонд.", "请先选择探测节点。"],
      ["probe_node_not_capable", Code.InvalidArgument, "That node’s agent cannot run the probe. Update the agent first.", "Агент этой ноды не умеет запускать зонд. Сначала обнови агент.", "该节点的 Agent 无法运行探测。请先更新 Agent。"],
      ["interval_min: 30..1440", Code.InvalidArgument, "The interval is 30 minutes to 24 hours.", "Интервал от 30 минут до 24 часов.", "间隔须在 30 分钟到 24 小时之间。"],
    ];
    for (const [code, c, e, r, z] of codes) {
      const err = new ConnectError(code, c);
      expect([tspuErrorText(err, t), tspuErrorText(err, tRu), tspuErrorText(err, tZh)]).toEqual([e, r, z]);
    }
  });
});
