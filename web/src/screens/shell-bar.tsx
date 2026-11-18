import { Link } from "@tanstack/react-router";
import { useState, type Ref } from "react";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { Kbd } from "@/components/ui/bits";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { PopFooter, Popover, popRowClass } from "@/components/ui/popover";
import { StatusDot, type StatusKind } from "@/components/ui/status";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { fleetDots, useFleetHealth, useFleetNodes } from "@/lib/fleet";
import { nodeKind, statusWord } from "@/lib/node-status";
import { GitHubButton, paletteKeys } from "./shell-parts";
import { MobileMenu } from "./shell-nav";

// The top bar: 64px (56 on a phone), the search, the state of the nodes, GitHub, the language and the theme. Sizes, spacing and
// motion are the redesign board's (pn.css, .bar .find .btn.hb .pop .pr2 .pf2 .ic.bg).

const pillText: Record<StatusKind, string> = { ok: "text-ok-text", warn: "text-warn-text", bad: "text-danger-text", off: "text-fg", blip: "text-fg", busy: "text-fg" };
const nodeText = (k: StatusKind) => (k === "bad" ? "text-danger-text" : k === "warn" ? "text-warn-text" : "text-fg");
const MAX_ROWS = 8;

/**
 * The state of the fleet as a 40px button: a dot for each node (or three for a long list), the verdict in its colour, a
 * chevron. It opens the list of the nodes, problems first, each a link to the node, and a way to Health.
 */
function FleetPill({ defaultOpen }: { defaultOpen?: boolean }) {
  const t = useT();
  const health = useFleetHealth();
  const nodes = useFleetNodes();
  const [open, setOpen] = useState(!!defaultOpen);
  const dots = fleetDots(nodes.map(nodeKind));
  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      aria-label={t("shell.fleet.label")}
      trigger={
        <button
          type="button"
          className={cx(
            "inline-flex h-10 flex-none cursor-pointer items-center gap-2.5 rounded-field border border-line bg-step ps-3 pe-2.5 text-sm font-medium whitespace-nowrap outline-none transition-[background-color,transform] duration-150 ease-out hover:bg-(--step-hover) focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent-mark active:scale-[0.98] data-popup-open:bg-(--step-hover)",
            pillText[health.kind],
          )}
        >
          <span aria-hidden className="inline-flex items-center gap-1 px-0.5">
            {dots.map((k, i) => (
              <i key={i} className={`tone-${k} size-[7px] rounded-full bg-(--c) transition-colors duration-[400ms] ease-out`} />
            ))}
          </span>
          <span className="max-md:hidden">{health.label}</span>
          <span className="md:hidden" aria-label={health.label}>
            {health.short}
          </span>
          <Icon name="chevronDown" size={16} className="text-muted" />
        </button>
      }
    >
      <div className="max-h-[min(400px,calc(100dvh-140px))] overflow-y-auto">
        {nodes.slice(0, MAX_ROWS).map((n) => {
          const kind = nodeKind(n);
          return (
            <Link key={n.id} to="/nodes/$id" params={{ id: n.id }} onClick={() => setOpen(false)} className={cx(popRowClass, "grid-cols-[24px_minmax(44px,max-content)_minmax(0,1fr)_auto]")}>
              {n.countryCode ? <Flag code={n.countryCode} size={16} /> : <span />}
              <b className="max-w-[120px] truncate font-mono font-medium">{n.name}</b>
              <span className="truncate text-sec">{n.location}</span>
              <span className={cx("inline-flex items-center gap-2", nodeText(kind))}>
                <StatusDot kind={kind} />
                {statusWord(t, n)}
              </span>
            </Link>
          );
        })}
      </div>
      <PopFooter as={Link} to="/health" onClick={() => setOpen(false)}>
        {t("shell.fleet.open")}
        <Icon name="chevronRight" size={16} />
      </PopFooter>
    </Popover>
  );
}

/**
 * The bar. On a phone it holds the burger (which opens the full menu under it), the search as an icon button and the node
 * state; from `md` the search is a box with the shortcut, then the node state, GitHub, the language and the theme. The
 * content is as wide as the page below it (1240px at most). `demo` opens a list from the start (the dev kit's scenes).
 */
export function TopBar({
  onSearch,
  menuOpen,
  onToggleMenu,
  onCloseMenu,
  menuRef,
  demo,
}: {
  onSearch: () => void;
  menuOpen: boolean;
  onToggleMenu: () => void;
  onCloseMenu: () => void;
  menuRef: Ref<HTMLDivElement>;
  demo?: { fleet?: boolean; lang?: boolean };
}) {
  const t = useT();
  return (
    <header className="shell-in sticky top-0 z-20 border-b border-line bg-canvas px-4 md:px-5 rail:px-8">
      <div className="mx-auto flex h-14 max-w-[1240px] items-center gap-2 md:h-16">
        <button
          type="button"
          data-menu-toggle
          aria-label={t("shell.menu.title")}
          aria-expanded={menuOpen}
          aria-controls="mobile-menu"
          onClick={onToggleMenu}
          className="grid size-10 flex-none cursor-pointer place-items-center rounded-field border border-line bg-surface text-sec outline-none transition-colors duration-150 ease-out hover:bg-step hover:text-fg focus-visible:outline-2 focus-visible:outline-accent-mark md:hidden"
        >
          <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
            <path d={menuOpen ? "M6 6l12 12M18 6L6 18" : "M4 7h16M4 12h16M4 17h16"} />
          </svg>
        </button>
        <button
          type="button"
          onClick={onSearch}
          aria-label={t("header.search")}
          className="flex h-10 w-10 flex-none items-center justify-center gap-2 rounded-field border border-line bg-surface p-0 text-muted outline-none transition-[border-color] duration-150 ease-out hover:border-(--border-hover) focus-visible:outline-2 focus-visible:outline-accent-mark me-auto md:w-auto md:max-w-[400px] md:min-w-0 md:flex-[1_1_200px] md:justify-start md:px-3"
        >
          <Icon name="search" size={16} />
          <span className="min-w-0 flex-1 truncate text-start max-md:hidden">{t("header.search")}</span>
          <span className="flex-none max-md:hidden">
            <Kbd keys={paletteKeys} />
          </span>
        </button>
        <FleetPill defaultOpen={demo?.fleet} />
        <span aria-hidden className="mx-1 h-6 w-px flex-none bg-line max-md:hidden" />
        <GitHubButton className="max-md:hidden" />
        <LangToggle className="max-md:hidden" defaultOpen={demo?.lang} />
        <ThemeToggle className="max-md:hidden" />
      </div>
      <MobileMenu open={menuOpen} onNavigate={onCloseMenu} menuRef={menuRef} />
    </header>
  );
}
