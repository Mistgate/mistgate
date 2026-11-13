import { useState } from "react";
import { Icon } from "@/components/ui/icons";
import { StatusGlyph, kindTextClass } from "@/components/ui/status";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { kernelMarkIds, kernelMarks, kernelVerdict, type KernelRow, type KernelVerdict } from "@/lib/os-kernels";
import type { OsRow } from "@/lib/os-updates";

// The kernels of a node: one row per installed kernel (newest first) with five checks as small status marks, and under the title
// one line that says whether the next reboot would go through. What the agent's reader sees; the job decides at the reboot.
// An agent that predates the table gets one quiet line. In the Updates table (`onlyBlocked`) the block appears only when a reboot
// would be refused, so a healthy fleet is not longer for it; the node's card always has it.

// Version, then the five marks: on a phone the labels sit under the marks, from `sm` they are one header row.
const cols = "grid-cols-5 gap-x-1 sm:grid-cols-[minmax(0,1fr)_repeat(5,52px)] sm:items-center";

function Verdict({ v }: { v: KernelVerdict }) {
  const t = useT();
  if (v.kind === "ok") {
    return (
      <span className="inline-flex min-w-0 items-center gap-1.5 text-xs font-semibold text-muted">
        <StatusGlyph kind="ok" />
        {t("up.os.kern.verdict.ok")}
      </span>
    );
  }
  if (v.kind !== "blocked") return null;
  return (
    <span role="status" className={cx("inline-flex min-w-0 items-start gap-1.5 text-xs font-semibold text-pretty", kindTextClass.bad)}>
      <span className="mt-px flex-none">
        <StatusGlyph kind="bad" />
      </span>
      <span className="min-w-0">
        {t("up.os.kern.refused", { why: t(v.why, { kernel: v.kernel }) })}
        {v.more > 0 && <span className="ms-1.5 font-medium text-muted">{t("up.os.kern.more", { n: v.more })}</span>}
      </span>
    </span>
  );
}

function Row({ k }: { k: KernelRow }) {
  const t = useT();
  const marks = kernelMarks(k);
  const tags = [k.running && t("up.os.kern.running"), k.nextBoot && t("up.os.kern.next")].filter(Boolean).join(" · ");
  return (
    <li className={cx("grid gap-y-1.5 py-1.5", cols)}>
      <div className="col-span-5 flex min-w-0 flex-col sm:col-span-1">
        <span dir="ltr" className="truncate font-mono text-xs font-bold" title={k.version}>
          {k.version}
        </span>
        {tags && <span className={cx("text-[11px]", k.nextBoot ? "font-semibold text-accent-text" : "text-muted")}>{tags}</span>}
      </div>
      {marks.map((m) => {
        const label = t(`up.os.kern.col.${m.id}` as MessageKey);
        const title = `${label}: ${t(`up.os.kern.state.${m.state}` as MessageKey)}`;
        return (
          <div key={m.id} className="flex flex-col items-center gap-1 sm:block sm:justify-self-center">
            <span role="img" aria-label={title} title={title} className="block">
              <StatusGlyph kind={m.kind} />
            </span>
            <span aria-hidden className="max-w-full truncate text-[10px] leading-none text-muted sm:hidden">
              {label}
            </span>
          </div>
        );
      })}
    </li>
  );
}

/** The kernel table of a node's row. */
export function OsKernels({ row, onlyBlocked = false }: { row: OsRow; onlyBlocked?: boolean }) {
  const t = useT();
  const s = row.status;
  const v = kernelVerdict(s);
  const blocked = v.kind === "blocked";
  const [open, setOpen] = useState(blocked);
  if (!row.nodeId || v.kind === "none") return null;
  if (v.kind === "old") return onlyBlocked ? null : <p className="text-[11px] leading-snug text-pretty text-muted">{t("up.os.kern.old")}</p>;
  if (onlyBlocked && !blocked) return null;
  const rows = s?.kernels ?? [];
  return (
    <div data-shot="os-kernels" className="flex min-w-0 flex-col rounded-field border border-line bg-surface-2">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="flex min-h-9 w-full min-w-0 items-start gap-2 rounded-field px-3 py-2 text-start transition-colors duration-200 hover:bg-surface-3 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
      >
        <span className="mt-0.5 flex-none text-muted">
          <Icon name={open ? "chevronDown" : "chevronRight"} size={12} />
        </span>
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <b className="text-[13px]">{t("up.os.kern.title")}</b>
          <Verdict v={v} />
        </span>
      </button>
      {open && (
        <div className="flex flex-col border-t border-line px-3 pb-1.5 pt-2">
          <div aria-hidden className={cx("hidden pb-1 text-[10px] font-semibold text-muted sm:grid", cols)}>
            <span />
            {kernelMarkIds.map((id) => (
              <span key={id} className="truncate text-center">
                {t(`up.os.kern.col.${id}` as MessageKey)}
              </span>
            ))}
          </div>
          <ul className="flex flex-col divide-y divide-line">
            {rows.map((k) => (
              <Row key={k.version} k={k} />
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
