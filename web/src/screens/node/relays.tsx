import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { IconChip } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { StatusDot, StatusPill, type StatusKind } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { useT, type T } from "@/i18n";
import { relays as relaysApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorCode, errorText, errorVars } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { countryQuery } from "@/lib/globalping";
import { msText } from "@/lib/health";
import { plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import {
  agentError,
  candidatesQuery,
  checkReason,
  countryHint,
  enableGate,
  groupCandidates,
  namesOf,
  reachHint,
  relayState,
  relaysQuery,
  seenHint,
  stageReason,
  statePill,
  type Candidate,
  type CandidateNode,
  type CountryHint,
  type Hint,
  type RelayMsg,
  type RelayState,
  type Stage,
} from "@/lib/relay";
import { protocolName } from "@/lib/series";
import { useNow } from "@/lib/time";
import { settingsQuery } from "@/screens/subscriptions/queries";

// Relays on the node page (relay.proto; the cascade plan §8 screens 1-3), the pieces the "Ways in" tab (ways.tsx) is made
// of: a relay's row with its state, its two checks (the leg from the entry to the exit's server, the path from outside
// through the entry), the round the panel makes through it and what went through, and the switches; the "Add relay"
// dialog, also the last step of "New way in". RelayService is the owner's: nothing here renders, and nothing is asked,
// for a helper.

/** The country code of every node, for the small code chip in front of a name. */
export function useCountryOf(): (id: string) => string {
  const q = useQuery(nodesQuery);
  const by = new Map((q.data?.nodes ?? []).map((n) => [n.id, n.countryCode]));
  return (id) => by.get(id) ?? "";
}

/** The label of a twin in the subscriptions: "via RU", or "via a relay" for an entry with no country. */
const viaLabel = (t: T, cc: string) => (cc ? t("relay.via", { cc }) : t("relay.viaAny"));

export const ccChip = (cc: string) => (
  <span className="flex h-[22px] min-w-[30px] flex-none items-center justify-center rounded-[7px] bg-surface-2 px-[7px] font-mono text-[11px] font-bold text-muted">{cc || "—"}</span>
);

type Busy = "check" | "on" | "off";

/** The record without one key. */
function without<V>(rec: Record<string, V>, key: string): Record<string, V> {
  const out = { ...rec };
  delete out[key];
  return out;
}

/** What runs on the rows of a list: the calls, which row is busy with which, and the sentence a failed call left. */
export function useRelayRuns() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const ccOf = useCountryOf();
  const hours = useQuery(settingsQuery).data?.settings.updateIntervalHours || 12;
  const [busy, setBusy] = useState<Record<string, Busy>>({});
  const [notes, setNotes] = useState<Record<string, string>>({});

  const run = async (r: RelayMsg, kind: Busy, call: () => Promise<unknown>, done?: () => void, refresh = true) => {
    setBusy((b) => ({ ...b, [r.id]: kind }));
    setNotes((n) => without(n, r.id));
    try {
      await call();
      done?.();
    } catch (e) {
      // a failed check is the row's own story (the stored check names the stage): no second sentence for it
      if (errorCode(ConnectError.from(e).rawMessage) !== "relay_check_failed") setNotes((n) => ({ ...n, [r.id]: errorText(e, t) }));
    } finally {
      setBusy((b) => without(b, r.id));
      if (refresh) await qc.invalidateQueries({ queryKey: ["relays"] });
    }
  };
  const check = (r: RelayMsg, refresh = true) => run(r, "check", () => relaysApi.checkRelay({ id: r.id }), undefined, refresh);
  return {
    busy,
    notes,
    check: (r: RelayMsg) => void check(r),
    on: (r: RelayMsg) =>
      void run(r, "on", () => relaysApi.updateRelay({ id: r.id, enabled: true }), () => toast(t("relay.turnedOn", { via: viaLabel(t, ccOf(r.viaNodeId)), hours }), { ms: 8000 })),
    off: (r: RelayMsg) => void run(r, "off", () => relaysApi.updateRelay({ id: r.id, enabled: false }), () => toast(t("relay.turnedOff"))),
    /** One after another: the panel checks one relay per exit at a time anyway, and the rows answer in order. */
    checkAll: async (list: readonly RelayMsg[]) => {
      for (const r of list) await check(r, false);
      await qc.invalidateQueries({ queryKey: ["relays"] });
    },
    checking: Object.values(busy).length > 0,
  };
}
export type RelayRuns = ReturnType<typeof useRelayRuns>;

/**
 * The relays of one lane. On the exit's page (`side` "exit") each row leads with the profile, the entry being the lane; on
 * the entry's page ("entry") with the exit and its profile.
 */
export function RelayRows({ list, side, runs }: { list: readonly RelayMsg[]; side: "entry" | "exit"; runs: RelayRuns }) {
  const ccOf = useCountryOf();
  const nowS = Math.floor(useNow(15_000) / 1000);
  const [removing, setRemoving] = useState<RelayMsg | null>(null);
  return (
    <>
      <ul className="flex flex-col">
        {list.map((r) => (
          <RelayRow
            key={r.id}
            r={r}
            side={side}
            cc={ccOf(r.exitNodeId)}
            busy={runs.busy[r.id]}
            note={runs.notes[r.id]}
            nowS={nowS}
            onCheck={() => runs.check(r)}
            onOn={() => runs.on(r)}
            onOff={() => runs.off(r)}
            onRemove={() => setRemoving(r)}
          />
        ))}
      </ul>
      {removing && <RemoveRelay r={removing} onClose={() => setRemoving(null)} />}
    </>
  );
}

type RowProps = {
  r: RelayMsg;
  side: "entry" | "exit";
  cc: string;
  busy?: Busy;
  note?: string;
  nowS: number;
  onCheck: () => void;
  onOn: () => void;
  onOff: () => void;
  onRemove: () => void;
};

// the columns line up from row to row: the server, the port on the entry, where it goes, the state. Only from lg: at 720-1000
// px the list beside it leaves the detail too narrow for four columns, and an address would break in the middle
const gridClass = "lg:grid-cols-[minmax(0,1.6fr)_minmax(0,0.7fr)_minmax(0,1fr)_124px]";

/** One relay: what it passes on, where to, its state and checks, and the switches. */
export function RelayRow({ r, side, cc, busy, note, nowS, onCheck, onOn, onOff, onRemove }: RowProps) {
  const t = useT();
  const fmt = useFmt();
  const state: RelayState = busy === "check" || busy === "on" ? "checking" : relayState(r);
  const pill = statePill[state];
  const gate = enableGate(r, nowS);
  const reason = checkReason(t, r);
  const cells = [
    { label: side === "exit" ? t("ways.col.port", { entry: r.viaNodeName }) : t("relay.col.port"), value: `${r.network || "tcp"}/${r.port}` },
    { label: t("relay.col.to"), value: r.toIp ? `${r.toIp}:${r.exitPort}` : r.check?.to || `${r.exitNodeName}:${r.exitPort || "—"}` },
  ];
  // what the row says under the facts: the failure, the check, or why it cannot be turned on yet
  let line: ReactNode = null;
  if (busy === "check" || busy === "on") {
    // in the slot of the line below, so a row keeps its height while it is checked
    const names = { entry: r.viaNodeName, exit: r.exitNodeName };
    line = (
      <p role="status" className="text-xs leading-snug font-semibold text-fg">
        {busy === "on" ? t("relay.progress.path", names) : t("relay.progress.leg", names)}
      </p>
    );
  } else if (!busy) {
    if (state === "failed") {
      const agent = r.enabled && r.state === InboundState.FAILED && r.lastError !== "forward_drop" ? agentError(t, r.lastError, { entry: r.viaNodeName, exit: r.exitNodeName }) : "";
      const since = r.check && !r.check.ok ? r.check.atUnix : r.appliedUnix;
      line = (reason || agent) && (
        <div data-testid="relay-down" className="flex flex-col gap-0.5 rounded-field border border-[color-mix(in_oklch,var(--danger)_45%,var(--border))] bg-danger-soft px-3 py-2">
          <p className="text-[13px] leading-snug font-semibold text-danger-text">
            {reason ? t("relay.line.down", { time: since ? fmt.stamp(since) : "—", reason }) : agent}
          </p>
          {agent && reason && <p className="text-xs leading-snug text-danger-text">{agent}</p>}
          {r.lastError && r.lastError !== "forward_drop" && r.state === InboundState.FAILED && <p className="font-mono text-[11px] leading-snug break-words text-muted">{r.lastError}</p>}
        </div>
      );
    } else if (state === "on" && r.check && !r.monitor) {
      line = <p className="text-xs leading-snug text-muted">{t("relay.line.on", { ago: fmt.ago(r.check.atUnix, nowS * 1000) })}</p>;
    } else if (state === "off") {
      line = <p className="text-xs leading-snug text-pretty text-muted">{gate ? t(`relay.gate.${gate}`, { exit: r.exitNodeName }) : t("relay.line.offReady", { ago: fmt.ago(r.check!.leg!.atUnix, nowS * 1000) })}</p>;
    }
  }
  // the next useful step is the filled button: turn on after a fresh pass, else check
  const checkFirst = !r.enabled && (gate === "unchecked" || gate === "stale" || gate === "failed");
  return (
    <li data-testid="relay-row" data-state={state} className="flex flex-col gap-2.5 border-t border-line py-3 first:border-t-0 first:pt-1">
      <div className={`grid items-center gap-3 max-lg:grid-cols-[minmax(0,1fr)_auto] ${gridClass}`}>
        <div className="flex min-w-0 items-center gap-2.5">
          {side === "exit" ? <IconChip icon="sliders" tone="sage" size={28} /> : ccChip(cc)}
          <div className="flex min-w-0 flex-col gap-[3px]">
            <span className="text-sm leading-snug font-bold break-words">
              {side === "entry" && (
                <>
                  <Link to="/nodes/$id" params={{ id: r.exitNodeId }} search={{ tab: "ways", way: `via:${r.viaNodeId}` }} className="hover:text-accent-text hover:underline">
                    {r.exitNodeName}
                  </Link>
                  <span className="text-muted"> · </span>
                </>
              )}
              <Link to="/profiles/$id" params={{ id: r.profileId }} className="hover:text-accent-text hover:underline">
                {r.profileName}
              </Link>
            </span>
            <span className="text-xs text-muted">{protocolName(r.protocol)}</span>
          </div>
        </div>
        {cells.map((c) => (
          <div key={c.label} className="flex min-w-0 flex-col gap-[3px] max-lg:hidden">
            <span className="text-[11px] text-muted">{c.label}</span>
            <span className="font-mono text-[13px] break-all">{c.value}</span>
          </div>
        ))}
        <div className="flex justify-end">
          <StatusPill kind={pill.kind} label={t(pill.word)} sm />
        </div>
      </div>
      <dl className="flex flex-wrap gap-x-4 gap-y-1 text-[11px] lg:hidden">
        {cells.map((c) => (
          <div key={c.label} className="flex min-w-0 gap-1.5">
            <dt className="text-muted">{c.label}</dt>
            <dd className="min-w-0 font-mono font-semibold break-all">{c.value}</dd>
          </div>
        ))}
      </dl>
      <Stages r={r} busy={busy} />
      {!busy && <Watch r={r} nowS={nowS} />}
      {line}
      {note && (
        <Notice tone="danger" className="items-start">
          {note}
        </Notice>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {r.enabled ? (
          <Button variant="secondary" size="sm" disabled={!!busy} onClick={onOff}>
            {t("relay.off")}
          </Button>
        ) : (
          // a disabled button shows no tooltip: the reason sits on a wrapper (and in the line above)
          <span title={gate ? t(`relay.gate.${gate}`, { exit: r.exitNodeName }) : undefined} className="flex">
            <Button variant={gate ? "secondary" : "primary"} size="sm" disabled={!!busy || !!gate} onClick={onOn}>
              {t("relay.on")}
            </Button>
          </span>
        )}
        <Button variant={checkFirst ? "primary" : "secondary"} size="sm" disabled={!!busy} onClick={onCheck}>
          {t("relay.check")}
        </Button>
        <Button variant="ghostDanger" size="sm" className="ml-auto" disabled={!!busy} onClick={onRemove}>
          {t("relay.remove")}
        </Button>
      </div>
    </li>
  );
}

const monStages = ["leg", "entry", "path", "freeze"] as const;

/**
 * What the panel's rounds through a relay that is on say (Relay.monitor): works with the response time, or the stage that
 * fails and for how many rounds; and what went through the forward by the entry's counter, when its agent reports one.
 */
function Watch({ r, nowS }: { r: RelayMsg; nowS: number }) {
  const t = useT();
  const fmt = useFmt();
  const m = r.enabled ? r.monitor : undefined;
  const names = { entry: r.viaNodeName, exit: r.exitNodeName };
  const ago = (unix: number) => fmt.ago(unix, nowS * 1000);
  if (!m && !r.countedUnix) return null;
  const stage = monStages.find((s) => s === m?.stage) ?? "path";
  return (
    <div data-testid="relay-watch" className="flex flex-col gap-1 text-xs leading-snug">
      {m && (
        <p className="flex min-w-0 items-start gap-1.5">
          <span className="mt-[4px] flex">
            <StatusDot kind={m.ok ? "ok" : "bad"} />
          </span>
          <span className={m.ok ? "text-muted" : "font-semibold text-danger-text"}>
            {m.ok
              ? t("relay.mon.ok", { ms: msText(t, fmt, m.responseMs), ago: ago(m.atUnix) })
              : [t("relay.mon.fail", { why: t(`relay.mon.stage.${stage}`, names), ago: ago(m.atUnix) }), m.failStreak > 1 ? t.n("relay.mon.streak", m.failStreak) : ""].filter(Boolean).join(" · ")}
          </span>
        </p>
      )}
      {r.countedUnix > 0 && <p className="pl-3.5 text-muted">{t("relay.bytes", { bytes: fmt.bytes(r.bytes), packets: fmt.num(r.packets, 0), ago: ago(r.countedUnix) })}</p>}
    </div>
  );
}

type StageLook = "ok" | "fail" | "skipped" | "running" | "waiting";
const stageDot: Record<StageLook, StatusKind> = { ok: "ok", fail: "bad", skipped: "off", running: "busy", waiting: "off" };
const lookOf = (s?: Stage): StageLook | null => (s?.status === "ok" ? "ok" : s?.status === "fail" ? "fail" : s?.status === "skipped" ? "skipped" : null);

/** The two checks of a relay, each with its result: the leg from the entry, the path from outside (when the relay is on). */
function Stages({ r, busy }: { r: RelayMsg; busy?: Busy }) {
  const t = useT();
  const c = r.check;
  const names = { entry: r.viaNodeName, exit: r.exitNodeName };
  const sender = c?.path?.senderName === "panel" ? t("ports.senderPanel") : c?.path?.senderName;
  // while a call runs: "check" re-runs both (the path only when on), "on" keeps the leg that let it on and checks the path
  const leg: StageLook | null = busy === "check" ? "running" : lookOf(c?.leg);
  const path: StageLook | null = busy === "check" ? (r.enabled ? "waiting" : null) : busy === "on" ? "running" : lookOf(c?.path);
  const showPath = path && (path !== "skipped" || r.enabled);
  if (!leg && !showPath) return null;
  const item = (label: string, look: StageLook) => (
    <li className="flex min-w-0 items-center gap-1.5">
      <StatusDot kind={stageDot[look]} />
      <span className="text-muted">{label}</span>
      <span className={cx("font-semibold", look === "fail" ? "text-danger-text" : look === "ok" ? "text-fg" : "text-muted")}>{t(`relay.stage.${look}`)}</span>
    </li>
  );
  return (
    <ul data-testid="relay-stages" className="flex flex-wrap gap-x-5 gap-y-1 text-xs">
      {leg && item(t("relay.stage.leg", names), leg)}
      {showPath && item(sender && (path === "ok" || path === "fail") ? t("relay.stage.pathFrom", { sender, entry: r.viaNodeName }) : t("relay.stage.path", { entry: r.viaNodeName }), path)}
    </ul>
  );
}

function RemoveRelay({ r, onClose }: { r: RelayMsg; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const remove = async () => {
    setPending(true);
    try {
      await relaysApi.deleteRelay({ id: r.id });
      onClose();
      toast(t("relay.removed"));
    } catch (e) {
      setError(e);
    } finally {
      setPending(false);
      void qc.invalidateQueries({ queryKey: ["relays"] });
    }
  };
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("relay.removeTitle", { entry: r.viaNodeName, exit: r.exitNodeName })}
      description={t("relay.removeBody", { profile: r.profileName, exit: r.exitNodeName, entry: r.viaNodeName })}
      footer={
        <>
          <Button variant="ghost" size="md" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={pending} onClick={() => void remove()}>
            {t("relay.remove")}
          </Button>
        </>
      }
    >
      {error != null && (
        <Notice tone="danger" className="items-start">
          {errorText(error, t)}
        </Notice>
      )}
    </Modal>
  );
}

// ---- the dialog ----

export type Entry = { id: string; name: string; cc: string };
/** One picked server while the dialog works on it. */
type Run = { leg: StageLook; path: StageLook | null; tone?: "ok" | "bad"; text?: string; free?: number };

type AddProps = {
  /** The node that will pass the servers on. */
  entry: Entry;
  /** Only this node's servers (a way in to it), else every node's. */
  exitId?: string;
  /** Ask for the candidates: the dialog is open. */
  enabled: boolean;
  onClose: () => void;
  /** A Back button in place of Cancel while nothing runs ("New way in" goes back a step). */
  onBack?: () => void;
};

/**
 * "Add relay": the candidates with the three hints, the ports, then the checks one server after another. The body and the
 * buttons, for a dialog of their own (AddRelayDialog) or the last step of "New way in".
 */
export function useAddRelay({ entry, exitId, enabled, onClose, onBack }: AddProps) {
  const t = useT();
  const qc = useQueryClient();
  const ccOf = useCountryOf();
  const cc = useQuery(countryQuery).data?.country || "RU";
  const hours = useQuery(settingsQuery).data?.settings.updateIntervalHours || 12;
  const cands = useQuery({ ...candidatesQuery(entry.id), enabled: enabled && entry.id !== "" });
  const [picked, setPicked] = useState<Record<string, string>>({}); // inbound id -> port text ("" = auto)
  const [runs, setRuns] = useState<Record<string, Run> | null>(null);
  const [mode, setMode] = useState<"on" | "check">("on");
  const [working, setWorking] = useState(false);
  const candidates = (cands.data?.candidates ?? []).filter((c) => !exitId || c.nodeId === exitId);
  const ready = !!cands.data?.agentSupported && candidates.length > 0;
  const groups = groupCandidates(candidates);
  const chosen = candidates.filter((c) => c.inboundId in picked);

  const reset = () => {
    setPicked({});
    setRuns(null);
  };
  const close = () => {
    if (working) return;
    onClose();
    reset();
  };
  const set = (id: string, run: Partial<Run>) => setRuns((all) => ({ ...all, [id]: { ...all![id]!, ...run } }));

  /** Make the relay (off), check the leg, and on "Check and turn on" switch it on (the panel checks the path then). */
  const one = async (c: Candidate, on: boolean, port: string) => {
    const names = { entry: entry.name, exit: c.nodeName, exitPort: c.port, port: Number(port) || c.port, network: c.network };
    set(c.inboundId, { leg: "running", path: on ? "waiting" : null, tone: undefined, text: undefined, free: undefined });
    let r: RelayMsg;
    try {
      r = plain(await relaysApi.createRelay({ inboundId: c.inboundId, viaNodeId: entry.id, port: Number(port) || 0 })).relay!;
      r = plain(await relaysApi.checkRelay({ id: r.id })).relay!;
    } catch (e) {
      const ce = ConnectError.from(e);
      const free = errorCode(ce.rawMessage) === "port_taken" ? Number(errorVars(ce.rawMessage).free) || 0 : 0;
      set(c.inboundId, { leg: "skipped", path: null, tone: "bad", text: errorText(e, t), free });
      return;
    }
    const leg = r.check?.leg;
    if (leg?.status !== "ok") {
      const reason = leg ? stageReason(t, "leg", leg, namesOf(r)) : t("relay.why.leg", { ...names, port: c.port, net: c.network });
      set(c.inboundId, { leg: "fail", path: on ? "skipped" : null, tone: "bad", text: t(on ? "relay.dlg.result.fail" : "relay.dlg.result.failCheck", { reason }) });
      return;
    }
    if (!on) {
      set(c.inboundId, { leg: "ok", tone: "ok", text: t("relay.dlg.result.checked") });
      return;
    }
    set(c.inboundId, { leg: "ok", path: "running" });
    try {
      await relaysApi.updateRelay({ id: r.id, enabled: true });
      set(c.inboundId, { path: "ok", tone: "ok", text: t("relay.dlg.result.on", { via: viaLabel(t, entry.cc), hours }) });
    } catch (e) {
      const ce = ConnectError.from(e);
      const v = errorVars(ce.rawMessage);
      const failed = ce.code === Code.FailedPrecondition && errorCode(ce.rawMessage) === "relay_check_failed";
      const reason = failed ? stageReason(t, v.stage === "leg" ? "leg" : "path", { code: v.code ?? "", verdict: "", sent: 0, got: 0 }, namesOf(r)) : errorText(e, t);
      set(c.inboundId, { path: "fail", tone: "bad", text: failed ? t("relay.dlg.result.fail", { reason }) : reason });
    }
  };

  /** Runs the picked servers, or `only` one again (on another port: "Take 8444"). */
  const go = async (on: boolean, only?: Candidate, port?: string) => {
    const list = only ? [only] : chosen;
    setMode(on ? "on" : "check");
    setWorking(true);
    setRuns((all) => ({ ...(only ? all : {}), ...Object.fromEntries(list.map((c) => [c.inboundId, { leg: "waiting", path: on ? "waiting" : null } satisfies Run])) }));
    // one at a time: the panel checks one relay per exit at a time anyway, and the owner reads them in order
    for (const c of list) await one(c, on, port ?? picked[c.inboundId] ?? "");
    setWorking(false);
    await qc.invalidateQueries({ queryKey: ["relays"] });
  };

  const body = runs ? (
    <ul data-testid="relay-runs" className="flex flex-col">
      {chosen.map((c) => {
        const run = runs[c.inboundId];
        if (!run) return null;
        const names = { entry: entry.name, exit: c.nodeName };
        return (
          <li key={c.inboundId} className="flex flex-col gap-2 border-t border-line py-3 first:border-t-0 first:pt-0">
            <div className="flex min-w-0 items-center gap-2.5">
              {ccChip(ccOf(c.nodeId))}
              <span className="min-w-0 text-[13px] font-bold break-words">
                {c.nodeName} <span className="text-muted">·</span> {c.profileName}
              </span>
            </div>
            <ul className="flex flex-col gap-1 text-xs">
              <RunStage look={run.leg} running={t("relay.progress.leg", names)} label={t("relay.stage.leg", names)} />
              {run.path && <RunStage look={run.path} running={t("relay.progress.path", names)} label={t("relay.stage.path", { entry: entry.name })} />}
            </ul>
            {run.text && (
              <div className={cx("flex flex-wrap items-center gap-2 rounded-field border px-3 py-2 text-xs leading-snug", run.tone === "ok" ? "tone-ok tint border-transparent" : "border-[color-mix(in_oklch,var(--danger)_45%,var(--border))] bg-danger-soft")}>
                <span className={cx("min-w-0 flex-1", run.tone === "bad" && "text-danger-text")}>{run.text}</span>
                {run.free ? (
                  <Button
                    variant="secondary"
                    size="xs"
                    disabled={working}
                    onClick={() => {
                      setPicked((p) => ({ ...p, [c.inboundId]: String(run.free) }));
                      void go(mode === "on", c, String(run.free));
                    }}
                  >
                    {t("node.check.port.take", { port: run.free })}
                  </Button>
                ) : null}
              </div>
            )}
          </li>
        );
      })}
    </ul>
  ) : cands.isError && !cands.data ? (
    <QueryError compact error={cands.error} onRetry={() => void cands.refetch()} />
  ) : !cands.data ? (
    <Pending compact />
  ) : !cands.data.agentSupported ? (
    <Notice>
      {t("relay.agentOld")}{" "}
      <Link to="/updates" className="font-bold underline decoration-dotted underline-offset-2">
        {t("relay.openUpdates")}
      </Link>
    </Notice>
  ) : candidates.length === 0 ? (
    <p className="text-[13px] text-muted">{exitId ? t("ways.new.empty", { node: cands.data.candidates.find((c) => c.nodeId === exitId)?.nodeName ?? "", entry: entry.name }) : t("relay.dlg.empty")}</p>
  ) : (
    <>
      {groups.suggested.length > 0 && candidateGroup(exitId ? "" : t("relay.dlg.suggested", { cc }), groups.suggested, cands.data.entryCountry)}
      {groups.others.length > 0 && candidateGroup(groups.suggested.length > 0 ? t("relay.dlg.others") : "", groups.others, cands.data.entryCountry)}
      <p className="text-xs leading-snug text-muted">{t("relay.dlg.portHint", { entry: entry.name })}</p>
    </>
  );

  // plain render functions, not components: a component declared in here would be a new type on every render, and the
  // port field would lose the keyboard on every key
  function candidateGroup(title: string, nodes: CandidateNode[], entryCountry?: CountryHint) {
    return (
      <section key={title || "all"} className="flex flex-col gap-2">
        {title && <h3 className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{title}</h3>}
        {nodes.map((g) => {
          const first = g.servers[0]!;
          const hints = [countryHint(t, cc, first.country), reachHint(t, entry.name, g.nodeName, first.reach), seenHint(t, entry.name, first.country, entryCountry)].filter((h): h is Hint => !!h);
          return (
            <div key={g.nodeId} data-testid="relay-candidate-node" className="flex flex-col overflow-hidden rounded-card border border-line">
              <div className="flex flex-col gap-1.5 bg-surface-2/60 px-3.5 py-2.5">
                <div className="flex items-center gap-2">
                  {ccChip(ccOf(g.nodeId))}
                  <span className="text-[13px] font-extrabold">{g.nodeName}</span>
                </div>
                <ul className="flex flex-col gap-0.5 text-xs leading-snug">
                  {hints.map((h) => (
                    <li key={h.text} className="flex items-start gap-1.5">
                      <span className="mt-[4px] flex">
                        <StatusDot kind={h.tone} />
                      </span>
                      <span className={h.tone === "bad" ? "text-danger-text" : "text-fg"}>{h.text}</span>
                    </li>
                  ))}
                </ul>
              </div>
              {g.servers.map(serverRow)}
            </div>
          );
        })}
      </section>
    );
  }

  function serverRow(c: Candidate) {
    const off = !!c.unsupported || !!c.relayId;
    const on = c.inboundId in picked;
    const why = c.unsupported
      ? (["cdn", "acme_ip", "awg", "build_failed"] as const).includes(c.unsupported as "cdn")
        ? t(`relay.unsupported.${c.unsupported as "cdn"}`)
        : c.unsupported
      : c.relayId
        ? t("relay.dlg.exists", { entry: entry.name })
        : "";
    return (
      <div key={c.inboundId} data-testid="relay-candidate" data-off={off || undefined} className="flex items-center gap-3 border-t border-line px-3.5 py-2.5">
        <label className={cx("flex min-w-0 flex-1 items-start gap-2.5", off ? "cursor-not-allowed" : "cursor-pointer")}>
          <input
            type="checkbox"
            className="mt-0.5 size-4 flex-none accent-accent"
            aria-label={t("relay.dlg.select", { profile: c.profileName, exit: c.nodeName })}
            checked={on}
            disabled={off}
            onChange={(e) => setPicked((p) => (e.target.checked ? { ...p, [c.inboundId]: "" } : without(p, c.inboundId)))}
          />
          <span className={cx("flex min-w-0 flex-col gap-0.5", off && "opacity-60")}>
            <span className="text-[13px] font-bold break-words">{c.profileName}</span>
            <span className="text-[11px] text-muted">
              {protocolName(c.protocol)}
              {c.port > 0 && (
                <span className="font-mono">
                  {" "}
                  · {c.network}/{c.port}
                </span>
              )}
            </span>
            {why && <span className="text-[11px] leading-snug text-muted">{why}</span>}
            {!off && c.hop && <span className="text-[11px] leading-snug text-warn-text">{t("relay.dlg.hop", { port: c.port })}</span>}
          </span>
        </label>
        {!off && (
          // always laid out, shown when picked: ticking a box moves nothing
          <input
            className={cx("h-8 w-[84px] flex-none rounded-ctl border border-line bg-surface px-2.5 font-mono text-[13px] outline-none focus:border-accent", !on && "invisible")}
            inputMode="numeric"
            aria-label={t("relay.dlg.portLabel", { entry: entry.name, profile: c.profileName })}
            placeholder={t("relay.dlg.portAuto")}
            value={picked[c.inboundId] ?? ""}
            tabIndex={on ? 0 : -1}
            onChange={(e) => setPicked((p) => ({ ...p, [c.inboundId]: e.target.value.replace(/\D/g, "").slice(0, 5) }))}
          />
        )}
      </div>
    );
  }

  const footer = runs ? (
    <Button variant="primary" size="md" disabled={working} onClick={close}>
      {t("common.done")}
    </Button>
  ) : (
    <>
      {onBack ? (
        <Button
          variant="ghost"
          size="md"
          onClick={() => {
            reset();
            onBack();
          }}
        >
          {t("ways.new.back")}
        </Button>
      ) : (
        <Button variant="ghost" size="md" className="max-md:hidden" onClick={close}>
          {t("common.cancel")}
        </Button>
      )}
      <Button variant="secondary" size="md" disabled={!ready || chosen.length === 0} onClick={() => void go(false)}>
        {t("relay.dlg.checkOnly")}
      </Button>
      <Button variant="primary" size="md" disabled={!ready || chosen.length === 0} onClick={() => void go(true)}>
        {t("relay.dlg.checkOn")}
      </Button>
    </>
  );
  return { body, footer, close, reset, working };
}

/** "Add relay" in a dialog of its own: from an entry ("Relay another node…"), or more servers of one exit ("Add server…"). */
export function AddRelayDialog({ open, onOpenChange, entry, exitId }: { open: boolean; onOpenChange: (open: boolean) => void; entry: Entry; exitId?: string }) {
  const t = useT();
  const add = useAddRelay({ entry, exitId, enabled: open, onClose: () => onOpenChange(false) });
  return (
    <Modal
      open={open}
      onOpenChange={(o) => !o && add.close()}
      wide
      closeLabel={t("common.close")}
      title={t("relay.dlg.title", { entry: entry.name })}
      description={t("relay.dlg.intro", { entry: entry.name })}
      footer={add.footer}
    >
      {add.body}
    </Modal>
  );
}

/** One stage of a run in the dialog: what is being checked now, or how it went. */
function RunStage({ look, running, label }: { look: StageLook; running: string; label: string }) {
  const t = useT();
  return (
    <li className="flex min-w-0 items-center gap-1.5">
      <StatusDot kind={stageDot[look]} />
      {look === "running" ? (
        <span className="font-semibold text-fg">{running}</span>
      ) : (
        <>
          <span className="text-muted">{label}</span>
          <span className={cx("font-semibold", look === "fail" ? "text-danger-text" : look === "ok" ? "text-fg" : "text-muted")}>{t(`relay.stage.${look}`)}</span>
        </>
      )}
    </li>
  );
}

/** The relays with this node at either end (the ways in, a profile's removal, retiring the node). */
export function useRelaysOf(nodeId: string, enabled: boolean) {
  return useQuery({ ...relaysQuery(nodeId), enabled }).data ?? [];
}

/** The relays that go when a server is removed (§8 screen 8): "Also removes 2 relays through msk1." */
export function alsoRemovesText(t: T, gone: RelayMsg[]): string {
  if (gone.length === 0) return "";
  const entries = [...new Set(gone.map((r) => r.viaNodeName))].join(", ");
  return t.n("relay.removeAlso", gone.length, { entries });
}
