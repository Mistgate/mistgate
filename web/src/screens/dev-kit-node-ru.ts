import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";

// Development only: the country checks the node page's Overview card ends with (the board's nd-ru), one scene per state and the long ones
// of live data (many networks, long names of networks and probes, a result days old). /dev-kit?only=nodepage-frame&tab=overview&ru=<kind>.
// Made-up data: documentation networks, operators that do not exist, probes "dacha" and "office", the probe node msk1.

export type RuKind = "ok" | "partial" | "tcp" | "bad" | "listed" | "unknown" | "none" | "probe" | "points" | "conflict" | "old" | "long";
export const ruKinds: readonly RuKind[] = ["ok", "partial", "tcp", "bad", "listed", "unknown", "none", "probe", "points", "conflict", "old", "long"];
export const ruKindOf = (s: string | null): RuKind | undefined => ruKinds.find((k) => k === s);

const MIN = 60;
const H = 3600;

const gpProbe = (network: string, city: string, valid = true, icmp = true, tcp = true) => ({
  network, city, asn: 0, valid, dark: false, icmpOk: icmp, icmpMs: icmp ? 38 : 0, tcp: [{ port: 443, ok: tcp }],
});
const gp = (now: number, network: string, state: string, region = "", asn = "") => ({
  source: "globalping", key: `gp:${network}`, network, region, asn, kind: "home", state, atUnix: now - 6 * MIN, dpiHop: 0, icmpOk: state === "reach", tcpOk: state === "reach", why: "", probeName: "", port: 0,
});
const own = (now: number, name: string, network: string, state: string, why: string, ago: number, extra: object = {}) => ({
  source: "probes", key: `probe:${name}`, network, region: "", asn: "", kind: "home", state, atUnix: now - ago, dpiHop: 0, icmpOk: false, tcpOk: state === "reach", why, probeName: name, port: state === "dark" ? 443 : 0, ...extra,
});

/** The country checks of the node `nod_de1` for a scene (and a row of another node, so that the column exists where this one has none). */
export function ruChecksOf(kind: RuKind, now: number) {
  const base = (verdict: CountryVerdict, over: object = {}) => ({
    nodeId: "nod_de1", country: "RU", verdict, atUnix: now - 9 * MIN, probes: [] as object[], reason: "", eyeball: true, confidence: "eyeball", listedBy: [] as string[], cdnProvider: "", cdnRegion: "",
    sources: ["globalping"], globalpingVerdict: verdict, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [] as object[], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [] as string[],
    vantages: [] as object[], sourceVerdicts: [] as object[], ...over,
  });
  const four = [gpProbe("Operator A", "Moscow"), gpProbe("Operator B", "Kazan"), gpProbe("Operator C", "Perm"), gpProbe("Operator D", "Omsk")];
  const other = base(CountryVerdict.OK, { nodeId: "nod_msk1" });
  switch (kind) {
    case "ok":
      return [base(CountryVerdict.OK, { probes: four })];
    case "partial":
      return [
        base(CountryVerdict.PARTIAL, {
          probes: four,
          vantages: [gp(now, "Operator A", "dark", "Moscow", "64496"), gp(now, "Operator B", "reach", "Kazan", "64497"), gp(now, "Operator C", "reach", "Perm", "64498"), gp(now, "Operator D", "reach", "Omsk", "64499")],
        }),
      ];
    case "tcp":
      return [base(CountryVerdict.TCP_BLOCKED, { probes: four.map((p) => ({ ...p, tcp: [{ port: 443, ok: false }] })) })];
    case "bad":
      return [base(CountryVerdict.BLOCKED, { probes: four.map((p) => ({ ...p, icmpOk: false, icmpMs: 0, tcp: [{ port: 443, ok: false }] })) })];
    case "listed":
      return [base(CountryVerdict.LISTED, { listedBy: ["antifilter_ipsum"], sources: ["lists"], confidence: "listed", cdnProvider: "Example Cloud", cdnRegion: "DE" })];
    case "unknown":
      return [base(CountryVerdict.UNKNOWN, { reason: "too_few_probes", probes: [gpProbe("Operator A", "Moscow")], confidence: "" })];
    case "none":
      return [other];
    case "probe":
      // the owner's screenshot: the networks, the probe node, the registry, when
      return [
        base(CountryVerdict.OK, {
          probes: [gpProbe("Operator A", "Moscow"), gpProbe("Operator B", "Kazan")], listedBy: ["antifilter_ipsum"], sources: ["globalping", "tspu"], confidence: "eyeball", tspuVerdict: CountryVerdict.OK,
          tspuPorts: [{ port: 443, verdict: "open" }], tspuVantageName: "msk1", tspuAtUnix: now - 34 * MIN, atUnix: now - 34 * MIN,
        }),
      ];
    case "points":
      // two own probes: one reaches, one has no clear answer (the line of the owner's screenshot)
      return [
        base(CountryVerdict.OK, {
          probes: four, sources: ["globalping", "probes"],
          vantages: [
            gp(now, "Operator A", "reach", "Moscow", "64496"),
            own(now, "dacha", "AS64500 · Antalya", "uncertain", "", 5 * H),
            own(now, "office", "Operator E · Perm", "reach", "", 12 * MIN),
          ],
        }),
      ];
    case "conflict":
      return [
        base(CountryVerdict.TCP_BLOCKED, {
          probes: four, sources: ["globalping", "tspu"], confidence: "conflict", globalpingVerdict: CountryVerdict.OK, tspuVerdict: CountryVerdict.TCP_BLOCKED,
          tspuPorts: [{ port: 443, verdict: "blocked" }], tspuVantageName: "msk1", tspuAtUnix: now - 40 * MIN,
        }),
      ];
    case "old":
      return [base(CountryVerdict.OK, { probes: four, atUnix: now - 3 * 86400 - 5 * H })];
    case "long": {
      const names = ["Example Telecom of the Central Federal District", "Operator Mobile Communications Limited", "Operator A", "Operator B", "Operator C", "Operator D", "Operator E", "Operator F", "Operator G", "Operator H", "Operator I", "Operator J"];
      return [
        base(CountryVerdict.PARTIAL, {
          probes: names.map((n, i) => gpProbe(n, "Moscow", i % 3 !== 2)),
          listedBy: ["antifilter_ipsum", "antifilter_community"], cdnProvider: "Example Hosting International Limited", cdnRegion: "DE", sources: ["globalping", "tspu", "probes"], tspuVerdict: CountryVerdict.OK,
          tspuPorts: [{ port: 443, verdict: "open" }, { port: 8443, verdict: "open" }, { port: 2053, verdict: "timeout" }, { port: 10819, verdict: "blocked" }], tspuVantageName: "msk1-edge-probe-node-with-a-long-name", tspuAtUnix: now - 50 * MIN, dpiHop: 7,
          noProbe: ["Operator Long Name Mobile", "Another Mobile Operator", "Third One"],
          vantages: [
            ...names.slice(0, 9).map((n, i) => gp(now, n, i < 4 ? "dark" : "reach", ["Moscow", "Kazan", "Novosibirsk", "Yekaterinburg"][i % 4], String(64500 + i))),
            own(now, "dacha-in-the-village-with-a-long-name", "Example Telecom of the Central Federal District · Antalya", "dark", "tls_reset", 2 * H),
            own(now, "office", "Operator E · Perm", "uncertain", "unknown", 5 * H),
            own(now, "flat", "Operator Mobile Communications Limited · Saint Petersburg", "reach", "", 20 * MIN),
          ],
        }),
      ];
    }
  }
}

// ---------------------------------------------------------------------------------------------------
// The round "Check now" asks of the country check for the node (health.proto CountryNow), one scene per state: the radar and the lines under
// "From Russia". /dev-kit?only=nodepage-frame&tab=overview&ru=ok&cn=<kind> (also tab=ways and the Reachability page). Made-up numbers.

export type NowKind = "running" | "done" | "early" | "nobudget" | "failed" | "off" | "partial";
export const nowKinds: readonly NowKind[] = ["running", "done", "early", "nobudget", "failed", "off", "partial"];
export const nowKindOf = (s: string | null): NowKind | undefined => nowKinds.find((k) => k === s);

type Src = [source: string, state: string, reason?: string];
const sourcesOf = (rows: Src[]) => rows.map(([source, state, reason]) => ({ source, state, reason: reason ?? "" }));

/** The round of the node `nod_de1` for a scene. */
export function nowRoundOf(kind: NowKind, now: number, nodeId = "nod_de1") {
  const base = { nodeId, running: false, startedUnix: now - 3 * MIN, nextUnix: now + 7 * MIN, retryAfterS: 0, askedUnix: now - 3 * MIN, globalpingLeft: 140, globalpingCost: 12, globalpingAtUnix: now - 3 * MIN };
  const rest: Src[] = [["bsbord", "skipped", "schedule_only"], ["lists", "reread"], ["klick", "reread"]];
  switch (kind) {
    case "running":
      return [{ ...base, running: true, startedUnix: now - 20, askedUnix: now - 20, nextUnix: now + 580, globalpingAtUnix: now - 20,
        sources: sourcesOf([["globalping", "running"], ["tspu", "running"], ["cheburcheck", "running"], ["cheburcheck_own", "skipped", "off"], ["probes", "running"], ...rest]) }];
    case "done":
      return [{ ...base, sources: sourcesOf([["globalping", "done"], ["tspu", "done"], ["cheburcheck", "done"], ["cheburcheck_own", "skipped", "off"], ["probes", "queued"], ...rest]) }];
    case "early":
      return [{ ...base, startedUnix: now - 3 * MIN, nextUnix: now + 7 * MIN, retryAfterS: 7 * MIN,
        sources: sourcesOf([["globalping", "skipped", "too_early"], ["tspu", "skipped", "too_early"], ["cheburcheck", "skipped", "too_early"], ["cheburcheck_own", "skipped", "too_early"], ["probes", "skipped", "too_early"], ["bsbord", "skipped", "too_early"], ["lists", "skipped", "too_early"], ["klick", "skipped", "too_early"]]) }];
    case "nobudget":
      return [{ ...base, globalpingLeft: 4, sources: sourcesOf([["globalping", "skipped", "no_budget"], ["tspu", "done"], ["cheburcheck", "done"], ["cheburcheck_own", "skipped", "off"], ["probes", "queued"], ...rest]) }];
    case "failed":
      return [{ ...base, sources: sourcesOf([["globalping", "done"], ["tspu", "failed", "control_failed"], ["cheburcheck", "failed", "timeout"], ["cheburcheck_own", "skipped", "held"], ["probes", "skipped", "probe_silent"], ...rest]) }];
    case "off":
      // nothing could be asked: no round, no time of a round
      return [{ nodeId, running: false, startedUnix: 0, nextUnix: 0, retryAfterS: 0, askedUnix: now - 60, globalpingLeft: undefined, globalpingCost: 0, globalpingAtUnix: 0,
        sources: sourcesOf([["globalping", "skipped", "off"], ["tspu", "skipped", "probe_node"], ["cheburcheck", "skipped", "off"], ["cheburcheck_own", "skipped", "off"], ["probes", "skipped", "no_probe"], ["bsbord", "skipped", "off"], ["lists", "skipped", "off"], ["klick", "reread"]]) }];
    case "partial":
      return [{ ...base, running: true, startedUnix: now - 70, askedUnix: now - 70, nextUnix: now + 530, globalpingAtUnix: now - 70,
        sources: sourcesOf([["globalping", "done"], ["tspu", "running"], ["cheburcheck", "done"], ["cheburcheck_own", "skipped", "held"], ["probes", "queued"], ...rest]) }];
  }
}
