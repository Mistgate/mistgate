import { Select as BaseSelect } from "@base-ui/react/select";
import type { ReactNode } from "react";
import { cx } from "@/lib/cx";
import { Icon } from "./icons";

/**
 * `hint` is a second, muted line under the label in the list; a `disabled` option is greyed out and cannot be picked; `icon`
 * (a small mark, decorative) stands before the label in the list and in the closed field.
 */
export type SelectOption = { value: string; label: string; hint?: string; disabled?: boolean; icon?: ReactNode };

type Props = {
  value: string;
  onValueChange: (value: string) => void;
  options: readonly SelectOption[];
  "aria-label": string;
  /** 32px filter box for toolbars (Nodes list); the default is the 40px field. */
  compact?: boolean;
  className?: string;
};

/** The look of a dropdown list, shared with the language menu (components/prefs.tsx). */
export const selectPopupClass =
  "min-w-(--anchor-width) origin-(--transform-origin) rounded-pop border border-line bg-surface p-1.5 shadow-(--shadow-pop) outline-none transition-[scale,opacity] duration-150 data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0";
export const selectItemClass =
  "flex min-h-9 cursor-pointer items-center gap-2.5 rounded-ctl px-2.5 py-1 text-sm text-fg outline-none select-none data-highlighted:bg-step data-selected:font-medium";
/** The tick of the chosen row (16px, as the board's). */
export function SelectCheck() {
  return <Icon name="check" size={16} />;
}

/** Dropdown styled like a 40px text field; the list pops over in a menu. */
export function Select({ value, onValueChange, options, compact, className, ...rest }: Props) {
  return (
    <BaseSelect.Root
      value={value}
      items={options as SelectOption[]}
      onValueChange={(v) => v !== null && onValueChange(v)}
    >
      <BaseSelect.Trigger
        className={cx(
          "group flex w-full items-center justify-between gap-3 border border-line bg-canvas text-start text-fg outline-none transition-[border-color,box-shadow] duration-150 ease-out hover:border-(--border-hover) focus-visible:border-accent-line focus-visible:shadow-[0_0_0_3px_var(--focus-ring)] data-popup-open:border-accent-line data-disabled:opacity-45",
          compact ? "h-8 rounded-ctl px-3 text-[13px]" : "h-10 rounded-field px-3 text-sm",
          className,
        )}
        {...rest}
      >
        {/* one line always: a long label is cut with an ellipsis, never wrapped out of the box */}
        <span className="flex min-w-0 items-center gap-2">
          {options.find((o) => o.value === value)?.icon && (
            <span aria-hidden className="flex flex-none text-fg">
              {options.find((o) => o.value === value)!.icon}
            </span>
          )}
          <BaseSelect.Value className="min-w-0 truncate" />
        </span>
        <BaseSelect.Icon className="flex-none text-muted transition-transform duration-200 ease-(--e) group-data-popup-open:rotate-180">
          <Icon name="chevronDown" size={16} />
        </BaseSelect.Icon>
      </BaseSelect.Trigger>
      <BaseSelect.Portal>
        <BaseSelect.Positioner sideOffset={6} alignItemWithTrigger={false} className="z-50 outline-none">
          <BaseSelect.Popup className={selectPopupClass}>
            {/* about nine rows, then it scrolls: a long list must not cover the whole screen */}
            <BaseSelect.List className="max-h-[min(var(--available-height),22rem)] overflow-y-auto overscroll-contain">
              {options.map((o) => (
                <BaseSelect.Item
                  key={o.value}
                  value={o.value}
                  disabled={o.disabled}
                  className={cx(selectItemClass, "data-disabled:cursor-default data-disabled:opacity-45")}
                >
                  {o.icon && (
                    <span aria-hidden className="flex flex-none text-fg">
                      {o.icon}
                    </span>
                  )}
                  <span className="flex min-w-0 flex-1 flex-col">
                    <BaseSelect.ItemText>{o.label}</BaseSelect.ItemText>
                    {o.hint && <span className="truncate text-[11px] leading-snug font-medium text-muted">{o.hint}</span>}
                  </span>
                  <BaseSelect.ItemIndicator className="text-accent-text">
                    <SelectCheck />
                  </BaseSelect.ItemIndicator>
                </BaseSelect.Item>
              ))}
            </BaseSelect.List>
          </BaseSelect.Popup>
        </BaseSelect.Positioner>
      </BaseSelect.Portal>
    </BaseSelect.Root>
  );
}
