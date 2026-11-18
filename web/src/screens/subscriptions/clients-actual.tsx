import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { Tip } from "@/components/ui/tooltip";
import { cx } from "@/lib/cx";
import { actualTip, actualWord, ClientCompatActual, helloLine, mismatchText, windowDays, type CompatDevice, type CompatDevices } from "@/lib/clientcompat";
import { useTx, type Tx } from "@/screens/users/t";
import { Glyph } from "./parts";

// "In fact" of the App versions tab: what the nodes really saw of a device's ClientHellos (client_compat.proto, WHAT THE NODES SAW)
// next to the verdict of the table of minimum versions. The table is what the panel knows about the app; this is what happened.

const classes = [ClientCompatActual.PQ, ClientCompatActual.CLASSIC, ClientCompatActual.MIXED, ClientCompatActual.NONE] as const;

/** Devices of a measured class over all devices that receive REALITY (the counts do not follow the filters). */
const actualCount = (data: CompatDevices, a: ClientCompatActual) => data.actualCounts.find((c) => c.actual === a)?.devices ?? 0;

/**
 * The classes as chips with their device counts: the filter by what the nodes saw (the board's row "By what the nodes saw for 7 days").
 * One is chosen at a time (a second tap goes back to "All"); below it, in a few quiet lines, the devices where the table and the
 * nodes disagree and the nodes that do not count yet.
 */
export function ActualBar({ data, actual, onPick }: { data: CompatDevices; actual: ClientCompatActual; onPick: (a: ClientCompatActual) => void }) {
  const t = useTx();
  const days = windowDays(data.helloWindowHours);
  const total = classes.reduce((n, a) => n + actualCount(data, a), 0);
  const nodes = data.helloKexNodesWithout;
  return (
    <>
      <div className="sb-vf">
        <span className="sb-vfl">
          {t("cc.act.label")} {t("cc.act.sub", { days })}
        </span>
        <RadioGroup aria-label={t("cc.act.label")} value={String(actual)} onValueChange={(v) => onPick(Number(v) as ClientCompatActual)} className="sb-ch">
          <Radio.Root value={String(ClientCompatActual.UNSPECIFIED)}>
            {t(actualWord[ClientCompatActual.UNSPECIFIED])}
            <span className="mono">{total}</span>
          </Radio.Root>
          {classes.map((a) => (
            <Radio.Root key={a} value={String(a)} onClick={() => actual === a && onPick(ClientCompatActual.UNSPECIFIED)}>
              {t(actualWord[a])}
              <span className="mono">{actualCount(data, a)}</span>
            </Radio.Root>
          ))}
        </RadioGroup>
      </div>
      {data.mismatchWorse > 0 && <Note kind="part" text={t.n("cc.mis.noteWorse", data.mismatchWorse)} />}
      {data.mismatchBetter > 0 && <Note kind="ok" text={t.n("cc.mis.noteBetter", data.mismatchBetter)} />}
      {nodes.length > 0 && <Note kind="part" text={`${t.n("cc.nodesWithout", nodes.length)} (${nodes.join(", ")})`} />}
    </>
  );
}

function Note({ kind, text }: { kind: "ok" | "part"; text: string }) {
  return (
    <p className="sb-note">
      <Glyph kind={kind} />
      {text}
    </p>
  );
}

/** The mark next to the word where the table and the nodes disagree: "≠", warm when the table is too optimistic, calm when it is too careful. */
function MismatchMark({ d, t }: { d: CompatDevice; t: Tx }) {
  const text = mismatchText(t, d);
  if (!text) return null;
  return (
    <Tip label={text}>
      <span tabIndex={0} role="img" aria-label={`${t("cc.mis.label")}: ${text}`} data-mismatch={d.tableMismatch} className={cx("sb-ne", d.tableMismatch === "worse" ? "part" : "ok")}>
        ≠
      </span>
    </Tip>
  );
}

/** What the nodes saw: the word (warm when old-style), the counts under it, and the mark of a disagreement with the table. */
export function ActualCell({ d, windowHours, t }: { d: CompatDevice; windowHours: number; t: Tx }) {
  const line = helloLine(t, d);
  const worse = d.actual === ClientCompatActual.CLASSIC || d.actual === ClientCompatActual.MIXED;
  return (
    <span className="sb-v2">
      <Tip label={actualTip(t, d, windowHours)}>
        <b tabIndex={0} className={cx(worse && "warn")}>
          {t(actualWord[d.actual])}
          <MismatchMark d={d} t={t} />
        </b>
      </Tip>
      {line && <em className="mono">{line}</em>}
    </span>
  );
}
