import { Link } from "@tanstack/react-router";
import { useEffect, useId, useRef, useState } from "react";
import { EditionNotice } from "@/components/edition-notice";
import { Scramble } from "@/components/fx/scramble-react";
import { Button, buttonClass } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CheckStatus, type GetChecksResponse } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { cellLook, checkError, countryColumn, countryLook, listOf, SLOW_CONNECT_MS, type CellLook, type CheckCell, type CountryCheck, type CountryLook } from "@/lib/health";
import type { Plain } from "@/lib/plain";
import { protocolName } from "@/lib/series";
import { runLook } from "@/lib/tspu";
import { OwnPoints, PartialBreakdown, useCountryTitle } from "./country";
import { Bars, ResultLine, useRunChecks } from "./history";
import { Calm, Dot, Gl, P, Sec, type DotKind } from "./page-parts";
import "./health.css";

type Checks = Plain<GetChecksResponse>;

// The client-eye checks as the board draws them (PnHealth, "Проверки"): a card with the table of nodes, a row a node with the verdict of each profile (and,
// when the country check is on, of the country), a press opens the row into a box per profile with its last result and its day. "Check now" walks the rows one
// after another (the row being asked sweeps, the rest wait, the last ones flash once with their tick popping), and the numbers settle into place with the
// reveal animation on. The columns are the profiles deployed on the nodes, not the board's three: the table takes as many as there are.

/**
 * "The panel's server loses UDP" (GetChecksResponse.udp_loss): the UDP checks of some nodes keep paying for lost packets
 * against VLESS to the same nodes, with the Hysteria2 packets lost both ways (the panel's client on the way there, the
 * nodes on the way back); the panel's own network, people are not affected. Calm and grey, it is not a problem of the nodes.
 */
export function UdpLossNotice({ data }: { data: Checks }) {
  const t = useT();
  const fmt = useFmt();
  const loss = data.udpLoss;
  if (!loss || loss.nodeIds.length === 0) return null;
  const nodes = loss.nodeIds.map((id) => data.rows.find((r) => r.nodeId === id)?.nodeName ?? id);
  const udp = [...new Set(data.columns.filter((c) => c.protocol === "hysteria2" || c.protocol === "awg" || c.protocol === "amneziawg").map((c) => protocolName(c.protocol)))];
  const title = t("hl.udp.title");
  const body = t("hl.udp.body", {
    protocols: listOf(fmt.locale, udp),
    to: t.n("hl.udp.to", nodes.length, { nodes: listOf(fmt.locale, nodes) }),
    share: loss.sharePercent,
    lost:
      loss.lostPermille + loss.nodeLostPermille > 0
        ? t("hl.udp.lost", { client: (loss.lostPermille / 10).toFixed(1), node: (loss.nodeLostPermille / 10).toFixed(1) })
        : "",
  });
  return (
    <p className="rc-nt hl-udp">
      <Dot kind="none" />
      <span>
        {/* no space after a full-width colon (zh) */}
        <b>{title}</b>
        {title.endsWith("：") ? "" : " "}
        {body}
      </span>
    </p>
  );
}

/**
 * The newest run of the TSPU probe (GetChecksResponse.tspu_run), the foot of the table: on which node it ran, when, and where the box sits, or why it gave
 * no verdicts. A run that found nothing has no rows, so this line is the only trace of it here.
 */
export function TspuRunLine({ data }: { data: Checks }) {
  const t = useT();
  const fmt = useFmt();
  const run = data.tspuRun;
  if (!run) return null;
  const look = runLook(t, fmt, run);
  const node = data.rows.find((r) => r.nodeId === run.nodeId)?.nodeName || data.countryChecks.find((c) => c.tspuVantageId === run.nodeId)?.tspuVantageName || run.nodeId;
  return (
    <p data-testid="tspu-run" className="sb-vfoot">
      {t("tp.title")} · <span className="mono">{node}</span>
      {look.kind === "ok" ? ` · ${look.detail}` : ` · ${look.word} · ${look.detail}`}
    </p>
  );
}

/** What the words in the cells mean, under the table: the tooltip says it too, but a phone has no hover. Folded, as the board folds it. */
export function CheckLegend({ slow = false, defaultOpen = false }: { slow?: boolean; defaultOpen?: boolean }) {
  const t = useT();
  const id = useId();
  const [open, setOpen] = useState(defaultOpen);
  const items = [
    ["hlp.leg.fail", "hlp.leg.fail.d"],
    ["hlp.leg.failed", "hlp.leg.failed.d"],
    ["hlp.leg.off", "hlp.leg.off.d"],
    ["hlp.leg.offline", "hlp.leg.offline.d"],
    ["hlp.leg.na", "hlp.leg.na.d"],
  ] as const;
  return (
    <section className="card sb-mv2">
      <button type="button" className="sb-mvh" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>
        <Gl d={P.chev} />
        <span>
          <b>{t("hlp.leg.title")}</b>
          <em>{t("hl.legend.ms")}</em>
        </span>
      </button>
      {open && (
        <div id={id} className="hl-leg">
          {items.map(([b, d]) => (
            <div key={b}>
              <b>{t(b)}</b>
              <span>{t(d)}</span>
            </div>
          ))}
          {slow && (
            <div>
              <b>{t("hlp.leg.slow")}</b>
              <span>{t("hl.legend.slow")}</span>
            </div>
          )}
        </div>
      )}
    </section>
  );
}

/** The walk of "Check now" over the rows (the board's ckr): the one asked is "run", the rest "wait", the done ones "done"; when the last is done they flash. */
function useCheckWalk(ids: readonly string[], frozen: "" | "run" | "flash" = "") {
  // a scene fixes the walk at its first step ("run") or at the flash after it ("flash"); a real page starts at rest
  const [step, setStep] = useState(frozen === "run" ? 1 : 0);
  const [flash, setFlash] = useState(frozen === "flash");
  const timer = useRef<ReturnType<typeof setInterval>>(undefined);
  const wait = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(
    () => () => {
      clearInterval(timer.current);
      clearTimeout(wait.current);
    },
    [],
  );
  const start = () => {
    if (step > 0 || frozen) return;
    clearTimeout(wait.current);
    setFlash(true);
    if (ids.length === 0) {
      wait.current = setTimeout(() => setFlash(false), 1600);
      return;
    }
    setStep(1);
    let k = 1;
    timer.current = setInterval(() => {
      k += 1;
      if (k > ids.length) {
        clearInterval(timer.current);
        setStep(0);
        wait.current = setTimeout(() => setFlash(false), 1600);
      } else setStep(k);
    }, 650);
  };
  const phase = (id: string): "" | "run" | "wait" | "done" => {
    const at = ids.indexOf(id);
    if (at < 0 || step === 0) return "";
    return at === step - 1 ? "run" : at > step - 1 ? "wait" : "done";
  };
  return { busy: step > 0, flash: flash && step === 0, start, phase };
}

/** The board writes a failing value without its cross ("не проходит"): the dot in front of it is the cross (the legend keeps the "✕"). */
const bare = (s: string) => s.replace(/^✕\s*/, "");

/** What a cell says in the row, as a state of the dot and a colour of the text. */
function cellView(look: CellLook): { dot: DotKind; tone: "" | "bad" | "part" | "dim" } {
  switch (look.kind) {
    case "ok":
      return { dot: "ok", tone: "" };
    case "warn":
      return { dot: "part", tone: "part" };
    case "bad":
    case "blip":
      return { dot: "bad", tone: "bad" };
    case "busy":
      return { dot: "busy", tone: "" };
    default:
      return { dot: "none", tone: "dim" };
  }
}

/** What the country makes of the node, as a state of the dot and a colour of the text. */
function countryView(look: CountryLook | null): { dot: DotKind; tone: "" | "bad" | "part" | "dim"; text: string } {
  if (!look) return { dot: "none", tone: "dim", text: "—" };
  const view = { ok: ["ok", ""], partial: ["part", "part"], warn: ["part", "part"], listed: ["part", "part"], bad: ["bad", "bad"], unknown: ["none", "dim"] } as const;
  const [dot, tone] = view[look.kind];
  return { dot, tone, text: look.label };
}

type Row = Checks["rows"][number];

/** One cell of a row: the dot, the figure (settling into place), the slow mark; while the walk is at the row it says so. */
function Cell({ label, dot, tone, text, slow, tip }: { label: string; dot: DotKind; tone: string; text: string; slow?: string; tip?: string }) {
  return (
    <span className={cx("hl-cv", tone)} data-l={label} title={tip}>
      <Dot kind={dot} />
      <Scramble value={text} className="mono" />
      {slow && (
        <span className="hl-slow">
          <Gl d="M12 7v5l3 2M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z" />
          {slow}
        </span>
      )}
    </span>
  );
}

/** The opened row's "check this node now": one node, its own call. */
function NodeRun({ nodeId }: { nodeId: string }) {
  const t = useT();
  const run = useRunChecks(nodeId);
  if (!run.can) return null;
  return (
    <Button variant="secondary" size="md" className="btn sm" disabled={run.running} onClick={run.run}>
      {t("hl.checks.nowOne")}
    </Button>
  );
}

/** One profile's box of an opened row: its name, how it looks now, the last result, the day. */
function ProfileBox({ name, cell, look }: { name: string; cell: CheckCell | undefined; look: CellLook | null }) {
  const t = useT();
  const view = look ? cellView(look) : { dot: "none" as DotKind, tone: "" as const };
  const last = cell?.last;
  const failed = last?.status === CheckStatus.FAILED;
  return (
    <div>
      <span>{name}</span>
      <b className={view.tone === "dim" ? "" : view.tone}>{look && look.kind !== "none" ? bare(look.label) : "—"}</b>
      {look?.slow && <em>{t("hl.cell.connect", { time: look.slow })}</em>}
      {cell && cell.deployed && last && <em>{failed ? checkError(t, last.errorCode) : <ResultLine cell={cell} />}</em>}
      {failed && last?.errorDetail && <em className="mono">{last.errorDetail}</em>}
      {failed && cell && cell.failStreak > 1 && <em>{t("hl.hist.streak", { n: cell.failStreak })}</em>}
      {!cell?.deployed && look && <em>{look.state}</em>}
      {cell && cell.deployed && last && (
        <div className="hl-bars">
          <Bars cell={cell} />
        </div>
      )}
    </div>
  );
}

/** The box of the country in an opened row: the verdict, the line under it, the points of the owner's own probes, the networks of a partial block, the way to the page. */
function CountryBox({ title, look }: { title: string; look: CountryLook | null }) {
  const t = useT();
  const view = countryView(look);
  return (
    <div>
      <span>{title}</span>
      <b className={view.tone === "dim" ? "" : view.tone}>{view.text}</b>
      <em>{look ? look.line : t("cr.v.notYet")}</em>
      {look?.breakdown && <PartialBreakdown breakdown={look.breakdown} className="hl-bars" />}
      {look && !look.breakdown && look.ownPoints.length > 0 && <OwnPoints points={look.ownPoints} className="hl-bars" />}
      <Link to="/reachability" className="an-lk hl-bars">
        {t("rc.open")}
      </Link>
    </div>
  );
}

/**
 * Node × profile table of the synthetic checks, in the board's rows; a press opens a row into a box per profile (and one for the country, with the country check
 * on) with its last result and the 24 h strip, the way to the node and "check this node now". A phone stacks a row's cells under its node. The TSPU probe's
 * last run is the foot of the table, the UDP notice stands over it; the legend folds under the card.
 */
/** What a scene of the dev kit starts the tab with: the row that is open, the walk of "Check now" fixed at a step, the legend opened. */
export type ChecksInit = { open?: string; walk?: "run" | "flash"; legend?: boolean };

export function ChecksTab({ data, first = false, init }: { data: Checks; first?: boolean; init?: ChecksInit }) {
  const t = useT();
  const fmt = useFmt();
  const noChecks = useEdition().off("health_checks"); // no rounds there: no "every 5 min"
  const cols = data.columns;
  const country = countryColumn(data.countryChecks);
  const countryTitle = useCountryTitle(country?.code ?? "");
  // the row that opens by itself: the first node with a profile that fails
  const worst = data.rows.find((r) => r.cells.some((c) => c.last?.status === CheckStatus.FAILED))?.nodeId ?? "";
  const [chosen, setChosen] = useState<string | null>(init?.open ?? null);
  const open = chosen ?? worst;
  const online = data.rows.filter((r) => r.cells.some((c) => c.deployed) && (r.nodeStatus === NodeStatus.ONLINE || r.nodeStatus === NodeStatus.NO_TRAFFIC)).map((r) => r.nodeId);
  const walk = useCheckWalk(online, init?.walk ?? "");
  // the rows walk once the panel says it has something to ask (not while the nodes are offline or were checked a moment ago)
  const run = useRunChecks("", (scheduled) => scheduled > 0 && walk.start());
  const newest = Math.max(0, ...data.rows.flatMap((r) => r.cells.map((c) => c.last?.atUnix ?? 0)));
  const slow = data.rows.some((r) => r.cells.some((c) => c.connectHourMs >= SLOW_CONNECT_MS));
  const n = cols.length + (country ? 1 : 0);

  if (cols.length === 0)
    return (
      <div className="sb-tab">
        <Calm className="hl-calm" icon={P.eye} title={t("hl.checks.title")} text={t("hl.checks.noProfiles")} />
      </div>
    );

  const go = () => {
    if (walk.busy || run.running) return;
    run.run();
  };
  const cellOf = (r: Row, i: number) => {
    const c = r.cells[i];
    return c ? { cell: c, look: cellLook(t, fmt, c, r.nodeStatus) } : null;
  };
  const checkOf = (r: Row): CountryCheck | undefined => country?.byNode.get(r.nodeId);

  return (
    <div className="sb-tab">
      <EditionNotice feature="health_checks" className="mb-4" />
      <Sec
        tone="mint"
        icon={P.eye}
        title={t("hl.checks.title")}
        className="hl-ck"
        action={
          run.can && (
            <Button variant="secondary" size="md" className={cx("btn sm", walk.busy && "busy")} aria-disabled={walk.busy || run.running} onClick={go}>
              {walk.busy && <span className="an-sp" aria-hidden="true" />}
              {walk.busy ? t("hlp.ck.busy") : t("hl.checks.now")}
            </Button>
          )
        }
      >
        {!noChecks && (
          <p className="sb-lead">
            {t("hl.checks.how", { min: Math.max(1, Math.round(data.intervalS / 60)) })}
            {walk.busy ? ` · ${t("hl.checks.checkedAgo", { ago: t("hlp.ck.running") })}` : newest > 0 ? ` · ${t("hl.checks.checkedAgo", { ago: fmt.ago(newest) })}` : ""}
          </p>
        )}
        <UdpLossNotice data={data} />
        <div className="hl-ct">
          <div className="hl-cs" style={{ "--n": n } as React.CSSProperties}>
            <div className="hl-cr hl-ch">
              <span>{t("hl.checks.node")}</span>
              {cols.map((c) => (
                <span key={c.profileId} title={c.profileName}>
                  {c.profileName}
                </span>
              ))}
              {country && <span>{countryTitle}</span>}
              <span />
            </div>
            {data.rows.map((r) => {
              const isOpen = open === r.nodeId;
              // the first round has not come: every row waits in the queue (the board's "first round")
              const ph = first ? "wait" : walk.phase(r.nodeId);
              const check = checkOf(r);
              const clook = country && check ? countryLook(t, fmt, check) : null;
              const cview = countryView(clook);
              return (
                <div key={r.nodeId} className={cx("hl-cw", isOpen && "open", ph === "run" && "cking", ph === "wait" && "ckw", !ph && walk.flash && "ckn")}>
                  <button type="button" className="hl-cr" aria-expanded={isOpen} onClick={() => setChosen(isOpen ? "" : r.nodeId)}>
                    <span className="hl-cnode">
                      <Flag code={r.countryCode ?? ""} size={13} />
                      <b className="mono">{r.nodeName}</b>
                    </span>
                    {cols.map((col, i) => {
                      const v = cellOf(r, i);
                      if (!v) return <span key={col.profileId} className="hl-cv dim" data-l={col.profileName} />;
                      const view = cellView(v.look);
                      if (ph === "run") return <Cell key={col.profileId} label={col.profileName} dot="busy" tone="" text={t("hlp.ck.checking")} />;
                      if (ph === "wait" && (!first || v.cell.deployed)) return <Cell key={col.profileId} label={col.profileName} dot="none" tone="" text={t("hlp.ck.queued")} />;
                      return <Cell key={col.profileId} label={col.profileName} dot={view.dot} tone={view.tone} text={bare(v.look.label)} slow={v.look.slow} tip={v.look.tip.join("\n")} />;
                    })}
                    {country &&
                      (ph === "run" ? (
                        <Cell label={countryTitle} dot="busy" tone="" text={t("hlp.ck.checking")} />
                      ) : ph === "wait" ? (
                        <Cell label={countryTitle} dot="none" tone="" text={t("hlp.ck.queued")} />
                      ) : (
                        <Cell label={countryTitle} dot={cview.dot} tone={cview.tone} text={cview.text} tip={clook?.head} />
                      ))}
                    <Gl d={P.chev} className="sb-chev" />
                  </button>
                  {isOpen && (
                    <div className="hl-cd">
                      {cols.map((col, i) => {
                        const v = cellOf(r, i);
                        return <ProfileBox key={col.profileId} name={col.profileName} cell={v?.cell} look={v?.look ?? null} />;
                      })}
                      {country && <CountryBox title={countryTitle} look={clook} />}
                      <div className="hl-cda">
                        <Link to="/nodes/$id" params={{ id: r.nodeId }} className={cx(buttonClass("secondary", "md"), "btn sm")}>
                          {t("hl.alerts.openNode")}
                        </Link>
                        <NodeRun nodeId={r.nodeId} />
                      </div>
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </div>
        <TspuRunLine data={data} />
      </Sec>
      <CheckLegend slow={slow} defaultOpen={init?.legend} />
    </div>
  );
}
