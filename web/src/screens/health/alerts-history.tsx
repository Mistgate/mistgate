import { useId, useMemo, useState } from "react";
import { Button } from "@/components/ui/button";
import { Tip } from "@/components/ui/tooltip";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { dayLabel, useFmt } from "@/lib/format";
import { alertTitle, groupHistory, groupSeverity, groupTitle, resolutionWord, severityWord, type Alert } from "@/lib/health";
import { Ipv6Tag, pillOf, targeted } from "./alert-bits";
import { Dot, Gl, P, Sec } from "./page-parts";
import "./health.css";

// The closed alerts of the window: the board's "History · 7 days" (.hl-hist > .hl-day > .hl-hr): a line a day, a row an event with its time, its
// verdict, its nodes, what it was and how long, and the product's own: how it ended, the episodes of an event on several nodes under it, "Show N more".

const STEP = 20;
const historyId = (h: Alert) => `alert-${h.id}-${h.resolvedAtUnix}`;

/** How an episode ended, as a quiet glyph with its word in the tooltip; "fixed" in the colour of what works. */
const endings: Record<string, { d: string; ok?: boolean }> = {
  cleared: { d: "M20 12a8 8 0 1 1-2.3-5.6M20 4v4h-4" },
  node_returned: { d: "M20 12a8 8 0 1 1-2.3-5.6M20 4v4h-4" },
  fix_applied: { d: P.check, ok: true },
  dismissed: { d: "M6 6l12 12M18 6L6 18" },
  accepted: { d: P.check },
  superseded: { d: "M12 3l9 5-9 5-9-5zM3 13l9 5 9-5" },
  node_retired: { d: "M6 6l12 12M18 6L6 18" },
};

function Ending({ resolution }: { resolution: string }) {
  const t = useT();
  const word = t(resolutionWord(resolution));
  const e = endings[resolution] ?? { d: "M12 8v5M12 16.5v.01" };
  return (
    <Tip label={word}>
      <span tabIndex={0} role="img" aria-label={word} className={cx("hl-end", e.ok && "ok")}>
        <Gl d={e.d} />
      </span>
    </Tip>
  );
}

/** The nodes of a row as marks, the first four and "+N". */
function Who({ names }: { names: string[] }) {
  const t = useT();
  return (
    <>
      {names.slice(0, 4).map((n) => (
        <span key={n} className="hl-who mono">
          {n}
        </span>
      ))}
      {names.length > 4 && <span className="hl-who mono">{t("hl.alerts.nodesMore", { n: names.length - 4 })}</span>}
    </>
  );
}

/**
 * One event: when it closed, the verdict, the nodes, what it was and how long it lasted, how it ended. The episodes of an event on several nodes
 * or ports open under it, each with its own anchor (a link from Reachability, /health#alert-<id>-<closed>).
 */
function Row({ g }: { g: Alert[] }) {
  const t = useT();
  const fmt = useFmt();
  const id = useId();
  const many = g.length > 1;
  const [open, setOpen] = useState(() => many && targeted(g.map(historyId)));
  const h = g[0]!;
  const sev = groupSeverity(g);
  const pill = pillOf(sev);
  const lasted = (x: Alert) => Math.max(0, x.resolvedAtUnix - (x.openedUnix || x.firstSeenUnix));
  const longest = Math.max(...g.map(lasted));
  // the episodes under the row name what differs: the port when the titles differ, else only the node
  const oneTitle = new Set(g.map((x) => alertTitle(t, x))).size === 1;
  const names = [...new Set(g.map((x) => x.nodeName).filter(Boolean))];
  const line = (
    <>
      <span className="mono">{fmt.clock(h.resolvedAtUnix)}</span>
      <span className={cx("pv-pill", pill.kind)}>
        <Dot kind={pill.glyph} />
        {t(severityWord(sev))}
      </span>
      <Who names={names} />
      <b>
        {groupTitle(t, g)}
        <Ipv6Tag alert={h} />
      </b>
      {longest > 0 && <em>{t("hl.alerts.lasted", { duration: fmt.duration(longest) })}</em>}
      {many && <Gl d={P.chev} className="sb-chev" />}
    </>
  );
  return (
    <li id={many ? undefined : historyId(h)}>
      <div className="hl-hr">
        {many ? (
          <button type="button" className="hl-hrb" aria-expanded={open} aria-controls={id} data-history-toggle onClick={() => setOpen(!open)}>
            {line}
          </button>
        ) : (
          <div className="hl-hrb">{line}</div>
        )}
        {[...new Set(g.map((x) => x.resolution))].map((r) => (
          <Ending key={r} resolution={r} />
        ))}
      </div>
      {many &&
        (open ? (
          <ul id={id} className="hl-hs">
            {g.map((x) => (
              <li key={historyId(x)} id={historyId(x)}>
                {x.nodeName && <span className="hl-who mono">{x.nodeName}</span>}
                {!oneTitle && <span className="min-w-0">{alertTitle(t, x)}</span>}
                <span className="what">{[lasted(x) > 0 && t("hl.alerts.lasted", { duration: fmt.duration(lasted(x)) }), t(resolutionWord(x.resolution))].filter(Boolean).join(" · ")}</span>
                <span className="when mono">{fmt.clock(x.resolvedAtUnix)}</span>
              </li>
            ))}
          </ul>
        ) : (
          // folded, every episode keeps its anchor for the links from Reachability
          g.map((x) => <span key={historyId(x)} id={historyId(x)} className="block scroll-mt-20" />)
        ))}
    </li>
  );
}

/**
 * The closed alerts of the window (at most 200), one row per event under its day, newest first: the latest 20, "Show N more" for the rest. A link to
 * a row further down shows enough to reach it.
 */
export function HistoryList({ rows }: { rows: Alert[] }) {
  const t = useT();
  const fmt = useFmt();
  const groups = useMemo(() => groupHistory(rows), [rows]);
  const [count, setCount] = useState(() => Math.max(STEP, groups.findIndex((g) => targeted(g.map(historyId))) + 1));
  const days: { label: string; groups: Alert[][] }[] = [];
  for (const g of groups.slice(0, count)) {
    const label = dayLabel(t, fmt, g[0]!.resolvedAtUnix);
    const day = days.at(-1);
    if (day?.label === label) day.groups.push(g);
    else days.push({ label, groups: [g] });
  }
  const more = Math.min(STEP, groups.length - count);
  return (
    <Sec tone="sand" icon={P.clock} title={t("hl.alerts.history")}>
      {rows.length === 0 && <p className="sb-emp hl-emp">{t("hl.alerts.historyNone")}</p>}
      <div className="hl-hist">
        {days.map((d) => (
          <div key={d.label} className="hl-day">
            <h4 className="hl-dl">{d.label}</h4>
            <ul className="hl-hl">
              {d.groups.map((g) => (
                <Row key={historyId(g[0]!)} g={g} />
              ))}
            </ul>
          </div>
        ))}
      </div>
      {more > 0 && (
        <div className="hl-more">
          <Button variant="secondary" size="md" onClick={() => setCount(count + STEP)}>
            {t("hl.alerts.more", { n: more })}
          </Button>
        </div>
      )}
    </Sec>
  );
}
