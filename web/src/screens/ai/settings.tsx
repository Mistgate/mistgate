import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { halo } from "@/components/fx/halo";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { Segmented } from "@/components/ui/segmented";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { AssistantAudience, AssistantEffort, AssistantProvider, type AssistantSettings, type GetAssistantSettingsResponse, type ProviderChoice, type ProviderStatus } from "@/gen/mistgate/admin/v1/assistant_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { assistant } from "@/lib/api";
import { assistantErrorKey, looksSecret, providerName, providers, settingsQuery } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { plain, type Plain } from "@/lib/plain";
import { SaveBar } from "@/screens/subscriptions/ui";
import { Dd, Dlg, ErrCard, Gl, ICONS, Pill, Row, Sec, Skeleton } from "./bits";

// The owner's settings of the assistant (ADR 0014), as the board draws them (PnAI): on or off, the provider the chat uses, how hard it
// thinks, who may use it, the notes every admin's assistant reads, how long conversations are kept; per provider the key (sealed,
// step-up), the model and the monthly budget; and what it cost this month.

type Settings = Plain<AssistantSettings>;
type Status = Plain<ProviderStatus>;
type Choice = Plain<ProviderChoice>;

const retentions = [7, 30, 90, 180, 365, 0] as const;
const isCustom = (p: AssistantProvider) => p >= AssistantProvider.CUSTOM_1 && p <= AssistantProvider.CUSTOM_5;

export function AssistantTab() {
  const q = useQuery(settingsQuery);
  if (q.isPending) return <Skeleton />;
  if (q.isError) return <ErrCard onRetry={() => void q.refetch()} />;
  return <SettingsForm key={JSON.stringify(q.data.settings)} data={q.data} />;
}

function SettingsForm({ data }: { data: Plain<GetAssistantSettingsResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const settings = (data.settings ?? {}) as Settings;
  const statuses = (data.providers ?? []) as Status[];
  // what the form starts from (the same object tells whether anything changed, and is what Discard goes back to)
  const [initDraft] = useState<Settings>({
    ...settings,
    effort: settings.effort || AssistantEffort.NORMAL,
    audience: settings.audience || AssistantAudience.OWNER,
  });
  const [initChoices] = useState<Record<number, Choice>>(() =>
    Object.fromEntries(statuses.map((s) => [s.provider, { provider: s.provider, model: s.choice?.model ?? "", budgetUsdMonth: s.choice?.budgetUsdMonth ?? 20 } as Choice])),
  );
  const [draft, setDraft] = useState<Settings>(initDraft);
  const [choices, setChoices] = useState<Record<number, Choice>>(initChoices);
  const connected = statuses.filter((s) => s.connected);
  const shownProvider = connected.some((s) => s.provider === draft.provider) ? draft.provider : (connected[0]?.provider ?? AssistantProvider.UNSPECIFIED);
  // the scheduled tasks' fallback: another connected provider, or none (a fallback that became the main one, or lost its key, is none)
  const fallbacks = connected.filter((s) => s.provider !== shownProvider);
  const shownFallback = fallbacks.some((s) => s.provider === draft.fallbackProvider) ? draft.fallbackProvider : AssistantProvider.UNSPECIFIED;
  const set = (patch: Partial<Settings>) => setDraft((d) => ({ ...d, ...patch }));

  const save = useMutation({
    // the provider the select shows: the stored one, else the first connected (the owner may never have touched the select)
    // and only the choices of connected providers: the panel refuses a model or budget for a provider without a key
    mutationFn: (over?: Record<number, Choice>) =>
      assistant.updateAssistantSettings({
        settings: { ...draft, provider: shownProvider, fallbackProvider: shownFallback } as never,
        choices: Object.values(over ?? choices).filter((c) => connected.some((s) => s.provider === c.provider)) as never,
      }),
    onSuccess: () => {
      toast(t("ai.set.saved"));
      halo("save");
      void qc.invalidateQueries({ queryKey: ["assistant"] });
    },
    onError: (e) => toast.error(t((assistantErrorKey(e) ?? "") as MessageKey) || errorText(e, t)),
  });

  const dirty = JSON.stringify([draft, choices]) !== JSON.stringify([initDraft, initChoices]);
  const rank = (s: Status) => (s.connected && shownProvider === s.provider ? 0 : s.connected ? 1 : 2);
  const rows = providers
    .map((p) => ({ p, status: statuses.find((s) => s.provider === p.id) ?? ({ provider: p.id, connected: false } as Status) }))
    .sort((a, b) => rank(a.status) - rank(b.status));
  const custom = statuses.filter((s) => isCustom(s.provider));
  const displayName = (s: Status) => s.customName || (isCustom(s.provider) ? `${t("ai.custom.title")} ${s.provider - AssistantProvider.CUSTOM_1 + 1}` : t(providerName(s.provider)));

  const chat = connected.find((s) => s.provider === shownProvider);
  const chatChoice = chat ? (choices[chat.provider] ?? initChoices[chat.provider]) : undefined;
  const budgetOut = !!chat && !!chatChoice && chatChoice.budgetUsdMonth > 0 && (chat.spentUsdMonth ?? 0) >= chatChoice.budgetUsdMonth;
  const keyBad = !!chat && chat.lastErrorCode === "bad_key";
  const [today] = useState(() => new Date());
  const nextMonth = new Date(today.getFullYear(), today.getMonth() + 1, 1);
  const money = (n: number) => n.toLocaleString(fmt.locale, { maximumFractionDigits: 2 });
  const raise = () => {
    if (!chat || !chatChoice) return;
    const next = { ...choices, [chat.provider]: { ...chatChoice, budgetUsdMonth: Math.max(chatChoice.budgetUsdMonth * 2, 5) } };
    setChoices(next);
    save.mutate(next);
  };

  return (
    <div className="sb-tab">
      <div className="pf-g ai-grid">
        <div className="col">
          {budgetOut && chat && chatChoice && (
            <div className="rc-pb bad ai-pb" role="alert">
              <i className="rw-gl bad" aria-hidden="true" />
              <div>
                <b>{t("aip.bud.title")}</b>
                <span>{t("aip.bud.body", { date: nextMonth.toLocaleDateString(fmt.locale, { day: "numeric", month: "long" }) })}</span>
              </div>
              <Button size="md" disabled={save.isPending} onClick={raise}>
                {t("aip.bud.raise", { usd: money(Math.max(chatChoice.budgetUsdMonth * 2, 5)) })}
              </Button>
            </div>
          )}
          {keyBad && chat && (
            <div className="rc-pb bad ai-pb" role="alert">
              <i className="rw-gl bad" aria-hidden="true" />
              <div>
                <b>{t("aip.badkey.title", { name: displayName(chat) })}</b>
                <span>{t("aip.badkey.body")}</span>
              </div>
            </div>
          )}

          <Sec tone="lav" d={ICONS.spark} title={t("aip.as.title")} lead={t("ai.set.lead")}>
            <div className="pp-rows">
              <div className="pp-row ai-onr">
                <div className="pp-lb">
                  <b>{t(draft.enabled ? "aip.as.on" : "aip.as.off")}</b>
                  <span>{t(draft.enabled ? "aip.as.onHint" : "aip.as.offHint")}</span>
                </div>
                <div className="pp-ct">
                  <Switch checked={draft.enabled} onCheckedChange={(v) => set({ enabled: v })} aria-label={t("aip.as.title")} />
                </div>
              </div>
              <div className={cx("ai-body", !draft.enabled && "off")}>
                <Row label={t("ai.set.provider")}>
                  {connected.length === 0 ? (
                    <span className="ai-cs">{t("ai.set.providerNone")}</span>
                  ) : (
                    <Dd label={t("ai.set.provider")} value={String(shownProvider)} onChange={(v) => set({ provider: Number(v) as AssistantProvider })} options={connected.map((s) => ({ value: String(s.provider), label: displayName(s) }))} />
                  )}
                </Row>
                {fallbacks.length > 0 && (
                  <Row label={t("ai.set.fallback")} hint={t("aip.fall.hint")}>
                    <Dd
                      label={t("ai.set.fallback")}
                      value={String(shownFallback)}
                      onChange={(v) => set({ fallbackProvider: Number(v) as AssistantProvider })}
                      options={[{ value: String(AssistantProvider.UNSPECIFIED), label: t("ai.set.fallbackNone") }, ...fallbacks.map((s) => ({ value: String(s.provider), label: displayName(s) }))]}
                    />
                  </Row>
                )}
                <Row label={t("ai.set.effort")} hint={t("aip.effort.hint")}>
                  <Segmented
                    className="ai-seg"
                    aria-label={t("ai.set.effort")}
                    value={String(draft.effort)}
                    onValueChange={(v) => set({ effort: Number(v) as AssistantEffort })}
                    options={[
                      { value: String(AssistantEffort.QUICK), label: t("ai.effort.quick") },
                      { value: String(AssistantEffort.NORMAL), label: t("ai.effort.normal") },
                      { value: String(AssistantEffort.THOROUGH), label: t("ai.effort.thorough") },
                    ]}
                  />
                </Row>
              </div>
            </div>
          </Sec>

          <Sec tone="sand" d={ICONS.lock} title={t("aip.acc.title")}>
            <div className={cx("pp-rows ai-body", !draft.enabled && "off")}>
              <Row label={t("ai.set.audience")} hint={t("aip.aud.hint")}>
                <Segmented
                  className="ai-seg"
                  aria-label={t("ai.set.audience")}
                  value={String(draft.audience)}
                  onValueChange={(v) => set({ audience: Number(v) as AssistantAudience })}
                  options={[
                    { value: String(AssistantAudience.OWNER), label: t("ai.aud.owner") },
                    { value: String(AssistantAudience.ADMINS), label: t("ai.aud.admins") },
                    { value: String(AssistantAudience.EVERYONE), label: t("ai.aud.everyone") },
                  ]}
                />
              </Row>
              <Row label={t("ai.set.retention")}>
                <Segmented
                  className="ai-seg"
                  aria-label={t("ai.set.retention")}
                  value={String(draft.retentionDays)}
                  onValueChange={(v) => set({ retentionDays: Number(v) })}
                  options={retentions.map((d) => ({ value: String(d), label: d === 0 ? t("ai.ret.forever") : d === 365 ? t("aip.ret.year") : t.n("ai.ret.days", d) }))}
                />
              </Row>
              <Row label={t("ai.set.notes")} hint={t("aip.notes.hint")}>
                <textarea className="an-i sb-ta" aria-label={t("ai.set.notes")} value={draft.panelNotes} maxLength={4000} rows={3} onChange={(e) => set({ panelNotes: e.target.value })} />
                {looksSecret(draft.panelNotes) && <span className="ai-err">{t("ai.secretHint")}</span>}
              </Row>
            </div>
          </Sec>

          <Sec tone="sky" d={ICONS.plug} title={t("ai.prov.title")} lead={t("aip.prov.lead")}>
            {connected.length === 0 && (
              <div className="ai-np">
                <b>{t("aip.prov.none")}</b>
                <span>{t("aip.prov.noneBody")}</span>
              </div>
            )}
            <div className="ai-provs">
              {rows.map(({ p, status }) => (
                <ProviderRow
                  key={p.id}
                  status={status}
                  name={t(p.name)}
                  data={t(p.data)}
                  inUse={shownProvider === p.id && status.connected}
                  choice={choices[p.id] ?? ({ provider: p.id, model: "", budgetUsdMonth: 20 } as Choice)}
                  onChoice={(c) => setChoices((all) => ({ ...all, [p.id]: c }))}
                />
              ))}
              {custom.map((status) => (
                <ProviderRow
                  key={status.provider}
                  status={status}
                  name={displayName(status)}
                  data={status.customUrl || t("ai.custom.hint")}
                  inUse={shownProvider === status.provider && status.connected}
                  choice={choices[status.provider] ?? ({ provider: status.provider, model: "", budgetUsdMonth: 20 } as Choice)}
                  onChoice={(c) => setChoices((all) => ({ ...all, [status.provider]: c }))}
                />
              ))}
            </div>
          </Sec>
        </div>

        <aside className="col pp-as">
          <Usage usage={data.usage ?? []} />
        </aside>
      </div>

      {dirty && (
        <SaveBar
          busy={save.isPending}
          canSave
          onSave={() => save.mutate(undefined)}
          onDiscard={() => {
            setDraft(initDraft);
            setChoices(initChoices);
          }}
        />
      )}
    </div>
  );
}

function ProviderRow({ status, name, data, inUse, choice, onChoice }: { status: Status; name: string; data: string; inUse: boolean; choice: Choice; onChoice: (c: Choice) => void }) {
  const t = useT();
  const toast = useToast();
  const guard = useStepUp();
  const qc = useQueryClient();
  const fmt = useFmt();
  const custom = isCustom(status.provider);
  const [keying, setKeying] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [open, setOpen] = useState(inUse);
  const [just, setJust] = useState(false);
  const models = useQuery({
    queryKey: ["assistant", "models", status.provider],
    queryFn: async ({ signal }) => plain(await assistant.listProviderModels({ provider: status.provider }, { signal })).models,
    enabled: status.connected && open,
    staleTime: 10 * 60_000,
  });
  const refusal = (e: unknown) => {
    if (isStepUpCancelled(e)) return;
    const k = assistantErrorKey(e);
    toast.error(k ? t(k) : errorText(e, t));
  };
  const removeM = useMutation({
    mutationFn: () => guard(() => assistant.removeProviderKey({ provider: status.provider })),
    onSuccess: () => {
      setRemoving(false);
      toast(t("ai.prov.removed"));
      void qc.invalidateQueries({ queryKey: ["assistant"] });
    },
    onError: refusal,
  });
  const money = (n: number) => n.toLocaleString(fmt.locale, { maximumFractionDigits: 2 });
  const model = models.data?.find((m) => m.id === choice.model);
  const spent = status.spentUsdMonth ?? 0;
  const pct = choice.budgetUsdMonth > 0 ? Math.min(100, (spent / choice.budgetUsdMonth) * 100) : 0;
  const bad = status.connected && status.lastErrorCode === "bad_key";
  const kind = bad ? "bad" : status.connected ? "ok" : "";
  const letter = custom ? String(status.provider - AssistantProvider.CUSTOM_1 + 1) : name.charAt(0).toUpperCase();
  const stText = bad ? t("aip.prov.badKey") : status.connected ? (custom && !status.keyHint ? t("ai.custom.keyless") : t("ai.prov.connected", { hint: status.keyHint })) : t("ai.prov.notConnected");

  return (
    <div className={cx("ai-prov", status.connected ? "on" : "off", open && status.connected && "open", just && "just", bad && "bad")}>
      <button type="button" className="ai-ph" aria-expanded={open && status.connected} aria-label={name} onClick={() => status.connected && setOpen((v) => !v)}>
        <span className="ai-mono" aria-hidden="true">
          {letter}
        </span>
        <span className="ai-pn">
          <b>
            {name}
            {inUse && <i className="uq-otg now">{t("ai.prov.inUse")}</i>}
          </b>
          <em>{data}</em>
        </span>
        <Pill kind={kind}>{stText}</Pill>
        <Gl className="sb-chev" d={ICONS.chev} />
      </button>
      {!status.connected && (
        <Button size="md" className="ai-pc" onClick={() => setKeying(true)}>
          {t("ai.prov.connect")}
        </Button>
      )}
      {status.connected && open && (
        <div className="ai-pd">
          <dl className="uq-dl ai-dl">
            <div>
              <dt>{t("ai.prov.model")}</dt>
              <dd>
                <select className="an-i ai-in wide" aria-label={t("ai.prov.model")} value={choice.model} onChange={(e) => onChoice({ ...choice, model: e.target.value })}>
                  <option value="">{t("ai.prov.modelDefault")}</option>
                  {(models.data ?? []).map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name || m.id}
                    </option>
                  ))}
                </select>
                {model && model.inputUsdPerMtok > 0 && <span>{t("ai.prov.price", { in: money(model.inputUsdPerMtok), out: money(model.outputUsdPerMtok) })}</span>}
              </dd>
            </div>
            <div>
              <dt>{t("aip.prov.budget")}</dt>
              <dd>
                <input
                  className="an-i ai-in mono"
                  aria-label={t("ai.prov.budget")}
                  inputMode="decimal"
                  value={String(choice.budgetUsdMonth)}
                  onChange={(e) => {
                    const n = Number(e.target.value.replace(",", "."));
                    if (Number.isFinite(n) && n >= 0) onChoice({ ...choice, budgetUsdMonth: n });
                  }}
                />
                <span>{t("ai.prov.budgetHint")}</span>
              </dd>
            </div>
            <div>
              <dt>{t("ai.usage.title")}</dt>
              <dd className="ai-sp">
                <b className="mono">{t("aip.prov.spent", { spent: `$${money(spent)}`, budget: `$${money(choice.budgetUsdMonth)}` })}</b>
                <i className={cx("rw-pg", pct >= 100 && "bad")} style={{ ["--w" as string]: `${pct}%` }} role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(pct)} aria-label={t("ai.prov.budget")} />
              </dd>
            </div>
          </dl>
          {status.lastErrorCode && <p className="ai-err">{t("ai.prov.lastError", { error: t((assistantErrorKey(status.lastErrorCode) ?? "ai.err.provider_unreachable") as MessageKey) })}</p>}
          <div className="ai-pa">
            <Button size="md" onClick={() => setKeying(true)}>
              {t("aip.prov.replace")}
            </Button>
            <Button size="md" className="rw-dng" onClick={() => setRemoving(true)}>
              {t("aip.prov.remove")}
            </Button>
          </div>
        </div>
      )}
      {keying && (
        <KeyDialog
          status={status}
          name={name}
          custom={custom}
          replace={status.connected}
          onClose={() => setKeying(false)}
          onDone={() => {
            setKeying(false);
            setOpen(true);
            setJust(true);
            setTimeout(() => setJust(false), 1600);
            toast(t("aip.prov.connectedToast", { name }));
            halo("save");
            void qc.invalidateQueries({ queryKey: ["assistant"] });
          }}
        />
      )}
      {removing && (
        <Dlg
          title={t("ai.prov.removeT", { name })}
          onClose={() => setRemoving(false)}
          busy={removeM.isPending}
          footer={
            <>
              <Button size="lg" onClick={() => setRemoving(false)}>
                {t("ai.prov.cancel")}
              </Button>
              <Button size="lg" variant="primary" className="ai-dngp" disabled={removeM.isPending} onClick={() => removeM.mutate()}>
                {t("aip.prov.remove")}
              </Button>
            </>
          }
        >
          <p>{t("ai.prov.removeBody", { name })}</p>
        </Dlg>
      )}
    </div>
  );
}

/** The key window: the key is sent once, to the panel's server, and sealed there; a custom provider also names itself, its address and its model. */
function KeyDialog({ status, name, custom, replace, onClose, onDone }: { status: Status; name: string; custom: boolean; replace: boolean; onClose: () => void; onDone: () => void }) {
  const t = useT();
  const toast = useToast();
  const guard = useStepUp();
  const [key, setKey] = useState("");
  const [customName, setCustomName] = useState(status.customName || "");
  const [customUrl, setCustomUrl] = useState(status.customUrl || "");
  const [customModel, setCustomModel] = useState(status.choice?.model || "");
  const [allowLocal, setAllowLocal] = useState(status.allowLocal || false);
  const [inputPrice, setInputPrice] = useState(String(status.inputUsdPerMtok || 0));
  const [outputPrice, setOutputPrice] = useState(String(status.outputUsdPerMtok || 0));
  const [err, setErr] = useState("");
  const setKeyM = useMutation({
    mutationFn: () => guard(() => assistant.setProviderKey({ provider: status.provider, apiKey: key.trim(), customName, customUrl, customModel, allowLocal, inputUsdPerMtok: Number(inputPrice), outputUsdPerMtok: Number(outputPrice) })),
    onSuccess: () => {
      setKey("");
      onDone();
    },
    onError: (e) => {
      if (isStepUpCancelled(e)) return;
      const k = assistantErrorKey(e);
      if (k === "ai.err.bad_key") setErr(t("aip.key.bad"));
      else toast.error(k ? t(k) : errorText(e, t));
    },
  });
  const need = custom ? !customName.trim() || !customUrl.trim() || !customModel.trim() || (!key.trim() && !allowLocal) : !key.trim();
  const busy = setKeyM.isPending;
  return (
    <Dlg
      title={t(replace ? "aip.key.replaceT" : "aip.key.connectT", { name })}
      onClose={onClose}
      busy={busy}
      shot="ai-key-dialog"
      footer={
        <>
          <Button size="lg" disabled={busy} onClick={onClose}>
            {t("ai.prov.cancel")}
          </Button>
          <Button size="lg" variant="primary" aria-disabled={need || undefined} disabled={busy} onClick={() => !need && !busy && setKeyM.mutate()}>
            {busy && <span className="an-sp" aria-hidden="true" />}
            {t(busy ? "aip.key.busy" : "ai.prov.check")}
          </Button>
        </>
      }
    >
      <p>{t("aip.key.body")}</p>
      {custom && (
        <>
          <label className="an-f">
            <span>{t("ai.custom.name")}</span>
            <input className="an-i" value={customName} onChange={(e) => setCustomName(e.target.value)} autoComplete="off" />
          </label>
          <label className="an-f">
            <span>{t("ai.custom.url")}</span>
            <input className="an-i mono" placeholder="https://example.com/v1" value={customUrl} onChange={(e) => setCustomUrl(e.target.value)} autoComplete="off" spellCheck={false} />
          </label>
          <label className="an-f">
            <span>{t("ai.custom.model")}</span>
            <input className="an-i mono" value={customModel} onChange={(e) => setCustomModel(e.target.value)} autoComplete="off" spellCheck={false} />
          </label>
          <label className="an-f">
            <span>{t("ai.custom.input")}</span>
            <input className="an-i mono" type="number" min="0" value={inputPrice} onChange={(e) => setInputPrice(e.target.value)} />
          </label>
          <label className="an-f">
            <span>{t("ai.custom.output")}</span>
            <input className="an-i mono" type="number" min="0" value={outputPrice} onChange={(e) => setOutputPrice(e.target.value)} />
          </label>
          <label className="ai-chk">
            <input type="checkbox" checked={allowLocal} onChange={(e) => setAllowLocal(e.target.checked)} />
            {t("ai.custom.local")}
          </label>
        </>
      )}
      <label className={cx("an-f", err && "err")}>
        <span>{t("aip.key.label")}</span>
        <input
          className="an-i mono"
          type="password"
          autoComplete="off"
          spellCheck={false}
          placeholder={t("aip.key.ph")}
          aria-label={t("ai.prov.keyLabel", { name })}
          value={key}
          onChange={(e) => {
            setKey(e.target.value);
            setErr("");
          }}
        />
        <small>{err}</small>
      </label>
    </Dlg>
  );
}

function Usage({ usage }: { usage: { adminId: string; adminName: string; usd: number; turns: number }[] }) {
  const t = useT();
  const fmt = useFmt();
  return (
    <Sec tone="mint" d={ICONS.coin} title={t("ai.usage.title")} foot={t("aip.usage.foot")}>
      {usage.length === 0 ? (
        <p className="sb-emp">{t("ai.usage.none")}</p>
      ) : (
        <div className="ai-use">
          <div className="ai-ur ai-uh">
            <span>{t("ai.usage.admin")}</span>
            <span>{t("ai.usage.turns")}</span>
            <span>{t("ai.usage.cost")}</span>
          </div>
          {usage.map((u) => (
            <div key={u.adminId} className="ai-ur">
              <b>{u.adminName}</b>
              <span className="mono">{u.turns.toLocaleString(fmt.locale)}</span>
              <span className="mono">${u.usd.toLocaleString(fmt.locale, { maximumFractionDigits: 2 })}</span>
            </div>
          ))}
        </div>
      )}
    </Sec>
  );
}
