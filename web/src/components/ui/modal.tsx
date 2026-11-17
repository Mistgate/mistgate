import { Dialog } from "@base-ui/react/dialog";
import type { ReactNode } from "react";
import { cx } from "@/lib/cx";
import { IconButton } from "./icon-button";
import { Icon } from "./icons";

type Props = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
  /** Buttons row under the content. */
  footer?: ReactNode;
  className?: string;
  /** Accessible name of a round x in the corner; without it there is no button (Esc and the backdrop still close). */
  closeLabel?: string;
  /** 760 wide instead of 520 (a form in two columns). */
  wide?: boolean;
  /** `data-shot` of the window, the handle the dev kit's screenshots find it by. */
  shot?: string;
};

/**
 * Modal on the desktop, bottom sheet on the phone (same component, one breakpoint). Max 520 wide (760 when `wide`), a 20px
 * radius (top corners only on the sheet). Esc and the backdrop close it. The header and the footer stay where they are: only
 * the content between them scrolls when it is taller than the window. The footer is a band with a hairline above it.
 */
export function Modal({ open, onOpenChange, title, description, children, footer, className, closeLabel, wide, shot }: Props) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-50 bg-(--scrim) transition-opacity duration-200 data-ending-style:opacity-0 data-starting-style:opacity-0" />
        <Dialog.Viewport className="fixed inset-0 z-50 flex items-end justify-center overflow-y-auto md:items-center md:p-4">
          <Dialog.Popup
            data-shot={shot}
            className={cx(
              "flex max-h-full w-full flex-col overflow-hidden border border-line bg-surface shadow-(--shadow-dlg) outline-none transition-[transform,opacity] duration-300 ease-out md:rounded-sheet",
              wide ? "md:max-w-[760px]" : "md:max-w-[520px]",
              "rounded-t-sheet data-ending-style:translate-y-full data-starting-style:translate-y-full md:data-ending-style:translate-y-2 md:data-ending-style:opacity-0 md:data-starting-style:translate-y-2 md:data-starting-style:opacity-0",
              className,
            )}
          >
            <div className={cx("flex flex-none items-start gap-3 ps-4 pe-3 pt-4 pb-2 md:ps-6 md:pe-4 md:pt-5 md:pb-3", !children && !footer && "pb-5 md:pb-6")}>
              <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                <Dialog.Title className="text-xl leading-7 font-medium">{title}</Dialog.Title>
                {description && (
                  <Dialog.Description className="text-sm leading-5 text-sec">{description}</Dialog.Description>
                )}
              </div>
              {closeLabel && (
                <Dialog.Close render={<IconButton variant="close" aria-label={closeLabel} />}>
                  <Icon name="x" size={16} />
                </Dialog.Close>
              )}
            </div>
            {/* the scrolling part reaches the edges, so its scrollbar sits on the border and focus rings are not cut */}
            {children && <div className="flex min-h-0 flex-col gap-5 overflow-y-auto px-4 pt-2 pb-5 md:px-6 md:pb-6">{children}</div>}
            {footer && (
              <div className="flex flex-none flex-wrap items-center justify-end gap-2 border-t border-line bg-[color-mix(in_srgb,var(--text)_2%,transparent)] px-4 py-3 md:px-6 md:py-3.5">
                {footer}
              </div>
            )}
          </Dialog.Popup>
        </Dialog.Viewport>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
