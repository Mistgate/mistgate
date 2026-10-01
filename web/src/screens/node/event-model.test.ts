import { describe, expect, it } from "vitest";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { fill, pickForm } from "@/i18n";
import { en } from "@/i18n/en";
import { byDay, buildLines, foldWindowS, lineRows, matchesFilter, updateWindowS, type NodeEvent } from "./event-model";
import { lineText } from "./event-text";
import { kernelReadiness } from "./awg-kernel";

const t = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(en[key], vars), { n: () => "" });

const T0 = 1_790_000_000;
let n = 0;
const ev = (code: string, at: number, params: Record<string, string> = {}, over: Partial<NodeEvent> = {}): NodeEvent => ({
  id: ++n,
  timeUnix: at,
  severity: EventSeverity.INFO,
  code,
  params,
  nodeId: "nod_1",
  nodeName: "de1",
  userId: "",
  userName: "",
  inboundId: "",
  profileName: "",
  protocol: "",
  source: "agent",
  ...over,
});
const start = (at: number, profile: string, inbound: string, reason?: string) =>
  ev("engine_started", at, { protocol: "hysteria2", ...(reason ? { reason } : {}) }, { inboundId: inbound, profileName: profile });

describe("event lines", () => {
  it("folds the profile starts of an agent update into one line", () => {
    const rows = [
      ev("agent_started", T0, { version: "0.1.0-b", prev_version: "0.1.0-a", reason: "update" }),
      start(T0 + 3, "test", "inb_1", "agent_start"),
      start(T0 + 4, "Amnezia 3.1 test", "inb_2", "agent_start"),
      ev("state_applied", T0 + 5, { revision: "9", inbounds: "2", added: "0", removed: "0", changed: "0", users: "0" }),
    ];
    const lines = buildLines(rows);
    expect(lines).toHaveLength(1);
    const l = lines[0]!;
    expect(l.kind).toBe("agent_started");
    expect(l.profiles).toEqual(["test", "Amnezia 3.1 test"]);
    expect(l.started).toBe(2);
    expect(lineRows(l)).toHaveLength(4); // the cause and what followed stay readable as details
    expect(lineText(t as never, l)).toEqual({ title: "Agent updated 0.1.0-a → 0.1.0-b", sub: "profiles up: test, Amnezia 3.1 test" });
    expect(l.status).toBe("ok");
  });

  it("does not fold what happened long after the agent came up, nor a start that says it was something else", () => {
    const rows = [
      ev("agent_started", T0, { version: "1", reason: "restart" }),
      start(T0 + foldWindowS + 30, "late", "inb_1", "agent_start"),
      start(T0 + 5, "added", "inb_2", "profile_added"), // no profile_added row to join: it stands on its own
    ];
    const lines = buildLines(rows);
    expect(lines.map((l) => l.kind)).toEqual(["profiles_started", "profiles_started", "agent_started"]);
    expect(lines.at(-1)!.profiles).toEqual([]);
    expect(lines.at(-1)!.status).toBe("blip"); // a plain restart is a short interruption, grey-blue, not red
  });

  it("turns a wall of identical starts from before the agent_started event into one line per burst", () => {
    const rows = [start(T0, "test", "inb_1"), start(T0 + 1, "Amnezia 3.1 test", "inb_2"), start(T0 + 4000, "test", "inb_1"), start(T0 + 4001, "Amnezia 3.1 test", "inb_2")];
    const lines = buildLines(rows);
    expect(lines).toHaveLength(2);
    expect(lines.map((l) => l.profiles)).toEqual([["test", "Amnezia 3.1 test"], ["test", "Amnezia 3.1 test"]]);
    expect(lineText(t as never, lines[0]!).title).toBe("Profiles started: test, Amnezia 3.1 test");
    expect(lineRows(lines[0]!)).toHaveLength(2);
  });

  it("names a lone start by its profile and says why", () => {
    const [l] = buildLines([start(T0, "test", "inb_1", "recovered")]);
    expect(lineText(t as never, l!)).toEqual({ title: "Profile “test” started", sub: "recovered after a failure" });
  });

  it("joins the start that an admin's add and restart caused to that action", () => {
    const rows = [
      ev("profile_added", T0, { actor: "ops", profile: "Amnezia 3.1 test", protocol: "awg" }, { source: "admin", inboundId: "inb_2", profileName: "Amnezia 3.1 test" }),
      start(T0 + 2, "Amnezia 3.1 test", "inb_2", "profile_added"),
      ev("profiles_restarted", T0 + 600, { actor: "ops", count: "2" }, { source: "admin" }),
      ev("engine_restarted", T0 + 601, { reason: "restart_command" }, { inboundId: "inb_1", profileName: "test" }),
      ev("engine_restarted", T0 + 602, { reason: "restart_command" }, { inboundId: "inb_2", profileName: "Amnezia 3.1 test" }),
    ];
    const lines = buildLines(rows);
    expect(lines).toHaveLength(2);
    expect(lineText(t as never, lines[1]!)).toEqual({ title: "Profile “Amnezia 3.1 test” added to the node · by ops", sub: "started on the node" });
    expect(lineText(t as never, lines[0]!)).toEqual({ title: "Profiles restarted · by ops", sub: "restarted: test, Amnezia 3.1 test" });
  });

  it("keeps a failure visible: it is its own line, red, with its error as the detail", () => {
    const rows = [
      ev("agent_started", T0, { version: "1", reason: "restart" }),
      ev("engine_failed", T0 + 2, { error: "bind: address already in use" }, { severity: EventSeverity.ERROR, inboundId: "inb_1", profileName: "test" }),
    ];
    const lines = buildLines(rows);
    expect(lines).toHaveLength(2);
    expect(lines[0]!.status).toBe("bad");
    expect(lineText(t as never, lines[0]!)).toEqual({ title: "Profile “test” failed to start", sub: "bind: address already in use" });
  });

  // The automatic build of the kernel module: started / done / failed, with the failure's reason in plain words under it.
  it("words the events of the kernel module build, a failure with its reason", () => {
    const rows = [
      ev("awg_kernel_prepare_started", T0, { kernel: "6.8.0-142-generic" }),
      ev("awg_kernel_prepare_failed", T0 + 240, { code: "apt_lock", reason: "another package manager held the lock", kernel: "6.8.0-142-generic", minutes: "4" }, { severity: EventSeverity.ERROR }),
      ev("awg_kernel_prepare_done", T0 + 600, { kernel: "6.8.0-142-generic", minutes: "2" }),
      ev("awg_kernel_switched", T0 + 601, { backend: "kernel", minutes: "2" }, { source: "panel" }),
    ];
    const lines = buildLines(rows);
    const text = (code: string) => lineText(t as never, lines.find((l) => l.head.code === code)!);
    expect(text("awg_kernel_prepare_started")).toEqual({ title: "building the AmneziaWG kernel module for 6.8.0-142-generic", sub: "" });
    expect(text("awg_kernel_prepare_failed")).toEqual({
      title: "AmneziaWG kernel module was not built",
      sub: "another package manager (apt, unattended-upgrades) held the lock for too long",
    });
    expect(text("awg_kernel_prepare_done").title).toBe("AmneziaWG kernel module is ready (2 min)");
    expect(text("awg_kernel_switched").title).toBe("AmneziaWG switched to the kernel module");
    expect(lines.find((l) => l.head.code === "awg_kernel_prepare_failed")!.status).toBe("bad");
    // a code from the future reads as the node's own short fact
    const odd = buildLines([ev("awg_kernel_prepare_failed", T0, { code: "disk_full", reason: "no space left on /usr" }, { severity: EventSeverity.ERROR })]);
    expect(lineText(t as never, odd[0]!).sub).toBe("no space left on /usr");
  });

  it("filters before it folds", () => {
    const rows = [
      ev("agent_started", T0, { version: "1", reason: "restart" }),
      start(T0 + 1, "test", "inb_1", "agent_start"),
      ev("clock_skew", T0 + 2, { offset_s: "40" }, { severity: EventSeverity.WARNING }),
    ];
    expect(buildLines(rows, "problems").map((l) => l.head.code)).toEqual(["clock_skew"]);
    expect(buildLines(rows, "profiles").map((l) => l.kind)).toEqual(["profiles_started"]);
    expect(buildLines(rows, "agent").map((l) => l.head.code)).toEqual(["clock_skew", "agent_started"]);
    expect(buildLines(rows, "all")).toHaveLength(2);
    expect(matchesFilter(rows[1]!, "agent")).toBe(false);
  });

  it("splits lines into days, newest first", () => {
    const at = (d: number, h: number) => new Date(2026, 8, d, h, 30).getTime() / 1000;
    const lines = buildLines([ev("node_recovered", at(28, 9)), ev("node_blip", at(28, 23)), ev("node_down", at(27, 12))]);
    const days = byDay(lines);
    expect(days.map((d) => d.lines.length)).toEqual([2, 1]);
    expect(days[0]!.day).toBe("2026-09-28");
  });
});

// One thing that happened is one line: an update is not three rows ("updated", "updated to", "step done"), a rollback
// not two, and the numbers say something ("1 profile added · 14 devices", "for 3 min", "after 47 min").
describe("event lines say what happened, once", () => {
  const tn = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(en[key], vars), {
    n: (key: keyof typeof en, count: number, vars?: Record<string, string | number>) => fill(pickForm(en[key], count, "en"), { n: count, ...vars }),
  });
  const panel = { source: "panel" };

  it("folds an update (the new build, its commit, the panel's verdict) into one line", () => {
    const rows = [
      ev("agent_started", T0, { version: "0.3.1", prev_version: "0.3.0", reason: "update" }),
      start(T0 + 3, "hy2", "inb_1", "agent_start"),
      ev("update_committed", T0 + 305, { from_version: "0.3.0", to_version: "0.3.1" }),
      ev("update_step_passed", T0 + 320, { from_version: "0.3.0", to_version: "0.3.1" }, panel),
    ];
    const lines = buildLines(rows);
    expect(lines).toHaveLength(1);
    expect(lineText(tn as never, lines[0]!)).toEqual({ title: "Agent updated 0.3.0 → 0.3.1", sub: "the check passed, the version is pinned; profiles up: hy2" });
    expect(lineRows(lines[0]!)).toHaveLength(4);
    expect(lines[0]!.status).toBe("ok");
  });

  it("an update whose start is not loaded is still one line, and an update long after is another", () => {
    const rows = [
      ev("update_committed", T0, { from_version: "0.3.0", to_version: "0.3.1" }),
      ev("update_step_passed", T0 + 20, { from_version: "0.3.0", to_version: "0.3.1" }, panel),
      ev("update_step_passed", T0 + updateWindowS + 60, { from_version: "0.3.1", to_version: "0.3.2" }, panel),
    ];
    const lines = buildLines(rows);
    expect(lines.map((l) => l.kind)).toEqual(["update", "update"]);
    expect(lineText(tn as never, lines[1]!)).toEqual({ title: "Agent updated 0.3.0 → 0.3.1", sub: "the check passed, the version is pinned" });
    expect(lineText(tn as never, lines[0]!)).toEqual({ title: "Agent updated 0.3.1 → 0.3.2", sub: "the check passed" });
  });

  it("folds a rollback (the panel's and the agent's row, the old build starting again) into one line with its reason", () => {
    const rows = [
      ev("agent_started", T0, { version: "0.3.1", prev_version: "0.3.0", reason: "update" }),
      ev("update_step_rolled_back", T0 + 200, { from_version: "0.3.0", to_version: "0.3.1", reason: "probe_failed" }, { ...panel, severity: EventSeverity.WARNING }),
      ev("update_rolled_back", T0 + 230, { from_version: "0.3.0", to_version: "0.3.1", reason: "not_committed" }, { severity: EventSeverity.WARNING }),
      ev("agent_started", T0 + 230, { version: "0.3.0", prev_version: "0.3.1", reason: "update" }),
      start(T0 + 232, "hy2", "inb_1", "agent_start"),
    ];
    const lines = buildLines(rows);
    expect(lines.map((l) => l.kind)).toEqual(["rollback", "agent_started"]); // the update, then its rollback above it
    expect(lineText(tn as never, lines[0]!)).toEqual({
      title: "The update to 0.3.1 rolled back: The client-eye check failed after the update.",
      sub: "0.3.0 runs again; profiles up: hy2",
    });
    expect(lines[0]!.status).toBe("warn");
    expect(lineRows(lines[0]!)).toHaveLength(4);
  });

  it("takes the agent's reason when the panel only says the agent gave up by itself", () => {
    const rows = [
      ev("update_rolled_back", T0, { to_version: "0.3.1", reason: "crash_loop" }, { severity: EventSeverity.WARNING }),
      ev("update_step_rolled_back", T0 + 40, { to_version: "0.3.1", reason: "rolled_back_by_agent" }, { ...panel, severity: EventSeverity.WARNING }),
    ];
    const [l] = buildLines(rows);
    expect(lineText(tn as never, l!).title).toBe("The update to 0.3.1 rolled back: The new version crashed three times in a row.");
  });

  it("words an applied configuration without the zeros, and the link to the panel with its length", () => {
    const applied = (p: Record<string, string>) => lineText(tn as never, buildLines([ev("state_applied", T0, { revision: "4", ...p })])[0]!).title;
    expect(applied({ added: "1", removed: "0", changed: "0", users: "14" })).toBe("Settings applied: 1 profile added · 14 devices");
    expect(applied({ added: "0", removed: "2", changed: "1", users: "0" })).toBe("Settings applied: 2 profiles removed · 1 profile changed");
    expect(applied({ added: "0", removed: "0", changed: "0", users: "0" })).toBe("Settings applied");
    const one = (code: string, p: Record<string, string>) => lineText(tn as never, buildLines([ev(code, T0, p)])[0]!).title;
    expect(one("node_blip", { minutes: "3", rebooted: "false" })).toBe("the link dropped for 3 min and came back by itself");
    expect(one("node_blip", { minutes: "3", rebooted: "true" })).toBe("the server rebooted (3 min without a link)");
    expect(one("node_recovered", { minutes: "47" })).toBe("back online after 47 min");
    expect(one("clock_skew", { offset_s: "4" })).toBe("the server clock differs from the panel’s by 4 s");
  });
});

describe("kernel readiness of a node", () => {
  const item = (detailCode: string, params: Record<string, string> = {}) => ({ detailCode, params });
  it("reads the kernel_headers check", () => {
    expect(kernelReadiness(item("kernel_headers.ready"), "kvm").state).toBe("ready");
    expect(kernelReadiness(item("kernel_headers.userspace", { missing: "headers,dkms" }), "kvm")).toMatchObject({ state: "missing", missing: "headers, dkms" });
    expect(kernelReadiness(item("kernel_headers.missing", { missing: "gcc" }), "kvm")).toMatchObject({ state: "missing", missing: "gcc" });
  });
  it("cannot say before the node serves AmneziaWG (the check is skipped then), unless the host is a container", () => {
    expect(kernelReadiness(item("kernel_headers.no_awg"), "kvm").state).toBe("unchecked");
    expect(kernelReadiness(undefined, "kvm").state).toBe("unchecked");
    expect(kernelReadiness(undefined, "OpenVZ")).toMatchObject({ state: "container", virt: "OpenVZ" });
    expect(kernelReadiness(item("kernel_headers.container", { virt: "lxc" }), "").state).toBe("container");
  });
});
