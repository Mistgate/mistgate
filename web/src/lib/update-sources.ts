import type { T } from "@/i18n";

// Where a node got (or failed to get) the update file. The node reports it, so every value here is untrusted: only a
// kind and a code this file knows is worded, anything else becomes a generic "error" or is dropped; the node's own
// text is never shown (React escapes it anyway, the point is that nothing unknown reaches the screen).

const kinds = ["panel", "github"] as const;
export type SourceKind = (typeof kinds)[number];
export const isSourceKind = (v: string): v is SourceKind => (kinds as readonly string[]).includes(v);

const codes = ["stalled", "slow", "busy", "refused", "timeout", "tls", "pin", "size", "not_found", "error"] as const;
type SourceCode = (typeof codes)[number];
const isCode = (v: string): v is SourceCode => (codes as readonly string[]).includes(v);

/** What the panel keeps of `sources_failed` (rollout.go safeSourcesFailed); a longer value is not ours. */
const listRE = /^[a-z0-9_:,]{1,200}$/;
const httpRE = /^http_[1-5]\d\d$/;
const maxItems = 8;

export type SourceFailure = { source: SourceKind; code: SourceCode | `http_${number}` };

/** "panel:stalled,github:http_404" -> [{panel, stalled}, {github, http_404}]. A kind we do not know is left out, a code we do not know is "error". */
export function parseSourcesFailed(raw: string | undefined): SourceFailure[] {
  if (!raw || !listRE.test(raw)) return [];
  const out: SourceFailure[] = [];
  for (const item of raw.split(",").slice(0, maxItems)) {
    const [source, code, ...extra] = item.split(":");
    if (!source || !isSourceKind(source) || code === undefined || extra.length > 0) continue;
    out.push({ source, code: httpRE.test(code) ? (code as `http_${number}`) : isCode(code) ? code : "error" });
  }
  return out;
}

/** "panel,github" -> the kinds we know, in order, without repeats. */
export function parseSourcesUsed(raw: string | undefined): SourceKind[] {
  if (!raw || !listRE.test(raw)) return [];
  return [...new Set(raw.split(",").filter(isSourceKind))];
}

export const sourceName = (t: T, source: SourceKind): string => t(`up.downloadSource.${source}`);

function failureText(t: T, code: SourceFailure["code"]): string {
  return httpRE.test(code) ? `HTTP ${code.slice("http_".length)}` : t(`up.downloadSource.fail.${code as SourceCode}`);
}

/** "Tried: panel (stalled), GitHub (HTTP 404)", or "" when the step carries nothing usable. */
export function triedText(t: T, raw: string | undefined): string {
  const items = parseSourcesFailed(raw);
  if (items.length === 0) return "";
  const list = items.map((f) => t("up.downloadSource.item", { source: sourceName(t, f.source), why: failureText(t, f.code) })).join(t("up.downloadSource.sep"));
  return t("up.downloadSource.tried", { list });
}

/** "panel + GitHub": the sources whose bytes the file is made of. */
export const usedText = (t: T, raw: string | undefined): string =>
  parseSourcesUsed(raw)
    .map((s) => sourceName(t, s))
    .join(" + ");
