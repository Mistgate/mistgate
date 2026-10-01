import { describe, expect, it } from "vitest";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { fill } from "@/i18n";
import { en } from "@/i18n/en";
import { describeAudit, isFailure } from "./audit";
import { describeEvent, eventKind } from "./events";

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
