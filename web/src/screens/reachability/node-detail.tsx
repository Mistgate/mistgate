import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { buttonClass } from "@/components/ui/button";
import { useT, type T } from "@/i18n";
import { groupText, troubleText, type ClientProbeNode } from "@/lib/clientprobe";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { countryIn, ipv6Why, type Alert, type CountryCheck, type CountryLook } from "@/lib/health";
import { ipv6Helps, ipv6State, shownSources, type Lane, type ReachSource } from "@/lib/reachability";
import { entryOffered } from "@/lib/ways";
import { Dot, Gl, P } from "@/screens/health/page-parts";
import { glyphOf, networksOf, sayOf, sourceKey, startedAt, verdictText, weekBar, type Hold } from "./detail-model";
import type { NodeRow, State } from "./nodes";
import "./reach.css";

// The open row of a node (the board's `.rc-d.r3`): the verdict in words with the facts beside it; the week of each source as a bar, and the
// networks that see the node and those that do not; what the reports of kl!ck users say; the hint for a profile through a CDN (or for the
// IPv6 entry), and the buttons. The numbers and the words come from detail-model.ts.

type Props = {
  n: NodeRow;
  check: CountryCheck | undefined;
  look: CountryLook | null;
  state: State;
  lanes: ReadonlyMap<string, Lane> | undefined;
  lane6: Lane | undefined;
  v6: ReturnType<typeof ipv6State>;
  sources: readonly string[];
  probe: boolean;
  country: string;
  since: number;
  now: number;
  owner: boolean;
  report: ClientProbeNode | undefined;
  reportsOn: boolean;
  holdOf: (s: ReachSource) => Hold;
  alert: Alert | undefined;
};

const DAY = 86400;

export function NodeDetail(p: Props) {
  const t = useT();
  const fmt = useFmt();
  const { n, check: c, look, state, v6 } = p;
  const shown = shownSources(p.sources);
  const verdict = verdictText(t, fmt, state, c, look, p.lanes, p.now, p.country);
  const nets = networksOf(t, fmt, c);
  const began = state === "ok" || state === "nd" ? 0 : startedAt(shown.flatMap((s) => p.lanes?.get(s) ?? []), p.now);
  const facts = [
    nets.total > 0 && state !== "nd" ? { k: t("r3.f.nets"), v: t("r3.f.nets.v", { n: nets.seen, total: nets.total }) } : null,
    began > 0 ? { k: t("r3.f.since"), v: fmt.dateTime(began) } : null,
    c && c.atUnix > 0 && state !== "nd" ? { k: t("r3.f.checked"), v: fmt.ago(c.atUnix) } : null,
  ].filter((f) => f != null);
  const glyph = glyphOf(state === "ok" ? "" : state === "nd" ? "mut" : state === "part" || state === "tcp" ? "part" : "bad");
  return (
    <div id={`rc-d-${n.id}`} className="rc-d r3">
      <div className="r3-top">
        <div className={cx("r3-v", glyph === "none" ? "" : glyph)} data-testid="reach-verdict">
          <Dot kind={glyph} />
          <div>
            <b>{verdict.title}</b>
            {verdict.sub && <span>{verdict.sub}</span>}
          </div>
        </div>
        {facts.length > 0 && (
          <dl className="r3-f">
            {facts.map((f) => (
              <div key={f.k}>
                <dt>{f.k}</dt>
                <dd>{f.v}</dd>
              </div>
            ))}
          </dl>
        )}
      </div>
      <div className="r3-g">
        <section className="r3-wk">
          <h4>{t("rp.week")}</h4>
          {shown.map((src) => (
            <WeekRow key={src} source={src} on={p.sources.includes(src)} lane={p.lanes?.get(src)} probe={src === "tspu" && p.probe} hold={p.holdOf(src)} since={p.since} now={p.now} extra={src === "globalping" ? <Ipv6Row v6={v6} check={c} lane={p.lane6} since={p.since} now={p.now} hold={p.holdOf(src)} /> : null} />
          ))}
          <div className="r3-ax" aria-hidden="true">
            <span>{t("rc.axis.ago")}</span>
            <span>{t("rc.axis.now")}</span>
          </div>
        </section>
        <section className="r3-net">
          <h4>{t("r3.net")}</h4>
          {nets.groups.map((g) => (
            <div key={g.kind} className="r3-ng" data-testid={`nets-${g.kind}`}>
              <span className={cx("r3-nt", g.kind === "bad" && "bad")}>
                {t(g.kind === "bad" ? "r3.net.bad" : g.kind === "ok" ? "r3.net.ok" : "r3.net.none")}
                <em className="mono">{g.n}</em>
              </span>
              {g.items.map((o, i) => (
                <div key={i} className="r3-ni">
                  <Dot kind={g.kind === "bad" ? "bad" : g.kind === "ok" ? "ok" : "none"} />
                  <b title={o.name}>{o.name}</b>
                  <span title={o.meta}>{o.meta}</span>
                  <em>{o.source}</em>
                </div>
              ))}
            </div>
          ))}
          {(nets.skip || nets.groups.length === 0) && <p className="r3-nn">{nets.skip || t(state === "nd" ? "r3.net.wait" : "r3.net.empty")}</p>}
        </section>
      </div>
      {p.reportsOn && <Reports report={p.report} />}
      {ipv6Helps(c) && n.ipv6 && !n.ipv6.entry && (
        <div className="r3-v6">
          <EntryHint node={n} country={p.country} offer={p.owner && entryOffered(n.ipv6)} />
        </div>
      )}
      <div className="r3-ft">
        {(state === "part" || state === "tcp" || state === "bad") && (
          <p className="r3-tip">
            {t("r3.tip")}
            <Link to="/profiles" className="an-lk">
              {t("r3.tip.go")}
            </Link>
          </p>
        )}
        <div className="hl-act">
          <Link to="/nodes/$id" params={{ id: n.id }} className={buttonClass("secondary", "md")}>
            {t("rp.openNode")}
          </Link>
          {state !== "ok" && state !== "nd" && (
            <Link to="/health" hash={p.alert ? `alert-${p.alert.id}` : undefined} className={buttonClass("secondary", "md")}>
              {t("rp.openAlert")}
            </Link>
          )}
        </div>
      </div>
    </div>
  );
}

/** One source of one node: its name, its word now with the dot of its tone, and its week as a bar of stretches. */
function WeekRow(p: { source: ReachSource; on: boolean; lane: Lane | undefined; probe: boolean; hold: Hold; since: number; now: number; extra: ReactNode }) {
  const t = useT();
  const fmt = useFmt();
  const say = sayOf(t, p.source, p.on, p.lane, p.probe, p.now, p.hold, t("cr.v.unknown"));
  return (
    <>
      <div className="r3-r" data-testid={`week-${p.source}`} data-on={p.on}>
        <b>{t(sourceKey[p.source])}</b>
        <Verdict word={say.word} tone={say.tone} />
        <Bar stretches={weekBar(t, fmt, p.source, p.lane, p.on, p.since, p.now)} />
      </div>
      {p.extra}
    </>
  );
}

function Verdict({ word, tone, hint = "" }: { word: string; tone: "" | "part" | "bad" | "mut"; hint?: string }) {
  return (
    <span className={cx("r3-s", tone)}>
      <Dot kind={glyphOf(tone)} />
      <span>{word}</span>
      {hint && <span className="sr-only">: {hint}</span>}
    </span>
  );
}

function Bar({ stretches }: { stretches: ReturnType<typeof weekBar> }) {
  return (
    <span className="r3-b" aria-hidden="true">
      {stretches.map((s, i) => (
        <i key={i} className={s.kind === "none" ? "" : s.kind} style={{ flexGrow: s.sec / DAY }} title={s.title || undefined} />
      ))}
    </span>
  );
}

/** Globalping's IPv6 side under its IPv4 line (the board has it always): its week, or "no data", with the reason (the server has no IPv6) said to a screen reader and on hover. */
function Ipv6Row({ v6, check, lane, since, now, hold }: { v6: ReturnType<typeof ipv6State>; check: CountryCheck | undefined; lane: Lane | undefined; since: number; now: number; hold: Hold }) {
  const t = useT();
  const fmt = useFmt();
  const data = v6 === "data";
  const say = sayOf(t, "globalping", data, lane, false, now, hold, t("cr.v.unknown"));
  const why = !data && check?.ipv6 ? ipv6Why(t, check.ipv6) : "";
  return (
    <div className="r3-r sub" data-testid="week-ipv6" title={why || undefined}>
      <b>
        <span className="sr-only">{t("rc.src.globalping")} · </span>IPv6
      </b>
      <Verdict word={data ? say.word : t("cr.v.unknown")} tone={data ? say.tone : "mut"} hint={why} />
      <Bar stretches={weekBar(t, fmt, "globalping", lane, data, since, now)} />
    </div>
  );
}

/** What the reports of kl!ck users say of the node: their trouble and when, the operators and the regions as chips. */
function Reports({ report }: { report: ClientProbeNode | undefined }) {
  const t = useT();
  const fmt = useFmt();
  const x = report?.tally;
  const trouble = x ? troubleText(t, x) : "";
  const groups = (g: ClientProbeNode["operators"]) => g.map((o) => ({ key: o.name, text: groupText(t, o.name, o.tally!), bad: !!troubleText(t, o.tally!) }));
  return (
    <div className="r3-kl">
      <div className="r3-kr">
        <span className="rc-dt">{t("rp.klick")}</span>
        <p>
          <b>{x ? trouble || t("kc.ok") : t("rp.klick.none")}</b>
          {x && <em>{[t.n("kc.reports", x.reports), fmt.ago(x.lastUnix)].join(" · ")}</em>}
        </p>
      </div>
      {report && report.operators.length > 0 && <Chips label={t("kc.operators")} items={groups(report.operators)} />}
      {report && report.regions.length > 0 && <Chips label={t("kc.regions")} items={groups(report.regions)} />}
    </div>
  );
}

function Chips({ label, items }: { label: string; items: { key: string; text: string; bad: boolean }[] }) {
  return (
    <div className="r3-kr">
      <span className="rc-dt">{label}</span>
      <div className="rc-ch">
        {items.map((g) => (
          <i key={g.key} className={g.bad ? "bad" : ""}>
            {g.text}
          </i>
        ))}
      </div>
    </div>
  );
}

/**
 * "IPv4 is blocked from Russia, IPv6 gets through." under a node whose IPv6 entry is off (ipv6 plan §2.6, §3.8); `offer`: with
 * "Add IPv6 entry…", which opens the entry's dialog on the node's Ways in.
 */
function EntryHint({ node, country, offer }: { node: NodeRow; country: string; offer: boolean }) {
  const t: T = useT();
  return (
    <div data-testid="reach-v6-hint" className="rc-hint">
      <span>
        <Gl d={P.check} />
        {t("ways.ipv6.helps", { country: countryIn(t, country) })}
      </span>
      {offer && (
        <Link to="/nodes/$id" params={{ id: node.id }} search={{ tab: "ways", way: "ipv6", add: "ipv6" }} className={buttonClass("secondary", "md")}>
          {t("ways.ipv6.add")}
        </Link>
      )}
    </div>
  );
}
