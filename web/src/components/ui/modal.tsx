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
};

/**
 * Modal on the desktop, bottom sheet on the phone (same component, one breakpoint). Max 520 wide (760 when `wide`),
 * 18px radius; the sheet has 22px top corners and a grab handle. Esc and the backdrop close it. The title and the footer
 * stay where they are: only the content between them scrolls when it is taller than the window.
 */
export function Modal({ open, onOpenChange, title, description, children, footer, className, closeLabel, wide }: Props) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-50 bg-black/50 transition-opacity duration-200 data-ending-style:opacity-0 data-starting-style:opacity-0" />
        <Dialog.Viewport className="fixed inset-0 z-50 flex items-end justify-center overflow-y-auto md:items-center md:p-4">
          <Dialog.Popup
            className={cx(
              "flex max-h-full w-full flex-col gap-4 overflow-hidden border border-line bg-surface p-5 shadow-(--shadow-toast) outline-none transition-[transform,opacity] duration-300 ease-out-soft md:rounded-card-lg",
              wide ? "md:max-w-[760px]" : "md:max-w-[520px]",
              "rounded-t-sheet data-ending-style:translate-y-full data-starting-style:translate-y-full md:data-ending-style:translate-y-2 md:data-ending-style:opacity-0 md:data-starting-style:translate-y-2 md:data-starting-style:opacity-0",
              className,
            )}
          >
            <div aria-hidden className="mx-auto -mt-2 h-1 w-9 flex-none rounded-sm bg-line md:hidden" />
            <div className="flex flex-none items-start gap-3">
              <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                <Dialog.Title className="text-[19px] leading-tight font-extrabold tracking-[-0.03em]">{title}</Dialog.Title>
                {description && (
                  <Dialog.Description className="text-[13px] leading-normal text-muted">{description}</Dialog.Description>
                )}
              </div>
              {closeLabel && (
                <Dialog.Close render={<IconButton variant="close" aria-label={closeLabel} className="bg-surface-2" />}>
                  <Icon name="x" size={14} />
                </Dialog.Close>
              )}
            </div>
            {/* the scrolling part reaches the edges, so its scrollbar sits on the border and focus rings are not cut */}
            {children && <div className="-mx-5 -my-1 flex min-h-0 flex-col gap-4 overflow-y-auto px-5 py-1">{children}</div>}
            {footer && <div className="flex flex-none justify-end gap-2">{footer}</div>}
          </Dialog.Popup>
        </Dialog.Viewport>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
