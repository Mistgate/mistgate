import type { ReactNode } from "react";
import { IconButton } from "./icon-button";
import { Icon } from "./icons";

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

/**
 * "-" value "+" in a capsule (the board's .sv-step): 36px, a 10px radius, the page colour inside, two 32px buttons and the
 * value in the middle (56px at least, longer text widens it). The bars are drawn, not glyphs.
 */
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
    <div role="group" className="inline-flex h-9 items-center gap-0.5 rounded-field border border-line bg-canvas px-0.5">
      <IconButton variant="compact" aria-label={decrementLabel} disabled={decrementDisabled} onClick={onDecrement}>
        <Icon name="minus" size={16} />
      </IconButton>
      <output className="flex min-w-14 items-center justify-center text-[13px] font-medium whitespace-nowrap tabular-nums">{children}</output>
      <IconButton variant="compact" aria-label={incrementLabel} disabled={incrementDisabled} onClick={onIncrement}>
        <Icon name="plus" size={16} />
      </IconButton>
    </div>
  );
}
