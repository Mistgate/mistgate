import { describe, expect, it } from "vitest";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { fill } from "@/i18n";
import { en } from "@/i18n/en";
import { describeAudit, isFailure } from "./audit";
import { describeEvent, eventKind, userAnchorOf } from "./events";

const t = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(en[key], vars), { n: () => "" });

describe("events", () => {
  it("colours by code first (a blip is never red), then by severity", () => {
    expect(eventKind({ code: "node_blip", severity: EventSeverity.ERROR })).toBe("blip");
    expect(eventKind({ code: "node_down", severity: EventSeverity.ERROR })).toBe("bad");
    expect(eventKind({ code: "stats_stale", severity: EventSeverity.WARNING })).toBe("warn");
    expect(eventKind({ code: "node_recovered", severity: EventSeverity.INFO })).toBe("ok");
    // a restart that happened is a still dot, never the spinning ring of "in progress"
    expect(eventKind({ code: "engine_restarted", severity: EventSeverity.INFO })).toBe("ok");
  });
  it("says logs came from the Mistgate app, on request or on the person's own, with the count and nothing of the logs", () => {
    expect(describeEvent(t as never, { code: "client_logs_received", params: { id: "dbr_1", lines: "18", unasked: "false" } })).toEqual({ text: "the Mistgate app sent its logs (18 lines)", detail: "" });
    expect(describeEvent(t as never, { code: "client_logs_received", params: { id: "dbr_1", lines: "22", unasked: "true" } }).text).toBe("a person sent the logs of the Mistgate app on their own (22 lines)");
  });
  it("words the refusal to send logs, and leads the logs of the app to the section of the person's page", () => {
    expect(describeEvent(t as never, { code: "client_logs_declined", params: { id: "dbr_1" } })).toEqual({ text: "a person declined to send the logs of the Mistgate app", detail: "" });
    expect(userAnchorOf("client_logs_received")).toBe("client-logs");
    expect(userAnchorOf("client_logs_declined")).toBe("client-logs");
    expect(userAnchorOf("user_created")).toBeUndefined();
  });
  it("words a known code and keeps the error text as the detail", () => {
    expect(describeEvent(t as never, { code: "engine_failed", params: { error: "bind: address in use" } })).toEqual({
      text: "profile failed to start",
      detail: "bind: address in use",
    });
    expect(describeEvent(t as never, { code: "engine_failed", params: {}, profileName: "hy2 · WARP · 8443" }).text).toBe("“hy2 · WARP · 8443” failed to start");
  });
  it("says since when a node is silent, not how long it had been when the panel noticed", () => {
    const at = 1_790_000_000;
    const stamp = (unix: number) => `@${unix}`;
    expect(describeEvent(t as never, { code: "node_down", params: { minutes: "10" }, timeUnix: at }, stamp).text).toBe(`stopped answering (since @${at - 600})`);
    expect(describeEvent(t as never, { code: "node_down", params: { minutes: "10" } }).text).toBe("stopped answering");
  });
  it("gives a blip and a recovery their length, and a reboot its own words", () => {
    expect(describeEvent(t as never, { code: "node_blip", params: { minutes: "3", rebooted: "false" } }).text).toBe("the link dropped for 3 min and came back by itself");
    expect(describeEvent(t as never, { code: "node_blip", params: { minutes: "3", rebooted: "true" } }).text).toBe("the server rebooted (3 min without a link)");
    expect(describeEvent(t as never, { code: "node_recovered", params: { minutes: "2880" } }).text).toBe("back online after 2 d");
    expect(describeEvent(t as never, { code: "clock_skew", params: { offset_s: "4" } }).text).toBe("the server clock differs from the panel’s by 4 s");
  });
  it("words the bandwidth and tunnel-firewall fallbacks, the second one by how far it fell back", () => {
    expect(describeEvent(t as never, { code: "bandwidth_upload_missing", params: { down_mbps: "4984" } }).text).toContain("4984 Mbps down but no upload figure");
    expect(describeEvent(t as never, { code: "tunnel_v6_fallback", params: { mode: "drop" } }).text).toBe(en["event.tunnel_v6_fallback"]);
    expect(describeEvent(t as never, { code: "tunnel_v6_fallback", params: { mode: "none" } }).text).toBe(en["event.tunnel_v6_fallback.none"]);
  });
  it("says what the torrent guard and the host firewall need, with the ports the agent listed", () => {
    expect(describeEvent(t as never, { code: "torrent_guard_degraded", params: { error: "nfqueue: operation not supported" } })).toEqual({
      text: en["event.torrent_guard_degraded"],
      detail: "nfqueue: operation not supported",
    });
    const fw = describeEvent(t as never, { code: "host_firewall_sync_failed", params: { error: "firewalld is active", ports: "20000-20010, 443, tcp:8443" } });
    expect(fw.text).toBe("the server’s firewall may block these ports: 20000-20010, 443, tcp:8443 (plain numbers and ranges are UDP, “tcp:” is TCP): allow them in UFW, or add them to the active firewalld zone by hand");
    expect(fw.detail).toBe("firewalld is active");
    // an older agent names no ports
    expect(describeEvent(t as never, { code: "host_firewall_sync_failed", params: { error: "x" } }).text).toBe(en["event.host_firewall_sync_failed.plain"]);
    expect(describeEvent(t as never, { code: "host_firewall_sync_recovered", params: { resolved_error: "x" } }).text).toBe(en["event.host_firewall_sync_recovered"]);
  });
  it("words the OS update of a node: the agent's job, its reboot, and the panel's verdict", () => {
    const d = (code: string, params: Record<string, string> = {}) => describeEvent(t as never, { code, params }).text;
    expect(d("os_update_started", { action: "update" })).toBe("OS update started");
    expect(d("os_update_started", { action: "refresh" })).toBe("refreshing the package lists");
    expect(d("os_update_done", { action: "update", upgraded: "6" })).toBe("OS update finished, packages installed: 6");
    expect(d("os_update_done", { action: "update", upgraded: "0" })).toBe("OS update finished");
    expect(d("os_update_done", { action: "refresh" })).toBe("package lists refreshed");
    expect(d("os_update_failed", { code: "disk_full" })).toBe("OS update failed");
    expect(d("os_reboot_started", { kernel: "6.8.0-150-generic" })).toBe("rebooting after the OS update");
    expect(d("os_step_passed")).toBe("OS update finished, the node passed the check");
    expect(d("os_step_failed", { reason: "not_back" })).toBe("OS update of the node stopped");
    expect(eventKind({ code: "os_update_failed", severity: EventSeverity.ERROR })).toBe("bad");
    expect(eventKind({ code: "os_step_passed", severity: EventSeverity.INFO })).toBe("ok");
  });
  it("words the panel's own lossy path to a node as an info event", () => {
    expect(describeEvent(t as never, { code: "panel_path_lossy", params: { vantage: "nl2" } }).text).toBe("The panel's own path to the node loses packets; its profiles answer from nl2");
    expect(eventKind({ code: "panel_path_lossy", severity: EventSeverity.INFO })).toBe("ok");
  });
  it("tells which sources a node's update came from, and names only the ones it knows", () => {
    const p = (sources_used: string) => ({ code: "update_download_source", params: { to_version: "0.3.1", sources_used } });
    expect(describeEvent(t as never, p("panel,github")).text).toBe("downloaded 0.3.1, sources: panel + GitHub");
    expect(describeEvent(t as never, p("github")).text).toBe("downloaded 0.3.1, sources: GitHub");
    expect(describeEvent(t as never, p("<b>x</b>")).text).toBe("downloaded 0.3.1, sources: —");
  });
  it("shows an unknown code with its params, so nothing is hidden", () => {
    expect(describeEvent(t as never, { code: "brand_new", params: { a: "1", b: "2" } })).toEqual({ text: "brand_new", detail: "a=1 b=2" });
  });
});

describe("audit", () => {
  it("words known actions with their params and leaves unknown ones as they are", () => {
    expect(describeAudit(t as never, { action: "node.retire", paramsJson: '{"name":"de1","node_id":"nod_x"}' })).toBe("retired de1 from the fleet");
    expect(describeAudit(t as never, { action: "login", paramsJson: "not json" })).toBe("signed in with a passkey");
    expect(describeAudit(t as never, { action: "user.create", paramsJson: "{}" })).toBe("user.create");
  });
  it("calls anything but ok a failure", () => {
    expect(isFailure("ok")).toBe(false);
    expect(isFailure("")).toBe(false);
    expect(isFailure("locked")).toBe(true);
  });
});
