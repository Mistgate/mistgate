import { useId, useState } from "react";
import { Icon } from "@/components/ui/icons";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { securityNames } from "@/lib/os-fleet";
import type { Os } from "@/lib/os-updates";

// The names behind "Security: 2": which packages wait, and which of them the automatic updates will not install by themselves.
// A stuck one is a quiet amber chip with a clock, and the explanation says it is no failure: the next run of the security
// updates from the panel puts it in.

/** The button that opens the list; nothing at all when the node names no packages (an older agent, or none waiting). */
export function SecurityToggle({ status, open, onToggle, controls }: { status: Os | undefined; open: boolean; onToggle: () => void; controls: string }) {
  const t = useT();
  const { names } = securityNames(status);
  if (names.length === 0) return null;
  return (
    <button
      type="button"
      aria-expanded={open}
      aria-controls={controls}
      onClick={onToggle}
      className="-mx-1 flex min-h-7 items-center gap-1 self-start rounded-ctl px-1 text-xs font-bold text-accent-text transition-colors duration-200 hover:bg-surface-2"
    >
      {t("osf.sec.toggle", { n: status?.security ?? names.length })}
      <Icon name="chevronRight" size={12} className={cx("block flex-none transition-transform duration-200", open ? "-rotate-90" : "rotate-90")} />
    </button>
  );
}

/** The names, with the stuck ones marked and the calm note under them. */
export function SecurityList({ status, node, id }: { status: Os | undefined; node: string; id: string }) {
  const t = useT();
  const { names, stuck, more } = securityNames(status);
  if (names.length === 0) return null;
  const anyStuck = names.some((n) => stuck.has(n));
  return (
    <div id={id} role="group" aria-label={t("osf.sec.aria", { node })} className="flex min-w-0 flex-col gap-2 rounded-field border border-line bg-surface-2 px-3 py-2.5">
      <ul className="flex flex-wrap gap-1.5">
        {names.map((n) => (
          <li
            key={n}
            dir="ltr"
            title={stuck.has(n) ? t("osf.sec.stuck") : undefined}
            className={cx(
              "inline-flex max-w-full min-w-0 items-center gap-1.5 rounded-ctl border px-2 py-0.5 font-mono text-xs",
              stuck.has(n) ? "border-warn-line bg-warn-soft text-warn-text" : "border-line bg-surface text-fg",
            )}
          >
            {stuck.has(n) && <Icon name="clock" size={11} className="flex-none" aria-hidden />}
            <span className="truncate">{n}</span>
          </li>
        ))}
        {more > 0 && <li className="inline-flex items-center px-1 text-xs text-muted">{t("osf.sec.more", { n: more })}</li>}
      </ul>
      {anyStuck && (
        <p className="flex items-start gap-2 text-[11px] leading-snug text-pretty text-muted">
          <Icon name="clock" size={12} className="mt-px flex-none" aria-hidden />
          <span>
            <b className="font-bold text-fg">{t("osf.sec.stuck")}.</b> {t("osf.sec.note")}
          </span>
        </p>
      )}
    </div>
  );
}

/** Toggle and list in one place, for the node's card where there is room under the count. */
export function SecurityNames({ status, node, defaultOpen }: { status: Os | undefined; node: string; /** The dev kit shows the list open. */ defaultOpen?: boolean }) {
  const [open, setOpen] = useState(!!defaultOpen);
  const id = useId();
  if (securityNames(status).names.length === 0) return null;
  return (
    <div className="mt-0.5 flex flex-col gap-1.5">
      <SecurityToggle status={status} open={open} onToggle={() => setOpen(!open)} controls={id} />
      {open && <SecurityList status={status} node={node} id={id} />}
    </div>
  );
}
