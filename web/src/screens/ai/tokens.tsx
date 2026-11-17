import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { EditionNotice } from "@/components/edition-notice";
import { Button } from "@/components/ui/button";
import { Segmented } from "@/components/ui/segmented";
import { Switch } from "@/components/ui/switch";
import { TokenChannel, TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt, type Fmt } from "@/lib/format";
import {
  adminUrl,
  changesUsers,
  clients,
  defaultRate,
  defaultTtl,
  maxName,
  maxRate,
  mcpUrl,
  parseRate,
  profileInfo,
  profileOptions,
  snippet,
  splitTokens,
  tokenState,
  tokensQuery,
  ttlOptions,
  useTokenActions,
  validName,
  type Client,
  type NewToken,
  type Token,
} from "@/lib/integrations";
import { CopyBtn, Dlg, ErrCard, Gl, ICONS, Sec, Skeleton, SwRow, useFresh, type Kind } from "./bits";

// API tokens and MCP, as the board draws them (PnAI "MCP и токены"): the list of tokens (last use, expiry, limit, revoke), the create
// window (the secret is shown once, in the same window) and the card with the address and a ready snippet for each kind of client.

const channelKey = { [TokenChannel.API]: "int.chan.api", [TokenChannel.MCP]: "int.chan.mcp" } as const;
const roleKind = (p: TokenProfile): Kind => (p === TokenProfile.OPERATOR ? "part" : p === TokenProfile.ADMIN ? "bad" : "");

/** "until 12 Dec 2026 · last used 3 min ago from 203.0.113.5 via MCP · 120/min": everything the owner asks of a token in one line. */
function metaParts(t: T, fmt: Fmt, tok: Token, now: number): string[] {
  const state = tokenState(tok, now);
  const life = state === "revoked" ? t("int.tok.revokedOn", { date: fmt.date(tok.revokedUnix) }) : state === "expired" ? t("int.tok.expiredOn", { date: fmt.date(tok.expiresUnix) }) : t("int.tok.expires", { date: fmt.date(tok.expiresUnix) });
  const channel = tok.lastUsedVia === TokenChannel.API || tok.lastUsedVia === TokenChannel.MCP ? t(channelKey[tok.lastUsedVia]) : "";
  const used =
    tok.lastUsedUnix <= 0
      ? t("int.tok.neverUsed")
      : tok.lastUsedIp && channel
        ? t("int.tok.lastUsedFrom", { when: fmt.ago(tok.lastUsedUnix), ip: tok.lastUsedIp, channel })
        : t("int.tok.lastUsed", { when: fmt.ago(tok.lastUsedUnix) });
  const parts = [life, used, t("int.tok.rateOf", { n: tok.rateLimitPerMin })];
  if (changesUsers(tok.profile)) parts.push(t(tok.allowBulkUserChanges ? "int.tok.bulk.allowed" : "int.tok.bulk.limited"));
  return parts;
}

export function TokensTab() {
  const q = useQuery(tokensQuery);
  if (q.isPending) return <Skeleton />;
  if (q.isError) return <ErrCard onRetry={() => void q.refetch()} />;
  return (
    <div className="sb-tab">
      <TokensCard />
      <McpCard />
    </div>
  );
}

/** API tokens: the list (last use, expiry, limit, revoke) and the create window. The secret is shown once, in a window, and never stored here. */
function TokensCard() {
  const t = useT();
  const fmt = useFmt();
  const q = useQuery(tokensQuery);
  const { create, revoke, setBulk } = useTokenActions();
  const [creating, setCreating] = useState(false);
  const noCreate = useEdition().off("mcp"); // the edition refuses CreateApiToken: the list stays, the way to a new token goes
  // "?new=token" (the palette's "New API token") opens the form, then leaves the address
  const wantsNew = (useSearch({ strict: false }) as { new?: string }).new === "token";
  const [seenNew, setSeenNew] = useState(false);
  if (wantsNew !== seenNew) {
    setSeenNew(wantsNew);
    if (wantsNew && !noCreate) setCreating(true);
  }
  const navigate = useNavigate();
  useEffect(() => {
    if (wantsNew) void navigate({ to: "/ai", search: { tab: "mcp" }, replace: true } as never);
  }, [wantsNew, navigate]);
  const [secret, setSecret] = useState<{ name: string; value: string } | null>(null);
  const [revoking, setRevoking] = useState<Token | null>(null);
  const [bulkOf, setBulkOf] = useState<Token | null>(null);
  const [showOld, setShowOld] = useState(false);
  const now = q.data?.nowUnix ?? 0;
  const { live, old } = splitTokens(q.data?.tokens ?? [], now);
  const fresh = useFresh(live.map((x) => x.id));

  function onCreate(token: NewToken) {
    create.mutate({ token, onSecret: (value) => setSecret({ name: token.name, value }) }, { onSuccess: () => setCreating(false) });
  }

  return (
    <Sec
      tone="sand"
      d={ICONS.key}
      title={t("aip.tok.title")}
      aside={
        !noCreate ? (
          <Button size="md" variant="primary" onClick={() => setCreating(true)}>
            <Gl d={ICONS.plus} />
            {t("int.tok.new")}
          </Button>
        ) : undefined
      }
      lead={t("aip.tok.lead", { header: "Authorization: Bearer" })}
    >
      <EditionNotice feature="mcp">{t("edition.hint.mcp")}</EditionNotice>
      {q.data && q.data.tokens.length === 0 && <p className="sb-emp ai-emp">{t("aip.tok.empty")}</p>}
      <div className="ai-toks">
        {live.map((tok) => (
          <div key={tok.id} className={cx("ai-tok", fresh === tok.id && "fresh")}>
            <div>
              <b className="mono">
                {tok.name}
                {tok.hint && <span>…{tok.hint}</span>}
              </b>
              <em>{metaParts(t, fmt, tok, now).join(" · ")}</em>
            </div>
            <span className={cx("pv-pill", roleKind(tok.profile))}>{t(profileInfo(tok.profile).key)}</span>
            <span className="ai-na">
              {changesUsers(tok.profile) && (
                <Button
                  size="md"
                  aria-label={t("int.tok.bulk.action.aria", { name: tok.name })}
                  onClick={() => (tok.allowBulkUserChanges ? setBulk.mutate({ id: tok.id, name: tok.name, allow: false }) : setBulkOf(tok))}
                >
                  {t(tok.allowBulkUserChanges ? "aip.tok.limitBack" : "aip.tok.limit")}
                </Button>
              )}
              <Button size="md" aria-label={t("int.tok.revoke.aria", { name: tok.name })} onClick={() => setRevoking(tok)}>
                {t("int.tok.revoke")}
              </Button>
            </span>
          </div>
        ))}
      </div>
      {old.length > 0 && (
        <>
          <button type="button" className="ai-old" aria-expanded={showOld} onClick={() => setShowOld((v) => !v)}>
            {showOld ? t("int.tok.hideOld") : t("int.tok.showOld", { n: old.length })}
          </button>
          {showOld && (
            <div className="ai-toks old">
              {old.map((tok) => (
                <div key={tok.id} className="ai-tok">
                  <div>
                    <b className="mono">
                      {tok.name}
                      {tok.hint && <span>…{tok.hint}</span>}
                    </b>
                    <em>{metaParts(t, fmt, tok, now).join(" · ")}</em>
                  </div>
                  <span className="pv-pill">{t(tokenState(tok, now) === "revoked" ? "int.tok.state.revoked" : "int.tok.state.expired")}</span>
                </div>
              ))}
            </div>
          )}
        </>
      )}
      {creating && <CreateDialog busy={create.isPending} onCreate={onCreate} onClose={() => setCreating(false)} />}
      {secret && <SecretDialog name={secret.name} secret={secret.value} onClose={() => setSecret(null)} />}
      {bulkOf && (
        <Dlg
          title={t("aip.tok.bulkT", { name: bulkOf.name })}
          onClose={() => setBulkOf(null)}
          busy={setBulk.isPending}
          footer={
            <>
              <Button size="lg" disabled={setBulk.isPending} onClick={() => setBulkOf(null)}>
                {t("common.cancel")}
              </Button>
              <Button size="lg" variant="primary" className="ai-dngp" disabled={setBulk.isPending} onClick={() => setBulk.mutate({ id: bulkOf.id, name: bulkOf.name, allow: true }, { onSuccess: () => setBulkOf(null) })}>
                {t("int.tok.bulk.allow")}
              </Button>
            </>
          }
        >
          <p>{t("aip.tok.bulkBody")}</p>
          <div className="rc-pb ai-dw">
            <i className="rw-gl part" aria-hidden="true" />
            <div>
              <span>{t("aip.tok.bulkWarn")}</span>
            </div>
          </div>
        </Dlg>
      )}
      {revoking && (
        <Dlg
          title={t("int.tok.revoke.title", { name: revoking.name })}
          onClose={() => setRevoking(null)}
          busy={revoke.isPending}
          footer={
            <>
              <Button size="lg" disabled={revoke.isPending} onClick={() => setRevoking(null)}>
                {t("common.cancel")}
              </Button>
              <Button size="lg" variant="primary" className="ai-dngp" disabled={revoke.isPending} onClick={() => revoke.mutate({ id: revoking.id, name: revoking.name }, { onSuccess: () => setRevoking(null) })}>
                {t("int.tok.revoke.confirm")}
              </Button>
            </>
          }
        >
          <p>{t("aip.tok.revokeBody")}</p>
        </Dlg>
      )}
    </Sec>
  );
}

function CreateDialog({ busy, onCreate, onClose }: { busy: boolean; onCreate: (token: NewToken) => void; onClose: () => void }) {
  const t = useT();
  const [name, setName] = useState("");
  const [profile, setProfile] = useState(TokenProfile.READONLY);
  const [ttl, setTtl] = useState<number>(defaultTtl);
  const [rate, setRate] = useState(String(defaultRate));
  const [bulk, setBulk] = useState(false);
  const [tried, setTried] = useState(false);
  const nameOk = validName(name);
  const rateValue = parseRate(rate);
  const go = () => {
    setTried(true);
    if (busy || !nameOk || rateValue === null) return;
    onCreate({ name: name.trim(), profile, ttlDays: ttl, rateLimitPerMin: rateValue, allowBulkUserChanges: changesUsers(profile) && bulk });
  };
  return (
    <Dlg
      title={t("aip.tok.newT")}
      onClose={onClose}
      busy={busy}
      shot="ai-token-dialog"
      footer={
        <>
          <Button size="lg" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button size="lg" variant="primary" disabled={busy} onClick={go}>
            {busy && <span className="an-sp" aria-hidden="true" />}
            {busy ? t("int.tok.creating") : t("int.tok.create")}
          </Button>
        </>
      }
    >
      <label className={cx("an-f", tried && !nameOk && "err")}>
        <span>{t("int.tok.name")}</span>
        <input className="an-i mono" type="text" autoComplete="off" spellCheck={false} autoFocus maxLength={maxName} placeholder={t("aip.tok.namePh")} value={name} onChange={(e) => setName(e.target.value)} />
        <small>{tried && !nameOk ? t("int.err.name") : t("aip.tok.nameHint")}</small>
      </label>
      <span className="rw-sl">{t("aip.tok.can")}</span>
      <RadioGroup aria-label={t("int.tok.profile")} value={String(profile)} onValueChange={(v) => setProfile(Number(v) as TokenProfile)} className="uq-opts">
        {profileOptions.map((p) => {
          const info = profileInfo(p);
          return (
            <Radio.Root key={p} value={String(p)} className="uq-opt">
              <span className="nx-rad" aria-hidden="true">
                <i />
              </span>
              <span>
                <b>{t(info.key)}</b>
                <em>{t(info.desc)}</em>
              </span>
            </Radio.Root>
          );
        })}
      </RadioGroup>
      <span className="rw-sl">{t("int.tok.ttl")}</span>
      <Segmented
        className="us-seg"
        aria-label={t("int.tok.ttl")}
        value={String(ttl)}
        onValueChange={(v) => setTtl(Number(v))}
        options={ttlOptions.map((d) => ({ value: String(d), label: d === 365 ? t("int.tok.ttl.year") : t("int.tok.ttl.days", { n: d }) }))}
      />
      <small className="ai-tz">{t("int.tok.ttl.hint")}</small>
      <label className={cx("an-f", tried && rateValue === null && "err")}>
        <span>{t("int.tok.rate")}</span>
        <input className="an-i mono" inputMode="numeric" autoComplete="off" maxLength={String(maxRate).length + 1} value={rate} onChange={(e) => setRate(e.target.value)} />
        <small>{tried && rateValue === null ? t("int.err.rate") : t("int.tok.rate.hint")}</small>
      </label>
      {/* a read-only token changes nobody, so the limit has nothing to say there */}
      {changesUsers(profile) && (
        <div className="ai-sws">
          <SwRow title={t("int.tok.bulk.label")} hint={t("int.tok.bulk.hint")}>
            <Switch checked={bulk} onCheckedChange={setBulk} aria-label={t("int.tok.bulk.label")} />
          </SwRow>
        </div>
      )}
    </Dlg>
  );
}

/** The one place the secret is ever shown. Esc and a click outside do not close it: only the button does, so it is not lost by accident. */
function SecretDialog({ name, secret, onClose }: { name: string; secret: string; onClose: () => void }) {
  const t = useT();
  return (
    <Dlg
      locked
      title={t("aip.tok.newT")}
      onClose={onClose}
      shot="ai-token-secret"
      footer={
        <Button size="lg" variant="primary" onClick={onClose}>
          {t("int.secret.done")}
        </Button>
      }
    >
      <div className="ai-made">
        <span className="g" aria-hidden="true">
          <Gl d={ICONS.tick} />
        </span>
        <b>{t("aip.tok.made", { name })}</b>
        <span>{t("int.secret.body")}</span>
      </div>
      <div className="ai-copy big">
        <code className="mono" data-testid="token-secret">
          {secret}
        </code>
        <CopyBtn value={secret} />
      </div>
    </Dlg>
  );
}

const labels: Record<Client, MessageKey> = { code: "int.mcp.client.code", desktop: "int.mcp.client.desktop", other: "int.mcp.client.other", stdio: "int.mcp.client.stdio" };
const hints: Record<Client, MessageKey> = { code: "int.mcp.hint.code", desktop: "int.mcp.hint.desktop", other: "int.mcp.hint.other", stdio: "int.mcp.hint.stdio" };
// what the snippet is written in, shown in its header
const langs: Record<Client, string> = { code: "bash", desktop: "json", other: "json", stdio: "bash" };

/** MCP: the endpoint, a ready snippet for each kind of client (the token is a placeholder), and what the agent can and cannot do. */
function McpCard() {
  const t = useT();
  const [client, setClient] = useState<Client>("code");
  const mcp = mcpUrl();
  const text = snippet(client, mcp, adminUrl());
  const unavailable = useEdition().off("mcp");
  const addr = useRef<HTMLElement>(null);
  const code = useRef<HTMLPreElement>(null);
  // a browser that refuses to copy: the text is selected, so Ctrl+C finishes it
  const pick = (el: HTMLElement | null) => {
    if (!el) return;
    const r = document.createRange();
    r.selectNodeContents(el);
    const s = window.getSelection();
    s?.removeAllRanges();
    s?.addRange(r);
  };
  // the endpoint is not served there, so no snippet to paste: the notice stands in for the whole card
  if (unavailable)
    return (
      <Sec tone="sky" d={ICONS.plug} title={t("aip.mcp.title")}>
        <EditionNotice feature="mcp" />
      </Sec>
    );
  return (
    <Sec
      tone="sky"
      d={ICONS.plug}
      title={t("aip.mcp.title")}
      lead={t("aip.mcp.lead")}
      foot={
        <>
          {t("aip.mcp.foot")}{" "}
          <Link to="/settings/$section" params={{ section: "audit" }}>
            {t("int.mcp.auditLink")}
          </Link>
        </>
      }
    >
      <div className="pp-rows">
        <div className="pp-row">
          <div className="pp-lb">
            <b>{t("aip.mcp.addr")}</b>
          </div>
          <div className="pp-ct">
            <div className="ai-copy">
              <code className="mono" data-testid="mcp-url" ref={addr}>
                {mcp}
              </code>
              <CopyBtn value={mcp} onFail={() => pick(addr.current)} />
            </div>
          </div>
        </div>
        <div className="pp-row">
          <div className="pp-lb">
            <b>{t("aip.mcp.client")}</b>
          </div>
          <div className="pp-ct">
            <Segmented className="ai-seg" aria-label={t("int.mcp.client")} value={client} onValueChange={setClient} options={clients.map((c) => ({ value: c, label: t(labels[c]) }))} />
            <p className="ai-cs">{t(hints[client])}</p>
            <div className="ai-code">
              <div>
                <em className="mono">{langs[client]}</em>
                <CopyBtn value={text} onFail={() => pick(code.current)} />
              </div>
              <pre className="mono" ref={code}>
                {text}
              </pre>
            </div>
          </div>
        </div>
      </div>
    </Sec>
  );
}
