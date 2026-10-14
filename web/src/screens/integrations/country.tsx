import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { StatusDot, type StatusKind } from "@/components/ui/status";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
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
import { checksQuery, countryReason } from "@/lib/health";
import { card } from "./parts";

type Actions = ReturnType<typeof useGlobalpingActions>;

/**
 * "Reachability from a country": the country every source looks from, once, in the group's header, then a card per source.
 * Globalping is the only source today; block lists and a probe inside the country will be cards of their own next to it.
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
      <div className="flex items-start gap-3">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2.5 gap-y-0.5">
          <SectionLabel as="h3" icon="globe" tone="mint">
            {t("gp.title")}
          </SectionLabel>
          <span className="text-xs leading-normal text-pretty text-muted">{t("gp.sub")}</span>
        </div>
        {s && (
          <Switch
            checked={s.enabled}
            disabled={act.update.isPending || act.disconnect.isPending}
            onCheckedChange={(on) => act.update.mutate({ enabled: on })}
            aria-label={t("gp.switch")}
          />
        )}
      </div>
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
      onError: (error) => (ConnectError.from(error).code === Code.ResourceExhausted ? toast.error(t("gp.err.tooSoon")) : setOutcome({ at: Date.now() / 1000, error })),
    });

  return (
    <>
      <StateLine state={state} />
      {s.enabled && (
        <div className="flex flex-col gap-3 border-t border-line pt-3">
          <TokenField s={s} act={act} />
          <IntervalField s={s} act={act} />
        </div>
      )}
      {(s.enabled || s.hasToken) && (
        <div className="flex flex-wrap gap-2">
          {s.enabled && (
            <Button variant="secondary" size="sm" disabled={act.test.isPending} onClick={test}>
              {act.test.isPending ? t("gp.testing") : t("gp.test")}
            </Button>
          )}
          <Button variant="ghostDanger" size="sm" onClick={() => setDisconnecting(true)}>
            {t("gp.disconnect")}
          </Button>
        </div>
      )}
      <p className="flex items-start gap-1.5 text-xs leading-normal text-pretty text-muted">
        <Icon name="info" size={13} aria-hidden className="mt-px flex-none" />
        {t("gp.privacy")}
      </p>
      {disconnecting && (
        <Modal
          open
          onOpenChange={(o) => !o && !act.disconnect.isPending && setDisconnecting(false)}
          title={t("gp.disconnect.title")}
          description={t("gp.disconnect.body")}
          footer={
            <>
              <Button variant="ghost" size="md" disabled={act.disconnect.isPending} onClick={() => setDisconnecting(false)}>
                {t("common.cancel")}
              </Button>
              <Button variant="danger" size="md" disabled={act.disconnect.isPending} onClick={() => act.disconnect.mutate(undefined, { onSuccess: () => setDisconnecting(false) })}>
                {t("gp.disconnect")}
              </Button>
            </>
          }
        />
      )}
    </>
  );
}

/** One line: a dot, the state in a word, the detail (what is left of the hour, the error, why a round said nothing). */
function StateLine({ state }: { state: CardState }) {
  const t = useT();
  const fmt = useFmt();
  const [kind, word, detail] = ((): [StatusKind, string, string] => {
    switch (state.kind) {
      case "off":
        return ["off", t("gp.state.off"), t("gp.state.off.detail")];
      case "untested":
        return ["blip", t("gp.state.on"), t("gp.state.untested")];
      case "works": {
        const test = state.test;
        const parts = test
          ? [t("gp.state.left", { left: test.remaining, limit: test.limit }), test.tokenUsed ? t("gp.state.token") : ""]
          : [t("gp.state.round", { ago: fmt.ago(state.roundAt ?? 0) })];
        return ["ok", t("gp.state.works"), parts.filter(Boolean).join(" · ")];
      }
      case "fails":
        return ["bad", t("gp.state.fails"), globalpingErrorText(state.error, t)];
      case "noData":
        return ["warn", t("gp.state.noData"), [countryReason(t, state.reason), t("gp.state.round", { ago: fmt.ago(state.at) })].filter(Boolean).join(" · ")];
    }
  })();
  return (
    <div role="status" data-state={state.kind} className="flex items-start gap-2.5 rounded-field border border-line bg-canvas px-3 py-2.5 text-[13px] leading-snug">
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

/** Minutes between rounds. The panel may space them out further when the hourly limit needs it. */
function IntervalField({ s, act }: { s: GlobalpingSettings; act: Actions }) {
  const t = useT();
  const current = s.intervalMin || 15;
  const minutes: readonly number[] = (intervalOptions as readonly number[]).includes(current) ? intervalOptions : [...intervalOptions, current].sort((a, b) => a - b);
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
      <span aria-hidden className="text-[13px] font-bold">
        {t("gp.interval")}
      </span>
      <div className="w-[170px]">
        <Select
          compact
          aria-label={t("gp.interval")}
          value={String(current)}
          onValueChange={(v) => Number(v) !== current && act.update.mutate({ intervalMin: Number(v) })}
          options={minutes.map((m) => ({ value: String(m), label: t("gp.interval.every", { time: minutesText(t, m) }) }))}
        />
      </div>
    </div>
  );
}
