import type { ReactNode } from "react";
import { cx } from "@/lib/cx";
import { Glyph, glyphs } from "@/screens/overview-parts";

// The small pieces the node page's cards share.

/** The spinner of a busy button (the board's .an-sp): a ring that turns, in the button's own colour. */
export function Spin({ className }: { className?: string }) {
  return <span aria-hidden className={cx("size-3.5 flex-none animate-[mg-ui-spin_0.8s_linear_infinite] rounded-full border-2 border-current border-t-transparent", className)} />;
}

/** The transport a profile's port is on, as the board writes it ("udp/443"): Hysteria2 and AmneziaWG run over UDP, the rest over TCP. */
export const transportOf = (protocol: string) => (protocol === "hysteria2" || protocol === "amneziawg" || protocol === "awg" ? "udp" : "tcp");

/**
 * A note inside a page (the board's .nd-wa): a glyph, a line in medium and what it means under it, on the tint of its weight.
 * Amber when something is off, red when the node cannot be reached.
 */
export function Wa({ tone, title, children, className }: { tone: "warn" | "crit"; title: ReactNode; children?: ReactNode; className?: string }) {
  return (
    <div className={cx("grid grid-cols-[32px_minmax(0,1fr)] items-start gap-3 rounded-xl p-3", tone === "crit" ? "bg-danger-soft" : "bg-warn-soft", className)}>
      <span aria-hidden className={cx("grid size-8 place-items-center", tone === "crit" ? "text-danger" : "text-warn")}>
        <Glyph d={glyphs.warn} />
      </span>
      <div>
        <b className="block font-medium">{title}</b>
        {children && <span className="mt-0.5 block text-[13px] leading-[18px] text-sec">{children}</span>}
      </div>
    </div>
  );
}
