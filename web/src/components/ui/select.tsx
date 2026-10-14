import { Select as BaseSelect } from "@base-ui/react/select";
import { cx } from "@/lib/cx";

/** `hint` is a second, muted line under the label in the list. */
export type SelectOption = { value: string; label: string; hint?: string };

type Props = {
  value: string;
  onValueChange: (value: string) => void;
  options: readonly SelectOption[];
  "aria-label": string;
  /** 36px filter box for toolbars (Nodes list); the default is the 44px field. */
  compact?: boolean;
  className?: string;
};

/** The look of a dropdown list, shared with the language menu (components/prefs.tsx). */
export const selectPopupClass =
  "min-w-(--anchor-width) origin-(--transform-origin) rounded-card border border-line bg-surface p-1.5 shadow-(--shadow-toast) outline-none transition-[scale,opacity] duration-150 data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0";
export const selectItemClass =
  "flex min-h-10 cursor-pointer items-center gap-2 rounded-ctl px-2.5 py-1.5 text-[13px] font-semibold text-fg outline-none select-none data-highlighted:bg-surface-2";
/** The tick of the chosen row. */
export function SelectCheck() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M2.5 6.5l2.5 2.5 4.5-5.5" />
    </svg>
  );
}

/** Dropdown styled like a 44px text field; the list pops over in a card. */
export function Select({ value, onValueChange, options, compact, className, ...rest }: Props) {
  return (
    <BaseSelect.Root
      value={value}
      items={options as SelectOption[]}
      onValueChange={(v) => v !== null && onValueChange(v)}
    >
      <BaseSelect.Trigger
        className={cx(
          "flex w-full items-center justify-between gap-3 border border-line bg-surface text-left font-medium text-fg outline-none transition-colors duration-200 focus-visible:border-accent data-popup-open:border-accent-line",
          compact ? "h-9 rounded-ctl px-3 text-xs" : "h-11 rounded-field px-3.5 text-sm",
          className,
        )}
        {...rest}
      >
        {/* one line always: a long label is cut with an ellipsis, never wrapped out of the box */}
        <BaseSelect.Value className="min-w-0 truncate" />
        <BaseSelect.Icon className="flex-none text-muted">
          <svg width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <path d="M3 4.5l3 3 3-3" />
          </svg>
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
                  className={selectItemClass}
                >
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
