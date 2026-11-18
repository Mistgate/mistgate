import { Link } from "@tanstack/react-router";
import type { CSSProperties } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { useT } from "@/i18n";
import { useEffectsEnabled } from "@/lib/effects";
import type { RelayMsg } from "@/lib/relay";
import type { Lane } from "@/lib/ways";
import { stopJob, useJob } from "./relay-job";
import type { WaysModel } from "./ways";
import "./relay-wizard.css";

// The strip over the ways (the board's .nd-wa, plan §8 "Failures and restarts"): a run that goes on in the background says how far it is, and a run
// that was cut short (the tab was closed, the panel restarted) says what it left: relays made and checked but never turned on. They stay off, so people
// were given nothing; "Finish it" opens the wizard at its servers, where those are ticked already, and a run again makes nothing twice.

/** A relay that was made and never turned on: a run left it. One switched off by hand has been on once (`appliedUnix`). */
export const unfinished = (r: Pick<RelayMsg, "enabled" | "appliedUnix">) => !r.enabled && r.appliedUnix === 0;

type Group = { entry: { id: string; name: string }; exit: { id: string; name: string }; n: number };

/** What each pair of this node's lanes left unfinished: on an exit the relays through each entry, on an entry the relays it carries for each exit. */
export function unfinishedGroups(lanes: readonly Lane[], out: readonly Lane[], node: { id: string; name: string }): Group[] {
  const groups: Group[] = [];
  for (const l of lanes) if (l.kind === "via" && l.peer) groups.push({ entry: l.peer, exit: node, n: l.relays.filter(unfinished).length });
  for (const l of out) if (l.peer) groups.push({ entry: node, exit: l.peer, n: l.relays.filter(unfinished).length });
  return groups.filter((g) => g.n > 0);
}

const warnGlyph = (
  <svg viewBox="0 0 24 24" className="size-[22px]" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
    <path d="M12 4l9 16H3zM12 10v4M12 17v.5" />
  </svg>
);
const runGlyph = (
  <svg viewBox="0 0 24 24" className="size-[22px]" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
    <path d="M4 8h13l-3-3M20 16H7l3 3" />
  </svg>
);

/**
 * The strip: a run in the background (how many are through, a bar, "Open" and "Stop"), or one strip for each pair that has relays made and not turned on.
 * Owner's: a reader never sees the strip, the relays are not his to turn on.
 */
export function RelayStrip({ w, node, onOpen }: { w: WaysModel; node: { id: string; name: string }; onOpen: (entryId: string) => void }) {
  const t = useT();
  const job = useJob(node.id);
  const fx = useEffectsEnabled();
  if (!w.owner) return null;
  const running = job && (job.state === "running" || job.state === "stopping");
  // the pair the run is for is its own strip while it goes: its relays are off only until each is through
  const left = unfinishedGroups(w.lanes, w.out, node).filter((g) => !(running && g.exit.id === job.exitId && g.entry.id === job.entry.id));
  if (!running && left.length === 0) return null;
  return (
    <div className="flex flex-col" data-testid="relay-strip" data-fx={fx ? "1" : undefined}>
      {running && (
        <div className="wy-wip run" role="status" data-testid="relay-run-strip">
          <span className="g">{runGlyph}</span>
          <div>
            <b>{t("wy.wip.run", { entry: job.entry.name, k: job.done, n: job.items.length })}</b>
            <i className="wy-pg" style={{ "--pgw": `${Math.round((job.done / Math.max(1, job.items.length)) * 100)}%` } as CSSProperties} aria-hidden />
          </div>
          <div>
            <Button size="md" onClick={() => onOpen(job.entry.id)}>
              {t("wy.wip.open")}
            </Button>
            <Button size="md" disabled={job.state === "stopping"} onClick={() => stopJob(job.exitId)}>
              {job.state === "stopping" ? t("wy.wiz.stopping") : t("wy.wiz.stop")}
            </Button>
          </div>
        </div>
      )}
      {left.map((g) => (
        <div key={`${g.entry.id}>${g.exit.id}`} className="wy-wip" role="status" data-testid="relay-unfinished">
          <span className="g">{warnGlyph}</span>
          <div>
            <b>{t("wy.wip.t")}</b>
            <span className="s">{t.n("wy.wip.s", g.n, { entry: g.entry.name })}</span>
          </div>
          <div>
            {g.exit.id === node.id ? (
              <Button variant="primary" size="md" onClick={() => onOpen(g.entry.id)}>
                {t("wy.wip.go")}
              </Button>
            ) : (
              // an entry's page: the wizard is the exit's, its page is one click away
              <Link to="/nodes/$id" params={{ id: g.exit.id }} search={{ tab: "ways", add: `via:${g.entry.id}` }} className={buttonClass("primary", "md")}>
                {t("wy.wip.go")}
              </Link>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}
