import type { ReactNode } from "react";
import { cx } from "@/lib/cx";

// The AmneziaWG obfuscation keys of a .conf: junk packets, padding, headers, signature packets and their timers.
const obfuscationKey = /^(?:Jc|Jmin|Jmax|S[1-4]|H[1-4]|I[1-5]|J[1-3]|ITime)$/i;

export type ConfPart = { text: string; kind: "plain" | "section" | "key" | "obf" | "mask" | "comment" };

/**
 * One line of the client fragment as coloured parts: `[Section]` headers, `key =` names (the obfuscation keys apart),
 * the masked secrets dimmed, `#` comments dimmed. A line that is not .conf-shaped (a share link) only has its masks
 * dimmed, so every protocol's preview goes through here.
 */
export function confParts(line: string): ConfPart[] {
  const masks = (text: string, kind: ConfPart["kind"] = "plain"): ConfPart[] =>
    text
      .split(/(•{4,})/)
      .filter(Boolean)
      .map((p) => ({ text: p, kind: /^•+$/.test(p) ? "mask" : kind }));
  if (/^\s*#/.test(line)) return [{ text: line, kind: "comment" }];
  if (/^\s*\[[^\]]+\]\s*$/.test(line)) return [{ text: line, kind: "section" }];
  const kv = /^(\s*)([A-Za-z][A-Za-z0-9]*)(\s*=\s*)(.*)$/.exec(line);
  if (!kv) return masks(line);
  const [, lead, key, eq, value] = kv;
  return [{ text: lead! + key!, kind: obfuscationKey.test(key!) ? "obf" : "key" }, { text: eq!, kind: "plain" as const }, ...masks(value!)];
}

// Contrast: the tones go through .id-fg (darkened on the light theme); masks and comments are the muted text colour.
const look: Record<ConfPart["kind"], { tone?: string; cls?: string }> = {
  plain: {},
  section: { tone: "lavender", cls: "font-bold" },
  key: { tone: "sky" },
  obf: { tone: "sage" },
  mask: { cls: "text-muted" },
  comment: { cls: "text-muted" },
};

/** One line of the client fragment, coloured (see confParts). The text is unchanged, so copying it still gives the file. */
export function ConfLine({ text }: { text: string }) {
  return (
    <div className="break-all whitespace-pre-wrap">
      {confParts(text).map((p, i): ReactNode => {
        const l = look[p.kind];
        return l.tone || l.cls ? (
          <span key={i} data-tone={l.tone} className={cx(l.tone && "id-fg", l.cls)}>
            {p.text}
          </span>
        ) : (
          p.text
        );
      })}
    </div>
  );
}
