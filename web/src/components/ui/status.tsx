import type { MessageKey } from "@/i18n/en";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";

// Status is always colour + glyph + word (never colour alone); see .tone-* and .pill in index.css.
// blip = the hoster dropped for a moment: grey-blue, deliberately not red.
const kinds = {
  ok: { glyph: "", word: "status.ok" },
  warn: { glyph: "!", word: "status.warn" },
  bad: { glyph: "×", word: "status.bad" },
  off: { glyph: "–", word: "status.off" },
  busy: { glyph: "", word: "status.busy" },
  blip: { glyph: "~", word: "status.blip" },
} as const satisfies Record<string, { glyph: string; word: MessageKey }>;

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
  glyphOnly,
}: {
  kind: StatusKind;
  label?: string;
  sm?: boolean;
  /** Just the round glyph in a 20px pill (the fleet health strip); the surrounding text says the rest. */
  glyphOnly?: boolean;
}) {
  const t = useT();
  const { glyph, word } = kinds[kind];
  const text = glyphOnly ? "" : (label ?? t(word));
  return (
    <span
      role={glyphOnly ? "img" : undefined}
      aria-label={glyphOnly ? t(word) : undefined}
      className={cx(
        `tone-${kind} pill inline-flex flex-none items-center gap-1.5 rounded-xl font-bold whitespace-nowrap leading-none`,
        glyphOnly ? "h-5 px-0.5" : sm ? "h-[22px] pr-2 pl-1 text-[11px]" : "h-[26px] pr-2.5 pl-1.5 text-xs",
      )}
    >
      <span
        aria-hidden
        className={cx(
          "box-border grid size-3.5 flex-none place-items-center rounded-full text-[10px] font-extrabold text-on-accent",
          kind === "busy" ? "animate-[mg-ui-spin_1s_linear_infinite] border-2 border-(color:--c) border-t-transparent" : "bg-(--c)",
        )}
      >
        {kind === "ok" ? (
          <span className="-mt-0.5 h-1.5 w-[3px] rotate-45 border-r-2 border-b-2 border-on-accent" />
        ) : (
          glyph
        )}
      </span>
      {text}
    </span>
  );
}
