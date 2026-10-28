import { describe, expect, it } from "vitest";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { dictionaries, fill, type Lang, type T } from "@/i18n";
import { agentError, checkReason, countryHint, enableGate, groupCandidates, reachHint, relayState, seenHint, stageReason, type Candidate, type RelayMsg } from "./relay";

const tOf = (lang: Lang) => Object.assign((key: string, vars?: Record<string, string | number>) => fill(dictionaries[lang][key as "relay.title"], vars), { n: () => "" }) as unknown as T;
const tRu = tOf("ru");
const tEn = tOf("en");
const now = 1_800_000_000;

const stage = (over: Record<string, unknown> = {}) => ({ status: "ok", code: "", senderId: "", senderName: "", verdict: "", sent: 0, got: 0, atUnix: now - 60, ...over });
const check = (over: Record<string, unknown> = {}) => ({ atUnix: now - 60, network: "tcp", port: 8443, to: "203.0.113.7:443", leg: stage(), path: stage({ status: "skipped", code: "off" }), ok: true, stage: "", verified: false, ...over });
const relay = (over: Partial<RelayMsg> = {}): RelayMsg =>
  ({
    id: "rly_1", inboundId: "inb_1", exitNodeId: "nod_de1", exitNodeName: "de1", profileId: "prf_1", profileName: "VLESS · TCP", protocol: "vless", viaNodeId: "nod_msk1", viaNodeName: "msk1",
    network: "tcp", port: 8443, exitPort: 443, enabled: false, state: InboundState.PENDING, lastError: "", toIp: "", appliedUnix: 0, exitLive: true, agentSupported: true, createdUnix: now - 3600, check: undefined,
    ...over,
  }) as RelayMsg;

describe("the state of a relay", () => {
  it("is on when the entry holds the forward, off when switched off, failed when the agent or the last check says so", () => {
    expect(relayState(relay({ enabled: true, state: InboundState.ACTIVE, check: check({ verified: true, path: stage() }) as never }))).toBe("on");
    expect(relayState(relay({ enabled: true, state: InboundState.PENDING }))).toBe("applying");
    expect(relayState(relay())).toBe("off");
    expect(relayState(relay({ check: check() as never }))).toBe("off"); // a passing leg on a relay that is off: still off
    expect(relayState(relay({ enabled: true, state: InboundState.FAILED, lastError: "forward_drop" }))).toBe("failed");
    // switched off again by its own failed path check: down, not merely off
    expect(relayState(relay({ check: check({ ok: false, stage: "path", path: stage({ status: "fail", code: "timeout" }) }) as never }))).toBe("failed");
  });

  it("can be turned on only after a passing leg check under 15 minutes old, for its network, with the agent and the exit able", () => {
    expect(enableGate(relay(), now)).toBe("unchecked");
    expect(enableGate(relay({ check: check() as never }), now)).toBe("");
    expect(enableGate(relay({ check: check({ leg: stage({ atUnix: now - 16 * 60 }) }) as never }), now)).toBe("stale");
    expect(enableGate(relay({ check: check({ network: "udp" }) as never }), now)).toBe("stale"); // checked for another target
    expect(enableGate(relay({ check: check({ ok: false, leg: stage({ status: "fail", code: "timeout" }) }) as never }), now)).toBe("failed");
    expect(enableGate(relay({ agentSupported: false, check: check() as never }), now)).toBe("agent_old");
    expect(enableGate(relay({ exitLive: false, check: check() as never }), now)).toBe("exit_down");
  });
});

describe("why a check failed", () => {
  const n = { entry: "msk1", exit: "de1", exitPort: 443, port: 8443, network: "tcp" };
  it("names the leg by the exit's port and the path by the entry's, in plain words", () => {
    expect(stageReason(tRu, "leg", stage({ code: "timeout" }), n)).toBe("msk1 не достаёт de1 по 443/tcp");
    expect(stageReason(tRu, "path", stage({ code: "refused" }), n)).toBe("8443/tcp на msk1 недоступен снаружи");
    expect(stageReason(tEn, "leg", stage({ code: "no_route" }), n)).toBe("msk1 cannot reach de1 on 443/tcp");
    expect(stageReason(tEn, "path", stage({ code: "timeout" }), n)).toBe("8443/tcp on msk1 is not reachable from outside");
  });
  it("has its own words for an answer that is not ours, lost UDP packets and the codes that are not about the network", () => {
    expect(stageReason(tRu, "path", stage({ code: "other", verdict: "other" }), n)).toBe("на 8443/tcp у msk1 отвечает не сервер de1");
    expect(stageReason(tRu, "leg", stage({ code: "lossy", sent: 300, got: 240 }), { ...n, network: "udp" })).toBe("msk1 → de1 по 443/udp: дошло 240 из 300 пакетов");
    expect(stageReason(tRu, "leg", stage({ code: "node_offline" }), n)).toBe("msk1 не на связи");
    expect(stageReason(tRu, "path", stage({ code: "apply_timeout" }), n)).toBe("msk1 не поставил проброс за 30 секунд");
    expect(stageReason(tEn, "path", stage({ code: "no_sender" }), n)).toBe("no other node can check it from outside");
    expect(stageReason(tEn, "leg", stage({ code: "brand_new" }), n)).toBe("msk1 cannot reach de1 on 443/tcp"); // a code from a newer panel
  });
  it("reads the failed stage of the stored check, nothing for a check that passed", () => {
    const failed = relay({ check: check({ ok: false, stage: "path", path: stage({ status: "fail", code: "timeout" }) }) as never });
    expect(checkReason(tRu, failed)).toBe("8443/tcp на msk1 недоступен снаружи");
    expect(checkReason(tRu, relay({ check: check() as never }))).toBe("");
    expect(checkReason(tRu, relay())).toBe("");
  });
  it("words what the entry's agent said", () => {
    const names = { entry: "msk1", exit: "de1" };
    expect(agentError(tRu, "forward_drop", names)).toBe("Фаервол сервера отбрасывает пересылаемый трафик (политика FORWARD DROP). Разрешите пересылку и проверьте снова.");
    expect(agentError(tRu, "resolve: no such host", names)).toBe("msk1 не смог узнать адрес de1.");
    expect(agentError(tEn, "something new", names)).toBe("msk1 could not install the forward.");
  });
});

describe("the hints of a candidate", () => {
  it("say where the exit is dark, whether the entry reaches it and whether people see the entry there; no data is said, never guessed", () => {
    expect(countryHint(tRu, "RU", { verdict: "partial", dark: ["MTS", "Tele2"], atUnix: now })).toEqual({ tone: "warn", text: "Из RU: заблокирована у MTS, Tele2" });
    expect(countryHint(tRu, "RU", { verdict: "blocked", dark: [], atUnix: now })).toEqual({ tone: "bad", text: "Из RU: заблокирована" });
    expect(countryHint(tRu, "RU", { verdict: "no_data", dark: [], atUnix: 0 }).text).toBe("Из RU: нет данных проверки");
    expect(reachHint(tRu, "msk1", "de1", { verdict: "reach", source: "tspu_row", atUnix: now }).text).toBe("msk1 → de1 проходит (проверка ТСПУ)");
    expect(reachHint(tRu, "msk1", "de1", { verdict: "reach", source: "refs_pass", atUnix: now }).text).toBe("На пути msk1 ТСПУ не видно");
    expect(reachHint(tRu, "msk1", "de1", { verdict: "blocked", source: "tspu_row", atUnix: now })).toEqual({ tone: "bad", text: "msk1 → de1 заблокирован (проверка ТСПУ)" });
    expect(reachHint(tRu, "msk1", "de1", { verdict: "no_data", source: "", atUnix: 0 }).text).toBe("Нет данных проверки с msk1");
    const exit = { verdict: "partial", dark: ["MTS", "Tele2"], atUnix: now };
    expect(seenHint(tRu, "msk1", exit, { verdict: "ok", dark: [], atUnix: now })).toEqual({ tone: "ok", text: "msk1 видна из MTS, Tele2" });
    expect(seenHint(tRu, "msk1", exit, { verdict: "partial", dark: ["Tele2"], atUnix: now })).toEqual({ tone: "bad", text: "msk1 тоже не видна из Tele2" });
    expect(seenHint(tRu, "msk1", exit, { verdict: "no_data", dark: [], atUnix: 0 })?.text).toBe("Видна ли msk1 людям: нет данных");
    expect(seenHint(tRu, "msk1", { verdict: "ok", dark: [], atUnix: now }, undefined)).toBeNull(); // an exit seen everywhere: nothing to compare
  });

  it("put the cut exits with a server that can be relayed first, keeping the panel's order", () => {
    const c = (nodeId: string, over: Partial<Candidate> = {}) => ({ inboundId: `inb_${nodeId}_${over.profileName ?? "a"}`, nodeId, nodeName: nodeId, profileName: "a", unsupported: "", country: { verdict: "ok", dark: [], atUnix: 0 }, ...over }) as Candidate;
    const cut = { verdict: "partial", dark: ["MTS"], atUnix: now };
    const g = groupCandidates([c("de1", { country: cut }), c("de1", { profileName: "b", country: cut, unsupported: "cdn" }), c("fi1"), c("nl1", { country: cut, unsupported: "acme_ip" })]);
    expect(g.suggested.map((x) => [x.nodeId, x.servers.length])).toEqual([["de1", 2]]);
    expect(g.others.map((x) => x.nodeId)).toEqual(["fi1", "nl1"]); // nl1 is cut, but nothing of it can be relayed
  });
});
