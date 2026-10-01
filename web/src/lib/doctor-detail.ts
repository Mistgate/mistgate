import type { T } from "@/i18n";
import type { Fmt } from "./format";
import { hasKey, type DoctorItem } from "./health";
import { warpErrorLine } from "./warp-error";

// The node's fact line in the viewer's language. The agent sends a code ("disk_space.usage") and plain-decimal
// params; the sentence is "doctor.detail.<code>" in i18n/health.ts. Numbers and sizes are formatted here (format.ts),
// short machine tokens ("headers", "yes", "expiring") are worded by "doctor.tok.<token>". A code the SPA does not
// know, or none (an agent that predates the codes), leaves the agent's English line to be shown as it is.

const MiB = 1024 * 1024;
/** Params that hold a size in MiB, and the formatted size added next to them. */
const sizes: Record<string, string> = { free_mb: "free", journal_mb: "journal", cap_mb: "cap", btmp_mb: "btmp" };
/** Params that are single machine tokens. */
const tokens = new Set(["missing", "differs", "notes", "reason", "ntp_synced", "mode", "backend", "state", "connect", "ipv6", "oom_kills", "hint", "error"]);
/** Params that are comma separated lists; the ones in `tokens` are worded item by item. */
const lists = new Set(["missing", "differs", "notes", "failed", "tasks", "names", "tables", "nat_tables", "ports", "hop_holders", "process", "on_our_ports", "inbounds", "warp_inbounds", "oom_victims"]);

type Params = Record<string, string | number>;

/** The wording of a machine token, or the token itself when there is none. */
function word(t: T, v: string): string {
  const k = `doctor.tok.${v}`;
  return hasKey(k) ? t(k) : v;
}

/**
 * The params of an item ready for a sentence: sizes and numbers formatted, tokens and lists worded. The original
 * names stay (the "why" texts use them), derived ones are added ("free" next to "free_mb").
 */
export function itemParams(t: T, fmt: Fmt, item: Pick<DoctorItem, "params">): Params {
  const p: Params = { ...item.params };
  for (const [k, v] of Object.entries(item.params)) {
    const n = Number(v);
    if (k in sizes && v !== "" && Number.isFinite(n)) p[sizes[k]!] = fmt.bytes(n * MiB, 1024);
    else if (k === "offset_s" && v !== "" && Number.isFinite(n)) p[k] = fmt.num(Math.abs(n), 0);
    else if (k === "median_ms" && v !== "" && Number.isFinite(n)) p[k] = fmt.num(n, 0);
    else if (k === "error") p[k] = warpErrorLine(t, v) || v; // WARP's last_error: the same words as the WARP card
    else if (lists.has(k)) p[k] = v.split(",").map((x) => (tokens.has(k) ? word(t, x) : x)).join(", ");
    else if (tokens.has(k)) p[k] = word(t, v);
  }
  // pieces of a sentence that exist only when their value does
  if (!p.error) p.error = word(t, "no_handshake");
  p.via = item.params.backend ? t("doctor.tok.via", { backend: p.backend ?? "" }) : "";
  p.colo_part = item.params.colo ? t("doctor.tok.colo", { colo: item.params.colo }) : "";
  const days = Number(item.params.days_left);
  p.left = item.params.reason !== "expired" && item.params.days_left && Number.isFinite(days) ? t("doctor.tok.left", { days: fmt.num(Math.max(days, 0), 0) }) : "";
  // profiles by name, never by inb_ id: the panel puts the names next to the ids it knows (eval.go withNames); an id it
  // could not name stays readable as it is. {profile} is bare (the sentence quotes it), {profiles} a quoted list.
  const ip = item.params;
  p.profile = ip.profile || ip.inbound_id || ip.inbound || "";
  p.profiles = quoteNames(t, ip.profiles || ip.inbounds || "");
  p.profiles_part = ip.profiles ? t("doctor.tok.through", { profiles: p.profiles }) : "";
  p.server_part = ip.server_name ? ` (${ip.server_name})` : "";
  return p;
}

/** "a, b" as “a”, “b” (or «a», «b»): profile names in a sentence, each in the language's quotes. */
export function quoteNames(t: T, list: string): string {
  return list
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean)
    .map((name) => t("doctor.tok.quoted", { name }))
    .join(", ");
}

/** The sentence for the item's detail code, or null when there is no code or no wording for it. */
export function itemDetail(t: T, item: Pick<DoctorItem, "detailCode">, params: Params): string | null {
  const key = `doctor.detail.${item.detailCode}`;
  return item.detailCode && hasKey(key) ? t(key, params) : null;
}
