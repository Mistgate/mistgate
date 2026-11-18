import { useBlocker, useRouter } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { isLang, setLang } from "@/i18n";
import { setEffectsEnabled } from "@/lib/effects";
import { setTheme } from "@/lib/theme";

// Development only: what a scene page of the dev kit (/dev-kit?only=<page>&state=…&lang=…&theme=…&fx=1) does around its screen, so that opening
// one leaves nothing on the device: the language, the theme and the effects of the URL are applied in memory only (the writes to the device's
// storage are taken back at once, and whatever a click inside the scene writes goes at pagehide), and a link inside the scene does not leave it.

const take = (): [string, string][] => {
  try {
    return Object.keys(localStorage).map((k) => [k, localStorage.getItem(k) ?? ""]);
  } catch {
    return [];
  }
};

const restore = (was: [string, string][]) => {
  try {
    const keep = new Map(was);
    for (const k of Object.keys(localStorage)) if (!keep.has(k)) localStorage.removeItem(k);
    for (const [k, v] of keep) if (localStorage.getItem(k) !== v) localStorage.setItem(k, v);
  } catch {
    // no storage: nothing was written
  }
};

/** Applies `lang`, `theme` and `fx` of the URL for this page only: the device's own choices stay as they were. Call once, before the first render. */
export function applyScene(params: URLSearchParams) {
  const was = take();
  const lang = params.get("lang");
  if (isLang(lang)) setLang(lang);
  const theme = params.get("theme");
  if (theme === "light" || theme === "dark") setTheme(theme);
  setEffectsEnabled(params.get("fx") === "1", false);
  restore(was);
  // what a click inside the scene writes (the folded menu, a switch) goes when the page does
  if (typeof window !== "undefined") window.addEventListener("pagehide", () => restore(was), { once: true });
}

/** A link of the scene (the menu, "Open the node") stays on the scene: its click is taken at the door, a link out of the panel still opens. */
export function SceneGuard({ children }: { children: ReactNode }) {
  return (
    <div
      className="contents"
      onClickCapture={(e) => {
        const a = (e.target as HTMLElement).closest?.("a[href]");
        const href = a?.getAttribute("href") ?? "";
        if (a && href.startsWith("/") && !e.defaultPrevented) {
          e.preventDefault();
          e.stopPropagation();
        }
      }}
    >
      {children}
    </div>
  );
}

// the search of a real page: what a tab, a sort or a filter writes there (the scene's own keys, ?only= ?state= ?fx= …, are not in it)
const screenKeys = ["tab", "sv", "view", "way", "add", "map", "full", "from", "focus", "before", "size", "page", "sort", "desc", "group", "create"];

/**
 * A scene is one page of the dev kit standing in for a real route: a tab, a section, a sort or a filter of the screen asks the router for
 * that route (/nodes/<id>?tab=doctor) and would leave the scene for a page with no data behind it. Here the scene keeps its own address and
 * takes the search of what the screen asked for (so the click shows the tab, as the page would), and the links stay on it (SceneGuard).
 */
export function SceneKeep({ children }: { children: ReactNode }) {
  const router = useRouter();
  useBlocker({
    shouldBlockFn: ({ next }) => {
      if (next.pathname === location.pathname) return false;
      // a number stays a number: the router writes a string that looks like one in quotes (?fx=0 would come back as ?fx="0")
      const here = Object.fromEntries([...new URLSearchParams(location.search)].map(([k, v]) => [k, /^\d+$/.test(v) ? Number(v) : v]));
      for (const k of screenKeys) delete here[k];
      void router.navigate({ to: location.pathname as never, search: { ...here, ...(next.search as Record<string, unknown>) } as never, replace: true });
      return true;
    },
    enableBeforeUnload: false,
  });
  return <SceneGuard>{children}</SceneGuard>;
}
