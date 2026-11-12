import type { ReactNode } from "react";
import type { MessageKey } from "@/i18n/en";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";

// Status is always colour + glyph + word (never colour alone); see .tone-* and .pill in index.css.
// blip = the hoster dropped for a moment: grey-blue, deliberately not red.
// `mark` is the glyph as a stroke path in an 8x8 box: drawn, not typed, so it sits in the middle of its disc in any font.
const kinds = {
  ok: { mark: "M1.6 4.2l1.6 1.6 3.2-3.5", word: "status.ok" },
  warn: { mark: "M4 1.3v3.1M4 6.6v.01", word: "status.warn" },
  bad: { mark: "M2 2l4 4M6 2L2 6", word: "status.bad" },
  off: { mark: "M1.9 4h4.2", word: "status.off" },
  busy: { mark: "", word: "status.busy" },
  blip: { mark: "M1.2 4.7c.9-1.7 1.9-1.7 2.8-.7s1.9 1 2.8-.7", word: "status.blip" },
} as const satisfies Record<string, { mark: string; word: MessageKey }>;

/** A glyph for the round status disc: a stroke path in an 8x8 box, in the disc's text colour. */
export function GlyphMark({ d }: { d: string }) {
  return (
    <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden className="block">
      <path d={d} fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

export type StatusKind = keyof typeof kinds;

/** Text colour of a status label in lists: healthy, blip and off stay quiet grey, the rest take their tone. */
export const kindTextClass: Record<StatusKind, string> = {
  ok: "text-muted",
  off: "text-muted",
  blip: "text-muted",
  warn: "text-warn-text",
  bad: "text-danger-text",
  busy: "text-[color-mix(in_oklch,var(--accent-sky)_var(--_text-pct),#000)]",
};

/** Bare dot for dense lists (nodes table, cards). `busy` is a spinning ring. */
export function StatusDot({ kind }: { kind: StatusKind }) {
  return (
    <span
      aria-hidden
      className={cx(
        `tone-${kind} box-border inline-block size-2 flex-none rounded-full`,
        kind === "busy" ? "animate-[mg-ui-spin_1s_linear_infinite] border-2 border-(color:--c) border-t-transparent" : "bg-(--c)",
      )}
    />
  );
}

/** Tinted pill: glyph, then the word. `label` overrides the default word; `sm` is the 22px size. */
export function StatusPill({
  kind,
  label,
  sm,
  bar,
  glyphOnly,
}: {
  kind: StatusKind;
  label?: string;
  sm?: boolean;
  /** As tall as the other controls of the top bar (h-bar), with their radius. */
  bar?: boolean;
  /** Just the round glyph in a 20px pill (the fleet health strip); the surrounding text says the rest. */
  glyphOnly?: boolean;
}) {
  const t = useT();
  const { word } = kinds[kind];
  const text = glyphOnly ? "" : (label ?? t(word));
  return (
    <span
      role={glyphOnly ? "img" : undefined}
      aria-label={glyphOnly ? t(word) : undefined}
      className={cx(
        `tone-${kind} pill inline-flex flex-none items-center gap-1.5 font-bold whitespace-nowrap leading-none`,
        glyphOnly ? "h-5 rounded-xl px-0.5" : bar ? "h-bar rounded-ctl pe-3 ps-2 text-xs" : sm ? "h-[22px] rounded-xl pe-2 ps-1 text-[11px]" : "h-[26px] rounded-xl pe-2.5 ps-1.5 text-xs",
      )}
    >
      <StatusGlyph kind={kind} />
      {text}
    </span>
  );
}

/**
 * The pill's round 14px glyph on its own: a tick, "!", "×" or "–" on the status colour, so a state reads by its shape as
 * well as its hue. Decorative: whoever shows it names the state (aria-label, a word beside it). `glyph` replaces the mark
 * for a state that shares a colour with another (a node in a block list: the warn colour, a list mark instead of "!").
 */
export function StatusGlyph({ kind, glyph }: { kind: StatusKind; glyph?: ReactNode }) {
  return (
    <span
      aria-hidden
      className={cx(
        `tone-${kind} box-border grid size-3.5 flex-none place-items-center rounded-full text-on-accent`,
        kind === "busy" ? "animate-[mg-ui-spin_1s_linear_infinite] border-2 border-(color:--c) border-t-transparent" : "bg-(--c)",
      )}
    >
      {glyph ?? (kinds[kind].mark && <GlyphMark d={kinds[kind].mark} />)}
    </span>
  );
}
