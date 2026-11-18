import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { Icon } from "@/components/ui/icons";
import { AssistantEffort, AssistantProvider } from "@/gen/mistgate/admin/v1/assistant_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { accessQuery, providerName, providers } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { readPref, writePref } from "@/lib/storage";
import { AUTO_OPEN_KEY } from "@/lib/ui-actions";
import { BrandMark, customProviderBrand, providerBrand } from "./brand-icons";
import { I } from "./as-icons";
import { useScene } from "./scene";

// The chat's agent picker (the board's "model" button and its window): which connected provider answers this turn and how hard it thinks. The
// choice is the browser's (localStorage) and goes with every turn (ChatRequest.provider / .effort); empty means the panel's own setting. The list
// of connected providers (with their model and price) comes from the access the panel answers every admin. A provider answers with the one model
// the owner set for it, so its column holds that model; a provider without a key is on the list too, with the way to add one.

/** The provider's own mark, or the generic one for a provider without a known mark. */
function ProviderMark({ provider, size, url = "" }: { provider: number; size: number; url?: string }) {
  const brand = providerBrand[provider as AssistantProvider] ?? customProviderBrand(url);
  return brand ? <BrandMark brand={brand} size={size} /> : <Icon name="layers" size={size} />;
}

const efforts = [
  [AssistantEffort.QUICK, "ai.effort.quick", "as.dp.quick"],
  [AssistantEffort.NORMAL, "ai.effort.normal", "as.dp.normal"],
  [AssistantEffort.THOROUGH, "ai.effort.thorough", "as.dp.thorough"],
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
  /** The provider and the depth in effect, in words: "Ollama Cloud · deepseek-v4.1-flash · Thorough". */
  label: string;
  /** The same without the depth: "Ollama Cloud · deepseek-v4.1-flash". */
  name: string;
  selectedProvider: number;
  selectedEffort: number;
};

export function usePick({ provider: accessProvider, model: accessModel }: { provider?: number; model?: string }): Pick {
  const t = useT();
  const q = useQuery(accessQuery);
  const scene = useScene();
  const [stored, setStored] = useState(() => ({ provider: readNum(PROVIDER_KEY), effort: scene.has("d0") ? AssistantEffort.QUICK : scene.has("d2") ? AssistantEffort.THOROUGH : readNum(EFFORT_KEY) }));
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
  const name = providerLabel && modelName ? `${providerLabel} · ${modelName}` : providerLabel || modelName;
  const label = [name, effortLabel].filter(Boolean).join(" · ");
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
    name,
    selectedProvider,
    selectedEffort,
  };
}

/** The place of the depth on the slider: 0 quick, 1 normal, 2 thorough; normal when neither the browser nor the panel names one. */
const depthIndex = (effort: number) => {
  const k = efforts.findIndex((e) => e[0] === effort);
  return k < 0 ? 1 : k;
};

type Tab = { id: number; name: string; connected: boolean; option?: Pick["options"][number]; dataKey?: MessageKey };

/** What the left column lists: the connected providers as the panel orders them, then the built-in ones that have no key yet. */
function tabsOf(pick: Pick, t: ReturnType<typeof useT>): Tab[] {
  const known = new Map(providers.map((p) => [p.id as number, p]));
  const out: Tab[] = pick.options.map((o) => ({ id: o.id, name: o.name, connected: true, option: o, dataKey: known.get(o.id)?.data }));
  for (const p of providers) if (!pick.options.some((o) => o.id === p.id)) out.push({ id: p.id, name: t(p.name), connected: false, dataKey: p.data });
  return out;
}

export function AgentPicker({ pick, owner, onNavigate, defaultOpen, disabled }: { pick: Pick; owner: boolean; onNavigate?: () => void; defaultOpen?: boolean; disabled?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const money = (n: number) => n.toLocaleString(fmt.locale, { maximumFractionDigits: 2 });
  const [open, setOpen] = useState(!!defaultOpen && !disabled);
  const scene = useScene();
  const [seen, setSeen] = useState<number | null>(() => {
    const flag = [...scene].find((f) => f.startsWith("seen-"));
    return flag ? Number(flag.slice(5)) : null;
  });
  // this browser's "open pages without asking": a page the assistant opens in a turn is opened at once
  const [auto, setAuto] = useState(() => readPref(AUTO_OPEN_KEY) === "1");
  const wrap = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const tabs = tabsOf(pick, t);
  const cur = tabs.find((x) => x.id === pick.selectedProvider);
  const see = tabs.find((x) => x.id === (seen ?? pick.selectedProvider)) ?? tabs[0];
  const depth = depthIndex(pick.selectedEffort);

  // a press outside shuts it, as Esc does
  useEffect(() => {
    if (!open) return;
    const away = (e: PointerEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", away);
    return () => document.removeEventListener("pointerdown", away);
  }, [open]);

  const shut = (focus = true) => {
    setOpen(false);
    setSeen(null);
    if (focus) trigger.current?.focus();
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key === "Escape" && open) {
      e.stopPropagation();
      shut();
    }
  };
  const slide = (e: KeyboardEvent) => {
    const rtl = getComputedStyle(e.currentTarget).direction === "rtl";
    const step = e.key === "ArrowRight" ? (rtl ? -1 : 1) : e.key === "ArrowLeft" ? (rtl ? 1 : -1) : e.key === "ArrowDown" ? 1 : e.key === "ArrowUp" ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const next = Math.min(efforts.length - 1, Math.max(0, depth + step));
    pick.setEffort(efforts[next]![0]);
    (e.currentTarget.querySelectorAll<HTMLElement>('[role="radio"]')[next] ?? null)?.focus();
  };
  const tabKey = (e: KeyboardEvent) => {
    const step = e.key === "ArrowDown" ? 1 : e.key === "ArrowUp" ? -1 : 0;
    if (!step || !see) return;
    e.preventDefault();
    const at = tabs.findIndex((x) => x.id === see.id);
    const next = tabs[(at + step + tabs.length) % tabs.length]!;
    setSeen(next.id);
    (e.currentTarget.querySelectorAll<HTMLElement>('[role="tab"]')[tabs.indexOf(next)] ?? null)?.focus();
  };

  const note = see ? (see.dataKey ? t(see.dataKey) : see.option?.url || "") : "";
  const price = see?.option && see.option.inUsd > 0 ? t("ai.prov.price", { in: money(see.option.inUsd), out: money(see.option.outUsd) }) : "";
  const mark = (id: number, url?: string) => (
    <span className="as-pv" aria-hidden>
      <ProviderMark provider={id} size={14} url={url} />
    </span>
  );

  return (
    <div ref={wrap} className="as-mw" onKeyDown={onKey}>
      <button ref={trigger} type="button" className="as-md" aria-haspopup="listbox" aria-expanded={open} aria-label={t("ai.pick.agent")} title={t("ai.pick.agent")} disabled={disabled} onClick={() => (open ? shut(false) : setOpen(true))}>
        <span className="as-pv" aria-hidden>
          {pick.selectedProvider && !disabled ? <ProviderMark provider={pick.selectedProvider} size={14} url={cur?.option?.url} /> : "–"}
        </span>
        <span>{disabled ? t("as.off.model") : pick.name || t("as.off.model")}</span>
        <I g="chev" />
      </button>
      {open && !disabled && (
        <div className="as-mp">
          <div className="as-m2">
            <div className="as-m2p" role="tablist" aria-label={t("as.pick.prov")} onKeyDown={tabKey}>
              {tabs.map((g) => (
                <button key={g.id} type="button" role="tab" aria-selected={g.id === see?.id} tabIndex={g.id === see?.id ? 0 : -1} className={cx("as-pt", g.id === pick.selectedProvider && "as-cur")} onClick={() => setSeen(g.id)} onMouseEnter={() => setSeen(g.id)}>
                  {mark(g.id, g.option?.url)}
                  <span>{g.name}</span>
                  <i className="as-dt" aria-hidden />
                </button>
              ))}
            </div>
            <div className="as-m2m" role="listbox" aria-label={t("as.pick.model")}>
              <p className="as-m2n">{[note, price].filter(Boolean).join(" · ")}</p>
              {see?.option && (
                <button
                  key={see.id}
                  type="button"
                  role="option"
                  aria-selected={see.id === pick.selectedProvider}
                  className="as-mo"
                  disabled={see.option.budgetReached}
                  onClick={() => {
                    pick.setProvider(see.option!.isDefault ? 0 : see.id);
                    shut();
                  }}
                >
                  <span className="as-mn">{see.option.model || t("ai.prov.modelDefault")}</span>
                  <span>{see.option.budgetReached ? t("ai.pick.budget") : see.option.isDefault ? t("ai.pick.default") : ""}</span>
                  <I g="check" />
                </button>
              )}
              {see && !see.connected && (
                <div className="as-m2e">
                  <span>{t("as.pick.nokey")}</span>
                  {owner && (
                    <Link to="/ai" onClick={onNavigate} className="inline-flex h-8 items-center rounded-ctl border border-line bg-step px-3 text-[13px] font-medium text-fg hover:bg-(--step-hover)">
                      {t("ai.prov.connect")}
                    </Link>
                  )}
                </div>
              )}
            </div>
          </div>
          <div className="as-dp">
            <div className="as-dpl">
              <span>{t("as.dp.t")}</span>
              <b>{t(efforts[depth]![1])}</b>
              <span className="as-dph">{t(efforts[depth]![2])}</span>
            </div>
            <div className={`as-sl d${depth}`} role="radiogroup" aria-label={t("ai.pick.depth")} style={{ ["--p" as string]: depth / 2 }} onKeyDown={slide}>
              <i className="as-slf" aria-hidden />
              <span className="as-spt" aria-hidden>
                {Array.from({ length: 7 }, (_, k) => (
                  <i key={k} />
                ))}
              </span>
              {efforts.map(([value, name, hint], k) => (
                <button key={value} type="button" role="radio" aria-checked={k === depth} tabIndex={k === depth ? 0 : -1} aria-label={t(name)} title={t(hint)} onClick={() => pick.setEffort(value)}>
                  <i aria-hidden />
                </button>
              ))}
              <span className="as-slt" aria-hidden />
            </div>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={auto}
            className="as-ao"
            onClick={() => {
              const next = !auto;
              setAuto(next);
              writePref(AUTO_OPEN_KEY, next ? "1" : null);
            }}
          >
            <span>{t("ai.ui.auto")}</span>
            <span className="as-sw2" aria-hidden />
          </button>
        </div>
      )}
    </div>
  );
}
