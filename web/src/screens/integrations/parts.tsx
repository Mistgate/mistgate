import { CopyButton } from "@/components/copy-button";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { profileInfo } from "@/lib/integrations";
import type { TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";

/** The card every section of the screen is drawn in (same as the Updates cards). */
export const card = "flex min-w-0 flex-col gap-3 rounded-card-lg border border-line bg-surface p-4 md:p-[18px]";

/** What a token may do, as a 22px chip: grey for reads, accent for day-to-day work, red for admin. */
export function ProfileChip({ profile }: { profile: TokenProfile }) {
  const t = useT();
  const p = profileInfo(profile);
  return <span className={cx("inline-flex h-[22px] flex-none items-center rounded-[7px] px-2 font-mono text-[10px] font-bold whitespace-nowrap", p.cls)}>{t(p.key)}</span>;
}

/** A snippet to paste somewhere: a small header with the language and a copy button, then the text (scrolls sideways, never wraps). */
export function CodeBlock({ text, lang }: { text: string; lang: string }) {
  return (
    <div className="overflow-hidden rounded-field border border-line bg-canvas">
      <div className="flex items-center justify-between gap-2 border-b border-line py-1.5 pr-1.5 pl-3">
        <span className="font-mono text-[11px] text-muted">{lang}</span>
        <CopyButton value={text} />
      </div>
      <pre className="overflow-x-auto p-3 font-mono text-xs leading-relaxed whitespace-pre text-fg select-all">{text}</pre>
    </div>
  );
}
