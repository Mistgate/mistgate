import { Dialog } from "@base-ui/react/dialog";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";
import { lazy, Suspense, useEffect, useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { StepUpProvider } from "@/components/step-up";
import { Brand, Logo, Wordmark } from "@/components/brand";
import { navKey, sections, type SectionId } from "@/components/nav";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { UpdateToast } from "@/components/update-toast";
import { Avatar, Kbd } from "@/components/ui/bits";
import { Icon, NavIcon } from "@/components/ui/icons";
import { StatusPill } from "@/components/ui/status";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { healthKind, problemCount, useFleetHealth, useFleetSummary } from "@/lib/fleet";
import { useAwaitingApprovals } from "@/lib/integrations";
import { basepath } from "@/lib/api";
import { meQuery, useSignOut } from "@/lib/session";
import { cx } from "@/lib/cx";

// The palette (and Base UI's dialog behind it) is loaded the first time it is opened.
const Palette = lazy(() => import("@/components/palette"));

export const roleKey: Record<Role, MessageKey> = {
  [Role.UNSPECIFIED]: "role.unknown",
  [Role.OWNER]: "role.owner",
  [Role.HELPER]: "role.helper",
  [Role.READONLY]: "role.readonly",
};

const isMac = /Mac|iPhone|iPad/.test(navigator.platform);
const paletteKeys = isMac ? ["⌘", "K"] : ["Ctrl", "K"];

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
  const t = useT();
  const [palette, setPalette] = useState(false);
  const [paletteLoaded, setPaletteLoaded] = useState(false);
  // matches: root, app layout, then the section route: the key that restarts the enter animation
  const section = useRouterState({ select: (s) => s.matches[2]?.routeId ?? "" });

  function openPalette() {
    setPaletteLoaded(true);
    setPalette(true);
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteLoaded(true);
        setPalette(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  return (
    <div className="min-h-dvh md:pl-56">
      <a
        href="#main"
        className="sr-only z-50 rounded-ctl bg-accent px-3 py-2 font-semibold text-on-accent focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        {t("nav.skip")}
      </a>

      <Sidebar />
      <TopBar onSearch={openPalette} />
      <MobileHeader onSearch={openPalette} />

      {/* keyed by the section so every screen fades and rises in; sub-pages of one section do not */}
      {/* one width rule for every screen: a centred column of at most 1360px, side padding that grows with the window.
          A screen that needs more (a wide table) puts an element with the class "app-wide" inside and the column lifts its cap. */}
      <main id="main" className="px-4 pt-1.5 pb-24 md:px-[clamp(24px,4vw,64px)] md:pt-6 md:pb-8">
        <div key={section} className="screen-enter mx-auto max-w-[1360px] has-[.app-wide]:max-w-none">
          <Outlet />
        </div>
      </main>

      <Dock />
      <UpdateToast />
      {paletteLoaded && (
        <Suspense fallback={null}>
          <Palette open={palette} onOpenChange={setPalette} />
        </Suspense>
      )}
    </div>
  );
}

/** A count in a nav row: red for faults (health), amber for something that waits for the owner. `label` is spoken instead of the bare number. */
function CountBadge({ n, tone = "danger", label }: { n: number; tone?: "danger" | "warn"; label?: string }) {
  return (
    <span
      role={label ? "img" : undefined}
      aria-label={label}
      title={label}
      className={cx(
        "flex h-[18px] min-w-[18px] items-center justify-center rounded-[9px] px-[5px] font-mono text-[11px] font-bold",
        tone === "warn" ? "bg-warn-soft text-warn-text" : "bg-danger-soft text-danger-text",
      )}
    >
      {n}
    </span>
  );
}

/** The Health badge: the same number of problems as the header pill, red when something is broken or critical, amber otherwise. */
function useProblemsBadge(): { n: number; tone: "danger" | "warn" } {
  const s = useFleetSummary();
  const n = problemCount(s);
  return { n, tone: healthKind(s.broken, n, s.critical) === "bad" ? "danger" : "warn" };
}

const linkCls =
  "flex h-[34px] items-center gap-2.5 rounded-ctl px-2.5 text-[13px] font-semibold text-muted transition-colors duration-200 data-[status=active]:bg-surface-2 data-[status=active]:font-bold data-[status=active]:text-fg";

function Sidebar() {
  const t = useT();
  const signOut = useSignOut();
  const { data } = useSuspenseQuery(meQuery);
  const { nodes, users } = useFleetSummary();
  const problems = useProblemsBadge();
  const approvals = useAwaitingApprovals();
  const name = data.admin?.displayName ?? "";
  const counts: Partial<Record<SectionId, number>> = { nodes, users };

  return (
    <aside className="fixed inset-y-0 left-0 hidden w-56 flex-col gap-0.5 border-r border-line px-2.5 py-3.5 md:flex">
      <Link to="/" className="mb-3 flex h-[34px] items-center px-2" aria-label={t("nav.overview")}>
        <Brand />
      </Link>
      <nav aria-label={t("nav.main")} className="flex flex-col gap-0.5">
        {sections.map((s) => {
          const count = counts[s.id];
          return (
            <div key={s.id} className="contents">
              {"group" in s && (
                <div className="px-2.5 pt-[18px] pb-1.5 text-[10px] font-bold tracking-[0.12em] text-faint">
                  {t(s.group)}
                </div>
              )}
              <Link to={s.to} activeOptions={{ exact: s.to === "/" }} className={linkCls}>
                <NavIcon name={s.id} />
                <span className="min-w-0 flex-1 truncate">{t(navKey(s.id))}</span>
                {!!count && <span className="font-mono text-[11px] font-medium text-muted">{count}</span>}
                {s.id === "health" && problems.n > 0 && <CountBadge n={problems.n} tone={problems.tone} label={t.n("health.problems", problems.n)} />}
                {s.id === "integrations" && approvals > 0 && <CountBadge n={approvals} tone="warn" label={t.n("int.badge", approvals)} />}
              </Link>
            </div>
          );
        })}
      </nav>
      <div className="flex-1" />
      <div className="flex items-center gap-2.5 border-t border-line px-2 py-2.5">
        <Avatar name={name} size={28} />
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="truncate text-[13px] font-bold">{name}</span>
          <span className="text-[11px] text-muted lowercase">{t(roleKey[data.admin?.role ?? Role.UNSPECIFIED])}</span>
        </div>
        <button type="button" onClick={signOut} className="text-xs font-semibold text-muted transition-colors hover:text-fg">
          {t("header.signOut")}
        </button>
      </div>
      <VersionLine className="px-2" />
    </aside>
  );
}

/**
 * The panel's version and, when the panel names one (serve --source-url), the link to its source code (AGPL-3.0
 * section 13). Admin only: the decoy site and the user page never show it.
 */
export function VersionLine({ className }: { className?: string }) {
  const t = useT();
  const { data } = useSuspenseQuery(meQuery);
  return (
    <p className={cx("font-mono text-[11px] text-muted", className)}>
      {t("header.version", { version: data.version })}
      {data.sourceUrl && (
        <>
          {" · "}
          <a href={data.sourceUrl} target="_blank" rel="noopener noreferrer" className="underline-offset-2 transition-colors hover:text-fg hover:underline">
            {t("header.source")}
          </a>
        </>
      )}
    </p>
  );
}

function TopBar({ onSearch }: { onSearch: () => void }) {
  const t = useT();
  const health = useFleetHealth();
  return (
    <header className="sticky top-0 z-10 hidden h-14 items-center gap-3 border-b border-line bg-canvas px-5 md:flex">
      <button
        type="button"
        onClick={onSearch}
        className="flex h-9 max-w-[420px] flex-1 items-center gap-2 rounded-ctl border border-line bg-surface pr-1.5 pl-3 text-[13px] text-muted"
      >
        <Icon name="search" size={15} />
        <span className="flex-1 truncate text-left">{t("header.search")}</span>
        <Kbd keys={paletteKeys} />
      </button>
      <div className="flex-1" />
      <Link to={health.to} className="flex">
        <StatusPill kind={health.kind} label={health.label} />
      </Link>
      <LangToggle />
      <ThemeToggle />
    </header>
  );
}

function MobileHeader({ onSearch }: { onSearch: () => void }) {
  const t = useT();
  const health = useFleetHealth();
  return (
    <header className="sticky top-0 z-10 flex h-14 items-center gap-2.5 bg-canvas px-4 pt-2 md:hidden">
      <Logo size={32} />
      <Wordmark size={16} />
      <div className="flex-1" />
      <Link to={health.to} aria-label={health.label} className="flex">
        <StatusPill kind={health.kind} label={health.short} sm />
      </Link>
      <button
        type="button"
        onClick={onSearch}
        aria-label={t("header.search")}
        className="grid size-9 place-items-center rounded-ctl border border-line bg-surface"
      >
        <Icon name="search" />
      </button>
    </header>
  );
}

const tabCls =
  "relative flex min-w-0 flex-1 flex-col items-center justify-center gap-1 rounded-[14px] text-[11px] font-bold text-muted transition-colors duration-300 data-[status=active]:bg-surface-2 data-[status=active]:text-fg";

/** Floating dock: three sections and "More", which opens a sheet with the rest. */
function Dock() {
  const t = useT();
  const [more, setMore] = useState(false);
  const problems = useProblemsBadge();
  const approvals = useAwaitingApprovals();
  const base = basepath.replace(/\/$/, "");
  const path = useRouterState({ select: (s) => s.location.pathname }).replace(base, ""); // tolerate a prefixed pathname
  const primary = sections.slice(0, 3);
  const rest = sections.slice(3);
  const moreActive = more || rest.some((s) => path.startsWith(s.to));

  return (
    <nav
      aria-label={t("nav.main")}
      className="fixed inset-x-3.5 bottom-3.5 z-10 flex h-[60px] gap-1 rounded-dock border border-line bg-dock p-1.5 shadow-(--shadow-dock) backdrop-blur-[16px] md:hidden"
    >
      {primary.map((s) => (
        <Link key={s.id} to={s.to} activeOptions={{ exact: s.to === "/" }} className={tabCls}>
          <NavIcon name={s.id} size={20} />
          <span className="max-w-full truncate">{t(navKey(s.id))}</span>
          {s.id === "overview" && problems.n > 0 && (
            <span aria-hidden className={cx("absolute top-1.5 right-3.5 size-[7px] rounded-full", problems.tone === "danger" ? "bg-danger" : "bg-warn")} />
          )}
        </Link>
      ))}
      <Dialog.Root open={more} onOpenChange={setMore}>
        <Dialog.Trigger data-status={moreActive ? "active" : undefined} className={tabCls}>
          <NavIcon name="more" size={20} />
          <span>{t("nav.more")}</span>
          {approvals > 0 && <span aria-hidden className="absolute top-1.5 right-3.5 size-[7px] rounded-full bg-warn" />}
        </Dialog.Trigger>
        <Dialog.Portal>
          <Dialog.Backdrop className="fixed inset-0 z-20 bg-black/45 transition-opacity duration-200 data-ending-style:opacity-0 data-starting-style:opacity-0" />
          <Dialog.Popup className="fixed inset-x-0 bottom-0 z-30 flex flex-col gap-0.5 rounded-t-sheet border-t border-line bg-surface px-3 pt-2.5 pb-7 outline-none transition-transform duration-300 ease-out-soft data-ending-style:translate-y-full data-starting-style:translate-y-full">
            <div aria-hidden className="mx-auto mb-2.5 h-1 w-9 rounded-sm bg-line" />
            <Dialog.Title className="sr-only">{t("nav.more")}</Dialog.Title>
            {rest.map((s) => (
              <Link
                key={s.id}
                to={s.to}
                onClick={() => setMore(false)}
                className="flex h-12 items-center gap-3 rounded-field px-3 text-[15px] font-semibold"
              >
                <NavIcon name={s.id} size={20} />
                <span className="flex-1">{t(navKey(s.id))}</span>
                {s.id === "health" && problems.n > 0 && <CountBadge n={problems.n} tone={problems.tone} label={t.n("health.problems", problems.n)} />}
                {s.id === "integrations" && approvals > 0 && <CountBadge n={approvals} tone="warn" label={t.n("int.badge", approvals)} />}
                <span aria-hidden className="text-base text-faint">
                  ›
                </span>
              </Link>
            ))}
            <div className="mx-3 my-1.5 h-px bg-line" />
            <div className="flex h-[52px] items-center gap-2.5 px-3">
              <span className="flex-1 text-[15px] font-semibold">{t("more.theme")}</span>
              <ThemeToggle />
            </div>
            <div className="flex h-[52px] items-center gap-2.5 px-3">
              <span className="flex-1 text-[15px] font-semibold">{t("more.language")}</span>
              <LangToggle />
            </div>
            <SignOutRow />
          </Dialog.Popup>
        </Dialog.Portal>
      </Dialog.Root>
    </nav>
  );
}

function SignOutRow() {
  const t = useT();
  const signOut = useSignOut();
  return (
    <>
      <button
        type="button"
        onClick={signOut}
        className={cx("flex h-12 items-center px-3 text-left text-[15px] font-semibold text-danger-text")}
      >
        {t("header.signOut")}
      </button>
      <VersionLine className="px-3 pt-1" />
    </>
  );
}
