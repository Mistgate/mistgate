import type { CSSProperties, ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import type { StatusKind } from "@/components/ui/status";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useNodes } from "@/lib/queries";
import { G } from "@/screens/users/user-bits";
import "./updates.css";

// The small pieces of the Updates screen the board repeats (PnUpdates): the glyph of a state, the pill, the bar under a card's
// header, a node's flag and name, a card's head. Their classes are in updates.css.

/** The board's four states of a glyph: works (ok), half (part), crossed (bad), spinning (busy), empty ("" ). */
export type Gk = "" | "ok" | "part" | "bad" | "busy";

/** The board's word for a status of the product's own: amber is "part", the grey of "off" and "blip" is the empty one. */
export const gk = (kind: StatusKind): Gk => (kind === "ok" ? "ok" : kind === "warn" ? "part" : kind === "bad" ? "bad" : kind === "busy" ? "busy" : "");

export const Gl = ({ k }: { k: Gk }) => <i className={cx("rw-gl", k)} aria-hidden />;

/** The state pill: a glyph and a word. */
export function Pill({ k, children, className }: { k: Gk; children: ReactNode; className?: string }) {
  return (
    <span className={cx("pv-pill", k, className)}>
      <Gl k={k} />
      {children}
    </span>
  );
}

/** The board's 24-grid paths used here. */
export const D = {
  chev: "M9 6l6 6-6 6",
  down: "M7 10l5 5 5-5",
  arrow: "M5 12h14M13 6l6 6-6 6",
  check: "M5 12.5l4.5 4.5L19 7.5",
  x: "M6 6l12 12M18 6L6 18",
  warn: "M12 8v5M12 16.5v.01M10.3 4l-7.3 12.5a2 2 0 0 0 1.7 3h14.6a2 2 0 0 0 1.7-3L13.7 4a2 2 0 0 0-3.4 0z",
  spark: "M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z",
  server: "M4 5h16v6H4zM4 13h16v6H4zM8 8h.01M8 16h.01",
  chip: "M7 7h10v10H7zM10 3v4M14 3v4M10 17v4M14 17v4M3 10h4M3 14h4M17 10h4M17 14h4",
  key: "M15 7a4 4 0 1 1-3.9 5H8v3H5v-3H3v-2h8.1A4 4 0 0 1 15 7z",
  history: "M4 12a8 8 0 1 0 2.4-5.7M4 5v4h4M12 8v4l3 2",
  shield: "M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6zM9 12l2 2 4-4",
  flask: "M9 3h6M10 3v6l-5 9a2 2 0 0 0 1.8 3h10.4a2 2 0 0 0 1.8-3l-5-9V3M7.5 15h9",
  branch: "M7 5v9M7 14a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5zM17 5a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5zM17 10c0 4-7 2.5-9 5.5",
} as const;

/** A card's head: its glyph on its hue and its uppercase title; `children` at the end (a button, a pill). */
export function CardHead({ icon, tone, title, children }: { icon: string; tone: "sky" | "mint" | "sand" | "lav"; title: ReactNode; children?: ReactNode }) {
  return (
    <div className="pp-sh">
      <span className={cx("pp-ic", tone)} aria-hidden>
        <G d={icon} size={15} stroke={1.75} />
      </span>
      <h3>{title}</h3>
      {children}
    </div>
  );
}

/** The bar under a card's header: amber (a state that wants a look) or red (something failed); the buttons sit at its end. */
export function Bar({ bad, children, action }: { bad?: boolean; children: ReactNode; action?: ReactNode }) {
  return (
    <div className={cx("rc-pb", bad && "bad")} role={bad ? "alert" : "status"}>
      <Gl k={bad ? "bad" : "part"} />
      <div>{children}</div>
      {action}
    </div>
  );
}

/** A node's flag and name. The country comes from the node list; a node it does not know yet is drawn with no flag. */
export function NodeName({ name, countryCode, to }: { name: string; countryCode?: string; to?: ReactNode }) {
  return (
    <span className="hl-cnode">
      {countryCode ? <Flag code={countryCode} size={13} /> : <span aria-hidden className="inline-block h-[13px] w-[18px] flex-none rounded-[3px] border border-dashed border-line" />}
      {to ?? <b className="mono">{name}</b>}
    </span>
  );
}

/** Node id -> country code, from the node list (the shell already has it cached; empty until it arrives). */
export function useFlags(): ReadonlyMap<string, string> {
  const nodes = useNodes().data?.nodes;
  return new Map((nodes ?? []).map((n) => [n.id, n.countryCode]));
}

/** `--w` for a line that fills to a share ("35%"). */
export const w = (pct: number): CSSProperties => ({ ["--w" as string]: `${Math.max(0, Math.min(100, Math.round(pct)))}%` });

/** The skeleton of a loading page: the board's two cards of grey lines. */
export function Skeleton() {
  const t = useT();
  return (
    <div className="uq-skl" aria-busy="true" aria-label={t("uq.loading")}>
      <section className="uq-card uq-skc">
        <i className="sk" style={{ width: 180 }} />
        <i className="sk b" />
        <i className="sk b" />
      </section>
      <section className="uq-card uq-skc">
        <i className="sk" style={{ width: 140 }} />
        <i className="sk b" />
        <i className="sk b" />
        <i className="sk b" />
      </section>
    </div>
  );
}

/** The page did not load: the board's own card, with the one thing to say (nothing changed on the nodes) and the button to try again. */
export function ErrorCard({ onRetry }: { onRetry: () => void }) {
  const t = useT();
  return (
    <section className="uq-card us-none pv-qe" role="alert">
      <span className="g bad" aria-hidden>
        <G d={D.warn} size={20} stroke={1.75} />
      </span>
      <b>{t("uq.err.title")}</b>
      <span>{t("uq.err.text")}</span>
      <Button variant="secondary" size="lg" onClick={onRetry}>
        {t("uq.retry")}
      </Button>
    </section>
  );
}
