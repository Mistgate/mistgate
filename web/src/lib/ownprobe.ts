import { queryOptions, skipToken, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { useToast } from "@/components/ui/toast";
import type { OwnProbeServiceGetResponse } from "@/gen/mistgate/ownprobe/v1/own_probe_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { ownProbe } from "./api";
import { sourceErrorText } from "./globalping";
import type { CountryCheck } from "./health";
import { plain, type Plain } from "./plain";

// The "Own probes" card of the Reachability sources (own_probe.proto): routers and servers the owner controls inside a country
// check the nodes from their own networks. Every call is the owner's (API tokens and the MCP have none); creating, revoking,
// switching and disconnecting need a step-up, "check now" and "confirm network" do not. The one-time install command comes with
// Create and never again. The words are in i18n/probes.ts ("pr."); nothing here knows an address of a device.

export type OwnProbes = Plain<OwnProbeServiceGetResponse>;
export type OwnProbeSettings = NonNullable<OwnProbes["settings"]>;
export type OwnProbeRow = OwnProbes["probes"][number];

export const ownProbeQuery = queryOptions({
  queryKey: ["ownProbe"],
  queryFn: async ({ signal }) => plain(await ownProbe.get({}, { signal })),
  // a probe comes online, goes silent or changes its network without the page: look again now and then
  refetchInterval: 30_000,
});

/** Minutes between rounds on offer (the server takes 30..240, default 60). A probe itself never measures more often than every 30. */
export const probeIntervals = [30, 60, 90, 120, 180, 240] as const;
export const probeDefaultInterval = 60;

export const probeKinds = ["home", "mobile", "dc"] as const;
export type ProbeKind = (typeof probeKinds)[number];

/** A probe that was last heard longer ago than this is "not heard from" (it asks every 12-18 minutes, the script at most hourly). */
export const offlineAfterSec = 3600;

/** The panel's script as the probes report it (the first 12 hex of its sha256), of the newest command made in this session. */
const scriptQuery = { queryKey: ["ownProbe", "script"] as const };
export const useLatestScript = () => useQuery<string>({ ...scriptQuery, queryFn: skipToken, staleTime: Infinity }).data;

/** The probe is older than the script the panel hands out, as far as this session knows it. */
export const isOutdated = (p: Pick<OwnProbeRow, "version" | "state">, latest: string | undefined) =>
  !!latest && !!p.version && p.state !== "pending" && p.state !== "revoked" && p.version !== latest.slice(0, 12);

/** The ids of probes that see no filtering on their path (the point says "no_filter"): the checks carry it, the probe does not. */
export function unfilteredProbes(checks: readonly CountryCheck[]): Set<string> {
  const ids = new Set<string>();
  for (const c of checks) for (const v of c.vantages ?? []) if (v.source === "probes" && v.why === "no_filter") ids.add(v.key.replace(/^probe:/, ""));
  return ids;
}

export type ProbeLook = "pending" | "expired" | "online" | "offline" | "netChanged" | "noFilter" | "revoked";

/** What a probe is now. A pending one is waiting for its command; an active one is on, or silent for too long; the rest as the server says. */
export function probeLook(p: OwnProbeRow, now: number, unfiltered: ReadonlySet<string>): ProbeLook {
  if (p.state === "revoked") return "revoked";
  if (p.state === "pending") return p.tokenExpiresUnix > 0 && p.tokenExpiresUnix < now ? "expired" : "pending";
  if (p.state === "network_changed") return "netChanged";
  if (p.lastSeenUnix < now - offlineAfterSec) return "offline";
  return unfiltered.has(p.id) ? "noFilter" : "online";
}

export type ProbesState =
  | { kind: "off" }
  | { kind: "none" }
  | { kind: "pending" }
  | { kind: "offline"; since: number }
  | { kind: "netChanged" }
  | { kind: "works"; n: number; m: number; lastAt: number; changed: number; unfiltered: number };

/**
 * The state line: off; on with no probe (a revoked one does not count); only probes waiting for their command; the ones that
 * ought to work are silent; a network change waits for the owner; else "N of M" with the newest result.
 */
export function probesState(s: Pick<OwnProbeSettings, "enabled">, probes: readonly OwnProbeRow[], now: number, unfiltered: ReadonlySet<string>): ProbesState {
  if (!s.enabled) return { kind: "off" };
  const live = probes.filter((p) => p.state !== "revoked");
  if (live.length === 0) return { kind: "none" };
  const looks = live.map((p) => probeLook(p, now, unfiltered));
  const works = looks.filter((l) => l === "online" || l === "noFilter").length;
  const changed = looks.filter((l) => l === "netChanged").length;
  if (works === 0 && changed > 0) return { kind: "netChanged" };
  if (works === 0 && looks.every((l) => l === "pending" || l === "expired")) return { kind: "pending" };
  if (works === 0) return { kind: "offline", since: Math.max(...live.map((p) => p.lastSeenUnix)) };
  return {
    kind: "works",
    n: works,
    m: live.length,
    lastAt: Math.max(0, ...live.map((p) => p.lastResultUnix)),
    changed,
    unfiltered: looks.filter((l) => l === "noFilter").length,
  };
}

/** The probes in the order of the list: the ones to look at first (a changed network, no contact), then the working, then the rest. */
export function sortProbes(probes: readonly OwnProbeRow[], now: number, unfiltered: ReadonlySet<string>): OwnProbeRow[] {
  const rank: Record<ProbeLook, number> = { netChanged: 0, offline: 1, expired: 2, pending: 3, online: 4, noFilter: 5, revoked: 6 };
  return [...probes].sort((a, b) => rank[probeLook(a, now, unfiltered)] - rank[probeLook(b, now, unfiltered)] || a.name.localeCompare(b.name));
}

export const probeConfPath = { keenetic: "/opt/etc/mistgate-probe/conf", server: "/etc/mistgate-probe/conf" } as const;
export const probeBinPath = { keenetic: "/opt/sbin/mistgate-probe", server: "/usr/local/sbin/mistgate-probe" } as const;
export type ProbeHost = keyof typeof probeConfPath;

/** The system a kind of probe usually stands on: a router at home or on the mobile network, a server in a data centre. */
export const hostOf = (kind: string): ProbeHost => (kind === "dc" ? "server" : "keenetic");

/**
 * The command that updates a probe already installed. The panel's address is in the probe's own configuration (its `url=` line),
 * so the command needs nothing secret: it downloads the current script and runs its `upgrade`. It cannot pin the script's hash
 * the way the install command does: the panel's answer does not carry the hash of the script it serves now (see the report).
 */
export const updateCommand = (host: ProbeHost) =>
  `cd /tmp && curl -fsS --proto '=https' -o mgp.sh "$(sed -n 's/^url=//p' ${probeConfPath[host]})/probe.sh" && sh mgp.sh upgrade`;

export const uninstallCommand = (host: ProbeHost) => `sh ${probeBinPath[host]} uninstall`;

const errorKeys: Record<string, MessageKey> = {
  "public HTTPS URL is required to build an install command": "pr.err.publicUrl",
  "probe not found": "pr.err.notFound",
  "interval_min must be 30..240": "pr.err.interval",
  "fallback_url must be an absolute HTTPS URL": "pr.err.fallback",
  "name, city or kind is invalid": "pr.err.input",
  "probe network has not changed": "pr.err.network",
};

export const ownProbeErrorText = (e: unknown, t: T) => sourceErrorText(e, t, errorKeys);

/** A fallback address is empty or an absolute https:// URL, as the panel takes it. */
export function validFallback(v: string): boolean {
  if (v.trim() === "") return true;
  try {
    const u = new URL(v.trim());
    return u.protocol === "https:" && u.hostname !== "" && !u.username && !u.password && !u.hash;
  } catch {
    return false;
  }
}

/** What the window that makes a probe keeps while it is open, and only then: the install command and who it is for. */
export type Issued = { probe: OwnProbeRow; command: string };

export function useOwnProbeActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const fail = (e: unknown) => {
    if (!isStepUpCancelled(e)) toast.error(ownProbeErrorText(e, t));
  };
  const fresh = () => void qc.invalidateQueries({ queryKey: ownProbeQuery.queryKey });

  const update = useMutation({
    mutationFn: async (patch: Partial<OwnProbeSettings>) => {
      const now = qc.getQueryData(ownProbeQuery.queryKey)?.settings;
      const next = { enabled: false, intervalMin: probeDefaultInterval, fallbackUrl: "", ...now, ...patch };
      await guard(() => ownProbe.update({ settings: next }));
      return patch;
    },
    onSuccess: (patch) => {
      fresh();
      toast(t("enabled" in patch ? (patch.enabled ? "pr.on" : "pr.off") : "pr.saved"));
    },
    onError: fail,
  });
  const disconnect = useMutation({
    mutationFn: () => guard(() => ownProbe.disconnect({})),
    onSuccess: () => {
      fresh();
      toast(t("pr.disconnected"));
    },
    onError: fail,
  });
  const revoke = useMutation({
    mutationFn: (id: string) => guard(() => ownProbe.revoke({ id })),
    onSuccess: () => {
      fresh();
      toast(t("pr.revoked"));
    },
    onError: fail,
  });
  const confirmNetwork = useMutation({
    mutationFn: (id: string) => ownProbe.confirmNetwork({ id }),
    onSuccess: () => {
      fresh();
      toast(t("pr.confirmNetwork.done"));
    },
    onError: fail,
  });
  const checkNow = useMutation({
    mutationFn: (id: string) => ownProbe.checkNow({ id }),
    onSuccess: fresh, // the row says when the probe takes it up
    onError: fail,
  });

  /**
   * Makes a probe and returns its install command. Not a mutation on purpose: a mutation keeps its result in the cache for
   * minutes, and the command is shown once. The caller holds it as long as its window is open and no longer; the token inside is
   * dropped here, the command carries it.
   */
  async function create(req: { name: string; city: string; kind: ProbeKind }): Promise<Issued> {
    const res = await guard(() => ownProbe.create(req));
    fresh();
    qc.setQueryData(scriptQuery.queryKey, res.scriptSha256.slice(0, 12));
    return { probe: plain(res.probe!), command: res.command };
  }

  /** "I did not copy it": the probe that waits for its command is revoked and the same one is made again, with a new command. */
  async function recreate(old: Issued, req: { name: string; city: string; kind: ProbeKind }): Promise<Issued> {
    await guard(() => ownProbe.revoke({ id: old.probe.id }));
    return create(req);
  }

  return { update, disconnect, revoke, confirmNetwork, checkNow, create, recreate, fail };
}
