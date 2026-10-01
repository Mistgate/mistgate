import { useSyncExternalStore } from "react";

// The admin has one breakpoint (45rem = 720px): tables become cards, the sidebar a dock, modals sheets.
// Tailwind's `md:` covers layout; this is for the few places where the list itself differs on a phone.
const query = "(max-width: 44.99rem)";

function subscribe(cb: () => void) {
  const m = window.matchMedia(query);
  m.addEventListener("change", cb);
  return () => m.removeEventListener("change", cb);
}

export function useIsPhone(): boolean {
  return useSyncExternalStore(
    subscribe,
    () => window.matchMedia(query).matches,
    () => false,
  );
}
