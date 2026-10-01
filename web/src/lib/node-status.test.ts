import { describe, expect, it } from "vitest";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { isProblem, nodeKind, reasonText, sortByProblems, statusLine, statusWord } from "./node-status";
import { fill } from "@/i18n";

const t = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(en[key], vars), { n: () => "" });
const n = (status: NodeStatus, code?: string, params: Record<string, string> = {}) => ({ status, reason: code ? { code, params } : undefined });

describe("nodeKind", () => {
  it("maps statuses to the display kinds", () => {
    expect(nodeKind(n(NodeStatus.ONLINE))).toBe("ok");
    expect(nodeKind(n(NodeStatus.DOWN))).toBe("bad");
    expect(nodeKind(n(NodeStatus.NO_TRAFFIC))).toBe("bad");
    expect(nodeKind(n(NodeStatus.BLIP))).toBe("blip"); // grey-blue, never red
    expect(nodeKind(n(NodeStatus.UPDATING))).toBe("busy");
    expect(nodeKind(n(NodeStatus.PENDING))).toBe("off");
  });
  it("turns a reason on an online node into attention or broken", () => {
    expect(nodeKind(n(NodeStatus.ONLINE, "state_drift"))).toBe("warn");
    expect(nodeKind(n(NodeStatus.ONLINE, "clock_skew", { seconds: "9" }))).toBe("warn");
    expect(nodeKind(n(NodeStatus.ONLINE, "inbound_failed", { error: "x" }))).toBe("bad");
  });
});

describe("isProblem", () => {
  it("counts broken nodes but not blips, pending nodes or a slightly drifting clock", () => {
    expect(isProblem(n(NodeStatus.DOWN))).toBe(true);
    expect(isProblem(n(NodeStatus.ONLINE, "inbound_failed"))).toBe(true);
    expect(isProblem(n(NodeStatus.ONLINE, "state_drift"))).toBe(true);
    expect(isProblem(n(NodeStatus.ONLINE, "clock_skew"))).toBe(false);
    expect(isProblem(n(NodeStatus.BLIP))).toBe(false);
    expect(isProblem(n(NodeStatus.PENDING))).toBe(false);
    expect(isProblem(n(NodeStatus.ONLINE))).toBe(false);
  });
});

describe("sortByProblems", () => {
  it("puts broken first, then blips, then the rest, keeping the server's order inside a group", () => {
    const list = [
      { id: "a", ...n(NodeStatus.ONLINE) },
      { id: "b", ...n(NodeStatus.BLIP) },
      { id: "c", ...n(NodeStatus.DOWN) },
      { id: "d", ...n(NodeStatus.ONLINE) },
      { id: "e", ...n(NodeStatus.ONLINE, "state_drift") },
    ];
    expect(sortByProblems(list).map((x) => x.id)).toEqual(["c", "e", "b", "a", "d"]);
  });
});

describe("texts", () => {
  it("fills the reason's placeholders and shows an unknown code as it is", () => {
    expect(reasonText(t as never, { code: "agent_silent", params: { minutes: "12" } })).toBe("agent silent for 12 min");
    expect(reasonText(t as never, { code: "something_new", params: {} })).toBe("something_new");
    expect(reasonText(t as never, undefined)).toBe("");
  });
  it("uses the reason as the line under a name, else the status word", () => {
    expect(statusLine(t as never, n(NodeStatus.DOWN, "agent_silent", { minutes: "3" }))).toBe("agent silent for 3 min");
    expect(statusLine(t as never, n(NodeStatus.ONLINE))).toBe("Healthy");
    expect(statusWord(t as never, n(NodeStatus.ONLINE, "state_drift"))).toBe("Needs attention");
  });
  it("says long silences in days and hours, not thousands of minutes", () => {
    expect(reasonText(t as never, { code: "agent_silent", params: { minutes: "2880" } })).toBe("agent silent for 2 d");
    expect(reasonText(t as never, { code: "agent_silent", params: { minutes: "3065" } })).toBe("agent silent for 2 d 3 h");
    expect(reasonText(t as never, { code: "agent_silent", params: { minutes: "125" } })).toBe("agent silent for 2 h 5 min");
  });
});

// The fleet tells the truth: a node nobody gets is not "healthy", one failed profile of three is not the whole node, and
// a node that waits for its install is not "up".
describe("truthful statuses", () => {
  it("an online node without profiles needs attention and counts as a problem", () => {
    const node = n(NodeStatus.ONLINE, "no_profiles");
    expect(nodeKind(node)).toBe("warn");
    expect(isProblem(node)).toBe(true);
    expect(statusWord(t as never, node)).toBe("Needs attention");
    expect(statusLine(t as never, node)).toBe("no profiles: users do not get this node");
  });
  it("one failed profile of several is 'partly working' (still red), all of them is 'broken'", () => {
    const partly = n(NodeStatus.ONLINE, "inbound_failed", { profile: "hy2 · WARP · 8443", error: "bind: address already in use", failed: "1", total: "3" });
    expect(nodeKind(partly)).toBe("bad");
    expect(statusWord(t as never, partly)).toBe("Partly working");
    expect(statusLine(t as never, partly)).toBe("“hy2 · WARP · 8443” failed to start: bind: address already in use");
    expect(statusWord(t as never, n(NodeStatus.ONLINE, "inbound_failed", { profile: "p", failed: "2", total: "2" }))).toBe("Broken");
    // a reason without the counts (an older panel) stays "broken" and keeps its old words
    expect(statusWord(t as never, n(NodeStatus.ONLINE, "inbound_failed", { error: "x" }))).toBe("Broken");
    expect(reasonText(t as never, { code: "inbound_failed", params: { error: "x" } })).toBe("a profile failed to start: x");
  });
  it("a pending node says it waits for the install and how long the command works, or that it expired", () => {
    const pending = n(NodeStatus.PENDING, "enrollment_pending", { expires_in_minutes: "57", expires_unix: "1790000000" });
    expect(nodeKind(pending)).toBe("off");
    expect(isProblem(pending)).toBe(false);
    expect(statusLine(t as never, pending)).toBe("Waiting for install · 57 min left");
    expect(statusLine(t as never, n(NodeStatus.PENDING, "enrollment_expired"))).toBe("Waiting for install · the command has expired");
    expect(statusLine(t as never, n(NodeStatus.PENDING))).toBe("Waiting for install");
  });
  it("in Russian too", () => {
    const tr = Object.assign((key: keyof typeof ru, vars?: Record<string, string | number>) => fill(ru[key], vars), { n: () => "" });
    // short enough for a tile: the part that matters (how long) is never cut off
    expect(statusLine(tr as never, n(NodeStatus.PENDING, "enrollment_pending", { expires_in_minutes: "57" }))).toBe("Ждёт установки · ещё 57 мин");
    expect(statusLine(tr as never, n(NodeStatus.PENDING, "enrollment_expired"))).toBe("Ждёт установки · команда истекла");
    expect(statusLine(tr as never, n(NodeStatus.ONLINE, "no_profiles"))).toBe("нет профилей — пользователи её не получат");
    expect(statusWord(tr as never, n(NodeStatus.ONLINE, "inbound_failed", { profile: "hy2", failed: "1", total: "2" }))).toBe("Работает частично");
    expect(reasonText(tr as never, { code: "agent_silent", params: { minutes: "3065" } })).toBe("агент молчит 2 дн 3 ч");
    expect(reasonText(tr as never, { code: "clock_skew", params: { seconds: "4" } })).toBe("часы сервера расходятся с панелью на 4 с");
  });
});
