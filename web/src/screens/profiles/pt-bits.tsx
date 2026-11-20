import type { ReactNode } from "react";
import { cx } from "@/lib/cx";
import { useT } from "@/i18n";
import { G } from "@/screens/users/user-bits";
import type { PtGroup, PtKind } from "./put-check";

// The pieces of the board's "Put on nodes" and "Put a profile on a node" windows (pn.css .pt-*): the bar with its filters, the head of a
// section ("Can be put 3"), and the line under a row that says why, with the mark of what kind of line it is. The rows themselves are
// the windows' own (a node with its country, a profile with its port).

/** The mark of a line: a triangle (a remark), a clock (out of touch), a pencil (an action to take), an arrow (update), a crossed circle (no). */
const MARK: Record<PtKind | "clock", string> = {
  ok: "",
  warn: "M12 4.5 3.5 19h17zM12 10v4.5M12 16.8v.01",
  clock: "M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17zM12 7.5V12l3 2",
  act: "M4 20h4L19 9l-4-4L4 16zM13.5 6.5l4 4",
  go: "M12 19V6M6.5 11.5 12 6l5.5 5.5",
  no: "M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17zM6 6l12 12",
};

/** The line under a row: what the check said, in the colour of its kind (an `ok` line has no mark). */
export function PtWhy({ kind, clock, children }: { kind: PtKind; clock?: boolean; children: ReactNode }) {
  const d = MARK[clock ? "clock" : kind];
  return (
    <p className={cx("pt-why", kind)}>
      {d && <G d={d} size={16} stroke={1.5} />}
      <span>{children}</span>
    </p>
  );
}

/** The head of a section of the list: its name, how many rows it has, a word about it, and what goes at the end (a link). */
export function PtHead({ title, count, hint, children }: { title: string; count: number; hint?: string; children?: ReactNode }) {
  return (
    <div className="pt-gh">
      <b>{title}</b>
      <span className="mono">{count}</span>
      {hint && <em>{hint}</em>}
      {children}
    </div>
  );
}

export type PtTab = "all" | PtGroup;

/** The bar over the list: what to show (with the counts), and a search when the list is long. It stays at the top while the list scrolls. */
export function PtBar({ counts, value, onValue, query, onQuery, nodes }: { counts: Record<PtTab, number>; value: PtTab; onValue: (v: PtTab) => void; query: string; onQuery: (q: string) => void; nodes?: boolean }) {
  const t = useT();
  const tabs: { id: PtTab; label: string }[] = [
    { id: "all", label: t("ps.pt.fAll") },
    { id: "can", label: t("ps.pt.fCan") },
    { id: "act", label: t("ps.pt.fAct") },
    { id: "no", label: t("ps.pt.fNo") },
  ];
  return (
    <div className="pt-bar">
      <div className="pt-fl" role="group" aria-label={t("ps.pt.filter")}>
        {tabs.map((x) => (
          <button key={x.id} type="button" className={value === x.id ? "on" : ""} aria-pressed={value === x.id} onClick={() => onValue(x.id)}>
            {x.label}
            <i>{counts[x.id]}</i>
          </button>
        ))}
      </div>
      {nodes && <input className="an-i pt-q" type="search" placeholder={t("ps.pt.find")} aria-label={t("ps.pt.find")} value={query} onChange={(e) => onQuery(e.target.value.trim())} />}
    </div>
  );
}
