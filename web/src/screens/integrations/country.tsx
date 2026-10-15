import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon, type IconName } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { StatusDot, type StatusKind } from "@/components/ui/status";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { useT, type T } from "@/i18n";
import { blocklistErrorText, blocklistQuery, listsState, useBlocklistActions, type BlocklistSettings, type ListsOutcome, type ListsState } from "@/lib/blocklist";
import { cx } from "@/lib/cx";
import { minutesText, useFmt } from "@/lib/format";
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
import { checksQuery, countryReason, tspuPortsText } from "@/lib/health";
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
import { card } from "./parts";

type Actions = ReturnType<typeof useGlobalpingActions>;
type ListsActions = ReturnType<typeof useBlocklistActions>;
type TspuActions = ReturnType<typeof useTspuActions>;

/**
 * "Reachability from a country": the country every source looks from, once, in the group's header, then a card per source:
 * Globalping, the block lists and the TSPU probe, two to a row on a wide screen.
 */
export function CountryGroup() {
  const t = useT();
  return (
    <section aria-labelledby="country-group" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end gap-x-6 gap-y-2.5 pt-1.5">
        <div className="flex min-w-[240px] flex-1 flex-col gap-1">
          <h2 id="country-group" className="text-[15px] font-extrabold tracking-[-0.02em]">
            {t("cr.group.title")}
          </h2>
          <p className="max-w-2xl text-xs leading-normal text-pretty text-muted">{t("cr.group.sub")}</p>
        </div>
        <CountryPicker />
      </div>
      <div className="grid items-start gap-3.5 xl:grid-cols-2">
        <GlobalpingCard />
        <BlocklistCard />
        <TspuCard />
      </div>
    </section>
  );
}

/** The shared country: it only says where to look from and sends nothing, so it saves on the spot without a step-up. */
function CountryPicker() {
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
          <TokenField s={s} act={act} />
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

/** Write-only: with a token stored the card says "set" and offers to replace it; the token itself never comes back. */
function TokenField({ s, act }: { s: GlobalpingSettings; act: Actions }) {
  const t = useT();
  const [editing, setEditing] = useState(false);
  const [token, setToken] = useState("");
  const [tried, setTried] = useState(false);
  const busy = act.update.isPending;

  function stop() {
    setEditing(false);
    setToken("");
    setTried(false);
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    setTried(true);
    if (busy || !looksLikeToken(token)) return;
    act.update.mutate({ token: token.trim() }, { onSuccess: stop });
  }

  if (s.hasToken && !editing)
    return (
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-field border border-line bg-canvas px-3 py-2.5">
        <span className="text-[13px] font-bold">{t("gp.token")}</span>
        <span data-testid="gp-token" className="tone-ok tone-text text-xs font-semibold">
          {t("gp.token.set")}
        </span>
        <span className="ml-auto">
          <Button variant="secondary" size="sm" onClick={() => setEditing(true)}>
            {t("gp.token.replace")}
          </Button>
        </span>
      </div>
    );

  return (
    <form onSubmit={submit} noValidate className="flex flex-col gap-3">
      <TextField
        label={t("gp.token")}
        hint={t("gp.token.hint")}
        error={tried && !looksLikeToken(token) ? t("gp.err.token") : undefined}
        value={token}
        onChange={(e) => setToken(e.target.value)}
        type="password"
        autoComplete="off"
        spellCheck={false}
        mono
      />
      <div className="flex flex-wrap justify-end gap-2">
        {s.hasToken && (
          <Button variant="ghost" size="md" disabled={busy} onClick={stop}>
            {t("common.cancel")}
          </Button>
        )}
        <Button type="submit" variant="secondary" size="md" disabled={busy}>
          {busy && token ? t("gp.token.saving") : t("gp.token.save")}
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
function IntervalField({ current, options, onPick }: { current: number; options: readonly number[]; onPick: (minutes: number) => void }) {
  const t = useT();
  const minutes = options.includes(current) ? options : [...options, current].sort((a, b) => a - b);
  return (
    <SettingRow label={t("cr.interval")} width="w-[170px]">
      <Select
        compact
        aria-label={t("cr.interval")}
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
