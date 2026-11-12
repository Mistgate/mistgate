import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { badgeOf, clientLogFileName, downloadClientLogs, logView, openRequestOf, type ClientLog } from "./client-debug";

const ops = vi.hoisted(() => ({ exportClientDebug: vi.fn() }));
vi.mock("@/lib/api", () => ({ ops, auth: { me: () => Promise.resolve({}) } }));

const NOW = 1_800_000_000;
const log = (over: Partial<ClientLog>): ClientLog =>
  ({ id: "dbr_1", deviceId: "dev_1", state: "open", unasked: false, requestedUnix: NOW - 100, expiresUnix: NOW + 1000, closedUnix: 0, ...over }) as ClientLog;

describe("the state of a request as the owner reads it", () => {
  it("tells an open request from one whose time passed, and a bundle sent on its own from one that was asked for", () => {
    expect(logView(log({}), NOW)).toBe("open");
    expect(logView(log({ expiresUnix: NOW }), NOW)).toBe("expired"); // the panel says expired at that second too
    expect(logView(log({ state: "received" }), NOW)).toBe("received");
    expect(logView(log({ state: "received", unasked: true }), NOW)).toBe("unasked");
    expect(logView(log({ state: "declined" }), NOW)).toBe("declined");
    expect(logView(log({ state: "cancelled" }), NOW)).toBe("cancelled");
    expect(logView(log({ state: "expired" }), NOW)).toBe("expired");
    expect(logView(log({ state: "something new" }), NOW)).toBe("expired");
  });

  it("finds the one open request of a device, and the answer that deserves a badge", () => {
    const logs = [
      log({ id: "a", deviceId: "d1", state: "declined", requestedUnix: NOW - 500 }),
      log({ id: "b", deviceId: "d1", state: "open" }),
      log({ id: "c", deviceId: "d2", state: "cancelled" }),
      log({ id: "d", deviceId: "d3", state: "received", unasked: true }),
      log({ id: "e", deviceId: "d3", state: "declined" }),
    ];
    expect(openRequestOf(logs, "d1", NOW)?.id).toBe("b");
    expect(openRequestOf(logs, "d2", NOW)).toBeUndefined();
    expect(badgeOf(logs, "d1", NOW)?.id).toBe("b"); // an open request first
    expect(badgeOf(logs, "d2", NOW)).toBeUndefined(); // a cancelled one is no news
    expect(badgeOf(logs, "d3", NOW)?.id).toBe("d"); // the newest answer (the list is newest first)
    expect(badgeOf(logs, "d9", NOW)).toBeUndefined();
  });
});

describe("the download", () => {
  beforeEach(() => {
    URL.createObjectURL = vi.fn(() => "blob:logs");
    URL.revokeObjectURL = vi.fn();
  });
  afterEach(() => vi.restoreAllMocks());

  it("names the file by the id alone, never by what the person wrote", () => {
    expect(clientLogFileName("dbr_8sQe2LkVt0bW3nYpZr6uHg")).toBe("mistgate-client-logs-dbr_8sQe2LkVt0bW3nYpZr6uHg.ndjson");
    expect(clientLogFileName("dbr_x/../evil name.exe")).toBe("mistgate-client-logs-dbr_xevilnameexe.ndjson");
  });

  it("streams the chunks into one file, through the step-up guard", async () => {
    ops.exportClientDebug.mockImplementation(async function* () {
      yield { data: new TextEncoder().encode('{"seq":1}\n') };
      yield { data: new TextEncoder().encode('{"seq":2}\n') };
    });
    const saved: string[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      saved.push(this.download);
    });
    let guarded = 0;
    const guard = <T>(call: () => Promise<T>) => {
      guarded++;
      return call();
    };
    await downloadClientLogs(guard, "dbr_1");
    expect(guarded).toBe(1);
    expect(ops.exportClientDebug).toHaveBeenCalledWith({ id: "dbr_1" });
    expect(saved).toEqual(["mistgate-client-logs-dbr_1.ndjson"]);
    const blob = vi.mocked(URL.createObjectURL).mock.calls[0]![0] as Blob;
    expect(await blob.text()).toBe('{"seq":1}\n{"seq":2}\n');
  });

  it("saves nothing when the panel refuses", async () => {
    ops.exportClientDebug.mockImplementation(async function* () {
      yield await Promise.reject(new ConnectError("no logs", Code.FailedPrecondition));
    });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    await expect(downloadClientLogs((call) => call(), "dbr_1")).rejects.toThrow("no logs");
    expect(click).not.toHaveBeenCalled();
  });
});
