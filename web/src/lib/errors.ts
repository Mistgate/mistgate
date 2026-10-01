import { Code, ConnectError } from "@connectrpc/connect";
import type { T } from "@/i18n";

/**
 * The panel's refusal codes that have a sentence of their own, "err.<code>" (src/i18n/en.ts). The server writes "code",
 * "code: k=v&k=v" (its values as a query string) or the older "code: detail". A code that is not listed reads as the server
 * wrote it, so a new code never breaks an older page, and API and MCP clients get the same readable message.
 */
export const errorCodes = [
  "name_taken",
  "group_not_empty",
  "profile_deployed",
  "stale_version",
  "node_offline",
  "agent_too_old",
  "node_retired",
  "sub_address_missing",
  "preset_is_default",
  "preset_builtin",
  // a profile on a node (CreateInbound / UpdateInbound)
  "acme_needs_domain",
  "port_taken",
  "hop_taken",
  "port_in_hop",
  "sni_needs_domain",
  "sni_invalid",
  "already_on_node",
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

/** Our sentence for a message whose code is listed, else undefined. */
function known(c: ConnectError, t: T): string | undefined {
  const code = errorCode(c.rawMessage) as (typeof errorCodes)[number];
  return errorCodes.includes(code) ? t(`err.${code}`, errorVars(c.rawMessage)) : undefined;
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
      return t("err.denied");
    case Code.NotFound:
      return t("err.notFound");
    case Code.ResourceExhausted:
      return known(c, t) ?? t("err.rateLimited");
    case Code.InvalidArgument:
    case Code.AlreadyExists:
    case Code.FailedPrecondition:
    case Code.Aborted:
      return known(c, t) ?? (c.rawMessage || t("err.generic"));
    default:
      return t("err.generic");
  }
}

/** True for failures that are worth another try (the network, not a verdict from the server). */
export function isTransient(e: unknown): boolean {
  const code = ConnectError.from(e).code;
  return code === Code.Unavailable || code === Code.DeadlineExceeded || code === Code.Unknown || code === Code.Internal;
}
