import { useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { Scramble } from "@/components/fx/scramble-react";
import { Logo } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useTheme } from "@/lib/theme";
import { G } from "@/screens/users/user-bits";
import { D, Gl } from "./bits";
import { runMatrix } from "./matrix";
import { Track, trackOf } from "./track";

// The screen that takes over while the panel installs itself (the board's .uq-take): the whole window, a live matrix of dots, the
// version, the four steps of the update and what the person can count on. The panel goes down for about a minute and comes back
// by itself; after two minutes of nothing it offers to reload the page. When the new version answers, the screen says so and
// fades out. `step` is the board's step (0..3); the panel reports none (only that it is installing), so the page passes
// none and the screen shows what is true of every step: the version, the spinner and the reassurance.

export type TakeMode = "run" | "slow" | "out";

export function Takeover({ mode, step, from, to, onReload, hold }: { mode: TakeMode; step?: number; from: string; to: string; onReload: () => void; /** The "done" screen stays (the dev kit's screenshot). */ hold?: boolean }) {
  const t = useT();
  const light = useTheme() === "light";
  const out = mode === "out";
  const slow = mode === "slow";
  const cv = useRef<HTMLCanvasElement>(null);
  // the matrix reads the step, the mode and the theme at every frame: it is started once
  const live = useRef({ k: 0, mode: "" as "" | "slow" | "out", light });
  useEffect(() => {
    live.current = { k: out ? 4 : (step ?? 0), mode: out ? "out" : slow ? "slow" : "", light };
  }, [out, slow, step, light]);
  useEffect(() => {
    const el = cv.current;
    return el ? runMatrix(el, () => live.current) : undefined;
  }, []);
  const title = out ? t("uq.take.done") : t("uq.take.title");
  const known = step !== undefined;
  const line = slow ? t("uq.take.wait") : known ? t(`uq.step.${Math.min(step, 3) + 1}` as "uq.step.1") + "…" : t("uq.run.generic", { version: to });
  const note = known && step >= 2 ? t("uq.take.n2") : t("uq.take.n1");
  const tr = trackOf({ run: true, step: known ? step : -1 });
  return createPortal(
    <div className={cx("uq-take", out && "out", hold && "hold", slow && "slow")} role="alertdialog" aria-modal="true" aria-label={title} data-shot="updates-takeover">
      <div className="uq-tw">
        <canvas ref={cv} className="uq-mx" aria-hidden />
        <div className="uq-tk">
          <span className="uq-tlogo">
            <Logo size={28} />
          </span>
          <span className="uq-tt">{title}</span>
          <b className="mono uq-tv">
            <Scramble value={out ? to : from} />
          </b>
          {!out && (
            <>
              <span className="uq-tto mono">
                <G d={D.arrow} size={16} stroke={1.75} />
                {to}
              </span>
              {known && (
                <div className="uq-trk uq-ttrk" role="img" aria-label={t("uq.trk.label")}>
                  <Track steps={tr.steps} width={tr.width} />
                </div>
              )}
              <p className="uq-ts">
                <span className="an-sp" aria-hidden />
                {line}
              </p>
              <p className="uq-tn">{note}</p>
            </>
          )}
          {out && (
            <span className="pv-pill ok uq-tok">
              <Gl k="ok" />
              {t("uq.take.ok")}
            </span>
          )}
          {slow && (
            <div className="uq-tslow">
              <span>{t("uq.take.slow")}</span>
              <Button variant="secondary" size="lg" onClick={onReload}>
                {t("uq.take.reload")}
              </Button>
            </div>
          )}
        </div>
      </div>
    </div>,
    document.body,
  );
}

