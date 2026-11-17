import { useId, useState } from "react";
import { parseMarkdown, renderInline, safeHttpsUrl, type Inline } from "@/components/mini-markdown";
import { useLang, useT, type Lang } from "@/i18n";
import { asChannel } from "@/lib/channels";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import type { Updates } from "@/lib/updates";
import { ChannelChip } from "./channel";
import { CardHead, D, Gl } from "./bits";

// "What's new" in the board's shape: a card with a dotted line of releases (the newest on top), each one a list of what it
// brings, a word in front of every line ("New", "Fixed", "Security"). The text is the GitHub release page's, unsigned: it says so.

export type ReleaseNote = Updates["agentNotes"][number];

/**
 * The notes in the interface language; a release without them in it (releases carry English, Russian and Chinese) falls
 * back to English, then to whatever it has.
 */
export const noteText = (n: Pick<ReleaseNote, "en" | "ru" | "zh">, lang: Lang): string =>
  (lang === "en" || lang === "ru" || lang === "zh" ? n[lang] : "") || n.en || n.ru || n.zh;

export type RelTag = "new" | "fix" | "sec" | "";
export type RelItem = { tag: RelTag; label: string; body: Inline[]; sub: Inline[][] };

const tagOf = (heading: string): RelTag => {
  const h = heading.toLowerCase();
  if (/secur|безопас|安全|güvenlik|امنیت/.test(h)) return "sec";
  if (/fix|исправ|修复|düzelt|اصلاح|رفع/.test(h)) return "fix";
  if (/feature|new|возможност|нов|新功能|新增|özellik|yeni|ویژگی|تازه/.test(h)) return "new";
  return "";
};

const plainOf = (nodes: Inline[]): string => nodes.map((n) => ("text" in n ? n.text : plainOf(n.children))).join("");

/**
 * What a release says, as the lines of the board: every bullet is a line, with the word of the section it stands in
 * (a "## Fixes" heading gives "Fixed"); a nested bullet stays under its line; a paragraph is a line of its own. A text with no
 * section at all gives lines with no word.
 */
export function releaseItems(text: string): RelItem[] {
  const out: RelItem[] = [];
  let tag: RelTag = "";
  let label = "";
  for (const b of parseMarkdown(text)) {
    if (b.kind === "heading") {
      label = plainOf(b.children);
      tag = tagOf(label);
    } else if (b.kind === "paragraph") out.push({ tag, label, body: b.children, sub: [] });
    else
      for (const it of b.items) {
        if (it.depth === 1 && out.length > 0 && out[out.length - 1]!.body.length > 0) out[out.length - 1]!.sub.push(it.children);
        else out.push({ tag, label, body: it.children, sub: [] });
      }
  }
  return out;
}

function Entry({ note, top }: { note: ReleaseNote; top: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const lang = useLang();
  const items = releaseItems(noteText(note, lang));
  const href = safeHttpsUrl(note.url);
  const word = (i: RelItem) => (i.tag === "new" ? t("uq.rel.new") : i.tag === "fix" ? t("uq.rel.fix") : i.tag === "sec" ? t("uq.rel.secTag") : i.label);
  return (
    <article className={cx("uq-rel", top && "top")} aria-label={note.version}>
      <div className="uq-rh">
        <i className="uq-rd" aria-hidden />
        {href ? (
          <a href={href} target="_blank" rel="noopener noreferrer" title={t("up.notes.open", { version: note.version })}>
            <b className="mono">{note.version}</b>
          </a>
        ) : (
          <b className="mono">{note.version}</b>
        )}
        {note.publishedUnix > 0 && <em>{fmt.date(note.publishedUnix)}</em>}
        {asChannel(note.channel) !== "stable" && <ChannelChip channel={asChannel(note.channel)} />}
      </div>
      <ul>
        {items.length === 0 && (
          <li>
            <span className="uq-tg" />
            <span>{t("up.notes.noText")}</span>
          </li>
        )}
        {items.map((it, i) => (
          <li key={i} dir="auto">
            <span className={cx("uq-tg", it.tag)}>{word(it)}</span>
            <span>
              {renderInline(it.body)}
              {it.sub.map((s, j) => (
                <span key={j} className="block ps-3">
                  – {renderInline(s)}
                </span>
              ))}
            </span>
          </li>
        ))}
      </ul>
    </article>
  );
}

/**
 * The card of releases: the newest in full, the earlier ones behind "and N earlier versions". A Security section gets its own
 * line that says the update is worth doing soon; without one nothing of the kind is said. Nothing renders without notes (the
 * edition, a release the panel could not read, nothing behind).
 */
export function ReleaseCard({ notes, done }: { notes: readonly ReleaseNote[] | undefined; done?: boolean }) {
  const t = useT();
  const id = useId();
  const [all, setAll] = useState(false);
  if (!notes || notes.length === 0) return null;
  const shown = all ? notes : notes.slice(0, 1);
  const older = notes.length - 1;
  const security = !done && notes.some((n) => n.security);
  return (
    <section className="uq-card pp-sec" aria-label={t("up.notes.title")}>
      <CardHead icon={D.spark} tone="lav" title={done ? t("uq.rel.titleDone") : t("up.notes.title")} />
      {security && (
        <p className="rc-nt part uq-secn">
          <Gl k="part" />
          <span>{t("uq.rel.sec")}</span>
        </p>
      )}
      <div className="uq-rels" id={id}>
        {shown.map((n, i) => (
          <Entry key={n.version} note={n} top={i === 0} />
        ))}
      </div>
      {older > 0 && (
        <button type="button" className={cx("an-lk", "uq-more")} aria-expanded={all} aria-controls={id} onClick={() => setAll(!all)}>
          {all ? t("up.notes.hideEarlier") : t.n("up.notes.earlier", older)}
        </button>
      )}
      <p className="sb-vfoot">{t("uq.rel.src")}</p>
    </section>
  );
}
