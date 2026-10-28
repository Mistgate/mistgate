import type { ReactNode } from "react";
import { useLang } from "@/i18n";
import { cx } from "@/lib/cx";
import { initialOf } from "@/lib/initial";
import { IconChip, type IconName, type Tone } from "./icons";

/** Small static label: 22px, grey on surface-2. */
export function Chip({ children, mono, className }: { children: ReactNode; mono?: boolean; className?: string }) {
  return (
    <span
      className={cx(
        "inline-flex h-[22px] flex-none items-center rounded-ctl bg-surface-2 px-2 text-[11px] font-semibold whitespace-nowrap text-muted",
        mono && "font-mono",
        className,
      )}
    >
      {children}
    </span>
  );
}

/** Keycaps: <Kbd keys={["⌘", "K"]} /> */
export function Kbd({ keys }: { keys: readonly string[] }) {
  return (
    <span className="flex gap-1">
      {keys.map((k) => (
        <kbd
          key={k}
          className="flex h-6 min-w-6 items-center justify-center rounded-[7px] border border-b-2 border-line bg-surface-2 px-1.5 font-mono text-xs font-bold text-fg"
        >
          {k}
        </kbd>
      ))}
    </span>
  );
}

/**
 * Eyebrow: 11px / 700 / +0.1em / uppercase / muted. With `icon` + `tone` a small tinted chip leads the text (the text
 * itself stays muted, so contrast does not depend on the tone). `as` picks the element for a real heading.
 */
export function SectionLabel({
  children,
  className,
  icon,
  tone,
  as: Tag = "div",
}: {
  children: ReactNode;
  className?: string;
  icon?: IconName;
  tone?: Tone;
  as?: "div" | "h2" | "h3" | "h4" | "span";
}) {
  return (
    <Tag className={cx("text-[11px] font-bold tracking-[0.1em] text-muted uppercase", icon && tone && "flex items-center gap-2", className)}>
      {icon && tone && <IconChip icon={icon} tone={tone} />}
      {icon && tone ? <span className="min-w-0">{children}</span> : children}
    </Tag>
  );
}

/**
 * "Danger zone" at the end of a page (node, user, profile, DNS preset): the same pink section everywhere, its label in
 * the danger colour. It holds the page's own rows and buttons.
 */
export function DangerZone({ title, children, className }: { title: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx("flex min-w-0 flex-col gap-3 rounded-card-lg border border-[color-mix(in_oklch,var(--danger)_40%,var(--border))] bg-danger-soft p-4", className)}>
      <h2 className="flex items-center gap-2 text-[11px] font-bold tracking-[0.1em] text-danger-text uppercase">
        <IconChip icon="warn" tone="rose" />
        {title}
      </h2>
      {children}
    </section>
  );
}

/**
 * A block of a card under a small heading, a hairline above it (an open Reachability source, a node's forms); `flush` is
 * the first one, right under the card's header.
 */
export function Block({ title, flush, className, children }: { title?: ReactNode; flush?: boolean; className?: string; children: ReactNode }) {
  return (
    <div className={cx("flex flex-col gap-3 px-4 md:px-5", flush ? "pb-4" : "border-t border-line py-4", className)}>
      {title && <SectionLabel as="h4">{title}</SectionLabel>}
      {children}
    </div>
  );
}

/** A card's last row of buttons: the main ones on the left, a destructive one (or Save) pushed right with `ml-auto`. */
export function ActionBar({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx("flex flex-wrap items-center gap-2 border-t border-line px-4 py-3 md:px-5", className)}>{children}</div>;
}

// Solid pastel circles, the initial in near-black, cycling in this order.
const avatarColors = [
  "var(--accent-lavender)",
  "var(--accent-sky)",
  "var(--accent-sand)",
  "var(--accent-rose)",
  "var(--accent-mint)",
  "var(--accent-sage)",
  "#c9b8e8",
  "#9fd6cf",
  "#e8b98f",
];

/**
 * `index` picks the colour (the row number in a list); the name only supplies the initial: its first letter,
 * upper-case. A flex box with line-height 1 centres the line; Onest (ascent 0.97, descent 0.305, cap height 0.707)
 * then puts the middle of a capital 0.02em above the middle of the circle, so the letter moves down by that much.
 */
export function Avatar({ name, index = 0, size = 28 }: { name: string; index?: number; size?: number }) {
  const lang = useLang();
  return (
    <span
      aria-hidden
      className="flex flex-none items-center justify-center rounded-full leading-none font-extrabold text-[#0c0c0e] select-none"
      style={{
        width: size,
        height: size,
        fontSize: Math.round(size * 0.44),
        background: avatarColors[index % avatarColors.length],
      }}
    >
      <span className="block translate-y-[0.02em] leading-none">{initialOf(name, lang)}</span>
    </span>
  );
}

/** Surface with a 1px border and no shadow. `lift` adds the hover rise for clickable cards. */
export function Card({
  children,
  lg,
  lift,
  className,
}: {
  children: ReactNode;
  lg?: boolean;
  lift?: boolean;
  className?: string;
}) {
  return (
    <div
      className={cx(
        "border border-line bg-surface",
        lg ? "rounded-card-lg" : "rounded-card",
        lift && "card-hover cursor-pointer",
        className,
      )}
    >
      {children}
    </div>
  );
}

/** Screen H1: 26px (24 on the phone), heavy, tight. */
export function PageTitle({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <h1 className={cx("text-2xl font-extrabold tracking-[-0.035em] md:text-[26px]", className)}>{children}</h1>
  );
}
