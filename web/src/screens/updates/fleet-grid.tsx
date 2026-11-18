import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Scramble } from "@/components/fx/scramble-react";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { D, NodeFlag, NodeName, useFlags, w } from "./bits";
import { fleetMatrix, MATRIX_FROM, type CardKind, type FleetCard } from "./fleet";

// The fleet of the agents tab (the board's .uq-fleet): a card per node while there are few, and from MATRIX_FROM nodes the lead's card
// beside a matrix of cells (.uq-fleet.many). Only the first rows of the matrix show until the person asks for the rest.

const wordKey = { ok: "uq.fl.ok", can: "uq.fl.can", sent: "uq.fl.sent", gate: "uq.fl.gate", back: "uq.fl.back", off: "uq.fl.off", wait: "uq.fl.wait", manual: "uq.fl.manual", failed: "uq.fl.failed" } as const;

/** What a card or a cell says about its node's state, in words. */
const word = (t: T, c: FleetCard) => t(wordKey[c.word], { version: c.wordVersion });

/** One card of the strip: flag and name, the canary mark, the tick (or the spinner, or the cross), the version and what it is doing. */
function Card({ c, countryCode, t }: { c: FleetCard; countryCode?: string; t: T }) {
  return (
    <div className={cx("uq-ft", c.kind, c.canary && "can", c.just && "just")}>
      <div className="uq-fc">
        <NodeName name={c.name} countryCode={countryCode} />
        {c.canary && <em className="uq-fr">{t("uq.fl.canary")}</em>}
        <i className="uq-fd" aria-hidden>
          <svg viewBox="0 0 24 24">
            <path d={D.check} />
          </svg>
          <svg className="x" viewBox="0 0 24 24">
            <path d={D.x} />
          </svg>
        </i>
      </div>
      <b className="mono uq-fv">
        <Scramble value={c.version} once />
      </b>
      <span className="uq-fs">{word(t, c)}</span>
      {c.pg !== undefined && <i className="rw-pg" style={w(c.pg)} aria-hidden />}
    </div>
  );
}

/** The mark of a cell: the tick of a node that is up to date, the cross of one that failed, else a dot or a ring in the card's colour. */
const mark = (kind: CardKind) =>
  kind === "ok" || kind === "bad" ? (
    <svg className="i" viewBox="0 0 24 24" aria-hidden>
      <path d={kind === "ok" ? D.check : D.x} />
    </svg>
  ) : (
    <i className="mk" aria-hidden />
  );

function Cell({ c, countryCode, t }: { c: FleetCard; countryCode?: string; t: T }) {
  return (
    <span className={cx("uq-mc", c.kind, c.just && "just")} title={`${c.name}: ${word(t, c)}`}>
      <NodeFlag countryCode={countryCode} />
      <b className="mono">{c.name}</b>
      {mark(c.kind)}
      <span className="sr-only">{word(t, c)}</span>
    </span>
  );
}

/** Rows of cells that show before "Show all" (a row is 44px, see .uq-mcs.fold). */
const FOLD_ROWS = 8;

/** The lead's card and the matrix of the other nodes. */
function Matrix({ cards, version, unfolded }: { cards: readonly FleetCard[]; version: string; unfolded?: boolean }) {
  const t = useT();
  const flags = useFlags();
  const m = fleetMatrix(cards);
  const allUp = m.cells.every((c) => c.kind === "ok");
  const grid = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(!!unfolded);
  const [tall, setTall] = useState(false);
  // the columns follow the width, so whether the rows pass the fold is measured, not counted: after every render, and when the grid is resized
  const measure = () => setTall((grid.current?.scrollHeight ?? 0) > FOLD_ROWS * 44 + 1);
  useLayoutEffect(measure);
  useEffect(() => {
    const el = grid.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => setTall(el.scrollHeight > FOLD_ROWS * 44 + 1));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return (
    <div className={cx("uq-fleet many", !m.lead && "all")}>
      {m.lead && (
        <>
          <div className="uq-mh uq-mh1">
            <span>{t("uq.mx.wait")}</span>
            <em className="mono">{m.waiting}</em>
          </div>
          <Card c={m.lead} countryCode={flags.get(m.lead.id)} t={t} />
        </>
      )}
      <div className="uq-rest">
        <div className="uq-mh">
          <span>{t(allUp ? "uq.mx.cur" : "uq.mx.rest")}</span>
          <em className="mono">{m.cells.length}</em>
          {version && <b className="mono">{version}</b>}
        </div>
        <div ref={grid} className={cx("uq-mcs", tall && !open && "fold")}>
          {m.cells.map((c) => (
            <Cell key={c.id} c={c} countryCode={flags.get(c.id)} t={t} />
          ))}
        </div>
      </div>
      {tall && (
        <button type="button" className="an-lk uq-mxm" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? t("uq.mx.fold") : t("uq.mx.all", { n: m.cells.length })}
        </button>
      )}
    </div>
  );
}

/** The cards of the nodes, or the lead's card and the matrix of the rest when the fleet is big. `version` is the release the fleet is measured by; `unfolded` opens the matrix (a scene of the dev kit). */
export function Fleet({ cards, version, unfolded }: { cards: readonly FleetCard[]; version: string; unfolded?: boolean }) {
  const t = useT();
  const flags = useFlags();
  if (cards.length >= MATRIX_FROM) return <Matrix cards={cards} version={version} unfolded={unfolded} />;
  return (
    <div className="uq-fleet">
      {cards.map((c) => (
        <Card key={c.id} c={c} countryCode={flags.get(c.id)} t={t} />
      ))}
    </div>
  );
}
