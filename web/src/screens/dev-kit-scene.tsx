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
  if (params.get("fx") === "1") setEffectsEnabled(true);
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
