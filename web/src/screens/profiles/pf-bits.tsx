import { G } from "@/screens/users/user-bits";
import type { Gk } from "./profiles-model";

// The small pieces the profile screens share (list, matrix, page): the mark of a protocol, the glyph of a state, the board's
// 24-grid paths. The classes are in profiles.css (and profile-page.css).

export type Kind = "hy" | "vl" | "aw" | "tg" | "tt" | "x";

/** The protocol's class on the boards (hy, vl, aw, tg, tt); a protocol the panel does not know yet wears the sage mark. */
export const kindOf = (protocol: string): Kind => ({ hysteria2: "hy", vless: "vl", awg: "aw", mtproto: "tg", trusttunnel: "tt" })[protocol] as Kind | undefined ?? "x";

/** The board's mark per protocol (PK in the board's script) and the network it listens on. */
export const PK: Record<Kind, { d: string; net: "udp" | "tcp" }> = {
  hy: { d: "M13 3L5 14h6l-1 7 8-11h-6z", net: "udp" },
  vl: { d: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.7 2.5 15.3 0 18M12 3c-2.5 2.7-2.5 15.3 0 18", net: "tcp" },
  aw: { d: "M4 15.5a3.5 3.5 0 1 0 7 0 3.5 3.5 0 0 0-7 0zM11 15.5h9M17 15.5v-3", net: "udp" },
  tg: { d: "M20 4L3 11l6 2.5L11 20l3-4.5 4 3z", net: "tcp" },
  tt: { d: "M4.5 19v-6.5a7.5 7.5 0 0 1 15 0V19M9.5 19v-6a2.5 2.5 0 0 1 5 0v6", net: "tcp" },
  x: { d: "M12 4l8 4-8 4-8-4zM4 12l8 4 8-4M4 16l8 4 8-4", net: "tcp" },
};

export const D = {
  chev: "M9 6l6 6-6 6",
  plus: "M12 5v14M5 12h14",
  search: "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-4-4",
  warn: "M12 8v5M12 16.5v.01M10.3 4l-7.3 12.5a2 2 0 0 0 1.7 3h14.6a2 2 0 0 0 1.7-3L13.7 4a2 2 0 0 0-3.4 0z",
  stack: "M4 7h10M18 7h2M4 12h4M12 12h8M4 17h12M20 17h0M14 5v4M8 10v4M16 15v4",
  close: "M6 6l12 12M18 6L6 18",
  back: "M15 6l-6 6 6 6",
  server: "M4 5h16v6H4zM4 13h16v6H4zM8 8h.01M8 16h.01",
} as const;

/** The glyph of a state: a shape as well as a colour (works, half, crossed, spinning, empty). */
export const Gl = ({ k, shut }: { k: Gk | "shut"; shut?: boolean }) => <i className={`rw-gl ${shut ? "shut" : k}`} aria-hidden />;

/** The protocol's mark: its glyph on its hue (32px; the matrix draws it at 28). */
export function Mark({ k, className = "" }: { k: Kind; className?: string }) {
  return (
    <span className={`pv-g ${k} ${className}`.trim()} aria-hidden>
      <G d={PK[k].d} size={16} stroke={2} />
    </span>
  );
}
