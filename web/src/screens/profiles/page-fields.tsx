import { useId, useState, type ReactNode } from "react";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { cx } from "@/lib/cx";
import { useTx, type Tx } from "@/screens/users/t";
import { G } from "@/screens/users/user-bits";
import { D, Gl } from "./pf-bits";
import { errorsOf, getAt, isChanged, listIssues, makeSecret, setAt, type Field, type Group, type Settings } from "./schema";
import { NumberInput, SecretField, StringList, type Problem } from "./schema-form";
import { problemText } from "./problem-text";
import "./profiles.css";
import "./profile-page.css";
import "./profile-settings.css";

/** What a schema group is about, as the board draws its cards: basics sand, the disguise sage, the network sky, the rest lavender. */
const marks: Record<string, { tone: string; d: string }> = {
  basics: { tone: "sand", d: "M4 7h10M18 7h2M4 12h4M12 12h8M4 17h12M14 5v4M8 10v4M16 15v4" },
  obfuscation: { tone: "sage", d: "M4 6c5-2 11-2 16 0v5c0 5-3.5 8-8 9-4.5-1-8-4-8-9zM8.5 11h2M13.5 11h2M9.5 15c1.5 1 3.5 1 5 0" },
  tls: { tone: "sky", d: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.7 2.5 15.3 0 18M12 3c-2.5 2.7-2.5 15.3 0 18" },
  advanced: { tone: "lav", d: "M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM12 3v3M12 18v3M3 12h3M18 12h3M5.6 5.6l2.1 2.1M16.3 16.3l2.1 2.1M5.6 18.4l2.1-2.1M16.3 7.7l2.1-2.1" },
};
const markOf = (id: string) => marks[id] ?? marks.basics!;

type Props = {
  protocol: string;
  groups: Group[];
  settings: Settings;
  base: Settings;
  problems: readonly Problem[];
  onChange: (next: Settings) => void;
  hidden?: ReadonlySet<string>;
  readOnly?: ReadonlySet<string>;
  offers?: (f: Field, value: string, settings: Settings) => boolean;
  extra?: (f: Field) => ReactNode;
  /** Rows that lead the first card (the profile's name). */
  lead?: ReactNode;
  /** Opened groups start folded when they are "advanced"; a group with a problem is always open. */
  className?: string;
};

/**
 * The profile's settings as the board draws them: a card per group of the plugin's schema, its title over rows of label (with the dot
 * of a changed value, the badge of a critical one and the hint) and control, a field's problems under its control; the advanced group
 * folds into a card of its own. Text the dictionary has for a field (profiles.f.<protocol>.<field>.title|hint|enum.<value>) replaces
 * the schema's own English. `data-k` on a row is what "To the field" of a remark scrolls to.
 */
export function PageFields({ protocol, groups, settings, base, problems, onChange, hidden, readOnly, extra, offers, lead }: Props) {
  const t = useTx();
  const [open, setOpen] = useState<Record<string, boolean>>({});
  if (groups.length === 0) return <section className="card pp-sec pp-sh"><p className="pp-quiet">{t("profiles.noSchema")}</p></section>;
  const shown = groups.map((group) => (hidden ? { ...group, fields: group.fields.filter((f) => !hidden.has(f.id)) } : group));
  // the name leads the first card that has fields (the first card at all, when none has)
  const firstId = (shown.find((g) => g.fields.length > 0) ?? shown[0])?.id;
  return (
    <>
      {shown.map((g) => {
        const isFirst = g.id === firstId;
        if (g.fields.length === 0 && !(isFirst && lead)) return null;
        const changed = g.fields.filter((f) => isChanged(f, settings, base)).length;
        const errs = g.fields.filter((f) => errorsOf(f, problems).length > 0).length;
        const folds = g.id === "advanced";
        const isOpen = !folds || errs > 0 || (open[g.id] ?? false);
        const m = markOf(g.id);
        const title = t.opt(`profiles.group.${g.id}`) ?? g.id;
        const rows = g.fields.map((f) => (
          <FieldRow
            key={f.key}
            protocol={protocol}
            f={f}
            t={t}
            settings={settings}
            locked={readOnly?.has(f.id) ?? false}
            offers={offers}
            extra={extra?.(f)}
            dirty={isChanged(f, settings, base)}
            problems={errorsOf(f, problems)}
            onChange={(path, value) => onChange(setAt(settings, path, value))}
          />
        ));
        return (
          <section key={g.id} className="card pp-sec" data-group={g.id}>
            <div className="pp-sh">
              <span className={cx("pp-ic", m.tone)} aria-hidden>
                <G d={m.d} size={15} stroke={1.5} />
              </span>
              <h3>{title}</h3>
            </div>
            {folds ? (
              <div className={cx("pp-adv", isOpen && "open", changed > 0 && "ch")}>
                <button type="button" className="pp-advh" aria-expanded={isOpen} onClick={() => setOpen({ ...open, [g.id]: !isOpen })}>
                  <G d={D.chev} size={16} stroke={1.5} />
                  <span className="t">{t("pf.st.advanced")}</span>
                  <em>{errs > 0 ? t.n("profiles.errors", errs) : changed > 0 ? t("pf.st.changed", { n: changed }) : ""}</em>
                </button>
                {isOpen && <div className="pp-advb">{rows}</div>}
              </div>
            ) : (
              <div className="pp-rows">
                {isFirst && lead}
                {rows}
              </div>
            )}
          </section>
        );
      })}
    </>
  );
}

/** One row of a protocol's form, from the schema's field: its label, hint, control and problems. `after` sits in the box's line (a "Random port" button), `below` under the control,
 *  `masked` shows a typed value as dots (the board hides the block of AmneziaWG's keys until "Show values"). */
export function FieldRow({ protocol, f, t, settings, locked, offers, extra, dirty, problems, onChange, after, below, masked }: { protocol: string; f: Field; t: Tx; settings: Settings; locked: boolean; offers?: Props["offers"]; extra?: ReactNode; dirty: boolean; problems: Problem[]; onChange: (path: string[], value: unknown) => void; after?: ReactNode; below?: ReactNode; masked?: boolean }) {
  const copy = (part: string) => t.opt(`profiles.f.${protocol}.${f.id}.${part}`);
  const label = copy("title") ?? f.title;
  const hint = copy("hint") ?? f.hint;
  const unit = f.unit ? (t.opt(`profiles.unit.${f.unit}`) ?? f.unit) : "";
  const invalid = problems.length > 0;
  const choices = f.choices.filter((c) => offers?.(f, c.value, settings) ?? true).map((c) => ({ ...c, label: copy(`enum.${c.value}`) ?? c.label }));
  const errId = useId();
  const describedBy = invalid ? errId : undefined;
  // a list shows a bad item's problem under that item; the server's sentence about the whole list would repeat it
  const onItem = f.kind === "list" ? new Set<string>(listIssues(asList(getAt(settings, f.path)), f.pattern).filter((x) => x !== null)) : null;
  const shown = onItem ? problems.filter((p) => !(p.pointer === f.key && p.code && onItem.has(p.code))) : problems;

  let control: ReactNode = null;
  switch (f.kind) {
    case "toggle":
      control = <Switch aria-label={label} checked={!!getAt(settings, f.path)} onCheckedChange={(on) => onChange(f.path, on)} className="self-start" />;
      break;
    case "choice": {
      const cur = String(getAt(settings, f.path) ?? "");
      const pick = (v: string) => onChange(f.path, choices.find((c) => c.value === v)?.raw ?? v);
      control = f.segmented ? (
        <Segmented aria-label={label} value={cur} onValueChange={pick} options={choices.map((c) => ({ value: c.value, label: c.label }))} className="pp-seg flex-wrap" />
      ) : (
        <div className="w-[240px] max-w-full">
          <Select aria-label={label} value={cur} onValueChange={pick} options={choices.map((c) => ({ value: c.value, label: c.label }))} />
        </div>
      );
      break;
    }
    case "number":
      control = (
        <div className={cx("pf-ib", !after && "pp-short")}>
          <NumberInput label={label} value={getAt(settings, f.path)} integer={f.integer} invalid={invalid} describedBy={describedBy} masked={masked} disabled={locked} onChange={(v) => onChange(f.path, v)} className="an-i mono" />
          {unit && <em>{unit}</em>}
          {after}
        </div>
      );
      break;
    case "range":
      control = (
        <div className="pf-ib pp-short pp-range">
          {f.parts!.map((p, i) => (
            <span key={i} className="pf-ib">
              {i === 1 && <em>–</em>}
              <NumberInput label={`${label} ${i === 0 ? "1" : "2"}`} value={getAt(settings, p)} integer invalid={invalid} onChange={(v) => onChange(p, v)} className="an-i mono" />
            </span>
          ))}
        </div>
      );
      break;
    case "generate":
    case "secret":
      control = <SecretField label={label} value={getAt(settings, f.path)} invalid={invalid} t={t} make={() => makeSecret(f)} onChange={(v) => onChange(f.path, v)} />;
      break;
    case "text":
      const box = (
        <input
          type={masked ? "password" : "text"}
          aria-label={label}
          aria-invalid={invalid || undefined}
          aria-describedby={describedBy}
          autoComplete="off"
          spellCheck={false}
          disabled={locked}
          placeholder={copy("placeholder")}
          value={String(getAt(settings, f.path) ?? "")}
          onChange={(e) => onChange(f.path, e.target.value)}
          className="an-i mono"
        />
      );
      control = unit ? (
        <div className="pf-ib pp-short">
          {box}
          <em>{unit}</em>
        </div>
      ) : (
        box
      );
      break;
    case "list":
      control = <StringList f={f} label={label} t={t} value={getAt(settings, f.path)} describedBy={shown.length > 0 ? errId : undefined} onChange={(v) => onChange(f.path, v)} />;
      break;
  }
  return (
    <div className={cx("pp-row", f.kind === "list" && "wide", invalid && "err")} data-k={f.id}>
      <div className="pp-lb">
        <b>
          {label}
          <i className={cx("pp-dot", dirty && "on")} aria-hidden />
        </b>
        {f.critical && (
          <i className={cx("pp-crit", dirty && "on")} title={t("pf.st.critTip")}>
            {t("pf.st.crit")}
          </i>
        )}
        {hint && <span>{hint}</span>}
        {extra}
      </div>
      <div className="pp-ct">
        {control}
        {below}
        {shown.length > 0 && (
          <span id={errId} className="pp-fes">
            {shown.map((p, i) => (
              <p key={i} className="pp-ferr" role="alert">
                <Gl k="bad" />
                {problemText(t, protocol, p) || p.message}
              </p>
            ))}
          </span>
        )}
      </div>
    </div>
  );
}

const asList = (v: unknown): string[] => (Array.isArray(v) ? v.map(String) : []);
