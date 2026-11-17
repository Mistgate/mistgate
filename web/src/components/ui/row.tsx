import type { ComponentPropsWithoutRef, ElementType } from "react";
import { cx } from "@/lib/cx";

type RowProps<T extends ElementType> = { as?: T; className?: string } & Omit<ComponentPropsWithoutRef<T>, "className">;

/**
 * A row of a table-like list: 56px at least, a hairline above it, 24px of side room (16 on the phone), a grid whose columns
 * the screen sets in `className` (`grid-cols-[minmax(176px,1.2fr)_176px_16px] max-md:grid-cols-[minmax(0,1fr)_auto]`), so each
 * list keeps its own columns and still shares the metrics. `as="button"` (or "a") makes the whole row a target: it lightens
 * on hover.
 */
export function Row<T extends ElementType = "div">({ as, className, ...rest }: RowProps<T>) {
  const Tag: ElementType = as ?? "div";
  const target = Tag === "button" || Tag === "a";
  return (
    <Tag
      className={cx(
        "grid min-h-14 w-full items-center gap-4 border-t border-line px-6 text-start max-md:px-4",
        target && "cursor-pointer transition-colors duration-150 ease-out hover:bg-[color-mix(in_srgb,var(--text)_3%,transparent)]",
        className,
      )}
      {...(Tag === "button" ? { type: "button" } : {})}
      {...rest}
    />
  );
}

/** The header of such a list: the same grid, 36px at least, 13px muted. Give it the same column classes as the rows. */
export function RowHead({ className, ...rest }: Omit<ComponentPropsWithoutRef<"div">, "className"> & { className?: string }) {
  return <div role="row" className={cx("grid min-h-9 w-full items-center gap-4 border-t border-line px-6 text-[13px] text-muted max-md:px-4", className)} {...rest} />;
}
