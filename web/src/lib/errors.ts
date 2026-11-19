import { Code, ConnectError } from "@connectrpc/connect";
import type { T } from "@/i18n";
import { editionFeatures, type EditionFeature } from "@/i18n/edition";
import { asChannel, panelChannelCommand } from "./channels";
import { lossyVars } from "./port-check";

/**
 * The panel's refusal codes that have a sentence of their own, "err.<code>" (src/i18n/en.ts). The server writes "code",
 * "code: k=v&k=v" (its values as a query string) or the older "code: detail". A code that is not listed reads as the server
 * wrote it, so a new code never breaks an older page, and API and MCP clients get the same readable message.
 */
export const errorCodes = [
  "name_taken",
  "group_not_empty",
  // the password of a person's page that was removed cannot be changed: it is brought back first (UpdateUser page_password_action)
  "page_password_removed",
  // a change of a person's groups (UpdateUser add_group_ids / remove_group_ids): "group_in_both: group=<id>", "main_group: group=<id>", "too_many_groups"
  "group_in_both",
  "main_group",
  "too_many_groups",
  "profile_deployed",
  "stale_version",
  "node_offline",
  "agent_too_old",
  "node_retired",
  // a group's own form that would switch on a node the owner holds back from it (UpdateGroup, "node=<id>"): the node is given from its page
  "node_held",
  "sub_address_missing",
  "preset_is_default",
  "preset_builtin",
  // a profile on a node (CreateInbound / UpdateInbound)
  "acme_needs_domain",
  "reality_needs_domain",
  "mtproto_needs_domain",
  "port_taken",
  // services that share a TCP port by SNI (plugin.TCPSharePlan): the name is held, no service shows the node's site, a service has no
  // name, the agent is too old
  "sni_taken",
  "share_needs_site",
  "share_unsupported",
  "share_agent_old",
  "hop_taken",
  "port_in_hop",
  "sni_needs_domain",
  "sni_invalid",
  "already_on_node",
  // the site a node shows instead of its profile's REALITY target (CreateInbound / UpdateInbound reality_target_override): "reality_target_invalid: target=..."
  "reality_target_invalid",
  "reality_target_same_node",
  "reality_target_own",
  "reality_target_unsupported",
  // a person at the device limit (an AmneziaVPN or TrustTunnel key, the app switch that makes one): "device_limit: 3/3: alice-phone, laptop"
  "device_limit",
  // the UDP delivery check (design/udp-port-check.md): a port that loses packets; a twin with no clean port on all nodes
  "port_lossy",
  "no_clean_port",
  // an RPC that would start work the Cloudflare edition does not run yet (internal/panel/edition), "feature=<id>"
  "unavailable_in_edition",
  // the owner's list of sites WARP is checked against (InvalidArgument)
  "reach_domains_too_many",
  "reach_domain_invalid",
  "reach_domain_duplicate",
  // an own decoy archive the panel refuses (InvalidArgument): "decoy_bundle_<reason>" or "decoy_bundle_<reason>: path=<entry>"
  "decoy_bundle_empty",
  "decoy_bundle_bad_archive",
  "decoy_bundle_too_large",
  "decoy_bundle_too_many_files",
  "decoy_bundle_bad_path",
  "decoy_bundle_not_static",
  "decoy_bundle_no_index",
  "decoy_bundle_duplicate",
  // the panel's own decoy site cannot be used (FailedPrecondition); also the reason a node shows (Node.decoy_reason_code)
  "panel_unavailable",
  "panel_too_large",
  "panel_no_index",
  "panel_invalid",
  // the library of decoy sites (decoysite.proto): the refusals of its calls; "decoy_site_in_use: nodes=<ids>[&panel=1]" names where the site stands
  "decoy_site_not_found",
  "decoy_version_not_found",
  "decoy_name_taken",
  "decoy_name_invalid",
  "decoy_note_invalid",
  "decoy_library_full",
  "decoy_site_draft",
  "decoy_site_not_draft",
  "decoy_site_changed",
  "decoy_checks_failed",
  "decoy_site_in_use",
  "node_not_assigned",
  "panel_not_assigned",
  "decoy_file_not_found",
  "decoy_file_binary",
  "decoy_page_not_found",
  "decoy_preview_too_large",
  // relays (relay.proto): CreateRelay / UpdateRelay / CheckRelay
  "relay_exists",
  "same_node",
  "agent_old",
  "relay_unsupported",
  "relay_unchecked",
  "relay_check_stale",
  "relay_check_failed",
  // a bridge the pair cannot do (UpdateRelay / CreateRelay with mode "reverse"): "reverse_unavailable: reason=udp|entry_agent_old|exit_agent_old"
  "reverse_unavailable",
  "check_unavailable",
  // a debug capture of a node (ops.proto StartDebugCapture): one at a time per node
  "debug_busy",
  // an OS update of a node (update.proto StartOsRollout / CancelOsRollout), "os_blocked: reason=<key of os.block.*>"; and a node being updated (awgprep)
  "os_blocked",
  "os_cancel_inflight",
  "node_updating",
  // the root update helper follows /etc/mistgate/panel-update-channel (InstallPanelUpdate, FailedPrecondition): "channel=<selected>&allowed=<policy>"
  "panel_update_channel_not_allowed",
  "panel_update_channel_config_invalid",
] as const;

/**
 * The stable code of a message the panel writes as "code" or "code: detail" ("device_limit: 5/5" -> "device_limit",
 * "agent too old" -> "agent_too_old"): the words before the first colon, lower case, joined by "_".
 */
export function errorCode(message: string): string {
  const head = message.split(":")[0] ?? "";
  return head.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "");
}

/** The values of a coded message: {detail} is everything after the first colon, and its k=v pairs are values of their own. */
export function errorVars(message: string): Record<string, string> {
  const i = message.indexOf(":");
  const detail = i < 0 ? "" : message.slice(i + 1).trim();
  return { ...Object.fromEntries(new URLSearchParams(detail)), detail };
}

/** The feature id of "unavailable_in_edition: feature=backups" as the admin names it; an id this page does not know reads as sent. */
function editionVars(t: T, vars: Record<string, string>): Record<string, string> {
  const id = vars.feature ?? "";
  return { ...vars, feature: (editionFeatures as readonly string[]).includes(id) ? t(`edition.feature.${id as EditionFeature}`) : id };
}

/** "device_limit: 3/3: alice-phone, laptop": the places used and allowed, and the devices that hold them (": a, b", or nothing). */
function deviceLimitVars(vars: Record<string, string>): Record<string, string> {
  const m = /^(\d+)\/(\d+)(?::\s*(.*))?$/.exec(vars.detail ?? "");
  return { ...vars, used: m?.[1] ?? "?", limit: m?.[2] ?? "?", names: m?.[3] ? `: ${m[3]}` : "" };
}

/** "nodes de1, nl1 and the panel": where a site that cannot be deleted stands (the names, or the ids when the page has no names). */
export function inUseWhere(t: T, names: readonly string[], panel: boolean): string {
  const list = names.join(", ");
  return list && panel ? t("err.decoy_site_in_use.both", { nodes: list }) : panel ? t("err.decoy_site_in_use.panel") : t("err.decoy_site_in_use.nodes", { nodes: list });
}

/** The words of a relay refusal's values: why a server cannot be relayed, which stage of the check failed. */
function relayVars(t: T, vars: Record<string, string>): Record<string, string> {
  const reason = vars.reason ?? "";
  const worded = ["cdn", "acme_ip", "build_failed"].includes(reason);
  const bridge = ["udp", "entry_agent_old", "exit_agent_old"].includes(reason);
  const stage = vars.stage === "path" || vars.stage === "reverse_reach" || vars.stage === "bridge" ? vars.stage : "leg";
  return {
    ...vars,
    reason: worded ? t(`relay.unsupported.${reason as "cdn"}`) : bridge ? t(`relay.way.blockedWord.${reason as "udp"}`) : reason,
    stage: t(`relay.stageName.${stage}`),
  };
}

const osBlocks = ["offline", "agent_old", "unsupported", "panel_host", "node_busy", "no_status", "nothing_to_do", "rollout_active", "agent_rollout", "panel_busy", "awg_build", "reboot_wait"];

/** The words of an OS update refusal's reason (a key of "os.block.*"); a reason from a newer panel reads as sent. */
function osVars(t: T, vars: Record<string, string>): Record<string, string> {
  const reason = vars.reason ?? "";
  return { ...vars, reason: osBlocks.includes(reason) ? t(`os.block.${reason as "offline"}`) : reason };
}

/**
 * The sentence of a refusal about a TCP port that services share by SNI (sni_taken, share_needs_site, share_unsupported,
 * share_agent_old): the one that names a free port when the panel found one, and no placeholder left for a value it did not send.
 */
export function shareRefusalText(t: T, code: string, vars: Record<string, string>): string {
  const key = code === "sni_taken" && vars.free ? "err.sni_taken_free" : (`err.${code}` as "err.sni_taken");
  return t(key, { protocol: "", ...vars });
}

/** Our sentence for a message whose code is listed, else undefined. */
function known(c: ConnectError, t: T): string | undefined {
  const code = errorCode(c.rawMessage) as (typeof errorCodes)[number];
  if (!errorCodes.includes(code)) return undefined;
  const vars = errorVars(c.rawMessage);
  // the command is built here from the validated channel word, never taken from the message
  if (code === "panel_update_channel_not_allowed") {
    const channel = asChannel(vars.channel);
    return t("err.panel_update_channel_not_allowed", { ...vars, channel, allowed: asChannel(vars.allowed), command: panelChannelCommand(channel) });
  }
  // a relay holds the port: the panel names it "relay" or "relay to <exit>", not a profile
  if (code === "port_taken" && /^relay($| )/.test(vars.profile ?? "")) return t("err.port_taken_relay", vars);
  if (code === "sni_taken" || code === "share_unsupported") return shareRefusalText(t, code, vars);
  if (code === "device_limit") return t("err.device_limit", deviceLimitVars(vars));
  // where a site stands is the node ids the panel sent (a screen that knows the names words it itself: lib/decoysites.ts decoyErrorText)
  if (code === "decoy_site_in_use") return t("err.decoy_site_in_use", { where: inUseWhere(t, (vars.nodes ?? "").split(",").filter(Boolean), vars.panel === "1") });
  // the bridge needs a path the panel can prove from a third node; a server whose path cannot be checked (an MTProto server with the site
  // fallback) never passes that proof, so the refusal says so instead of "the relay did not pass its check"
  if (code === "relay_check_failed" && vars.stage === "path" && vars.code === "unverifiable") return t("err.relay_bridge_unverifiable", vars);
  const filled =
    code === "port_lossy"
      ? lossyVars(t, vars)
      : code === "unavailable_in_edition"
        ? editionVars(t, vars)
        : code.startsWith("decoy_bundle_")
          ? { path: "?", ...vars }
          : code.startsWith("relay_") || code === "reverse_unavailable"
            ? relayVars(t, vars)
            : code === "os_blocked"
              ? osVars(t, vars)
              : vars;
  return t(`err.${code}`, filled);
}

/**
 * The generic "something went wrong" line, with the request code the panel stamped on the response (X-Request-Id, VPS edition) so
 * a person can quote it and an operator finds the request in the panel logs and the audit log.
 */
function generic(c: ConnectError, t: T): string {
  const id = requestIdOf(c);
  return id ? `${t("err.generic")} ${t("err.requestCode", { id })}` : t("err.generic");
}

/** The request code the panel put on a failed call (the X-Request-Id header of a signed-in call, VPS edition), or "". */
export function requestIdOf(e: unknown): string {
  const id = ConnectError.from(e).metadata.get("x-request-id") ?? "";
  return /^[0-9a-f]{8}$/.test(id) ? id : "";
}

/** PermissionDenied messages from bearer policy have distinct causes; keep the actual role denial distinct too. */
function permissionDenied(c: ConnectError, t: T): string {
  switch (c.rawMessage) {
    case "this call is not available to API tokens":
      return t("err.apiTokenCallUnavailable");
    case "this token's profile cannot do this":
      return t("err.apiTokenProfileDenied");
    case "this needs the owner's approval; it is not available over the API":
      return t("err.apiOwnerApprovalRequired");
    case "your role cannot do this":
      return t("err.denied");
    default: {
      const code = errorCode(c.rawMessage);
      return code === "mihomo_templates_owner_only" ? t("err.mihomo_templates_owner_only") : code === "routing_profiles_owner_only" ? t("err.routing_profiles_owner_only") : t("err.permissionDenied");
    }
  }
}

/**
 * A sentence for a failed admin RPC. A listed code reads as our sentence. Otherwise the server's own short message is kept
 * for the codes that mean "you sent something the rules refuse" (it names the rule); everything else gets a localized
 * line, so a stack trace or an internal id never reaches a toast.
 */
export function errorText(e: unknown, t: T): string {
  if (e instanceof DOMException) {
    return e.name === "NotAllowedError" || e.name === "AbortError" ? t("err.cancelled") : t("err.generic");
  }
  const c = ConnectError.from(e);
  switch (c.code) {
    case Code.Unavailable:
    case Code.DeadlineExceeded:
      return known(c, t) ?? t("err.network");
    case Code.PermissionDenied:
      return permissionDenied(c, t);
    case Code.NotFound:
      return t("err.notFound");
    case Code.ResourceExhausted:
      return known(c, t) ?? t("err.rateLimited");
    case Code.InvalidArgument:
    case Code.AlreadyExists:
    case Code.FailedPrecondition:
    case Code.Aborted:
      return known(c, t) ?? (c.rawMessage || generic(c, t));
    default:
      return generic(c, t);
  }
}

/** True for failures that are worth another try (the network, not a verdict from the server). */
export function isTransient(e: unknown): boolean {
  const code = ConnectError.from(e).code;
  return code === Code.Unavailable || code === Code.DeadlineExceeded || code === Code.Unknown || code === Code.Internal;
}
