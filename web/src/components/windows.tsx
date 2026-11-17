import type { CSSProperties, SVGProps } from "react";
import { Icon } from "@/components/ui/icons";
import { cx } from "@/lib/cx";
import { prefersReducedMotion, useEffectsEnabled } from "@/lib/effects";
import "./windows.css";

// What the four windows (sign-in, confirm, search, new node) share: the glyphs the icon set lacks, the spinner, the check mark
// of a step, the halo and the wave. The glyphs are the board's paths on the 24 grid, 1.5 stroke.

export const glyphs = {
  fingerprint:
    "M12 10a2 2 0 0 0-2 2c0 1.02-.1 2.51-.26 4M14 13.12c0 2.38 0 6.38-1 8.88M17.29 21.02c.12-.6.43-2.3.5-3.02M2 12a10 10 0 0 1 18-6M2 16h.01M21.8 16c.2-2 .131-5.354 0-6M5 19.5C5.5 18 6 15 6 12a6 6 0 0 1 .34-2M8.65 22c.21-.66.45-1.32.57-2M9 6.8a6 6 0 0 1 9 5.2v2",
  swap: "M4 8h13l-3-3M20 16H7l3 3",
  shield: "M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z",
  lock: "M7 11V8a5 5 0 0 1 10 0v3M6 11h12v9H6z",
  firewall: "M4 6h16v12H4zM4 12h16M9 6v6M15 12v6",
  agent: "M12 3l8 4.5v9L12 21l-8-4.5v-9zM4 7.5l8 4.5 8-4.5M12 12v9",
  host: "M4 7h10M18 7h2M4 17h2M10 17h10M16 5v4M8 15v4",
  warn: "M12 4.5l8.5 14.5h-17zM12 10.5v4M12 17v.01",
  chevron: "M7 10l5 5 5-5",
  search: "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-4-4",
  plus: "M12 5v14M5 12h14",
  key: "M8 15a4 4 0 1 1 3.9-5H21v3h-2v2h-3v-2h-4.1A4 4 0 0 1 8 15z",
  password: "M4 9h16v6H4zM8 12h.01M12 12h.01M16 12h.01",
  terminal: "M4 5h16v14H4zM7 9l3 3-3 3M12 15h5",
  dot: "M12 12h.01",
  check: "M5 12.5l4.5 4.5L19 7.5",
  cross: "M7 7l10 10M17 7L7 17",
  package: "M12 3l8 4.5v9L12 21l-8-4.5v-9zM4 7.5l8 4.5 8-4.5M12 12v9",
} as const;

/** A board glyph the icon set lacks: a path on the 24 grid, 16 px, drawn as the icons are. */
export function Glyph({ d, size = 16, strokeWidth = 1.5, className, ...rest }: { d: string; size?: number; strokeWidth?: number } & Omit<SVGProps<SVGSVGElement>, "d">) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className={cx("block flex-none", className)}
      {...rest}
    >
      <path d={d} />
    </svg>
  );
}

/** The turning ring of a button that waits (an-sp): 14px, the colour of the text, the top of the ring open. */
export function Spinner({ className }: { className?: string }) {
  return <span aria-hidden className={cx("size-3.5 flex-none animate-[mg-ui-spin_0.7s_linear_infinite] rounded-full border-[1.5px] border-current border-t-transparent", className)} />;
}

/** The mark of a step or a check: a ring turns while it works (`now`), the tick draws itself when it is done (`done`). */
export function Tick({ state, className }: { state?: "now" | "done" | "bad" | ""; className?: string }) {
  return (
    <span aria-hidden className={cx("wn-ts", state, className)}>
      <Icon name={state === "bad" ? "x" : "check"} />
    </span>
  );
}

/** Whether the reveal animation of this device is on and the person asked for no calm: the halo and the wave belong to it. */
export function useReveal(): boolean {
  const on = useEffectsEnabled();
  return on && !prefersReducedMotion();
}

/** The dotted halo of a window (lg::before, cfm::before, an-dn::before): only with the reveal animation on. */
export function Halo({ className, style }: { className?: string; style?: CSSProperties }) {
  const on = useReveal();
  return on ? <span aria-hidden className={cx("wn-halo", className)} style={style} /> : null;
}

/** The wave across a window from `at` (a CSS position, the board's 50% 46% for the sign-in page): only with the reveal animation on. */
export function WaveField({ at, className }: { at?: string; className?: string }) {
  const on = useReveal();
  if (!on) return null;
  return (
    <div aria-hidden className={cx("wn-wave", className)} style={at ? ({ ["--wave-at" as string]: at } as CSSProperties) : undefined}>
      <i />
    </div>
  );
}
