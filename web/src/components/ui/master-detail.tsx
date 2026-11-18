import type { KeyboardEvent } from "react";

// A list of things, each with its state, and the one that is open (Reachability's sources, a node's ways in): the shared moving part.

/** Arrows (and Home, End) walk the rows of a list, Enter or Space opens: the list is one stop for Tab. */
export function walkTabs(e: KeyboardEvent<HTMLElement>) {
  const tabs = [...e.currentTarget.querySelectorAll<HTMLElement>("[role=tab]")];
  const i = tabs.indexOf(document.activeElement as HTMLElement);
  const to = ({ ArrowDown: i + 1, ArrowRight: i + 1, ArrowUp: i - 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 } as Record<string, number>)[e.key];
  if (i < 0 || to === undefined) return;
  e.preventDefault();
  tabs[(to + tabs.length) % tabs.length]!.focus();
}
