import { describe, expect, it } from "vitest";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { dictionaries, fill, langs, type Lang, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { checkReason, enableGate, relayState, type RelayMsg } from "./relay";
import {
  activeOf,
  blockedOf,
  blockedText,
  bridgeCarries,
  bridgeErrorText,
  bridgeLoad,
  bridgeNote,
  modeOf,
  reachReason,
  reasonText,
  speedOk,
  speedReason,
  wantsReach,
} from "./relay-bridge";

const tOf = (lang: Lang) => Object.assign((key: string, vars?: Record<string, string | number>) => fill(dictionaries[lang][key as MessageKey], vars), { n: () => "" }) as unknown as T;
const tEn = tOf("en");
const now = 1_800_000_000;
const n = { entry: "msk1", exit: "de1" };

const stage = (over: Record<string, unknown> = {}) => ({ status: "ok", code: "", senderId: "", senderName: "", verdict: "", sent: 0, got: 0, atUnix: now - 60, mb: 0, mbitS: 0, bytes: 0, connectMs: 0, firstByteMs: 0, durationMs: 0, port: 0, ...over });
const check = (over: Record<string, unknown> = {}) => ({ atUnix: now - 60, network: "tcp", port: 8443, to: "203.0.113.7:443", leg: stage({ verdict: "ours" }), path: stage({ status: "skipped", code: "off" }), ok: true, stage: "", verified: false, ...over });
const relay = (over: Record<string, unknown> = {}) =>
  ({
    id: "rly_1", inboundId: "inb_1", exitNodeId: "nod_de1", exitNodeName: "de1", profileId: "prf_1", profileName: "VLESS · TCP", protocol: "vless", viaNodeId: "nod_msk1", viaNodeName: "msk1",
    network: "tcp", port: 8443, exitPort: 443, enabled: false, state: InboundState.PENDING, lastError: "", toIp: "", appliedUnix: 0, exitLive: true, agentSupported: true, createdUnix: now - 3600, check: undefined,
    mode: "auto", activeMode: "dnat", modeChangedUnix: 0, modeReason: "", directStreak: 0, reverseBlocked: "", ...over,
  }) as unknown as RelayMsg;
const legDown = { ok: false, stage: "leg", leg: stage({ status: "fail", code: "timeout" }) };

describe("the way of a relay", () => {
  it("reads an empty mode as auto and an empty active mode as direct (a panel before the bridge)", () => {
    expect(modeOf({})).toBe("auto");
    expect(modeOf({ mode: "reverse" })).toBe("reverse");
    expect(activeOf({})).toBe("dnat");
    expect(activeOf({ activeMode: "reverse" })).toBe("reverse");
    expect(wantsReach({})).toBe(false); // an older panel does not know the bridge: ask it nothing about it
    expect(wantsReach({ mode: "auto", network: "tcp" })).toBe(true);
    expect(wantsReach({ mode: "dnat", network: "tcp" })).toBe(false);
    expect(wantsReach({ mode: "auto", network: "udp" })).toBe(false);
  });

  it("blocks the bridge for a UDP server, even for a panel that did not say, and names the agent to update", () => {
    expect(blockedOf({ reverseBlocked: "", network: "udp" })).toBe("udp");
    expect(blockedOf({ reverseBlocked: "entry_agent_old", network: "tcp" })).toBe("entry_agent_old");
    expect(blockedOf({ reverseBlocked: "", network: "tcp" })).toBe("");
    expect(blockedText(tEn, "udp", n)).toBe("A bridge works only for TCP for now: VLESS and MTProto. Hysteria2 and AmneziaWG stay direct.");
    expect(blockedText(tEn, "exit_agent_old", n)).toBe("A bridge is not available: update the agent of de1.");
    expect(blockedText(tEn, "entry_agent_old", n)).toBe("A bridge is not available: update the agent of msk1.");
    expect(blockedText(tEn, "", n)).toBe("");
  });

  it("has a sentence, in every language, for every reason the panel gives and for none", () => {
    const modeReasons = ["leg_failed", "direct_back", "bridge_failed", "owner", "reverse_unsupported", "udp"];
    for (const lang of langs) {
      const t = tOf(lang);
      for (const code of modeReasons) {
        for (const active of ["dnat", "reverse"]) {
          const text = reasonText(t, relay({ modeReason: code, activeMode: active, reverseBlocked: code === "reverse_unsupported" ? "exit_agent_old" : "" }));
          expect(text, `${lang} ${code} ${active}`).not.toBe("");
          expect(text, `${lang} ${code} ${active}`).not.toContain("{");
          expect(text, `${lang} ${code} ${active}`).not.toContain("undefined");
        }
      }
      expect(reasonText(t, relay({ modeReason: "" })), lang).toBe("");
    }
    expect(reasonText(tEn, relay({ modeReason: "leg_failed", activeMode: "reverse" }))).toBe("msk1 does not reach de1 directly, so the relay goes through a bridge.");
    expect(reasonText(tEn, relay({ modeReason: "owner", mode: "reverse", activeMode: "reverse" }))).toBe("Bridge only: your choice.");
    expect(reasonText(tEn, relay({ modeReason: "owner", mode: "dnat" }))).toBe("Direct only: your choice.");
    expect(reasonText(tEn, relay({ modeReason: "reverse_unsupported", reverseBlocked: "entry_agent_old" }))).toBe("A bridge is not available: update the agent of msk1.");
  });

  it("words every word an agent says for the bridge, and none for the ones that are not errors", () => {
    const codes = [
      "disconnected", "stale", "warming", "no_verification", "pin_mismatch", "connect", "tls_protocol", "tls_exporter", "http2", "connection_limit", "stream_unavailable", "queue_full", "stream_limit",
      "pair_timeout", "target_connect", "identity_unavailable", "site_listen_failed", "site_certificate_unavailable", "port_conflict", "timeout_unsupported", "bad_config", "target_not_local", "a_new_word",
    ];
    for (const lang of langs) {
      for (const code of codes) {
        const text = bridgeErrorText(tOf(lang), code, n);
        expect(text, `${lang} ${code}`).not.toBe("");
        expect(text, `${lang} ${code}`).not.toMatch(/\{\w+\}/);
      }
      for (const quiet of ["", "request_idle", "closed"]) expect(bridgeErrorText(tOf(lang), quiet, n)).toBe("");
    }
    expect(bridgeErrorText(tEn, "site_listen_failed", n)).toContain("(site_listen_failed)");
  });

  it("words every code of the exit's check towards the bridge and of the speed checks", () => {
    for (const lang of langs) {
      const t = tOf(lang);
      for (const code of ["timeout", "other", "entry_site_unavailable", "unsupported", "exit_offline", "node_offline", "no_route", "failed", ""]) {
        const text = reachReason(t, { code, port: 8443 }, n);
        expect(text, `${lang} reach ${code}`).not.toBe("");
        expect(text, `${lang} reach ${code}`).not.toMatch(/\{\w+\}/);
      }
      for (const s of [
        { status: "fail", code: "timeout", bytes: 3_000_000, mb: 8 },
        { status: "fail", code: "unreachable", bytes: 0, mb: 8 },
        { status: "fail", code: "entry_offline", bytes: 0, mb: 8 },
        { status: "fail", code: "exit_offline", bytes: 0, mb: 8 },
        { status: "fail", code: "failed", bytes: 0, mb: 8 },
        { status: "skipped", code: "busy", bytes: 0, mb: 0 },
        { status: "skipped", code: "unsupported", bytes: 0, mb: 0 },
      ]) {
        const text = speedReason(t, s, n);
        expect(text, `${lang} speed ${s.code}`).not.toBe("");
        expect(text, `${lang} speed ${s.code}`).not.toMatch(/\{\w+\}/);
      }
    }
    expect(reachReason(tEn, { code: "timeout", port: 0 }, n)).toBe("de1 does not reach the site of msk1 on 443: a bridge will not help here");
    expect(speedReason(tEn, { status: "fail", code: "timeout", bytes: 3_000_000, mb: 8 }, n)).toContain("3 of 8 MB arrived");
    const fmt = { mbit: (bps: number) => `${bps / 1e6} Mbit/s`, num: (v: number) => String(v) } as never;
    expect(speedOk(tEn, fmt, { mbitS: 31, mb: 8, durationMs: 2100 })).toMatch(/^31 Mbit\/s · 8 MB in 2\.1/);
  });
});

describe("the load of a bridge", () => {
  const load = (clients: number, over: Record<string, unknown> = {}) => bridgeLoad({ clients, capacity: 230, nearFull: false, pool: 2, error: "", ...over } as never);
  it("is nearly full from 80 % and at its limit at 100 % or when an agent says the bridge turns people away", () => {
    expect(load(120)).toMatchObject({ percent: 52, nearFull: false, atLimit: false, segments: 6 });
    expect(load(183)).toMatchObject({ nearFull: false });
    expect(load(184)).toMatchObject({ percent: 80, nearFull: true, atLimit: false });
    expect(load(120, { nearFull: true })).toMatchObject({ nearFull: true }); // the panel's own word counts too
    expect(load(230)).toMatchObject({ percent: 100, nearFull: false, atLimit: true, segments: 10 });
    expect(load(100, { error: "queue_full" })).toMatchObject({ atLimit: true });
    expect(load(100, { error: "pair_timeout" })).toMatchObject({ atLimit: false });
  });
  it("has nothing to compare without a capacity", () => {
    expect(bridgeLoad(undefined)).toBeNull();
    expect(bridgeLoad({ clients: 5, capacity: 0, nearFull: false, pool: 0, error: "" })).toBeNull();
  });
});

describe("the note of a server in the dialog about the bridge", () => {
  const c = (over: Record<string, unknown> = {}) => ({ reverseBlocked: "", network: "tcp", reach: { verdict: "blocked" }, exitReachesEntry: { verdict: "reach" }, ...over });
  it("says a bridge is possible when direct does not pass and the exit reaches the entry", () => {
    expect(bridgeNote(tEn, c(), n)).toEqual({ tone: "ok", text: "Direct does not pass, but a bridge is possible: de1 reaches msk1" });
    expect(bridgeNote(tEn, c({ reach: { verdict: "partial" } }), n)?.tone).toBe("ok");
  });
  it("says nothing while direct passes, says when neither passes", () => {
    expect(bridgeNote(tEn, c({ reach: { verdict: "reach" } }), n)).toBeNull();
    expect(bridgeNote(tEn, c({ exitReachesEntry: { verdict: "no_data" } }), n)).toBeNull();
    expect(bridgeNote(tEn, c({ exitReachesEntry: { verdict: "blocked" } }), n)).toEqual({ tone: "bad", text: "Neither direct nor a bridge passes: msk1 and de1 do not reach each other" });
  });
  it("never offers a bridge to a UDP server, and names the agent when it is the agent", () => {
    expect(bridgeNote(tEn, c({ network: "udp", reverseBlocked: "udp" }), n)).toEqual({ tone: "off", text: "Direct only: UDP does not go over a bridge yet" });
    expect(bridgeNote(tEn, c({ network: "udp", reverseBlocked: "" }), n)?.text).toContain("UDP");
    expect(bridgeNote(tEn, c({ reverseBlocked: "exit_agent_old" }), n)).toEqual({ tone: "warn", text: "A bridge is not available: update the agent of de1." });
  });
});

describe("the state of a relay that may use a bridge", () => {
  const on = { enabled: true, state: InboundState.ACTIVE };
  it("is not down for a failing direct leg while it stands in the bridge, and is down when the bridge does not hold", () => {
    const reverse = relay({ ...on, activeMode: "reverse", modeReason: "leg_failed", check: check({ ...legDown, bridge: stage({ code: "" }) }) });
    expect(relayState(reverse)).toBe("on");
    expect(bridgeCarries(reverse)).toBe(true);
    const broken = relay({ ...on, activeMode: "reverse", check: check({ ...legDown, bridge: stage({ status: "fail", code: "connect" }) }) });
    expect(relayState(broken)).toBe("failed");
    expect(checkReason(tEn, broken)).toBe("de1 cannot connect to msk1");
    // the same leg on a direct relay is a failure
    expect(relayState(relay({ ...on, check: check(legDown) }))).toBe("failed");
    // a path that fails on the bridge is a failure too
    expect(relayState(relay({ ...on, activeMode: "reverse", check: check({ ok: false, stage: "path", path: stage({ status: "fail", code: "timeout" }) }) }))).toBe("failed");
  });

  it("is off, not down, for a relay that is off whose leg fails but whose exit reaches the bridge; down when direct is all it may use", () => {
    const reach = stage({ verdict: "ours", port: 443 });
    expect(relayState(relay({ check: check({ ...legDown, reverseReach: reach }) }))).toBe("off");
    expect(relayState(relay({ mode: "dnat", check: check({ ...legDown, reverseReach: reach }) }))).toBe("failed");
    expect(relayState(relay({ network: "udp", reverseBlocked: "udp", check: check({ ...legDown, reverseReach: reach }) }))).toBe("failed");
    expect(relayState(relay({ check: check({ ...legDown, reverseReach: stage({ status: "fail", code: "timeout" }) }) }))).toBe("failed");
  });

  it("says when neither works, with the leg's reason and the bridge's", () => {
    const r = relay({ check: check({ ...legDown, reverseReach: stage({ status: "fail", code: "timeout" }) }) });
    expect(checkReason(tEn, r)).toBe("msk1 cannot reach de1 on 443/tcp; a bridge does not help either: de1 does not reach the site of msk1 on 443: a bridge will not help here");
    // direct only: the bridge is not asked, not mentioned
    expect(checkReason(tEn, { ...r, mode: "dnat" })).toBe("msk1 cannot reach de1 on 443/tcp");
    expect(checkReason(tEn, relay({ check: check({ ok: false, stage: "reverse_reach", reverseReach: stage({ status: "fail", code: "other", port: 8443 }) }), mode: "reverse" }))).toBe("on 8443 of msk1 something other than its site answers: check what holds the port");
  });

  it("can be turned on by the bridge's check, by mode: auto when the leg fails and the bridge passes, reverse by the bridge alone", () => {
    const reach = (over: Record<string, unknown> = {}) => stage({ verdict: "ours", port: 443, ...over });
    // auto: the leg passes -> as before; fails with a fresh bridge pass -> on; fails alone -> failed
    expect(enableGate(relay({ check: check() }), now)).toBe("");
    expect(enableGate(relay({ check: check({ ...legDown, reverseReach: reach() }) }), now)).toBe("");
    expect(enableGate(relay({ check: check({ ...legDown, reverseReach: reach({ atUnix: now - 20 * 60 }) }) }), now)).toBe("failed");
    expect(enableGate(relay({ check: check(legDown) }), now)).toBe("failed");
    // direct only: the bridge's pass does not count
    expect(enableGate(relay({ mode: "dnat", check: check({ ...legDown, reverseReach: reach() }) }), now)).toBe("failed");
    // bridge only: the leg does not matter, the bridge's check does
    expect(enableGate(relay({ mode: "reverse", check: check() }), now)).toBe("unchecked");
    expect(enableGate(relay({ mode: "reverse", check: check({ reverseReach: reach() }) }), now)).toBe("");
    expect(enableGate(relay({ mode: "reverse", check: check({ reverseReach: reach({ atUnix: now - 16 * 60 }) }) }), now)).toBe("stale");
    expect(enableGate(relay({ mode: "reverse", check: check({ reverseReach: reach({ status: "fail", code: "timeout" }) }) }), now)).toBe("failed");
    // a UDP server has no bridge to pass
    expect(enableGate(relay({ network: "udp", reverseBlocked: "udp", check: check({ ...legDown, network: "udp", reverseReach: reach() }) }), now)).toBe("failed");
  });
});
