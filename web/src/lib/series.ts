// Overview series: hourly points with one value per protocol. The charts want dense arrays, a stable protocol
// order (the first protocol takes the accent, the rest the secondary tone) and, for the 7-day view, local days.

export type Series = {
  /** Start of each bucket, unix seconds. */
  times: number[];
  /** Protocol ids in drawing order. */
  protocols: string[];
  /** One dense array per protocol, same length as `times`. */
  values: Record<string, number[]>;
  /** Sum over protocols. */
  total: number[];
};

const protocolOrder = ["hysteria2", "amneziawg", "awg"];
const protocolNames: Record<string, string> = { hysteria2: "Hysteria2", amneziawg: "AmneziaWG", awg: "AmneziaWG" };

/** Display name of a protocol plugin id: "hysteria2" -> "Hysteria2"; an unknown id is shown as it is. */
export const protocolName = (id: string) => protocolNames[id] ?? id;

const protocolShorts: Record<string, string> = { hysteria2: "HY2", amneziawg: "AWG", awg: "AWG" };

/** The short tag of a protocol for chips and narrow cells, as the owner writes it himself: "HY2", "AWG"; an unknown id is shown upper-case, whole. */
export const protocolShort = (id: string) => protocolShorts[id] ?? id.toUpperCase();

const rank = (p: string) => {
  const i = protocolOrder.indexOf(p);
  return i < 0 ? protocolOrder.length : i;
};

export const sortProtocols = (ids: Iterable<string>) =>
  [...new Set(ids)].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));

/** Hourly points as the overview returns them (after plain()). */
export type Point = { startUnix: number; values: readonly { protocol: string; value: number }[] };

export function toSeries(points: readonly Point[]): Series {
  const protocols = sortProtocols(points.flatMap((p) => p.values.map((v) => v.protocol)));
  const values: Record<string, number[]> = Object.fromEntries(protocols.map((p) => [p, new Array<number>(points.length).fill(0)]));
  const total = new Array<number>(points.length).fill(0);
  points.forEach((pt, i) => {
    for (const v of pt.values) {
      values[v.protocol]![i] = v.value;
      total[i]! += v.value;
    }
  });
  return { times: points.map((p) => p.startUnix), protocols, values, total };
}

/** The last `n` buckets. */
export function tail(s: Series, n: number): Series {
  const from = Math.max(0, s.times.length - n);
  return {
    times: s.times.slice(from),
    protocols: s.protocols,
    values: Object.fromEntries(s.protocols.map((p) => [p, s.values[p]!.slice(from)])),
    total: s.total.slice(from),
  };
}

const dayKey = (d: Date) => d.getFullYear() * 10000 + d.getMonth() * 100 + d.getDate();

/**
 * Folds hourly buckets into the last `days` local calendar days (today included). Traffic adds up ("sum"),
 * online people take the day's peak ("max"). Hours older than the window are dropped; days without data are zero.
 */
export function foldDays(s: Series, mode: "sum" | "max", days = 7, now = new Date()): Series {
  const starts: Date[] = [];
  for (let i = days - 1; i >= 0; i--) starts.push(new Date(now.getFullYear(), now.getMonth(), now.getDate() - i));
  const index = new Map(starts.map((d, i) => [dayKey(d), i]));
  const fold = (src: number[]) => {
    const out = new Array<number>(days).fill(0);
    src.forEach((v, i) => {
      const at = index.get(dayKey(new Date(s.times[i]! * 1000)));
      if (at === undefined) return;
      out[at] = mode === "sum" ? out[at]! + v : Math.max(out[at]!, v);
    });
    return out;
  };
  return {
    times: starts.map((d) => Math.floor(d.getTime() / 1000)),
    protocols: s.protocols,
    values: Object.fromEntries(s.protocols.map((p) => [p, fold(s.values[p]!)])),
    total: fold(s.total),
  };
}

/** Smallest "nice" number (1, 2, 2.5, 5, 10 x 10^k) that is >= v. */
export function niceCeil(v: number): number {
  if (!(v > 0)) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}

/** SVG path through the points with cubic segments whose control points sit at the midpoints in x. */
export function smoothPath(xs: readonly number[], ys: readonly number[]): string {
  if (xs.length === 0) return "";
  let d = `M${r(xs[0]!)},${r(ys[0]!)}`;
  for (let i = 1; i < xs.length; i++) {
    const cx = (xs[i - 1]! + xs[i]!) / 2;
    d += ` C${r(cx)},${r(ys[i - 1]!)} ${r(cx)},${r(ys[i]!)} ${r(xs[i]!)},${r(ys[i]!)}`;
  }
  return d;
}

const r = (n: number) => Math.round(n * 100) / 100;
