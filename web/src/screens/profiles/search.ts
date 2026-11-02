// The URL state of the profiles screens: the list (/profiles?protocol=vless&q=de&state=idle) and the step of a new profile
// (/profiles/new?protocol=vless), so a link, a reload and Back all land on the same view.

/** The list's state filter: a server warning, on no node, in no group, leaving through WARP. */
export const facetIds = ["warn", "idle", "nogroup", "warp"] as const;
export type FacetId = (typeof facetIds)[number];
const isFacet = (v: string): v is FacetId => (facetIds as readonly string[]).includes(v);

// the router parses "?q=123" into a number and "?q=true" into a boolean: a typed word comes back as text
const text = (v: unknown) => (typeof v === "string" ? v : typeof v === "number" || typeof v === "boolean" ? String(v) : "");
const id = (v: unknown) => (/^[a-z0-9_-]{1,32}$/.test(text(v)) ? text(v) : "");

export type ProfilesSearch = { protocol?: string; q?: string; state?: FacetId };

export function validateProfilesSearch(s: Record<string, unknown>): ProfilesSearch {
  const protocol = id(s.protocol);
  const q = text(s.q).slice(0, 64);
  const state = text(s.state);
  return { ...(protocol && { protocol }), ...(q && { q }), ...(isFacet(state) && { state }) };
}

/** /profiles/new: `node` (made from that node's page) and the chosen `protocol` (step 2; without it, step 1). */
export function validateNewProfileSearch(s: Record<string, unknown>): { node?: string; protocol?: string } {
  const node = text(s.node);
  const protocol = id(s.protocol);
  return { ...(node && { node }), ...(protocol && { protocol }) };
}
