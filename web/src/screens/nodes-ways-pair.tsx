import { useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { StatusDot, StatusPill, type StatusKind } from "@/components/ui/status";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { useToast } from "@/components/ui/toast";
import { useT, type T } from "@/i18n";
import { relays as relaysApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import type { Direct, NodeWays, WayPair, WayTone } from "@/lib/fleet-map";
import { useFmt } from "@/lib/format";
import { countryIn, msText } from "@/lib/health";
import { agentError, checkReason, viaLabel, type RelayMsg } from "@/lib/relay";
import { activeOf, blockedOf, bridgeErrorText, bridgeFailed, bridgeLoad, directStreakGoal, modeOf, reasonText, speedKind, speedOk, speedReason, type Mode } from "@/lib/relay-bridge";
import { relayState } from "@/lib/relay";
import { useRelayRuns } from "./node/relays";
import { WayGlyph } from "./nodes-ways-parts";

// The pair's sheet of the Ways view (the board's rw-dr): an exit through an entry, how its relays work and why, what was
// measured, the servers, and the few things to do with the pair. One panel at the end of a wide table, a sheet over the foot of a
// narrow one. The owner's, like every relay screen.

/** What the sheet starts with, for the dev kit: the choice of the way open, the removal asked, a speed check done, a check done. */
export type PairStart = { pick?: boolean; asking?: boolean; speed?: "busy" | "done"; checked?: boolean };

export type PairSheetProps = {
  start?: PairStart;
  exit: NodeWays;
  pair: WayPair;
  /** The country of the exit and of the entry, for the "DE · via RU" a person is shown. */
  exitCc: string;
  entryCc: string;
  entryDirect: Direct;
  country: string;
  nowS: number;
  /** Another entry the exit could go through, or undefined when there is none: the pair's own "Another entry…". */
  other: string | undefined;
  onClose: () => void;
  /** More servers of the exit through this entry; another entry for the exit (the entry id, or "" for the panel's choice). */
  onMore: () => void;
  onOther: () => void;
};

const hair = "border-t border-line";
const sec = `flex flex-col gap-1.5 px-5 py-3.5 ${hair}`;
const label = "flex items-center gap-2 text-[11px] leading-4 font-medium tracking-[.06em] text-muted uppercase";

/** The mode a pair's relays stand in as one: all auto, all direct only, all bridge only, or a mix the owner set by hand. */
function pairMode(rs: readonly RelayMsg[]): Mode | "custom" {
  const m = new Set(rs.map(modeOf));
  return m.size === 1 ? [...m][0]! : "custom";
}

export function PairSheet(p: PairSheetProps) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const runs = useRelayRuns();
  const { pair, exit } = p;
  const rs = pair.relays;
  const n = { entry: pair.entryName, exit: exit.name };
  const [pick, setPick] = useState(!!p.start?.pick);
  const [asking, setAsking] = useState(!!p.start?.asking);
  const [checked, setChecked] = useState(!!p.start?.checked);
  const [speed, setSpeed] = useState<"idle" | "busy" | "done">(p.start?.speed ?? "idle");
  const [error, setError] = useState<unknown>(null);
  // the parent keys the sheet by the pair: another pair starts with none of this one's state
  useEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === "Escape" && p.onClose();
    document.addEventListener("keydown", esc);
    return () => document.removeEventListener("keydown", esc);
  });

  const ago = (unix: number) => fmt.ago(unix, p.nowS * 1000);
  const bridge = pair.bridge > 0;
  const mode = pairMode(rs);
  const lead = rs.find((r) => activeOf(r) === "reverse" && r.enabled) ?? rs[0]!;
  const since = Math.max(0, ...rs.filter((r) => r.enabled && activeOf(r) === "reverse").map((r) => r.modeChangedUnix));
  const b = lead.bridge;
  const load = bridge ? bridgeLoad(b) : null;
  const held = bridge && !!b?.connected;
  const dropped = bridge && rs.some((r) => bridgeFailed(r));
  const udpDown = rs.find((r) => blockedOf(r) === "udp" && relayState(r) === "failed");
  const reverseOk = rs.some((r) => r.check?.reverseReach?.status === "ok" && blockedOf(r) === "");
  const pill = ({ ok: ["ok", t("nw.p.ok")], warn: ["warn", t("nw.p.part")], bad: ["bad", t("nw.p.bad")], busy: ["busy", t("relay.state.applying")], off: ["off", t("relay.state.off")] } as Record<StatusKind, [StatusKind, string]>)[pair.state] ?? ["off", ""];

  // ---- the calls: one per relay of the pair, one after another (the panel runs one check per exit at a time)
  const done = async () => void (await qc.invalidateQueries({ queryKey: ["relays"] }));
  const check = async () => {
    setChecked(false);
    await runs.checkAll(rs);
    setChecked(true);
  };
  const askSpeed = rs.find((r) => blockedOf(r) === "");
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
  const remove = async () => {
    setError(null);
    try {
      for (const r of rs) await relaysApi.deleteRelay({ id: r.id });
      toast(t("relay.removed"));
      p.onClose();
    } catch (e) {
      setError(e);
    } finally {
      await done();
    }
  };

  // ---- the lines of "Way"
  const reasonLeg = lead.check?.leg?.atUnix ? fmt.stamp(lead.check.leg.atUnix) : "";
  const why = !bridge
    ? mode === "dnat"
      ? t("nw.why.manual.fwd")
      : ""
    : mode === "reverse"
      ? t("nw.why.manual.bridge")
      : lead.modeReason === "leg_failed"
        ? (reasonLeg ? t("nw.why.leg.at", { ...n, time: reasonLeg }) : t("nw.why.leg", n))
        : reasonText(t, lead);
  const bridgeText = [
    b && b.pool > 0 ? t("relay.bridge.conns", { conns: b.conns, pool: b.pool }) : b ? t.n("relay.bridge.connsNoPool", b.conns) : "",
    b && b.rttMs > 0 ? msText(t, fmt, b.rttMs) : "",
    b && b.sinceUnix > 0 ? t("relay.bridge.since", { time: fmt.duration(Math.max(60, p.nowS - b.sinceUnix)) }) : "",
  ]
    .filter(Boolean)
    .join(" · ");
  const rows: { k: string; text: string; tone?: "ok" | "mut"; load?: boolean }[] = [];
  if (checked) rows.push({ k: t("nw.k.check"), text: t("nw.k.checked"), tone: "ok" });
  if (why) rows.push({ k: t("nw.k.why"), text: why });
  if (bridge && held && bridgeText) rows.push({ k: t("nw.k.bridge"), text: bridgeText });
  if (bridge && held && load) rows.push({ k: t("nw.k.load"), text: "", load: true });
  if (bridge && mode === "auto" && lead.check?.leg?.status === "fail" && !load?.atLimit)
    rows.push({ k: t("nw.k.fwd"), tone: "mut", text: t("nw.fwdStreak", { time: reasonLeg, k: Math.min(lead.directStreak, directStreakGoal), goal: directStreakGoal }) });
  if (!bridge && mode === "auto" && reverseOk && lead.check?.reverseReach) rows.push({ k: t("nw.k.bridge"), tone: "mut", text: t("nw.bridgeOk", { ...n, ago: ago(lead.check.reverseReach.atUnix) }) });

  // the notices that need a button
  const plt: { tone: "warn" | "bad" | "mut"; text: string; code?: string; btn?: string; go?: () => void }[] = [];
  if (load?.atLimit) plt.push({ tone: "warn", text: t("nw.plt.limit"), btn: p.other ? t("nw.plt.other", { exit: exit.name }) : undefined, go: p.onOther });
  if (dropped) {
    const code = b?.error ?? "";
    plt.push({ tone: "bad", text: t("nw.plt.down", { reason: bridgeErrorText(t, code, n) || t("relay.bridge.err.disconnected", n) }), code, btn: t("nw.plt.check", n), go: () => void check() });
  }
  if (udpDown) plt.push({ tone: "warn", text: t("nw.plt.udp", { server: udpDown.profileName, ...n, port: udpDown.exitPort || udpDown.port, net: udpDown.network || "udp" }), btn: p.other ? t("nw.sv.other") : undefined, go: p.onOther });

  // ---- what was measured
  const checksRows: { tone: WayTone; k: string; text: string; at: string }[] = [];
  const own = p.exit.direct;
  checksRows.push({
    tone: own.tone,
    k: t("nw.ck.direct", { country: countryIn(t, p.country) }),
    text: own.tone === "part" ? t("nw.ck.d.part", { exit: exit.name, networks: own.dark.join(", ") }) : own.tone === "bad" ? t("nw.ck.d.bad", { exit: exit.name }) : own.tone === "ok" ? t("nw.allNets") : t("nw.nodata"),
    at: own.atUnix ? ago(own.atUnix) : "",
  });
  checksRows.push({
    tone: p.entryDirect.tone,
    k: t("nw.ck.seen", { entry: pair.entryName, country: countryIn(t, p.country) }),
    text: p.entryDirect.tone === "ok" ? t("nw.allNets") : p.entryDirect.tone === "part" ? p.entryDirect.dark.join(", ") : p.entryDirect.tone === "bad" ? t("nw.entry.hidden", { country: countryIn(t, p.country) }) : t("nw.nodata"),
    at: p.entryDirect.atUnix ? ago(p.entryDirect.atUnix) : "",
  });
  const newest = (read: (r: RelayMsg) => { atUnix: number } | undefined) => rs.map((r) => ({ r, s: read(r) })).filter((x) => x.s && x.s.atUnix > 0).sort((a, c) => c.s!.atUnix - a.s!.atUnix)[0];
  const leg = newest((r) => r.check?.leg);
  if (leg) {
    const ok = leg.r.check!.leg!.status === "ok";
    checksRows.push({ tone: ok ? "ok" : leg.r.check!.leg!.status === "fail" ? "bad" : "none", k: `${pair.entryName} → ${exit.name}`, text: ok ? t("nw.ck.pass") : leg.r.check!.leg!.status === "fail" ? (bridge ? t("nw.ck.failBridge") : t("nw.ck.fail")) : t("nw.ck.skipped"), at: ago(leg.r.check!.leg!.atUnix) });
  }
  const reach = newest((r) => r.check?.reverseReach);
  if (reach) {
    const s = reach.r.check!.reverseReach!;
    checksRows.push({ tone: s.status === "ok" ? "ok" : s.status === "fail" ? "bad" : "none", k: `${exit.name} → ${pair.entryName}`, text: s.status === "ok" ? t("nw.ck.pass") : s.status === "fail" ? t("nw.ck.fail") : t("nw.ck.skipped"), at: ago(s.atUnix) });
  }
  const sp = newest((r) => r.check?.reverseSpeed);
  if (sp) {
    const s = sp.r.check!.reverseSpeed!;
    checksRows.push({ tone: speedKind(s) === "ok" ? "ok" : speedKind(s) === "bad" ? "bad" : "none", k: t("relay.stage.reverseSpeed", n), text: s.status === "ok" ? speedOk(t, fmt, s) : speedReason(t, s, n), at: ago(s.atUnix) });
  }
  const mon = newest((r) => r.monitor);
  if (mon) {
    const m = mon.r.monitor!;
    checksRows.push({ tone: m.ok ? "ok" : "bad", k: t("nw.ck.client", { entry: pair.entryName }), text: m.ok ? t("nw.ck.clientOk", { ms: m.responseMs, country: countryIn(t, p.country) }) : t("nw.ck.fail"), at: ago(m.atUnix) });
  }

  // the last way of the exit: it opens nowhere else
  const last = exit.direct.tone !== "ok" && !exit.pairs.some((x) => x.entryId !== pair.entryId);
  const hasAwg = rs.some((r) => r.protocol === "amneziawg");
  const checking = rs.some((r) => runs.busy[r.id] !== undefined);

  return (
    <aside aria-label={t("nw.d.aria")} className="rw-dr flex flex-col rounded-card border border-line">
      <header className="grid grid-cols-[minmax(0,1fr)_auto_32px] items-center gap-2 py-3.5 ps-5 pe-3">
        <div>
          <b className="flex items-center gap-2 text-[15px] font-medium">
            <Flag code={p.exitCc} size={14} />
            <span className="font-mono font-medium tabular-nums">{exit.name}</span>
            {t("nw.t.via")}
            <span className="font-mono font-medium tabular-nums">{pair.entryName}</span>
          </b>
        </div>
        <StatusPill sm kind={pill[0]} label={pill[1]} />
        <button type="button" aria-label={t("common.close")} onClick={p.onClose} className="grid size-8 place-items-center rounded-ctl text-sec transition-colors duration-150 ease-out hover:bg-step hover:text-fg">
          <Icon name="x" size={16} />
        </button>
      </header>

      <div className={sec}>
        <span className={label}>{t("nw.s.mode")}</span>
        <div className="flex flex-wrap items-center gap-x-2 gap-y-2">
          <span className="inline-flex h-6 flex-none items-center gap-1 rounded-[8px] bg-step px-2 text-xs font-medium whitespace-nowrap text-sec">
            {bridge ? (
              <Icon name="link" size={14} />
            ) : (
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden className="block">
                <path d="M5 12h14M13 6l6 6-6 6" />
              </svg>
            )}
            {bridge ? t("nw.m.bridge") : t("nw.m.fwd")}
          </span>
          <em className="text-xs whitespace-nowrap text-muted not-italic">{[mode === "auto" ? t("nw.m.auto") : t("nw.m.manual"), bridge && since > 0 ? t("nw.m.since", { time: fmt.stamp(since) }) : ""].filter(Boolean).join(" · ")}</em>
          {load?.atLimit && <span className="inline-flex h-6 flex-none items-center rounded-[8px] bg-warn-soft px-2 text-xs font-medium whitespace-nowrap text-warn-text">{t("relay.bridge.atLimit")}</span>}
          <Button size="md" className="ms-auto" disabled={checking} onClick={() => void check()}>
            {checking ? t("nw.m.checking") : t("nw.m.check")}
          </Button>
        </div>
        <small className="block text-xs text-muted">{t("nw.m.hint")}</small>
        <p className="m-0 text-[13px] leading-[18px] text-sec">{bridge ? t("nw.m.how.bridge", n) : t("nw.m.how.fwd", n)}</p>
        {rows.length > 0 && (
          <dl className="m-0 mt-1 grid">
            {rows.map((x) => (
              <div key={x.k} className={cx("grid grid-cols-[64px_minmax(0,1fr)] items-baseline gap-3 py-1.5", "not-first:border-t not-first:border-line")}>
                <dt className="text-xs leading-[18px] text-muted">{x.k}</dt>
                <dd className={cx("m-0 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 text-[13px] leading-[18px]", x.tone === "ok" ? "text-ok-text" : x.tone === "mut" ? "text-sec" : "text-fg")}>
                  {x.load && load ? (
                    <span className="flex flex-1 flex-wrap items-center gap-x-3 gap-y-2 text-[13px] text-sec">
                      <span>
                        <b className="font-mono font-medium text-fg">{load.clients}</b> {t("nw.occ.of")} <span className="font-mono">~{load.capacity}</span>
                      </span>
                      <i aria-hidden className={cx("rw-sg", load.atLimit ? "lim" : load.nearFull ? "near" : "")}>
                        {Array.from({ length: 10 }, (_, i) => (
                          <u key={i} className={i < load.segments ? "on" : ""} />
                        ))}
                      </i>
                      {(load.nearFull || load.atLimit) && <em className="text-xs text-warn-text not-italic">{load.atLimit ? t("relay.bridge.atLimit") : t("nw.m.nearFull")}</em>}
                    </span>
                  ) : (
                    <span className="min-w-0 flex-[1_1_240px]">{x.text}</span>
                  )}
                </dd>
              </div>
            ))}
          </dl>
        )}
        {plt.map((l) => (
          <div key={l.text} className={cx("mt-1 flex flex-col items-start gap-3 rounded-[10px] px-3 py-2.5 text-[13px] leading-[18px]", l.tone === "bad" ? "bg-danger-soft" : "bg-warn-soft")}>
            <div className="flex min-w-0 flex-1 flex-col gap-0.5">
              <span>{l.text}</span>
              {l.code && <small className="font-mono text-xs text-muted">{l.code}</small>}
            </div>
            {l.btn && (
              <Button size="md" className="flex-none" onClick={l.go}>
                {l.btn}
              </Button>
            )}
          </div>
        ))}
        <button type="button" aria-expanded={pick} onClick={() => setPick((x) => !x)} className="mt-0.5 inline-flex items-center gap-1 self-start text-[13px] text-sec transition-colors duration-150 ease-out hover:text-fg">
          {t("nw.mode.label", { mode: t(({ auto: "nw.mode.auto", dnat: "nw.mode.dnat", reverse: "nw.mode.reverse", custom: "nw.mode.custom" } as const)[mode]) })}
          <Icon name="chevronDown" size={16} className={cx("transition-transform duration-[250ms] ease-out", pick && "rotate-180")} />
        </button>
        {pick && (
          <>
            <div role="group" aria-label={t("nw.s.mode")} className="flex w-full gap-0.5 rounded-field border border-line bg-canvas p-0.5">
              {(["auto", "dnat", "reverse"] as const).map((m) => (
                <button
                  key={m}
                  type="button"
                  aria-pressed={mode === m}
                  onClick={() => void setMode(m)}
                  className={cx("h-7 flex-1 rounded-ctl px-1 text-center text-xs transition-colors duration-150 ease-out hover:text-fg", mode === m ? "bg-surface-2 text-fg" : "text-sec")}
                >
                  {t(`nw.mode.opt.${m}` as const)}
                </button>
              ))}
            </div>
            <small className="text-xs text-muted">{t("nw.mode.drop")}</small>
            <small className="mt-1 block text-xs text-muted">{t("nw.mode.udp")}</small>
          </>
        )}
      </div>

      <div className={sec}>
        <span className={label}>{t("nw.s.checks")}</span>
        {checksRows.map((c) => (
          <div key={c.k} className="grid grid-cols-[14px_minmax(0,1fr)_auto] items-start gap-2 text-[13px] leading-[18px]">
            <span className="mt-[5px] flex">
              <WayGlyph kind={c.tone} />
            </span>
            <span className="text-sec">
              <b className="font-medium text-fg">{c.k}:</b> {c.text}
            </span>
            <em className="text-xs whitespace-nowrap text-muted not-italic">{c.at}</em>
          </div>
        ))}
        {askSpeed && (
          <div className="mt-1 flex flex-col items-start gap-1.5">
            <Button size="md" className={cx("w-full justify-center", speed === "busy" && "text-tone-sky")} onClick={() => void runSpeed()} aria-busy={speed === "busy"}>
              {speed === "busy" && <span aria-hidden className="size-3.5 animate-[mg-ui-spin_0.7s_linear_infinite] rounded-full border-[1.5px] border-current border-t-transparent" />}
              {speed === "busy" ? t("nw.sp.busy", n) : t("nw.sp.run")}
            </Button>
            <small className="text-xs text-muted">{speed === "done" ? t("nw.sp.done", n) : t("nw.sp.hint", n)}</small>
          </div>
        )}
        {error != null && <p role="alert" className="m-0 text-[13px] text-danger-text">{errorText(error, t)}</p>}
      </div>

      <div className={sec}>
        <span className={label}>{t("nw.s.servers")}</span>
        {rs.map((r, i) => (
          <ServerRow key={r.id} r={r} first={i === 0} entry={pair.entryName} exit={exit.name} nowS={p.nowS} other={p.other !== undefined ? p.onOther : undefined} t={t} />
        ))}
      </div>

      <div className={cx("flex flex-wrap items-center gap-x-3 gap-y-2 px-5 py-3.5", hair)}>
        <Button size="md" disabled={checking} onClick={() => void check()}>
          {t("nw.a.check")}
        </Button>
        <Button size="md" onClick={p.onMore}>
          {t("nw.a.more")}
        </Button>
        <Link to="/nodes/$id" params={{ id: pair.exitId }} search={{ tab: "ways", way: `via:${pair.entryId}` }} className="text-[13px] underline decoration-line underline-offset-4 hover:decoration-current">
          {t("nw.a.open", { node: exit.name })} ›
        </Link>
        <Link to="/nodes/$id" params={{ id: pair.entryId }} search={{ tab: "ways", way: `out:${pair.exitId}` }} className="text-[13px] underline decoration-line underline-offset-4 hover:decoration-current">
          {t("nw.a.open", { node: pair.entryName })} ›
        </Link>
      </div>
      {!asking ? (
        <button type="button" onClick={() => setAsking(true)} className="mx-5 mb-4 self-start text-[13px] text-muted transition-colors duration-150 ease-out hover:text-danger-text">
          {t("nw.a.remove", { entry: pair.entryName })}
        </button>
      ) : (
        <div className="mx-3 mb-3 flex animate-[mg-up_0.2s_var(--e)_both] flex-col gap-1.5 rounded-[12px] border border-[color-mix(in_srgb,var(--danger)_40%,transparent)] p-3">
          <b className="font-medium">{t.n("nw.del.t", rs.length, n)}</b>
          <p className="m-0 text-[13px] leading-[18px] text-sec">{t("nw.del.s", { label: `${p.exitCc.toUpperCase()} · ${viaLabel(t, p.entryCc)}`, exit: exit.name })}</p>
          {last && <p className="m-0 text-[13px] leading-[18px] text-danger-text">{t(exit.direct.tone === "part" ? "nw.del.last.some" : "nw.del.last.shut", { exit: exit.name, country: countryIn(t, p.country) })}</p>}
          {hasAwg && <p className="m-0 text-[13px] leading-[18px] text-warn-text">{t("nw.del.awg")}</p>}
          <div className="mt-1.5 flex justify-end gap-1.5">
            <Button size="md" onClick={() => setAsking(false)}>
              {t("common.cancel")}
            </Button>
            <Button size="md" variant="danger" onClick={() => void remove()}>
              {t("relay.remove")}
            </Button>
          </div>
        </div>
      )}
    </aside>
  );
}

/** One server of the pair: its name and port, its state, and for a failed one why and the way out. */
function ServerRow({ r, first, entry, exit, nowS, other, t }: { r: RelayMsg; first: boolean; entry: string; exit: string; nowS: number; other: (() => void) | undefined; t: T }) {
  const fmt = useFmt();
  const st = relayState(r);
  const reverse = activeOf(r) === "reverse" && r.enabled;
  const word = st === "on" ? t("nw.sv.on") + (reverse && blockedOf(r) === "" ? ` · ${t("nw.c.bridge")}` : "") : st === "failed" ? t("nw.p.bad") : st === "applying" ? t("relay.state.applying") : t("nw.c.off");
  const kind: StatusKind = st === "on" ? "ok" : st === "failed" ? "bad" : st === "applying" ? "busy" : "off";
  const reason = st === "failed" ? checkReason(t, r) || (r.state === InboundState.FAILED && r.lastError && r.lastError !== "forward_drop" ? agentError(t, r.lastError, { entry, exit }) : "") : "";
  const since = r.check && !r.check.ok ? r.check.atUnix : r.appliedUnix;
  const mon = r.monitor?.ok ? t("nw.sv.client", { ms: msText(t, fmt, r.monitor.responseMs), ago: fmt.ago(r.monitor.atUnix, nowS * 1000) }) : "";
  return (
    <div className={cx("grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-2.5 gap-y-1 py-1.5 text-[13px]", !first && "border-t border-line")}>
      <b className="font-medium">{r.profileName}</b>
      <span className="truncate font-mono text-xs text-muted">{t("nw.sv.port", { net: r.network || "tcp", port: r.port, entry })}</span>
      <span className={cx("inline-flex items-center gap-1.5 text-xs", st === "failed" ? "text-danger-text" : "text-sec")}>
        <StatusDot kind={kind} />
        {word}
      </span>
      {reason && (
        <div className="col-span-full flex flex-col items-start gap-2 rounded-[8px] bg-danger-soft px-2.5 py-2 text-xs leading-4">
          <span>{since ? t("nw.sv.why", { time: fmt.stamp(since), reason }) : reason}</span>
          {other && (
            <Button size="md" onClick={other}>
              {t("nw.sv.other")}
            </Button>
          )}
        </div>
      )}
      {!reason && first && mon && <em className="col-span-full text-xs text-muted not-italic">{mon}</em>}
    </div>
  );
}
