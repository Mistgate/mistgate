import type { T } from "@/i18n";
import { bridgeFailed, bridgeLoad } from "@/lib/relay-bridge";
import { relayState } from "@/lib/relay";
import type { Direct, FleetWays, NodeWays, WayPair, WayTone } from "@/lib/fleet-map";

// The words of the ways out of the country (lib/fleet-map.ts's fleetWays decides, these say it): the "From Russia" cell of the
// Nodes list, the cells of the Ways view, its summary line. Pure, with the language's `t` handed in, so a test reads them.

/** What the cell of a node says: the glyph's tone, the word, the line under it, the tooltip, and whether "Get around…" is offered. */
export type RuCell = { tone: WayTone; word: string; sub: string; tip: string; noWay: boolean };

/** The state of the data the cell stands on: nothing to say yet, or the relays did not load. */
export type RuState = "ready" | "load" | "error";

const names = (d: Direct) => d.dark.join(", ");

/** What the country sees of a node at its own address, in a word. */
export function directWord(t: T, d: Direct): string {
  if (d.tone === "ok") return t("nw.direct");
  if (d.tone === "part") return d.dark.length > 0 ? t("nw.direct.some", { networks: names(d) }) : t("nw.direct.part");
  if (d.tone === "bad") return t("nw.shut");
  return t("nw.nodata");
}

/** The line about the direct way under a word that is about a way through an entry. */
const directSub = (t: T, d: Direct) => (d.tone === "bad" ? t("nw.shut") : d.tone === "part" ? (d.dark.length > 0 ? t("nw.sub.some", { networks: names(d) }) : t("nw.direct.part")) : "");

/** "through msk1 · bridge and spb1" / "through msk1 +2": the way that carries, and how many more there are. */
export function viaWord(t: T, pairs: readonly WayPair[]): string {
  const lead = pairs[0];
  if (!lead) return "";
  const head = t(lead.bridge > 0 ? "nw.via.bridge" : "nw.via", { entry: lead.entryName });
  return pairs.length === 2 ? head + t("nw.via.and", { entry: pairs[1]!.entryName }) : pairs.length > 2 ? head + t("nw.via.more", { n: pairs.length - 1 }) : head;
}

/** Why a pair is down, in a few words: the relay that failed, and at which stage (a cell has room for one line). */
export function pairWhy(t: T, p: WayPair, exit: string): string {
  const r = p.relays.find((x) => relayState(x) === "failed");
  if (!r) return "";
  const n = { entry: p.entryName, exit };
  if (bridgeFailed(r)) return t("nw.c.why.bridge");
  if (r.check && !r.check.ok && r.check.stage === "path") return t("nw.c.why.path", n);
  return t("nw.c.why.leg", n) + (r.network === "udp" ? t("nw.c.why.udp") : "");
}

/** The cell of a node of the list. null: nothing to say (a node with nothing deployed). */
export function ruCell(t: T, w: NodeWays | undefined, o: { country: string; unmeasured: boolean; state?: RuState; helper?: boolean }): RuCell | null {
  if (!w || w.kind === "idle") return null;
  const cell = (tone: WayTone, word: string, sub = "", tip = ""): RuCell => ({ tone, word, sub, tip: tip || [word, sub].filter(Boolean).join(" · "), noWay: false });
  if (o.state === "load") return cell("none", t("nw.loading"));
  if (o.state === "error") return cell("none", t("nw.relayErr"));
  const stale = w.staleDays > 0 ? t.n("nw.stale", w.staleDays) : "";
  const withStale = (sub: string) => [sub, stale].filter(Boolean).join(" · ");
  // a helper reads no relays: what the country sees of the node, and nothing else
  if (o.helper) return cell(w.direct.tone, directWord(t, w.direct), "");

  const e = w.entry;
  if (w.kind === "entry" && e) {
    const via = e.passes > 0 ? [t("nw.entry.for", { n: e.passes }), e.broken > 0 ? t("nw.entry.broken", { n: e.broken }) : ""].filter(Boolean).join(" · ") : t("nw.entry.none");
    const seen = w.direct.tone === "ok" ? t("nw.entry.seen") : w.direct.tone === "part" ? (w.direct.dark.length > 0 ? t("nw.d.part", { networks: names(w.direct) }) : t("nw.direct.part")) : t("nw.nodata");
    const tip = (word: string) => t("nw.tip.entry", { name: w.name, country: o.country, state: word });
    if (o.unmeasured) return cell("none", t("nw.nodata"), via, tip(t("nw.nodata")));
    if (e.offline) return cell("bad", t("nw.entry.off"), withStale(via), tip(t("nw.entry.off")));
    if (e.hidden) return cell("bad", t("nw.entry.hidden", { country: o.country }), t("nw.entry.hidden.sub"), tip(t("nw.entry.hidden", { country: o.country })));
    if (e.oldAgent) return cell("part", seen, t("nw.entry.old"), tip(seen));
    return cell(w.direct.tone, seen, withStale(via), tip(seen));
  }

  const lead = w.pairs[0];
  if (o.unmeasured) return cell("none", t("nw.nodata"), lead ? t("nw.via", { entry: lead.entryName }) : "");
  switch (w.kind) {
    case "direct": {
      const entries = w.pairs.map((p) => p.entryName).join(", ");
      return cell("ok", t("nw.direct"), withStale(w.pairs.length > 0 ? t("nw.also", { entries }) : t("nw.allNets")));
    }
    case "bypass":
      return cell("ok", viaWord(t, w.pairs), withStale(directSub(t, w.direct)));
    case "partly": {
      // a pair that runs only in part: how many; a pair that runs but is not seen on every network: where it is not
      if (lead && lead.ok < lead.n) return cell("part", t("nw.via.part", { entry: lead.entryName, ok: lead.ok, n: lead.n }), withStale(pairWhy(t, lead, w.name)));
      return cell("part", viaWord(t, w.pairs), withStale(lead && lead.dark.length > 0 ? t("nw.seen.part", { entry: lead.entryName, networks: lead.dark.join(", ") }) : directSub(t, w.direct)));
    }
    case "directPart":
      return cell("part", t("nw.direct.some", { networks: names(w.direct) || "…" }), withStale(""));
    case "noway": {
      const c = cell("bad", t("nw.noway"), withStale(w.busy ? t("nw.turningOn") : directSub(t, w.direct)));
      return { ...c, noWay: w.noWay };
    }
    case "deadEntry":
      return cell("bad", t("nw.noway"), withStale(t(w.deadBy?.why === "offline" ? "nw.dead.off" : "nw.dead.hidden", { entry: w.deadBy?.entry ?? "" })));
    default:
      return cell("none", t("nw.nodata"), "");
  }
}

/** The words of a verdict of the Ways view's first column. */
export const verdictWord = (t: T, tone: WayTone) => t(({ ok: "nw.v.ok", part: "nw.v.part", bad: "nw.v.bad", none: "nw.v.none" } as const)[tone]);

/** What a cell of the Ways view says about a pair (a glyph tone, the word and the line under it). */
export type PairCell = { tone: WayTone | "busy" | "off"; word: string; sub: string };

export function pairCell(t: T, p: WayPair, exit: string, fmt: { stamp: (unix: number) => string }): PairCell {
  const count = t.n("nw.c.count", p.ok);
  if (p.state === "busy") return { tone: "busy", word: t("nw.c.busy"), sub: "" };
  if (p.state === "off") return { tone: "off", word: t("nw.c.off"), sub: t.n("nw.c.count", p.n) };
  if (p.state === "warn") return { tone: "part", word: t("nw.c.partial", { ok: p.ok, n: p.n }), sub: pairWhy(t, p, exit) };
  if (p.state === "bad") return { tone: "bad", word: t("nw.c.down"), sub: pairWhy(t, p, exit) };
  if (p.bridge === 0) return { tone: "ok", word: t("nw.c.fwd"), sub: count };
  // a bridge says how it stands: at its limit, nearly full, or since when it holds; a pair that is bridge for some servers and forward for the rest says TCP
  const enabled = p.relays.filter((r) => r.enabled).length;
  const load = bridgeLoad(p.relays.find((r) => r.enabled && r.bridge && r.activeMode === "reverse")?.bridge);
  const since = Math.max(0, ...p.relays.filter((r) => r.enabled && r.activeMode === "reverse").map((r) => r.modeChangedUnix));
  const tail = load?.atLimit ? t("nw.c.limit") : load?.nearFull ? t("nw.c.nearFull") : p.bridge < enabled ? t("nw.c.tcp") : since > 0 ? t("nw.c.since", { time: fmt.stamp(since) }) : "";
  return { tone: load?.atLimit ? "part" : "ok", word: t("nw.c.bridge"), sub: count + tail };
}

/** The line under the title of the Ways view: how many are reached, how many without a way, the relays and the ones that fail. */
export function waysSummary(t: T, f: FleetWays, o: { country: string; deadEntry?: { name: string; why: "offline" | "hidden" } }): string {
  // an entry that does not work is said once, in place of the counts
  if (o.deadEntry) return t(o.deadEntry.why === "offline" ? "nw.sum.entoff" : "nw.sum.enthid", { country: o.country, entry: o.deadEntry.name, n: f.relayN });
  const head = t("nw.sum.reach", { country: o.country, ok: f.reachN, total: f.exits.length });
  const noway = f.noWayN > 0 ? ` · ${t("nw.sum.nopath", { n: f.noWayN })}` : "";
  const fail = f.downN > 0 ? `, ${t.n("nw.sum.fail", f.downN)}` : "";
  return `${head}${noway} · ${t.n("nw.sum.relays", f.relayN)}${fail}`;
}
