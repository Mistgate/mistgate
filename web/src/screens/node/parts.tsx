import { cx } from "@/lib/cx";

// The small pieces the node page's cards share.

/** The spinner of a busy button (the board's .an-sp): a ring that turns, in the button's own colour. */
export function Spin({ className }: { className?: string }) {
  return <span aria-hidden className={cx("size-3.5 flex-none animate-[mg-ui-spin_0.8s_linear_infinite] rounded-full border-2 border-current border-t-transparent", className)} />;
}

/** The transport a profile's port is on, as the board writes it ("udp/443"): Hysteria2 and AmneziaWG run over UDP, the rest over TCP. */
export const transportOf = (protocol: string) => (protocol === "hysteria2" || protocol === "amneziawg" || protocol === "awg" ? "udp" : "tcp");
