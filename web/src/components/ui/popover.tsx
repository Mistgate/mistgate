import { Popover as BasePopover } from "@base-ui/react/popover";
import type { ComponentPropsWithoutRef, ElementType, ReactElement, ReactNode } from "react";
import { cx } from "@/lib/cx";

/**
 * A floating panel opened from a button: the fleet's nodes under the top bar, a language list. `trigger` is the one element
 * that opens it (a button that names itself); the panel is 340px wide (`narrow`: 220), with a 14px radius, a hairline and
 * the one shadow of floating things. Esc and a click outside close it; focus goes in and comes back. Lay its rows out with
 * PopRow and PopFooter.
 */
export function Popover({
  trigger,
  children,
  align = "end",
  narrow,
  open,
  onOpenChange,
  className,
  ...rest
}: {
  trigger: ReactElement;
  children: ReactNode;
  align?: "start" | "center" | "end";
  narrow?: boolean;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  className?: string;
  /** Accessible name of the panel. */
  "aria-label"?: string;
}) {
  return (
    <BasePopover.Root open={open} onOpenChange={onOpenChange}>
      <BasePopover.Trigger render={trigger} />
      <BasePopover.Portal>
        <BasePopover.Positioner side="bottom" align={align} sideOffset={8} className="z-50 outline-none">
          <BasePopover.Popup
            className={cx(
              "origin-(--transform-origin) rounded-pop border border-line bg-surface p-1.5 shadow-(--shadow-pop) outline-none transition-[opacity,transform] duration-200 ease-out data-ending-style:translate-y-2 data-ending-style:opacity-0 data-starting-style:translate-y-2 data-starting-style:opacity-0",
              narrow ? "w-[220px]" : "w-[340px]",
              "max-w-[calc(100vw-32px)]",
              className,
            )}
            {...rest}
          >
            {children}
          </BasePopover.Popup>
        </BasePopover.Positioner>
      </BasePopover.Portal>
    </BasePopover.Root>
  );
}

type PopRowProps<T extends ElementType> = { as?: T; className?: string } & Omit<ComponentPropsWithoutRef<T>, "className">;

/** A row of a Popover: 40px, an 8px radius, a step of grey on hover. The columns are the caller's (`grid-cols-[24px_44px_minmax(0,1fr)_auto]`); a button by default. */
export function PopRow<T extends ElementType = "button">({ as, className, ...rest }: PopRowProps<T>) {
  const Tag: ElementType = as ?? "button";
  return (
    <Tag
      className={cx(
        "grid h-10 w-full cursor-pointer items-center gap-2.5 rounded-ctl px-2.5 text-start text-[13px] transition-colors duration-150 ease-out hover:bg-step",
        className,
      )}
      {...(Tag === "button" ? { type: "button" } : {})}
      {...rest}
    />
  );
}

/** The last line of a Popover (a link to the whole list): a hairline above it, quiet text that brightens on hover. */
export function PopFooter({ className, ...rest }: Omit<ComponentPropsWithoutRef<"a">, "className"> & { className?: string }) {
  return (
    <a
      className={cx("mt-1.5 flex h-10 items-center justify-between border-t border-line px-2.5 text-[13px] text-sec transition-colors duration-150 hover:text-fg", className)}
      {...rest}
    />
  );
}
