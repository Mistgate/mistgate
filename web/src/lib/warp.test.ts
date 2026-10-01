import { describe, expect, it } from "vitest";
import { WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { warpGaps } from "@/screens/profiles/egress-note";
import { ageParts, looksLikeWgcfProfile, probeLook, safeHttpUrl, warpKind, warpPill, warpTrouble, warpWord } from "./warp";

describe("warp helpers", () => {
  it("only plain http(s) links reach an anchor", () => {
    expect(safeHttpUrl("https://www.cloudflare.com/application/terms/")).toBe("https://www.cloudflare.com/application/terms/");
    expect(safeHttpUrl("javascript:alert(1)")).toBe("");
    expect(safeHttpUrl("https://a b")).toBe("");
    expect(safeHttpUrl("")).toBe("");
  });
  it("tells a wgcf profile from anything else", () => {
    const ok = "[Interface]\nPrivateKey = abc\nAddress = 172.16.0.2/32\n[Peer]\nPublicKey = def\n";
    expect(looksLikeWgcfProfile(ok)).toBe(true);
    expect(looksLikeWgcfProfile("[Interface]\nAddress = 10.0.0.2/32\n")).toBe(false);
    expect(looksLikeWgcfProfile("access_token = 'x'")).toBe(false);
  });
  it("ages in the coarsest unit", () => {
    expect(ageParts(0, 100)).toBeNull();
    expect(ageParts(9990, 10000)).toEqual({ unit: "s", n: 10 });
    expect(ageParts(10000 - 300, 10000)).toEqual({ unit: "m", n: 5 });
    expect(ageParts(100000 - 7200, 100000)).toEqual({ unit: "h", n: 2 });
    expect(ageParts(1000000 - 3 * 86400, 1000000)).toEqual({ unit: "d", n: 3 });
  });
  it("every state has a pill kind and a word", () => {
    for (const s of Object.values(WarpState).filter((v): v is WarpState => typeof v === "number")) {
      expect(warpKind[s]).toBeTruthy();
      expect(warpWord[s]).toBeTruthy();
    }
  });
});

describe("the pill agrees with the latest check", () => {
  it("never shows a spinner or a green tick while the last check fails", () => {
    expect(warpPill(WarpState.STARTING, false)).toEqual({ kind: "busy", word: "warp.state.starting" });
    expect(warpPill(WarpState.STARTING, true)).toEqual({ kind: "warn", word: "warp.state.startingFailing" });
    expect(warpPill(WarpState.UP, true)).toEqual({ kind: "warn", word: "warp.state.upFailing" });
    expect(warpPill(WarpState.UP, false)).toEqual({ kind: "ok", word: "warp.state.up" });
    expect(warpPill(WarpState.DOWN, true)).toEqual({ kind: "bad", word: "warp.state.down" });
  });
});

describe("probeLook (the dots show the latest check)", () => {
  it("a failed probe is a red dot and 'no answer', not 'ok', whatever the flag of an older check said", () => {
    expect(probeLook({ ok: false, latencyMs: 6000 }, true, true)).toEqual({ kind: "bad", word: "warp.probe.fail", ms: null });
  });
  it("a passing probe shows its latency; one over 2 s is slow and warn", () => {
    expect(probeLook({ ok: true, latencyMs: 420 }, true, true)).toEqual({ kind: "ok", word: "warp.probe.ok", ms: 420 });
    expect(probeLook({ ok: true, latencyMs: 2000 }, true, true).kind).toBe("ok");
    expect(probeLook({ ok: true, latencyMs: 3200 }, true, true)).toEqual({ kind: "warn", word: "warp.probe.slow", ms: 3200 });
  });
  it("a probe the round did not run claims nothing", () => {
    expect(probeLook(undefined, false, true)).toEqual({ kind: "off", word: null, ms: null });
    expect(probeLook(undefined, undefined, false).kind).toBe("off");
  });
  it("an agent without per-probe results falls back to the flags, with no timing", () => {
    expect(probeLook(undefined, true, false)).toEqual({ kind: "ok", word: "warp.probe.ok", ms: null });
    expect(probeLook(undefined, false, false)).toEqual({ kind: "bad", word: "warp.probe.fail", ms: null });
  });
});

describe("warpGaps (the note under Exit: WARP)", () => {
  const nodes = [
    { id: "a", name: "de1", warp: { state: WarpState.UP } },
    { id: "b", name: "fr1", warp: { state: WarpState.NOT_CONFIGURED } },
    { id: "c", name: "nl1", warp: { state: WarpState.DISABLED } },
    { id: "d", name: "nl1" },
  ];
  it("names the nodes of the profile that cannot give a WARP exit", () => {
    expect(warpGaps(nodes, ["a", "b", "c"]).missing).toEqual(["fr1", "nl1"]);
    expect(warpGaps(nodes, ["a"]).missing).toEqual([]);
  });
  it("a profile on no node yet only asks whether any node has WARP", () => {
    expect(warpGaps(nodes, []).anyNode).toBe(true);
    expect(warpGaps(nodes.slice(1), []).anyNode).toBe(false);
  });
  it("gives the missing nodes with their ids too, for the links to their WARP cards", () => {
    expect(warpGaps(nodes, ["a", "b", "d"]).missingNodes).toEqual([
      { id: "b", name: "fr1" },
      { id: "d", name: "nl1" },
    ]);
  });
});

describe("warpTrouble (what the WARP card offers first)", () => {
  const health = (state: WarpState) => ({ state, lastHandshakeUnix: 1000, consecutiveFailures: 37 });
  const card = (state: WarpState, attentionReason = "", enabled = true) => ({ account: { enabled }, health: health(state), attentionReason });
  it("a revoked account is registered again, whatever the tunnel says", () => {
    expect(warpTrouble(card(WarpState.DOWN, "revoked"))).toEqual({ kind: "revoked" });
    expect(warpTrouble(card(WarpState.UP, "revoked", false))).toEqual({ kind: "revoked" });
  });
  it("a live account with a dead tunnel is restarted: since when, after how many checks", () => {
    expect(warpTrouble(card(WarpState.DOWN))).toEqual({ kind: "down", since: 1000, checks: 37 });
    expect(warpTrouble(card(WarpState.UP, "down_after_ladder"))).toEqual({ kind: "down", since: 1000, checks: 37 });
  });
  it("nothing to fix: working, paused, no report yet, no account", () => {
    expect(warpTrouble(card(WarpState.UP))).toBeNull();
    expect(warpTrouble(card(WarpState.DOWN, "", false))).toBeNull();
    expect(warpTrouble({ account: { enabled: true }, attentionReason: "" })).toBeNull();
    expect(warpTrouble({ attentionReason: "revoked" })).toBeNull();
  });
});
