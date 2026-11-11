import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { Icon } from "@/components/ui/icons";
import { StatusPill } from "@/components/ui/status";
import { Tip } from "@/components/ui/tooltip";
import { cx } from "@/lib/cx";
import { actualKind, actualTip, actualWord, ClientCompatActual, helloLine, mismatchText, windowDays, type CompatDevice, type CompatDevices } from "@/lib/clientcompat";
import { useTx, type Tx } from "@/screens/users/t";

// "In fact" of the App versions tab: what the nodes really saw of a device's ClientHellos (client_compat.proto, WHAT THE NODES SAW)
// next to the verdict of the table of minimum versions. The table is what the panel knows about the app; this is what happened.

const classes = [ClientCompatActual.PQ, ClientCompatActual.CLASSIC, ClientCompatActual.MIXED, ClientCompatActual.NONE] as const;

/** Devices of a measured class over all devices that receive REALITY (the counts do not follow the filters). */
const actualCount = (data: CompatDevices, a: ClientCompatActual) => data.actualCounts.find((c) => c.actual === a)?.devices ?? 0;

/**
 * The classes as chips with their device counts: the filter by what the nodes saw. One is chosen at a time (a second tap
 * goes back to "All"); below it, in a few quiet lines, the devices where the table and the nodes disagree and the nodes that
 * do not count yet.
 */
export function ActualBar({ data, actual, onPick }: { data: CompatDevices; actual: ClientCompatActual; onPick: (a: ClientCompatActual) => void }) {
  const t = useTx();
  const days = windowDays(data.helloWindowHours);
  const total = classes.reduce((n, a) => n + actualCount(data, a), 0);
  const chip = "group flex h-[34px] min-w-0 cursor-pointer items-center gap-2 rounded-ctl border border-line bg-surface ps-1.5 pe-2.5 text-start transition-colors duration-200 hover:bg-surface-2 data-checked:border-accent-line data-checked:bg-accent-soft";
  const nodes = data.helloKexNodesWithout;
  return (
    <section className="flex flex-col gap-2.5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-2">
          <h3 className="text-xs font-bold">{t("cc.act.label")}</h3>
          <span className="text-[11px] text-muted">{t("cc.act.sub", { days })}</span>
        </div>
        <RadioGroup aria-label={t("cc.act.label")} value={String(actual)} onValueChange={(v) => onPick(Number(v) as ClientCompatActual)} className="flex flex-wrap gap-2">
          <Radio.Root value={String(ClientCompatActual.UNSPECIFIED)} className={cx(chip, "ps-2.5")}>
            <span className="text-xs font-bold">{t(actualWord[ClientCompatActual.UNSPECIFIED])}</span>
            <span className="text-[15px] leading-none font-extrabold tabular-nums">{total}</span>
          </Radio.Root>
          {classes.map((a) => {
            const n = actualCount(data, a);
            return (
              <Radio.Root key={a} value={String(a)} onClick={() => actual === a && onPick(ClientCompatActual.UNSPECIFIED)} className={chip}>
                <StatusPill sm kind={actualKind(a)} label={t(actualWord[a])} />
                <span className={cx("text-[15px] leading-none font-extrabold tabular-nums", n === 0 && "text-faint")}>{n}</span>
              </Radio.Root>
            );
          })}
        </RadioGroup>
      </div>
      {(data.mismatchWorse > 0 || data.mismatchBetter > 0 || nodes.length > 0) && (
        <ul className="flex flex-col gap-1">
          {data.mismatchWorse > 0 && <Note tone="warn" text={t.n("cc.mis.noteWorse", data.mismatchWorse)} />}
          {data.mismatchBetter > 0 && <Note tone="info" text={t.n("cc.mis.noteBetter", data.mismatchBetter)} />}
          {nodes.length > 0 && <Note tone="warn" text={`${t.n("cc.nodesWithout", nodes.length)} (${nodes.join(", ")})`} />}
        </ul>
      )}
    </section>
  );
}

function Note({ tone, text }: { tone: "warn" | "info"; text: string }) {
  return (
    <li className="flex items-start gap-1.5 text-xs leading-snug text-muted">
      <Icon name={tone === "warn" ? "warn" : "info"} size={13} className={cx("mt-px flex-none", tone === "warn" && "text-warn-text")} aria-hidden />
      <span className="min-w-0 text-pretty">{text}</span>
    </li>
  );
}

/** The mark next to the pill where the table and the nodes disagree: "≠", warm when the table is too optimistic, calm when it is too careful. */
function MismatchMark({ d, t }: { d: CompatDevice; t: Tx }) {
  const text = mismatchText(t, d);
  if (!text) return null;
  return (
    <Tip label={text}>
      <span
        tabIndex={0}
        role="img"
        aria-label={`${t("cc.mis.label")}: ${text}`}
        data-mismatch={d.tableMismatch}
        className={cx(
          d.tableMismatch === "worse" ? "tone-warn" : "tone-ok",
          "pill inline-flex size-[18px] flex-none items-center justify-center rounded-full text-xs leading-none font-bold",
        )}
      >
        ≠
      </span>
    </Tip>
  );
}

/** The pill of what the nodes saw (with its tooltip), the mismatch mark and, under them, the counts. */
export function ActualCell({ d, windowHours, t }: { d: CompatDevice; windowHours: number; t: Tx }) {
  const line = helloLine(t, d);
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <span className="flex items-center gap-1.5">
        <Tip label={actualTip(t, d, windowHours)}>
          <span tabIndex={0} className="inline-flex min-w-0">
            <StatusPill sm kind={actualKind(d.actual)} label={t(actualWord[d.actual])} />
          </span>
        </Tip>
        <MismatchMark d={d} t={t} />
      </span>
      {line && <span className="truncate text-[11px] text-muted">{line}</span>}
    </div>
  );
}

/** The same in a card: the pill and the counts in a line, the sentence about a disagreement under it. */
export function ActualCard({ d, windowHours, t }: { d: CompatDevice; windowHours: number; t: Tx }) {
  const line = helloLine(t, d);
  const mismatch = mismatchText(t, d);
  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="text-[11px] text-faint">{t("cc.col.actual")}:</span>
        <Tip label={actualTip(t, d, windowHours)}>
          <span tabIndex={0} className="inline-flex">
            <StatusPill sm kind={actualKind(d.actual)} label={t(actualWord[d.actual])} />
          </span>
        </Tip>
        {line && <span className="text-[11px] text-muted">{line}</span>}
      </div>
      {mismatch && (
        <p className={cx("flex items-start gap-1.5 text-xs leading-snug text-pretty", d.tableMismatch === "worse" ? "text-warn-text" : "text-muted")} data-mismatch={d.tableMismatch}>
          <span aria-hidden className="font-bold">
            ≠
          </span>
          {mismatch}
        </p>
      )}
    </div>
  );
}
