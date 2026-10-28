/** The sources of the Reachability page, in the order of their list; the id is what `?source=` carries. */
export const sourceIds = ["globalping", "lists", "tspu", "cheburcheck", "bsbord", "klick"] as const;
export type SourceId = (typeof sourceIds)[number];

export const isSourceId = (v: unknown): v is SourceId => sourceIds.includes(v as SourceId);

/** Search-param validation of /reachability: the source picked in the Sources section, when it is a known one. */
export const validateReachSearch = (s: Record<string, unknown>): { source?: SourceId } => (isSourceId(s.source) ? { source: s.source } : {});
