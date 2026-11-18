import { Dialog } from "@base-ui/react/dialog";
import type { ReactNode } from "react";
import { cx } from "@/lib/cx";
import { useEffectsEnabled } from "@/lib/effects";
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
  /** Something before the title (a person's avatar), which makes the window the board's snug one (the send window): the header starts at the top with a 4px-tight title and a 13px quiet line, and the body has 8px between its blocks and 4px above the first. */
  lead?: ReactNode;
  /** `data-shot` of the window, the handle the dev kit's screenshots find it by. */
  shot?: string;
};

/**
 * Modal on the desktop, bottom sheet on the phone (same component, one breakpoint). Max 520 wide (760 when `wide`), a 20px
 * radius (top corners only on the sheet). Esc and the backdrop close it. The header and the footer stay where they are: only
 * the content between them scrolls when it is taller than the window. The header, the body and the footer are the board's, and
 * their room follows the window's own width as the board's container query does: a window up to 720 wide (the default 520, a
 * sheet on a phone) has a 64px header (16 above the 20px title, 8 below, the 40px x on the end), 16px sides and a 12px-high
 * footer; a wider one (760 and up) has a 72px header, 24px sides and a 14px-high footer. The footer is a band with a hairline above it.
 * Like the board's window it has no shadow: the scrim and the hairline are enough. It is at most 760 tall on a desktop (the board's min(760px, ...)) and leaves 24px above a sheet; what does not fit scrolls.
 * It opens the way the board's window does: a spring from 90% on a desktop, a slide up on the phone. That motion is part of
 * the effects (Settings -> Interface); with them off, or with reduced motion, the window just fades in.
 */
export function Modal({ open, onOpenChange, title, description, children, footer, className, closeLabel, wide, lead, shot }: Props) {
  const fx = useEffectsEnabled();
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-50 bg-(--scrim) transition-opacity duration-200 data-ending-style:opacity-0 data-starting-style:opacity-0" />
        <Dialog.Viewport className="fixed inset-0 z-50 flex items-end justify-center overflow-y-auto md:items-center md:p-4">
          <Dialog.Popup
            data-shot={shot}
            className={cx(
              "@container/win flex max-h-[calc(100%-24px)] w-full flex-col overflow-hidden border border-line bg-surface outline-none transition-[translate,scale,opacity] md:max-h-[min(760px,100%)] md:rounded-sheet",
              wide ? "md:max-w-[760px]" : "md:max-w-[520px]",
              "rounded-t-sheet data-ending-style:opacity-0 data-starting-style:opacity-0",
              // the sheet rises 48px (the board's an-sheet, .4s), the window grows from 90% on the spring (pn-pop, .3s)
              fx
                ? "duration-[400ms] ease-(--e) data-starting-style:translate-y-12 md:duration-300 md:ease-spring md:data-starting-style:translate-y-0 md:data-starting-style:scale-90"
                : "duration-150 ease-out",
              className,
            )}
          >
            <div className={cx("flex flex-none ps-6 pe-4 pt-5 pb-3 @max-[720px]/win:ps-4 @max-[720px]/win:pe-3 @max-[720px]/win:pt-4 @max-[720px]/win:pb-2", lead ? "items-start" : "items-center gap-3", !children && !footer && "pb-6 @max-[720px]/win:pb-5")}>
              {lead && <span className="me-3 mt-0.5 flex-none">{lead}</span>}
              <div className={cx("flex min-w-0 flex-1 flex-col", lead ? "gap-1" : "gap-1.5")}>
                <Dialog.Title className={cx("text-xl font-medium", lead ? "leading-7" : "leading-5")}>{title}</Dialog.Title>
                {description && (
                  <Dialog.Description className={lead ? "text-[13px] leading-[18px] text-pretty text-muted" : "text-sm leading-5 text-sec"}>{description}</Dialog.Description>
                )}
              </div>
              {closeLabel && (
                <Dialog.Close render={<IconButton variant="square" aria-label={closeLabel} />}>
                  <Icon name="x" size={16} />
                </Dialog.Close>
              )}
            </div>
            {/* the scrolling part reaches the edges, so its scrollbar sits on the border and focus rings are not cut */}
            {children && (
              <div className={cx("flex min-h-0 flex-col overflow-y-auto [scrollbar-width:thin] [&>*]:shrink-0 px-6 pb-6 @max-[720px]/win:px-4 @max-[720px]/win:pb-5", lead ? "gap-2 pt-1" : "gap-5 pt-2")}>{children}</div>
            )}
            {footer && (
              <div className="flex flex-none flex-wrap items-center justify-end gap-2 border-t border-line bg-[color-mix(in_srgb,var(--text)_2%,transparent)] px-6 py-3.5 @max-[720px]/win:px-4 @max-[720px]/win:py-3">
                {footer}
              </div>
            )}
          </Dialog.Popup>
        </Dialog.Viewport>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
