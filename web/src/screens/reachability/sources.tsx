import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { isStepUpCancelled } from "@/components/step-up";
import { useState, type FormEvent, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon, type IconName } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { StatusDot, type StatusKind } from "@/components/ui/status";
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
import { minutesText, useFmt, type Fmt } from "@/lib/format";
import {
  cardState,
  countryOptions,
  countryQuery,
  globalpingErrorText,
  globalpingQuery,
  intervalOptions,
  looksLikeToken,
  useCountryActions,
  useGlobalpingActions,
  type CardState,
  type GlobalpingSettings,
  type TestOutcome,
} from "@/lib/globalping";
import { checksQuery, countryReason, hasKey, tspuPortsText } from "@/lib/health";
import { nodesQuery } from "@/lib/queries";
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
import { CountryMark } from "@/screens/health/country";
import { card } from "@/screens/integrations/parts";

type Actions = ReturnType<typeof useGlobalpingActions>;
type ListsActions = ReturnType<typeof useBlocklistActions>;
type TspuActions = ReturnType<typeof useTspuActions>;
type CheburActions = ReturnType<typeof useCheburcheckActions>;
type BsbordActions = ReturnType<typeof useBsbordActions>;

/**
 * The sources of the Reachability page: a card per source (Globalping, the block lists, the TSPU probe, cheburcheck, bsbord, the
 * reports of kl!ck users), two to a row on a wide screen. Owner only, like the services behind them; the country they all look from is the
 * picker in the page's header (the kl!ck reports come from wherever the people are, and say where).
 */
export function SourceCards() {
  return (
    <div className="grid items-start gap-3.5 xl:grid-cols-2">
      <GlobalpingCard />
      <BlocklistCard />
      <TspuCard />
      <CheburcheckCard />
      <BsbordCard />
      <KlickCard />
    </div>
  );
}

/** The shared country: it only says where to look from and sends nothing, so it saves on the spot without a step-up. */
export function CountryPicker() {
  const t = useT();
  const fmt = useFmt();
  const q = useQuery(countryQuery);
  const save = useCountryActions();
  if (q.isError && !q.data) return <QueryError compact error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return null;
  const current = q.data.country;
  const codes: readonly string[] = (countryOptions as readonly string[]).includes(current) ? countryOptions : [current, ...countryOptions];
  return (
    <div className="flex items-center gap-2.5">
      <span aria-hidden className="text-xs font-semibold text-muted">
        {t("cr.country")}
      </span>
      <div className="w-[170px]">
        <Select
          compact
          aria-label={t("cr.country")}
          value={save.isPending && save.variables ? save.variables : current}
          onValueChange={(v) => v !== current && save.mutate(v)}
          options={codes.map((c) => ({ value: c, label: fmt.country(c) }))}
        />
      </div>
    </div>
  );
}

/** Globalping: off by default; on, it asks probes inside the country about every online node each round. */
export function GlobalpingCard() {
  const t = useT();
  const q = useQuery(globalpingQuery);
  const act = useGlobalpingActions();
  const s = q.data;
  return (
    <section className={card}>
      <CardHead icon="globe" title={t("gp.title")} sub={t("gp.sub")}>
        {s && (
          <Switch
            checked={s.enabled}
            disabled={act.update.isPending || act.disconnect.isPending}
            onCheckedChange={(on) => act.update.mutate({ enabled: on })}
            aria-label={t("gp.switch")}
          />
        )}
      </CardHead>
      {q.isPending && <Pending compact />}
      {q.isError && !s && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {/* switched off and on again, the card starts over: the last test said nothing about the new state */}
      {s && <Body key={String(s.enabled)} s={s} act={act} />}
    </section>
  );
}

function Body({ s, act }: { s: GlobalpingSettings; act: Actions }) {
  const t = useT();
  const toast = useToast();
  const [outcome, setOutcome] = useState<TestOutcome | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  // the newest round tells whether Globalping keeps answering between tests
  const rounds = useQuery({ ...checksQuery(), enabled: s.enabled }).data?.countryChecks ?? [];
  const state = cardState(s, outcome, rounds);

  const test = () =>
    act.test.mutate(undefined, {
      onSuccess: (ok) => setOutcome({ at: Date.now() / 1000, ok }),
      // asked again within 10 s: that is not a failure of Globalping
      onError: (error) => (ConnectError.from(error).code === Code.ResourceExhausted ? toast.error(t("cr.tooSoon")) : setOutcome({ at: Date.now() / 1000, error })),
    });

  return (
    <>
      <GlobalpingState state={state} />
      {s.enabled && (
        <div className="flex flex-col gap-3 border-t border-line pt-3">
          <SecretField
            label={t("gp.token")}
            hint={t("gp.token.hint")}
            has={s.hasToken}
            testId="gp-token"
            busy={act.update.isPending}
            valid={looksLikeToken}
            invalid={t("gp.err.token")}
            saveLabel={t("gp.token.save")}
            onSave={(token, done) => act.update.mutate({ token }, { onSuccess: done })}
          />
          <IntervalField current={s.intervalMin || 15} options={intervalOptions} onPick={(m) => act.update.mutate({ intervalMin: m })} />
        </div>
      )}
      {(s.enabled || s.hasToken) && (
        <div className="flex flex-wrap gap-2">
          {s.enabled && (
            <Button variant="secondary" size="sm" disabled={act.test.isPending} onClick={test}>
              {act.test.isPending ? t("cr.testing") : t("cr.test")}
            </Button>
          )}
          <Button variant="ghostDanger" size="sm" onClick={() => setDisconnecting(true)}>
            {t("cr.disconnect")}
          </Button>
        </div>
      )}
      <Privacy>{t("gp.privacy")}</Privacy>
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

/** The parts every source card shares: its title and what it does, with the switch on the right. */
function CardHead({ icon, title, sub, children }: { icon: IconName; title: string; sub: string; children: ReactNode }) {
  return (
    <div className="flex items-start gap-3">
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2.5 gap-y-0.5">
        <SectionLabel as="h3" icon={icon} tone="mint">
          {title}
        </SectionLabel>
        <span className="text-xs leading-normal text-pretty text-muted">{sub}</span>
      </div>
      {children}
    </div>
  );
}

/** What leaves the panel with the source on: the card's last line, in every state. */
function Privacy({ children }: { children: string }) {
  return (
    <p className="flex items-start gap-1.5 text-xs leading-normal text-pretty text-muted">
      <Icon name="info" size={13} aria-hidden className="mt-px flex-none" />
      {children}
    </p>
  );
}

/** Disconnect asks first: it forgets more than the switch does. */
function ConfirmDisconnect({ title, body, busy, onClose, onConfirm }: { title: string; body: string; busy: boolean; onClose: () => void; onConfirm: () => void }) {
  const t = useT();
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={title}
      description={body}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={busy} onClick={onConfirm}>
            {t("cr.disconnect")}
          </Button>
        </>
      }
    />
  );
}

/** Switching a paid or third-party source on asks first: what leaves the panel and to whom. The step-up comes after. */
function ConfirmOn(p: { title: string; body: string; busy: boolean; onClose: () => void; onConfirm?: () => void; confirm?: ReactNode; children?: ReactNode }) {
  const t = useT();
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !p.busy && p.onClose()}
      title={p.title}
      description={p.body}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={p.busy} onClick={p.onClose}>
            {t("common.cancel")}
          </Button>
          {p.confirm ?? (
            <Button variant="primary" size="md" disabled={p.busy} onClick={p.onConfirm}>
              {t("cr.switchOn")}
            </Button>
          )}
        </>
      }
    >
      {p.children}
    </Modal>
  );
}

/** One line: a dot, the state in a word, the detail. data-state names the state for the tests. */
function StateLine({ state, kind, word, detail }: { state: string; kind: StatusKind; word: string; detail: string }) {
  return (
    <div role="status" data-state={state} className="flex items-start gap-2.5 rounded-field border border-line bg-canvas px-3 py-2.5 text-[13px] leading-snug">
      <span className="mt-[5px] flex">
        <StatusDot kind={kind} />
      </span>
      <span className="min-w-0 text-pretty">
        <b>{word}</b>
        <span className="text-muted"> · {detail}</span>
      </span>
    </div>
  );
}

/** Globalping's state: what is left of the hour, the error, why a round said nothing. */
function GlobalpingState({ state }: { state: CardState }) {
  const t = useT();
  const fmt = useFmt();
  const [kind, word, detail] = ((): [StatusKind, string, string] => {
    switch (state.kind) {
      case "off":
        return ["off", t("cr.state.off"), t("gp.state.off.detail")];
      case "untested":
        return ["blip", t("cr.state.on"), t("cr.state.untested")];
      case "works": {
        const test = state.test;
        const parts = test
          ? [t("gp.state.left", { left: test.remaining, limit: test.limit }), test.tokenUsed ? t("gp.state.token") : ""]
          : [t("gp.state.round", { ago: fmt.ago(state.roundAt ?? 0) })];
        return ["ok", t("cr.state.works"), parts.filter(Boolean).join(" · ")];
      }
      case "fails":
        return ["bad", t("cr.state.fails"), globalpingErrorText(state.error, t)];
      case "noData":
        return ["warn", t("gp.state.noData"), [countryReason(t, state.reason), t("gp.state.round", { ago: fmt.ago(state.at) })].filter(Boolean).join(" · ")];
    }
  })();
  return <StateLine state={state.kind} kind={kind} word={word} detail={detail} />;
}

/** Write-only: with a secret stored the card says "set" and offers to replace (or remove) it; the secret itself never comes back. */
function SecretField(p: {
  label: string;
  hint: string;
  has: boolean;
  testId: string;
  busy: boolean;
  valid: (value: string) => boolean;
  invalid: string;
  saveLabel: string;
  onSave: (value: string, done: () => void) => void;
  onRemove?: () => void;
}) {
  const t = useT();
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [tried, setTried] = useState(false);

  function stop() {
    setEditing(false);
    setValue("");
    setTried(false);
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    setTried(true);
    if (p.busy || !p.valid(value)) return;
    p.onSave(value.trim(), stop);
  }

  if (p.has && !editing)
    return (
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-field border border-line bg-canvas px-3 py-2.5">
        <span className="text-[13px] font-bold">{p.label}</span>
        <span data-testid={p.testId} className="tone-ok tone-text text-xs font-semibold">
          {t("gp.token.set")}
        </span>
        <span className="ml-auto flex gap-2">
          {p.onRemove && (
            <Button variant="ghostDanger" size="sm" disabled={p.busy} onClick={p.onRemove}>
              {t("cc.token.remove")}
            </Button>
          )}
          <Button variant="secondary" size="sm" onClick={() => setEditing(true)}>
            {t("gp.token.replace")}
          </Button>
        </span>
      </div>
    );

  return (
    <form onSubmit={submit} noValidate className="flex flex-col gap-3">
      <TextField
        label={p.label}
        hint={p.hint}
        error={tried && !p.valid(value) ? p.invalid : undefined}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        type="password"
        autoComplete="off"
        spellCheck={false}
        mono
      />
      <div className="flex flex-wrap justify-end gap-2">
        {p.has && (
          <Button variant="ghost" size="md" disabled={p.busy} onClick={stop}>
            {t("common.cancel")}
          </Button>
        )}
        <Button type="submit" variant="secondary" size="md" disabled={p.busy}>
          {p.busy && value ? t("gp.token.saving") : p.saveLabel}
        </Button>
      </div>
    </form>
  );
}

/** A setting of a source card: its name on the left, the control on the right; on a phone they wrap. */
function SettingRow({ label, width, children }: { label: string; width: string; children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
      <span aria-hidden className="text-[13px] font-bold">
        {label}
      </span>
      <div className={width}>{children}</div>
    </div>
  );
}

/** Whole hours from an hour on ("every 2 h", "every 24 h"), minutes below: a day reads as 24 hours, not as "1 d". */
const everyText = (t: T, m: number) => (m >= 60 && m % 60 === 0 ? `${m / 60} ${t("unit.h")}` : minutesText(t, m));

/** Minutes between rounds or runs. Globalping may space its rounds out further when the hourly limit needs it. */
function IntervalField({ current, options, onPick, label }: { current: number; options: readonly number[]; onPick: (minutes: number) => void; label?: string }) {
  const t = useT();
  const minutes = options.includes(current) ? options : [...options, current].sort((a, b) => a - b);
  const name = label ?? t("cr.interval");
  return (
    <SettingRow label={name} width="w-[170px]">
      <Select
        compact
        aria-label={name}
        value={String(current)}
        onValueChange={(v) => Number(v) !== current && onPick(Number(v))}
        options={minutes.map((m) => ({ value: String(m), label: t("cr.interval.every", { time: everyText(t, m) }) }))}
      />
    </SettingRow>
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
    <section className={card}>
      <CardHead icon="list" title={t("bl.title")} sub={t("bl.sub")}>
        {s && (
          <Switch
            checked={s.enabled}
            disabled={act.update.isPending || act.disconnect.isPending}
            onCheckedChange={(on) => act.update.mutate({ enabled: on })}
            aria-label={t("bl.switch")}
          />
        )}
      </CardHead>
      {q.isPending && <Pending compact />}
      {q.isError && !s && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {/* switched off and on again, the card starts over, as the Globalping card does */}
      {s && <ListsBody key={String(s.enabled)} s={s} act={act} />}
    </section>
  );
}

function ListsBody({ s, act }: { s: BlocklistSettings; act: ListsActions }) {
  const t = useT();
  const toast = useToast();
  const [outcome, setOutcome] = useState<ListsOutcome | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  const state = listsState(s, outcome);

  const test = () =>
    act.test.mutate(undefined, {
      onSuccess: (ok) => setOutcome({ at: Date.now() / 1000, ok }),
      onError: (error) => (ConnectError.from(error).code === Code.ResourceExhausted ? toast.error(t("cr.tooSoon")) : setOutcome({ at: Date.now() / 1000, error })),
    });

  return (
    <>
      <ListsStateLine state={state} />
      {s.enabled && (
        <div className="flex flex-wrap gap-2">
          {/* another country than Russia: there is nothing to test */}
          {s.available && (
            <Button variant="secondary" size="sm" disabled={act.test.isPending} onClick={test}>
              {act.test.isPending ? t("cr.testing") : t("cr.test")}
            </Button>
          )}
          <Button variant="ghostDanger" size="sm" onClick={() => setDisconnecting(true)}>
            {t("cr.disconnect")}
          </Button>
        </div>
      )}
      <Privacy>{t("bl.privacy")}</Privacy>
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

/** The lists' state: their date, then what the last Test found (prefixes, CDN ranges, nodes in the registry) or when they were last compared. */
function ListsStateLine({ state }: { state: ListsState }) {
  const t = useT();
  const fmt = useFmt();
  const [kind, word, detail] = ((): [StatusKind, string, string] => {
    switch (state.kind) {
      case "off":
        return ["off", t("cr.state.off"), t("bl.state.off.detail")];
      case "unavailable":
        return ["off", t("cr.state.unavailable"), t("bl.state.onlyRU")];
      case "untested":
        return ["blip", t("cr.state.on"), t("cr.state.untested")];
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
        return ["ok", t("cr.state.works"), [state.listsAt ? t("bl.state.lists", { date: fmt.dateTime(state.listsAt) }) : "", ...parts].filter(Boolean).join(" · ")];
      }
      case "fails":
        return ["bad", t("cr.state.fails"), blocklistErrorText(state.error, t)];
    }
  })();
  return <StateLine state={state.kind} kind={kind} word={word} detail={detail} />;
}

/**
 * The TSPU probe: off by default; on, one of the owner's nodes in Russia (the probe node) finds the TSPU on its path every
 * interval and tests whether the TCP connections to the other nodes die there. Only for Russia, and not on the Cloudflare
 * edition: the card says so, and there is no switch on the edge, where it could not run.
 */
export function TspuCard() {
  const t = useT();
  const q = useQuery(tspuQuery);
  const act = useTspuActions();
  const s = q.data;
  return (
    <section className={card}>
      <CardHead icon="network" title={t("tp.title")} sub={t("tp.sub")}>
        {s && s.unavailableReason !== "edition" && (
          <Switch
            checked={s.enabled}
            disabled={act.update.isPending || act.disconnect.isPending}
            onCheckedChange={(on) => act.update.mutate({ enabled: on })}
            aria-label={t("tp.switch")}
          />
        )}
      </CardHead>
      {q.isPending && <Pending compact />}
      {q.isError && !s && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {/* switched off and on again, the card starts over, as the other cards do */}
      {s && <TspuBody key={String(s.enabled)} s={s} act={act} />}
    </section>
  );
}

function TspuBody({ s, act }: { s: TspuSettings; act: TspuActions }) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const [outcome, setOutcome] = useState<TspuOutcome | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  const state = tspuState(s, outcome, act.test.isPending);
  const ready = s.enabled && s.available;

  // a run takes minutes: the state line spins until it ends, and nothing times it out on this side
  const test = () => {
    const started = Math.floor(Date.now() / 1000);
    act.test.mutate(undefined, {
      onSuccess: (ok) => setOutcome({ at: ok.run?.atUnix || started, ok }),
      // asked again within the minute, or a scheduled run is on: not a failure of the probe
      onError: (error) => (isNotAFailure(error) ? toast(tspuErrorText(error, t)) : setOutcome({ at: started, error })),
    });
  };

  return (
    <>
      <TspuStateLine state={state} />
      {state.kind === "run" && state.nodes && state.nodes.length > 0 && <NodeResults nodes={state.nodes} />}
      {ready && (
        <div className="flex flex-col gap-3 border-t border-line pt-3">
          <SettingRow label={t("tp.node")} width="w-[200px]">
            <Select
              compact
              aria-label={t("tp.node")}
              value={s.probeNodeId}
              onValueChange={(v) => v !== s.probeNodeId && act.update.mutate({ probeNodeId: v })}
              options={probeNodeOptions(t, fmt, s.nodes, s.probeNodeId)}
            />
          </SettingRow>
          <IntervalField current={s.intervalMin || tspuDefaultInterval} options={tspuIntervals} onPick={(m) => act.update.mutate({ intervalMin: m })} />
        </div>
      )}
      {(s.enabled || s.probeNodeId !== "") && (
        <div className="flex flex-wrap gap-2">
          {ready && s.probeNodeId && (
            <Button variant="secondary" size="sm" disabled={act.test.isPending} onClick={test}>
              {act.test.isPending ? t("cr.testing") : t("cr.test")}
            </Button>
          )}
          <Button variant="ghostDanger" size="sm" onClick={() => setDisconnecting(true)}>
            {t("cr.disconnect")}
          </Button>
        </div>
      )}
      <Privacy>{t("tp.privacy")}</Privacy>
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

/** The probe's state: off, not here, no probe node, a run in flight (a spinner), or the last run: its status, time, hop and reference. */
function TspuStateLine({ state }: { state: TspuState }) {
  const t = useT();
  const fmt = useFmt();
  const [kind, word, detail] = ((): [StatusKind, string, string] => {
    switch (state.kind) {
      case "off":
        return ["off", t("cr.state.off"), t("tp.state.off.detail")];
      case "unavailable":
        return ["off", t("cr.state.unavailable"), t(state.reason === "edition" ? "tp.state.edition" : "tp.state.onlyRU")];
      case "noNode":
        return ["blip", t("cr.state.on"), t("tp.state.noNode")];
      case "untested":
        return ["blip", t("cr.state.on"), t("cr.state.untested")];
      case "running":
        return ["busy", t("cr.testing"), t("tp.state.running")];
      case "run": {
        const r = runLook(t, fmt, state.run);
        return [r.kind, r.word, r.detail];
      }
      case "fails":
        return ["bad", t("cr.state.fails"), tspuErrorText(state.error, t)];
    }
  })();
  return <StateLine state={state.kind === "run" ? `run-${state.run.status}` : state.kind} kind={kind} word={word} detail={detail} />;
}

const nodeVerdicts: Record<string, ["ok" | "warn", "cr.v.ok" | "cr.v.tcp" | "tp.v.partial"]> = {
  open: ["ok", "cr.v.ok"],
  blocked: ["warn", "cr.v.tcp"],
  partial: ["warn", "tp.v.partial"],
};

/** What the last Test found per node: its name, the verdict with its mark, the ports by what happened to them. */
function NodeResults({ nodes }: { nodes: TspuTest["nodes"] }) {
  const t = useT();
  return (
    <ul aria-label={t("tp.results")} className="flex flex-col divide-y divide-line rounded-field border border-line bg-canvas">
      {nodes.map((n) => {
        const [kind, key] = nodeVerdicts[n.verdict] ?? (["unknown", "cr.v.unknown"] as const);
        return (
          <li key={n.nodeId} data-testid="tspu-node" className="flex flex-wrap items-center gap-x-3 gap-y-0.5 px-3 py-2">
            <span className="min-w-14 font-mono text-[13px] font-bold">{n.name}</span>
            <span className={cx("flex items-center gap-1.5 text-xs font-bold", kind === "warn" ? "tone-warn tone-text" : "text-muted")}>
              <CountryMark kind={kind} />
              {t(key)}
            </span>
            <span className="ml-auto font-mono text-[11px] text-muted">{tspuPortsText(t, n.ports)}</span>
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
    <section className={card}>
      <CardHead icon="pulse" title={t("cc.title")} sub={t("cc.sub")}>
        {s && (
          <Switch
            checked={s.enabled}
            disabled={act.update.isPending || act.disconnect.isPending}
            // on asks first what leaves the panel and to whom; off needs nothing
            onCheckedChange={(on) => (on ? setAsking(true) : act.update.mutate({ enabled: false }))}
            aria-label={t("cc.switch")}
          />
        )}
      </CardHead>
      {q.isPending && <Pending compact />}
      {q.isError && !s && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {/* switched off and on again, the card starts over, as the other cards do */}
      {s && <CheburBody key={String(s.enabled)} s={s} act={act} />}
      {asking && (
        <ConfirmOn
          title={t("cc.consent.title")}
          body={t("cc.consent.body")}
          busy={act.update.isPending}
          onClose={() => setAsking(false)}
          onConfirm={() => act.update.mutate({ enabled: true }, { onSuccess: () => setAsking(false) })}
        />
      )}
    </section>
  );
}

function CheburBody({ s, act }: { s: CheburcheckSettings; act: CheburActions }) {
  const t = useT();
  const toast = useToast();
  const [outcome, setOutcome] = useState<CheburcheckOutcome | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  const state = cheburcheckState(s, outcome);
  const ready = s.enabled && !s.unavailable;
  const busy = act.update.isPending;

  const test = () =>
    act.test.mutate(undefined, {
      onSuccess: (ok) => setOutcome({ at: Date.now() / 1000, ok }),
      // asked again within the minute: not a failure of the site
      onError: (error) => (ConnectError.from(error).code === Code.ResourceExhausted ? toast.error(t("cr.tooSoon")) : setOutcome({ at: Date.now() / 1000, error })),
    });

  return (
    <>
      <CheburStateLine state={state} />
      {ready && (
        <div className="flex flex-col gap-3 border-t border-line pt-3">
          <SettingRow label={t("cc.public")} width="flex">
            <Switch checked={s.publicEnabled} disabled={busy} onCheckedChange={(v) => act.update.mutate({ publicEnabled: v })} aria-label={t("cc.public.switch")} />
          </SettingRow>
          {s.publicEnabled && (
            <IntervalField current={s.intervalMin || publicIntervals[0]} options={publicIntervals} onPick={(m) => act.update.mutate({ intervalMin: m })} />
          )}
          <SecretField
            label={t("cc.token")}
            hint={t("cc.token.hint")}
            has={s.hasToken}
            testId="cc-token"
            busy={busy}
            valid={looksLikeToken}
            invalid={t("cc.err.token")}
            saveLabel={t("gp.token.save")}
            onSave={(token, done) => act.update.mutate({ token }, { onSuccess: done })}
            onRemove={() => act.update.mutate({ token: "" })}
          />
          {s.hasToken && (
            <IntervalField
              label={t("cc.ownInterval")}
              current={s.ownIntervalMin || 60}
              options={ownIntervals}
              onPick={(m) => act.update.mutate({ ownIntervalMin: m })}
            />
          )}
        </div>
      )}
      {(s.enabled || s.hasToken) && (
        <div className="flex flex-wrap gap-2">
          {ready && (
            <Button variant="secondary" size="sm" disabled={act.test.isPending} onClick={test}>
              {act.test.isPending ? t("cr.testing") : t("cr.test")}
            </Button>
          )}
          <Button variant="ghostDanger" size="sm" onClick={() => setDisconnecting(true)}>
            {t("cr.disconnect")}
          </Button>
        </div>
      )}
      <Privacy>{t("cc.privacy")}</Privacy>
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
function CheburStateLine({ state }: { state: CheburcheckState }) {
  const t = useT();
  const fmt = useFmt();
  const [kind, word, detail] = ((): [StatusKind, string, string] => {
    switch (state.kind) {
      case "off":
        return ["off", t("cr.state.off"), t("cc.state.off.detail")];
      case "unavailable":
        return ["off", t("cr.state.unavailable"), t("cc.state.onlyRU")];
      case "untested":
        return ["blip", t("cr.state.on"), t("cc.state.waiting")];
      case "works": {
        if (state.test) return ["ok", t("cr.state.works"), t("cc.state.test")];
        const v = sourceVerdicts[state.verdict ?? ""];
        return ["ok", t("cr.state.works"), [t("cc.state.run", { ago: fmt.ago(state.runAt ?? 0) }), v ? t("cc.state.verdict", { verdict: t(v) }) : ""].filter(Boolean).join(" · ")];
      }
      case "fails":
        return ["bad", t("cr.state.fails"), cheburcheckErrorText(state.error, t)];
      case "status": {
        const [what, why] = statusLine(t, fmt, "cc", state.status, state.holdUntil);
        return [state.status === "token_unknown" ? "bad" : "warn", what, why || t("cc.status.later")];
      }
    }
  })();
  return <StateLine state={state.kind === "status" ? `status-${state.status}` : state.kind} kind={kind} word={word} detail={detail} />;
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
    <section className={card}>
      <CardHead icon="phone" title={t("bb.title")} sub={t("bb.sub")}>
        {s && (
          <Switch
            checked={s.enabled}
            // the server takes "on" only with a key and units: until then the switch waits, and the state line says why
            disabled={act.update.isPending || act.disconnect.isPending || (!s.enabled && !(s.hasKey && s.units.length > 0))}
            onCheckedChange={(on) => (on ? setAsking(true) : act.update.mutate({ enabled: false }))}
            aria-label={t("bb.switch")}
          />
        )}
      </CardHead>
      {q.isPending && <Pending compact />}
      {away && (
        <>
          <StateLine state="unavailable" kind="off" word={t("cr.state.unavailable")} detail={t("bb.state.onlyRU")} />
          <Privacy>{t("bb.privacy")}</Privacy>
        </>
      )}
      {q.isError && !s && !away && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {s && <BsbordBody key={String(s.enabled)} s={s} act={act} price={price} setPrice={setPrice} />}
      {asking && s && <BsbordConsent s={s} act={act} price={price} setPrice={setPrice} onClose={() => setAsking(false)} />}
    </section>
  );
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
          <Button variant="primary" size="md" disabled={busy} onClick={ask}>
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

function BsbordBody({ s, act, price, setPrice }: { s: BsbordSettings; act: BsbordActions } & PriceProps) {
  const t = useT();
  const toast = useToast();
  const [outcome, setOutcome] = useState<BsbordOutcome | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  const state = bsbordState(s, outcome);
  const busy = act.update.isPending;
  // a new interval or new units make the last price wrong
  const change = (req: { intervalMin?: number; units?: string[] }, done?: () => void) =>
    act.update.mutate(req, {
      onSuccess: () => {
        setPrice(null);
        done?.();
      },
    });

  const test = () =>
    act.test.mutate(undefined, {
      onSuccess: (ok) => setOutcome({ at: Date.now() / 1000, ok }),
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
      <BsbordStateLine state={state} />
      {s.enabled && <Spend s={s} test={outcome && "ok" in outcome ? outcome.ok : undefined} />}
      {s.enabled && (
        <div className="flex flex-col items-start gap-2">
          {price && <PriceLine s={s} price={price} />}
          <Button variant="secondary" size="sm" disabled={act.preview.isPending} onClick={ask}>
            {act.preview.isPending ? t("bb.price.checking") : t("bb.price.check")}
          </Button>
        </div>
      )}
      <div className="flex flex-col gap-3 border-t border-line pt-3">
        <SecretField
          label={t("bb.key")}
          hint={t("bb.key.hint")}
          has={s.hasKey}
          testId="bb-key"
          busy={busy}
          valid={looksLikeKey}
          invalid={t("bb.err.key")}
          saveLabel={t("bb.key.save")}
          onSave={(key, done) => act.update.mutate({ key }, { onSuccess: done })}
        />
        {s.hasKey && (
          <>
            <UnitPicker s={s} busy={busy} onSave={(units, done) => change({ units }, done)} />
            <IntervalField current={s.intervalMin || bsbordDefaultInterval} options={bsbordIntervals} onPick={(m) => change({ intervalMin: m })} />
            <CapField s={s} busy={busy} onSave={(capCredits) => act.update.mutate({ capCredits })} />
          </>
        )}
      </div>
      {(s.enabled || s.hasKey) && (
        <div className="flex flex-wrap gap-2">
          {s.hasKey && (
            <Button variant="secondary" size="sm" disabled={act.test.isPending} onClick={test}>
              {act.test.isPending ? t("cr.testing") : t("cr.test")}
            </Button>
          )}
          <Button variant="ghostDanger" size="sm" onClick={() => setDisconnecting(true)}>
            {t("cr.disconnect")}
          </Button>
        </div>
      )}
      <Privacy>{t("bb.privacy")}</Privacy>
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

/** The balance a Test read: "balance 523.40 ₽ · bonus 50 ₽". */
const balanceText = (t: T, fmt: Fmt, b: BsbordTest) =>
  [t("bb.test.balance", { balance: rub(fmt, b.balanceTotal) }), b.bonusCredits > 0 ? t("bb.test.bonus", { bonus: rub(fmt, b.bonusCredits) }) : ""].filter(Boolean).join(" · ");

const bsbordKinds: Record<string, StatusKind> = { pending: "busy", no_balance: "bad", tier_too_low: "bad", unauthorized: "bad", unavailable: "bad", rejected: "bad", cancelled: "blip" };

/** bsbord's state: off (and what is missing to switch it on), no round yet, the last round, a Test (balance, plan, units), or what the last round ended with. */
function BsbordStateLine({ state }: { state: BsbordState }) {
  const t = useT();
  const fmt = useFmt();
  const [kind, word, detail] = ((): [StatusKind, string, string] => {
    switch (state.kind) {
      case "off":
        return ["off", t("cr.state.off"), t(state.ready ? "bb.state.off.detail" : "bb.state.setup")];
      case "untested":
        return ["blip", t("cr.state.on"), t("bb.state.waiting")];
      case "works": {
        const b = state.test;
        const line = b
          ? [balanceText(t, fmt, b), b.tier ? t("bb.test.tier", { tier: b.tier }) : "", t.n("bb.test.units", b.nProbeable)].filter(Boolean).join(" · ")
          : t("bb.state.run", { ago: fmt.ago(state.runAt ?? 0) });
        return ["ok", t("cr.state.works"), line];
      }
      case "fails":
        return ["bad", t("cr.state.fails"), bsbordErrorText(state.error, t)];
      case "status": {
        const [what, why] = statusLine(t, fmt, "bb", state.status, state.holdUntil, [state.runAt ? t("bb.state.run", { ago: fmt.ago(state.runAt) }) : ""]);
        return [bsbordKinds[state.status] ?? "warn", what, why];
      }
    }
  })();
  return <StateLine state={state.kind === "status" ? `status-${state.status}` : state.kind} kind={kind} word={word} detail={detail} />;
}

/** This month's spend against the cap, in words and a bar; what a round under way holds back; the balance from the last Test. */
function Spend({ s, test }: { s: BsbordSettings; test?: BsbordTest }) {
  const t = useT();
  const fmt = useFmt();
  const used = s.spentCredits + s.reservedCredits;
  const share = Math.min(1, used / Math.max(1, s.capCredits));
  const tone = share >= 1 || s.lastStatus === "cap_reached" ? "tone-bad" : share >= 0.8 ? "tone-warn" : "tone-ok";
  const words = t("bb.spend.of", { spent: rub(fmt, s.spentCredits), cap: rub(fmt, s.capCredits) });
  const notes = [s.reservedCredits > 0 ? t("bb.spend.held", { held: rub(fmt, s.reservedCredits) }) : "", test ? balanceText(t, fmt, test) : ""].filter(Boolean);
  return (
    <div data-testid="bsbord-spend" className="flex flex-col gap-1.5 rounded-field border border-line bg-canvas px-3 py-2.5">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 text-[13px]">
        <span className="font-bold">{t("bb.spend")}</span>
        <span>
          <b className="font-semibold">{words}</b>
          <span className="text-muted"> · {t("bb.spend.left", { left: rub(fmt, Math.max(0, s.capCredits - used)) })}</span>
        </span>
      </div>
      <div
        role="meter"
        aria-label={t("bb.spend")}
        aria-valuemin={0}
        aria-valuemax={s.capCredits}
        aria-valuenow={Math.min(used, s.capCredits)}
        aria-valuetext={words}
        className="h-1.5 overflow-hidden rounded-full bg-surface-2"
      >
        <div className={cx("h-full rounded-full bg-(--c)", tone)} style={{ width: `${share * 100}%` }} />
      </div>
      {notes.length > 0 && <span className="text-xs text-muted">{notes.join(" · ")}</span>}
    </div>
  );
}

/**
 * The units by district: Crimea first (the server's default until a selection is saved), each district behind a disclosure with
 * "one per operator" (each operator's first unit there without the white list); a unit under the white list says so, one that
 * cannot probe now is greyed. Nothing goes to the server until Save.
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
    <details open={s.units.length === 0 || undefined} className="group rounded-field border border-line bg-canvas">
      <summary className="flex min-h-11 cursor-pointer list-none items-center gap-2 px-3 py-2 text-[13px] [&::-webkit-details-marker]:hidden">
        <span className="font-bold">{t("bb.units")}</span>
        <span className="text-xs text-muted">{t("bb.units.picked", { n: current.size })}</span>
        <Icon name="chevronDown" size={14} aria-hidden className="ml-auto text-muted transition-transform group-open:rotate-180" />
      </summary>
      <div className="flex flex-col gap-2.5 border-t border-line px-3 py-2.5">
        <p className="text-xs leading-normal text-pretty text-muted">{t("bb.units.sub")}</p>
        {q.isPending && <Pending compact />}
        {q.isError && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
        {units.length > 0 && (
          <>
            <div className="flex flex-wrap gap-2">
              <Button variant="secondary" size="sm" disabled={busy} onClick={() => set([...current, ...crimeaPreset(units)])}>
                {t("bb.units.crimea")}
              </Button>
            </div>
            <ul className="flex flex-col divide-y divide-line rounded-field border border-line bg-surface">
              {districts(units, fmt.locale).map((d) => (
                <li key={d.code}>
                  <details open={d.units.some((u) => open.has(u.opKey)) || undefined} className="group/d">
                    <summary className="flex min-h-10 cursor-pointer list-none items-center gap-2 px-3 py-2 text-[13px] [&::-webkit-details-marker]:hidden">
                      <span className="font-semibold">{d.name}</span>
                      <span className="text-xs text-muted">{t("bb.units.picked", { n: d.units.filter((u) => current.has(u.opKey)).length })}</span>
                      <Icon name="chevronDown" size={14} aria-hidden className="ml-auto text-muted transition-transform group-open/d:rotate-180" />
                    </summary>
                    <div className="flex flex-col items-start gap-1.5 px-3 pb-2.5">
                      {perOperatorPreset(d.units, d.code).length > 0 && (
                        <Button variant="secondary" size="sm" disabled={busy} onClick={() => set([...current, ...perOperatorPreset(d.units, d.code)])}>
                          {t("bb.units.perOperator")}
                        </Button>
                      )}
                      <ul aria-label={d.name} className="flex w-full flex-col gap-1">
                        {d.units.map((u) => (
                          <UnitRow key={u.opKey} u={u} checked={current.has(u.opKey)} busy={busy} onToggle={(on) => toggle(u.opKey, on)} />
                        ))}
                      </ul>
                    </div>
                  </details>
                </li>
              ))}
            </ul>
          </>
        )}
        {dirty && (
          <div className="flex flex-wrap items-center justify-end gap-2">
            {picked && (
              <Button variant="ghost" size="md" disabled={busy} onClick={() => setPicked(null)}>
                {t("bb.units.undo")}
              </Button>
            )}
            <Button variant="secondary" size="md" disabled={busy || current.size === 0} onClick={() => onSave([...current].sort(), () => setPicked(null))}>
              {t("bb.units.save")}
            </Button>
          </div>
        )}
      </div>
    </details>
  );
}

/** One unit: its box, the operator and the unit's name, the white-list mark, and "cannot probe now" greyed (it stays tickable off). */
function UnitRow({ u, checked, busy, onToggle }: { u: BsbordUnit; checked: boolean; busy: boolean; onToggle: (on: boolean) => void }) {
  const t = useT();
  return (
    <li>
      <label className={cx("flex min-h-8 items-start gap-2.5 py-0.5 text-[13px]", !u.probeable && "text-muted")}>
        <input
          type="checkbox"
          className="mt-0.5 size-4 flex-none accent-accent"
          checked={checked}
          disabled={busy || (!u.probeable && !checked)}
          onChange={(e) => onToggle(e.target.checked)}
        />
        <span className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
          <span className="font-semibold">{u.operator}</span>
          {u.name && u.name !== u.operator && <span className="text-xs text-muted">{u.name}</span>}
          {isWhiteList(u) && <span className="tone-warn tint tone-text rounded-full px-1.5 text-[10px] leading-4 font-bold">{t("bb.units.white")}</span>}
          {!u.probeable && <span className="text-[11px]">{t("bb.units.cannot")}</span>}
        </span>
      </label>
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
    <form onSubmit={submit} noValidate className="flex flex-col gap-3">
      <TextField
        label={t("bb.cap")}
        hint={t("bb.cap.hint")}
        error={valid ? undefined : t("bb.err.cap")}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        inputMode="decimal"
        autoComplete="off"
        mono
      />
      <div className="flex justify-end">
        <Button type="submit" variant="secondary" size="md" disabled={busy || !valid || credits === s.capCredits}>
          {t("bb.cap.save")}
        </Button>
      </div>
    </form>
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
  const [disconnecting, setDisconnecting] = useState(false);
  const p = q.data;
  const on = !!p?.settings?.enabled;
  return (
    <section className={card}>
      <CardHead icon="people" title={t("kc.title")} sub={t("kc.sub")}>
        {p && (
          <Switch
            checked={on}
            disabled={act.update.isPending || act.disconnect.isPending}
            onCheckedChange={(v) => act.update.mutate({ enabled: v })}
            aria-label={t("kc.switch")}
          />
        )}
      </CardHead>
      {q.isPending && <Pending compact />}
      {q.isError && !p && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {p && (
        <>
          <KlickStateLine p={p} />
          {on && p.nodes.length > 0 && <KlickNodes nodes={p.nodes} />}
          {on && (
            <div className="flex flex-wrap gap-2">
              <Button variant="ghostDanger" size="sm" onClick={() => setDisconnecting(true)}>
                {t("cr.disconnect")}
              </Button>
            </div>
          )}
        </>
      )}
      <Privacy>{t("kc.privacy")}</Privacy>
      {disconnecting && (
        <ConfirmDisconnect
          title={t("kc.disconnect.title")}
          body={t("kc.disconnect.body")}
          busy={act.disconnect.isPending}
          onClose={() => setDisconnecting(false)}
          onConfirm={() => act.disconnect.mutate(undefined, { onSuccess: () => setDisconnecting(false) })}
        />
      )}
    </section>
  );
}

function KlickStateLine({ p }: { p: ClientProbe }) {
  const t = useT();
  const fmt = useFmt();
  const state = clientProbeState(p);
  const [kind, word, detail] = ((): [StatusKind, string, string] => {
    switch (state.kind) {
      case "off":
        return ["off", t("cr.state.off"), t("kc.state.off.detail")];
      case "waiting":
        return ["blip", t("cr.state.on"), t("kc.state.waiting")];
      case "works":
        return ["ok", t("cr.state.works"), [t.n("kc.state.reports", state.reports), t("kc.state.last", { ago: fmt.ago(state.lastAt) })].join(" · ")];
    }
  })();
  return <StateLine state={state.kind} kind={kind} word={word} detail={detail} />;
}

/** Per node: its name, what went wrong (or "ok"), how many reports and the newest; under it the operators and the regions. */
function KlickNodes({ nodes }: { nodes: ClientProbeNode[] }) {
  const t = useT();
  const fmt = useFmt();
  const names = new Map((useQuery(nodesQuery).data?.nodes ?? []).map((n) => [n.id, n.name]));
  return (
    <ul aria-label={t("kc.results")} className="flex flex-col divide-y divide-line rounded-field border border-line bg-canvas">
      {nodes.map((n) => {
        const x = n.tally!;
        const kind = nodeKind(x);
        const trouble = troubleText(t, x);
        return (
          <li key={n.nodeId} data-testid="klick-node" className="flex flex-col gap-1 px-3 py-2">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5">
              <span className="min-w-14 font-mono text-[13px] font-bold">{names.get(n.nodeId) ?? n.nodeId}</span>
              <span className={cx("flex items-center gap-1.5 text-xs font-bold", kind === "warn" ? "tone-warn tone-text" : "text-muted")}>
                <CountryMark kind={kind} />
                {trouble || t("kc.ok")}
              </span>
              <span className="ml-auto font-mono text-[11px] text-muted">{[t.n("kc.reports", x.reports), fmt.ago(x.lastUnix)].join(" · ")}</span>
            </div>
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
    <p className="text-xs leading-normal text-pretty text-muted">
      <b className="font-semibold text-fg">{label}:</b> {groups.map((g) => groupText(t, g.name, g.tally!)).join("; ")}
    </p>
  );
}
