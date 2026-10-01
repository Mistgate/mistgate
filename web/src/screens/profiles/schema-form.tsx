import { useState, type ReactNode } from "react";
import { Icon, type IconName, type Tone } from "@/components/ui/icons";
import { useToast } from "@/components/ui/toast";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { cx } from "@/lib/cx";
import { useTx, type Tx } from "@/screens/users/t";
import { Card, SectionLabel } from "@/components/ui/bits";
import { errorsOf, getAt, isChanged, makeSecret, MASK, setAt, type Field, type Group, type Settings } from "./schema";

/** What a schema group is about: basics sand, the disguise sage, the rest of the protocol's knobs sky. */
const groupMarks: Record<string, { icon: IconName; tone: Tone }> = {
  basics: { icon: "sliders", tone: "sand" },
  obfuscation: { icon: "mask", tone: "sage" },
  advanced: { icon: "gear", tone: "sky" },
};
const groupMark = (id: string) => groupMarks[id] ?? groupMarks.basics!;

/** A problem the server found in a field (JSON pointer, its code and a short English message). */
export type Problem = { pointer: string; code?: string; message: string };

type Props = {
  protocol: string;
  /** Fields (dotted ids) another part of the screen draws, so the form leaves them out. */
  hidden?: ReadonlySet<string>;
  /** Fields (dotted ids) that show their value but cannot be edited. */
  readOnly?: ReadonlySet<string>;
  /** Something a protocol adds under a field's hint ("random" next to a port, a note under the exit). */
  extra?: (f: Field) => ReactNode;
  groups: Group[];
  settings: Settings;
  base: Settings;
  problems: readonly Problem[];
  onChange: (next: Settings) => void;
};

/**
 * The profile form, generated from the protocol's schema: collapsible groups ("N changed" in the header), one
 * row per field with its label, hint and control, an accent dot on what differs from the saved value.
 * Text that the dictionary has for a field (profiles.f.<protocol>.<field>.title|hint|enum.<value>) replaces
 * the schema's own English; a field without an entry shows the schema text, so a new protocol needs no UI work.
 */
export function SchemaForm({ protocol, groups, settings, base, problems, onChange, hidden, readOnly, extra }: Props) {
  const t = useTx();
  // advanced starts folded; a group with a problem is always open
  const [open, setOpen] = useState<Record<string, boolean>>({});
  if (groups.length === 0) return <Card lg className="p-4 text-[13px] text-muted">{t("profiles.noSchema")}</Card>;

  return (
    <>
      {groups.map((group) => {
        const g = hidden ? { ...group, fields: group.fields.filter((f) => !hidden.has(f.id)) } : group;
        if (g.fields.length === 0) return null;
        const changed = g.fields.filter((f) => isChanged(f, settings, base)).length;
        const errs = g.fields.filter((f) => errorsOf(f, problems).length > 0).length;
        const isOpen = errs > 0 || (open[g.id] ?? g.id !== "advanced");
        return (
          <Card key={g.id} lg className="px-4 py-1">
            <button
              type="button"
              aria-expanded={isOpen}
              onClick={() => setOpen({ ...open, [g.id]: !isOpen })}
              className="flex h-12 w-full items-center gap-2.5 text-left"
            >
              <SectionLabel className="flex-1" icon={groupMark(g.id).icon} tone={groupMark(g.id).tone}>
                {t.opt(`profiles.group.${g.id}`) ?? g.id}
              </SectionLabel>
              {errs > 0 && <span className="text-[11px] font-bold text-danger-text">{t.n("profiles.errors", errs)}</span>}
              {changed > 0 && <span className="text-[11px] font-bold text-accent-text">{t("profiles.changed", { n: changed })}</span>}
              <Icon name="chevronRight" size={14} className={cx("text-muted transition-transform duration-300 ease-spring", isOpen && "rotate-90")} />
            </button>
            {isOpen && (
              <div className="flex flex-col pb-1.5">
                {g.fields.map((f) => (
                  <FieldRow
                    key={f.key}
                    protocol={protocol}
                    f={f}
                    t={t}
                    settings={settings}
                    locked={readOnly?.has(f.id) ?? false}
                    extra={extra?.(f)}
                    dirty={isChanged(f, settings, base)}
                    problems={errorsOf(f, problems)}
                    onChange={(path, value) => onChange(setAt(settings, path, value))}
                  />
                ))}
              </div>
            )}
          </Card>
        );
      })}
    </>
  );
}

const inputCls =
  "h-[38px] rounded-xl border border-line bg-canvas font-mono text-[13px] text-fg outline-none transition-colors duration-200 focus:border-accent aria-invalid:border-danger";

function FieldRow({ protocol, f, t, settings, locked, extra, dirty, problems, onChange }: { protocol: string; f: Field; t: Tx; settings: Settings; locked: boolean; extra?: ReactNode; dirty: boolean; problems: Problem[]; onChange: (path: string[], value: unknown) => void }) {
  const copy = (part: string) => t.opt(`profiles.f.${protocol}.${f.id}.${part}`);
  const label = copy("title") ?? f.title;
  const hint = copy("hint") ?? f.hint;
  const unit = f.unit ? (t.opt(`profiles.unit.${f.unit}`) ?? f.unit) : "";
  const invalid = problems.length > 0;
  const choices = f.choices.map((c) => ({ ...c, label: copy(`enum.${c.value}`) ?? c.label }));

  let control: ReactNode = null;
  switch (f.kind) {
    case "toggle":
      control = <Switch aria-label={label} checked={!!getAt(settings, f.path)} onCheckedChange={(on) => onChange(f.path, on)} />;
      break;
    case "choice": {
      const cur = String(getAt(settings, f.path) ?? "");
      const pick = (v: string) => onChange(f.path, choices.find((c) => c.value === v)?.raw ?? v);
      control = f.segmented ? (
        <Segmented aria-label={label} value={cur} onValueChange={pick} options={choices.map((c) => ({ value: c.value, label: c.label }))} className="flex-wrap" />
      ) : (
        <div className="w-[200px] max-w-full">
          <Select aria-label={label} value={cur} onValueChange={pick} options={choices.map((c) => ({ value: c.value, label: c.label }))} />
        </div>
      );
      break;
    }
    case "number":
      control = (
        <>
          <NumberInput label={label} value={getAt(settings, f.path)} integer={f.integer} invalid={invalid} onChange={(v) => onChange(f.path, v)} className="w-24 text-right" />
          {unit && <span className="min-w-10 text-xs text-muted">{unit}</span>}
        </>
      );
      break;
    case "range":
      control = (
        <>
          {f.parts!.map((p, i) => (
            <span key={i} className="flex items-center gap-1.5">
              {i === 1 && <span className="text-muted">–</span>}
              <NumberInput label={`${label} ${i === 0 ? "1" : "2"}`} value={getAt(settings, p)} integer invalid={invalid} onChange={(v) => onChange(p, v)} className="w-20 px-2.5 text-center" />
            </span>
          ))}
        </>
      );
      break;
    case "generate":
    case "secret":
      control = <SecretField label={label} value={getAt(settings, f.path)} invalid={invalid} t={t} make={() => makeSecret(f)} onChange={(v) => onChange(f.path, v)} />;
      break;
    case "text":
      control = (
        <input
          aria-label={label}
          aria-invalid={invalid || undefined}
          autoComplete="off"
          spellCheck={false}
          disabled={locked}
          placeholder={copy("placeholder")}
          value={String(getAt(settings, f.path) ?? "")}
          onChange={(e) => onChange(f.path, e.target.value)}
          className={cx(inputCls, "w-[220px] max-w-full px-3 disabled:opacity-60")}
        />
      );
      break;
  }

  return (
    <div className="flex min-h-14 flex-wrap items-center gap-x-4 gap-y-2 border-t border-line py-2">
      <div className="flex min-w-0 flex-[1_1_180px] flex-col gap-0.5">
        <span className="flex items-center gap-1.5 text-[13px] font-bold">
          {label}
          {dirty && <span aria-hidden className="size-1.5 rounded-full bg-accent" />}
        </span>
        {hint && <span className="text-[11px] leading-snug text-muted">{hint}</span>}
        {extra}
        {problems.map((p, i) => (
          <span key={i} role="alert" className="text-[11px] leading-snug text-danger-text">
            {(p.code && t.opt(`field${p.pointer.replaceAll("/", ".")}.${p.code}`)) || p.message}
          </span>
        ))}
      </div>
      <div className="flex max-w-full min-w-0 flex-none items-center justify-end gap-1.5">{control}</div>
    </div>
  );
}

/** Digits only (plus one dot for a non-integer); an empty box stays empty and the server names the problem. */
function NumberInput({ label, value, integer, invalid, onChange, className }: { label: string; value: unknown; integer: boolean; invalid: boolean; onChange: (v: number | string) => void; className?: string }) {
  return (
    <input
      aria-label={label}
      aria-invalid={invalid || undefined}
      inputMode={integer ? "numeric" : "decimal"}
      autoComplete="off"
      value={value === undefined || value === null ? "" : String(value)}
      onChange={(e) => {
        const raw = e.target.value.replace(integer ? /\D/g : /[^\d.]/g, "");
        onChange(raw === "" || raw.endsWith(".") || Number.isNaN(Number(raw)) ? raw : Number(raw));
      }}
      className={cx(inputCls, "px-3", className)}
    />
  );
}

/**
 * A secret field (obfuscation password) made like the server-access password: dots when hidden, an eye to show it,
 * copy and "generate" inside the box, one short line under it. The panel never sends a stored secret back (it
 * says "••••"), so a saved one can be replaced but not read; a value typed or generated here stays local until the
 * profile is saved, so showing and copying it costs nothing.
 */
function SecretField({ label, value, invalid, t, make, onChange }: { label: string; value: unknown; invalid: boolean; t: Tx; make: () => string; onChange: (v: string) => void }) {
  const toast = useToast();
  const [shown, setShown] = useState(false);
  const stored = value === MASK; // kept on the server: nothing to show or copy
  const text = stored ? "" : String(value ?? "");

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      toast(t("common.copied"));
    } catch {
      toast(t("common.copyFailed"));
    }
  }
  const btn = "flex size-[30px] flex-none items-center justify-center rounded-lg text-muted transition-colors hover:bg-surface-2 hover:text-fg disabled:pointer-events-none disabled:opacity-35";
  const eye = t(shown ? "profiles.secret.hide" : "profiles.secret.show");
  return (
    <div className="flex w-[300px] max-w-full flex-col gap-1">
      <div className={cx("flex h-[38px] items-center gap-0.5 rounded-xl border bg-canvas pr-1 pl-3 transition-colors duration-200 focus-within:border-accent", invalid ? "border-danger" : "border-line")}>
        <input
          type={shown && !stored ? "text" : "password"}
          aria-label={label}
          aria-invalid={invalid || undefined}
          autoComplete="new-password"
          spellCheck={false}
          // a saved secret shows as dots; typing over it starts a new value, emptying the box keeps the saved one
          placeholder={stored ? "••••••••••••" : ""}
          value={text}
          onChange={(e) => onChange(e.target.value === "" ? MASK : e.target.value)}
          className="min-w-0 flex-1 bg-transparent font-mono text-[13px] text-fg outline-none placeholder:text-fg"
        />
        <button type="button" className={btn} disabled={stored} aria-pressed={shown} aria-label={eye} title={eye} onClick={() => setShown(!shown)}>
          <Icon name={shown && !stored ? "eyeOff" : "eye"} size={15} />
        </button>
        <button type="button" className={btn} disabled={stored || text === ""} aria-label={t("common.copy")} title={t("common.copy")} onClick={() => void copy()}>
          <Icon name="copy" size={14} />
        </button>
        <button
          type="button"
          className={cx(btn, "text-accent-text")}
          aria-label={t("profiles.generate")}
          title={t("profiles.generate")}
          onClick={() => {
            onChange(make());
            setShown(true);
          }}
        >
          <Icon name="refresh" size={15} />
        </button>
      </div>
      <span className="text-[11px] leading-snug text-muted">{stored ? t("profiles.secret.stored") : t("profiles.secret.local")}</span>
    </div>
  );
}
