import { Menu as BaseMenu } from "@base-ui/react/menu";
import type { ComponentPropsWithoutRef, ReactElement, ReactNode } from "react";
import { cx } from "@/lib/cx";

/**
 * A menu opened from a button (the owner's menu, the language list), on Base UI's Menu: arrow keys move, a letter jumps, Esc
 * closes and the focus goes back to the button. `trigger` is the one element that opens it and names itself. The panel has
 * the radius, hairline and shadow of the other floating things (Popover); its rows are MenuItem, MenuLink and MenuRadioItem.
 */
export function Menu({
  trigger,
  children,
  side = "bottom",
  align = "end",
  narrow,
  pop,
  defaultOpen,
  className,
  ...rest
}: {
  trigger: ReactElement;
  children: ReactNode;
  side?: "top" | "bottom" | "inline-start" | "inline-end";
  align?: "start" | "center" | "end";
  /** 220px wide (a short list); the default is as wide as its button, 216px at least. */
  narrow?: boolean;
  /** Opens with a spring pop (scale from 0.9) instead of the 8px rise: the owner's menu, which grows out of its button. */
  pop?: boolean;
  defaultOpen?: boolean;
  className?: string;
  /** Accessible name of the list. */
  "aria-label"?: string;
}) {
  return (
    <BaseMenu.Root defaultOpen={defaultOpen}>
      <BaseMenu.Trigger render={trigger} />
      <BaseMenu.Portal>
        <BaseMenu.Positioner side={side} align={align} sideOffset={side === "top" || side === "bottom" ? 6 : 12} className="z-50 outline-none">
          <BaseMenu.Popup
            className={cx(
              "flex origin-(--transform-origin) flex-col rounded-pop border border-line bg-surface p-1.5 shadow-(--shadow-pop) outline-none transition-[opacity,transform] duration-200 data-ending-style:opacity-0 data-starting-style:opacity-0",
              pop ? "ease-spring data-ending-style:scale-90 data-starting-style:scale-90" : "ease-out data-ending-style:translate-y-2 data-starting-style:translate-y-2",
              narrow ? "w-[220px]" : "min-w-[max(216px,var(--anchor-width))]",
              "max-w-[calc(100vw-32px)]",
              className,
            )}
            {...rest}
          >
            {children}
          </BaseMenu.Popup>
        </BaseMenu.Positioner>
      </BaseMenu.Portal>
    </BaseMenu.Root>
  );
}

const row =
  "flex h-9 cursor-pointer items-center gap-2.5 rounded-ctl px-2.5 text-start text-sm font-medium text-fg outline-none transition-colors duration-150 ease-out select-none data-highlighted:bg-step [&>svg]:text-sec";

/** A row that does something (sign out); `danger` makes it red. */
export function MenuItem({ danger, className, ...rest }: Omit<ComponentPropsWithoutRef<typeof BaseMenu.Item>, "className"> & { danger?: boolean; className?: string }) {
  return <BaseMenu.Item className={cx(row, danger && "mt-1.5 text-danger-text [&>svg]:text-danger-text", className)} {...rest} />;
}

/** A row that goes somewhere: give it the router's Link in `render` (<MenuLink render={<Link to="/settings" />}>). */
export function MenuLink({ className, ...rest }: Omit<ComponentPropsWithoutRef<typeof BaseMenu.LinkItem>, "className"> & { className?: string }) {
  return <BaseMenu.LinkItem closeOnClick className={cx(row, className)} {...rest} />;
}

/** The line at the top of a menu: who is signed in, with a hairline under it. */
export function MenuLabel({ children }: { children: ReactNode }) {
  return <p className="mb-1.5 border-b border-line px-2.5 pt-1.5 pb-2 text-xs leading-[18px] text-muted [&_b]:font-medium [&_b]:text-fg">{children}</p>;
}

/** A list where one row is chosen (the language): `value` and `onValueChange` as a radio group. */
export function MenuRadioGroup<V extends string>({ value, onValueChange, children, ...rest }: { value: V; onValueChange: (value: V, row: Element | null) => void; children: ReactNode; "aria-label"?: string }) {
  return (
    // `row` is the item that was chosen (where an effect starts from)
    <BaseMenu.RadioGroup value={value} onValueChange={(v, d) => onValueChange(v as V, d.event.target instanceof Element ? d.event.target.closest("[role=menuitemradio]") : null)} {...rest}>
      {children}
    </BaseMenu.RadioGroup>
  );
}

/** One row of such a list: a short mono code, the name, a tick on the chosen one. */
export function MenuRadioItem({ value, code, children, lang }: { value: string; code: string; children: ReactNode; lang?: string }) {
  return (
    <BaseMenu.RadioItem
      value={value}
      closeOnClick
      lang={lang}
      className="grid h-10 cursor-pointer grid-cols-[28px_minmax(0,1fr)_16px] items-center gap-2.5 rounded-ctl px-2.5 text-start text-[13px] text-fg outline-none transition-colors duration-150 ease-out select-none data-highlighted:bg-step"
    >
      <span aria-hidden className="font-mono text-xs font-medium text-muted">
        {code}
      </span>
      <span className="truncate">{children}</span>
      <BaseMenu.RadioItemIndicator className="grid place-items-center text-accent-text">
        <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
          <path d="M5 12.5l4.5 4.5L19 7.5" />
        </svg>
      </BaseMenu.RadioItemIndicator>
    </BaseMenu.RadioItem>
  );
}
