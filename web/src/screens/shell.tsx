import { Outlet, useRouterState } from "@tanstack/react-router";
import { lazy, Suspense, useEffect, useRef, useState, type ReactNode } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { StepUpProvider } from "@/components/step-up";
import { ChatDock } from "@/components/assistant/chat";
import { type SectionId } from "@/components/nav";
import { StaleToast } from "@/components/stale-toast";
import { useT } from "@/i18n";
import { setEffectsAllowed } from "@/lib/effects";
import { useEdition } from "@/lib/edition";
import { useMenuWidth } from "@/lib/menu";
import { TopBar } from "./shell-bar";
import { Dock, Sidebar } from "./shell-nav";

// What was here moved next to its kin; these stay importable from the shell.
export { Dock, Sidebar } from "./shell-nav";
export { compactStars, GitHubButton, roleKey, VersionLine } from "./shell-parts";

// The palette (and Base UI's dialog behind it) is loaded the first time it is opened.
const Palette = lazy(() => import("@/components/palette"));

export function Shell() {
  return (
    <StepUpProvider>
      <AddNodeProvider>
        <ShellFrame />
      </AddNodeProvider>
    </StepUpProvider>
  );
}

function ShellFrame() {
  // the Cloudflare edition has no reveal animation: set before the effects below read it
  const edge = useEdition().edge;
  setEffectsAllowed(!edge);
  // matches: root, app layout, then the section route: the key that restarts the enter animation
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const section = useRouterState({ select: (s) => s.matches[2]?.routeId ?? "" });
  return (
    <ShellLayout fab={<ChatDock />} contentKey={section} pathname={pathname}>
      <Outlet />
    </ShellLayout>
  );
}

/**
 * The frame of every screen of the admin: the menu column, the top bar, the page, the dock of a phone and the assistant's
 * button. The page is as wide as 1240px, with 32px at its sides (20 from 720px, 16 on a phone). `demo` opens one of the lists
 * from the start and `current` names the open section without a route: the dev kit's scenes use them, the app does not.
 */
export function ShellLayout({
  children,
  fab,
  contentKey,
  pathname,
  current,
  demo,
}: {
  children: ReactNode;
  fab?: ReactNode;
  contentKey?: string;
  pathname?: string;
  current?: SectionId;
  demo?: { fleet?: boolean; lang?: boolean; owner?: boolean; mobile?: boolean };
}) {
  const t = useT();
  const mode = useMenuWidth();
  const [palette, setPalette] = useState(false);
  const [paletteLoaded, setPaletteLoaded] = useState(false);
  const [menuOpen, setMenuOpen] = useState(!!demo?.mobile);
  const menuRef = useRef<HTMLDivElement>(null);
  const opener = useRef<HTMLElement | null>(null);

  function openPalette() {
    setPaletteLoaded(true);
    setPalette(true);
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        openPalette();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // the phone's menu: opened by the burger or by "More" in the dock; Esc, a press outside it and a page change close it, and the
  // focus goes back to what opened it (a disclosure under the bar, not a window: the bar stays in reach)
  const close = (restore = true) => {
    setMenuOpen(false);
    if (restore) opener.current?.focus();
  };
  const toggle = () => {
    if (!menuOpen) opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setMenuOpen(!menuOpen);
  };
  useEffect(() => {
    if (!menuOpen) return;
    menuRef.current?.focus({ preventScroll: true });
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && close();
    const onPress = (e: PointerEvent) => {
      if (e.target instanceof Element && !e.target.closest("[data-mobile-menu], [data-menu-toggle]")) close(false);
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("pointerdown", onPress);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("pointerdown", onPress);
    };
    // close() only reads refs and sets state
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [menuOpen]);
  const lastPath = useRef(pathname);
  useEffect(() => {
    if (lastPath.current !== pathname) setMenuOpen(false);
    lastPath.current = pathname;
  }, [pathname]);
  useEffect(() => {
    if (mode !== "none" && !demo?.mobile) setMenuOpen(false); // the window grew past a phone: there is no such menu any more
  }, [mode, demo?.mobile]);

  return (
    <div className="min-h-dvh ps-(--menu-w)">
      <a
        href="#main"
        className="sr-only z-50 rounded-ctl bg-accent px-3 py-2 font-medium text-on-accent focus:not-sr-only focus:fixed focus:top-2 focus:inset-s-2"
      >
        {t("nav.skip")}
      </a>

      <Sidebar current={current} defaultOwnerOpen={demo?.owner} />
      <TopBar onSearch={openPalette} menuOpen={menuOpen} onToggleMenu={toggle} onCloseMenu={() => close(false)} menuRef={menuRef} demo={demo} />

      {/* keyed by the section so every screen fades and rises in; sub-pages of one section do not */}
      {/* one width rule for every screen: a centred column of at most 1240px, side padding that grows with the window (16, 20, 32).
          A screen that needs more (a wide table) puts an element with the class "app-wide" inside and the column lifts its cap. */}
      <main id="main" tabIndex={-1} className="shell-in px-4 pt-5 pb-[104px] outline-none md:px-5 md:pt-7 md:pb-10 rail:px-8">
        <div key={contentKey} className="screen-enter mx-auto max-w-[1240px] has-[.app-wide]:max-w-none">
          {children}
        </div>
      </main>

      <Dock moreOpen={menuOpen} onMore={toggle} current={current} />
      {/* on a phone the open menu has the screen: the button waits */}
      <div className={menuOpen ? "max-md:hidden" : undefined}>{fab}</div>
      <StaleToast />
      {paletteLoaded && (
        <Suspense fallback={null}>
          <Palette open={palette} onOpenChange={setPalette} />
        </Suspense>
      )}
    </div>
  );
}
