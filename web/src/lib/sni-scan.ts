import { queryOptions } from "@tanstack/react-query";
import { probe } from "@/lib/api";

// "Sites near the node" (screens/node/sni-scan.tsx): the node looks through its own /24 for a REALITY target (ProbeService.NodeSniScan,
// sni-picker-plan §4.2). This file is what the screen reads of it: the last result the panel kept, the words of its error codes, and
// how a candidate is labelled. Candidate names are the text of other servers' certificates: the screen shows them as text, never as a
// link, an image or a request of the browser.

/** The panel lets a node be scanned once in this many seconds. */
export const SCAN_PAUSE_S = 30 * 60;

/** The last result kept for a node (run = false): nothing is scanned. */
export const sniScanQuery = (nodeId: string) =>
  queryOptions({ queryKey: ["sni-scan", nodeId], queryFn: () => probe.nodeSniScan({ nodeId, run: false }), staleTime: 30_000 });

export const errorCodes = ["node_offline", "agent_too_old", "busy", "too_soon", "not_my_address", "no_ipv4", "no_roots", "failed"] as const;
export type SniErrorCode = (typeof errorCodes)[number];
/** The panel's error code in the dictionary's words: a code this screen does not know is "failed". */
export const errorCodeOf = (code: string): SniErrorCode => ((errorCodes as readonly string[]).includes(code) ? (code as SniErrorCode) : "failed");

/** Minutes to wait out of the seconds, at least one. */
export const minutesOf = (seconds: number) => Math.max(1, Math.ceil(seconds / 60));

/** What the panel says about one scan: the fields of NodeSniScanResponse this screen uses (the generated message is structurally the same). */
export type Scan = {
  error: string;
  retryAfterS: number;
  atUnix: number | bigint;
  range: string;
  addresses: number;
  open: number;
  tls13: number;
  mlkem: number;
  valid: number;
  partial: boolean;
  ownLeftOut: number;
  candidates: readonly Candidate[];
};

export type Candidate = { ip: string; name: string; dns: string; h2: boolean; singleSite: boolean; http: number; ms: number; days: number };

/** What the card is showing, from the first line to the last. */
export type SniPhase = "never" | "running" | "result" | "partial" | "empty";

/** A reason the card cannot scan, or did not: shown above what is kept, which stays visible. */
export type SniProblem = SniErrorCode;

export type SniView = {
  phase: SniPhase;
  /** The reason a scan did not run, if any; also set while the node is not connected (before any call). */
  problem: SniProblem | null;
  /** Seconds until the next scan is allowed (0 = now); the button says it. */
  waitS: number;
  /** The button may start a scan. */
  canRun: boolean;
  /** The kept list is shown (something was scanned once). */
  kept: boolean;
};

/**
 * What the card shows. `scan` is the last answer (the stored result, or the answer of the last run), `running` a scan the page started,
 * `linked` whether the node's agent is connected, `now` the clock in seconds, `retryUntil` the second a too_soon answer said to wait until
 * (0 = none).
 */
export function viewOf(scan: Scan | undefined, o: { running: boolean; linked: boolean; now: number; retryUntil: number }): SniView {
  const at = scan ? Number(scan.atUnix) : 0;
  const kept = at > 0;
  const stored = kept ? at + SCAN_PAUSE_S - o.now : 0;
  const waitS = Math.max(0, Math.ceil(Math.max(stored, o.retryUntil - o.now)));
  let problem: SniProblem | null = null;
  if (scan?.error) problem = errorCodeOf(scan.error);
  // the pause is the panel's rule: the card says it before the panel has to refuse
  if (problem === "too_soon" && waitS === 0) problem = null;
  if (!o.linked) problem = "node_offline";
  let phase: SniPhase = "never";
  if (o.running) phase = "running";
  else if (kept && scan) phase = scan.partial ? "partial" : scan.candidates.length > 0 ? "result" : "empty";
  const blocked = problem === "node_offline" || problem === "agent_too_old" || problem === "no_ipv4" || problem === "not_my_address" || problem === "no_roots";
  return { phase, problem, waitS, canRun: !o.running && !blocked && waitS === 0, kept };
}

/** A candidate's label: its word in the dictionary (with what it is filled with), a tone for its colour and the dictionary's hint. */
export type Mark = { id: string; key: string; hint?: string; vars?: Record<string, string | number>; tone?: "good" | "warn" | "plain" };

/**
 * The labels of a candidate, best news first: where its name leads, HTTP/2, whose certificate it is, MLKEM (every kept site has it), the
 * answer to GET /, the handshake time and the days the certificate still holds. All are words and numbers the panel checked itself.
 */
export function marksOf(c: Candidate): Mark[] {
  const dns = ["here", "subnet", "elsewhere", "none"].includes(c.dns) ? c.dns : "none";
  const marks: Mark[] = [{ id: "dns", key: `sni.mark.dns.${dns}`, hint: `sni.mark.dns.${dns}.hint`, tone: dns === "here" ? "good" : dns === "subnet" ? "plain" : "warn" }];
  if (c.h2) marks.push({ id: "h2", key: "sni.mark.h2", tone: "good" });
  marks.push(c.singleSite ? { id: "single", key: "sni.mark.single", hint: "sni.mark.single.hint", tone: "good" } : { id: "shared", key: "sni.mark.shared", hint: "sni.mark.shared.hint", tone: "warn" });
  marks.push({ id: "mlkem", key: "sni.mark.mlkem", hint: "sni.mark.mlkem.hint", tone: "plain" });
  marks.push(c.http > 0 ? { id: "http", key: "sni.mark.http", vars: { code: c.http }, tone: c.http < 300 ? "good" : c.http < 400 ? "plain" : "warn" } : { id: "http", key: "sni.mark.nohttp", tone: "warn" });
  if (c.ms > 0) marks.push({ id: "ms", key: "sni.mark.ms", vars: { ms: c.ms }, tone: "plain" });
  if (c.days > 0) marks.push({ id: "days", key: "sni.mark.days", vars: { days: c.days }, tone: "plain" });
  return marks;
}

/** The counters of a result, in the order the screen shows them. */
export function statsOf(s: Scan): { id: "checked" | "open" | "tls13" | "mlkem" | "valid"; n: number }[] {
  return [
    { id: "checked", n: s.addresses },
    { id: "open", n: s.open },
    { id: "tls13", n: s.tls13 },
    { id: "mlkem", n: s.mlkem },
    { id: "valid", n: s.valid },
  ];
}
