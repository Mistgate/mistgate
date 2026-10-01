import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { useState } from "react";
import { useT } from "@/i18n";
import { accentPresets, followInstanceAccent, isHex, setAccent, useAccent, useAccentChoice } from "@/lib/accent";
import { cx } from "@/lib/cx";

const FOLLOW = "follow";

/**
 * Six 38px swatches (the chosen one rings) plus a custom #rrggbb field. By default it edits this device's
 * accent (saved per device); pass `value` and `onChange` to edit another colour, e.g. the instance default.
 * With `follow` (this device's picker) the first swatch is "like the panel": it forgets the device's own colour.
 * Each column is as wide as its label, so the longer names never run into each other.
 */
export function AccentPicker({ value, onChange, follow }: { value?: string; onChange?: (hex: string) => void; follow?: boolean }) {
  const t = useT();
  const device = useAccent();
  const choice = useAccentChoice();
  const accent = value ?? device;
  const pick = onChange ?? setAccent;
  const following = !!follow && !choice.own;
  const preset = following ? undefined : accentPresets.find((p) => p.color === accent.toLowerCase());
  return (
    <div className="flex flex-col gap-3.5">
      <RadioGroup
        aria-label={t("settings.accent")}
        value={following ? FOLLOW : (preset?.color ?? "")}
        onValueChange={(v) => (v === FOLLOW ? followInstanceAccent() : pick(String(v)))}
        className="flex flex-wrap justify-start gap-x-3 gap-y-3.5 px-0.5"
      >
        {follow && (
          <Swatch value={FOLLOW} color={choice.instance} label={t("settings.accent.follow")} follow />
        )}
        {accentPresets.map((p) => (
          <Swatch key={p.id} value={p.color} color={p.color} label={t(`settings.accent.${p.id}`)} />
        ))}
      </RadioGroup>
      <HexField accent={accent} onPick={pick} />
    </div>
  );
}

function Swatch({ value, color, label, follow }: { value: string; color: string; label: string; follow?: boolean }) {
  return (
    <Radio.Root value={value} aria-label={label} className="group flex min-w-[52px] cursor-pointer flex-col items-center gap-2">
      <span
        className={cx(
          "grid size-[38px] place-items-center rounded-full transition-[transform,box-shadow] duration-[400ms] ease-spring-strong group-hover:scale-[1.12] group-data-checked:scale-110 group-data-checked:shadow-[0_0_0_3px_var(--bg),0_0_0_5px_var(--swatch)]",
          // "like the panel": the panel's colour inside a dashed ring, so it reads as "whatever the panel says"
          follow && "outline-dashed outline-[1.5px] outline-offset-2 outline-faint",
        )}
        style={{ background: color, ["--swatch" as string]: color }}
      >
        <span className="-mt-[3px] h-3 w-1.5 scale-0 rotate-45 border-r-[2.5px] border-b-[2.5px] border-[#0c0c0e] transition-transform duration-[350ms] ease-spring-strong group-data-checked:scale-100" />
      </span>
      <span className="text-[11px] font-medium whitespace-nowrap text-muted transition-colors group-data-checked:font-bold group-data-checked:text-fg">{label}</span>
    </Radio.Root>
  );
}

function HexField({ accent, onPick }: { accent: string; onPick: (hex: string) => void }) {
  const t = useT();
  // The draft lives here so a half-typed value does not fight the saved accent.
  const [draft, setDraft] = useState<string | null>(null);
  const shown = draft ?? accent.slice(1);
  const valid = isHex("#" + shown);
  return (
    <label className="flex items-center gap-3">
      <span className="text-[13px] text-muted">{t("settings.accent.custom")}</span>
      <span
        className={cx(
          "flex h-[38px] items-center gap-2 rounded-xl border bg-surface pr-1.5 pl-3 transition-colors duration-200 focus-within:border-accent",
          valid ? "border-line" : "border-danger",
        )}
      >
        <span aria-hidden className="font-mono text-[13px] text-muted">
          #
        </span>
        <input
          value={shown}
          maxLength={6}
          spellCheck={false}
          autoCapitalize="off"
          autoComplete="off"
          aria-invalid={!valid}
          onChange={(e) => {
            const v = e.target.value.replace(/[^0-9a-f]/gi, "").slice(0, 6);
            setDraft(v);
            if (isHex("#" + v)) onPick("#" + v);
          }}
          onBlur={() => setDraft(null)}
          className="w-[72px] border-none bg-transparent p-0 font-mono text-[13px] font-bold text-fg outline-none"
        />
        <span
          aria-hidden
          className="size-[26px] rounded-lg transition-colors duration-300"
          style={{ background: valid ? "#" + shown : "var(--surface-2)" }}
        />
      </span>
    </label>
  );
}
