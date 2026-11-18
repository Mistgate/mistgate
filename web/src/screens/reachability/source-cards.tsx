import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import { isStepUpCancelled } from "@/components/step-up";
import { Button, buttonClass } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { type StatusKind } from "@/components/ui/status";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { blocklistErrorText, blocklistQuery, listsState, useBlocklistActions, type BlocklistSettings, type ListsOutcome, type ListsState } from "@/lib/blocklist";
import {
  bsbordDefaultInterval,
  bsbordErrorText,
  bsbordIntervals,
  bsbordQuery,
  bsbordState,
  capRange,
  crimeaPreset,
  districts,
  holdShown,
  isCountryOff,
  isWhiteList,
  looksLikeKey,
  perOperatorPreset,
  rub,
  unitsQuery,
  useBsbordActions,
  type BsbordOutcome,
  type BsbordPreview,
  type BsbordSettings,
  type BsbordState,
  type BsbordTest,
  type BsbordUnit,
} from "@/lib/bsbord";
import {
  cheburcheckErrorText,
  cheburcheckQuery,
  cheburcheckState,
  ownIntervals,
  publicIntervals,
  useCheburcheckActions,
  type CheburcheckOutcome,
  type CheburcheckSettings,
  type CheburcheckState,
} from "@/lib/cheburcheck";
import { clientProbeQuery, clientProbeState, groupText, nodeKind, troubleText, useClientProbeActions, type ClientProbe, type ClientProbeNode } from "@/lib/clientprobe";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt, type Fmt } from "@/lib/format";
import {
  cardState,
  globalpingErrorText,
  globalpingQuery,
  intervalOptions,
  looksLikeToken,
  useGlobalpingActions,
  type CardState,
  type GlobalpingSettings,
  type TestOutcome,
} from "@/lib/globalping";
import { checksQuery, countryReason, hasKey, tspuPortsText } from "@/lib/health";
import { ownProbeQuery, probeDefaultInterval, probeIntervals, probesState, useOwnProbeActions, validFallback, type OwnProbes, type ProbesState } from "@/lib/ownprobe";
import { nodesQuery } from "@/lib/queries";
import { useNow } from "@/lib/time";
import {
  isNotAFailure,
  probeNodeOptions,
  runLook,
  tspuDefaultInterval,
  tspuErrorText,
  tspuIntervals,
  tspuQuery,
  tspuState,
  useTspuActions,
  type TspuOutcome,
  type TspuSettings,
  type TspuState,
  type TspuTest,
} from "@/lib/tspu";
import { Gl, P } from "@/screens/health/page-parts";
import { AddProbeModal, ProbeList } from "./probes";
import {
  ConfirmDisconnect,
  ConfirmOn,
  Detail,
  Foot,
  IntervalField,
  Loading,
  PRow,
  Privacy,
  ProblemBox,
  Rows,
  SecretField,
  everyText,
  useLook,
  type Look,
} from "./source-ui";

// The seven source cards of the Sources tab: what each source is, its state, its settings, what leaves the panel, its test, its pause and its
// disconnect. The layout is the board's open source (source-ui.tsx); here are the words of each state and the calls of each source.

type Actions = ReturnType<typeof useGlobalpingActions>;
type ListsActions = ReturnType<typeof useBlocklistActions>;
type TspuActions = ReturnType<typeof useTspuActions>;
type CheburActions = ReturnType<typeof useCheburcheckActions>;
type BsbordActions = ReturnType<typeof useBsbordActions>;

const look = (state: string, kind: StatusKind, word: string, detail: string, tag?: string): Look => ({ state, kind, word, detail, ...(tag && { tag }) });
const rawOf = (e: unknown) => ConnectError.from(e).rawMessage;

/** A Test that went through says so, as the board's toast does. */
const useTestDone = (name: MessageKey) => {
  const t = useT();
  const toast = useToast();
  return () => toast(t("rp.check.ok", { name: t(name) }));
};

/** Pause: the checks stop and the settings stay; the toast takes it back. */
function usePause(update: { mutate: (req: { enabled: boolean }, o?: { onSuccess?: () => void }) => void }, off: MessageKey) {
  const t = useT();
  const toast = useToast();
  return () => update.mutate({ enabled: false }, { onSuccess: () => toast(t(off), { undo: () => update.mutate({ enabled: true }) }) });
}

/** Globalping: off by default; on, it asks probes inside the country about every online node each round. */
export function GlobalpingCard() {
  const t = useT();
  const q = useQuery(globalpingQuery);
  const act = useGlobalpingActions();
  const s = q.data;
  return (
    <Detail id="globalping" sub={t("gp.sub")}>
      <Loading q={q} />
      {/* switched off and on again, the card starts over: the last test said nothing about the new state */}
      {s && <GlobalpingBody key={String(s.enabled)} s={s} act={act} />}
    </Detail>
  );
}

/** Globalping's state: what is left of the hour, the error, why a round said nothing. */
function globalpingLook(t: T, fmt: Fmt, state: CardState): Look {
  switch (state.kind) {
    case "off":
      return look("off", "off", t("cr.state.off"), t("gp.state.off.detail"));
    case "untested":
      return look("untested", "blip", t("cr.state.on"), t("cr.state.untested"));
    case "works": {
      const test = state.test;
      const parts = test ? [t("gp.state.left", { left: test.remaining, limit: test.limit }), test.tokenUsed ? t("gp.state.token") : ""] : [t("gp.state.round", { ago: fmt.ago(state.roundAt ?? 0) })];
      return look("works", "ok", t("cr.state.works"), parts.filter(Boolean).join(" · "));
    }
    case "fails":
      return look("fails", "bad", t("cr.state.fails"), globalpingErrorText(state.error, t), rawOf(state.error) === "globalping_unauthorized" ? "gp.token" : undefined);
    case "noData":
      return look("noData", "warn", t("gp.state.noData"), [countryReason(t, state.reason), t("gp.state.round", { ago: fmt.ago(state.at) })].filter(Boolean).join(" · "), state.reason === "rate_limited" ? "gp.limit" : undefined);
  }
}

function GlobalpingBody({ s, act }: { s: GlobalpingSettings; act: Actions }) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const done = useTestDone("gp.title");
  const [outcome, setOutcome] = useState<TestOutcome | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  const [replace, setReplace] = useState(0);
  // the newest round tells whether Globalping keeps answering between tests
  const rounds = useQuery({ ...checksQuery(), enabled: s.enabled }).data?.countryChecks ?? [];
  const state = cardState(s, outcome, rounds);
  const l = globalpingLook(t, fmt, state);
  useLook(l);
  const pause = usePause(act.update, "gp.off");

  const test = () =>
    act.test.mutate(undefined, {
      onSuccess: (ok) => {
        setOutcome({ at: Date.now() / 1000, ok });
        done();
      },
      // asked again within 10 s: that is not a failure of Globalping
      onError: (error) => (ConnectError.from(error).code === Code.ResourceExhausted ? toast.error(t("cr.tooSoon")) : setOutcome({ at: Date.now() / 1000, error })),
    });

  return (
    <>
      <ProblemBox
        action={
          l.tag === "gp.token" && (
            <Button size="md" data-act="replace" onClick={() => setReplace((n) => n + 1)}>
              {t("rp.pb.replaceToken")}
            </Button>
          )
        }
      />
      {s.enabled && (
        <Rows>
          <SecretField
            label={t("gp.token")}
            hint={t("gp.token.hint")}
            has={s.hasToken}
            testId="gp-token"
            busy={act.update.isPending}
            valid={looksLikeToken}
            invalid={t("gp.err.token")}
            saveLabel={t("gp.token.save")}
            onSave={(token, stop) => act.update.mutate({ token }, { onSuccess: stop })}
            signal={replace}
          />
          <IntervalField current={s.intervalMin || 15} options={intervalOptions} onPick={(m) => act.update.mutate({ intervalMin: m })} />
          {/* IPv6: a round of its own with its own control site, for the nodes whose IPv6 works; it takes more of the hour */}
          <PRow label={t("gp.ipv6")} hint={t("gp.ipv6.hint")}>
            <Switch checked={s.ipv6} disabled={act.update.isPending} onCheckedChange={(v) => act.update.mutate({ ipv6: v })} aria-label={t("gp.ipv6")} />
          </PRow>
        </Rows>
      )}
      <Privacy>{t("gp.privacy")}</Privacy>
      <Foot
        test={s.enabled ? { pending: act.test.isPending, onClick: test } : undefined}
        on={s.enabled}
        onSwitchOn={() => act.update.mutate({ enabled: true })}
        switchBusy={act.update.isPending || act.disconnect.isPending}
        pause={pause}
        forget={s.enabled || s.hasToken ? () => setDisconnecting(true) : undefined}
      />
      {disconnecting && (
        <ConfirmDisconnect
          title={t("gp.disconnect.title")}
          body={t("gp.disconnect.body")}
          busy={act.disconnect.isPending}
          onClose={() => setDisconnecting(false)}
          onConfirm={() => act.disconnect.mutate(undefined, { onSuccess: () => setDisconnecting(false) })}
        />
      )}
    </>
  );
}

/**
 * The block lists: off by default; on, the panel downloads the public lists every 6 hours and compares each node's address
 * with them itself. Only Russia has lists: for another country the card says so and nothing is downloaded.
 */
export function BlocklistCard() {
  const t = useT();
  const q = useQuery(blocklistQuery);
  const act = useBlocklistActions();
  const s = q.data;
  return (
    <Detail id="lists" sub={t("bl.sub")}>
      <Loading q={q} />
      {/* switched off and on again, the card starts over, as the Globalping card does */}
      {s && <ListsBody key={String(s.enabled)} s={s} act={act} />}
    </Detail>
  );
}

/** The lists' state: their date, then what the last Test found (prefixes, CDN ranges, nodes in the registry) or when they were last compared. */
function listsLook(t: T, fmt: Fmt, state: ListsState): Look {
  switch (state.kind) {
    case "off":
      return look("off", "off", t("cr.state.off"), t("bl.state.off.detail"));
    case "unavailable":
      return look("unavailable", "off", t("cr.state.unavailable"), t("bl.state.onlyRU"));
    case "untested":
      return look("untested", "blip", t("cr.state.on"), t("cr.state.untested"));
    case "works": {
      const r = state.test;
      const parts = r
        ? [
            t.n("bl.state.prefixes", r.prefixes, { n: fmt.num(r.prefixes, 0) }),
            t.n("bl.state.cdn", r.cdnRanges, { n: fmt.num(r.cdnRanges, 0) }),
            t.n("bl.state.nodes", r.nodesChecked, { listed: r.nodesListed }),
            r.mirrorUsed ? t("bl.state.mirror") : "",
          ]
        : [t("bl.state.checked", { ago: fmt.ago(state.checkedAt) })];
      return look("works", "ok", t("cr.state.works"), [state.listsAt ? t("bl.state.lists", { date: fmt.dateTime(state.listsAt) }) : "", ...parts].filter(Boolean).join(" · "));
    }
    case "fails":
      return look("fails", "bad", t("cr.state.fails"), blocklistErrorText(state.error, t));
  }
}

function ListsBody({ s, act }: { s: BlocklistSettings; act: ListsActions }) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const done = useTestDone("bl.title");
  const [outcome, setOutcome] = useState<ListsOutcome | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  useLook(listsLook(t, fmt, listsState(s, outcome)));
  const pause = usePause(act.update, "bl.off");

  const test = () =>
    act.test.mutate(undefined, {
      onSuccess: (ok) => {
        setOutcome({ at: Date.now() / 1000, ok });
        done();
      },
      onError: (error) => (ConnectError.from(error).code === Code.ResourceExhausted ? toast.error(t("cr.tooSoon")) : setOutcome({ at: Date.now() / 1000, error })),
    });

  return (
    <>
      <ProblemBox />
      <Privacy>{t("bl.privacy")}</Privacy>
      <Foot
        // another country than Russia: there is nothing to test
        test={s.enabled && s.available ? { pending: act.test.isPending, onClick: test } : undefined}
        on={s.enabled}
        onSwitchOn={() => act.update.mutate({ enabled: true })}
        switchBusy={act.update.isPending || act.disconnect.isPending}
        pause={pause}
        forget={s.enabled ? () => setDisconnecting(true) : undefined}
      />
      {disconnecting && (
        <ConfirmDisconnect
          title={t("bl.disconnect.title")}
          body={t("bl.disconnect.body")}
          busy={act.disconnect.isPending}
          onClose={() => setDisconnecting(false)}
          onConfirm={() => act.disconnect.mutate(undefined, { onSuccess: () => setDisconnecting(false) })}
        />
      )}
    </>
  );
}

/**
 * The TSPU probe: off by default; on, one of the owner's nodes in Russia (the probe node) finds the TSPU on its path every
 * interval and tests whether the TCP connections to the other nodes die there. Only for Russia, and not on the Cloudflare
 * edition: the card says so, and there is nothing to switch on the edge, where it could not run.
 */
export function TspuCard() {
  const t = useT();
  const q = useQuery(tspuQuery);
  const act = useTspuActions();
  const s = q.data;
  return (
    <Detail id="tspu" sub={t("tp.sub")}>
      <Loading q={q} />
      {/* switched off and on again, the card starts over, as the other cards do */}
      {s && <TspuBody key={String(s.enabled)} s={s} act={act} />}
    </Detail>
  );
}

/** The probe's state: off, not here, no probe node, a run in flight (a spinner), or the last run: its status, time, hop and reference. */
function tspuLook(t: T, fmt: Fmt, state: TspuState): Look {
  switch (state.kind) {
    case "off":
      return look("off", "off", t("cr.state.off"), t("tp.state.off.detail"));
    case "unavailable":
      return look("unavailable", "off", t("cr.state.unavailable"), t(state.reason === "edition" ? "tp.state.edition" : "tp.state.onlyRU"));
    case "noNode":
      return look("noNode", "blip", t("cr.state.on"), t("tp.state.noNode"));
    case "untested":
      return look("untested", "blip", t("cr.state.on"), t("cr.state.untested"));
    case "running":
      return look("running", "busy", t("cr.testing"), t("tp.state.running"));
    case "run": {
      const r = runLook(t, fmt, state.run);
      return look(`run-${state.run.status}`, r.kind, r.word, r.detail);
    }
    case "fails":
      return look("fails", "bad", t("cr.state.fails"), tspuErrorText(state.error, t));
  }
}

function TspuBody({ s, act }: { s: TspuSettings; act: TspuActions }) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const done = useTestDone("tp.title");
  const [outcome, setOutcome] = useState<TspuOutcome | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  const state = tspuState(s, outcome, act.test.isPending);
  const ready = s.enabled && s.available;
  // the probe node dropped off: nothing runs until it is back, which no run's status says
  const node = s.nodes.find((n) => n.id === s.probeNodeId);
  const offline = ready && !!node && !node.online && state.kind !== "running";
  useLook(offline ? look("nodeOffline", "bad", t("rp.tp.offline"), t("rp.tp.offline.detail", { node: node.name }), "tp.node") : tspuLook(t, fmt, state));
  const pause = usePause(act.update, "tp.off");

  // a run takes minutes: the pill spins until it ends, and nothing times it out on this side
  const test = () => {
    const started = Math.floor(Date.now() / 1000);
    act.test.mutate(undefined, {
      onSuccess: (ok) => {
        setOutcome({ at: ok.run?.atUnix || started, ok });
        done();
      },
      // asked again within the minute, or a scheduled run is on: not a failure of the probe
      onError: (error) => (isNotAFailure(error) ? toast(tspuErrorText(error, t)) : setOutcome({ at: started, error })),
    });
  };

  return (
    <>
      <ProblemBox
        action={
          offline && (
            <Link to="/nodes/$id" params={{ id: s.probeNodeId }} className={buttonClass("secondary", "md")}>
              {t("rp.pb.openNode")}
            </Link>
          )
        }
      />
      {ready && (
        <Rows>
          <PRow label={t("tp.node")}>
            <div className="rc-sel">
              <Select
                compact
                aria-label={t("tp.node")}
                value={s.probeNodeId}
                onValueChange={(v) => v !== s.probeNodeId && act.update.mutate({ probeNodeId: v })}
                options={probeNodeOptions(t, fmt, s.nodes, s.probeNodeId)}
              />
            </div>
          </PRow>
          <IntervalField current={s.intervalMin || tspuDefaultInterval} options={tspuIntervals} onPick={(m) => act.update.mutate({ intervalMin: m })} />
        </Rows>
      )}
      {state.kind === "run" && state.nodes && state.nodes.length > 0 && <NodeResults nodes={state.nodes} />}
      <Privacy>{t("tp.privacy")}</Privacy>
      <Foot
        test={ready && s.probeNodeId ? { pending: act.test.isPending, onClick: test } : undefined}
        on={s.enabled}
        onSwitchOn={s.unavailableReason === "edition" ? undefined : () => act.update.mutate({ enabled: true })}
        switchBusy={act.update.isPending || act.disconnect.isPending}
        pause={pause}
        forget={s.enabled || s.probeNodeId !== "" ? () => setDisconnecting(true) : undefined}
      />
      {disconnecting && (
        <ConfirmDisconnect
          title={t("tp.disconnect.title")}
          body={t("tp.disconnect.body")}
          busy={act.disconnect.isPending}
          onClose={() => setDisconnecting(false)}
          onConfirm={() => act.disconnect.mutate(undefined, { onSuccess: () => setDisconnecting(false) })}
        />
      )}
    </>
  );
}

const nodeVerdicts: Record<string, ["ok" | "warn", "cr.v.ok" | "cr.v.tcp" | "tp.v.partial"]> = {
  open: ["ok", "cr.v.ok"],
  blocked: ["warn", "cr.v.tcp"],
  partial: ["warn", "tp.v.partial"],
};

/** What the last Test found per node: its name, the verdict, the ports by what happened to them (the board's `.rc-kl` rows). */
function NodeResults({ nodes }: { nodes: TspuTest["nodes"] }) {
  const t = useT();
  return (
    <ul aria-label={t("tp.results")} className="rc-kl">
      {nodes.map((n) => {
        const [kind, key] = nodeVerdicts[n.verdict] ?? (["unknown", "cr.v.unknown"] as const);
        return (
          <li key={n.nodeId} data-testid="tspu-node">
            <span className="hl-cnode">
              <b className="mono">{n.name}</b>
            </span>
            <span className={kind === "warn" ? "warn" : undefined}>{t(key)}</span>
            <em className="mono">{tspuPortsText(t, n.ports)}</em>
          </li>
        );
      })}
    </ul>
  );
}

/**
 * cheburcheck: off by default; on (after its consent), the volunteer probes of cheburcheck.ru test whether a SYN to each node's
 * IPv4 address passes the TSPU box, every 3 hours or more; an own probe (a cheburprobe token) can run more often. Only for
 * Russia: for another country the card says so and nothing is sent.
 */
export function CheburcheckCard() {
  const t = useT();
  const q = useQuery(cheburcheckQuery);
  const act = useCheburcheckActions();
  const [asking, setAsking] = useState(false);
  const s = q.data;
  return (
    <Detail id="cheburcheck" sub={t("cc.sub")}>
      <Loading q={q} />
      {/* switched off and on again, the card starts over, as the other cards do */}
      {s && <CheburBody key={String(s.enabled)} s={s} act={act} onSwitchOn={() => setAsking(true)} />}
      {asking && (
        <ConfirmOn
          title={t("cc.consent.title")}
          body={t("cc.consent.body")}
          busy={act.update.isPending}
          onClose={() => setAsking(false)}
          onConfirm={() => act.update.mutate({ enabled: true }, { onSuccess: () => setAsking(false) })}
        />
      )}
    </Detail>
  );
}

/** A source verdict in the card's words ("said: reachable"). */
const sourceVerdicts: Record<string, MessageKey> = { ok: "cr.v.ok", partial: "cr.v.partialLong", tcp_blocked: "cr.v.tcp", blocked: "cr.v.blocked", unknown: "cr.v.unknown" };

/** What a status says and why, with the next try when the server holds one (a hold "until never" has no time). */
function statusLine(t: T, fmt: Fmt, prefix: "cc" | "bb", status: string, holdUntil: number, extra: string[] = []): [string, string] {
  const k = `${prefix}.status.${status}`;
  const why = hasKey(`${k}.why`) ? t(`${k}.why` as MessageKey) : "";
  const next = holdShown(holdUntil) ? t("cr.state.next", { time: fmt.stamp(holdUntil) }) : "";
  return [hasKey(k) ? t(k as MessageKey) : status, [why, next, ...extra].filter(Boolean).join(" · ")];
}

/** cheburcheck's state: off, not here, no run yet, the last run and what it said, a Test, or what the site answered (slow down, no address checks, an unknown token). */
function cheburLook(t: T, fmt: Fmt, state: CheburcheckState): Look {
  switch (state.kind) {
    case "off":
      return look("off", "off", t("cr.state.off"), t("cc.state.off.detail"));
    case "unavailable":
      return look("unavailable", "off", t("cr.state.unavailable"), t("cc.state.onlyRU"));
    case "untested":
      return look("untested", "blip", t("cr.state.on"), t("cc.state.waiting"));
    case "works": {
      if (state.test) return look("works", "ok", t("cr.state.works"), t("cc.state.test"));
      const v = sourceVerdicts[state.verdict ?? ""];
      return look("works", "ok", t("cr.state.works"), [t("cc.state.run", { ago: fmt.ago(state.runAt ?? 0) }), v ? t("cc.state.verdict", { verdict: t(v) }) : ""].filter(Boolean).join(" · "));
    }
    case "fails":
      return look("fails", "bad", t("cr.state.fails"), cheburcheckErrorText(state.error, t));
    case "status": {
      const [what, why] = statusLine(t, fmt, "cc", state.status, state.holdUntil);
      return look(`status-${state.status}`, state.status === "token_unknown" ? "bad" : "warn", what, why || t("cc.status.later"), state.status === "token_unknown" ? "cc.token" : undefined);
    }
  }
}

function CheburBody({ s, act, onSwitchOn }: { s: CheburcheckSettings; act: CheburActions; onSwitchOn: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const done = useTestDone("cc.title");
  const [outcome, setOutcome] = useState<CheburcheckOutcome | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  const [replace, setReplace] = useState(0);
  const state = cheburcheckState(s, outcome);
  const l = cheburLook(t, fmt, state);
  useLook(l);
  const ready = s.enabled && !s.unavailable;
  const busy = act.update.isPending;
  const pause = usePause(act.update, "cc.off");

  const test = () =>
    act.test.mutate(undefined, {
      onSuccess: (ok) => {
        setOutcome({ at: Date.now() / 1000, ok });
        done();
      },
      // asked again within the minute: not a failure of the site
      onError: (error) => (ConnectError.from(error).code === Code.ResourceExhausted ? toast.error(t("cr.tooSoon")) : setOutcome({ at: Date.now() / 1000, error })),
    });

  return (
    <>
      <ProblemBox
        action={
          l.tag === "cc.token" && (
            <Button size="md" data-act="replace" onClick={() => setReplace((n) => n + 1)}>
              {t("rp.pb.replaceToken")}
            </Button>
          )
        }
      />
      {ready && (
        <Rows>
          <SecretField
            label={t("cc.token")}
            hint={t("cc.token.hint")}
            has={s.hasToken}
            testId="cc-token"
            busy={busy}
            valid={looksLikeToken}
            invalid={t("cc.err.token")}
            saveLabel={t("gp.token.save")}
            onSave={(token, stop) => act.update.mutate({ token }, { onSuccess: stop })}
            onRemove={() => act.update.mutate({ token: "" })}
            signal={replace}
          />
          <PRow label={t("cc.public")}>
            <Switch checked={s.publicEnabled} disabled={busy} onCheckedChange={(v) => act.update.mutate({ publicEnabled: v })} aria-label={t("cc.public.switch")} />
          </PRow>
          {s.publicEnabled && <IntervalField current={s.intervalMin || publicIntervals[0]} options={publicIntervals} onPick={(m) => act.update.mutate({ intervalMin: m })} />}
          {s.hasToken && <IntervalField label={t("cc.ownInterval")} current={s.ownIntervalMin || 60} options={ownIntervals} onPick={(m) => act.update.mutate({ ownIntervalMin: m })} />}
        </Rows>
      )}
      <Privacy>{t("cc.privacy")}</Privacy>
      <Foot
        test={ready ? { pending: act.test.isPending, onClick: test } : undefined}
        on={s.enabled}
        onSwitchOn={onSwitchOn}
        switchBusy={busy || act.disconnect.isPending}
        pause={pause}
        forget={s.enabled || s.hasToken ? () => setDisconnecting(true) : undefined}
      />
      {disconnecting && (
        <ConfirmDisconnect
          title={t("cc.disconnect.title")}
          body={t("cc.disconnect.body")}
          busy={act.disconnect.isPending}
          onClose={() => setDisconnecting(false)}
          onConfirm={() => act.disconnect.mutate(undefined, { onSuccess: () => setDisconnecting(false) })}
        />
      )}
    </>
  );
}

/**
 * bsbord: off by default and paid. The owner saves the key, picks units and a cap while it is off; switching it on asks for the
 * consent, then shows the price of a round and of a month (asking it already sends the addresses), and only then switches on:
 * the first paid round never comes before a price the owner saw. Only for Russia: the server says so and the card shows it.
 */
export function BsbordCard() {
  const t = useT();
  const q = useQuery(bsbordQuery);
  const act = useBsbordActions();
  const [asking, setAsking] = useState(false);
  // the last price, asked in the consent or on the card; a new interval or new units drop it
  const [price, setPrice] = useState<BsbordPreview | null>(null);
  const s = q.data;
  const away = !s && q.isError && isCountryOff(q.error);
  return (
    <Detail id="bsbord" sub={t("bb.sub")}>
      {away ? <AwayBsbord /> : <Loading q={q} />}
      {s && <BsbordBody key={String(s.enabled)} s={s} act={act} price={price} setPrice={setPrice} onSwitchOn={() => setAsking(true)} />}
      {asking && s && <BsbordConsent s={s} act={act} price={price} setPrice={setPrice} onClose={() => setAsking(false)} />}
    </Detail>
  );
}

/** bsbord of another country than Russia: not available, and what would leave the panel if it were. */
function AwayBsbord() {
  const t = useT();
  useLook(look("unavailable", "off", t("cr.state.unavailable"), t("bb.state.onlyRU")));
  return <Privacy>{t("bb.privacy")}</Privacy>;
}

type PriceProps = { price: BsbordPreview | null; setPrice: (p: BsbordPreview | null) => void };

/** The consent of bsbord: what leaves the panel, that it is paid, that the key is the account; then the price; then on. */
function BsbordConsent({ s, act, price, setPrice, onClose }: { s: BsbordSettings; act: BsbordActions; onClose: () => void } & PriceProps) {
  const t = useT();
  const [error, setError] = useState<unknown>(null);
  const busy = act.preview.isPending || act.update.isPending;
  const ask = () =>
    act.preview.mutate(undefined, {
      onSuccess: (p) => {
        setPrice(p);
        setError(null);
      },
      onError: (e) => {
        if (!isStepUpCancelled(e)) setError(e);
      },
    });
  return (
    <ConfirmOn
      title={t("bb.consent.title")}
      body={t("bb.consent.body")}
      busy={busy}
      onClose={onClose}
      onConfirm={() => act.update.mutate({ enabled: true }, { onSuccess: onClose })}
      confirm={
        price ? undefined : (
          <Button variant="primary" size="lg" disabled={busy} onClick={ask}>
            {act.preview.isPending ? t("bb.price.checking") : t("bb.price.check")}
          </Button>
        )
      }
    >
      {price ? <PriceLine s={s} price={price} /> : <p className="text-xs leading-normal text-pretty text-muted">{t("bb.consent.priceHint")}</p>}
      {error != null && <Notice tone="danger">{bsbordErrorText(error, t)}</Notice>}
    </ConfirmOn>
  );
}

/** "A round costs 1.60 ₽: 3 nodes × 4 units · about 48 ₽ a month, every 24 h", and a warning when a month would pass the cap. */
function PriceLine({ s, price }: { s: BsbordSettings; price: BsbordPreview }) {
  const t = useT();
  const fmt = useFmt();
  const every = t("cr.interval.every", { time: everyText(t, s.intervalMin || bsbordDefaultInterval) });
  const over = price.capReached || price.monthlyCredits > s.capCredits;
  return (
    <div className="flex flex-col gap-2">
      <p data-testid="bsbord-price" className="text-[13px] leading-snug text-pretty">
        <b>{t("bb.price.round", { cost: rub(fmt, price.costCredits), nodes: t.n("bb.price.nodes", price.targetCount), units: t.n("bb.price.units", price.unitCount) })}</b>
        <span className="text-muted"> · {t("bb.price.month", { cost: rub(fmt, price.monthlyCredits), every })}</span>
      </p>
      {over && <Notice>{t("bb.price.over", { cap: rub(fmt, s.capCredits) })}</Notice>}
    </div>
  );
}

/** The balance a Test read: "balance 523.40 ₽ · bonus 50 ₽". */
const balanceText = (t: T, fmt: Fmt, b: BsbordTest) =>
  [t("bb.test.balance", { balance: rub(fmt, b.balanceTotal) }), b.bonusCredits > 0 ? t("bb.test.bonus", { bonus: rub(fmt, b.bonusCredits) }) : ""].filter(Boolean).join(" · ");

const bsbordKinds: Record<string, StatusKind> = { pending: "busy", no_balance: "bad", tier_too_low: "bad", unauthorized: "bad", unavailable: "bad", rejected: "bad", cancelled: "blip" };

/** bsbord's state: off (and what is missing to switch it on), no round yet, the last round, a Test (balance, plan, units), or what the last round ended with. */
function bsbordLook(t: T, fmt: Fmt, state: BsbordState): Look {
  switch (state.kind) {
    case "off":
      return look("off", "off", t("cr.state.off"), t(state.ready ? "bb.state.off.detail" : "bb.state.setup"));
    case "untested":
      return look("untested", "blip", t("cr.state.on"), t("bb.state.waiting"));
    case "works": {
      const b = state.test;
      const line = b ? [balanceText(t, fmt, b), b.tier ? t("bb.test.tier", { tier: b.tier }) : "", t.n("bb.test.units", b.nProbeable)].filter(Boolean).join(" · ") : t("bb.state.run", { ago: fmt.ago(state.runAt ?? 0) });
      return look("works", "ok", t("cr.state.works"), line);
    }
    case "fails":
      return look("fails", "bad", t("cr.state.fails"), bsbordErrorText(state.error, t));
    case "status": {
      const [what, why] = statusLine(t, fmt, "bb", state.status, state.holdUntil, [state.runAt ? t("bb.state.run", { ago: fmt.ago(state.runAt) }) : ""]);
      return look(`status-${state.status}`, bsbordKinds[state.status] ?? "warn", what, why, state.status === "cap_reached" ? "bb.cap" : state.status === "unauthorized" ? "bb.key" : undefined);
    }
  }
}

function BsbordBody({ s, act, price, setPrice, onSwitchOn }: { s: BsbordSettings; act: BsbordActions; onSwitchOn: () => void } & PriceProps) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const done = useTestDone("bb.title");
  const [outcome, setOutcome] = useState<BsbordOutcome | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  const [replace, setReplace] = useState(0);
  const state = bsbordState(s, outcome);
  const l = bsbordLook(t, fmt, state);
  useLook(l);
  const busy = act.update.isPending;
  const pause = usePause(act.update, "bb.off");
  // a new interval or new units make the last price wrong
  const change = (req: { intervalMin?: number; units?: string[] }, doneFn?: () => void) =>
    act.update.mutate(req, {
      onSuccess: () => {
        setPrice(null);
        doneFn?.();
      },
    });

  const test = () =>
    act.test.mutate(undefined, {
      onSuccess: (ok) => {
        setOutcome({ at: Date.now() / 1000, ok });
        done();
      },
      // the panel's own "asked again within seconds" is no failure of bsbord; bsbord's "slow down" is
      onError: (error) => {
        const c = ConnectError.from(error);
        if (c.code === Code.ResourceExhausted && c.rawMessage !== "bsbord_rate_limited") toast.error(t("cr.tooSoon"));
        else setOutcome({ at: Date.now() / 1000, error });
      },
    });
  const ask = () =>
    act.preview.mutate(undefined, {
      onSuccess: setPrice,
      onError: (e) => {
        if (!isStepUpCancelled(e)) toast.error(bsbordErrorText(e, t));
      },
    });

  return (
    <>
      <ProblemBox
        action={
          l.tag === "bb.key" && (
            <Button size="md" data-act="replace" onClick={() => setReplace((n) => n + 1)}>
              {t("rp.pb.replaceKey")}
            </Button>
          )
        }
      />
      {s.enabled && <Spend s={s} test={outcome && "ok" in outcome ? outcome.ok : undefined} />}
      <Rows>
        <SecretField
          label={t("bb.key")}
          hint={t("bb.key.hint")}
          has={s.hasKey}
          testId="bb-key"
          busy={busy}
          valid={looksLikeKey}
          invalid={t("bb.err.key")}
          saveLabel={t("bb.key.save")}
          onSave={(key, stop) => act.update.mutate({ key }, { onSuccess: stop })}
          signal={replace}
        />
        {s.hasKey && <IntervalField current={s.intervalMin || bsbordDefaultInterval} options={bsbordIntervals} onPick={(m) => change({ intervalMin: m })} />}
        {s.hasKey && <CapField s={s} busy={busy} onSave={(capCredits) => act.update.mutate({ capCredits })} />}
        {s.hasKey && s.enabled && (
          <PRow label={t("bb.price")}>
            {price && <PriceLine s={s} price={price} />}
            <div className="rc-btns">
              <Button size="md" disabled={act.preview.isPending} onClick={ask}>
                {act.preview.isPending ? t("bb.price.checking") : t("bb.price.check")}
              </Button>
            </div>
          </PRow>
        )}
      </Rows>
      {s.hasKey && <UnitPicker s={s} busy={busy} onSave={(units, doneFn) => change({ units }, doneFn)} />}
      <Privacy>{t("bb.privacy")}</Privacy>
      <Foot
        test={s.hasKey ? { pending: act.test.isPending, onClick: test } : undefined}
        on={s.enabled}
        onSwitchOn={onSwitchOn}
        // the server takes "on" only with a key and units: until then the button waits, and the state says why
        blocked={!s.enabled && !(s.hasKey && s.units.length > 0)}
        switchBusy={busy || act.disconnect.isPending}
        pause={pause}
        forget={s.enabled || s.hasKey ? () => setDisconnecting(true) : undefined}
      />
      {disconnecting && (
        <ConfirmDisconnect
          title={t("bb.disconnect.title")}
          body={t("bb.disconnect.body")}
          busy={act.disconnect.isPending}
          onClose={() => setDisconnecting(false)}
          onConfirm={() => act.disconnect.mutate(undefined, { onSuccess: () => setDisconnecting(false) })}
        />
      )}
    </>
  );
}

/** This month's spend against the cap, in words and a bar (the board's `.rc-bud`); what a round under way holds back; the balance from the last Test. */
function Spend({ s, test }: { s: BsbordSettings; test?: BsbordTest }) {
  const t = useT();
  const fmt = useFmt();
  const used = s.spentCredits + s.reservedCredits;
  const share = Math.min(1, used / Math.max(1, s.capCredits));
  // the board fills a spent month in the warn tone, as the pill and the problem box of the source say it
  const tone = share >= 0.8 || s.lastStatus === "cap_reached" ? "warn" : "";
  const words = t("bb.spend.of", { spent: rub(fmt, s.spentCredits), cap: rub(fmt, s.capCredits) });
  const notes = [s.reservedCredits > 0 ? t("bb.spend.held", { held: rub(fmt, s.reservedCredits) }) : "", test ? balanceText(t, fmt, test) : ""].filter(Boolean);
  return (
    <div className="rc-bud" data-testid="bsbord-spend">
      <div>
        <span>{t("bb.spend")}</span>
        <b className="mono">{words}</b>
        <em>{t("bb.spend.left", { left: rub(fmt, Math.max(0, s.capCredits - used)) })}</em>
        {notes.length > 0 && <em>{notes.join(" · ")}</em>}
      </div>
      <i
        role="meter"
        aria-label={t("bb.spend")}
        aria-valuemin={0}
        aria-valuemax={s.capCredits}
        aria-valuenow={Math.min(used, s.capCredits)}
        aria-valuetext={words}
        className={cx("rw-pg", tone)}
        style={{ "--w": `${share * 100}%` } as React.CSSProperties}
      />
    </div>
  );
}

/**
 * The units by district: Crimea first (the server's default until a selection is saved), each district behind a disclosure with
 * "one per operator" (each operator's first unit there without the white list); a unit under the white list says so, one that
 * cannot probe now is greyed. Nothing goes to the server until Save. A unit is one operator in one district, with or without the list.
 */
function UnitPicker({ s, busy, onSave }: { s: BsbordSettings; busy: boolean; onSave: (units: string[], done: () => void) => void }) {
  const t = useT();
  const fmt = useFmt();
  const q = useQuery(unitsQuery);
  const units = q.data?.units ?? [];
  const saved = s.units.length ? s.units : units.filter((u) => u.selected).map((u) => u.opKey);
  const [picked, setPicked] = useState<string[] | null>(null);
  const current = new Set(picked ?? saved);
  const dirty = current.size !== s.units.length || s.units.some((k) => !current.has(k));
  const set = (keys: Iterable<string>) => setPicked([...new Set(keys)].sort());
  const toggle = (key: string, on: boolean) => set(on ? [...current, key] : [...current].filter((k) => k !== key));
  const open = new Set(saved);
  return (
    <div className="rc-un">
      <div className="rc-unh">
        <b>{t("bb.units")}</b>
        <span className="mono">{t("bb.units.picked", { n: current.size })}</span>
        <em>{t("bb.units.sub")}</em>
      </div>
      {q.isPending && <Pending compact />}
      {q.isError && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {units.length > 0 && (
        <>
          <div className="rc-unp">
            <Button size="md" disabled={busy} onClick={() => set([...current, ...crimeaPreset(units)])}>
              {t("bb.units.crimea")}
            </Button>
          </div>
          {districts(units, fmt.locale).map((d) => (
            <details key={d.code} open={d.units.some((u) => open.has(u.opKey)) || undefined} className="rc-ud">
              <summary>
                <span className="rc-udn">{d.name}</span>
                <span className="mono">{t("bb.units.picked", { n: d.units.filter((u) => current.has(u.opKey)).length })}</span>
                <Gl d={P.chev} className="sb-chev" />
              </summary>
              {perOperatorPreset(d.units, d.code).length > 0 && (
                <div className="rc-unp">
                  <Button size="md" disabled={busy} onClick={() => set([...current, ...perOperatorPreset(d.units, d.code)])}>
                    {t("bb.units.perOperator")}
                  </Button>
                </div>
              )}
              <ul aria-label={d.name} className="rc-ung">
                {d.units.map((u) => (
                  <UnitTile key={u.opKey} u={u} checked={current.has(u.opKey)} busy={busy} onToggle={(on) => toggle(u.opKey, on)} />
                ))}
              </ul>
            </details>
          ))}
        </>
      )}
      {dirty && (
        <div className="rc-una">
          {picked && (
            <Button variant="ghost" size="md" disabled={busy} onClick={() => setPicked(null)}>
              {t("bb.units.undo")}
            </Button>
          )}
          <Button variant="primary" size="md" disabled={busy || current.size === 0} onClick={() => onSave([...current].sort(), () => setPicked(null))}>
            {t("bb.units.save")}
          </Button>
        </div>
      )}
    </div>
  );
}

/** One unit: its box, the operator and the unit's name, the white-list mark, and "cannot probe now" greyed (it stays tickable off). */
function UnitTile({ u, checked, busy, onToggle }: { u: BsbordUnit; checked: boolean; busy: boolean; onToggle: (on: boolean) => void }) {
  const t = useT();
  const more = [u.name && u.name !== u.operator ? u.name : "", !u.probeable ? t("bb.units.cannot") : ""].filter(Boolean).join(" · ");
  return (
    <li>
      <button type="button" role="checkbox" aria-checked={checked} disabled={busy || (!u.probeable && !checked)} className={cx(!u.probeable && "dim")} onClick={() => onToggle(!checked)}>
        <span className={cx("rc-cb", checked && "on")} aria-hidden="true">
          <i>
            <Gl d={P.check} />
          </i>
        </span>
        <span>
          <b>
            {u.operator}
            {isWhiteList(u) && <span className="rc-wl">{t("bb.units.white")}</span>}
          </b>
          {more && <em>{more}</em>}
        </span>
      </button>
    </li>
  );
}

/** The monthly cap in roubles (the server keeps kopecks); rounds stop for the month when the spend reaches it. */
function CapField({ s, busy, onSave }: { s: BsbordSettings; busy: boolean; onSave: (credits: number) => void }) {
  const t = useT();
  const [value, setValue] = useState(String(s.capCredits / 100));
  const rubles = Number(value.replace(",", ".").replace(/\s/g, ""));
  const valid = value.trim() !== "" && Number.isFinite(rubles) && rubles >= capRange.min && rubles <= capRange.max;
  const credits = Math.round(rubles * 100);

  function submit(e: FormEvent) {
    e.preventDefault();
    if (valid && !busy && credits !== s.capCredits) onSave(credits);
  }

  return (
    <PRow label={t("bb.cap")} hint={t("bb.cap.hint")}>
      <form onSubmit={submit} noValidate className="rc-set edit">
        <TextField aria-label={t("bb.cap")} error={valid ? undefined : t("bb.err.cap")} value={value} onChange={(e) => setValue(e.target.value)} inputMode="decimal" autoComplete="off" mono className="rc-fld" />
        <span className="rc-btns">
          <Button type="submit" size="md" disabled={busy || !valid || credits === s.capCredits}>
            {t("bb.cap.save")}
          </Button>
        </span>
      </form>
    </PRow>
  );
}

/**
 * The reports of kl!ck users: off by default; on, the subscription tells the kl!ck app that the panel takes the result of its
 * local server check, and a person who switched "send to the panel" on sends it. Per node: how many reports, what went wrong
 * (a TSPU freeze above all), from which operators and regions, and how fresh.
 */
export function KlickCard() {
  const t = useT();
  const q = useQuery(clientProbeQuery);
  const act = useClientProbeActions();
  const p = q.data;
  return (
    <Detail id="klick" sub={t("kc.sub")}>
      <Loading q={q} />
      {p && <KlickBody p={p} act={act} />}
    </Detail>
  );
}

function klickLook(t: T, fmt: Fmt, p: ClientProbe): Look {
  const state = clientProbeState(p);
  switch (state.kind) {
    case "off":
      return look("off", "off", t("cr.state.off"), t("kc.state.off.detail"));
    case "waiting":
      return look("waiting", "blip", t("cr.state.on"), t("kc.state.waiting"));
    case "works":
      return look("works", "ok", t("cr.state.works"), [t.n("kc.state.reports", state.reports), t("kc.state.last", { ago: fmt.ago(state.lastAt) })].join(" · "));
  }
}

function KlickBody({ p, act }: { p: ClientProbe; act: ReturnType<typeof useClientProbeActions> }) {
  const t = useT();
  const fmt = useFmt();
  const [disconnecting, setDisconnecting] = useState(false);
  const on = !!p.settings?.enabled;
  useLook(klickLook(t, fmt, p));
  const pause = usePause(act.update, "kc.off");
  return (
    <>
      <ProblemBox />
      {on && p.nodes.length > 0 && <KlickNodes nodes={p.nodes} />}
      <Privacy>{t("kc.privacy")}</Privacy>
      <Foot on={on} onSwitchOn={() => act.update.mutate({ enabled: true })} switchBusy={act.update.isPending || act.disconnect.isPending} pause={pause} forget={on ? () => setDisconnecting(true) : undefined} />
      {disconnecting && (
        <ConfirmDisconnect
          title={t("kc.disconnect.title")}
          body={t("kc.disconnect.body")}
          busy={act.disconnect.isPending}
          onClose={() => setDisconnecting(false)}
          onConfirm={() => act.disconnect.mutate(undefined, { onSuccess: () => setDisconnecting(false) })}
        />
      )}
    </>
  );
}

/** Per node: its name, what went wrong (or "ok"), how many reports and the newest; under it the operators and the regions (the board's `.rc-kl`). */
function KlickNodes({ nodes }: { nodes: ClientProbeNode[] }) {
  const t = useT();
  const fmt = useFmt();
  const known = new Map((useQuery(nodesQuery).data?.nodes ?? []).map((n) => [n.id, n]));
  return (
    <ul aria-label={t("kc.results")} className="rc-kl">
      {nodes.map((n) => {
        const x = n.tally!;
        const kind = nodeKind(x);
        const trouble = troubleText(t, x);
        const node = known.get(n.nodeId);
        return (
          <li key={n.nodeId} data-testid="klick-node">
            <span className="hl-cnode">
              {node?.countryCode && <Flag code={node.countryCode} size={12} className="fl" />}
              <b className="mono">{node?.name ?? n.nodeId}</b>
            </span>
            <span className={kind === "warn" ? "warn" : undefined}>{trouble || t("kc.ok")}</span>
            <em>{[t.n("kc.reports", x.reports), fmt.ago(x.lastUnix)].join(" · ")}</em>
            <GroupLine label={t("kc.operators")} groups={n.operators} />
            <GroupLine label={t("kc.regions")} groups={n.regions} />
          </li>
        );
      })}
    </ul>
  );
}

function GroupLine({ label, groups }: { label: string; groups: ClientProbeNode["operators"] }) {
  const t = useT();
  if (groups.length === 0) return null;
  return (
    <p className="rc-gl">
      <b>{label}:</b> {groups.map((g) => groupText(t, g.name, g.tally!)).join("; ")}
    </p>
  );
}

/**
 * Own probes: routers and servers the owner (or a friend) runs inside a country, which check the nodes from their own networks.
 * Off by default. Owner only; the Cloudflare edition has none. The probes' list, the window that makes one and the revoke are in
 * probes.tsx; this card has the state, the settings, "Add a probe", the pause and Disconnect.
 */
export function ProbesCard() {
  const t = useT();
  const edge = useEdition().off("own_probes");
  const q = useQuery({ ...ownProbeQuery, enabled: !edge });
  const act = useOwnProbeActions();
  return (
    <Detail id="probes" sub={t("pr.sub")}>
      {edge ? (
        <Away />
      ) : (
        <>
          <Loading q={q} />
          {/* switched off and on again, the card starts over, as the other cards do */}
          {q.data && <ProbesBody key={String(q.data.settings?.enabled)} data={q.data} act={act} />}
        </>
      )}
    </Detail>
  );
}

/** The Cloudflare edition has no own probes: the state says so and nothing can be switched on. */
function Away() {
  const t = useT();
  useLook(look("unavailable", "off", t("cr.state.unavailable"), t("pr.state.edition")));
  return <Privacy>{t("pr.privacy")}</Privacy>;
}

/** The state of the probes: off, none yet, waiting for a command, silent, a network to confirm, or "N of M" with the newest check. */
function probesLook(t: T, fmt: Fmt, state: ProbesState, now: number): Look {
  switch (state.kind) {
    case "off":
      return look("off", "off", t("cr.state.off"), t("pr.state.off.detail"));
    case "none":
      return look("none", "blip", t("cr.state.on"), t("pr.state.none"));
    case "pending":
      return look("pending", "blip", t("pr.state.pending"), t("pr.state.pending.detail"));
    case "offline":
      return look("offline", "bad", t("pr.state.offlineAll"), t("pr.state.offlineAll.detail", { age: fmt.duration(now - state.since) }));
    case "netChanged":
      return look("netChanged", "warn", t("pr.state.netChanged"), t("pr.state.netChanged.detail"));
    case "works": {
      const parts = [
        state.lastAt > 0 ? t("pr.state.last", { ago: fmt.ago(state.lastAt) }) : t("pr.state.firstCheck"),
        state.changed ? t.n("pr.state.changed", state.changed) : "",
        state.unfiltered ? t.n("pr.state.unfiltered", state.unfiltered) : "",
      ];
      return look("works", "ok", t("pr.state.works", { n: state.n, m: state.m }), parts.filter(Boolean).join(" · "));
    }
  }
}

function ProbesBody({ data, act }: { data: OwnProbes; act: ReturnType<typeof useOwnProbeActions> }) {
  const t = useT();
  const fmt = useFmt();
  const s = data.settings!;
  const [adding, setAdding] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);
  const now = Math.floor(useNow() / 1000);
  const state = probesState(s, data.probes, now);
  useLook(probesLook(t, fmt, state, now));
  const live = data.probes.filter((p) => p.state !== "revoked");
  const pause = usePause(act.update, "pr.off");

  return (
    <>
      <ProblemBox />
      {s.enabled && data.probes.length > 0 && <ProbeList probes={data.probes} now={now} scriptVersion={data.scriptVersion} actions={act} />}
      {s.enabled && (
        <Rows>
          <IntervalField current={s.intervalMin || probeDefaultInterval} options={probeIntervals} hint={t("pr.interval.hint")} onPick={(m) => act.update.mutate({ intervalMin: m })} />
          <FallbackField current={s.fallbackUrl} busy={act.update.isPending} onSave={(v, stop) => act.update.mutate({ fallbackUrl: v }, { onSuccess: stop })} />
        </Rows>
      )}
      <Privacy>{t("pr.privacy")}</Privacy>
      <Foot
        lead={
          s.enabled && (
            <Button variant={live.length === 0 ? "primary" : "secondary"} size="md" data-act="add" onClick={() => setAdding(true)}>
              <Gl d="M12 5v14M5 12h14" />
              {t("pr.add")}
            </Button>
          )
        }
        on={s.enabled}
        onSwitchOn={() => act.update.mutate({ enabled: true })}
        switchBusy={act.update.isPending || act.disconnect.isPending}
        pause={pause}
        forget={s.enabled ? () => setDisconnecting(true) : undefined}
      />
      {adding && <AddProbeModal actions={act} onClose={() => setAdding(false)} />}
      {disconnecting && (
        <ConfirmDisconnect
          title={t("pr.disconnect.title")}
          body={t("pr.disconnect.body")}
          busy={act.disconnect.isPending}
          onClose={() => setDisconnecting(false)}
          onConfirm={() => act.disconnect.mutate(undefined, { onSuccess: () => setDisconnecting(false) })}
        />
      )}
    </>
  );
}

/** The spare address of the panel: empty, or an https:// URL; saved with its own button, as the settings that are text are. */
function FallbackField({ current, busy, onSave }: { current: string; busy: boolean; onSave: (value: string, done: () => void) => void }) {
  const t = useT();
  const [value, setValue] = useState(current);
  const [tried, setTried] = useState(false);
  const bad = !validFallback(value);
  const changed = value.trim() !== current;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTried(true);
    if (busy || bad || !changed) return;
    onSave(value.trim(), () => setTried(false));
  };
  return (
    <PRow label={t("pr.fallback")} hint={t("pr.fallback.hint")}>
      <form onSubmit={submit} noValidate className="rc-set edit">
        <TextField
          aria-label={t("pr.fallback")}
          error={tried && bad ? t("pr.err.fallback") : undefined}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          type="url"
          inputMode="url"
          autoComplete="off"
          spellCheck={false}
          placeholder="https://panel.example.com"
          mono
          className="rc-fld"
        />
        <span className="rc-btns">
          <Button type="submit" size="md" disabled={busy || !changed}>
            {t("pr.fallback.save")}
          </Button>
        </span>
      </form>
    </PRow>
  );
}
