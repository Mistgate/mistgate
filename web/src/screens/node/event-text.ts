import type { T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { describeEvent } from "@/lib/events";
import { reasonText } from "@/lib/updates";
import { prepReasonText } from "./awg-kernel";
import type { EventLine, NodeEvent } from "./event-model";

const reasonKeys: Record<string, MessageKey> = {
  agent_start: "node.ev.reason.agent_start",
  profile_added: "node.ev.reason.profile_added",
  recovered: "node.ev.reason.recovered",
  restart_command: "node.ev.reason.restart_command",
  config_changed: "node.ev.reason.config_changed",
};

/** The name to show for a profile: its own, else its protocol, else "a profile" (an old row whose inbound is gone). */
export function profileLabel(t: T, e: Pick<NodeEvent, "profileName" | "protocol" | "params">): string {
  return e.profileName || e.params.profile || e.protocol || e.params.protocol || t("node.ev.someProfile");
}

const joinNames = (names: readonly string[]) => names.join(", ");

/** What the panel and the agent said about an update: the check passed (the panel's gate), the version is pinned (the commit). */
function verdict(t: T, rows: readonly NodeEvent[]): string {
  const passed = rows.some((r) => r.code === "update_step_passed");
  const pinned = rows.some((r) => r.code === "update_committed");
  if (passed && pinned) return t("node.ev.update.verified");
  if (passed) return t("node.ev.update.checked");
  return pinned ? t("node.ev.update.pinned") : "";
}

// "; " and not " · ": profile names are written with dots ("hy2 · WARP · 8443")
const parts = (...p: string[]) => p.filter(Boolean).join("; ");

/**
 * The two lines of an event line: what happened, and the short "why / what followed" under it (empty when there is none).
 * `stamp` formats a time for the sentences that name one ("stopped answering (since 17:16)").
 */
export function lineText(t: T, l: EventLine, stamp?: (unix: number) => string): { title: string; sub: string } {
  const e = l.head;
  const p = e.params;
  const by = e.source === "admin" && p.actor ? ` · ${t("node.ev.by", { actor: p.actor })}` : "";
  const names = joinNames(l.profiles);
  const up = names ? t("node.ev.agent.profilesUp", { names }) : "";
  switch (l.kind) {
    case "agent_started": {
      const title =
        p.reason === "update"
          ? p.prev_version
            ? t("node.ev.agent.update", { prev: p.prev_version, version: p.version ?? "" })
            : t("node.ev.agent.updateNoPrev", { version: p.version ?? "" })
          : p.reason === "boot"
            ? t("node.ev.agent.boot", { version: p.version ?? "" })
            : t("node.ev.agent.restart", { version: p.version ?? "" });
      return { title, sub: parts(p.reason === "update" ? verdict(t, l.children) : "", up) };
    }
    case "update": {
      // the commit or the verdict without the start of the build (an older agent, or the start is on the next page)
      const from = p.from_version ?? "";
      const to = p.to_version ?? "";
      return { title: from ? t("node.ev.agent.update", { prev: from, version: to }) : t("node.ev.agent.updateNoPrev", { version: to }), sub: verdict(t, [e, ...l.children]) };
    }
    case "rollback": {
      const rows = [e, ...l.children];
      const to = rows.map((r) => r.params.to_version).find(Boolean) ?? "";
      // the panel's reason says what its gate saw ("probe_failed", "manual"); when it only says the agent gave up by
      // itself, the agent's own reason says why ("not_committed", "crash_loop")
      const reasonOf = (code: string) => rows.find((r) => r.code === code && r.params.reason)?.params.reason;
      const panel = reasonOf("update_step_rolled_back");
      const why = (panel !== "rolled_back_by_agent" ? panel : undefined) ?? reasonOf("update_rolled_back") ?? panel ?? "";
      const back = rows.find((r) => r.code === "agent_started")?.params.version;
      return {
        title: why ? t("node.ev.rollback", { to, reason: reasonText(t, why) }) : t("node.ev.rollbackPlain", { to }),
        sub: parts(back ? t("node.ev.rollback.back", { version: back }) : "", up),
      };
    }
    case "profile_added":
      return { title: t("node.ev.profile.added", { profile: profileLabel(t, e) }) + by, sub: l.started > 0 ? t("node.ev.profile.addedStarted") : "" };
    case "profile_removed":
      return { title: t("node.ev.profile.removed", { profile: profileLabel(t, e) }) + by, sub: "" };
    case "profiles_restarted":
      return {
        title: (e.inboundId ? t("node.ev.restart.one", { profile: profileLabel(t, e) }) : t("node.ev.restart.all")) + by,
        sub: l.restarted > 0 ? t("node.ev.restart.done", { names }) : "",
      };
    case "profiles_started": {
      const n = l.children.length;
      const again = e.code === "engine_restarted";
      const reason = n === 1 && p.reason && reasonKeys[p.reason] ? t(reasonKeys[p.reason]!) : "";
      if (n === 1) return { title: t(again ? "node.ev.restarted.one" : "node.ev.started.one", { profile: profileLabel(t, e) }), sub: reason };
      return { title: t(again ? "node.ev.restarted.many" : "node.ev.started.many", { names }), sub: "" };
    }
  }
  return { title: eventTitle(t, e, stamp) + by, sub: eventSub(t, e) };
}

/** "Settings applied: 1 profile added · 14 devices", the zeros left out. */
function appliedTitle(t: T, p: Record<string, string>): string {
  const n = (k: string) => Number(p[k] ?? 0);
  const what = [
    n("added") ? t.n("node.ev.applied.added", n("added")) : "",
    n("removed") ? t.n("node.ev.applied.removed", n("removed")) : "",
    n("changed") ? t.n("node.ev.applied.changed", n("changed")) : "",
    n("users") ? t.n("node.ev.applied.devices", n("users")) : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return what ? t("node.ev.applied", { what }) : t("node.ev.applied.none");
}

function eventTitle(t: T, e: NodeEvent, stamp?: (unix: number) => string): string {
  switch (e.code) {
    case "torrent_attempt":
      return t("node.ev.torrentAttempt", { user: e.params.user_name || e.params.user_id || t("node.ev.torrentUnknown") });
    case "engine_failed":
      return t("node.ev.failed", { profile: profileLabel(t, e) });
    case "awg_backend_unavailable":
      return t("node.ev.awgBackend", { profile: profileLabel(t, e) });
    case "update_step_failed":
      return t("node.ev.update.failed");
    case "state_applied":
      return e.params.added !== undefined ? appliedTitle(t, e.params) : describeEvent(t, e).text;
    default:
      return describeEvent(t, e, stamp).text;
  }
}

function eventSub(t: T, e: NodeEvent): string {
  const p = e.params;
  if (e.code === "torrent_attempt") {
    const protocol = [p.protocol, p.torrent_protocol].filter(Boolean).join(" / ");
    return [
      protocol ? t("node.ev.torrentProtocol", { protocol }) : "",
      p.destination ? t("node.ev.torrentDestination", { destination: p.destination }) : "",
    ].filter(Boolean).join("; ");
  }
  if (e.code === "update_step_failed") return p.reason ? reasonText(t, p.reason) : "";
  if (e.code === "engine_started" || e.code === "engine_restarted") return p.reason && reasonKeys[p.reason] ? t(reasonKeys[p.reason]!) : "";
  if (e.code === "awg_kernel_prepare_failed") return prepReasonText(t, p.code ?? "", p.reason ?? "");
  return describeEvent(t, e).detail;
}

/** One row of the expanded details: the exact time is the caller's, the rest is here. */
export function rowDetail(e: NodeEvent): string {
  return Object.entries(e.params)
    .map(([k, v]) => `${k}=${v}`)
    .join(" ");
}
