import { describe, expect, it } from "vitest";
import { OsUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { dictionaries, fill, pickForm, type Lang, type T } from "@/i18n";
import type { en } from "@/i18n/en";
import { osPill, osRows, osSummary, osUpdatesLine, type OsRow, type OsUpdates } from "./os-updates";

// What a row of Updates -> Server operating system says: the pill is about security and the reboot, ordinary packages are a note.

const tOf = (lang: Lang): T => {
  const dict = dictionaries[lang];
  return Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(dict[key], vars), {
    n: (key: keyof typeof en, count: number, vars?: Record<string, string | number>) => fill(pickForm(dict[key], count, lang), { n: count, ...vars }),
  }) as unknown as T;
};
const t = tOf("en");
const tRu = tOf("ru");

const base = {
  measuredUnix: 1, distroId: "ubuntu", distroVersion: "24.04", pending: 0, security: 0, notable: [], kernelRunning: "6.8.0-147-generic", kernelNewest: "6.8.0-147-generic",
  rebootRequired: false, rebootReasons: [], held: [], apt: "ok", halfConfigured: [], listsUnix: 1, dpkgUnix: 1, autoUpdates: "on", autoReboot: false, awgModuleNext: "", container: "", bootIdHash: "", error: "",
};
const row = (state: OsUpdateState, status: Record<string, unknown>): OsRow =>
  ({ id: "nod_1", name: "de1", nodeId: "nod_1", onPanelServer: false, state, status: { ...base, ...status }, updateCapable: true, updateBlock: "", rebootBlock: "" }) as never;

describe("the pill of a row", () => {
  it("is green for a server with no security update and no reboot due, whether or not ordinary packages wait", () => {
    expect(osPill(row(OsUpdateState.UP_TO_DATE, { pending: 18 }))).toEqual({ kind: "ok", key: "up.os.state.security_ok" });
    expect(osPill(row(OsUpdateState.UP_TO_DATE, { pending: 0 }))).toEqual({ kind: "ok", key: "up.os.state.up_to_date" });
    expect(t("up.os.state.security_ok")).toBe("Security up to date");
    expect(tRu("up.os.state.security_ok")).toBe("Безопасность в порядке");
  });
  it("stays amber for security updates and for a reboot, and red for a failure", () => {
    expect(osPill(row(OsUpdateState.UPDATES, { pending: 18, security: 2 })).kind).toBe("warn");
    expect(osPill(row(OsUpdateState.REBOOT_REQUIRED, { pending: 18, rebootRequired: true })).kind).toBe("warn");
    expect(osPill(row(OsUpdateState.FAILED, {})).kind).toBe("bad");
  });
  it("keeps grey for what is not known or not available", () => {
    for (const state of [OsUpdateState.OFFLINE, OsUpdateState.UNSUPPORTED, OsUpdateState.UNKNOWN]) expect(osPill(row(state, {})).kind).toBe("off");
  });
});

describe("the count of a row", () => {
  it("says None when nothing waits", () => {
    expect(osUpdatesLine(t, { pending: 0, security: 0 })).toEqual({ main: "None", warn: false, note: "" });
  });
  it("puts ordinary updates in a quiet note next to \"no security updates\"", () => {
    expect(osUpdatesLine(t, { pending: 18, security: 0 })).toEqual({ main: "No security updates", warn: false, note: "18 ordinary updates" });
    expect(osUpdatesLine(t, { pending: 1, security: 0 }).note).toBe("1 ordinary update");
    expect(osUpdatesLine(tRu, { pending: 18, security: 0 })).toEqual({ main: "Обновлений безопасности нет", warn: false, note: "18 обычных обновлений" });
    expect(osUpdatesLine(tRu, { pending: 2, security: 0 }).note).toBe("2 обычных обновления");
    expect(osUpdatesLine(tRu, { pending: 21, security: 0 }).note).toBe("21 обычное обновление");
  });
  it("shows the security count amber, with everything that waits", () => {
    expect(osUpdatesLine(t, { pending: 12, security: 4 })).toEqual({ main: "12 waiting · 4 security", warn: true, note: "" });
  });
});

describe("the rows of the answer", () => {
  const answer = (nodes: unknown[], panel = base): OsUpdates => ({ nowUnix: 1_800_000_000, scheduleTimezoneOffsetMinutes: 0, panelHost: panel, nodes }) as never;
  it("read the panel's own server by the same rule as a node's", () => {
    expect(osRows(answer([], { ...base, pending: 30 }))[0]!.state).toBe(OsUpdateState.UP_TO_DATE);
    expect(osRows(answer([], { ...base, pending: 30, security: 1 }))[0]!.state).toBe(OsUpdateState.UPDATES);
    expect(osRows(answer([], { ...base, pending: 30, rebootRequired: true }))[0]!.state).toBe(OsUpdateState.REBOOT_REQUIRED);
  });
  it("carry when a reboot is possible, and the server's clock the minutes are counted from", () => {
    const rows = osRows(
      answer([
        {
          nodeId: "nod_1", name: "de1", connected: true, supported: true, onlineUsers: 0, onPanelServer: false, state: OsUpdateState.UP_TO_DATE, status: base, updateCapable: true,
          updateBlock: "", rebootBlock: "reboot_wait", rebootPossibleUnix: 1_800_000_600,
        },
      ]),
    );
    expect(rows[1]).toMatchObject({ rebootBlock: "reboot_wait", rebootPossibleUnix: 1_800_000_600, nowUnix: 1_800_000_000 });
  });
  it("name the ordinary updates of green servers in the summary only as a note", () => {
    const rows = [row(OsUpdateState.UP_TO_DATE, { pending: 18 }), row(OsUpdateState.UP_TO_DATE, { pending: 0 })];
    expect(osSummary(rows)).toEqual({ reboot: 0, security: 0, updates: 1, known: 2 });
    expect(t.n("up.os.summary.updates", 1)).toBe("Security is up to date on every server. Ordinary updates wait on 1 server");
    expect(t.n("up.os.summary.updates", 3)).toBe("Security is up to date on every server. Ordinary updates wait on 3 servers");
    expect(tRu.n("up.os.summary.updates", 2)).toBe("Безопасность в порядке на всех серверах. Обычные обновления ждут на 2 серверах");
  });
});
