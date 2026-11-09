import { Menu } from "@base-ui/react/menu";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { Icon } from "@/components/ui/icons";
import { AssistantEffort, AssistantProvider } from "@/gen/mistgate/admin/v1/assistant_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { accessQuery, providerName } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { readPref, writePref } from "@/lib/storage";
import { AUTO_OPEN_KEY } from "@/lib/ui-actions";
import { BrandMark, customProviderBrand, modelBrand, providerBrand } from "./brand-icons";

// The chat's agent picker: which connected provider answers this turn and how hard it thinks. The choice is the browser's
// (localStorage) and goes with every turn (ChatRequest.provider / .effort); empty means the panel's own setting. The list of
// connected providers (with their model and price) comes from the access the panel answers every admin.

/** The provider's own mark, or the generic one for a provider without a known mark. */
function ProviderMark({ provider, size, url = "" }: { provider: number; size: number; url?: string }) {
  const brand = providerBrand[provider as AssistantProvider] ?? customProviderBrand(url);
  return brand ? <BrandMark brand={brand} size={size} /> : <Icon name="layers" size={size} />;
}

const efforts = [
  [AssistantEffort.QUICK, "ai.effort.quick", "ai.pick.quickD"],
  [AssistantEffort.NORMAL, "ai.effort.normal", "ai.pick.normalD"],
  [AssistantEffort.THOROUGH, "ai.effort.thorough", "ai.pick.thoroughD"],
] as const satisfies readonly (readonly [AssistantEffort, MessageKey, MessageKey])[];

const PROVIDER_KEY = "assistant-provider";
const EFFORT_KEY = "assistant-effort";
const readNum = (k: string) => Number(readPref(k)) || 0;

export type Pick = {
  /** What goes into the request: 0 = the panel's. */
  provider: number;
  effort: number;
  setProvider: (p: number) => void;
  setEffort: (e: number) => void;
  /** The connected providers, the panel's own first. A provider whose month's budget is spent can be seen but not picked. */
  options: { id: number; name: string; model: string; inUsd: number; outUsd: number; budgetReached: boolean; isDefault: boolean; url: string }[];
  /** The provider and the depth in effect, in words: "Ollama Cloud · deepseek-v4.1-flash". */
  label: string;
  selectedProvider: number;
  selectedEffort: number;
};

export function usePick({ provider: accessProvider, model: accessModel }: { provider?: number; model?: string }): Pick {
  const t = useT();
  const q = useQuery(accessQuery);
  const [stored, setStored] = useState(() => ({ provider: readNum(PROVIDER_KEY), effort: readNum(EFFORT_KEY) }));
  const choices = q.data?.choices ?? [];
  const def = choices[0]?.provider ?? accessProvider ?? 0;
  const options = choices.map((c, i) => ({ id: c.provider as number, name: c.customName || t(providerName(c.provider)), model: c.model, inUsd: c.inputUsdPerMtok, outUsd: c.outputUsdPerMtok, budgetReached: c.budgetReached, isDefault: i === 0, url: c.customUrl }));
  // a saved provider that is no longer connected (or whose budget is spent) falls back to the default without a word
  const valid = options.some((o) => o.id === stored.provider && !o.budgetReached);
  const provider = valid ? stored.provider : 0;
  const selectedProvider = provider || def;
  const selectedEffort = stored.effort || q.data?.effort || 0;
  const chosen = options.find((o) => o.id === selectedProvider);
  const modelName = chosen ? chosen.model || t("ai.prov.modelDefault") : accessModel || "";
  const providerLabel = chosen?.name ?? (accessProvider ? t(providerName(accessProvider)) : "");
  const effortLabel = stored.effort ? t(efforts.find((e) => e[0] === stored.effort)![1]) : "";
  const label = [providerLabel && modelName ? `${providerLabel} · ${modelName}` : providerLabel || modelName, effortLabel].filter(Boolean).join(" · ");
  return {
    provider,
    effort: stored.effort,
    setProvider: (p) => {
      writePref(PROVIDER_KEY, p ? String(p) : null);
      setStored((s) => ({ ...s, provider: p }));
    },
    setEffort: (e) => {
      writePref(EFFORT_KEY, e ? String(e) : null);
      setStored((s) => ({ ...s, effort: e }));
    },
    options,
    label,
    selectedProvider,
    selectedEffort,
  };
}

const item = "flex w-full cursor-default items-start gap-2.5 rounded-[10px] px-2.5 py-2 text-start outline-none data-highlighted:bg-surface-2";

export function AgentPicker({ pick, owner, onNavigate, defaultOpen }: { pick: Pick; owner: boolean; onNavigate?: () => void; defaultOpen?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const money = (n: number) => n.toLocaleString(fmt.locale, { maximumFractionDigits: 2 });
  // this browser's "open pages without asking": a page the assistant opens in a turn is opened at once
  const [auto, setAuto] = useState(() => readPref(AUTO_OPEN_KEY) === "1");
  return (
    <Menu.Root defaultOpen={defaultOpen}>
      <Menu.Trigger aria-label={t("ai.pick.agent")} title={t("ai.pick.agent")} className="flex min-w-0 items-center gap-1.5 rounded-full px-2 py-1.5 text-xs text-muted transition-colors outline-none hover:bg-surface-2 hover:text-fg focus-visible:ring-2 focus-visible:ring-accent data-popup-open:bg-surface-2 data-popup-open:text-fg">
        <ProviderMark provider={pick.selectedProvider} size={14} url={pick.options.find((o) => o.id === pick.selectedProvider)?.url} />
        <span className="min-w-0 truncate">{pick.label || t("ai.pick.agent")}</span>
        <Icon name="chevronDown" size={12} />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner side="top" align="start" sideOffset={8} className="z-50 outline-none">
          <Menu.Popup className="w-[min(320px,calc(100vw-2rem))] origin-(--transform-origin) rounded-xl border border-line bg-surface p-1.5 shadow-(--shadow-toast) outline-none transition-[scale,opacity] duration-150 data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0">
            {pick.options.length > 0 && (
              <>
                <Menu.RadioGroup value={String(pick.selectedProvider)} onValueChange={(v) => pick.setProvider(pick.options.find((o) => String(o.id) === v)?.isDefault ? 0 : Number(v))} aria-label={t("ai.pick.agent")}>
                  {pick.options.map((o) => (
                    <Menu.RadioItem key={o.id} value={String(o.id)} closeOnClick={false} disabled={o.budgetReached} className={cx(item, o.budgetReached && "opacity-50")}>
                      <span aria-hidden className="mt-0.5 grid size-7 flex-none place-items-center rounded-lg bg-surface-2 text-fg">
                        <ProviderMark provider={o.id} size={15} url={o.url} />
                      </span>
                      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                        <span className="flex items-center gap-2 text-[13px] font-semibold">
                          {o.name}
                          {o.isDefault && <span className="rounded-full bg-surface-2 px-1.5 py-px text-[10px] font-medium text-muted">{t("ai.pick.default")}</span>}
                        </span>
                        <span className="flex min-w-0 items-center gap-1.5 text-xs text-muted">
                          {!o.budgetReached && modelBrand(o.model) && <BrandMark brand={modelBrand(o.model)!} size={12} className="flex-none" />}
                          <span className="truncate">{o.budgetReached ? t("ai.pick.budget") : o.model || t("ai.prov.modelDefault")}</span>
                        </span>
                        {!o.budgetReached && o.inUsd > 0 && <span className="text-[11px] text-faint">{t("ai.prov.price", { in: money(o.inUsd), out: money(o.outUsd) })}</span>}
                      </span>
                      <Menu.RadioItemIndicator className="mt-1 flex-none text-accent-text">
                        <Icon name="check" size={14} />
                      </Menu.RadioItemIndicator>
                    </Menu.RadioItem>
                  ))}
                </Menu.RadioGroup>
                <div role="separator" className="mx-1 my-1.5 h-px bg-line" />
              </>
            )}
            <p className="px-2.5 pt-1 pb-1 text-[11px] font-medium text-muted">{t("ai.pick.depth")}</p>
            <Menu.RadioGroup value={String(pick.selectedEffort)} onValueChange={(v) => pick.setEffort(Number(v))} aria-label={t("ai.pick.depth")}>
              {efforts.map(([value, name, desc]) => (
                <Menu.RadioItem key={value} value={String(value)} closeOnClick={false} className={cx(item, "items-center")}>
                  <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                    <span className="text-[13px] font-semibold">{t(name)}</span>
                    <span className="text-xs text-muted">{t(desc)}</span>
                  </span>
                  <Menu.RadioItemIndicator className="flex-none text-accent-text">
                    <Icon name="check" size={14} />
                  </Menu.RadioItemIndicator>
                </Menu.RadioItem>
              ))}
            </Menu.RadioGroup>
            <div role="separator" className="mx-1 my-1.5 h-px bg-line" />
            <Menu.CheckboxItem
              checked={auto}
              closeOnClick={false}
              onCheckedChange={(v) => {
                setAuto(v);
                writePref(AUTO_OPEN_KEY, v ? "1" : null);
              }}
              className={cx(item, "items-center text-[13px]")}
            >
              <span className="min-w-0 flex-1">{t("ai.ui.auto")}</span>
              <Menu.CheckboxItemIndicator className="flex-none text-accent-text">
                <Icon name="check" size={14} />
              </Menu.CheckboxItemIndicator>
            </Menu.CheckboxItem>
            {owner && (
              <>
                <div role="separator" className="mx-1 my-1.5 h-px bg-line" />
                <Menu.Item render={<Link to="/ai" />} onClick={onNavigate} className={cx(item, "items-center text-[13px] text-muted")}>
                  <Icon name="gear" size={14} />
                  {t("ai.chat.settings")}…
                </Menu.Item>
              </>
            )}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}
