import type { MihomoTemplate, ResponseHeader } from "./model";

/**
 * Development only (the dev kit's scenes, dev-kit-subs-hx.tsx): what the two tabs start with besides the saved settings, for the
 * states that are not saved anywhere (a row open, a text typed and not saved, a save in flight, the question before a delete). Empty
 * in the product: every field reads as "not asked".
 */
export type HxDemo = {
  /** The row to start open (an index; null: none). */
  open?: number | null;
  /** The rows of the form instead of the saved ones (a change that is not saved yet). */
  headers?: ResponseHeader[];
  templates?: MihomoTemplate[];
  busy?: boolean;
  saved?: boolean;
  /** The template whose delete is being asked about (an index). */
  ask?: number;
  /** A file is being dragged over the text. */
  drag?: boolean;
  /** The file the person dropped was too big. */
  fileErr?: boolean;
  /** The person and the User-Agent of the preview. */
  who?: string;
  ua?: string;
  /** The list of the names the panel keeps is unfolded. */
  names?: boolean;
  /** The panel's own words about a failed save. */
  fail?: string;
  /** Click another tab once the screen stands (the question "Leave without saving?"). */
  leave?: boolean;
};

export const hxDemo: HxDemo = {};

/** A scene sets what it wants; whatever an earlier scene set is dropped. */
export function setHxDemo(d: HxDemo) {
  for (const k of Object.keys(hxDemo)) delete (hxDemo as Record<string, unknown>)[k];
  Object.assign(hxDemo, d);
}
