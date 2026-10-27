import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { fill, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { zh } from "@/i18n/zh";
import { cardState, globalpingErrorText, looksLikeToken, type GlobalpingTest } from "./globalping";
import type { CountryCheck } from "./health";

const tOf = (dict: Record<string, string>) => Object.assign((key: string, vars?: Record<string, string | number>) => fill(dict[key]!, vars), { n: () => "" }) as unknown as T;
const t = tOf(en);

const probe = { network: "Rostelecom", asn: 1, city: "Moscow", icmpOk: true, icmpMs: 42, tcp: [], valid: true, dark: false, eyeball: true };
const round = (atUnix: number, verdict = CountryVerdict.OK, reason = ""): CountryCheck => ({ nodeId: "nod_1", country: "RU", verdict, atUnix, probes: reason ? [] : [probe], reason, eyeball: true, confidence: "", listedBy: [], cdnProvider: "", cdnRegion: "", sources: [], globalpingVerdict: CountryVerdict.UNSPECIFIED, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [], vantages: [], sourceVerdicts: [] });
const limits: GlobalpingTest = { reachable: true, tokenUsed: true, limit: 500, remaining: 412, resetS: 1800, credits: 0 };
const on = { enabled: true };

describe("the state of the Globalping card", () => {
  it("is off while it is off, whatever happened before", () => {
    expect(cardState({ enabled: false }, { at: 200, ok: limits }, [round(100)])).toEqual({ kind: "off" });
  });

  it("is never tested when on with no test and no round yet", () => {
    expect(cardState(on, null, [])).toEqual({ kind: "untested" });
  });

  it("works after a good test, and says what the test said", () => {
    expect(cardState(on, { at: 200, ok: limits }, [])).toEqual({ kind: "works", test: limits });
    expect(cardState(on, { at: 200, ok: limits }, [round(100)])).toEqual({ kind: "works", test: limits });
  });

  it("works on rounds alone, from the newest one", () => {
    expect(cardState(on, null, [round(100), round(300), round(200, CountryVerdict.TCP_BLOCKED)])).toEqual({ kind: "works", roundAt: 300 });
    // a node's own unknown (too few probes, a mixed picture) is not Globalping's fault
    expect(cardState(on, null, [round(300, CountryVerdict.UNKNOWN, "too_few_probes")])).toEqual({ kind: "works", roundAt: 300 });
  });

  it("fails after a failed test until a newer round works", () => {
    const err = new ConnectError("globalping_unauthorized", Code.FailedPrecondition);
    expect(cardState(on, { at: 200, error: err }, [round(100)])).toEqual({ kind: "fails", error: err });
    expect(cardState(on, { at: 200, error: err }, [round(300)])).toEqual({ kind: "works", roundAt: 300 });
  });

  it("has no data when the newest round failed on Globalping's side", () => {
    for (const reason of ["rate_limited", "source_error", "timeout"])
      expect(cardState(on, { at: 100, ok: limits }, [round(300, CountryVerdict.UNKNOWN, reason)])).toEqual({ kind: "noData", reason, at: 300 });
  });
});

describe("Globalping errors and the token", () => {
  it("words the panel's codes in every language and leaves the rest to the common errors", () => {
    const err = (msg: string, code = Code.FailedPrecondition) => new ConnectError(msg, code);
    expect(globalpingErrorText(err("globalping_unauthorized"), t)).toBe("Globalping refused the token. Paste a new one or disconnect.");
    expect(globalpingErrorText(err("globalping_unreachable", Code.Unavailable), t)).toBe("Globalping does not answer right now.");
    expect(globalpingErrorText(err("token_invalid", Code.InvalidArgument), t)).toBe("This does not look like a Globalping token.");
    expect(globalpingErrorText(err("too soon", Code.ResourceExhausted), t)).toBe("Asked too often. Wait a few seconds.");
    expect(globalpingErrorText(err("globalping_unauthorized"), tOf(ru))).toBe("Globalping не принял токен. Вставь новый или отключи Globalping.");
    expect(globalpingErrorText(err("globalping_unauthorized"), tOf(zh))).toBe("Globalping 拒绝了此令牌。请粘贴新的令牌，或断开连接。");
  });

  it("takes a token of up to 256 characters without spaces", () => {
    expect(looksLikeToken(" abc123_DEF-xyz ")).toBe(true);
    expect(looksLikeToken("")).toBe(false);
    expect(looksLikeToken("two words")).toBe(false);
    expect(looksLikeToken("x".repeat(256))).toBe(true);
    expect(looksLikeToken("x".repeat(257))).toBe(false);
  });
});
