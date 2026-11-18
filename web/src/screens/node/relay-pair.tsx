import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { relays as relaysApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { directOf, type Direct } from "@/lib/fleet-map";
import { useFmt } from "@/lib/format";
import { countryIn, msText, type CountryCheck } from "@/lib/health";
import { relayState, type RelayMsg } from "@/lib/relay";
import { activeOf, blockedOf, blockedText, bridgeErrorText, bridgeFailed, bridgeLoad, bridgeProblem, bridgeWaits, directStreakGoal, modeOf, reasonText, speedKind, speedOk, speedReason, type BridgeAction, type Mode } from "@/lib/relay-bridge";
import type { RelayRuns } from "./relays";
import { Spin } from "./parts";

// The way of a pair, "de1 through msk1" (the board's Способ and Замеры, .nq-sec): how the relays carry people (forwarding or a bridge), why,
// what the bridge holds and how full it is, what was measured and when, and the choice of the way for all of the pair's relays at once.
// Owner's, like every relay screen; a reader sees the state without the buttons. One server's own way is in its menu (relay-way.tsx).

const FW = "M5 12h14M13 6l6 6-6 6";
const BK = "M19 12H5M11 6l-6 6 6 6";
const LK = "M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1";

const svg = (d: string, className?: string) => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden className={cx("i", className)}>
    <path d={d} />
  </svg>
);

/** The mode a pair's relays stand in as one: all auto, all forwarding only, all bridge only, or a mix the owner set by hand. */
export function pairMode(rs: readonly RelayMsg[]): Mode | "custom" {
  const m = new Set(rs.map(modeOf));
  return m.size === 1 ? [...m][0]! : "custom";
}

type Tone = "ok" | "part" | "bad" | "none";
const dotOf: Record<Tone, string> = { ok: "", part: "warn", bad: "crit", none: "idle" };

export type PairWayProps = {
  rs: readonly RelayMsg[];
  entry: string;
  exit: string;
  /** The country people look from (its code), and the exit and the entry as that country sees them. */
  cc: string;
  exitSeen?: CountryCheck;
  entrySeen?: CountryCheck;
  runs: RelayRuns;
  readOnly?: boolean;
  nowS: number;
  /** "Another entry…": open the way's wizard for the exit; undefined when there is no other entry. */
  onOther?: () => void;
  /** The panel did not answer the last time the relays were read: the block says so and keeps nothing but a way to ask again. */
  readError?: boolean;
  onRetry?: () => void;
  /** What the dev kit starts the block with: the choice of the way open, a check done, the speed check running or done. */
  start?: { pick?: boolean; checked?: boolean; speed?: "busy" | "done" };
};

/** The two blocks of the pair, "Way" and "Measurements". */
export function PairWay(p: PairWayProps) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const { rs, entry, exit, runs } = p;
  const n = { entry, exit };
  const [pick, setPick] = useState(!!p.start?.pick);
  const [checked, setChecked] = useState(!!p.start?.checked);
  const [speed, setSpeed] = useState<"idle" | "busy" | "done">(p.start?.speed ?? "idle");
  const [error, setError] = useState<unknown>(null);

  const ago = (unix: number) => fmt.ago(unix, p.nowS * 1000);
  const mode = pairMode(rs);
  const bridge = rs.some((r) => r.enabled && activeOf(r) === "reverse");
  const lead = rs.find((r) => activeOf(r) === "reverse" && r.enabled) ?? rs[0]!;
  const since = Math.max(0, ...rs.filter((r) => r.enabled && activeOf(r) === "reverse").map((r) => r.modeChangedUnix));
  const b = lead.bridge;
  const load = bridge ? bridgeLoad(b) : null;
  const held = bridge && !!b?.connected;
  const dropped = bridge && rs.some((r) => bridgeFailed(r));
  const udpDown = rs.find((r) => blockedOf(r) === "udp" && relayState(r) === "failed");
  const reverseOk = rs.some((r) => r.check?.reverseReach?.status === "ok" && blockedOf(r) === "");
  const checking = rs.some((r) => runs.busy[r.id] !== undefined);
  const dead = rs.every((r) => relayState(r) === "failed") && !bridge && rs.some((r) => r.check?.leg?.status === "fail") && !reverseOk && rs.some((r) => r.check?.reverseReach?.status === "fail");
  const askSpeed = !p.readOnly ? rs.find((r) => blockedOf(r) === "") : undefined;
  const note = rs.map((r) => runs.notes[r.id]).find(Boolean);

  const done = async () => void (await qc.invalidateQueries({ queryKey: ["relays"] }));
  const check = async () => {
    setChecked(false);
    await runs.checkAll(rs);
    setChecked(true);
  };
  const runSpeed = async () => {
    if (!askSpeed || speed === "busy") return;
    setSpeed("busy");
    setError(null);
    try {
      await relaysApi.checkRelay({ id: askSpeed.id, reverseSpeed: true });
      setSpeed("done");
    } catch (e) {
      setError(e);
      setSpeed("idle");
    } finally {
      await done();
    }
  };
  const setMode = async (m: Mode) => {
    setPick(false);
    setError(null);
    try {
      for (const r of rs) if (m !== "reverse" || blockedOf(r) === "") await relaysApi.updateRelay({ id: r.id, mode: m });
      toast(t("relay.way.saved"));
    } catch (e) {
      setError(e);
    } finally {
      await done();
    }
  };

  // ---- Way: the lines under the sentence
  const reasonLeg = lead.check?.leg?.atUnix ? fmt.stamp(lead.check.leg.atUnix) : "";
  const why = !bridge
    ? mode === "dnat"
      ? t("nw.why.manual.fwd")
      : ""
    : mode === "reverse"
      ? t("nw.why.manual.bridge")
      : lead.modeReason === "leg_failed"
        ? reasonLeg
          ? t("nw.why.leg.at", { ...n, time: reasonLeg })
          : t("nw.why.leg", n)
        : reasonText(t, lead);
  const bridgeText = [
    b && b.pool > 0 ? t("relay.bridge.conns", { conns: b.conns, pool: b.pool }) : b ? t.n("relay.bridge.connsNoPool", b.conns) : "",
    b && b.rttMs > 0 ? msText(t, fmt, b.rttMs) : "",
    b && b.sinceUnix > 0 ? t("relay.bridge.since", { time: fmt.duration(Math.max(60, p.nowS - b.sinceUnix)) }) : "",
  ]
    .filter(Boolean)
    .join(" · ");
  const rows: { k: string; text: string; tone?: "ok" | "mut"; load?: boolean; updates?: boolean }[] = [];
  // a bridge the agents cannot do yet: which one to update
  const old = rs.find((r) => blockedOf(r) === "entry_agent_old" || blockedOf(r) === "exit_agent_old");
  if (old) rows.push({ k: t("nw.k.bridge"), tone: "mut", text: blockedText(t, blockedOf(old), n), updates: true });
  if (checked) rows.push({ k: t("nw.k.check"), text: t("nw.k.checked"), tone: "ok" });
  if (why) rows.push({ k: t("nw.k.why"), text: why });
  if (bridge && held && bridgeText) rows.push({ k: t("nw.k.bridge"), text: bridgeText });
  if (bridge && held && load) rows.push({ k: t("nw.k.load"), text: "", load: true });
  if (bridge && mode === "auto" && lead.check?.leg?.status === "fail" && !load?.atLimit)
    rows.push({ k: t("nw.k.fwd"), tone: "mut", text: t("nw.fwdStreak", { time: reasonLeg, k: Math.min(lead.directStreak, directStreakGoal), goal: directStreakGoal }) });
  if (!bridge && mode === "auto" && reverseOk && lead.check?.reverseReach) rows.push({ k: t("nw.k.bridge"), tone: "mut", text: t("nw.bridgeOk", { ...n, ago: ago(lead.check.reverseReach.atUnix) }) });

  // the notices that need a button
  type Note = { tone: "warn" | "crit" | "mut"; text: string; code?: string; btn?: string; go?: () => void; to?: { id: string; tab: "profiles" | "server" | "doctor" } };
  let plt: Note[] = [];
  const capital = (x: string) => x.charAt(0).toUpperCase() + x.slice(1);
  if (load?.atLimit) plt.push({ tone: "warn", text: t("nw.plt.limit"), btn: p.onOther ? t("nw.plt.other", { exit }) : undefined, go: p.onOther });
  const code = bridge ? (b?.error ?? "") : "";
  if (bridge && (dropped || bridgeWaits(code))) {
    // what the agent said, its tone and the one thing to do about it (the board's "Ошибка моста")
    const pr = bridgeProblem(code);
    const reason = code === "closed" ? t("relay.bridge.err.other") : bridgeErrorText(t, code, n) || t("relay.bridge.err.disconnected", n);
    const at: Record<BridgeAction, Pick<Note, "btn" | "go" | "to">> = {
      check: { btn: t("nw.plt.check", n), go: () => void check() },
      recheck: { btn: t("wy.br.recheck"), go: () => void check() },
      speed: askSpeed ? { btn: t("wy.br.speed"), go: () => void runSpeed() } : {},
      profilesEntry: { btn: t("wy.br.profiles", { node: entry }), to: { id: lead.viaNodeId, tab: "profiles" } },
      profilesExit: { btn: t("wy.br.profiles", { node: exit }), to: { id: lead.exitNodeId, tab: "profiles" } },
      server: { btn: t("wy.br.server"), to: { id: lead.exitNodeId, tab: "server" } },
      doctorEntry: { btn: t("wy.br.doctor", { node: entry }), to: { id: lead.viaNodeId, tab: "doctor" } },
    };
    plt.push({ tone: pr.tone, text: pr.head ? t("nw.plt.down", { reason }) : pr.capital ? capital(reason) : reason, code, ...(pr.action ? at[pr.action] : {}) });
  }
  if (udpDown) plt.push({ tone: "warn", text: t("nw.plt.udp", { server: udpDown.profileName, ...n, port: udpDown.exitPort || udpDown.port, net: udpDown.network || "udp" }), btn: p.onOther ? t("nw.sv.other") : undefined, go: p.onOther });
  // the panel could not read the relays: no sentence about the bridge can be trusted, one line and a way to ask again
  if (p.readError) {
    rows.length = 0;
    plt = [{ tone: "warn", text: t("wy.br.read"), btn: p.onRetry ? t("wy.br.retry") : undefined, go: p.onRetry }];
  }

  // ---- Measurements
  const country = countryIn(t, p.cc);
  const own: Direct = directOf(p.exitSeen);
  const seen: Direct = directOf(p.entrySeen);
  const meas: { tone: Tone; k: string; text: string; at: string }[] = [];
  if (p.exitSeen)
    meas.push({
      tone: own.tone,
      k: t("nw.ck.direct", { country }),
      text: own.tone === "part" ? t("nw.ck.d.part", { exit, networks: own.dark.join(", ") }) : own.tone === "bad" ? t("nw.ck.d.bad", { exit }) : own.tone === "ok" ? t("nw.allNets") : t("nw.nodata"),
      at: own.atUnix ? ago(own.atUnix) : "",
    });
  if (p.entrySeen)
    meas.push({
      tone: seen.tone,
      k: t("nw.ck.seen", { entry, country }),
      text: seen.tone === "ok" ? t("nw.allNets") : seen.tone === "part" ? seen.dark.join(", ") : seen.tone === "bad" ? t("nw.entry.hidden", { country }) : t("nw.nodata"),
      at: seen.atUnix ? ago(seen.atUnix) : "",
    });
  const newest = (read: (r: RelayMsg) => { atUnix: number } | undefined) =>
    rs
      .map((r) => ({ r, s: read(r) }))
      .filter((x) => x.s && x.s.atUnix > 0)
      .sort((a, c) => c.s!.atUnix - a.s!.atUnix)[0];
  const leg = newest((r) => r.check?.leg);
  if (leg) {
    const s = leg.r.check!.leg!;
    meas.push({ tone: s.status === "ok" ? "ok" : s.status === "fail" ? "bad" : "none", k: `${entry} → ${exit}`, text: s.status === "ok" ? t("nw.ck.pass") : s.status === "fail" ? (bridge ? t("nw.ck.failBridge") : t("nw.ck.fail")) : t("nw.ck.skipped"), at: ago(s.atUnix) });
  }
  const reach = newest((r) => r.check?.reverseReach);
  if (reach) {
    const s = reach.r.check!.reverseReach!;
    meas.push({ tone: s.status === "ok" ? "ok" : s.status === "fail" ? "bad" : "none", k: `${exit} → ${entry}`, text: s.status === "ok" ? t("nw.ck.pass") : s.status === "fail" ? t("nw.ck.fail") : t("nw.ck.skipped"), at: ago(s.atUnix) });
  }
  const sp = newest((r) => r.check?.reverseSpeed);
  if (sp) {
    const s = sp.r.check!.reverseSpeed!;
    meas.push({ tone: speedKind(s) === "ok" ? "ok" : speedKind(s) === "bad" ? "bad" : "none", k: t("relay.stage.reverseSpeed", n), text: s.status === "ok" ? speedOk(t, fmt, s) : speedReason(t, s, n), at: ago(s.atUnix) });
  }
  const mon = newest((r) => r.monitor);
  if (mon) {
    const m = mon.r.monitor!;
    meas.push({ tone: m.ok ? "ok" : "bad", k: t("nw.ck.client", { entry }), text: m.ok ? t("nw.ck.clientOk", { ms: m.responseMs, country }) : t("nw.ck.fail"), at: ago(m.atUnix) });
  }

  const head = dead ? t("nw.p.bad") : bridge ? t("nw.m.bridge") : t("nw.m.fwd");
  const how = dead ? t("wy.m.how.none", n) : bridge ? t("nw.m.how.bridge", n) : t("nw.m.how.fwd", n);
  const m1 = [mode === "auto" ? t("nw.m.auto") : t("nw.m.manual"), bridge && since > 0 ? t("nw.m.since", { time: fmt.stamp(since) }) : ""].filter(Boolean).join(" · ");
  const modeWord = t(`nw.mode.${mode === "custom" ? "custom" : mode}` as const);

  return (
    <>
      <div className="wy-sec" data-testid="pair-way">
        <span className="wy-sl">{t("nw.s.mode")}</span>
        <div className="wy-m1">
          <span className={cx("wy-mchip", dead && "crit")}>
            {svg(bridge ? LK : FW)}
            {head}
          </span>
          {!dead && <em>{m1}</em>}
          {load?.atLimit && <span className="wy-mchip warn">{t("relay.bridge.atLimit")}</span>}
          <small className="wy-mh">{t("nw.m.hint")}</small>
          {!p.readOnly && (
            <Button size="md" disabled={checking} onClick={() => void check()}>
              {checking && <Spin />}
              {checking ? t("nw.m.checking") : t("nw.m.check")}
            </Button>
          )}
        </div>
        <div className={cx("wy-dir", dead ? "dead" : bridge ? "br" : "")}>
          <span className="dn">
            <Flag code={p.cc} size={14} />
            {t("wy.dir.people")}
          </span>
          {svg(FW)}
          <b className="mono">{entry}</b>
          {svg(bridge ? BK : FW, "da")}
          <b className="mono">{exit}</b>
          <p>{how}</p>
        </div>
        {rows.length > 0 && (
          <dl className="wy-kv">
            {rows.map((x) => (
              <div key={x.k} className={cx(x.tone === "mut" && "mut", x.tone === "ok" && "ok")}>
                <dt>{x.k}</dt>
                <dd>
                  {x.load && load ? (
                    <span className={cx("wy-occ", load.atLimit ? "lim" : load.nearFull ? "near" : "")}>
                      <span>
                        <b className="mono">{load.clients}</b> {t("nw.occ.of")} <span className="mono">~{load.capacity}</span>
                      </span>
                      <i aria-hidden className="wy-sg">
                        {Array.from({ length: 10 }, (_, i) => (
                          <u key={i} className={i < load.segments ? "on" : ""} />
                        ))}
                      </i>
                      {(load.nearFull || load.atLimit) && <em>{load.atLimit ? t("relay.bridge.atLimit") : t("nw.m.nearFull")}</em>}
                    </span>
                  ) : (
                    <span>
                      {x.text}
                      {x.updates && !p.readOnly && (
                        <>
                          {" "}
                          <Link to="/updates" className="wy-lk">
                            {t("relay.openUpdates")}
                          </Link>
                        </>
                      )}
                    </span>
                  )}
                </dd>
              </div>
            ))}
          </dl>
        )}
        {plt.map((l) => (
          <div key={l.text} className={cx("wy-plt", l.tone === "crit" && "crit", l.tone === "mut" && "mut")} data-tone={l.tone}>
            <div>
              <span>{l.text}</span>
              {l.code && <small className="mono">{l.code}</small>}
            </div>
            {l.btn && !p.readOnly && !l.to && (
              <Button size="md" onClick={l.go}>
                {l.btn}
              </Button>
            )}
            {l.btn && !p.readOnly && l.to && (
              <Link to="/nodes/$id" params={{ id: l.to.id }} search={{ tab: l.to.tab }} className={buttonClass("secondary", "md")}>
                {l.btn}
              </Link>
            )}
          </div>
        ))}
        {!p.readOnly && (
          <div className="wy-msl">
            <button type="button" className="wy-msb" aria-expanded={pick} onClick={() => setPick((x) => !x)}>
              {t("nw.mode.label", { mode: modeWord })}
              <Icon name="chevronDown" size={14} />
            </button>
            {pick && (
              <div className="flex flex-col gap-2">
                <div role="group" aria-label={t("nw.s.mode")} className="flex w-fit max-w-full gap-0.5 rounded-field border border-line bg-canvas p-0.5 max-[720px]:w-full max-[720px]:flex-col">
                  {(["auto", "dnat", "reverse"] as const).map((m) => (
                    <button
                      key={m}
                      type="button"
                      aria-pressed={mode === m}
                      onClick={() => void setMode(m)}
                      className={cx("h-7 rounded-ctl px-3 text-[13px] transition-colors duration-150 ease-out hover:text-fg", mode === m ? "bg-surface-2 text-fg" : "text-sec")}
                    >
                      {t(`nw.mode.opt.${m}` as const)}
                    </button>
                  ))}
                </div>
                <small className="text-xs text-muted">{t("nw.mode.drop")}</small>
                <small className="text-xs text-muted">{t("nw.mode.udp")}</small>
              </div>
            )}
          </div>
        )}
      </div>
      <div className="wy-sec" data-testid="pair-checks">
        <span className="wy-sl">{t("nw.s.checks")}</span>
        {meas.map((c) => (
          <p key={c.k} className={cx("wy-mr", c.tone === "none" && "mut")}>
            <i className={cx("wy-dot", dotOf[c.tone])} aria-hidden />
            <span>
              <b className="font-medium">{c.k}:</b> {c.text}
            </span>
            {c.at && <em>{c.at}</em>}
          </p>
        ))}
        {askSpeed && (
          <div className="wy-sp">
            <Button size="md" className={cx(speed === "busy" && "text-tone-sky")} aria-busy={speed === "busy"} onClick={() => void runSpeed()}>
              {speed === "busy" && <Spin />}
              {speed === "busy" ? t("nw.sp.busy", n) : t("nw.sp.run")}
            </Button>
            <small>{speed === "done" ? t("nw.sp.done", n) : t("nw.sp.hint", n)}</small>
          </div>
        )}
        {(error != null || note) && (
          <p role="alert" className="text-danger-text!">
            {error != null ? errorText(error, t) : note}
          </p>
        )}
      </div>
    </>
  );
}
