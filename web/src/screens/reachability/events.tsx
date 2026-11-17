import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { buttonClass } from "@/components/ui/button";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { alertsQuery, countryIn, type Alert } from "@/lib/health";
import { Ipv6Tag } from "@/screens/health/alerts";
import { Dot, Gl, P, Pill } from "@/screens/health/page-parts";

// The country_blocked alerts of the week, open and closed, newest first, as the board draws them (PnReach): the card of the Nodes tab (the
// last few, each a dot, the node and what is wrong, when) and the Events tab (all of them, each with its pill). Each leads to its row on
// Health → Alerts.

const WEEK = 7 * 86400;

/** The alert's verdict in a word: "blocked on some networks", "TCP blocked" or "unreachable". */
const eventVerdict: Record<string, MessageKey> = { partial: "cr.v.partialLong", tcp_blocked: "cr.v.tcp", blocked: "cr.v.blocked" };

/** The week's events: open ones first (newest first), then the closed ones of the last seven days. */
export function weekEvents(active: readonly Alert[], history: readonly Alert[], now: number): Alert[] {
  const mine = (a: Alert) => a.kind === AlertKind.COUNTRY_BLOCKED;
  return [...active.filter(mine).sort((a, b) => b.firstSeenUnix - a.firstSeenUnix), ...history.filter((a) => mine(a) && a.resolvedAtUnix >= now - WEEK)];
}

export function useEvents() {
  const q = useQuery(alertsQuery);
  return { q, events: q.data ? weekEvents(q.data.active, q.data.history, q.data.nowUnix) : [] };
}

function EventRow({ a, pill }: { a: Alert; pill?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const open = a.resolvedAtUnix === 0;
  const lasted = Math.max(0, a.resolvedAtUnix - (a.openedUnix || a.firstSeenUnix));
  const verdict = eventVerdict[a.params.verdict ?? ""];
  const when = open
    ? t("rc.ev.open", { ago: fmt.ago(a.openedUnix || a.firstSeenUnix) })
    : [t("rc.ev.closed", { ago: fmt.ago(a.resolvedAtUnix) }), lasted > 0 ? t("hl.alerts.lasted", { duration: fmt.duration(lasted) }) : ""].filter(Boolean).join(" · ");
  const kind = open ? (a.severity === AlertSeverity.CRITICAL ? "bad" : "part") : "none";
  return (
    <Link to="/health" hash={open ? `alert-${a.id}` : `alert-${a.id}-${a.resolvedAtUnix}`} data-testid="reach-event" className="rc-ev">
      {pill ? <Pill kind={open ? (kind === "bad" ? "bad" : "part") : ""} glyph={kind}>{open ? t("rp.ev.open") : t("rp.ev.closed")}</Pill> : <Dot kind={kind} />}
      <span>
        <b>
          <span className="mono">{a.nodeName}</span> · {verdict ? t(verdict) : a.params.verdict}
          <Ipv6Tag alert={a} className="ms-1.5" />
        </b>
        <em>{[when, fmt.stamp(open ? a.openedUnix || a.firstSeenUnix : a.resolvedAtUnix)].join(" · ")}</em>
      </span>
      <Gl d={P.chev} />
    </Link>
  );
}

const SHOWN = 5;

/** The card "Recent events" of the Nodes tab: the last few, the way to all of them, what the card says. */
export function EventsCard({ code, events, noneText }: { code: string; events: readonly Alert[]; noneText: string }) {
  const t = useT();
  return (
    <section className="card pp-sec sb-sec">
      <div className="pp-sh">
        <span className="pp-ic sand" aria-hidden="true">
          <Gl d={P.clock} />
        </span>
        <h3>{t("rc.ev.title")}</h3>
        <Link to="/health" className="an-lk rc-all">
          {t("rc.ev.all")}
        </Link>
      </div>
      {code && <p className="sb-lead">{t("rc.ev.sub", { country: countryIn(t, code) })}</p>}
      {events.length === 0 && <p className="sb-emp rc-evn">{noneText}</p>}
      <div className="rc-evs">
        {events.slice(0, SHOWN).map((a) => (
          <EventRow key={`${a.id}/${a.resolvedAtUnix}`} a={a} />
        ))}
      </div>
    </section>
  );
}

/** The Events tab: every event of the week with its pill, and what opens and closes one. */
export function EventsTab({ code, events, noneText }: { code: string; events: readonly Alert[]; noneText: string }) {
  const t = useT();
  return (
    <section className="card pp-sec sb-sec">
      <div className="pp-sh">
        <span className="pp-ic sand" aria-hidden="true">
          <Gl d={P.clock} />
        </span>
        <h3>{t("rp.ev.title")}</h3>
        <Link to="/health" className={cx(buttonClass("secondary", "md"), "ms-auto")}>
          {t("rc.ev.all")}
          <Gl d={P.arrow} />
        </Link>
      </div>
      <div className="rc-evs rc-evl">
        {events.length === 0 && <p className="sb-emp rc-evn">{noneText}</p>}
        {events.map((a) => (
          <EventRow key={`${a.id}/${a.resolvedAtUnix}`} a={a} pill />
        ))}
      </div>
      <p className="sb-vfoot">{t("rp.ev.foot", { country: countryIn(t, code) })}</p>
    </section>
  );
}
