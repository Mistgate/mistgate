import { useSuspenseQuery } from "@tanstack/react-query";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { meQuery } from "@/lib/session";

// Small pieces of the shell that several of its parts use (the menu, the top bar, the phone's menu).

export const roleKey: Record<Role, MessageKey> = {
  [Role.UNSPECIFIED]: "role.unknown",
  [Role.OWNER]: "role.owner",
  [Role.HELPER]: "role.helper",
  [Role.READONLY]: "role.readonly",
};

const isMac = /Mac|iPhone|iPad/.test(navigator.platform);
export const paletteKeys = isMac ? ["⌘", "K"] : ["Ctrl", "K"];

/** A star count as GitHub shows it: 823, 1.2k, 3.4M (rounded down, never more than there is). */
export function compactStars(n: number): string {
  const one = (x: number) => (Math.floor(x * 10) / 10).toFixed(1).replace(/\.0$/, "");
  return n < 1000 ? String(n) : n < 1e6 ? `${one(n / 1e3)}k` : `${one(n / 1e6)}M`;
}

/**
 * The panel's version and, when the panel names one (serve --source-url), the link to its source code (AGPL-3.0
 * section 13). Admin only: the decoy site and the user page never show it.
 */
export function VersionLine({ className }: { className?: string }) {
  const t = useT();
  const { data } = useSuspenseQuery(meQuery);
  return (
    <p className={cx("font-mono text-[11px] text-muted", className)}>
      {t("header.version", { version: data.version })}
      {data.sourceUrl && (
        <>
          {" · "}
          <a href={data.sourceUrl} target="_blank" rel="noopener noreferrer" className="underline-offset-2 transition-colors hover:text-fg hover:underline">
            {t("header.source")}
          </a>
        </>
      )}
    </p>
  );
}

/**
 * The panel's source repository as a 40px button of the top bar: the GitHub mark, the word, and after a hairline an outline
 * star and the count. The link is the one of the version line (serve --source-url) and the button is gone without it. The
 * count is read by the panel (Me.source_stars), never by the browser; without one (not read yet, not a GitHub repository, the
 * edge) the mark and the word stand alone. The word goes below 1200px, the mark below 900px (the count then stands alone:
 * the count's own line goes too); `full` (the phone's menu) keeps all of it.
 */
export function GitHubButton({ full, className }: { full?: boolean; className?: string }) {
  const t = useT();
  const { data } = useSuspenseQuery(meQuery);
  if (!data.sourceUrl) return null;
  const stars = data.sourceStars;
  const label = t("header.star");
  return (
    <a
      href={data.sourceUrl}
      target="_blank"
      rel="noopener noreferrer"
      aria-label={stars === undefined ? label : `${label}: ${stars}`}
      className={cx(
        "inline-flex h-10 flex-none items-center gap-2 rounded-field border border-line bg-step px-3 text-sm font-medium whitespace-nowrap text-fg outline-none transition-[background-color,transform] duration-150 ease-out hover:bg-(--step-hover) focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent-mark active:scale-[0.98]",
        full && "flex-1",
        className,
      )}
    >
      <svg aria-hidden width="16" height="16" viewBox="0 0 16 16" fill="currentColor" className={cx("flex-none text-sec", !full && stars !== undefined && "max-[900px]:hidden")}>
        <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z" />
      </svg>
      <span className={cx(!full && "max-mid:hidden")}>GitHub</span>
      {stars !== undefined && (
        <span className={cx("inline-flex items-center gap-1 border-s border-line ps-2 font-mono text-[13px] font-normal text-sec tabular-nums", !full && "max-[900px]:border-s-0 max-[900px]:ps-0", full && "ms-auto")}>
          <svg aria-hidden width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" strokeLinecap="round" className="flex-none">
            <path d="m12 4 2.5 5 5.5.8-4 3.9.9 5.5-4.9-2.6-4.9 2.6.9-5.5-4-3.9 5.5-.8z" />
          </svg>
          {compactStars(stars)}
        </span>
      )}
    </a>
  );
}
