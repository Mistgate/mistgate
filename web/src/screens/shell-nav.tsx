import { Tooltip } from "@base-ui/react/tooltip";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import type { CSSProperties, ReactElement, ReactNode, Ref } from "react";
import { UpdateCard, useUpdateNotice, vee } from "@/components/update-card";
import { Brand, Logo } from "@/components/brand";
import { navKey, sections, type Section, type SectionId } from "@/components/nav";
import { chooseLanguage, ThemeToggle } from "@/components/prefs";
import { playWave, originOf } from "@/components/fx/wave";
import { Avatar } from "@/components/ui/bits";
import { NavIcon, navPaths, type NavIconName } from "@/components/ui/icons";
import { Menu, MenuItem, MenuLabel, MenuLink } from "@/components/ui/menu";
import type { StatusKind } from "@/components/ui/status";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { langNames, langShort, langs, localeOf, useLang, useT, type T } from "@/i18n";
import { basepath } from "@/lib/api";
import { cx } from "@/lib/cx";
import { fleetDots, healthKind, problemCount, useFleetNodes, useFleetSummary } from "@/lib/fleet";
import { useAwaitingApprovals } from "@/lib/integrations";
import { setMenuFolded, useMenuFolded, useMenuMode } from "@/lib/menu";
import { nodeKind } from "@/lib/node-status";
import { meQuery, useSignOut } from "@/lib/session";
import { themeNow } from "@/lib/theme";
import { GitHubButton, roleKey, VersionLine } from "./shell-parts";

// The menu column (a full menu, or a rail of icons), the phone's dock and the phone's full menu. Sizes, spacing and motion
// are the redesign board's (pn.css, .side .ni .ic2 .upd .me .mm); the sections and their groups are components/nav.ts.

// ---------------------------------------------------------------------------------------------------
// What the rows show beside their names

export type NavMeta = {
  /** The fleet has nodes: only then are there counts to show beside Nodes and Users. */
  fleet: boolean;
  nodes: number;
  users: number;
  online: number;
  dots: StatusKind[];
  problems: { n: number; tone: "bad" | "warn" };
  approvals: number;
  update: ReturnType<typeof useUpdateNotice>;
};

export function useNavMeta(): NavMeta {
  const s = useFleetSummary();
  const cards = useFleetNodes();
  const approvals = useAwaitingApprovals();
  const update = useUpdateNotice();
  const n = problemCount(s);
  return {
    fleet: s.nodes > 0,
    nodes: s.nodes,
    users: s.users,
    online: s.usersOnline,
    dots: fleetDots(cards.map(nodeKind)),
    problems: { n, tone: healthKind(s.broken, n, s.critical) === "bad" ? "bad" : "warn" },
    approvals,
    update,
  };
}

type Pill = { text: string; tone?: "warn" | "sky" | "bad"; label?: string };

/** The badge of a row: a small pill (a count of problems, a version), or nothing. */
function pillOf(id: SectionId, m: NavMeta, t: T): Pill | null {
  if (id === "health" && m.problems.n > 0) return { text: String(m.problems.n), tone: m.problems.tone, label: t.n("health.problems", m.problems.n) };
  if (id === "ai" && m.approvals > 0) return { text: String(m.approvals), tone: "sky", label: t.n("int.badge", m.approvals) };
  if (id === "updates" && m.update) {
    const u = m.update;
    return { text: u.kind === "panel" ? vee(u.version) : String(u.outdated), label: t(u.kind === "panel" ? "up.toast.panelTitle" : "up.toast.nodesTitle", { version: vee(u.version) }) };
  }
  return null;
}

const pillTone = {
  none: "bg-step text-sec",
  warn: "bg-warn-soft text-warn-text",
  sky: "bg-tone-sky-bg text-tone-sky",
  bad: "bg-danger-soft text-danger-text",
} as const;

function PillBadge({ pill }: { pill: Pill }) {
  return (
    <span
      role={pill.label ? "img" : undefined}
      aria-label={pill.label}
      title={pill.label}
      className={cx("ms-auto inline-grid h-6 min-w-6 flex-none place-items-center rounded-ctl px-2 font-mono text-[11px] leading-4", pillTone[pill.tone ?? "none"])}
    >
      {pill.text}
    </span>
  );
}

/** The row's counter in the full menu: the dots of the nodes and their number, the people online, or a pill. */
function SideBadge({ id, m }: { id: SectionId; m: NavMeta }) {
  const t = useT();
  if (id === "nodes" && m.fleet)
    return (
      <span className="ms-auto inline-flex items-center gap-[3px] font-mono text-xs text-muted">
        {m.dots.map((k, i) => (
          <i key={i} aria-hidden className={`tone-${k} size-1.5 flex-none rounded-full bg-(--c) transition-colors duration-[400ms] ease-out`} />
        ))}
        <span className="ms-1.5">{m.nodes}</span>
      </span>
    );
  if (id === "users" && m.fleet && m.users > 0) return <span className="ms-auto font-mono text-xs text-accent-text">{t("shell.nav.online", { n: m.online })}</span>;
  const pill = pillOf(id, m, t);
  return pill ? <PillBadge pill={pill} /> : null;
}

/** The same counters as plain words, for the phone's menu (pn.css .mm .ni .n). */
function mobileBadge(id: SectionId, m: NavMeta, t: T): string {
  if (id === "nodes" && m.fleet) return String(m.nodes);
  if (id === "users" && m.fleet && m.users > 0) return t("shell.nav.online", { n: m.online });
  return pillOf(id, m, t)?.text ?? "";
}

// ---------------------------------------------------------------------------------------------------
// Which row is current

/** The path without the panel's base path; `current` (a scene of the dev kit) says which section is open without a route. */
function useIsCurrent(current?: SectionId): (s: Section) => boolean {
  const base = basepath.replace(/\/$/, "");
  const path = useRouterState({ select: (s) => s.location.pathname }).replace(base, "") || "/";
  return (s) => (current ? current === s.id : s.to === "/" ? path === "/" : path === s.to || path.startsWith(`${s.to}/`));
}

const toneVar = (id: NavIconName): CSSProperties => ({ "--tc": navPaths[id][1] === "muted" ? "var(--muted)" : `var(--tone-${navPaths[id][1]})` }) as CSSProperties;

/** The icon on its tile: the section's tone at 14 % on the surface, 22 % when the row is hovered or current. */
function Tile({ id, size = 28, iconSize = 16, className }: { id: NavIconName; size?: number; iconSize?: number; className?: string }) {
  return (
    <span
      aria-hidden
      className={cx(
        "grid flex-none place-items-center rounded-ctl bg-[color-mix(in_srgb,var(--tc)_14%,var(--surface))] transition-colors duration-150 ease-out group-hover/ni:bg-[color-mix(in_srgb,var(--tc)_22%,var(--surface))] group-aria-[current=page]/ni:bg-[color-mix(in_srgb,var(--tc)_22%,var(--surface))]",
        className,
      )}
      style={{ width: size, height: size }}
    >
      <NavIcon name={id} size={iconSize} />
    </span>
  );
}

/** The name of a rail row on hover and keyboard focus, at once, to the side the menu opens to (pn.css .rail .ni::after). Portalled, so the menu's own scrolling cannot cut it. */
function RailTip({ label, show, children }: { label: string; show: boolean; children: ReactNode }) {
  if (!show) return <>{children}</>;
  return (
    <Tooltip.Root>
      {children}
      <Tooltip.Portal>
        <Tooltip.Positioner side="inline-end" sideOffset={16} className="z-50">
          <Tooltip.Popup className="rounded-ctl border border-line bg-surface-2 px-2.5 py-1.5 text-[13px] leading-4 font-medium whitespace-nowrap text-fg transition-[opacity,transform] duration-150 ease-out data-ending-style:opacity-0 data-starting-style:opacity-0 data-ending-style:ltr:-translate-x-1 data-starting-style:ltr:-translate-x-1 data-ending-style:rtl:translate-x-1 data-starting-style:rtl:translate-x-1">
            {label}
          </Tooltip.Popup>
        </Tooltip.Positioner>
      </Tooltip.Portal>
    </Tooltip.Root>
  );
}
const railTrigger = (el: ReactElement) => <Tooltip.Trigger delay={0} closeDelay={0} render={el} />;

// ---------------------------------------------------------------------------------------------------
// The menu column

const marker = "before:absolute before:top-2.5 before:bottom-2.5 before:w-[3px] before:rounded-e-[2px] before:bg-accent-mark before:content-['']";

function SideLink({ s, rail, active, meta }: { s: Section; rail: boolean; active: boolean; meta: NavMeta }) {
  const t = useT();
  const label = t(navKey(s.id));
  const link = (
    <Link
      to={s.to}
      activeOptions={{ exact: s.to === "/" }}
      aria-current={active ? "page" : undefined}
      style={toneVar(s.id)}
      className={cx(
        "group/ni relative flex h-10 shrink-0 items-center rounded-field text-sm text-sec outline-none transition-colors duration-150 ease-out hover:bg-step hover:text-fg focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-accent-mark",
        rail ? "w-11 justify-center" : "w-full gap-3 px-2",
        active && cx("bg-surface-2 font-medium text-fg", marker, rail ? "before:inset-s-[-14px]" : "before:inset-s-[-12px]"),
      )}
    >
      <Tile id={s.id} />
      {rail ? (
        <span className="sr-only">{label}</span>
      ) : (
        <>
          <span className="min-w-0 flex-1 truncate">{label}</span>
          <SideBadge id={s.id} m={meta} />
        </>
      )}
    </Link>
  );
  return (
    <RailTip label={label} show={rail}>
      {rail ? railTrigger(link) : link}
    </RailTip>
  );
}

/** The glyph of the fold button: a window with its side column; the column's line steps toward the edge on hover (shell.css). */
function FoldGlyph() {
  return (
    <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="fold-glyph">
      <rect x="3.5" y="4.5" width="17" height="15" rx="3.5" />
      <path className="sb" d="M9.5 8v8" />
    </svg>
  );
}

function Glyph({ d }: { d: string }) {
  return (
    <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" className="flex-none">
      <path d={d} />
    </svg>
  );
}

/** The owner's button at the foot of the menu and the menu that grows out of it: sign-in and passkeys, sessions, settings, sign out. */
function OwnerMenu({ rail, defaultOpen }: { rail: boolean; defaultOpen?: boolean }) {
  const t = useT();
  const signOut = useSignOut();
  const { data } = useSuspenseQuery(meQuery);
  const name = data.admin?.displayName ?? "";
  const role = t(roleKey[data.admin?.role ?? Role.UNSPECIFIED]);
  const aria = t("shell.owner.aria", { name, role });
  const button = (
    <button
      type="button"
      aria-label={aria}
      className={cx(
        "flex cursor-pointer items-center rounded-field text-start outline-none transition-colors duration-150 ease-out hover:bg-step focus-visible:outline-2 focus-visible:outline-accent-mark data-popup-open:bg-step",
        rail ? "size-11 justify-center" : "w-full gap-3 p-2",
      )}
    >
      <Avatar name={name} size={32} />
      {!rail && (
        <>
          <span className="flex min-w-0 flex-1 flex-col">
            <span className="truncate text-sm">{name}</span>
            <span className="truncate text-xs leading-4 text-muted lowercase">{role}</span>
          </span>
          <Glyph d="M8 10l4-4 4 4M8 14l4 4 4-4" />
        </>
      )}
    </button>
  );
  return (
    <RailTip label={`${name} · ${role}`} show={rail}>
      <Menu
        pop
        defaultOpen={defaultOpen}
        side={rail ? "inline-end" : "top"}
        align={rail ? "end" : "start"}
        aria-label={aria}
        trigger={rail ? railTrigger(button) : button}
      >
        <MenuLabel>
          {t("shell.owner.signedIn")} <b>{name}</b>
        </MenuLabel>
        <MenuLink render={<Link to="/settings/$section" params={{ section: "security" }} />}>
          <Glyph d="M7 11V8a5 5 0 0 1 10 0v3M6 11h12v9H6z" />
          {t("shell.owner.security")}
        </MenuLink>
        <MenuLink render={<Link to="/settings/$section" params={{ section: "sessions" }} />}>
          <Glyph d="M4 5h16v11H4zM9 20h6M12 16v4" />
          {t("shell.owner.sessions")}
        </MenuLink>
        <MenuLink render={<Link to="/settings" />}>
          <Glyph d="M4 7h10M18 7h2M4 12h4M12 12h8M4 17h12M14 5v4M8 10v4M16 15v4" />
          {t("nav.settings")}
        </MenuLink>
        <MenuItem danger onClick={signOut}>
          <Glyph d="M14 4h5v16h-5M10 8l-4 4 4 4M6 12h9" />
          {t("header.signOut")}
        </MenuItem>
        {/* the rail has no version line of its own, and the source link (AGPL-3.0 section 13) must stay one step away */}
        {rail && <VersionLine className="mt-1.5 border-t border-line px-2.5 pt-2 pb-1" />}
      </Menu>
    </RailTip>
  );
}

/**
 * Exported for the dev kit's page frames (dev-kit-bulk.tsx); the app shows it only in the shell. 264px wide, or a 72px rail
 * of icons (the person folds it with the button at its top; below 1000px it is a rail by itself). Gone on a phone, where the
 * dock and the menu under the top bar take over. `current` names the open section when there is no route (the dev kit).
 */
export function Sidebar({ current, defaultOwnerOpen }: { current?: SectionId; defaultOwnerOpen?: boolean } = {}) {
  const t = useT();
  const mode = useMenuMode();
  const folded = useMenuFolded();
  const meta = useNavMeta();
  const isCurrent = useIsCurrent(current);
  const rail = mode !== "full";
  const fold = mode === "full" || folded; // below 1000px the rail is the window's doing: no button
  return (
    <aside
      data-menu={rail ? "rail" : "full"}
      className={cx("fixed inset-y-0 inset-s-0 z-30 hidden flex-col gap-0.5 border-e border-line bg-surface p-3 md:flex", rail ? "w-[72px] items-center" : "w-[264px]")}
    >
      <div className={cx("shell-in mt-1 mb-3 flex", rail ? "flex-col items-center gap-2" : "ms-2 items-center justify-between")}>
        <Link to="/" aria-label={t("nav.overview")} className={"flex h-10 items-center rounded-ctl outline-none focus-visible:outline-2 focus-visible:outline-accent-mark"}>
          {rail ? <Logo size={28} /> : <Brand logoSize={28} textSize={17} />}
        </Link>
        {fold && (
          <button
            type="button"
            aria-label={t("shell.menu.toggle")}
            aria-pressed={folded}
            onClick={(e) => {
              playWave(originOf(e.currentTarget), { light: themeNow() === "light" });
              setMenuFolded(!folded);
            }}
            className="fold-btn grid size-8 cursor-pointer place-items-center rounded-ctl text-muted transition-colors duration-150 ease-out hover:bg-step hover:text-fg"
          >
            <FoldGlyph />
          </button>
        )}
      </div>
      {/* the marker of the current row sits 12px outside it: the list is padded to make room, so its scrolling cannot cut it */}
      <nav aria-label={t("nav.main")} className={cx("shell-in -mx-3 flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto px-3 [scrollbar-width:none]", rail && "items-center")}>
        {sections.map((s) => (
          <div key={s.id} className="contents">
            {"group" in s &&
              (rail ? (
                <div aria-hidden className="mx-1.5 my-2.5 h-px self-stretch bg-line" />
              ) : (
                <div className="mt-3 shrink-0 border-t border-line px-2 pt-4 pb-2 text-xs leading-4 text-muted">{t(s.group)}</div>
              ))}
            <SideLink s={s} rail={rail} active={isCurrent(s)} meta={meta} />
          </div>
        ))}
      </nav>
      <div className={cx("shell-in mt-4 flex flex-col gap-2", rail && "items-center")}>
        {!rail && <UpdateCard />}
        <OwnerMenu rail={rail} defaultOpen={defaultOwnerOpen} />
        {!rail && <VersionLine className="px-2 pb-1" />}
      </div>
    </aside>
  );
}

// ---------------------------------------------------------------------------------------------------
// A phone: the dock and the full menu

const dockTab =
  "group/ni relative flex h-[60px] min-w-0 flex-1 cursor-pointer flex-col items-center gap-1 rounded-2xl px-0 pt-2 pb-1.5 text-[11px] leading-4 font-medium text-muted outline-none transition-colors duration-150 ease-out focus-visible:outline-2 focus-visible:outline-accent-mark";

/**
 * The floating dock of a phone: three sections and "More", which opens the full menu under the top bar. The Nodes page's bulk
 * bar takes its place (index.css). Exported for the dev kit's page frames; without the arguments "More" does nothing.
 */
export function Dock({ moreOpen = false, onMore, current }: { moreOpen?: boolean; onMore?: () => void; current?: SectionId } = {}) {
  const t = useT();
  const meta = useNavMeta();
  const isCurrent = useIsCurrent(current);
  const primary = sections.slice(0, 3);
  // a dot on "More" while something in the sections it holds asks for a look
  const attention = meta.problems.n > 0 || meta.approvals > 0;
  return (
    <nav
      data-dock
      aria-label={t("nav.main")}
      className="fixed inset-x-3.5 bottom-3.5 z-10 flex gap-1 rounded-[22px] border border-line bg-surface p-1.5 shadow-(--shadow-dock) md:hidden"
    >
      {primary.map((s) => {
        const active = isCurrent(s);
        return (
          <Link
            key={s.id}
            to={s.to}
            activeOptions={{ exact: s.to === "/" }}
            aria-current={active ? "page" : undefined}
            style={toneVar(s.id)}
            className={cx(dockTab, active && "bg-surface-2 text-fg before:absolute before:inset-s-1/2 before:top-0 before:-ms-2.5 before:h-[3px] before:w-5 before:rounded-b-[3px] before:bg-accent-mark before:content-['']")}
          >
            <Tile id={s.id} className="duration-200 group-active/ni:bg-step" />
            <span className="max-w-full truncate">{t(navKey(s.id))}</span>
          </Link>
        );
      })}
      <button
        type="button"
        data-menu-toggle
        aria-expanded={moreOpen}
        aria-controls="mobile-menu"
        onClick={onMore}
        style={toneVar("more")}
        className={cx(dockTab, moreOpen && "bg-surface-2 text-fg")}
      >
        <Tile id="more" className="bg-step duration-200 [&_svg]:[stroke-width:2.8] group-hover/ni:bg-step group-active/ni:bg-step" />
        <span>{t("nav.more")}</span>
        {attention && <span aria-hidden className="absolute inset-s-[calc(50%+8px)] top-1.5 size-2 rounded-full bg-warn shadow-[0_0_0_2px_var(--surface)]" />}
      </button>
    </nav>
  );
}

/**
 * The phone's full menu, under the top bar: every section with its group and counter, GitHub and the theme, the language, the
 * owner and the version. A disclosure, not a window: the bar stays in reach, Esc closes it (shell.tsx). `onNavigate` closes it
 * when a link is followed, also to the page that is already open.
 */
export function MobileMenu({ open, onNavigate, menuRef, current }: { open: boolean; onNavigate: () => void; menuRef: Ref<HTMLDivElement>; current?: SectionId }) {
  const t = useT();
  const lang = useLang();
  const signOut = useSignOut();
  const { data } = useSuspenseQuery(meQuery);
  const meta = useNavMeta();
  const isCurrent = useIsCurrent(current);
  if (!open) return null;
  const name = data.admin?.displayName ?? "";
  return (
    <div
      id="mobile-menu"
      ref={menuRef}
      role="dialog"
      aria-label={t("shell.menu.title")}
      tabIndex={-1}
      data-mobile-menu
      className="absolute inset-x-2 top-[calc(100%+8px)] flex max-h-[min(620px,calc(100dvh-140px))] animate-[mg-up_0.25s_var(--e)_both] flex-col gap-3 overflow-y-auto [scrollbar-width:none] rounded-card border border-line bg-surface p-3 shadow-(--shadow-pop) outline-none md:hidden"
    >
      <nav aria-label={t("nav.main")} className="flex flex-col gap-0.5">
        {sections.map((s) => {
          const active = isCurrent(s);
          const n = mobileBadge(s.id, meta, t);
          return (
            <div key={s.id} className="contents">
              {"group" in s && <div className="mt-2 border-t border-line px-2 pt-3 pb-1.5 text-xs leading-4 text-muted">{t(s.group)}</div>}
              <Link
                to={s.to}
                activeOptions={{ exact: s.to === "/" }}
                aria-current={active ? "page" : undefined}
                onClick={onNavigate}
                style={toneVar(s.id)}
                className={cx(
                  "group/ni relative flex h-11 items-center gap-3 rounded-field px-2 text-[15px] leading-5 text-sec outline-none transition-colors duration-150 ease-out hover:bg-step hover:text-fg focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-accent-mark",
                  active && cx("bg-surface-2 font-medium text-fg", marker, "before:inset-s-[-12px]"),
                )}
              >
                <Tile id={s.id} />
                <span className="min-w-0 flex-1 truncate">{t(navKey(s.id))}</span>
                {n && <span className="ms-auto font-mono text-xs text-muted">{n}</span>}
              </Link>
            </div>
          );
        })}
      </nav>
      <div className="flex gap-2 border-t border-line pt-3">
        <GitHubButton full />
        <ThemeToggle />
      </div>
      <div role="group" aria-label={t("header.language")} className="grid grid-cols-5 rounded-field border border-line bg-canvas p-0.5">
        {langs.map((l) => (
          <button
            key={l}
            type="button"
            lang={localeOf(l)}
            aria-pressed={l === lang}
            aria-label={langNames[l]}
            onClick={(e) => chooseLanguage(l, e.currentTarget)}
            className="h-9 cursor-pointer rounded-ctl p-0 text-center font-mono text-xs text-sec outline-none transition-colors duration-150 ease-out hover:text-fg focus-visible:outline-2 focus-visible:outline-accent-mark aria-pressed:bg-surface-2 aria-pressed:text-fg"
          >
            {langShort[l]}
          </button>
        ))}
      </div>
      <div className="flex items-center gap-3 px-2 py-1">
        <Avatar name={name} size={32} />
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate text-sm">{name}</span>
          <span className="truncate text-xs leading-4 text-muted lowercase">{t(roleKey[data.admin?.role ?? Role.UNSPECIFIED])}</span>
        </span>
        <button
          type="button"
          onClick={signOut}
          className="flex h-8 cursor-pointer items-center gap-2 rounded-ctl px-2.5 text-[13px] font-medium text-danger-text outline-none transition-colors duration-150 ease-out hover:bg-danger-soft focus-visible:outline-2 focus-visible:outline-accent-mark"
        >
          {t("header.signOut")}
        </button>
      </div>
      <VersionLine className="px-2" />
    </div>
  );
}
