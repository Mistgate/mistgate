import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Fragment, useEffect, useState, type FormEvent } from "react";
import { CopyButton } from "@/components/copy-button";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { EmptyState, Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { TextField } from "@/components/ui/text-field";
import { TokenChannel, TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import {
  defaultRate,
  defaultTtl,
  maxName,
  maxRate,
  parseRate,
  profileInfo,
  profileOptions,
  splitTokens,
  tokenState,
  tokensQuery,
  ttlOptions,
  useTokenActions,
  validName,
  type NewToken,
  type Token,
} from "@/lib/integrations";
import { card, ProfileChip } from "./parts";

const channelKey = { [TokenChannel.API]: "int.chan.api", [TokenChannel.MCP]: "int.chan.mcp" } as const;

/** "until 12 Dec 2026 · last used 3 min ago from 203.0.113.5 via MCP · 120/min": everything the owner asks of a token in one line. */
function metaParts(t: T, fmt: Fmt, tok: Token, now: number): string[] {
  const state = tokenState(tok, now);
  const life =
    state === "revoked" ? t("int.tok.revokedOn", { date: fmt.date(tok.revokedUnix) }) : state === "expired" ? t("int.tok.expiredOn", { date: fmt.date(tok.expiresUnix) }) : t("int.tok.expires", { date: fmt.date(tok.expiresUnix) });
  const channel = tok.lastUsedVia === TokenChannel.API || tok.lastUsedVia === TokenChannel.MCP ? t(channelKey[tok.lastUsedVia]) : "";
  const used =
    tok.lastUsedUnix <= 0
      ? t("int.tok.neverUsed")
      : tok.lastUsedIp && channel
        ? t("int.tok.lastUsedFrom", { when: fmt.ago(tok.lastUsedUnix), ip: tok.lastUsedIp, channel })
        : t("int.tok.lastUsed", { when: fmt.ago(tok.lastUsedUnix) });
  return [life, used, t("int.tok.rateOf", { n: tok.rateLimitPerMin })];
}

function TokenRow({ tok, now, onRevoke }: { tok: Token; now: number; onRevoke: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const state = tokenState(tok, now);
  return (
    <li className={cx("flex flex-wrap items-center gap-x-3 gap-y-1.5 border-t border-line py-2.5", state !== "active" && "opacity-70")}>
      <div className="flex min-w-[160px] flex-1 flex-col gap-0.5">
        <span className="flex min-w-0 items-center gap-2">
          <b className="truncate text-[13px]">{tok.name}</b>
          {tok.hint && <span className="flex-none font-mono text-[11px] text-faint">…{tok.hint}</span>}
        </span>
        {/* each part keeps its trailing dot, so a line never starts with a lone "·" */}
        <span className="text-[11px] leading-snug text-muted">
          {metaParts(t, fmt, tok, now).map((p, i, all) => (
            <Fragment key={i}>
              <span className="inline-block max-w-full align-top">{i < all.length - 1 ? `${p} ·` : p}</span>{" "}
            </Fragment>
          ))}
        </span>
      </div>
      {/* the chip and the action wrap together, so a narrow row never strands the action alone */}
      <div className="ml-auto flex flex-none items-center gap-3">
        <ProfileChip profile={tok.profile} />
        {state === "active" ? (
          <button type="button" onClick={onRevoke} aria-label={t("int.tok.revoke.aria", { name: tok.name })} className="text-xs font-bold text-danger-text">
            {t("int.tok.revoke")}
          </button>
        ) : (
          <span className="text-xs font-semibold text-muted">{t(state === "revoked" ? "int.tok.state.revoked" : "int.tok.state.expired")}</span>
        )}
      </div>
    </li>
  );
}

/** One radio card per profile: the name, and in plain words what that token will be able to do. */
function ProfilePicker({ value, onChange }: { value: TokenProfile; onChange: (p: TokenProfile) => void }) {
  const t = useT();
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("int.tok.profile")}</span>
      <RadioGroup aria-label={t("int.tok.profile")} value={String(value)} onValueChange={(v) => onChange(Number(v) as TokenProfile)} className="flex flex-col gap-1.5">
        {profileOptions.map((p) => {
          const info = profileInfo(p);
          return (
            <Radio.Root
              key={p}
              value={String(p)}
              className="group flex cursor-pointer items-start gap-3 rounded-field border border-line bg-surface p-3 text-left transition-colors duration-200 data-checked:border-accent-line data-checked:bg-accent-soft"
            >
              <span aria-hidden className="mt-0.5 grid size-4 flex-none place-items-center rounded-full border-[1.5px] border-faint group-data-checked:border-accent">
                <Radio.Indicator className="size-2 rounded-full bg-accent" />
              </span>
              <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="text-[13px] font-bold">{t(info.key)}</span>
                <span className="text-xs leading-snug text-pretty text-muted">{t(info.desc)}</span>
              </span>
            </Radio.Root>
          );
        })}
      </RadioGroup>
    </div>
  );
}

function CreateForm({ busy, onCreate, onCancel }: { busy: boolean; onCreate: (token: NewToken) => void; onCancel: () => void }) {
  const t = useT();
  const [name, setName] = useState("");
  const [profile, setProfile] = useState(TokenProfile.READONLY);
  const [ttl, setTtl] = useState<number>(defaultTtl);
  const [rate, setRate] = useState(String(defaultRate));
  const [tried, setTried] = useState(false);
  const nameOk = validName(name);
  const rateValue = parseRate(rate);

  function submit(e: FormEvent) {
    e.preventDefault();
    setTried(true);
    if (busy || !nameOk || rateValue === null) return;
    onCreate({ name: name.trim(), profile, ttlDays: ttl, rateLimitPerMin: rateValue });
  }

  return (
    <form onSubmit={submit} noValidate className="screen-enter flex flex-col gap-3.5 rounded-card border border-accent-line bg-canvas p-3 md:p-3.5">
      <TextField
        label={t("int.tok.name")}
        hint={t("int.tok.name.hint")}
        error={tried && !nameOk ? t("int.err.name") : undefined}
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder={t("int.tok.name.ph")}
        maxLength={maxName}
        autoComplete="off"
        autoFocus
      />
      <ProfilePicker value={profile} onChange={setProfile} />
      <div className="flex flex-col gap-1.5">
        <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">
          {t("int.tok.ttl")}
        </span>
        <FilterChips
          aria-label={t("int.tok.ttl")}
          value={String(ttl)}
          onValueChange={(v) => setTtl(Number(v))}
          options={ttlOptions.map((d) => ({ value: String(d), label: d === 365 ? t("int.tok.ttl.year") : t("int.tok.ttl.days", { n: d }) }))}
        />
        <span className="text-xs leading-snug text-muted">{t("int.tok.ttl.hint")}</span>
      </div>
      <TextField
        label={t("int.tok.rate")}
        hint={t("int.tok.rate.hint")}
        error={tried && rateValue === null ? t("int.err.rate") : undefined}
        value={rate}
        onChange={(e) => setRate(e.target.value)}
        inputMode="numeric"
        maxLength={String(maxRate).length + 1}
        autoComplete="off"
        mono
        className="md:max-w-[220px]"
      />
      <div className="flex flex-wrap justify-end gap-2">
        <Button variant="ghost" size="md" disabled={busy} onClick={onCancel}>
          {t("common.cancel")}
        </Button>
        <Button type="submit" variant="primary" size="md" disabled={busy}>
          {busy ? t("int.tok.creating") : t("int.tok.create")}
        </Button>
      </div>
    </form>
  );
}

/** The one place the secret is ever shown. Esc and a click outside do not close it: only the button does, so it is not lost by accident. */
function SecretDialog({ name, secret, onClose }: { name: string; secret: string; onClose: () => void }) {
  const t = useT();
  return (
    <Modal
      open
      onOpenChange={() => {}}
      title={t("int.secret.title")}
      description={t("int.secret.body")}
      footer={
        <Button variant="primary" size="md" onClick={onClose}>
          {t("int.secret.done")}
        </Button>
      }
    >
      <div className="flex flex-col gap-2">
        <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("int.secret.label", { name })}</span>
        <div className="flex flex-col gap-2 rounded-field border border-line bg-canvas p-3">
          <code data-testid="token-secret" className="font-mono text-[13px] leading-snug font-semibold break-all select-all">
            {secret}
          </code>
          <div className="flex justify-end">
            <CopyButton value={secret} size="md" />
          </div>
        </div>
        <Notice>{t("int.secret.warn")}</Notice>
      </div>
    </Modal>
  );
}

function RevokeDialog({ tok, busy, onConfirm, onClose }: { tok: Token; busy: boolean; onConfirm: () => void; onClose: () => void }) {
  const t = useT();
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("int.tok.revoke.title", { name: tok.name })}
      description={t("int.tok.revoke.body")}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={busy} onClick={onConfirm}>
            {t("int.tok.revoke.confirm")}
          </Button>
        </>
      }
    />
  );
}

/** API tokens: the list (last use, expiry, revoke) and the create form. The secret is shown once, in a dialog, and never stored here. */
export function TokensCard() {
  const t = useT();
  const q = useQuery(tokensQuery);
  const { create, revoke } = useTokenActions();
  const [creating, setCreating] = useState(false);
  // "?new=token" (the palette's "New API token") opens the form, then leaves the address
  const wantsNew = (useSearch({ strict: false }) as { new?: string }).new === "token";
  const [seenNew, setSeenNew] = useState(false);
  if (wantsNew !== seenNew) {
    setSeenNew(wantsNew);
    if (wantsNew) setCreating(true);
  }
  const navigate = useNavigate();
  useEffect(() => {
    if (wantsNew) void navigate({ to: "/integrations", search: {}, replace: true } as never);
  }, [wantsNew, navigate]);
  const [secret, setSecret] = useState<{ name: string; value: string } | null>(null);
  const [revoking, setRevoking] = useState<Token | null>(null);
  const [showOld, setShowOld] = useState(false);
  const now = q.data?.nowUnix ?? 0;
  const { live, old } = splitTokens(q.data?.tokens ?? [], now);

  function onCreate(token: NewToken) {
    create.mutate({ token, onSecret: (value) => setSecret({ name: token.name, value }) }, { onSuccess: () => setCreating(false) });
  }

  return (
    <section className={card}>
      <div className="flex flex-wrap items-center gap-2.5">
        <SectionLabel as="h2" icon="key" tone="lavender" className="min-w-32 flex-1">
          {t("int.tok.title")}
        </SectionLabel>
        {!creating && (
          <Button variant="secondary" size="sm" onClick={() => setCreating(true)}>
            <Icon name="plus" size={12} />
            {t("int.tok.new")}
          </Button>
        )}
      </div>
      <p className="text-xs leading-normal text-pretty text-muted">{t("int.tok.lead")}</p>
      {creating && <CreateForm busy={create.isPending} onCreate={onCreate} onCancel={() => setCreating(false)} />}
      {q.isPending && <Pending compact />}
      {q.isError && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {q.data && q.data.tokens.length === 0 && !creating && (
        <div className="rounded-card border border-dashed border-line">
          <EmptyState
            title={t("int.tok.none")}
            className="py-7"
            action={
              <Button variant="primary" size="md" onClick={() => setCreating(true)}>
                {t("int.tok.create")}
              </Button>
            }
          >
            {t("int.tok.none.text")}
          </EmptyState>
        </div>
      )}
      {live.length > 0 && (
        <ul className="flex flex-col">
          {live.map((tok) => (
            <TokenRow key={tok.id} tok={tok} now={now} onRevoke={() => setRevoking(tok)} />
          ))}
        </ul>
      )}
      {old.length > 0 && (
        <>
          <button type="button" aria-expanded={showOld} onClick={() => setShowOld((v) => !v)} className="self-start text-xs font-bold text-muted underline-offset-2 hover:underline">
            {showOld ? t("int.tok.hideOld") : t("int.tok.showOld", { n: old.length })}
          </button>
          {showOld && (
            <ul className="flex flex-col">
              {old.map((tok) => (
                <TokenRow key={tok.id} tok={tok} now={now} onRevoke={() => {}} />
              ))}
            </ul>
          )}
        </>
      )}
      {secret && <SecretDialog name={secret.name} secret={secret.value} onClose={() => setSecret(null)} />}
      {revoking && (
        <RevokeDialog
          tok={revoking}
          busy={revoke.isPending}
          onClose={() => setRevoking(null)}
          onConfirm={() => revoke.mutate({ id: revoking.id, name: revoking.name }, { onSuccess: () => setRevoking(null) })}
        />
      )}
    </section>
  );
}
