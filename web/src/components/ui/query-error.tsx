import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { Button } from "./button";
import { Notice } from "./notice";

/**
 * A failed load, the same on every screen: the reason in words (errorText, never "no network" for a refusal) and
 * "Try again". `compact` is the line inside a card: a red dot, the reason and the button on one row.
 */
export function QueryError({ error, onRetry, compact, className }: { error: unknown; onRetry?: () => void; compact?: boolean; className?: string }) {
  const t = useT();
  const retry = onRetry && (
    <Button variant="secondary" size="sm" onClick={onRetry}>
      {t("common.retry")}
    </Button>
  );
  if (compact)
    return (
      <div role="alert" className={cx("flex flex-wrap items-center gap-x-3 gap-y-2 text-[13px]", className)}>
        <span className="flex min-w-0 flex-1 basis-48 items-start gap-2 leading-snug text-danger-text">
          <span aria-hidden className="mt-[5px] size-2 flex-none rounded-full bg-danger" />
          {errorText(error, t)}
        </span>
        {retry}
      </div>
    );
  return (
    <div className={cx("flex flex-col items-start gap-3", className)}>
      <Notice tone="danger">{errorText(error, t)}</Notice>
      {retry}
    </div>
  );
}

/** "Loading…" while a query is on its way: a screen's own line, or `compact` inside a card. */
export function Pending({ compact, className }: { compact?: boolean; className?: string }) {
  const t = useT();
  return <p className={cx(compact ? "text-[13px] text-muted" : "px-1 py-8 text-muted", className)}>{t("common.loading")}</p>;
}
