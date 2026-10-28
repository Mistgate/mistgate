import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { isStepUpCancelled } from "@/components/step-up";
import { createContext, useContext, useLayoutEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { ActionBar, Block } from "@/components/ui/bits";
import { BackButton, DetailCard, useTurns, walkTabs } from "@/components/ui/master-detail";
import { Button } from "@/components/ui/button";
import { Icon, IconChip, type IconName } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { StatusDot, StatusPill, type StatusKind } from "@/components/ui/status";
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
import { isSourceId, sourceIds, type SourceId } from "./search";

type Actions = ReturnType<typeof useGlobalpingActions>;
type ListsActions = ReturnType<typeof useBlocklistActions>;
type TspuActions = ReturnType<typeof useTspuActions>;
type CheburActions = ReturnType<typeof useCheburcheckActions>;
type BsbordActions = ReturnType<typeof useBsbordActions>;

/** Each source in the list: its icon, its name, what it is in a few words, and whether it looks from Russia only. */
const meta: Record<SourceId, { icon: IconName; title: MessageKey; what: MessageKey; ruOnly: boolean; Card: () => ReactNode }> = {
  globalping: { icon: "globe", title: "gp.title", what: "src.what.globalping", ruOnly: false, Card: GlobalpingCard },
  lists: { icon: "list", title: "bl.title", what: "src.what.lists", ruOnly: true, Card: BlocklistCard },
  tspu: { icon: "network", title: "tp.title", what: "src.what.tspu", ruOnly: true, Card: TspuCard },
  cheburcheck: { icon: "pulse", title: "cc.title", what: "src.what.cheburcheck", ruOnly: true, Card: CheburcheckCard },
  bsbord: { icon: "phone", title: "bb.title", what: "src.what.bsbord", ruOnly: true, Card: BsbordCard },
  klick: { icon: "people", title: "kc.title", what: "src.what.klick", ruOnly: false, Card: KlickCard },
};

/** What a source's state line says. The line reports it up, so the row of the list and the open source never disagree. */
type Look = { state: string; kind: StatusKind; word: string; detail: string };
const ReportLook = createContext<((look: Look) => void) | null>(null);

/**
 * The sources of the Reachability page as a list and the open one: Globalping, the block lists, the TSPU probe, cheburcheck,
 * bsbord, the reports of kl!ck users. A wide screen shows the list on the left (it stays in view) and the source on the right; a
 * mid one puts the list on top as tiles; a phone shows the list, and a tap opens the source with a way back. The open source is
 * `?source=` in the URL; without it, the first one that is on, else Globalping. Every source stays mounted, only the open one is
 * shown: a key half typed, a test under way and its answer survive a look at another. Owner only, like the services behind them;
 * the country they all look from is the picker in the page's header (the kl!ck reports come from wherever the people are).
 */
export function SourceCards() {
  const t = useT();
  const navigate = useNavigate();
  const asked = (useSearch({ strict: false }) as { source?: unknown }).source;
  const chosen = isSourceId(asked) ? asked : undefined;

  // the first source that is on, once every one has answered; then it stays, so switching it off does not move the page
  const on = [
    useQuery({ ...globalpingQuery, select: (s) => s.enabled }),
    useQuery({ ...blocklistQuery, select: (s) => s.enabled }),
    useQuery({ ...tspuQuery, select: (s) => s.enabled }),
    useQuery({ ...cheburcheckQuery, select: (s) => s.enabled }),
    useQuery({ ...bsbordQuery, select: (s) => s.enabled }),
    useQuery({ ...clientProbeQuery, select: (p) => !!p.settings?.enabled }),
  ];
  const [first, setFirst] = useState<SourceId | null>(null);
  if (first === null && on.every((q) => !q.isPending)) setFirst(sourceIds.find((_, i) => on[i]!.data) ?? "globalping");
  const open = chosen ?? first;

  const [looks, setLooks] = useState<Partial<Record<SourceId, Look>>>({});
  const reporters = useMemo(
    () => Object.fromEntries(sourceIds.map((id) => [id, (look: Look) => setLooks((all) => ({ ...all, [id]: look }))])) as Record<SourceId, (look: Look) => void>,
    [],
  );

  // a phone takes turns: opening moves the focus to the way back, going back (the link or the browser's) to the row left
  const { pick, back } = useTurns({
    chosen,
    backId: "src-back",
    rowId: (id) => `src-tab-${id}`,
    open: (id, replace) => void navigate({ to: "/reachability", search: { source: id }, replace, resetScroll: false }),
    close: () => void navigate({ to: "/reachability", search: {}, replace: true, resetScroll: false }),
  });

  return (
    <div className="grid gap-3.5 lg:grid-cols-[336px_minmax(0,1fr)]">
      <div
        role="tablist"
        aria-label={t("rc.sources")}
        aria-orientation="vertical"
        onKeyDown={walkTabs}
        className={cx(
          "flex flex-col gap-1 self-start rounded-card-lg border border-line bg-surface p-1.5 min-[720px]:max-lg:grid min-[720px]:max-lg:grid-cols-2 lg:sticky lg:top-[72px]",
          chosen && "max-[720px]:hidden",
        )}
      >
        {sourceIds.map((id) => (
          <SourceRow key={id} id={id} look={looks[id]} selected={id === open} tabbable={id === (open ?? sourceIds[0])} onPick={() => pick(id)} />
        ))}
      </div>
      <div className={cx("flex min-w-0 flex-col gap-2.5", !chosen && "max-[720px]:hidden")}>
        <BackButton id="src-back" label={t("src.back")} onClick={back} />
        {open === null && <Pending />}
        {sourceIds.map((id) => {
          const { Card } = meta[id];
          return (
            <div key={id} role="tabpanel" id={`src-panel-${id}`} aria-labelledby={`src-tab-${id}`} hidden={id !== open} className="flex-1">
              <ReportLook.Provider value={reporters[id]}>
                <Card />
              </ReportLook.Provider>
            </div>
          );
        })}
      </div>
    </div>
  );
}

/** The list's word for a state, short enough for a row: works, off, an error, needs setup; and a few words on why or when. */
function chipOf(t: T, look: Look): { kind: StatusKind; word: string; note: string } {
  const head = look.detail.split(" · ")[0] ?? "";
  // a state line says "Does not work · why" or names its status itself ("The site asked to slow down · …"): the row takes the why
  const why = [t("cr.state.works"), t("cr.state.fails"), t("cr.state.on"), t("cr.testing")].includes(look.word) ? head : look.word;
  switch (look.kind) {
    case "ok":
      return { kind: "ok", word: t("cr.state.works"), note: head };
    case "off":
      return { kind: "off", word: look.word, note: "" };
    case "bad":
      return { kind: "bad", word: t("src.chip.error"), note: why };
    case "warn":
      return { kind: "warn", word: t("status.warn"), note: why };
    case "busy":
      return { kind: "busy", word: t("src.chip.busy"), note: why };
    case "blip":
      return look.state === "noNode" ? { kind: "blip", word: t("src.chip.setup"), note: head } : { kind: "blip", word: t("cr.state.on"), note: why };
  }
}

/** "Russia only" or "any country": the sources that need Russia say so before anyone opens them. */
function Coverage({ ruOnly }: { ruOnly: boolean }) {
  const t = useT();
  return (
    <span className="inline-flex h-[18px] flex-none items-center rounded-[6px] bg-surface-2 px-1.5 text-[10px] font-bold whitespace-nowrap text-muted">
      {t(ruOnly ? "src.cover.ru" : "src.cover.any")}
    </span>
  );
}

/**
 * A row of the list: the icon, the name and the state's word on the first line, what the source is under it, then where it
 * looks from and the state's few words. All rows are alike in height; on a mid screen a row is a tile (icon, name, word).
 */
function SourceRow({ id, look, selected, tabbable, onPick }: { id: SourceId; look?: Look; selected: boolean; tabbable: boolean; onPick: () => void }) {
  const t = useT();
  const m = meta[id];
  const chip = look && chipOf(t, look);
  const tile = "min-[720px]:max-lg:hidden";
  return (
    <button
      type="button"
      role="tab"
      id={`src-tab-${id}`}
      aria-selected={selected}
      aria-controls={`src-panel-${id}`}
      tabIndex={tabbable ? 0 : -1}
      onClick={onPick}
      className="grid w-full min-w-0 scroll-mt-20 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 rounded-card px-3 py-2.5 text-left transition-colors duration-200 hover:bg-surface-2 min-[720px]:aria-selected:bg-surface-2 min-[720px]:aria-selected:shadow-[inset_0_0_0_1px_var(--accent-line)] min-[720px]:max-lg:gap-x-2.5 min-[720px]:max-lg:px-2.5"
    >
      <IconChip icon={m.icon} tone="mint" size={30} className="row-span-3 self-start rounded-[9px] min-[720px]:max-lg:row-span-2" />
      <span className="truncate text-[13px] font-bold">{t(m.title)}</span>
      {/* the word's place is kept while the source loads: no row grows when it arrives */}
      <span className="flex h-[22px] items-center justify-self-end min-[720px]:max-lg:col-start-2 min-[720px]:max-lg:row-start-2 min-[720px]:max-lg:justify-self-start">
        {chip && <StatusPill sm kind={chip.kind} label={chip.word} />}
      </span>
      <span className={cx("col-span-2 truncate text-xs text-muted", tile)}>{t(m.what)}</span>
      <span className={cx("col-span-2 flex min-w-0 items-center gap-2", tile)}>
        <Coverage ruOnly={m.ruOnly} />
        <span className="ml-auto truncate text-[11px] text-muted" title={chip?.note || undefined}>
          {chip?.note}
        </span>
      </span>
    </button>
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
    <Detail
      id="globalping"
      sub={t("gp.sub")}
      control={
        s && (
          <Switch
            checked={s.enabled}
            disabled={act.update.isPending || act.disconnect.isPending}
            onCheckedChange={(on) => act.update.mutate({ enabled: on })}
            aria-label={t("gp.switch")}
          />
        )
      }
    >
      <Loading q={q} />
      {/* switched off and on again, the card starts over: the last test said nothing about the new state */}
      {s && <Body key={String(s.enabled)} s={s} act={act} />}
    </Detail>
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
      <Block flush>
        <GlobalpingState state={state} />
      </Block>
      {s.enabled && (
        <Block title={t("src.block.connection")}>
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
        </Block>
      )}
      {s.enabled && (
        <Block title={t("src.block.checks")}>
          <IntervalField current={s.intervalMin || 15} options={intervalOptions} onPick={(m) => act.update.mutate({ intervalMin: m })} />
          {/* IPv6: a round of its own with its own control site, for the nodes whose IPv6 works; it takes more of the hour */}
          <SettingRow label={t("gp.ipv6")} hint={t("gp.ipv6.hint")} width="flex">
            <Switch checked={s.ipv6} disabled={act.update.isPending} onCheckedChange={(v) => act.update.mutate({ ipv6: v })} aria-label={t("gp.ipv6")} />
          </SettingRow>
        </Block>
      )}
      <Privacy>{t("gp.privacy")}</Privacy>
      {(s.enabled || s.hasToken) && (
        <ActionBar>
          {s.enabled && (
            <Button variant="secondary" size="md" disabled={act.test.isPending} onClick={test}>
              {act.test.isPending ? t("cr.testing") : t("cr.test")}
            </Button>
          )}
          <Disconnect onClick={() => setDisconnecting(true)} />
        </ActionBar>
      )}
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
 * The open source: its header (icon, name, where it looks from, what it does, the on/off switch), then its blocks. The switch
 * lives here and only here: on asks for consent and setup on some sources, and a switch inside a row of the list would fight
 * the row's own click and the arrow keys. The panel is as tall as the list at least; a short source leaves the room at its end.
 */
function Detail({ id, sub, control, children }: { id: SourceId; sub: string; control?: ReactNode; children: ReactNode }) {
  const t = useT();
  const m = meta[id];
  return (
    <DetailCard icon={m.icon} title={t(m.title)} extra={<Coverage ruOnly={m.ruOnly} />} sub={sub} control={control}>
      {children}
    </DetailCard>
  );
}

/** The source is loading, or could not be read: in the place of its state. */
function Loading({ q }: { q: { isPending: boolean; isError: boolean; data: unknown; error: Error | null; refetch: () => unknown } }) {
  if (q.isPending)
    return (
      <Block flush>
        <Pending compact />
      </Block>
    );
  if (q.isError && !q.data)
    return (
      <Block flush>
        <QueryError compact error={q.error} onRetry={() => void q.refetch()} />
      </Block>
    );
  return null;
}

/** What leaves the panel with the source on, in every state. */
function Privacy({ children }: { children: string }) {
  const t = useT();
  return (
    <Block title={t("src.block.sent")}>
      <p className="max-w-2xl text-[13px] leading-normal text-pretty text-muted">{children}</p>
    </Block>
  );
}

function Disconnect({ onClick }: { onClick: () => void }) {
  const t = useT();
  return (
    <Button variant="ghostDanger" size="md" className="ml-auto" onClick={onClick}>
      {t("cr.disconnect")}
    </Button>
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

/** One line: a dot, the state in a word, the detail. data-state names the state for the tests. It also tells the list. */
function StateLine({ state, kind, word, detail }: { state: string; kind: StatusKind; word: string; detail: string }) {
  const report = useContext(ReportLook);
  // before the paint: the row and the line change in the same frame
  useLayoutEffect(() => {
    report?.({ state, kind, word, detail });
  }, [report, state, kind, word, detail]);
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
      <SettingRow label={p.label}>
        <div className="flex flex-wrap items-center gap-2">
          <span data-testid={p.testId} className="tone-ok tone-text mr-1 inline-flex items-center gap-1.5 text-[13px] font-semibold">
            <Icon name="check" size={14} aria-hidden />
            {t("gp.token.set")}
          </span>
          <Button variant="secondary" size="sm" onClick={() => setEditing(true)}>
            {t("gp.token.replace")}
          </Button>
          {p.onRemove && (
            <Button variant="ghostDanger" size="sm" disabled={p.busy} onClick={p.onRemove}>
              {t("cc.token.remove")}
            </Button>
          )}
        </div>
      </SettingRow>
    );

  return (
    <SettingRow label={p.label} top width="basis-full @min-[540px]:w-full">
      <form onSubmit={submit} noValidate className="flex flex-wrap items-start gap-2">
        <TextField
          aria-label={p.label}
          hint={p.hint}
          error={tried && !p.valid(value) ? p.invalid : undefined}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          type="password"
          autoComplete="off"
          spellCheck={false}
          mono
          className="min-w-[200px] flex-1"
        />
        <span className="mt-1 flex gap-2">
          <Button type="submit" variant="secondary" size="md" disabled={p.busy}>
            {p.busy && value ? t("gp.token.saving") : p.saveLabel}
          </Button>
          {p.has && (
            <Button variant="ghost" size="md" disabled={p.busy} onClick={stop}>
              {t("common.cancel")}
            </Button>
          )}
        </span>
      </form>
    </SettingRow>
  );
}

/**
 * A setting of a source: on a wide panel its name sits in a column of its own and the control starts at one line for all; the hint
 * goes under the control. On a narrow one the name is on the left of a short control (a wide one wraps under it). `top` is for a
 * tall control (a field): the name lines up with its first line, not its middle.
 */
function SettingRow({ label, hint, top, width, children }: { label: string; hint?: string; top?: boolean; width?: string; children: ReactNode }) {
  return (
    <div
      className={cx(
        "flex flex-wrap items-center justify-between gap-x-3 gap-y-2 @min-[540px]:grid @min-[540px]:grid-cols-[176px_minmax(0,1fr)] @min-[540px]:gap-x-6",
        top && "@min-[540px]:items-start",
      )}
    >
      <span aria-hidden className={cx("text-[13px] leading-snug font-bold", top && "@min-[540px]:pt-3")}>
        {label}
      </span>
      <div className={cx("min-w-0 @min-[540px]:justify-self-start", width)}>{children}</div>
      {hint && <p className="max-w-xl basis-full text-xs leading-normal text-pretty text-muted @min-[540px]:col-start-2 @min-[540px]:-mt-1">{hint}</p>}
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
    <SettingRow label={name} width="w-[200px]">
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
    <Detail
      id="lists"
      sub={t("bl.sub")}
      control={
        s && (
          <Switch
            checked={s.enabled}
            disabled={act.update.isPending || act.disconnect.isPending}
            onCheckedChange={(on) => act.update.mutate({ enabled: on })}
            aria-label={t("bl.switch")}
          />
        )
      }
    >
      <Loading q={q} />
      {/* switched off and on again, the card starts over, as the Globalping card does */}
      {s && <ListsBody key={String(s.enabled)} s={s} act={act} />}
    </Detail>
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
      <Block flush>
        <ListsStateLine state={state} />
      </Block>
      <Privacy>{t("bl.privacy")}</Privacy>
      {s.enabled && (
        <ActionBar>
          {/* another country than Russia: there is nothing to test */}
          {s.available && (
            <Button variant="secondary" size="md" disabled={act.test.isPending} onClick={test}>
              {act.test.isPending ? t("cr.testing") : t("cr.test")}
            </Button>
          )}
          <Disconnect onClick={() => setDisconnecting(true)} />
        </ActionBar>
      )}
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
    <Detail
      id="tspu"
      sub={t("tp.sub")}
      control={
        s &&
        s.unavailableReason !== "edition" && (
          <Switch
            checked={s.enabled}
            disabled={act.update.isPending || act.disconnect.isPending}
            onCheckedChange={(on) => act.update.mutate({ enabled: on })}
            aria-label={t("tp.switch")}
          />
        )
      }
    >
      <Loading q={q} />
      {/* switched off and on again, the card starts over, as the other cards do */}
      {s && <TspuBody key={String(s.enabled)} s={s} act={act} />}
    </Detail>
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
      <Block flush>
        <TspuStateLine state={state} />
        {state.kind === "run" && state.nodes && state.nodes.length > 0 && <NodeResults nodes={state.nodes} />}
      </Block>
      {ready && (
        <Block title={t("src.block.checks")}>
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
        </Block>
      )}
      <Privacy>{t("tp.privacy")}</Privacy>
      {(s.enabled || s.probeNodeId !== "") && (
        <ActionBar>
          {ready && s.probeNodeId && (
            <Button variant="secondary" size="md" disabled={act.test.isPending} onClick={test}>
              {act.test.isPending ? t("cr.testing") : t("cr.test")}
            </Button>
          )}
          <Disconnect onClick={() => setDisconnecting(true)} />
        </ActionBar>
      )}
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
    <Detail
      id="cheburcheck"
      sub={t("cc.sub")}
      control={
        s && (
          <Switch
            checked={s.enabled}
            disabled={act.update.isPending || act.disconnect.isPending}
            // on asks first what leaves the panel and to whom; off needs nothing
            onCheckedChange={(on) => (on ? setAsking(true) : act.update.mutate({ enabled: false }))}
            aria-label={t("cc.switch")}
          />
        )
      }
    >
      <Loading q={q} />
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
    </Detail>
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
      <Block flush>
        <CheburStateLine state={state} />
      </Block>
      {ready && (
        <Block title={t("src.block.connection")}>
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
        </Block>
      )}
      {ready && (
        <Block title={t("src.block.checks")}>
          <SettingRow label={t("cc.public")} width="flex">
            <Switch checked={s.publicEnabled} disabled={busy} onCheckedChange={(v) => act.update.mutate({ publicEnabled: v })} aria-label={t("cc.public.switch")} />
          </SettingRow>
          {s.publicEnabled && (
            <IntervalField current={s.intervalMin || publicIntervals[0]} options={publicIntervals} onPick={(m) => act.update.mutate({ intervalMin: m })} />
          )}
          {s.hasToken && (
            <IntervalField
              label={t("cc.ownInterval")}
              current={s.ownIntervalMin || 60}
              options={ownIntervals}
              onPick={(m) => act.update.mutate({ ownIntervalMin: m })}
            />
          )}
        </Block>
      )}
      <Privacy>{t("cc.privacy")}</Privacy>
      {(s.enabled || s.hasToken) && (
        <ActionBar>
          {ready && (
            <Button variant="secondary" size="md" disabled={act.test.isPending} onClick={test}>
              {act.test.isPending ? t("cr.testing") : t("cr.test")}
            </Button>
          )}
          <Disconnect onClick={() => setDisconnecting(true)} />
        </ActionBar>
      )}
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
    <Detail
      id="bsbord"
      sub={t("bb.sub")}
      control={
        s && (
          <Switch
            checked={s.enabled}
            // the server takes "on" only with a key and units: until then the switch waits, and the state line says why
            disabled={act.update.isPending || act.disconnect.isPending || (!s.enabled && !(s.hasKey && s.units.length > 0))}
            onCheckedChange={(on) => (on ? setAsking(true) : act.update.mutate({ enabled: false }))}
            aria-label={t("bb.switch")}
          />
        )
      }
    >
      {away ? (
        <>
          <Block flush>
            <StateLine state="unavailable" kind="off" word={t("cr.state.unavailable")} detail={t("bb.state.onlyRU")} />
          </Block>
          <Privacy>{t("bb.privacy")}</Privacy>
        </>
      ) : (
        <Loading q={q} />
      )}
      {s && <BsbordBody key={String(s.enabled)} s={s} act={act} price={price} setPrice={setPrice} />}
      {asking && s && <BsbordConsent s={s} act={act} price={price} setPrice={setPrice} onClose={() => setAsking(false)} />}
    </Detail>
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
      <Block flush>
        <BsbordStateLine state={state} />
        {s.enabled && <Spend s={s} test={outcome && "ok" in outcome ? outcome.ok : undefined} />}
      </Block>
      <Block title={t("src.block.connection")}>
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
        {s.hasKey && <UnitPicker s={s} busy={busy} onSave={(units, done) => change({ units }, done)} />}
      </Block>
      {s.hasKey && (
        <Block title={t("src.block.checks")}>
          <IntervalField current={s.intervalMin || bsbordDefaultInterval} options={bsbordIntervals} onPick={(m) => change({ intervalMin: m })} />
        </Block>
      )}
      {s.hasKey && (
        <Block title={t("src.block.budget")}>
          <CapField s={s} busy={busy} onSave={(capCredits) => act.update.mutate({ capCredits })} />
          {s.enabled && (
            <SettingRow label={t("bb.price")} width="flex flex-col items-start gap-2">
              {price && <PriceLine s={s} price={price} />}
              <Button variant="secondary" size="sm" disabled={act.preview.isPending} onClick={ask}>
                {act.preview.isPending ? t("bb.price.checking") : t("bb.price.check")}
              </Button>
            </SettingRow>
          )}
        </Block>
      )}
      <Privacy>{t("bb.privacy")}</Privacy>
      {(s.enabled || s.hasKey) && (
        <ActionBar>
          {s.hasKey && (
            <Button variant="secondary" size="md" disabled={act.test.isPending} onClick={test}>
              {act.test.isPending ? t("cr.testing") : t("cr.test")}
            </Button>
          )}
          <Disconnect onClick={() => setDisconnecting(true)} />
        </ActionBar>
      )}
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
    <SettingRow label={t("bb.cap")} hint={t("bb.cap.hint")} top width="basis-full @min-[540px]:w-full">
      <form onSubmit={submit} noValidate className="flex flex-wrap items-start gap-2">
        <TextField
          aria-label={t("bb.cap")}
          error={valid ? undefined : t("bb.err.cap")}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          inputMode="decimal"
          autoComplete="off"
          mono
          className="w-[160px] @min-[540px]:w-[200px]"
        />
        <Button type="submit" variant="secondary" size="md" className="mt-1" disabled={busy || !valid || credits === s.capCredits}>
          {t("bb.cap.save")}
        </Button>
      </form>
    </SettingRow>
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
    <Detail
      id="klick"
      sub={t("kc.sub")}
      control={
        p && (
          <Switch
            checked={on}
            disabled={act.update.isPending || act.disconnect.isPending}
            onCheckedChange={(v) => act.update.mutate({ enabled: v })}
            aria-label={t("kc.switch")}
          />
        )
      }
    >
      <Loading q={q} />
      {p && (
        <Block flush>
          <KlickStateLine p={p} />
          {on && p.nodes.length > 0 && <KlickNodes nodes={p.nodes} />}
        </Block>
      )}
      <Privacy>{t("kc.privacy")}</Privacy>
      {p && on && (
        <ActionBar>
          <Disconnect onClick={() => setDisconnecting(true)} />
        </ActionBar>
      )}
      {disconnecting && (
        <ConfirmDisconnect
          title={t("kc.disconnect.title")}
          body={t("kc.disconnect.body")}
          busy={act.disconnect.isPending}
          onClose={() => setDisconnecting(false)}
          onConfirm={() => act.disconnect.mutate(undefined, { onSuccess: () => setDisconnecting(false) })}
        />
      )}
    </Detail>
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
