import { beforeAll, describe, expect, it } from "vitest";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { fill, pickForm, setLang } from "@/i18n";
import { ru } from "@/i18n/ru";
import { makeFmt } from "@/lib/format";
import type { Alert } from "@/lib/health";
import { todoItems } from "./overview-todo";

// "Needs you": the order of the rows, which node gets a row of its own, and what the button of a row is.

beforeAll(() => setLang("ru"));

const t = Object.assign((key: keyof typeof ru, vars?: Record<string, string | number>) => fill(ru[key], vars), {
  n: (key: keyof typeof ru, count: number, vars?: Record<string, string | number>) => fill(pickForm(ru[key], count, "ru"), { n: count, ...vars }),
});
const fmt = makeFmt("ru", t as never);

let seq = 0;
const alert = (over: Partial<Alert>): Alert =>
  ({
    id: `alt_${++seq}`, severity: AlertSeverity.WARNING, kind: AlertKind.DOCTOR_WARN, nodeId: "nod_de1", nodeName: "de1", subject: "journald_size", titleKey: "health.alert.doctor_warn.title",
    params: { check: "journald_size" }, whyKey: "", firstSeenUnix: 1, lastSeenUnix: 2, resolvedAtUnix: 0, resolution: "", mutedUntilUnix: 0, actions: ["apply_fix:journald_vacuum", "open_node"], openedUnix: 1, ...over,
  }) as Alert;
const card = (name: string, status: NodeStatus, code?: string) =>
  ({ id: `nod_${name}`, name, countryCode: "DE", location: "", provider: "", status, reason: code ? { code, params: {} } : undefined, online: [], downBps: 0, upBps: 0, hasMetrics: false, cpuPct: 0, sparkBytes: [] }) as never;
const run = (over: Partial<Parameters<typeof todoItems>[2]>) => todoItems(t as never, fmt, { alerts: [], cards: [], approvals: [], canFix: true, ...over });

describe("the rows of 'Needs you'", () => {
  it("put a fault first, then the warnings, then what an agent asks", () => {
    const rows = run({
      alerts: [alert({}), alert({ kind: AlertKind.NODE_DOWN, severity: AlertSeverity.CRITICAL, nodeId: "nod_nl1", nodeName: "nl1", subject: "", titleKey: "health.alert.node_down.title", actions: ["open_node"] })],
      approvals: [{ id: "pln_1", tool: "node_fix" }],
    });
    expect(rows.map((r) => r.tone)).toEqual(["bad", "warn", "ai"]);
    expect(rows[0]).toMatchObject({ node: "nl1", title: "Нода недоступна", action: { kind: "node", label: "Открыть ноду" } });
    expect(rows[2]).toMatchObject({ id: "p-pln_1", title: "Агент ждёт твоего одобрения", action: { kind: "approvals", label: "Решить" } });
  });

  it("offers the fix itself to the owner, under the key the Health screen uses, and the node to anyone else", () => {
    const owner = run({ alerts: [alert({})] })[0]!;
    expect(owner.title).toBe("Размер журнала systemd");
    expect(owner.action).toMatchObject({ kind: "fix", target: { fixId: "journald_vacuum", nodeId: "nod_de1", key: "nod_de1/journald_size/" } });
    expect(run({ alerts: [alert({})], canFix: false })[0]!.action).toMatchObject({ kind: "node", id: "nod_de1", label: "Открыть ноду" });
  });

  it("sends a warning about a profile or a certificate to the profiles of its node, and a fleet-wide one to Health", () => {
    const profiles = run({ alerts: [alert({ kind: AlertKind.CERT_EXPIRY, titleKey: "health.alert.cert_expiry.title", actions: ["open_profiles", "mute"] })] })[0]!;
    expect(profiles.action).toMatchObject({ kind: "node", tab: "profiles", label: "Открыть профили ноды" });
    const wide = run({ alerts: [alert({ nodeId: "", nodeName: "", kind: AlertKind.CHECK_FAILED, titleKey: "health.alert.check_failed.title", params: { profile: "hy2 · 443" }, actions: ["mute"] })] })[0]!;
    expect(wide).toMatchObject({ node: "", action: { kind: "health" } });
  });

  it("makes one row of the profiles of one node that fail the same way", () => {
    const failing = (inbound: string) => alert({ kind: AlertKind.CHECK_FAILED, subject: inbound, titleKey: "health.alert.check_failed.title", params: { profile: inbound, inbound_id: inbound }, actions: ["restart_inbound", "open_node"] });
    const rows = run({ alerts: [failing("inb_1"), failing("inb_2")] });
    expect(rows).toHaveLength(1);
    expect(rows[0]!.action).toMatchObject({ kind: "node", label: "Открыть ноду" }); // two profiles: no single fix to start
  });

  it("gives a node that is a problem without an alert its own row, and none to a node the alerts already name", () => {
    const rows = run({
      cards: [card("de1", NodeStatus.ONLINE), card("fi1", NodeStatus.ONLINE, "no_profiles"), card("nl1", NodeStatus.DOWN, "agent_silent"), card("de2", NodeStatus.ONLINE, "clock_skew")],
      alerts: [alert({ nodeId: "nod_nl1", nodeName: "nl1", kind: AlertKind.NODE_DOWN, severity: AlertSeverity.CRITICAL, titleKey: "health.alert.node_down.title", actions: ["open_node"] })],
    });
    expect(rows).toHaveLength(2);
    expect(rows[0]!.id.startsWith("a-")).toBe(true);
    expect(rows[1]!.id).toBe("n-nod_fi1");
    expect(rows[1]).toMatchObject({ tone: "warn", node: "fi1", action: { kind: "node", id: "nod_fi1", tab: "profiles", label: "Добавить профиль на fi1" } });
  });

  it("has nothing for a calm fleet", () => {
    expect(run({ cards: [card("de1", NodeStatus.ONLINE), card("nl1", NodeStatus.PENDING, "enrollment_pending")] })).toEqual([]);
  });
});
