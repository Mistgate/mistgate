import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { Notice } from "@/components/ui/notice";
import { Button } from "@/components/ui/button";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { msText } from "@/lib/health";
import { agentError, checkReason, enableGate, relayState, statePill, unverifiable, type RelayMsg, type RelayState, type Stage } from "@/lib/relay";
import { modeOf } from "@/lib/relay-bridge";
import { useNow } from "@/lib/time";
import { ChangeWay } from "./relay-way";
import { ChangeRelayPort, RemoveRelay, useCountryOf, type RelayRuns } from "./relays";

// The servers of a relay pair (the board's .nq-sv rows): the profile and its port on the entry, its state, a menu of what to do with it,
// the reason when it does not work, and on a click the line from people through the entry to the exit. The way of the whole pair and
// what was measured are above them (relay-pair.tsx).

type Busy = "check" | "on" | "off";
const toneOf: Record<RelayState, string> = { on: "ok", off: "idle", failed: "crit", applying: "idle", checking: "idle" };

/** The hop of the line as a word: "passes" in the accent, "fails" in red, nothing known in grey. */
function hop(t: ReturnType<typeof useT>, s: Stage | undefined, ok: string, bad: string, ago: (u: number) => string): { cls: "" | "crit" | "idle"; word: string; note: string } {
  if (s?.status === "ok") return { cls: "", word: ok, note: s.atUnix > 0 ? ago(s.atUnix) : "" };
  if (s?.status === "fail") return { cls: "crit", word: bad, note: s.atUnix > 0 ? ago(s.atUnix) : "" };
  return { cls: "idle", word: t("wy.hop.nodata"), note: "" };
}

type RowProps = {
  r: RelayMsg;
  busy?: Busy;
  note?: string;
  nowS: number;
  /** The country codes of the people, the entry and the exit, for the flags of the line. */
  people: string;
  ccEntry: string;
  ccExit: string;
  onCheck: () => void;
  onOn: () => void;
  onOff: () => void;
  onRemove: () => void;
  onPort?: () => void;
  onWay?: () => void;
  /** "Another entry…" under a failed server (the way's wizard); undefined when the owner cannot or there is none. */
  onOther?: () => void;
  /** A reader: the state and its words, none of the buttons. */
  readOnly?: boolean;
  /** The line open to begin with (the dev kit). */
  open?: boolean;
};

/** One server: what it passes on, its state, the menu, why it fails, the line. */
export function RelayRow({ r, busy, note, nowS, people, ccEntry, ccExit, onCheck, onOn, onOff, onRemove, onPort, onWay, onOther, readOnly, open: start }: RowProps) {
  const t = useT();
  const fmt = useFmt();
  const [open, setOpen] = useState(!!start);
  const [menu, setMenu] = useState(false);
  const state: RelayState = busy === "check" || busy === "on" ? "checking" : relayState(r);
  const gate = enableGate(r, nowS);
  const reason = checkReason(t, r);
  const ago = (u: number) => fmt.ago(u, nowS * 1000);
  const mode = modeOf(r);
  const reverse = r.enabled && r.activeMode === "reverse";
  const port = [t("nw.sv.port", { net: r.network || "tcp", port: r.port, entry: r.viaNodeName }), mode === "reverse" ? t("nw.mode.reverse") : mode === "dnat" ? t("nw.mode.dnat") : ""].filter(Boolean);
  const failed = state === "failed";
  const forwardDrop = failed && r.enabled && r.lastError === "forward_drop";
  const agent = failed && r.enabled && r.state === InboundState.FAILED && !forwardDrop ? agentError(t, r.lastError, { entry: r.viaNodeName, exit: r.exitNodeName }) : "";
  const since = r.check && !r.check.ok ? r.check.atUnix : r.appliedUnix;
  const why = forwardDrop ? t("relay.err.forward_drop") : failed ? (reason ? t("relay.line.down", { time: since ? fmt.stamp(since) : "—", reason }) : agent) : "";
  // the line under the name: how the client sees it, or why it is off
  let line = "";
  if (busy === "check" || busy === "on") line = busy === "on" ? t("relay.progress.path", { entry: r.viaNodeName, exit: r.exitNodeName }) : t("relay.progress.leg", { entry: r.viaNodeName, exit: r.exitNodeName });
  else if (!failed && state === "on" && r.monitor?.ok) line = t("nw.sv.client", { ms: msText(t, fmt, r.monitor.responseMs), ago: ago(r.monitor.atUnix) });
  else if (!failed && state === "on" && r.monitor && !r.monitor.ok) line = t("relay.mon.fail", { why: t(`relay.mon.stage.${["leg", "entry", "path", "bridge", "freeze"].includes(r.monitor.stage) ? r.monitor.stage : "path"}` as "relay.mon.stage.path", { entry: r.viaNodeName, exit: r.exitNodeName }), ago: ago(r.monitor.atUnix) });
  else if (state === "on" && r.check && unverifiable(r.check.leg)) line = t("relay.line.onUnverified", { reason: t("relay.why.unverifiable") });
  else if (state === "off") {
    const bridgeOnly = !gate && r.check?.leg?.status !== "ok" && r.check?.reverseReach?.status === "ok";
    line = gate
      ? t(`relay.gate.${gate}`, { exit: r.exitNodeName })
      : bridgeOnly
        ? t("relay.line.offReadyBridge", { exit: r.exitNodeName, entry: r.viaNodeName, ago: ago(r.check!.reverseReach!.atUnix) })
        : unverifiable(r.check?.leg)
          ? t("relay.line.offUnverified", { reason: t("relay.why.unverifiable") })
          : r.check?.leg
            ? t("relay.line.offReady", { ago: ago(r.check.leg.atUnix) })
            : "";
  }
  const h1 = hop(t, r.check?.path, t("wy.hop.opens"), t("wy.hop.shut"), ago);
  const h2 = hop(t, r.check?.leg, t("wy.hop.passes"), t("wy.hop.fails"), ago);
  const chk = r.check?.leg?.status === "fail" && reverse ? { ...h2, cls: "" as const, word: t("wy.hop.bridge") } : h2;
  const menuRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!menu) return;
    const off = (e: KeyboardEvent) => e.key === "Escape" && setMenu(false);
    document.addEventListener("keydown", off);
    menuRef.current?.querySelector<HTMLElement>("button:not([disabled])")?.focus();
    return () => document.removeEventListener("keydown", off);
  }, [menu]);
  const pick = (f: (() => void) | undefined) => () => {
    setMenu(false);
    f?.();
  };

  return (
    <li data-testid="relay-row" data-state={state} className={cx("wy-sv", !r.enabled && "off", busy && "now")}>
      <div className="wy-s1">
        <span className="tg flex justify-center">
          <ProtocolTag protocol={r.protocol} compact />
        </span>
        <button type="button" className="wy-nm" aria-expanded={open} onClick={() => setOpen((x) => !x)}>
          <b>{r.profileName}</b>
          <span className="mono">{port.join(" · ")}</span>
        </button>
        <span className={cx("wy-gs", toneOf[state])}>
          <i className={cx("wy-dot", toneOf[state] === "crit" ? "crit" : toneOf[state] === "idle" ? "idle" : "")} aria-hidden />
          {t(statePill[state === "checking" ? "checking" : state].word)}
        </span>
        {!readOnly ? (
          <div className="wy-mw">
            <button
              type="button"
              aria-label={t("wy.mn.aria", { name: r.profileName })}
              aria-haspopup="menu"
              aria-expanded={menu}
              onClick={() => setMenu((x) => !x)}
              className="grid size-8 place-items-center rounded-ctl text-sec transition-colors duration-150 ease-out hover:bg-step hover:text-fg aria-expanded:bg-step aria-expanded:text-fg"
            >
              <Icon name="ellipsis" size={18} />
            </button>
            {menu && (
              <>
                <div className="wy-mnb" onClick={() => setMenu(false)} />
                <div ref={menuRef} className="wy-mn" role="menu">
                  <button type="button" role="menuitem" disabled={!!busy} onClick={pick(onCheck)}>
                    {t("relay.check")}
                  </button>
                  {r.enabled ? (
                    <button type="button" role="menuitem" disabled={!!busy} onClick={pick(onOff)}>
                      {t("relay.off")}
                    </button>
                  ) : (
                    <button type="button" role="menuitem" disabled={!!busy || !!gate} title={gate ? t(`relay.gate.${gate}`, { exit: r.exitNodeName }) : undefined} onClick={pick(onOn)}>
                      {t("relay.on")}
                    </button>
                  )}
                  <button type="button" role="menuitem" onClick={pick(onPort)}>
                    {t("wy.mn.port")}
                  </button>
                  <button type="button" role="menuitem" onClick={pick(onWay)}>
                    {t("wy.mn.way")}
                  </button>
                  <button type="button" role="menuitem" className="risk" disabled={!!busy} onClick={pick(onRemove)}>
                    {t("wy.mn.remove")}
                  </button>
                </div>
              </>
            )}
          </div>
        ) : (
          <span />
        )}
      </div>
      {why && (
        <div data-testid="relay-down" className="wy-why">
          <span>
            {why}
            {agent && reason && !forwardDrop && <span className="block text-xs text-sec">{agent}</span>}
            {r.lastError && !forwardDrop && r.state === InboundState.FAILED && <span className="mt-0.5 block font-mono text-[11px] break-words text-muted">{r.lastError}</span>}
          </span>
          {!readOnly &&
            (forwardDrop ? (
              <Link to="/nodes/$id" params={{ id: r.viaNodeId }} search={{ tab: "doctor" }} className="inline-flex h-8 flex-none items-center rounded-ctl border border-line bg-step px-3 text-[13px] font-medium whitespace-nowrap hover:bg-(--step-hover)">
                {t("wy.fix.doctor")}
              </Link>
            ) : onOther ? (
              <Button size="md" onClick={onOther}>
                {t("nw.sv.other")}
              </Button>
            ) : null)}
        </div>
      )}
      {line && <em className="wy-s2">{line}</em>}
      {note && (
        <Notice tone="danger" className="mt-2 ms-10 items-start">
          {note}
        </Notice>
      )}
      {open && (
        <>
          <div className="wy-chain" data-testid="relay-chain">
            <span className="wy-cn">
              <Flag code={people} size={14} />
              <b>{t("wy.dir.people")}</b>
            </span>
            <span className={cx("wy-cl", h1.cls)}>
              <b>{h1.word}</b>
              <i aria-hidden />
              <em>{h1.note}</em>
            </span>
            <span className="wy-cn">
              <Flag code={ccEntry} size={14} />
              <span>
                <b className="mono">{r.viaNodeName}</b>
                <em className="mono">
                  {r.network || "tcp"}/{r.port}
                </em>
              </span>
            </span>
            <span className={cx("wy-cl", chk.cls)}>
              <b>{chk.word}</b>
              <i aria-hidden />
              <em>{chk.note}</em>
            </span>
            <span className="wy-cn">
              <Flag code={ccExit} size={14} />
              <span>
                <b className="mono">{r.exitNodeName}</b>
                <em className="mono">{r.toIp ? `${r.toIp}:${r.exitPort}` : `${r.network || "tcp"}/${r.exitPort || "—"}`}</em>
              </span>
            </span>
          </div>
          {r.countedUnix > 0 && <p className="wy-fl">{t("relay.bytes", { bytes: fmt.bytes(r.bytes), packets: fmt.num(r.packets, 0), ago: ago(r.countedUnix) })}</p>}
        </>
      )}
    </li>
  );
}

/** The servers of one pair. `people` is the country people look from; the owner's calls come with the runs. */
export function RelayRows({ list, runs, readOnly, people, onOther }: { list: readonly RelayMsg[]; runs: RelayRuns; readOnly?: boolean; people: string; onOther?: () => void }) {
  const t = useT();
  const ccOf = useCountryOf();
  const nowS = Math.floor(useNow(15_000) / 1000);
  const [removing, setRemoving] = useState<RelayMsg | null>(null);
  const [changing, setChanging] = useState<RelayMsg | null>(null);
  const [porting, setPorting] = useState<RelayMsg | null>(null);
  return (
    <>
      {/* the owner's calls: a reader (helper, observer) sees the state, with one line saying who changes it */}
      {readOnly && <Notice tone="quiet" className="mx-6 mb-2 max-[720px]:mx-4">{t("relay.way.readOnly")}</Notice>}
      <ul>
        {list.map((r) => (
          <RelayRow
            key={r.id}
            r={r}
            busy={runs.busy[r.id]}
            note={runs.notes[r.id]}
            nowS={nowS}
            people={people}
            ccEntry={ccOf(r.viaNodeId)}
            ccExit={ccOf(r.exitNodeId)}
            onCheck={() => runs.check(r)}
            onOn={() => runs.on(r)}
            onOff={() => runs.off(r)}
            onRemove={() => setRemoving(r)}
            onPort={() => setPorting(r)}
            onWay={() => setChanging(r)}
            onOther={onOther}
            readOnly={readOnly}
          />
        ))}
      </ul>
      {removing && <RemoveRelay r={removing} onClose={() => setRemoving(null)} />}
      {changing && <ChangeWay key={changing.id} r={changing} onClose={() => setChanging(null)} />}
      {porting && <ChangeRelayPort key={porting.id} r={porting} onClose={() => setPorting(null)} />}
    </>
  );
}
