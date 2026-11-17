import type { ReactNode } from "react";
import type { StatusKind } from "@/components/ui/status";
import { cx } from "@/lib/cx";

// The small pieces the Overview's cards share (the board's .ch, .cf and .dot).

/** A card's head: the title 16/24 with a quiet count after it, and what goes at the end (a switch, a tag). */
export function CardHead({ title, count, children, className }: { title?: ReactNode; count?: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={cx("flex items-center gap-3 px-6 pt-4 pb-3 max-md:flex-wrap max-md:px-4", className)}>
      {title && (
        <h3 className="me-auto text-base leading-6 font-medium">
          {title}
          {count != null && count !== "" && (
            <>
              {" "}
              <span className="ms-1 font-normal text-muted">{count}</span>
            </>
          )}
        </h3>
      )}
      {children}
    </div>
  );
}

/** The link under a list ("All people"): a hairline above it, the link itself quiet and underlined. */
export const footLink = "text-sec underline decoration-(--border) underline-offset-4 hover:text-fg";
export function CardFoot({ children }: { children: ReactNode }) {
  return <div className="border-t border-line px-6 py-3 text-[13px] max-md:px-4">{children}</div>;
}

const dots: Record<StatusKind, string> = {
  ok: "bg-ok",
  warn: "bg-(--ov-warn)",
  bad: "bg-danger",
  off: "bg-muted",
  blip: "bg-tone-blip",
  busy: "bg-tone-sky",
};

/** The 8px dot of the board: green works, amber needs a look, red is broken, grey-blue is a blip, grey waits. */
export function OvDot({ kind, className }: { kind: StatusKind; className?: string }) {
  return <i aria-hidden className={cx("block size-2 flex-none rounded-full", dots[kind], className)} />;
}

/** A glyph of the board's rows: 18px, stroke 1.75 (a triangle, a ring with a bar, a star, a tick). */
export function Glyph({ d, size = 18, stroke = 1.75 }: { d: string; size?: number; stroke?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={stroke} strokeLinecap="round" strokeLinejoin="round" aria-hidden className="block flex-none">
      <path d={d} />
    </svg>
  );
}

export const glyphs = {
  warn: "M12 4.5l8.5 14.5h-17zM12 10.5v4M12 17v.01",
  bad: "M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17zM12 8v5M12 16v.01",
  ai: "M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z",
  check: "M5 12.5l4.5 4.5L19 7.5",
} as const;
