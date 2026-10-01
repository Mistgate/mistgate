import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { GenerateMode } from "@/gen/mistgate/admin/v1/awg_pb";
import type { FieldError, ObfuscationScore } from "@/gen/mistgate/admin/v1/profile_pb";
import { Card, Chip } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconChip, type IconName, type Tone } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { awg as awgApi } from "@/lib/api";
import { asVersion, changedCount, minClients, versions, type AwgVersion, type ConfImport } from "@/lib/awg";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useTx, type Tx } from "@/screens/users/t";
import { SectionLabel } from "@/components/ui/bits";
import { ConfImportBox } from "./conf-import";
import type { Settings } from "./schema";

const presetsQuery = {
  queryKey: ["awg", "presets"],
  queryFn: ({ signal }: { signal: AbortSignal }) => awgApi.listMimicryPresets({}, { signal }),
  staleTime: Infinity,
};

/** Fields this panel draws itself; the schema form leaves them out. */
export const awgHidden: ReadonlySet<string> = new Set(["version", "obfuscation.preset", "obfuscation.domain", "obfuscation.per_device_signature"]);

/**
 * A change that came from the schema form. Typing into I1-I5 by hand only reaches a device with the custom look (a look that
 * varies builds its own packets per device and ignores what is written), so typing there is choosing "custom".
 */
export function handTyped(prev: Settings, next: Settings): Settings {
  const was = (prev.obfuscation ?? {}) as Record<string, unknown>;
  const now = (next.obfuscation ?? {}) as Record<string, unknown>;
  if (now.preset === "custom" || !["i1", "i2", "i3", "i4", "i5"].some((k) => was[k] !== now[k])) return next;
  return { ...next, obfuscation: { ...now, preset: "custom" } };
}

/** What the server's preview says about valid settings: remarks and the obfuscation score. */
export type AwgAdvice = { warnings: readonly FieldError[]; score?: ObfuscationScore };

const card =
  "flex min-w-0 flex-col items-start gap-1 rounded-card border border-line bg-canvas px-3 py-2.5 text-left transition-colors duration-200 data-checked:border-accent-line data-checked:bg-accent-soft data-disabled:opacity-60";

/** What a look is: the web and file-transfer traffic it copies, the calls, name lookups, and so on. All one tone, the disguise's. */
const presetIcons: Record<string, IconName> = {
  quic: "bolt",
  curl_quic: "terminal",
  dns: "dns",
  stun: "network",
  webrtc: "video",
  sip: "phone",
  ntp: "clock",
  rtp: "traffic",
  ssdp: "search",
  dtls: "lock",
  custom: "sliders",
};

function Choice({ value, title, hint, mono, icon, tone }: { value: string; title: ReactNode; hint?: ReactNode; mono?: boolean; icon?: IconName; tone?: Tone }) {
  return (
    <Radio.Root value={value} className={cx(card, "cursor-pointer")}>
      <span className="flex items-center gap-2">
        {icon && tone && <IconChip icon={icon} tone={tone} />}
        <span className={cx("text-[13px] font-bold", mono && "font-mono")}>{title}</span>
      </span>
      {hint && <span className="text-[11px] leading-snug text-muted">{hint}</span>}
    </Radio.Root>
  );
}

/** Remarks that are information, not a problem: shown as a quiet line, not an amber notice. */
const infoCodes = new Set(["preset_port"]);

/** The text of a warning of the server: the dictionary by code, with the server's numbers; its English line as a fallback. */
export function warnText(t: Tx, w: FieldError): string {
  const vars: Record<string, string> = { ...w.params };
  if (vars.preset) vars.preset = t.opt(`awg.preset.${vars.preset}`) ?? vars.preset;
  return t.opt(`awg.warn.${w.code}`, vars) ?? (w.message || w.code);
}

const tones = { ok: "bg-accent-soft text-accent-text", warn: "bg-warn-soft text-warn-text", bad: "bg-danger-soft text-danger-text" };
const toneOf = (v: number) => (v >= 80 ? tones.ok : v >= 55 ? tones.warn : tones.bad);

function ScoreReasons({ score, t }: { score: ObfuscationScore; t: Tx }) {
  const tier = t.opt(`awg.score.tier.${score.tier}`) ?? score.tier;
  return (
    <div className="flex flex-col gap-1 rounded-field border border-line bg-canvas px-3 py-2.5 text-xs leading-snug">
      <span className="font-bold">{t("awg.score.base", { n: score.base, tier })}</span>
      {score.items.map((i) => (
        <span key={i.code + i.pointer} className="flex gap-2">
          <b className={cx("w-7 flex-none text-right font-mono", i.delta > 0 ? "text-accent-text" : "text-warn-text")}>{i.delta > 0 ? `+${i.delta}` : `−${-i.delta}`}</b>
          <span>{t.opt(`awg.score.${i.code}`, i.params) ?? i.code}</span>
        </span>
      ))}
      <span className="pt-1 text-[11px] text-muted">{t("awg.score.hint")}</span>
    </div>
  );
}

/**
 * The domain the packets carry: typed and applied on Enter or when the box is left, picked from the built-in list
 * (shown on demand), or one drawn from it. Empty = every device draws its own from the list.
 */
function DomainField({ value, pool, busy, t, onCommit }: { value: string; pool: readonly string[]; busy: boolean; t: Tx; onCommit: (d: string) => void }) {
  const [draft, setDraft] = useState(value);
  const [open, setOpen] = useState(false);
  const list = useId();
  const commit = (d: string) => {
    if (d.trim() !== value) onCommit(d.trim());
  };
  const others = pool.filter((d) => d !== value);
  const draw = () => commit(others[Math.floor(Math.random() * others.length)]!);
  return (
    <div className="flex flex-col gap-1.5 border-t border-line pt-3">
      <label htmlFor={list + "-in"} className="text-[13px] font-bold">
        {t("awg.domain.title")}
      </label>
      <div className="flex flex-wrap items-center gap-2">
        <input
          id={list + "-in"}
          list={list}
          value={draft}
          disabled={busy}
          placeholder={t("awg.domain.ph")}
          autoComplete="off"
          spellCheck={false}
          maxLength={100}
          onChange={(e) => {
            setDraft(e.target.value);
            if (pool.includes(e.target.value)) commit(e.target.value); // picked from the list
          }}
          onBlur={() => commit(draft)}
          onKeyDown={(e) => e.key === "Enter" && commit(draft)}
          className="h-[38px] w-[260px] max-w-full rounded-xl border border-line bg-canvas px-3 font-mono text-[13px] text-fg outline-none transition-colors duration-200 focus:border-accent disabled:opacity-60"
        />
        <datalist id={list}>
          {pool.map((d) => (
            <option key={d} value={d} />
          ))}
        </datalist>
        <Button size="sm" disabled={busy || others.length === 0} onClick={draw}>
          {t("awg.domain.random")}
        </Button>
        {pool.length > 0 && (
          <Button size="sm" variant="ghost" aria-expanded={open} onClick={() => setOpen(!open)}>
            {t("awg.domain.list", { n: pool.length })}
          </Button>
        )}
        {value !== "" && (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => onCommit("")}>
            {t("awg.domain.perDevice")}
          </Button>
        )}
      </div>
      {open && (
        <div className="flex max-h-44 flex-wrap gap-1.5 overflow-y-auto rounded-xl border border-line p-2">
          {pool.map((d) => (
            <button
              key={d}
              type="button"
              disabled={busy}
              aria-pressed={d === value}
              onClick={() => commit(d)}
              className={cx(
                "rounded-lg border px-2 py-1 font-mono text-[12px] transition-colors duration-150 disabled:opacity-60",
                d === value ? "border-accent bg-accent-soft text-fg" : "border-line text-muted hover:border-accent hover:text-fg",
              )}
            >
              {d}
            </button>
          ))}
        </div>
      )}
      <span className="text-[11px] leading-snug text-muted">{value === "" ? t("awg.domain.hint") : t("awg.domain.hintFixed")}</span>
    </div>
  );
}

/**
 * The AmneziaWG part of the profile editor: version cards, mimicry cards, the warnings and the score of the current
 * numbers (the server's preview) and the oldest client that understands the chosen version. Everything else is the
 * schema form. Picking a look fetches only the signature packets (not critical); the big button replaces the whole block.
 */
export function AwgPanel({ settings, onChange, advice }: { settings: Settings; onChange: (next: Settings) => void; advice?: AwgAdvice }) {
  const t = useTx();
  const toast = useToast();
  const meta = useQuery(presetsQuery);
  const version = asVersion(settings.version);
  const ob = (settings.obfuscation ?? {}) as Record<string, unknown>;
  const preset = typeof ob.preset === "string" && ob.preset ? ob.preset : "dns";
  const domain = typeof ob.domain === "string" ? ob.domain : "";
  const [busy, setBusy] = useState(false);
  const [showScore, setShowScore] = useState(false);
  // what the last "new values" did; stays until the next pick
  const [note, setNote] = useState<{ n: number } | null>(null);
  // an answer arrives after the owner may have typed on: it goes onto what the form holds then
  const latest = useRef(settings);
  useEffect(() => {
    latest.current = settings;
  });

  const presets = meta.data?.presets ?? [];
  const current = presets.find((p) => p.id === preset);
  const mtuOf = () => Number(latest.current.mtu) || 0;
  const fetchBlock = async (v: AwgVersion, p: string, d: string, mode: GenerateMode, mtu = mtuOf()) =>
    JSON.parse((await awgApi.generateObfuscation({ version: v, preset: p, mtu, domain: d, mode })).obfuscationJson) as Record<string, unknown>;

  async function run(job: () => Promise<void>): Promise<boolean> {
    setBusy(true);
    try {
      await job();
      return true;
    } catch (e) {
      toast.error(errorText(e, t));
      return false;
    } finally {
      setBusy(false);
    }
  }

  /** A whole new block for `v`: every critical field changes. The owner's per-device choice stays, and so do the packets of
   *  the custom look: nothing generates them, so the generator's empty I1-I5 must not wipe what was written. */
  async function fullBlock(v: AwgVersion, d: string, mtu?: number) {
    const next = await fetchBlock(v, preset, d, GenerateMode.FULL, mtu);
    next.preset = preset;
    const cur = latest.current.obfuscation as Record<string, unknown> | undefined;
    if (cur?.per_device_signature !== undefined) next.per_device_signature = cur.per_device_signature;
    if (preset === "custom") for (const k of ["i1", "i2", "i3", "i4", "i5"]) if (cur?.[k] !== undefined) next[k] = cur[k];
    return next;
  }

  const generate = (v: AwgVersion) =>
    run(async () => {
      const next = await fullBlock(v, domain);
      setNote({ n: changedCount(latest.current.obfuscation, next) });
      onChange({ ...latest.current, version: v, obfuscation: next });
    });

  /** Only preset, domain and I1-I5 change (client side, no reissue). "Custom" has nothing to generate. */
  const signature = (p: string, d: string) =>
    run(async () => {
      const patch = p === "custom" ? { preset: p, domain: d } : { ...(await fetchBlock(version, p, d, GenerateMode.SIGNATURE)), preset: p };
      setNote(null);
      onChange({ ...latest.current, obfuscation: { ...(latest.current.obfuscation as Record<string, unknown>), ...patch } });
    });

  const importConf = (imp: ConfImport) =>
    run(async () => {
      if (!imp.version && imp.mtu === undefined) return;
      // another version: start from a valid block of it, so what the file does not say is still consistent
      const base = imp.version && imp.version !== version ? await fullBlock(imp.version, domain, imp.mtu) : (latest.current.obfuscation as Record<string, unknown>);
      setNote(null);
      onChange({ ...latest.current, version: imp.version ?? version, ...(imp.mtu !== undefined && { mtu: imp.mtu }), obfuscation: { ...base, ...imp.obfuscation } });
    });

  // switching the version rebuilds the block: 3.x-only fields are refused on 2.0, so the old block would not pass
  const pickVersion = (v: string) => {
    if (v !== version) void generate(asVersion(v));
  };
  const shown = presets.filter((p) => p.versions.length === 0 || p.versions.includes(version));
  const warnings = advice?.warnings ?? [];
  const score = advice?.score;
  const varies = current?.variesPerDevice ?? true;
  const perDevice = ob.per_device_signature === true;

  return (
    <>
      <Card lg className="flex flex-col gap-3 p-4">
        <SectionLabel icon="layers" tone="sky">
          {t("awg.version.title")}
        </SectionLabel>
        <RadioGroup value={version} onValueChange={pickVersion} disabled={busy} aria-label={t("awg.version.title")} className="grid gap-2 sm:grid-cols-2">
          {versions.map((v) => (
            <Choice
              key={v}
              value={v}
              icon={v === "3.1" ? "sparkle" : "clock"}
              tone="sky"
              title={t(v === "3.1" ? "awg.version.31" : "awg.version.20")}
              hint={t(v === "3.1" ? "awg.version.31Hint" : "awg.version.20Hint")}
            />
          ))}
        </RadioGroup>
        {version === "2.0" && <Notice>{t("awg.version.oldWarn")}</Notice>}
        <div className="flex flex-col gap-1.5 border-t border-line pt-3">
          <span className="text-[13px] font-bold">{t("awg.clients.title")}</span>
          <div className="flex flex-wrap gap-1.5">
            {minClients[version].map((c) => (
              <Chip key={c.app} mono>
                {c.app} {c.min}+
              </Chip>
            ))}
          </div>
          <span className="text-[11px] leading-snug text-muted">{t("awg.clients.hint")}</span>
        </div>
        <ConfImportBox busy={busy} onApply={importConf} />
      </Card>

      <Card lg className="flex flex-col gap-3 p-4">
        <div className="flex items-center gap-3">
          <SectionLabel className="flex-1" icon="mask" tone="sage">
            {t("awg.mimicry.title")}
          </SectionLabel>
          {score && (
            <button
              type="button"
              aria-expanded={showScore}
              onClick={() => setShowScore(!showScore)}
              className={cx("inline-flex h-[22px] flex-none items-center rounded-ctl px-2 text-[11px] font-bold whitespace-nowrap", toneOf(score.value))}
            >
              {t("awg.score.chip", { n: score.value })}
            </button>
          )}
        </div>
        <p className="-mt-1 text-xs leading-normal text-pretty text-muted">{t("awg.mimicry.hint")}</p>
        {score && showScore && <ScoreReasons score={score} t={t} />}
        {meta.isError && <Notice tone="danger">{errorText(meta.error, t)}</Notice>}
        <RadioGroup value={preset} onValueChange={(p) => void signature(p, domain)} disabled={busy} aria-label={t("awg.mimicry.title")} className="grid grid-cols-2 gap-2 sm:grid-cols-3">
          {shown.map((p) => (
            <Choice key={p.id} value={p.id} icon={presetIcons[p.id] ?? "mask"} tone="sage" title={t.opt(`awg.preset.${p.id}`) ?? p.name} hint={t.opt(`awg.preset.${p.id}.hint`)} />
          ))}
        </RadioGroup>
        {current?.usesDomain && <DomainField key={domain} value={domain} pool={meta.data?.domains ?? []} busy={busy} t={t} onCommit={(d) => void signature(preset, d)} />}
        <div className="flex items-center gap-3 border-t border-line pt-3">
          <div className="flex min-w-0 flex-1 flex-col gap-0.5">
            <span className="text-[13px] font-bold">{t("awg.perDevice.title")}</span>
            <span className="text-[11px] leading-snug text-muted">{t(preset === "custom" ? "awg.perDevice.custom" : !varies ? "awg.perDevice.same" : "awg.perDevice.hint")}</span>
          </div>
          <Switch aria-label={t("awg.perDevice.title")} checked={perDevice} disabled={preset === "custom" || !varies} onCheckedChange={(on) => onChange({ ...settings, obfuscation: { ...ob, per_device_signature: on } })} />
        </div>
        <div className="flex flex-col items-start gap-1.5 border-t border-line pt-3">
          <Button size="sm" disabled={busy} onClick={() => void generate(version)}>
            {busy ? t("awg.generating") : t("awg.generate")}
          </Button>
          <span className="text-[11px] leading-snug text-muted">{t("awg.generateWarn")}</span>
          {note && (
            <p role="status" className="text-xs font-bold text-accent-text">
              {note.n > 0 ? t.n("awg.generated", note.n) : t("awg.generatedNone")}
            </p>
          )}
        </div>
        {warnings.map((w) =>
          infoCodes.has(w.code) ? (
            <p key={w.code + w.pointer} className="text-xs leading-snug text-muted">
              {warnText(t, w)}
            </p>
          ) : (
            <Notice key={w.code + w.pointer}>{warnText(t, w)}</Notice>
          ),
        )}
      </Card>
    </>
  );
}
