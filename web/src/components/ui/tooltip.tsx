import { Tooltip } from "@base-ui/react/tooltip";
import type { ReactElement, ReactNode } from "react";

/**
 * A small tooltip that opens on hover and on keyboard focus (Base UI's Tooltip). `children` is the trigger: one element
 * that can take focus (a button, or a span with tabIndex 0) and names itself (its text or an aria-label), because the
 * tooltip only repeats it for the eye. It sits above the trigger, from its left edge on (a mark at a row's start).
 */
export function Tip({ label, children }: { label: ReactNode; children: ReactElement }) {
  return (
    <Tooltip.Root>
      <Tooltip.Trigger delay={200} render={children} />
      <Tooltip.Portal>
        <Tooltip.Positioner side="top" align="start" sideOffset={6} className="z-50">
          <Tooltip.Popup className="max-w-[280px] origin-(--transform-origin) rounded-field border border-line bg-surface-2 px-3 py-2 text-xs leading-4 text-pretty text-fg transition-[scale,opacity] duration-150 data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0">
            {label}
          </Tooltip.Popup>
        </Tooltip.Positioner>
      </Tooltip.Portal>
    </Tooltip.Root>
  );
}
