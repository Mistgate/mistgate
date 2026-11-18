/**
 * Development only (the dev kit's scenes, dev-kit-subs-scenes.tsx): what the tabs "Who gets what", "App versions", "Names and texts" and
 * "Page" start with besides the panel's own answers, for the states that are not saved anywhere (a note under the rules, a row being
 * added, a text typed and not saved, a card open). Empty in the product: every field reads as "not asked".
 */
export type SbDemo = {
  /** The line under the rules ("Saved", with Undo after a removal). */
  note?: { text: string; undo?: () => void };
  /** A rule row is being added. */
  adding?: boolean;
  /** The rule the User-Agent of the tester lands on (an index; -1: the rest). */
  hit?: number;
  /** The row "Everything else" is the one the tester picked. */
  rest?: boolean;
  /** The User-Agent in the tester. */
  ua?: string;
  /** The "Minimum versions" table is open. */
  minOpen?: boolean;
  /** The state tile and the "in fact" class the versions tab starts on. */
  pick?: "attention" | "ok" | "below" | "unsupported" | "unknown";
  /** The texts typed and not saved yet. */
  draft?: { title?: string; announcement?: string; supportUrl?: string; template?: string };
  /** The app card of the page tab that starts open (an index; -1: none). */
  appOpen?: number;
  /** The platform and the kind the "Add app" bar starts on. */
  addPlatform?: number;
};

export const sbDemo: SbDemo = {};

/** A scene sets what it wants; whatever an earlier scene set is dropped. */
export function setSbDemo(d: SbDemo) {
  for (const k of Object.keys(sbDemo)) delete (sbDemo as Record<string, unknown>)[k];
  Object.assign(sbDemo, d);
}
