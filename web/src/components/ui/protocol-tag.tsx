import { cx } from "@/lib/cx";
import { protocolName } from "@/lib/series";
import { toneTagClass } from "./bits";
import type { Tone } from "./icons";

// One tone and one 24-grid glyph per protocol, so a protocol reads the same in every list: Hysteria2 mint, VLESS sky,
// AmneziaWG lavender (the older generation a quiet grey), the relay and the DNS tunnel sand, TrustTunnel sage.
const kinds = {
  hysteria2: { tone: "mint", d: "M13 3L5 14h6l-1 7 8-11h-6z" },
  vless: { tone: "sky", d: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.7 2.5 15.3 0 18M12 3c-2.5 2.7-2.5 15.3 0 18" },
  awg: { tone: "lavender", d: "M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z" },
  "awg-old": { tone: "lavender", quiet: true, d: "M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z" },
  relay: { tone: "sand", d: "M4 8h13l-3-3M20 16H7l3 3" },
  trusttunnel: { tone: "sage", d: "M4.5 19v-6.5a7.5 7.5 0 0 1 15 0V19M9.5 19v-6a2.5 2.5 0 0 1 5 0v6" },
  dns: { tone: "sand", d: "M4 9h12M13 6l3 3-3 3M20 15H8M11 12l-3 3 3 3" },
} as const satisfies Record<string, { tone: Tone; quiet?: boolean; d: string }>;

export type ProtocolKind = keyof typeof kinds;

type Look = { tone: Tone; quiet?: boolean; d: string };

// the ids a profile carries, and the short ones that are written by hand
const alias: Record<string, ProtocolKind> = { hy2: "hysteria2", amneziawg: "awg", awg2: "awg" };

/**
 * A protocol as a tag: its glyph in its tone, then its name (24px, 6px radius). `compact` is the 28px glyph alone that opens
 * into the name when the pointer or focus is on it, for dense lists; the name stays in the tag for screen readers. `label`
 * replaces the name (a translated one: the relay, the DNS tunnel). `expanded` keeps a compact tag open (a phone has no pointer).
 */
export function ProtocolTag({ protocol, label, compact, expanded, className }: { protocol: string; label?: string; compact?: boolean; expanded?: boolean; className?: string }) {
  const kind = protocol in kinds ? (protocol as ProtocolKind) : alias[protocol];
  const look: Look | undefined = kind ? kinds[kind] : undefined;
  const name = label ?? (kind === "trusttunnel" ? "TrustTunnel" : protocolName(protocol));
  return (
    <span
      title={compact ? name : undefined}
      tabIndex={compact ? 0 : undefined}
      className={cx(
        "group/pt inline-flex flex-none items-center text-xs leading-4 font-medium whitespace-nowrap",
        compact ? "h-7 rounded-ctl px-[7px] transition-colors duration-150 ease-out focus-visible:outline-offset-1" : "h-6 gap-1.5 rounded-chip ps-1.5 pe-2",
        !look ? "bg-step text-sec" : look.quiet ? "bg-step text-tone-lavender" : toneTagClass[look.tone],
        className,
      )}
    >
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden className="block flex-none">
        <path d={look?.d ?? "M12 12h.01"} />
      </svg>
      <span
        className={cx(
          compact &&
            (expanded
              ? "ms-1.5 inline-block max-w-[120px] overflow-hidden"
              : "inline-block max-w-0 overflow-hidden opacity-0 transition-[max-width,opacity,margin] duration-[350ms] ease-out group-hover/pt:ms-1.5 group-hover/pt:max-w-[120px] group-hover/pt:opacity-100 group-focus-visible/pt:ms-1.5 group-focus-visible/pt:max-w-[120px] group-focus-visible/pt:opacity-100"),
        )}
      >
        {name}
      </span>
    </span>
  );
}
