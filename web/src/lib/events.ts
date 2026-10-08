import type { StatusKind } from "@/components/ui/status";
import { EventSeverity, type Event } from "@/gen/mistgate/admin/v1/fleet_pb";
import type { T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { minutesText } from "./format";
import { lossPct, senderLabel } from "./port-check";

// Events carry an open vocabulary of codes (panel codes, agent codes, plugin codes). The UI owns the wording:
// a known code gets a localized sentence, an unknown one is shown as its code, so nothing is ever hidden.

const textKeys: Record<string, MessageKey> = {
  node_down: "event.node_down",
  node_blip: "event.node_blip",
  node_recovered: "event.node_recovered",
  traffic_stopped: "event.traffic_stopped",
  traffic_resumed: "event.traffic_resumed",
  check_failing: "event.check_failing",
  check_recovered: "event.check_recovered",
  node_enrolled: "event.node_enrolled",
  node_retired: "event.node_retired",
  stats_stale: "event.stats_stale",
  apply_rejected: "event.apply_rejected",
  state_drift: "event.state_drift",
  state_applied: "event.state_applied",
  engine_started: "event.engine_started",
  engine_failed: "event.engine_failed",
  engine_restarted: "event.engine_restarted",
  cert_renewed: "event.cert_renewed",
  clock_skew: "event.clock_skew",
  host_rebooted: "event.host_rebooted",
  stats_dropped: "event.stats_dropped",
  disk_low: "event.disk_low",
  credential_expired: "event.credential_expired",
  device_limit_reached: "event.device_limit_reached",
  hop_failed: "event.hop_failed",
  hop_rejected: "event.hop_rejected",
  tunnel_failed: "event.tunnel_failed",
  tunnel_v6_fallback: "event.tunnel_v6_fallback",
  warp_needs_attention: "event.warp_needs_attention",
  torrent_guard_degraded: "event.torrent_guard_degraded",
  host_firewall_sync_failed: "event.host_firewall_sync_failed",
  host_firewall_sync_recovered: "event.host_firewall_sync_recovered",
  user_created: "event.user_created",
  user_over_quota: "event.user_over_quota",
  user_expired: "event.user_expired",
  device_revoked: "event.device_revoked",
  subscription_shared_suspect: "event.subscription_shared_suspect",
  agent_started: "event.agent_started",
  profile_added: "event.profile_added",
  profile_removed: "event.profile_removed",
  profiles_restarted: "event.profiles_restarted",
  update_committed: "event.update_committed",
  update_rolled_back: "event.update_rolled_back",
  update_step_passed: "event.update_step_passed",
  update_step_failed: "event.update_step_failed",
  update_step_rolled_back: "event.update_step_rolled_back",
  awg_backend_unavailable: "event.awg_backend_unavailable",
  awg_kernel_prepare_started: "event.awg_kernel_prepare_started",
  awg_kernel_prepare_done: "event.awg_kernel_prepare_done",
  awg_kernel_prepare_failed: "event.awg_kernel_prepare_failed",
  awg_kernel_switched: "event.awg_kernel_switched",
  bandwidth_measured: "event.bandwidth_measured",
  bandwidth_upload_missing: "event.bandwidth_upload_missing",
  port_lossy: "event.port_lossy",
};

// Dot colour by code first (a blip is grey-blue, never red), else by severity. A restart that happened is a fact, not a
// process: a still dot, never the spinning ring of "busy".
const kindByCode: Record<string, StatusKind> = {
  node_blip: "blip",
  node_recovered: "ok",
  node_retired: "off",
};

export function eventKind(e: Pick<Event, "code" | "severity">): StatusKind {
  const byCode = kindByCode[e.code];
  if (byCode) return byCode;
  if (e.severity === EventSeverity.ERROR) return "bad";
  if (e.severity === EventSeverity.WARNING) return "warn";
  return "ok";
}

type Described = Pick<Event, "code" | "params"> & Partial<Pick<Event, "profileName">> & { timeUnix?: number };

/**
 * The sentence for an event (no subject: the caller puts the node or user name in front) and its detail line, if any.
 * `stamp` formats a time ("17:16", "30.09 17:16"): with it a node_down says since when the node is silent.
 */
export function describeEvent(t: T, e: Described, stamp?: (unix: number) => string): { text: string; detail: string } {
  const key = textKeys[e.code];
  const p = e.params;
  // an error text the agent or panel attached, kept verbatim: it is the thing an admin searches for
  const detail = p.error || p.detail || "";
  if (!key) return { text: e.code, detail: Object.entries(p).map(([k, v]) => `${k}=${v}`).join(" ") };
  // the profile the list joined in (a panel event also carries it in its params once the inbound is gone)
  const profile = e.profileName || p.profile || "";
  const span = minutesText(t, Number(p.minutes));
  switch (e.code) {
    case "node_down": {
      // the minutes were measured when the panel noticed: the silence began that much earlier and may still go on
      const since = (e.timeUnix ?? 0) - Number(p.minutes) * 60;
      return { text: stamp && e.timeUnix && p.minutes ? t(key, { since: stamp(since) }) : t("event.node_down.plain"), detail };
    }
    case "node_blip":
      return { text: t(p.rebooted === "true" ? "event.node_blip.rebooted" : key, { span }), detail };
    case "node_recovered":
      return { text: t(key, { span }), detail };
    case "engine_failed":
      return { text: profile ? t("event.engine_failed.named", { profile }) : t(key), detail };
    case "port_lossy":
      // the UDP delivery check found an enabled profile's port losing packets: {sent, got, sender} of its run
      return { text: t(key, { profile: profile || "—", port: p.port ?? "", lost: lossPct(Number(p.sent), Number(p.got)), sender: senderLabel(t, p.sender ?? "") }), detail };
    case "tunnel_v6_fallback":
      return { text: t(p.mode === "none" ? "event.tunnel_v6_fallback.none" : key), detail };
    case "host_firewall_sync_failed":
      // an older agent does not list the ports
      return { text: p.ports ? t(key, { ports: p.ports }) : t("event.host_firewall_sync_failed.plain"), detail };
  }
  // {lasted}: how long a health alert lasted (traffic_resumed, check_recovered), from the whole minutes the panel wrote
  return { text: t(key, { profile, ...p, lasted: span }), detail };
}
