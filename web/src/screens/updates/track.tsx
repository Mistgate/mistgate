import type { ReactNode } from "react";
import { useT } from "@/i18n";
import { D, w } from "./bits";

// The four steps of a panel update on a dotted line (the board's .uq-trk): the release, the copy, the restart, the check. The
// dots that are done tick, the one under way glows, a step that went wrong is crossed out.

export type TrackStep = { n: 1 | 2 | 3 | 4; c: "" | "ok" | "now" | "bad" };

/** The track for what the page is in: a run at `step` (0..3), a panel that went back (the last step crossed), a signature that did not match (the first). */
export function trackOf({ run, step = 0, back, sig }: { run?: boolean; step?: number; back?: boolean; sig?: boolean }): { steps: TrackStep[]; width: number } {
  const steps = ([1, 2, 3, 4] as const).map((n, i): TrackStep => ({ n, c: run ? (i < step ? "ok" : i === step ? "now" : "") : back ? (i < 3 ? "ok" : "bad") : sig ? (i === 0 ? "bad" : "") : "" }));
  return { steps, width: run ? Math.round((Math.max(0, step) / 3) * 100) : back ? 100 : 0 };
}

export function Track({ steps, width, children }: { steps: readonly TrackStep[]; width: number; children?: ReactNode }) {
  const t = useT();
  return (
    <>
      <i className="uq-tl" aria-hidden>
        <u style={w(width)} />
      </i>
      <ol>
        {steps.map((s) => (
          <li key={s.n} className={s.c}>
            <i aria-hidden>
              <svg viewBox="0 0 24 24">
                <path d={D.check} />
              </svg>
              <svg className="x" viewBox="0 0 24 24">
                <path d={D.x} />
              </svg>
            </i>
            <span>{t(`uq.trk.${s.n}` as "uq.trk.1")}</span>
          </li>
        ))}
      </ol>
      {children}
    </>
  );
}

/** The track as the hero draws it: its own box with the label a screen reader reads. */
export function HeroTrack({ steps, width }: { steps: readonly TrackStep[]; width: number }) {
  const t = useT();
  return (
    <div className="uq-trk" role="img" aria-label={t("uq.trk.label")}>
      <Track steps={steps} width={width} />
    </div>
  );
}

