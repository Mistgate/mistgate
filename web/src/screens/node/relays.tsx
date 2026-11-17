import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { halo } from "@/components/fx/halo";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { TextField } from "@/components/ui/text-field";
import { Pending, QueryError } from "@/components/ui/query-error";
import { StatusDot, type StatusKind } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { useT, type T } from "@/i18n";
import { relays as relaysApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorCode, errorText, errorVars } from "@/lib/errors";
import { countryQuery } from "@/lib/globalping";
import { plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import {
  candidatesQuery,
  countryHint,
  groupCandidates,
  namesOf,
  reachHint,
  relaysQuery,
  seenHint,
  stageReason,
  viaLabel,
  type Candidate,
  type CandidateNode,
  type CountryHint,
  type Hint,
  type RelayMsg,
  type Stage,
} from "@/lib/relay";
import { activeOf, blockedOf, blockedText, bridgeNote, bridgeReason, modeOf, reachReason, wantsReach, type Mode } from "@/lib/relay-bridge";
import { protocolName } from "@/lib/series";
import { markFresh } from "@/components/fx/registry";
import { settingsQuery } from "@/screens/subscriptions/queries";
import { WayChoice } from "./relay-way";

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
  // a relay that may use a bridge is checked both ways: the exit's reach to the entry's bridge comes with the leg
  const check = (r: RelayMsg, refresh = true) => run(r, "check", () => relaysApi.checkRelay({ id: r.id, ...(wantsReach(r) ? { reverseReach: true } : {}) }), undefined, refresh);
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


type StageLook = "ok" | "fail" | "skipped" | "running" | "waiting";
const stageDot: Record<StageLook, StatusKind> = { ok: "ok", fail: "bad", skipped: "off", running: "busy", waiting: "off" };
const lookOf = (s?: Stage): StageLook | null => (s?.status === "ok" ? "ok" : s?.status === "fail" ? "fail" : s?.status === "skipped" ? "skipped" : null);

export function RemoveRelay({ r, onClose }: { r: RelayMsg; onClose: () => void }) {
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
type Run = { leg: StageLook; reach?: StageLook | null; bridge?: StageLook | null; path: StageLook | null; /** The leg did not pass and the bridge did: no red mark on it. */ soft?: boolean; tone?: "ok" | "bad"; text?: string; free?: number };

type AddProps = {
  /** The node that will pass the servers on. */
  entry: Entry;
  /** Only this node's servers (a way in to it), else every node's. */
  exitId?: string;
  /** Ask for the candidates: the dialog is open. */
  enabled: boolean;
  /** A server (inbound id) ticked from the start: the graph draws a relay from an entry to it. */
  preselect?: string;
  /** A relay of this run was switched on (the graph's "move" then offers to remove the old one). */
  onSuccess?: () => void;
  onClose: () => void;
  /** A Back button in place of Cancel while nothing runs ("New way in" goes back a step). */
  onBack?: () => void;
};

/**
 * "Add relay": the candidates with the three hints, the ports, then the checks one server after another. The body and the
 * buttons, for a dialog of their own (AddRelayDialog) or the last step of "New way in".
 */
export function useAddRelay({ entry, exitId, enabled, preselect, onSuccess, onClose, onBack }: AddProps) {
  const t = useT();
  const qc = useQueryClient();
  const ccOf = useCountryOf();
  const cc = useQuery(countryQuery).data?.country || "RU";
  const hours = useQuery(settingsQuery).data?.settings.updateIntervalHours || 12;
  const cands = useQuery({ ...candidatesQuery(entry.id), enabled: enabled && entry.id !== "" });
  const [picked, setPicked] = useState<Record<string, string>>(() => (preselect ? { [preselect]: "" } : {})); // inbound id -> port text ("" = auto)
  const [runs, setRuns] = useState<Record<string, Run> | null>(null);
  const [mode, setMode] = useState<"on" | "check">("on");
  const [way, setWay] = useState<Mode>("auto");
  const [wayOpen, setWayOpen] = useState(false);
  const [working, setWorking] = useState(false);
  const candidates = (cands.data?.candidates ?? []).filter((c) => !exitId || c.nodeId === exitId);
  const ready = !!cands.data?.agentSupported && candidates.length > 0;
  // an exit in the entry's own country gains the people nothing: it is not suggested, and says why
  const sameCountry = (nodeId: string) => !!entry.cc && ccOf(nodeId) === entry.cc;
  const groups = groupCandidates(candidates, sameCountry);
  const chosen = candidates.filter((c) => c.inboundId in picked);
  // a bridge is not for every server: the first picked one that cannot (UDP, an agent to update) says why it is greyed
  const blockedPick = chosen.find((c) => blockedOf(c) !== "");
  const blockedSay = blockedPick ? blockedText(t, blockedOf(blockedPick), { entry: entry.name, exit: blockedPick.nodeName }) : "";
  const wayNow: Mode = way === "reverse" && blockedPick ? "auto" : way;

  const reset = () => {
    setPicked(preselect ? { [preselect]: "" } : {});
    setRuns(null);
    setWay("auto");
    setWayOpen(false);
  };
  const close = () => {
    if (working) return;
    onClose();
    reset();
  };
  const set = (id: string, run: Partial<Run>) => setRuns((all) => ({ ...all, [id]: { ...all![id]!, ...run } }));

  /**
   * Make the relay (off) in the chosen way, check it, and on "Check and turn on" switch it on (the panel checks the path then,
   * and in the bridge waits for the exit to hold it). A leg that does not pass is no end of it when the way may be a bridge
   * and the exit reaches the entry: the relay then goes the bridge's way.
   */
  const one = async (c: Candidate, on: boolean, port: string) => {
    const names = { entry: entry.name, exit: c.nodeName, exitPort: c.port, port: Number(port) || c.port, network: c.network };
    const pair = { entry: entry.name, exit: c.nodeName };
    // the way asked for: a server that cannot use a bridge is made direct, or auto (it stays direct)
    const asked: Mode = blockedOf(c) !== "" && way === "reverse" ? "auto" : way;
    set(c.inboundId, { leg: "running", reach: null, bridge: null, path: on ? "waiting" : null, soft: false, tone: undefined, text: undefined, free: undefined });
    // the first relay between these two nodes makes their lane: only that one sweeps in (a second relay joins a lane that is there)
    const hadLane = qc.getQueryData<RelayMsg[]>(["relays", c.nodeId])?.some((x) => x.viaNodeId === entry.id && x.exitNodeId === c.nodeId) ?? false;
    let r: RelayMsg;
    try {
      r = plain(await relaysApi.createRelay({ inboundId: c.inboundId, viaNodeId: entry.id, port: Number(port) || 0, ...(asked !== "auto" ? { mode: asked } : {}) })).relay!;
      r = plain(await relaysApi.checkRelay({ id: r.id, ...(wantsReach(r) ? { reverseReach: true } : {}) })).relay!;
    } catch (e) {
      const ce = ConnectError.from(e);
      const free = errorCode(ce.rawMessage) === "port_taken" ? Number(errorVars(ce.rawMessage).free) || 0 : 0;
      set(c.inboundId, { leg: "skipped", path: null, tone: "bad", text: errorText(e, t), free });
      return;
    }
    const leg = r.check?.leg;
    const reach = r.check?.reverseReach;
    const reachLook = reach?.status ? (lookOf(reach) ?? "skipped") : null;
    const legOk = leg?.status === "ok";
    const bridgeOk = blockedOf(r) === "" && reach?.status === "ok";
    const m = modeOf(r);
    // direct, bridge, or either: what the check found is enough to go on
    const usable = m === "dnat" ? legOk : m === "reverse" ? bridgeOk : legOk || bridgeOk;
    const viaBridge = m === "reverse" || (m === "auto" && !legOk && bridgeOk);
    // the panel's refusal of a switch-on names the bridge's code beside the leg's: both failed
    const withBridge = (reason: string, code?: string) => (code ? t("relay.why.both", { reason, bridge: reachReason(t, { code, port: 0 }, pair) }) : reason);
    if (!usable) {
      const direct = leg ? stageReason(t, "leg", leg, namesOf(r)) : t("relay.why.leg", { ...names, port: c.port, net: c.network });
      const reason = m === "reverse" ? (reach ? reachReason(t, reach, pair) : direct) : m === "auto" && reach?.status === "fail" ? t("relay.why.both", { reason: direct, bridge: reachReason(t, reach, pair) }) : direct;
      set(c.inboundId, { leg: legOk ? "ok" : "fail", reach: reachLook, path: on ? "skipped" : null, tone: "bad", text: t(on ? "relay.dlg.result.fail" : "relay.dlg.result.failCheck", { reason }) });
      return;
    }
    if (!on) {
      set(c.inboundId, { leg: legOk ? "ok" : "fail", reach: reachLook, soft: !legOk, tone: "ok", text: viaBridge && !legOk ? t("relay.dlg.result.checkedBridge", pair) : t("relay.dlg.result.checked") });
      return;
    }
    set(c.inboundId, { leg: legOk ? "ok" : "fail", reach: reachLook, soft: !legOk, bridge: viaBridge ? "running" : null, path: viaBridge ? "waiting" : "running" });
    try {
      const done = plain(await relaysApi.updateRelay({ id: r.id, enabled: true })).relay;
      if (!hadLane) {
        markFresh("lane", `${c.nodeId}:via:${entry.id}`); // on the exit's page ...
        markFresh("lane", `${entry.id}:out:${c.nodeId}`); // ... and on the entry's
      }
      // what the panel settled on, else what the check said
      const bridged = done ? activeOf(done) === "reverse" : viaBridge;
      set(c.inboundId, { bridge: bridged ? "ok" : null, path: "ok", tone: "ok", text: t(bridged ? "relay.dlg.result.onBridge" : "relay.dlg.result.on", { via: viaLabel(t, entry.cc), hours }) });
      onSuccess?.();
    } catch (e) {
      const ce = ConnectError.from(e);
      const v = errorVars(ce.rawMessage);
      const failed = ce.code === Code.FailedPrecondition && errorCode(ce.rawMessage) === "relay_check_failed";
      // the stage that failed: the leg (and the bridge's reach beside it), the bridge, or the path from outside
      const stage = v.stage === "leg" || v.stage === "reverse_reach" || v.stage === "bridge" ? v.stage : "path";
      const reason = !failed
        ? errorText(e, t)
        : stage === "bridge"
          ? bridgeReason(t, { code: v.code ?? "" }, pair)
          : stage === "reverse_reach"
            ? reachReason(t, { code: v.code ?? "", port: 0 }, pair)
            : withBridge(stageReason(t, stage, { code: v.code ?? "", verdict: "", sent: 0, got: 0 }, namesOf(r)), stage === "leg" ? v.reverse_reach : undefined);
      set(c.inboundId, { bridge: stage === "bridge" ? "fail" : viaBridge ? "ok" : null, path: stage === "bridge" ? "skipped" : "fail", tone: "bad", text: failed ? t("relay.dlg.result.fail", { reason }) : reason });
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
              <RunStage look={run.leg} soft={run.soft} running={t("relay.progress.leg", names)} label={t("relay.stage.leg", names)} />
              {run.reach && <RunStage look={run.reach} running={t("relay.progress.reach", names)} label={t("relay.stage.reverseReach", names)} />}
              {run.bridge && <RunStage look={run.bridge} running={t("relay.progress.bridge", names)} label={t("relay.stage.bridge", names)} />}
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
      {chosen.some((c) => c.protocol === "awg") && <Notice tone="quiet">{t("relay.dlg.awgNote")}</Notice>}
      <p className="text-xs leading-snug text-muted">{t("relay.dlg.portHint", { entry: entry.name })}</p>
      <div className="flex flex-col gap-2">
        <button
          type="button"
          aria-expanded={wayOpen}
          onClick={() => setWayOpen((o) => !o)}
          className="flex w-fit items-center gap-1.5 text-xs font-bold text-muted hover:text-fg focus-visible:outline-2 focus-visible:outline-accent"
        >
          <Icon name="chevronDown" size={12} className={cx("transition-transform duration-200", wayOpen && "rotate-180")} />
          {t("relay.way.dlg.summary", { name: t(`relay.way.opt.${wayNow}`) })}
        </button>
        {wayOpen && <WayChoice value={wayNow} onChange={setWay} blocked={blockedSay} entry={entry.name} label={t("relay.way.title")} />}
      </div>
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
          const same: Hint | null = sameCountry(g.nodeId) ? { tone: "off", text: t("relay.hint.sameCountryEntry", { entry: entry.name }) } : null;
          const hints = [countryHint(t, cc, first.country), reachHint(t, entry.name, g.nodeName, first.reach), seenHint(t, entry.name, first.country, entryCountry), same].filter((h): h is Hint => !!h);
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
    // direct does not pass but a bridge may; or neither; or why a bridge is not for this server
    const bridge = off ? null : bridgeNote(t, c, { entry: entry.name, exit: c.nodeName });
    const why = c.unsupported
      ? (["cdn", "acme_ip", "build_failed"] as const).includes(c.unsupported as "cdn")
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
            {bridge && (
              <span data-testid="relay-bridge-note" className="mt-0.5 flex items-start gap-1.5 text-[11px] leading-snug">
                <span className="mt-[3px] flex">
                  <StatusDot kind={bridge.tone} />
                </span>
                <span className={bridge.tone === "bad" ? "text-danger-text" : bridge.tone === "warn" ? "text-warn-text" : bridge.tone === "ok" ? "text-fg" : "text-muted"}>{bridge.text}</span>
              </span>
            )}
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
export function AddRelayDialog({ open, onOpenChange, entry, exitId, preselect, notice, onSuccess }: { open: boolean; onOpenChange: (open: boolean) => void; entry: Entry; exitId?: string; preselect?: string; notice?: string; onSuccess?: () => void }) {
  const t = useT();
  const add = useAddRelay({ entry, exitId, enabled: open, preselect, onSuccess, onClose: () => onOpenChange(false) });
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
      {notice && <Notice tone="quiet">{notice}</Notice>}
      {add.body}
    </Modal>
  );
}

/** One stage of a run in the dialog: what is being checked now, or how it went. */
function RunStage({ look, running, label, soft }: { look: StageLook; running: string; label: string; soft?: boolean }) {
  const t = useT();
  return (
    <li className="flex min-w-0 items-center gap-1.5">
      <StatusDot kind={soft && look === "fail" ? "off" : stageDot[look]} />
      {look === "running" ? (
        <span className="font-semibold text-fg">{running}</span>
      ) : (
        <>
          <span className="text-muted">{label}</span>
          <span className={cx("font-semibold", look === "fail" && !soft ? "text-danger-text" : look === "ok" ? "text-fg" : "text-muted")}>{t(`relay.stage.${look}`)}</span>
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

/** "Change port": the relay's port on its entry, through the same UpdateRelay the switch uses; the panel checks the port is free. */
export function ChangeRelayPort({ r, onClose }: { r: RelayMsg; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [port, setPort] = useState(String(r.port));
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState(false);
  const n = Number(port);
  const valid = Number.isInteger(n) && n >= 1 && n <= 65535;
  const save = async () => {
    setPending(true);
    try {
      await relaysApi.updateRelay({ id: r.id, port: n });
      onClose();
      toast(t("common.saved"));
      halo("save");
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
      onOpenChange={(o) => !o && !pending && onClose()}
      title={t("wg.port.title", { entry: r.viaNodeName })}
      description={t("wg.port.hint", { entry: r.viaNodeName, exit: r.exitNodeName })}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={pending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={pending || !valid || n === r.port} onClick={() => void save()}>
            {t("wg.port.save")}
          </Button>
        </>
      }
    >
      <TextField label={t("wg.port.label")} mono inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, "").slice(0, 5))} />
      {r.protocol === "awg" && <Notice tone="quiet">{t("relay.dlg.portAwg")}</Notice>}
      {error != null && (
        <Notice tone="danger" className="items-start">
          {errorText(error, t)}
        </Notice>
      )}
    </Modal>
  );
}
