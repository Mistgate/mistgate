import { describe, expect, it } from "vitest";
import { dictionaries, langs } from "@/i18n";
import { en } from "@/i18n/en";
import { errorCodeOf, errorCodes, marksOf, minutesOf, SCAN_PAUSE_S, statsOf, viewOf, type Candidate, type Scan } from "./sni-scan";

const NOW = 1_800_000_000;
const cand = (over: Partial<Candidate> = {}): Candidate => ({ ip: "203.0.113.40", name: "shop.example.com", dns: "here", h2: true, singleSite: true, http: 200, ms: 11, days: 64, ...over });
const scan = (over: Partial<Scan> = {}): Scan => ({
  error: "",
  retryAfterS: 0,
  atUnix: NOW - 600,
  range: "203.0.113.0/24",
  addresses: 253,
  open: 41,
  tls13: 19,
  mlkem: 7,
  valid: 1,
  partial: false,
  ownLeftOut: 0,
  candidates: [cand()],
  ...over,
});
const opts = { running: false, linked: true, now: NOW, retryUntil: 0 };

describe("viewOf: what the card shows", () => {
  it("never scanned: the plain card and the button that starts", () => {
    const v = viewOf(undefined, opts);
    expect(v).toMatchObject({ phase: "never", problem: null, kept: false, canRun: true, waitS: 0 });
    expect(viewOf(scan({ atUnix: 0, candidates: [] }), opts).phase).toBe("never");
  });

  it("a scan that is going", () => {
    const v = viewOf(undefined, { ...opts, running: true });
    expect(v.phase).toBe("running");
    expect(v.canRun).toBe(false);
  });

  it("a result, a partial result and an empty one, each with the last scan kept", () => {
    expect(viewOf(scan({ atUnix: NOW - SCAN_PAUSE_S - 1 }), opts)).toMatchObject({ phase: "result", kept: true, canRun: true, waitS: 0 });
    expect(viewOf(scan({ partial: true, atUnix: NOW - SCAN_PAUSE_S - 1 }), opts).phase).toBe("partial");
    expect(viewOf(scan({ candidates: [], valid: 0, atUnix: NOW - SCAN_PAUSE_S - 1 }), opts).phase).toBe("empty");
  });

  it("too early: the button waits out the 30 minutes of the last scan, and says how long", () => {
    const v = viewOf(scan({ atUnix: NOW - 10 * 60 }), opts);
    expect(v.waitS).toBe(20 * 60);
    expect(v.canRun).toBe(false);
    expect(minutesOf(v.waitS)).toBe(20);
    expect(viewOf(scan({ atUnix: NOW - SCAN_PAUSE_S + 1 }), opts).waitS).toBe(1);
    expect(minutesOf(1)).toBe(1);
    expect(minutesOf(61)).toBe(2);
  });

  it("the panel's own too_soon answer holds even when no scan was kept (a failed one counts)", () => {
    const v = viewOf(scan({ atUnix: 0, candidates: [], error: "too_soon", retryAfterS: 900 }), { ...opts, retryUntil: NOW + 900 });
    expect(v).toMatchObject({ problem: "too_soon", waitS: 900, canRun: false });
    // the wait is over: the notice goes and the button is back
    const over = viewOf(scan({ atUnix: 0, candidates: [], error: "too_soon", retryAfterS: 900 }), { ...opts, now: NOW + 901, retryUntil: NOW + 900 });
    expect(over).toMatchObject({ problem: null, waitS: 0, canRun: true });
  });

  it("each refusal of the panel is a problem of its own, and the kept list stays", () => {
    for (const code of errorCodes) {
      const v = viewOf(scan({ error: code, atUnix: NOW - SCAN_PAUSE_S - 1 }), opts);
      if (code === "too_soon") expect(v.problem).toBeNull(); // nothing to wait for any more
      else expect(v.problem, code).toBe(code);
      expect(v.kept, code).toBe(true);
      expect(v.phase, code).toBe("result");
    }
    for (const code of ["node_offline", "agent_too_old", "not_my_address", "no_ipv4", "no_roots"]) expect(viewOf(scan({ error: code }), opts).canRun, code).toBe(false);
    for (const code of ["busy", "failed"]) expect(viewOf(scan({ error: code, atUnix: 0, candidates: [] }), opts).canRun, code).toBe(true);
  });

  it("a node that is not connected cannot scan, whatever it was told before", () => {
    const v = viewOf(scan({ atUnix: NOW - SCAN_PAUSE_S - 1 }), { ...opts, linked: false });
    expect(v).toMatchObject({ problem: "node_offline", canRun: false, phase: "result" });
  });

  it("an error code it does not know reads as a failure", () => {
    expect(errorCodeOf("something_new")).toBe("failed");
    expect(viewOf(scan({ error: "something_new", atUnix: 0, candidates: [] }), opts).problem).toBe("failed");
  });
});

describe("marksOf: the labels of a site", () => {
  const ids = (c: Candidate) => marksOf(c).map((m) => m.key);
  it("says where DNS leads, HTTP/2, whose certificate, MLKEM, the answer, the time and the days", () => {
    expect(ids(cand())).toEqual(["sni.mark.dns.here", "sni.mark.h2", "sni.mark.single", "sni.mark.mlkem", "sni.mark.http", "sni.mark.ms", "sni.mark.days"]);
    expect(ids(cand({ dns: "subnet", h2: false, singleSite: false, http: 0, ms: 0, days: 0 }))).toEqual(["sni.mark.dns.subnet", "sni.mark.shared", "sni.mark.mlkem", "sni.mark.nohttp"]);
    expect(ids(cand({ dns: "elsewhere" }))[0]).toBe("sni.mark.dns.elsewhere");
    expect(ids(cand({ dns: "none" }))[0]).toBe("sni.mark.dns.none");
  });
  it("a verdict it does not know is no record (nothing is built from a stranger's word)", () => {
    expect(ids(cand({ dns: "<img src=x onerror=alert(1)>" }))[0]).toBe("sni.mark.dns.none");
  });
  it("the tone says good news from bad: a name that leads here with its own certificate is green, one behind a CDN is amber", () => {
    const tones = (c: Candidate) => Object.fromEntries(marksOf(c).map((m) => [m.id, m.tone]));
    expect(tones(cand())).toMatchObject({ dns: "good", h2: "good", single: "good", http: "good" });
    expect(tones(cand({ dns: "elsewhere", singleSite: false, http: 403 }))).toMatchObject({ dns: "warn", shared: "warn", http: "warn" });
    expect(tones(cand({ http: 301 })).http).toBe("plain");
  });
});

describe("statsOf", () => {
  it("lists the counters in the order the screen shows them", () => {
    expect(statsOf(scan()).map((s) => [s.id, s.n])).toEqual([["checked", 253], ["open", 41], ["tls13", 19], ["mlkem", 7], ["valid", 1]]);
  });
});

describe("the words", () => {
  const keys = Object.keys(en).filter((k) => k.startsWith("sni."));
  it("every code of the panel, every label and every state has a text in all five languages", () => {
    for (const code of errorCodes) expect(keys, code).toContain(`sni.err.${code}`);
    for (const k of ["sni.mark.dns.here", "sni.mark.dns.subnet", "sni.mark.dns.elsewhere", "sni.mark.dns.none", "sni.mark.h2", "sni.mark.single", "sni.mark.shared", "sni.mark.mlkem", "sni.mark.http", "sni.mark.nohttp", "sni.mark.ms", "sni.mark.days", "sni.warn.body", "sni.why.hysteria2", "sni.why.trusttunnel"]) expect(keys, k).toContain(k);
    for (const lang of langs) for (const k of keys) expect(dictionaries[lang][k as keyof typeof en], `${lang} ${k}`).toBeTruthy();
  });
  it("the warning is the plan's text in every language: the subnet, port 443, ten a second, the hoster, thirty minutes", () => {
    expect(en["sni.warn.body"]).toMatch(/253/);
    for (const lang of langs) {
      const body = dictionaries[lang]["sni.warn.body"];
      for (const part of ["253", "/24", "443", "10", "30"]) expect(body, `${lang} ${part}`).toContain(part);
    }
  });
  it("the dialect of the admin: ty (never the polite plural) in Russian", () => {
    for (const k of keys) expect(dictionaries.ru[k as keyof typeof en], k).not.toMatch(/\b(вы|вам|ваш\w*|вас|нажмите|откройте|скопируйте|выберите)\b/i);
  });
});
