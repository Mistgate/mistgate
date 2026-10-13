import { useCallback, useId, useState } from "react";
import { MiniMarkdown, safeHttpsUrl } from "@/components/mini-markdown";
import { SectionLabel } from "@/components/ui/bits";
import { StatusPill } from "@/components/ui/status";
import { useLang, useT, type Lang } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import type { Updates } from "@/lib/updates";

export type ReleaseNote = Updates["agentNotes"][number];

/** The notes in the interface language; a release without them in it falls back to English, then to whatever it has. */
export const noteText = (n: Pick<ReleaseNote, Lang>, lang: Lang): string => n[lang] || n.en || n.ru || n.zh;

/** One release: its version (a link to the GitHub page), date, the security badge and the notes. `clamp` caps the height. */
function Entry({ note, clamp }: { note: ReleaseNote; clamp?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const lang = useLang();
  const id = useId();
  const [open, setOpen] = useState(false);
  const [overflows, setOverflows] = useState(false);
  const text = noteText(note, lang);
  // measured when the (clamped) body appears; a change of language mounts a new body (its key), so it is measured again
  const measure = useCallback(
    (el: HTMLDivElement | null) => {
      if (clamp && el) setOverflows(el.scrollHeight > el.clientHeight + 1);
    },
    [clamp],
  );
  // jsdom and a not yet laid out pane measure nothing: a long text gets its button from its length as well
  const tall = clamp && (overflows || open || text.split("\n").length > 10);
  const href = safeHttpsUrl(note.url);
  return (
    <article className="flex min-w-0 flex-col gap-1.5" aria-label={note.version}>
      <header className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        {href ? (
          <a
            href={href}
            target="_blank"
            rel="noopener noreferrer"
            title={t("up.notes.open", { version: note.version })}
            className="cursor-pointer font-mono text-xs font-bold text-accent-text underline underline-offset-2 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
          >
            {note.version}
          </a>
        ) : (
          <span className="font-mono text-xs font-bold">{note.version}</span>
        )}
        {note.publishedUnix > 0 && <span className="text-xs text-muted">{fmt.date(note.publishedUnix)}</span>}
        {note.security && <StatusPill kind="warn" label={t("up.notes.security")} sm />}
      </header>
      {text ? (
        <>
          <div
            key={lang}
            ref={measure}
            id={id}
            className={cx(
              "min-w-0 text-[13px] leading-normal text-pretty break-words text-fg",
              clamp && !open && "max-h-56 overflow-hidden",
              tall && !open && "[mask-image:linear-gradient(to_bottom,#000_75%,transparent)]",
            )}
          >
            <MiniMarkdown text={text} />
          </div>
          {tall && (
            <button
              type="button"
              aria-expanded={open}
              aria-controls={id}
              onClick={() => setOpen(!open)}
              className="-ml-2 h-7 w-fit cursor-pointer rounded-ctl px-2 text-xs font-bold text-muted transition-colors duration-200 hover:bg-surface-2 hover:text-fg"
            >
              {open ? t("up.notes.showLess") : t("up.notes.showAll")}
            </button>
          )}
        </>
      ) : (
        <p className="text-xs text-muted">{t("up.notes.noText")}</p>
      )}
    </article>
  );
}

/**
 * "What's new" in a card: the newest release of the range in full (clamped, with "Show all"), the older ones behind "and N
 * earlier versions". A release with a Security section gets a badge, and the block says in plain words that the update is
 * worth doing soon; without one it says nothing of the kind. The text is the GitHub release page's, unsigned: it says so.
 * Nothing renders without notes (the edition, a release the panel could not read, nothing behind).
 */
export function WhatsNew({ notes }: { notes: readonly ReleaseNote[] | undefined }) {
  const t = useT();
  const id = useId();
  const [all, setAll] = useState(false);
  if (!notes || notes.length === 0) return null;
  const [latest, ...older] = notes;
  const security = notes.some((n) => n.security);
  return (
    <section className="flex min-w-0 flex-col gap-2.5 rounded-field border border-line bg-surface p-3.5" aria-label={t("up.notes.title")}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <SectionLabel as="h3">{t("up.notes.title")}</SectionLabel>
        {security && <span className="text-xs font-semibold text-warn-text">{t("up.notes.securityHint")}</span>}
      </div>
      <Entry note={latest!} clamp />
      {older.length > 0 && (
        <div className="flex flex-col gap-3">
          <button
            type="button"
            aria-expanded={all}
            aria-controls={id}
            onClick={() => setAll(!all)}
            className="h-7 w-fit cursor-pointer rounded-ctl border border-line px-2.5 text-xs font-bold text-muted transition-colors duration-200 hover:bg-surface-2 hover:text-fg"
          >
            {all ? t("up.notes.hideEarlier") : t.n("up.notes.earlier", older.length)}
          </button>
          {all && (
            <div id={id} className="flex flex-col gap-4 border-t border-line pt-3">
              {older.map((n) => (
                <Entry key={n.version} note={n} />
              ))}
            </div>
          )}
        </div>
      )}
      <p className="text-[11px] text-faint">{t("up.notes.source")}</p>
    </section>
  );
}
