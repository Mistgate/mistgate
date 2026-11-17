import type { ReactNode } from "react";
import { cx } from "@/lib/cx";

// The small drawn pieces of the wizard, in the board's own marks (pn.css: .i, .fz-cb, .nx-rad, .rw-gl, .rw-chip, .pv-g).

export const P = {
  check: "M5 12.5l4.5 4.5L19 7.5",
  back: "M15 6l-6 6 6 6",
  next: "M9 6l6 6-6 6",
  code: "M9 8l-4 4 4 4M15 8l4 4-4 4",
  phone: "M8 3h8v18H8zM11 18h2",
} as const;

/** A 24-grid glyph of the board: 16 px, a 1.5 stroke, round ends (the board's `.i`). */
export function Gl({ d, className }: { d: string; className?: string }) {
  return (
    <svg className={cx("pw-i", className)} viewBox="0 0 24 24" aria-hidden="true">
      <path d={d} />
    </svg>
  );
}

/** The tick box of a list of nodes or groups (the board's `.fz-cb.us-cb`). */
export function Cb({ on }: { on: boolean }) {
  return (
    <span aria-hidden="true" className={cx("pw-cb", on && "on")}>
      <i>
        <Gl d={P.check} />
      </i>
    </span>
  );
}

/** The radio dot of a task's card (the board's `.nx-rad`). */
export function Rad() {
  return (
    <span aria-hidden="true" className="pw-rad">
      <i />
    </span>
  );
}

/** The dot before a result: "ok" a filled accent, "part" half, "bad" a cross, "busy" a turning ring, "none" a ring (the board's `.rw-gl`). */
export function State({ kind, title }: { kind: "ok" | "part" | "bad" | "busy" | "none"; title?: string }) {
  return <i className={cx("pw-gl", kind)} title={title} aria-hidden="true" />;
}

/** The chip of a node's reach (the board's `.rw-chip`): its dot, then a few words. */
export function Chip({ kind, children }: { kind: "ok" | "part" | "bad" | "none"; children: ReactNode }) {
  return (
    <span className={cx("pw-chip", kind)}>
      <i aria-hidden="true" />
      {children}
    </span>
  );
}
