import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Card, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
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
  const [draft, setDraft] = useState<Settings>({
    ...settings,
    effort: settings.effort || AssistantEffort.NORMAL,
    audience: settings.audience || AssistantAudience.OWNER,
  });
  const [choices, setChoices] = useState<Record<number, Choice>>(() =>
    Object.fromEntries(statuses.map((s) => [s.provider, { provider: s.provider, model: s.choice?.model ?? "", budgetUsdMonth: s.choice?.budgetUsdMonth ?? 20 } as Choice])),
  );
  const connected = statuses.filter((s) => s.connected);
  const set = (patch: Partial<Settings>) => setDraft((d) => ({ ...d, ...patch }));

  const save = useMutation({
    mutationFn: () => assistant.updateAssistantSettings({ settings: draft as never, choices: Object.values(choices) as never }),
    onSuccess: () => {
      toast(t("ai.set.saved"));
      void qc.invalidateQueries({ queryKey: ["assistant"] });
    },
    onError: (e) => toast.error(t((assistantErrorKey(e) ?? "") as MessageKey) || errorText(e, t)),
  });

  return (
    <div className="flex flex-col gap-3.5">
      <p className="max-w-[760px] text-[13px] leading-normal text-pretty text-muted">{t("ai.set.lead")}</p>

      <Card lg className="flex flex-col gap-4 p-4">
        <label className="flex items-start gap-3">
          <span className="flex min-w-0 flex-1 flex-col gap-0.5">
            <span className="text-[14px] font-bold">{t("ai.set.enabled")}</span>
            <span className="text-xs text-muted">{t("ai.set.enabledHint")}</span>
          </span>
          <Switch checked={draft.enabled} onCheckedChange={(v) => set({ enabled: v })} aria-label={t("ai.set.enabled")} />
        </label>

        <div className="grid gap-4 md:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("ai.set.provider")}</span>
            {connected.length === 0 ? (
              <span className="text-[13px] text-muted">{t("ai.set.providerNone")}</span>
            ) : (
              <Select
                aria-label={t("ai.set.provider")}
                value={String(draft.provider || connected[0]!.provider)}
                onValueChange={(v) => set({ provider: Number(v) as AssistantProvider })}
                options={connected.map((s) => ({ value: String(s.provider), label: t(providerName(s.provider)) }))}
              />
            )}
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("ai.set.effort")}</span>
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
            <span className="text-xs text-muted">{t("ai.effort.hint")}</span>
          </div>
        </div>

        <div className="flex flex-col gap-1.5">
          <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("ai.set.audience")}</span>
          <div className="grid gap-1.5 md:grid-cols-3">
            {([
              [AssistantAudience.OWNER, "ai.aud.owner"],
              [AssistantAudience.ADMINS, "ai.aud.admins"],
              [AssistantAudience.EVERYONE, "ai.aud.everyone"],
            ] as const).map(([v, label]) => (
              <button
                key={v}
                type="button"
                aria-pressed={draft.audience === v}
                onClick={() => set({ audience: v })}
                className={cx("flex items-center gap-2.5 rounded-field border p-3 text-start text-[13px] font-semibold transition-colors", draft.audience === v ? "border-accent-line bg-accent-soft" : "border-line bg-surface")}
              >
                <span aria-hidden className={cx("grid size-4 flex-none place-items-center rounded-full border-[1.5px]", draft.audience === v ? "border-accent" : "border-faint")}>
                  {draft.audience === v && <span className="size-2 rounded-full bg-accent" />}
                </span>
                {t(label)}
              </button>
            ))}
          </div>
          <span className="text-xs text-muted">{t("ai.aud.hint")}</span>
        </div>

        <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_220px]">
          <label className="flex flex-col gap-1.5">
            <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("ai.set.notes")}</span>
            <textarea
              value={draft.panelNotes}
              maxLength={4000}
              rows={4}
              onChange={(e) => set({ panelNotes: e.target.value })}
              className="min-h-24 resize-y rounded-field border border-line bg-canvas px-3 py-2 text-[13px] leading-snug outline-none focus:border-accent"
            />
            <span className="text-xs text-muted">{t("ai.set.notesHint")}</span>
            {looksSecret(draft.panelNotes) && <span className="text-xs text-warn-text">{t("ai.secretHint")}</span>}
          </label>
          <div className="flex flex-col gap-1.5">
            <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("ai.set.retention")}</span>
            <Select
              aria-label={t("ai.set.retention")}
              value={String(draft.retentionDays)}
              onValueChange={(v) => set({ retentionDays: Number(v) })}
              options={retentions.map((d) => ({ value: String(d), label: d === 0 ? t("ai.ret.forever") : t.n("ai.ret.days", d) }))}
            />
          </div>
        </div>
      </Card>

      <section className="flex flex-col gap-2.5">
        <div className="flex flex-col gap-0.5">
          <SectionLabel icon="key" tone="lavender">
            {t("ai.prov.title")}
          </SectionLabel>
          <span className="text-xs text-muted">{t("ai.prov.hint")}</span>
        </div>
        <div className="grid gap-3 lg:grid-cols-2">
          {providers.map((p) => {
            const status = statuses.find((s) => s.provider === p.id) ?? ({ provider: p.id, connected: false } as Status);
            return (
              <ProviderCard
                key={p.id}
                status={status}
                name={t(p.name)}
                data={t(p.data)}
                inUse={(draft.provider || connected[0]?.provider) === p.id && status.connected}
                choice={choices[p.id] ?? ({ provider: p.id, model: "", budgetUsdMonth: 20 } as Choice)}
                onChoice={(c) => setChoices((all) => ({ ...all, [p.id]: c }))}
              />
            );
          })}
        </div>
      </section>

      <div className="flex justify-end">
        <Button type="button" variant="primary" size="lg" disabled={save.isPending} onClick={() => save.mutate()}>
          {t("ai.set.save")}
        </Button>
      </div>

      <Usage usage={data.usage ?? []} />
    </div>
  );
}

function ProviderCard({ status, name, data, inUse, choice, onChoice }: { status: Status; name: string; data: string; inUse: boolean; choice: Choice; onChoice: (c: Choice) => void }) {
  const t = useT();
  const toast = useToast();
  const guard = useStepUp();
  const qc = useQueryClient();
  const [entering, setEntering] = useState(false);
  const [key, setKey] = useState("");
  const [removing, setRemoving] = useState(false);
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
    mutationFn: () => guard(() => assistant.setProviderKey({ provider: status.provider, apiKey: key.trim() })),
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

  return (
    <Card lg className={cx("flex flex-col gap-3 p-4", inUse && "border-accent-line")}>
      <div className="flex items-start gap-2.5">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <b className="flex items-center gap-2 text-[15px]">
            {name}
            {inUse && <span className="rounded-ctl bg-accent-soft px-1.5 py-0.5 text-[10.5px] font-bold text-accent-text">{t("ai.prov.inUse")}</span>}
          </b>
          <span className={cx("text-xs", status.connected ? "text-ok-text" : "text-muted")}>{status.connected ? t("ai.prov.connected", { hint: status.keyHint }) : t("ai.prov.notConnected")}</span>
        </div>
        {status.connected && !entering && (
          <div className="flex gap-1">
            <Button type="button" size="sm" variant="ghost" onClick={() => setEntering(true)}>
              {t("ai.prov.replace")}
            </Button>
            <Button type="button" size="sm" variant="ghostDanger" onClick={() => setRemoving(true)}>
              {t("ai.prov.remove")}
            </Button>
          </div>
        )}
        {!status.connected && !entering && (
          <Button type="button" size="sm" variant="secondary" onClick={() => setEntering(true)}>
            {t("ai.prov.connect")}
          </Button>
        )}
      </div>
      <p className="flex items-start gap-1.5 text-xs leading-snug text-muted">
        <Icon name="globe" size={12} className="mt-0.5 flex-none" />
        <span>{data}</span>
      </p>
      {entering && (
        <div className="flex flex-col gap-2 rounded-field bg-surface-2 p-3">
          <TextField type="password" label={t("ai.prov.keyLabel", { name })} placeholder={t("ai.prov.keyPh")} value={key} onChange={(e) => setKey(e.target.value)} autoComplete="off" spellCheck={false} mono />
          <div className="flex justify-end gap-2">
            <Button type="button" size="sm" variant="ghost" onClick={() => (setEntering(false), setKey(""))}>
              {t("ai.prov.cancel")}
            </Button>
            <Button type="button" size="sm" variant="primary" disabled={!key.trim() || setKeyM.isPending} onClick={() => setKeyM.mutate()}>
              {t("ai.prov.check")}
            </Button>
          </div>
        </div>
      )}
      {status.connected && (
        <div className="grid gap-3 border-t border-line pt-3 sm:grid-cols-[minmax(0,1fr)_150px]">
          <div className="flex flex-col gap-1.5">
            <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("ai.prov.model")}</span>
            <Select
              aria-label={t("ai.prov.model")}
              value={choice.model}
              onValueChange={(v) => onChoice({ ...choice, model: v })}
              options={[{ value: "", label: t("ai.prov.modelDefault") }, ...(models.data ?? []).map((m) => ({ value: m.id, label: m.name || m.id }))]}
            />
            {model && model.inputUsdPerMtok > 0 && <span className="text-xs text-muted">{t("ai.prov.price", { in: money(model.inputUsdPerMtok), out: money(model.outputUsdPerMtok) })}</span>}
          </div>
          <TextField
            label={t("ai.prov.budget")}
            inputMode="decimal"
            value={String(choice.budgetUsdMonth)}
            onChange={(e) => {
              const n = Number(e.target.value.replace(",", "."));
              if (Number.isFinite(n) && n >= 0) onChoice({ ...choice, budgetUsdMonth: n });
            }}
            hint={t("ai.prov.spent", { usd: money(status.spentUsdMonth ?? 0), budget: money(choice.budgetUsdMonth) })}
          />
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
    </Card>
  );
}

function Usage({ usage }: { usage: { adminId: string; adminName: string; usd: number; turns: number }[] }) {
  const t = useT();
  const fmt = useFmt();
  return (
    <Card lg className="flex flex-col gap-2.5 p-4">
      <SectionLabel icon="gauge" tone="sky">
        {t("ai.usage.title")}
      </SectionLabel>
      {usage.length === 0 ? (
        <p className="text-[13px] text-muted">{t("ai.usage.none")}</p>
      ) : (
        <table className="w-full text-[13px]">
          <thead>
            <tr className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">
              <th className="pb-1.5 text-start font-bold">{t("ai.usage.admin")}</th>
              <th className="pb-1.5 text-end font-bold">{t("ai.usage.turns")}</th>
              <th className="pb-1.5 text-end font-bold">{t("ai.usage.cost")}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {usage.map((u) => (
              <tr key={u.adminId}>
                <td className="py-1.5">{u.adminName}</td>
                <td className="py-1.5 text-end tabular-nums">{u.turns.toLocaleString(fmt.locale)}</td>
                <td className="py-1.5 text-end tabular-nums">${u.usd.toLocaleString(fmt.locale, { maximumFractionDigits: 2 })}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}
