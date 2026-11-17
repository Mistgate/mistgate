import { useQuery } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import { accessQuery, askAssistant } from "@/lib/assistant";
import { peopleOrder, type Alert } from "@/lib/health";
import { AlertRows, useOpenRow } from "./alerts";
import { CloseAll } from "./alerts-parts";
import type { FixFlow } from "./fix";
import { Calm, Gl, P, Sec } from "./page-parts";
import "./health.css";

/**
 * People: the alerts the panel derives about a person, not a node (access ended but the app keeps asking; a key that never connected or went stale; a quiet
 * app; torrents; the device limit). Warnings first, then info; each is a row of the board's list with the person's name, a button to their page, Close and
 * Mute. They come from the evaluator (subscription fetches, handshakes, torrent attempts, device-limit refusals), not from the tunnel checks, so the
 * Cloudflare edition raises them too. Under them the card that has the assistant look at who cannot open what, where the assistant is on.
 */
export function PeopleTab({ active, now, flow, first = false }: { active: Alert[]; now: number; flow: FixFlow; first?: boolean }) {
  const t = useT();
  const rows = peopleOrder(active);
  const groups = rows.map((a) => [a]);
  const access = useQuery(accessQuery);
  const [open, setOpen] = useOpenRow(groups);
  return (
    <div className="sb-tab">
      {rows.length > 0 && (
        <Sec tone="sand" icon={P.user} title={t("hlp.open")} count={rows.length} action={<CloseAll alerts={rows} />}>
          <p className="sb-lead">{t("hlp.pp.lead")}</p>
          <div className="hl-als">
            <AlertRows groups={groups} now={now} flow={flow} open={open} setOpen={setOpen} />
          </div>
        </Sec>
      )}
      {rows.length === 0 && !first && <Calm kind="ok" className="hl-calm" title={t("hlp.calm.title")} text={t("hlp.pp.calm")} />}
      {access.data?.available && (
        <section className="card hl-ask">
          <span className="pp-ic lav" aria-hidden="true">
            <Gl d={P.sparkle} />
          </span>
          <div>
            <b>{t("hlp.ask.title")}</b>
            <span>{t("hlp.ask.text")}</span>
          </div>
          <Button variant="secondary" size="md" className="btn sm" onClick={() => askAssistant(t("hl.people.diagnosePrompt"))}>
            {t("hlp.ask.button")}
            <Gl d={P.arrow} />
          </Button>
        </section>
      )}
    </div>
  );
}
