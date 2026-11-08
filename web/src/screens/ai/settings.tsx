import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { SaveBar } from "@/screens/subscriptions/ui";
import { RowList, Section, SettingRow } from "./parts";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { BrandMark, customProviderBrand, modelBrand, providerBrand } from "@/components/assistant/brand-icons";
import { Modal } from "@/components/ui/modal";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
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

/** A provider's or a model maker's mark (components/assistant/brand-icons), or nothing for one without a known mark. */
const provMark = (provider: AssistantProvider, size = 14, url = "") => {
  const brand = providerBrand[provider] ?? customProviderBrand(url);
  return brand ? <BrandMark brand={brand} size={size} /> : provider >= AssistantProvider.CUSTOM_1 && provider <= AssistantProvider.CUSTOM_5 ? <Icon name="layers" size={size} /> : undefined;
};
const modelMark = (model: string) => {
  const brand = modelBrand(model);
  return brand ? <BrandMark brand={brand} size={14} /> : undefined;
};

// The owner's settings of the assistant (ADR 0014): on or off, the provider the chat uses, how hard it thinks, who may use
// it, the notes every admin's assistant reads, how long conversations are kept; per provider the key (sealed, step-up),
// the model and the monthly budget; and what it cost this month.

type Settings = Plain<AssistantSettings>;
type Status = Plain<ProviderStatus>;
type Choice = Plain<ProviderChoice>;

const retentions = [7, 30, 90, 180, 365, 0] as const;

export function AssistantTab() {
  const q = useQuery(settingsQuery);
  if (q.isPending) return <Pending />;
  if (q.isError) return <QueryError error={q.error} onRetry={() => void q.refetch()} />;
  return <SettingsForm key={JSON.stringify(q.data.settings)} data={q.data} />;
}

function SettingsForm({ data }: { data: Plain<GetAssistantSettingsResponse> }) {
  const t = useT();
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
    mutationFn: () =>
      assistant.updateAssistantSettings({
        settings: { ...draft, provider: shownProvider, fallbackProvider: shownFallback } as never,
        choices: Object.values(choices).filter((c) => connected.some((s) => s.provider === c.provider)) as never,
      }),
    onSuccess: () => {
      toast(t("ai.set.saved"));
      void qc.invalidateQueries({ queryKey: ["assistant"] });
    },
    onError: (e) => toast.error(t((assistantErrorKey(e) ?? "") as MessageKey) || errorText(e, t)),
  });

  const dirty = JSON.stringify([draft, choices]) !== JSON.stringify([initDraft, initChoices]);
  const rank = (s: Status) => (s.connected && shownProvider === s.provider ? 0 : s.connected ? 1 : 2);
  const rows = providers
    .map((p) => ({ p, status: statuses.find((s) => s.provider === p.id) ?? ({ provider: p.id, connected: false } as Status) }))
    .sort((a, b) => rank(a.status) - rank(b.status));
  const custom = statuses.filter((s) => s.provider >= AssistantProvider.CUSTOM_1 && s.provider <= AssistantProvider.CUSTOM_5);
  const displayName = (s: Status) => s.customName || (s.provider >= AssistantProvider.CUSTOM_1 ? `${t("ai.custom.title")} ${s.provider - AssistantProvider.CUSTOM_1 + 1}` : t(providerName(s.provider)));

  return (
    <div className="flex flex-col gap-6">
      <p className="max-w-[720px] text-[13px] leading-normal text-pretty text-muted">{t("ai.set.lead")}</p>

      <Section title={t("ai.sec.general")} desc={t("ai.sec.generalDesc")}>
        <RowList>
          <SettingRow label={t("ai.set.enabled")} hint={t("ai.set.enabledHint")}>
            <Switch checked={draft.enabled} onCheckedChange={(v) => set({ enabled: v })} aria-label={t("ai.set.enabled")} />
          </SettingRow>
          <SettingRow label={t("ai.set.provider")}>
            {connected.length === 0 ? (
              <span className="text-[13px] text-muted">{t("ai.set.providerNone")}</span>
            ) : (
              <Select
                aria-label={t("ai.set.provider")}
                className="min-w-[200px]"
                value={String(shownProvider)}
                onValueChange={(v) => set({ provider: Number(v) as AssistantProvider })}
                options={connected.map((s) => ({ value: String(s.provider), label: displayName(s), icon: provMark(s.provider, 14, s.customUrl) }))}
              />
            )}
          </SettingRow>
          {fallbacks.length > 0 && (
            <SettingRow label={t("ai.set.fallback")} hint={t("ai.set.fallbackHint")}>
              <Select
                aria-label={t("ai.set.fallback")}
                className="min-w-[200px]"
                value={String(shownFallback)}
                onValueChange={(v) => set({ fallbackProvider: Number(v) as AssistantProvider })}
                options={[{ value: String(AssistantProvider.UNSPECIFIED), label: t("ai.set.fallbackNone") }, ...fallbacks.map((s) => ({ value: String(s.provider), label: displayName(s), icon: provMark(s.provider, 14, s.customUrl) }))]}
              />
            </SettingRow>
          )}
          <SettingRow label={t("ai.set.effort")} hint={t("ai.effort.hint")}>
            <Segmented
              aria-label={t("ai.set.effort")}
              value={String(draft.effort)}
              onValueChange={(v) => set({ effort: Number(v) as AssistantEffort })}
              options={[
                { value: String(AssistantEffort.QUICK), label: t("ai.effort.quick") },
                { value: String(AssistantEffort.NORMAL), label: t("ai.effort.normal") },
                { value: String(AssistantEffort.THOROUGH), label: t("ai.effort.thorough") },
              ]}
            />
          </SettingRow>
        </RowList>
      </Section>

      <Section title={t("ai.sec.access")} desc={t("ai.sec.accessDesc")}>
        <RowList>
          <SettingRow label={t("ai.set.audience")} hint={t("ai.aud.hint")}>
            <Segmented
              aria-label={t("ai.set.audience")}
              value={String(draft.audience)}
              onValueChange={(v) => set({ audience: Number(v) as AssistantAudience })}
              options={[
                { value: String(AssistantAudience.OWNER), label: t("ai.aud.owner") },
                { value: String(AssistantAudience.ADMINS), label: t("ai.aud.admins") },
                { value: String(AssistantAudience.EVERYONE), label: t("ai.aud.everyone") },
              ]}
            />
          </SettingRow>
          <SettingRow label={t("ai.set.retention")}>
            <Select
              aria-label={t("ai.set.retention")}
              className="min-w-[160px]"
              value={String(draft.retentionDays)}
              onValueChange={(v) => set({ retentionDays: Number(v) })}
              options={retentions.map((d) => ({ value: String(d), label: d === 0 ? t("ai.ret.forever") : t.n("ai.ret.days", d) }))}
            />
          </SettingRow>
          <SettingRow label={t("ai.set.notes")} hint={t("ai.set.notesHint")} stack htmlFor="ai-notes">
            <div className="flex w-full flex-col gap-1.5">
              <textarea
                id="ai-notes"
                value={draft.panelNotes}
                maxLength={4000}
                rows={4}
                onChange={(e) => set({ panelNotes: e.target.value })}
                className="min-h-24 w-full resize-y rounded-field border border-line bg-canvas px-3 py-2 text-[13px] leading-snug outline-none focus:border-accent"
              />
              {looksSecret(draft.panelNotes) && <span className="text-xs text-warn-text">{t("ai.secretHint")}</span>}
            </div>
          </SettingRow>
        </RowList>
      </Section>

      <Section title={t("ai.prov.title")} desc={t("ai.prov.hint")}>
        <RowList>
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
            <ProviderRow key={status.provider} status={status} name={displayName(status)} data={status.customUrl || t("ai.custom.hint")} inUse={shownProvider === status.provider && status.connected}
              choice={choices[status.provider] ?? ({ provider: status.provider, model: "", budgetUsdMonth: 20 } as Choice)}
              onChoice={(c) => setChoices((all) => ({ ...all, [status.provider]: c }))} />
          ))}
        </RowList>
      </Section>

      <Usage usage={data.usage ?? []} />

      {dirty && (
        <SaveBar
          busy={save.isPending}
          canSave
          onSave={() => save.mutate()}
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
  const [entering, setEntering] = useState(false);
  const [key, setKey] = useState("");
  const custom = status.provider >= AssistantProvider.CUSTOM_1 && status.provider <= AssistantProvider.CUSTOM_5;
  const [customName, setCustomName] = useState(status.customName || "");
  const [customUrl, setCustomUrl] = useState(status.customUrl || "");
  const [customModel, setCustomModel] = useState(status.choice?.model || "");
  const [allowLocal, setAllowLocal] = useState(status.allowLocal || false);
  const [inputPrice, setInputPrice] = useState(String(status.inputUsdPerMtok || 0));
  const [outputPrice, setOutputPrice] = useState(String(status.outputUsdPerMtok || 0));
  const [removing, setRemoving] = useState(false);
  const [open, setOpen] = useState(inUse);
  const models = useQuery({
    queryKey: ["assistant", "models", status.provider],
    queryFn: async ({ signal }) => plain(await assistant.listProviderModels({ provider: status.provider }, { signal })).models,
    enabled: status.connected,
    staleTime: 10 * 60_000,
  });
  const refusal = (e: unknown) => {
    if (isStepUpCancelled(e)) return;
    const k = assistantErrorKey(e);
    toast.error(k ? t(k) : errorText(e, t));
  };
  const setKeyM = useMutation({
    mutationFn: () => guard(() => assistant.setProviderKey({ provider: status.provider, apiKey: key.trim(), customName, customUrl, customModel, allowLocal, inputUsdPerMtok: Number(inputPrice), outputUsdPerMtok: Number(outputPrice) })),
    onSuccess: () => {
      setKey("");
      setEntering(false);
      toast(t("ai.prov.saved"));
      void qc.invalidateQueries({ queryKey: ["assistant"] });
    },
    onError: refusal,
  });
  const removeM = useMutation({
    mutationFn: () => guard(() => assistant.removeProviderKey({ provider: status.provider })),
    onSuccess: () => {
      setRemoving(false);
      toast(t("ai.prov.removed"));
      void qc.invalidateQueries({ queryKey: ["assistant"] });
    },
    onError: refusal,
  });
  const fmt = useFmt();
  const money = (n: number) => n.toLocaleString(fmt.locale, { maximumFractionDigits: 2 });
  const model = models.data?.find((m) => m.id === choice.model);
  const spent = status.spentUsdMonth ?? 0;
  const pct = choice.budgetUsdMonth > 0 ? Math.min(100, (spent / choice.budgetUsdMonth) * 100) : 0;

  return (
    <div className="flex flex-col gap-3 py-3.5">
      <div className="flex items-center gap-2.5">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <b className="flex items-center gap-2 text-[13.5px] font-medium">
            {provMark(status.provider, 15, status.customUrl)}
            {name}
            {inUse && <span className="rounded-full bg-accent-soft px-2 py-0.5 text-[10.5px] font-semibold text-accent-text">{t("ai.prov.inUse")}</span>}
          </b>
          <span className={cx("flex items-center gap-1.5 text-xs", status.connected ? "text-ok-text" : "text-muted")}>
            {status.connected && <span aria-hidden className="size-1.5 rounded-full bg-ok" />}
            {status.connected ? (custom && !status.keyHint ? t("ai.custom.keyless") : t("ai.prov.connected", { hint: status.keyHint })) : t("ai.prov.notConnected")}
          </span>
        </div>
        {!entering && (
          <div className="flex flex-none items-center gap-1">
            {status.connected ? (
              <>
                <Button type="button" size="sm" variant="ghost" onClick={() => setEntering(true)}>
                  {t("ai.prov.replace")}
                </Button>
                <Button type="button" size="sm" variant="ghostDanger" onClick={() => setRemoving(true)}>
                  {t("ai.prov.remove")}
                </Button>
                <button
                  type="button"
                  aria-expanded={open}
                  aria-label={t("ai.prov.details")}
                  title={t("ai.prov.details")}
                  onClick={() => setOpen((v) => !v)}
                  className="grid size-8 place-items-center rounded-full text-muted transition-colors hover:bg-surface-2 hover:text-fg"
                >
                  <Icon name="chevronDown" size={14} className={cx("transition-transform", open && "rotate-180")} />
                </button>
              </>
            ) : (
              <Button type="button" size="sm" variant="secondary" onClick={() => setEntering(true)}>
                {t("ai.prov.connect")}
              </Button>
            )}
          </div>
        )}
      </div>
      {(entering || open || !status.connected) && (
        <p className="flex items-start gap-1.5 text-xs leading-snug text-muted">
          <Icon name="globe" size={12} className="mt-0.5 flex-none" />
          <span>{data}</span>
        </p>
      )}
      {entering && (
        <div className="flex flex-col gap-2 rounded-field bg-surface-2 p-3">
          {custom && <>
            <TextField label={t("ai.custom.name")} value={customName} onChange={(e) => setCustomName(e.target.value)} />
            <TextField label={t("ai.custom.url")} placeholder="https://example.com/v1" value={customUrl} onChange={(e) => setCustomUrl(e.target.value)} />
            <TextField label={t("ai.custom.model")} value={customModel} onChange={(e) => setCustomModel(e.target.value)} />
            <TextField label={t("ai.custom.input")} type="number" min="0" value={inputPrice} onChange={(e) => setInputPrice(e.target.value)} />
            <TextField label={t("ai.custom.output")} type="number" min="0" value={outputPrice} onChange={(e) => setOutputPrice(e.target.value)} />
            <label className="flex items-center gap-2 text-xs"><input type="checkbox" checked={allowLocal} onChange={(e) => setAllowLocal(e.target.checked)} />{t("ai.custom.local")}</label>
          </>}
          <TextField type="password" label={t("ai.prov.keyLabel", { name })} placeholder={t("ai.prov.keyPh")} value={key} onChange={(e) => setKey(e.target.value)} autoComplete="off" spellCheck={false} mono />
          <div className="flex justify-end gap-2">
            <Button type="button" size="sm" variant="ghost" onClick={() => (setEntering(false), setKey(""))}>
              {t("ai.prov.cancel")}
            </Button>
            <Button type="button" size="sm" variant="primary" disabled={setKeyM.isPending || (custom ? !customName.trim() || !customUrl.trim() || !customModel.trim() || (!key.trim() && !allowLocal) : !key.trim())} onClick={() => setKeyM.mutate()}>
              {t("ai.prov.check")}
            </Button>
          </div>
        </div>
      )}
      {status.connected && open && (
        <div className="flex flex-col gap-3">
          <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_140px]">
            <div className="flex flex-col gap-1.5">
              <span className="text-xs font-medium text-muted">{t("ai.prov.model")}</span>
              <Select
                aria-label={t("ai.prov.model")}
                value={choice.model}
                onValueChange={(v) => onChoice({ ...choice, model: v })}
                options={[{ value: "", label: t("ai.prov.modelDefault") }, ...(models.data ?? []).map((m) => ({ value: m.id, label: m.name || m.id, icon: modelMark(m.id) }))]}
              />
              {model && model.inputUsdPerMtok > 0 && <span className="text-xs text-muted">{t("ai.prov.price", { in: money(model.inputUsdPerMtok), out: money(model.outputUsdPerMtok) })}</span>}
            </div>
            <div className="flex flex-col gap-1.5">
              <span className="text-xs font-medium text-muted">{t("ai.prov.budget")}</span>
              <TextField
                aria-label={t("ai.prov.budget")}
                inputMode="decimal"
                value={String(choice.budgetUsdMonth)}
                onChange={(e) => {
                  const n = Number(e.target.value.replace(",", "."));
                  if (Number.isFinite(n) && n >= 0) onChoice({ ...choice, budgetUsdMonth: n });
                }}
              />
            </div>
          </div>
          <div className="flex flex-col gap-1.5">
            <div role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(pct)} aria-label={t("ai.prov.budget")} className="h-1 overflow-hidden rounded-full bg-surface-2">
              <div className={cx("h-full rounded-full", pct >= 100 ? "bg-warn" : "bg-accent")} style={{ width: `${pct}%` }} />
            </div>
            <span className="text-xs text-muted">{t("ai.prov.spent", { usd: money(spent), budget: money(choice.budgetUsdMonth) })}</span>
          </div>
        </div>
      )}
      {status.lastErrorCode && <p className="text-xs text-danger-text">{t("ai.prov.lastError", { error: t((assistantErrorKey(status.lastErrorCode) ?? "ai.err.provider_unreachable") as MessageKey) })}</p>}
      <Modal
        open={removing}
        onOpenChange={setRemoving}
        title={t("ai.prov.removeT", { name })}
        description={t("ai.prov.removeBody", { name })}
        footer={
          <>
            <Button type="button" variant="secondary" onClick={() => setRemoving(false)}>
              {t("ai.prov.cancel")}
            </Button>
            <Button type="button" variant="danger" disabled={removeM.isPending} onClick={() => removeM.mutate()}>
              {t("ai.prov.remove")}
            </Button>
          </>
        }
      />
    </div>
  );
}

function Usage({ usage }: { usage: { adminId: string; adminName: string; usd: number; turns: number }[] }) {
  const t = useT();
  const fmt = useFmt();
  return (
    <Section title={t("ai.usage.title")}>
      <RowList>
        {usage.length === 0 ? (
          <p className="py-3.5 text-[13px] text-muted">{t("ai.usage.none")}</p>
        ) : (
          <table className="my-1.5 w-full text-[13px]">
            <thead>
              <tr className="text-xs text-muted">
                <th className="py-2 text-start font-medium">{t("ai.usage.admin")}</th>
                <th className="py-2 text-end font-medium">{t("ai.usage.turns")}</th>
                <th className="py-2 text-end font-medium">{t("ai.usage.cost")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {usage.map((u) => (
                <tr key={u.adminId}>
                  <td className="py-2">{u.adminName}</td>
                  <td className="py-2 text-end tabular-nums">{u.turns.toLocaleString(fmt.locale)}</td>
                  <td className="py-2 text-end tabular-nums">${u.usd.toLocaleString(fmt.locale, { maximumFractionDigits: 2 })}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </RowList>
    </Section>
  );
}
