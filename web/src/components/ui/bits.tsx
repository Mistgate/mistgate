import { Checkbox } from "@base-ui/react/checkbox";
import type { ComponentProps, ReactNode } from "react";
import { useLang } from "@/i18n";
import { cx } from "@/lib/cx";
import { initialOf } from "@/lib/initial";
import { Icon, IconChip, type IconName, type Tone } from "./icons";

/** A tone as a tag's colours: the pastel on its own dark (or light) background. */
export const toneTagClass: Record<Tone, string> = {
  lavender: "bg-tone-lavender-bg text-tone-lavender",
  sky: "bg-tone-sky-bg text-tone-sky",
  sand: "bg-tone-sand-bg text-tone-sand",
  sage: "bg-tone-sage-bg text-tone-sage",
  mint: "bg-tone-mint-bg text-tone-mint",
  rose: "bg-tone-rose-bg text-tone-rose",
};

/** Small static label (a tag): 24px, grey on a step of grey; with a `tone` it takes that tone's pair. */
export function Chip({ children, mono, className, title, tone }: { children: ReactNode; mono?: boolean; className?: string; title?: string; tone?: Tone }) {
  return (
    <span
      title={title}
      className={cx(
        "inline-flex h-6 flex-none items-center gap-1 rounded-chip px-2 text-xs leading-4 whitespace-nowrap",
        tone ? toneTagClass[tone] : "bg-step text-sec",
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
        <kbd key={k} className="rounded-chip bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] leading-4 text-sec">
          {k}
        </kbd>
      ))}
    </span>
  );
}

/**
 * A section's heading, two ways. The default is a card's heading: 16/24, medium, with an optional `count` after it in muted.
 * `caps` is the small capital label (12px, spaced, quiet) that only the profile editor's sections use. With `icon` + `tone`
 * a small tinted chip leads the text (the text itself stays plain, so contrast does not depend on the tone). `as` picks
 * the element for a real heading.
 */
export function SectionLabel({
  children,
  className,
  icon,
  tone,
  count,
  caps,
  as: Tag = "div",
}: {
  children: ReactNode;
  className?: string;
  icon?: IconName;
  tone?: Tone;
  count?: ReactNode;
  caps?: boolean;
  as?: "div" | "h2" | "h3" | "h4" | "span";
}) {
  return (
    <Tag
      className={cx(
        caps ? "text-xs leading-4 font-medium tracking-[0.07em] text-sec uppercase" : "text-base leading-6 font-medium text-fg",
        icon && tone && "flex items-center gap-2",
        className,
      )}
    >
      {icon && tone && <IconChip icon={icon} tone={tone} />}
      {icon && tone ? <span className="min-w-0">{children}</span> : children}
      {count != null && <span className="ms-1 font-normal text-muted">{count}</span>}
    </Tag>
  );
}

/**
 * "Danger zone" at the end of a page (node, user, profile, DNS preset): the same pink section everywhere, its heading in
 * the danger colour. It holds the page's own rows and buttons.
 */
export function DangerZone({ title, children, className }: { title: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx("flex min-w-0 flex-col gap-3 rounded-card border border-[color-mix(in_srgb,var(--danger)_40%,transparent)] bg-danger-soft p-4", className)}>
      <h2 className="flex items-center gap-2 text-base leading-6 font-medium text-danger-text">
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

/** A card's last row of buttons: the main ones at the start, a destructive one (or Save) pushed to the end with `ms-auto`. */
export function ActionBar({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx("flex flex-wrap items-center gap-2 border-t border-line px-4 py-3 md:px-5", className)}>{children}</div>;
}

/**
 * The avatar colours of the board (UCOL): eight solid pastels, the initial in near-black. One list for the whole panel
 * (the people lists, the command palette), so a person keeps their colour everywhere.
 */
export const avatarColors = ["#c9b8e8", "#9fd6cf", "#e8c9a0", "#a9c8ec", "#e6a6b0", "#b8e0a8", "#d8b8e8", "#e0d49a"] as const;

/**
 * The colour slot of a person's avatar, the board's: the first letter's code plus the length of the name, around the palette.
 * It comes from the name alone, so the preview in the new-person window and the lists show the same colour.
 */
export function avatarIndex(name: string): number {
  return ((name.charCodeAt(0) || 0) + name.length) % avatarColors.length;
}

/**
 * `index` picks the colour (a slot from `avatarIndex`, or a row number) in a list, or a number made from the person's id); the name only supplies the
 * initial: its first letter, upper-case. It is the board's .ava: a grid that centres the letter, 600 weight, near-black
 * on the pastel; on the light theme a 1px inner hairline keeps the pale ones from melting into the page.
 */
export function Avatar({ name, index = 0, size = 28, fontSize }: { name: string; index?: number; size?: number; fontSize?: number }) {
  const lang = useLang();
  return (
    <span
      aria-hidden
      className="grid flex-none place-items-center rounded-full leading-none font-[600] text-on-accent select-none [:root[data-theme=light]_&]:shadow-[inset_0_0_0_1px_rgb(22_22_26/0.06)]"
      style={{
        width: size,
        height: size,
        fontSize: fontSize ?? Math.round(size * 0.44),
        background: avatarColors[index % avatarColors.length],
      }}
    >
      {initialOf(name, lang)}
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

/** Screen H1: 28/32, medium, tight (24 on the phone). */
export function PageTitle({ children, className }: { children: ReactNode; className?: string }) {
  return <h1 className={cx("text-2xl leading-8 font-medium tracking-[-0.02em] md:text-[28px]", className)}>{children}</h1>;
}

/**
 * A checkbox on Base UI: 20px, a 6px radius, the accent when ticked. With `label` the box and the words are one <label>
 * (a click on the words ticks it); without, give it an aria-label.
 */
export function Check({ label, className, ...rest }: Omit<ComponentProps<typeof Checkbox.Root>, "className"> & { label?: ReactNode; className?: string }) {
  const box = (
    <Checkbox.Root
      className={cx(
        "grid size-5 flex-none cursor-pointer place-items-center rounded-chip border border-[color-mix(in_srgb,var(--text)_28%,transparent)] text-transparent [transition:background-color_.15s_var(--e),border-color_.15s_var(--e),transform_.2s_var(--ease-spring)] hover:border-(--border-hover) data-checked:scale-[1.06] data-checked:border-accent-mark data-checked:bg-accent-mark data-checked:text-on-accent data-disabled:cursor-default data-disabled:opacity-45",
        !label && className,
      )}
      {...rest}
    >
      <Checkbox.Indicator className="grid place-items-center">
        <Icon name="check" size={14} strokeWidth={2.5} />
      </Checkbox.Indicator>
    </Checkbox.Root>
  );
  if (!label) return box;
  return (
    <label className={cx("flex cursor-pointer items-center gap-2.5 font-medium", className)}>
      {box}
      {label}
    </label>
  );
}
