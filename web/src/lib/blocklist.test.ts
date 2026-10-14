import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { fill, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { zh } from "@/i18n/zh";
import { blocklistErrorText, listsState, type BlocklistSettings, type BlocklistTest } from "./blocklist";

const tOf = (dict: Record<string, string>) => Object.assign((key: string, vars?: Record<string, string | number>) => fill(dict[key]!, vars), { n: () => "" }) as unknown as T;
const t = tOf(en);

const s = (over: Partial<BlocklistSettings> = {}): BlocklistSettings => ({ enabled: true, available: true, checkedUnix: 0, listsUnix: 0, ...over });
const found: BlocklistTest = { listsUnix: 150, prefixes: 20580, cdnRanges: 1234, nodesChecked: 4, nodesListed: 1, mirrorUsed: false };

describe("the state of the block-list card", () => {
  it("is off while it is off, and not available for another country, whatever happened before", () => {
    expect(listsState(s({ enabled: false, checkedUnix: 100 }), { at: 200, ok: found })).toEqual({ kind: "off" });
    expect(listsState(s({ available: false, checkedUnix: 100 }), { at: 200, ok: found })).toEqual({ kind: "unavailable" });
    // off for another country is off: nothing would happen either way
    expect(listsState(s({ enabled: false, available: false }), null)).toEqual({ kind: "off" });
  });

  it("is never tested when on with no test and no check yet", () => {
    expect(listsState(s(), null)).toEqual({ kind: "untested" });
  });

  it("works on the stored check, with the date of its lists, and after a good test with what the test found", () => {
    expect(listsState(s({ checkedUnix: 300, listsUnix: 120 }), null)).toEqual({ kind: "works", listsAt: 120, checkedAt: 300 });
    expect(listsState(s({ checkedUnix: 100, listsUnix: 90 }), { at: 200, ok: found })).toEqual({ kind: "works", listsAt: 150, checkedAt: 200, test: found });
  });

  it("fails after a failed test until a newer check works", () => {
    const err = new ConnectError("blocklist_unreachable", Code.Unavailable);
    expect(listsState(s({ checkedUnix: 100 }), { at: 200, error: err })).toEqual({ kind: "fails", error: err });
    expect(listsState(s({ checkedUnix: 300, listsUnix: 280 }), { at: 200, error: err })).toEqual({ kind: "works", listsAt: 280, checkedAt: 300 });
  });
});

describe("block-list errors", () => {
  it("words the panel's codes in every language and leaves the rest to the common errors", () => {
    const err = (msg: string, code = Code.FailedPrecondition) => new ConnectError(msg, code);
    expect(blocklistErrorText(err("blocklist_unreachable", Code.Unavailable), t)).toBe("Could not download the lists, and the panel holds no earlier copy.");
    expect(blocklistErrorText(err("blocklist_unavailable_for_country"), t)).toBe("Block lists exist only for Russia.");
    expect(blocklistErrorText(err("blocklist_off"), t)).toBe("The block lists are off.");
    expect(blocklistErrorText(err("tested a moment ago: try again in 4 s", Code.ResourceExhausted), t)).toBe("Asked too often. Wait a few seconds.");
    expect(blocklistErrorText(err("blocklist_unreachable", Code.Unavailable), tOf(ru))).toBe("Не удалось скачать списки, а прежней копии у панели нет.");
    expect(blocklistErrorText(err("blocklist_unavailable_for_country"), tOf(zh))).toBe("目前只有俄罗斯的封锁名单。");
    // a code from somewhere else is not taken for the panel's own
    expect(blocklistErrorText(err("blocklist_off", Code.PermissionDenied), t)).not.toBe("The block lists are off.");
  });
});
