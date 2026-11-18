// "Fresh items": the one place that says "this thing was just created". A create mutation calls markFresh("user", id) on
// success; the list item for it (wrapped in <Fresh kind id>, see fresh.tsx) takes the mark when it shows up and takes the
// board's wash once. A mark is taken once, so a refetch, a re-render or coming back to the list never washes it again; and it
// expires, so a thing that never shows up (the person went elsewhere) does not flash an hour later.
import { effectsEnabled } from "@/lib/effects";

/** How long a mark waits for its item to show up. */
export const FRESH_TTL_MS = 60_000;

const marks = new Map<string, number>(); // "kind:id" -> when it was marked
const listeners = new Set<(kind: string, id: string) => void>();
let clock: () => number = () => Date.now();

const keyOf = (kind: string, id: string) => `${kind}\u0000${id}`;

function expire() {
  const now = clock();
  for (const [k, at] of marks) if (now - at > FRESH_TTL_MS) marks.delete(k);
}

/** Says that `kind`/`id` was just created. Does nothing when the effects are off (no work then). */
export function markFresh(kind: string, id: string): void {
  if (!id || !effectsEnabled()) return;
  expire();
  marks.set(keyOf(kind, id), clock());
  listeners.forEach((l) => l(kind, id));
}

/** Whether `kind`/`id` is marked and not expired; does not take the mark. */
export function isFresh(kind: string, id: string): boolean {
  expire();
  return marks.has(keyOf(kind, id));
}

/** Takes the mark: true exactly once per mark. */
export function takeFresh(kind: string, id: string): boolean {
  expire();
  return marks.delete(keyOf(kind, id));
}

/** Called when something is marked (an item already on screen may be the one: the node whose agent just came online). */
export function subscribeFresh(l: (kind: string, id: string) => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** Tests. */
export function resetFresh(now?: () => number): void {
  marks.clear();
  clock = now ?? (() => Date.now());
}
