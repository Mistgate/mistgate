import { useNavigate, useSearch } from "@tanstack/react-router";
import { createContext, useContext } from "react";
import type { ReachSearch } from "./search";

// Where the page keeps its tab and its open source: the address (`?tab=`, `?source=`). A scene of the dev kit is a page of its own that must not
// leave itself, so it brings a place of its own (a state, not the address) through `ReachNav`.

export type Nav = { search: ReachSearch; go: (search: ReachSearch) => void };
export const ReachNav = createContext<Nav | null>(null);

/** The tab and the source the page shows, and the way to change them: the address, unless a scene says otherwise. */
export function useReachNav(): Nav {
  const given = useContext(ReachNav);
  const search = useSearch({ strict: false }) as ReachSearch;
  const navigate = useNavigate();
  return given ?? { search, go: (next) => void navigate({ to: "/reachability", search: next, replace: true, resetScroll: false }) };
}
