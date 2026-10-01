import type { ReactNode } from "react";
import { IconButton } from "./icon-button";

type Props = {
  /** What is shown between the buttons, e.g. "100 GB". */
  children: ReactNode;
  onDecrement: () => void;
  onIncrement: () => void;
  decrementDisabled?: boolean;
  incrementDisabled?: boolean;
  decrementLabel: string;
  incrementLabel: string;
};

/** "No limit" as ∞: short enough for a stepper; the word stays for screen readers and as a tooltip. */
export function Infinite({ label }: { label: string }) {
  return (
    <span title={label} className="text-lg leading-none">
      <span aria-hidden>∞</span>
      <span className="sr-only">{label}</span>
    </span>
  );
}

/** - value + with the plus in the accent. The bars are drawn, not glyphs. */
export function Stepper({
  children,
  onDecrement,
  onIncrement,
  decrementDisabled,
  incrementDisabled,
  decrementLabel,
  incrementLabel,
}: Props) {
  return (
    <div role="group" className="flex h-[26px] items-center gap-2">
      <IconButton variant="round" aria-label={decrementLabel} disabled={decrementDisabled} onClick={onDecrement}>
        <span aria-hidden className="h-0.5 w-2.5 rounded-[1px] bg-fg" />
      </IconButton>
      <output className="flex min-w-0 flex-1 items-center justify-center text-sm font-extrabold whitespace-nowrap tabular-nums">
        {children}
      </output>
      <IconButton variant="roundAccent" aria-label={incrementLabel} disabled={incrementDisabled} onClick={onIncrement}>
        <span aria-hidden className="relative block size-2.5">
          <span className="absolute top-1/2 left-0 h-0.5 w-full -translate-y-1/2 rounded-[1px] bg-on-accent" />
          <span className="absolute top-0 left-1/2 h-full w-0.5 -translate-x-1/2 rounded-[1px] bg-on-accent" />
        </span>
      </IconButton>
    </div>
  );
}
