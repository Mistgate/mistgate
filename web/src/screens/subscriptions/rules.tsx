import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { useToast } from "@/components/ui/toast";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useTx, type Tx } from "@/screens/users/t";
import { useDebounced } from "@/screens/users/ui";
import { ClientApiCard } from "./client-api";
import { DeviceIdleCard } from "./device-idle";
import { DeviceLimitCard } from "./device-limit";
import { LinkAutoCard } from "./link-auto";
import { LinkAwgCard } from "./link-awg";
import { formatKey, move, ruleFormats, singboxAgents, type Rule, type Settings } from "./model";
import { Glyph, Grid, path, Section, Svg } from "./parts";
import { testAgentQuery, useSaveSettings } from "./queries";
import { sbDemo } from "./demo-sb";

// Client User-Agents to try: the chip label and what it fills in.
export const samples = [
  ["Happ", "Happ/3.2.1 (iPhone; iOS 18.0)"],
  ["AmneziaVPN", "AmneziaVPN/4.8.2 Android"],
  ["Mihomo", "mihomo/1.18.9"],
  ["sing-box", singboxAgents[0]],
  ["v2rayNG", "v2rayNG/1.10.20"],
  ["Safari", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1"],
] as const;

/** The line under the rules that says what was saved (and undoes a removal) for a few seconds. */
type Note = { text: string; undo?: () => void };

/**
 * "Who gets what": which format a User-Agent gets, the two switches of what the apps get, the device limit and the inactive devices, and
 * beside them the tester of a User-Agent and the Mistgate app. Every change is saved at once (the order is the rules).
 */
export function RulesTab({ settings }: { settings: Settings }) {
  const t = useTx();
  return (
    <Grid
      main={
        <>
          <RulesCard settings={settings} />
          <Section tone="sand" icon={path.sliders} title={t("sx.rules.gets")}>
            <div className="sb-tgs">
              <LinkAutoCard settings={settings} />
              <LinkAwgCard settings={settings} />
            </div>
          </Section>
          <DeviceLimitCard settings={settings} />
          <DeviceIdleCard settings={settings} />
        </>
      }
      side={
        <>
          <Tester settings={settings} />
          <ClientApiCard />
        </>
      }
    />
  );
}

function RulesCard({ settings }: { settings: Settings }) {
  const t = useTx();
  const toast = useToast();
  const save = useSaveSettings();
  const rules = settings.rules;
  const [note, setNote] = useState<Note | null>(() => sbDemo.note ?? null);
  const [adding, setAdding] = useState(() => sbDemo.adding ?? false);
  const clear = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(clear.current), []);
  const say = (n: Note) => {
    setNote(n);
    clearTimeout(clear.current);
    clear.current = setTimeout(() => setNote(null), 6000);
  };
  const fmtName = (f: SubFormat) => t(formatKey[f] ?? "subs.fmt.base64");

  const run = (change: (s: Settings) => Settings, done?: string, undo?: () => void) =>
    save.mutateAsync(change).then(
      () => {
        if (done) say({ text: done, undo });
      },
      (e: unknown) => {
        toast.error(errorText(e, t));
        throw e;
      },
    );
  const hit = sbDemo.hit ?? -1;

  return (
    <Section
      tone="mint"
      icon={path.list}
      title={t("subs.rules.title")}
      shot="subs-rules"
      side={
        <Button className="sb-btn" onClick={() => setAdding(true)} disabled={adding}>
          <Svg d={path.plus} />
          {t("subs.rules.add")}
        </Button>
      }
    >
      <p className="sb-lead">{t("sx.rules.lead")}</p>
      <div className="sb-rls">
        {rules.length === 0 && !adding && (
          <p className="sb-emp">
            <b>{t("subs.rules.noneT")}</b> {t("subs.rules.noneBody")}
          </p>
        )}
        {rules.map((r, i) => (
          <RuleRow
            key={`${i}:${r.uaContains}:${r.format}`}
            rule={r}
            i={i}
            total={rules.length}
            hit={hit === i}
            t={t}
            taken={rules.filter((_, j) => j !== i).map((x) => x.uaContains.toLowerCase())}
            onMove={(from, to) => void run((s) => ({ ...s, rules: move(s.rules, from, to) }), t("subs.rules.moved")).catch(() => {})}
            onEdit={(patch) => run((s) => ({ ...s, rules: s.rules.map((x, j) => (j === i ? { ...x, ...patch } : x)) }), t("subs.saved"))}
            onRemove={() => {
              const gone = rules[i]!;
              // Undo puts it back where it was (or last, if the list got shorter meanwhile)
              const restore = () => void run((s) => ({ ...s, rules: [...s.rules.slice(0, i), gone, ...s.rules.slice(i)] }), t("subs.rules.restored")).catch(() => {});
              void run((s) => ({ ...s, rules: s.rules.filter((_, j) => j !== i) }), t("subs.rules.removedName", { ua: gone.uaContains || t("sx.rules.noText") }), restore).catch(() => {});
            }}
            fmtName={fmtName}
          />
        ))}
        {adding && <AddRow rules={rules} t={t} fmtName={fmtName} onCancel={() => setAdding(false)} onAdd={(r) => run((s) => ({ ...s, rules: [...s.rules, r] }), t("subs.rules.added")).then(() => setAdding(false))} />}
        <div className={cx("sb-rl sb-rest", sbDemo.rest && "hit")}>
          <span className="sb-no mono" aria-hidden="true">
            ∗
          </span>
          <b>{t("subs.rules.fallback")}</b>
          <Svg d={path.arrowR} className="sb-arr flip" />
          <span className="sb-rt">{t("sx.rules.rest")}</span>
        </div>
      </div>
      {note && (
        <p className="sb-msg" role="status">
          <Glyph kind="ok" />
          {note.text}
          {note.undo && (
            <button
              type="button"
              className="an-lk"
              onClick={() => {
                setNote(null);
                note.undo?.();
              }}
            >
              {t("common.undo")}
            </button>
          )}
        </p>
      )}
    </Section>
  );
}

type RowProps = { rule: Rule; i: number; total: number; hit: boolean; t: Tx; taken: string[]; fmtName: (f: SubFormat) => string; onMove: (from: number, to: number) => void; onEdit: (patch: Partial<Rule>) => Promise<unknown>; onRemove: () => void };

function RuleRow({ rule, i, total, hit, t, taken, fmtName, onMove, onEdit, onRemove }: RowProps) {
  const [text, setText] = useState(rule.uaContains);
  const n = i + 1;
  // an empty text would match every client, a repeated one never fires: both go back to the saved text
  const commit = () => {
    const v = text.trim();
    if (v === rule.uaContains) return;
    if (v === "" || taken.includes(v.toLowerCase())) setText(rule.uaContains);
    else void onEdit({ uaContains: v }).catch(() => setText(rule.uaContains));
  };
  return (
    <div className={cx("sb-rl", hit && "hit")}>
      <span className="sb-no mono">{n}</span>
      <label className="sb-ua">
        <span>{t("sx.rules.ua")}</span>
        <input
          className="an-i mono"
          type="text"
          aria-label={t("subs.rules.uaLabel", { n })}
          placeholder={t("sx.rules.uaPh")}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onBlur={commit}
          onKeyDown={(e) => {
            if (e.key === "Enter") e.currentTarget.blur();
            if (e.key === "Escape") setText(rule.uaContains);
          }}
          autoComplete="off"
          spellCheck={false}
        />
      </label>
      <Svg d={path.arrowR} className="sb-arr flip" />
      <div className="sb-sel">
        <Select aria-label={t("subs.rules.formatLabel", { n })} value={String(rule.format)} onValueChange={(v) => void onEdit({ format: Number(v) as SubFormat }).catch(() => {})} options={ruleFormats.map((f) => ({ value: String(f), label: fmtName(f) }))} />
      </div>
      <span className="sb-mv">
        <button type="button" className="sb-ic" aria-label={t("subs.rules.up", { n })} aria-disabled={i === 0} onClick={() => i > 0 && onMove(i, i - 1)}>
          <Svg d={path.up} />
        </button>
        <button type="button" className="sb-ic" aria-label={t("subs.rules.down", { n })} aria-disabled={i === total - 1} onClick={() => i < total - 1 && onMove(i, i + 1)}>
          <Svg d={path.chevD} />
        </button>
        <button type="button" className="sb-ic" aria-label={t("subs.rules.remove", { n })} onClick={onRemove}>
          <Svg d={path.x} />
        </button>
      </span>
    </div>
  );
}

/** The row of a rule that is not made yet: the text and the format, saved when the text is left (Enter), dropped by Esc or the cross. */
function AddRow({ rules, t, fmtName, onAdd, onCancel }: { rules: Rule[]; t: Tx; fmtName: (f: SubFormat) => string; onAdd: (r: Rule) => Promise<unknown>; onCancel: () => void }) {
  const [ua, setUa] = useState("");
  const [format, setFormat] = useState<SubFormat>(SubFormat.BASE64_URIS);
  const [busy, setBusy] = useState(false);
  const v = ua.trim();
  const dup = v !== "" && rules.some((r) => r.uaContains.toLowerCase() === v.toLowerCase());
  const n = rules.length + 1;
  async function add() {
    if (v === "" || dup || busy) return;
    setBusy(true);
    try {
      await onAdd({ uaContains: v, format });
    } catch {
      // the toast already told why; the text stays for another try
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <div className="sb-rl sb-new" data-shot="subs-rule-new">
        <span className="sb-no mono">{n}</span>
        <label className="sb-ua">
          <span>{t("sx.rules.ua")}</span>
          <input
            className="an-i mono"
            type="text"
            autoFocus
            aria-label={t("subs.rules.uaLabel", { n })}
            aria-invalid={dup || undefined}
            placeholder={t("subs.rules.addPh")}
            value={ua}
            onChange={(e) => setUa(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void add();
              if (e.key === "Escape") onCancel();
            }}
            onBlur={() => (v === "" ? onCancel() : void add())}
            autoComplete="off"
            spellCheck={false}
          />
        </label>
        <Svg d={path.arrowR} className="sb-arr flip" />
        <div className="sb-sel">
          <Select aria-label={t("subs.rules.formatLabel", { n })} value={String(format)} onValueChange={(x) => setFormat(Number(x) as SubFormat)} options={ruleFormats.map((f) => ({ value: String(f), label: fmtName(f) }))} />
        </div>
        <span className="sb-mv">
          <button type="button" className="sb-ic" aria-label={t("subs.rules.cancel")} onMouseDown={(e) => e.preventDefault()} onClick={onCancel}>
            <Svg d={path.x} />
          </button>
        </span>
      </div>
      {dup && (
        <p className="pp-q1 bad sb-pad" role="alert">
          {t("subs.rules.dup")}
        </p>
      )}
    </>
  );
}

/** "Try a User-Agent": the rule (or the rest) the panel would pick for a pasted User-Agent. */
function Tester({ settings }: { settings: Settings }) {
  const t = useTx();
  const [ua, setUa] = useState<string>(() => sbDemo.ua ?? samples[2][1]);
  const probe = useDebounced(ua.trim(), sbDemo.ua !== undefined ? 0 : 300);
  const test = useQuery({ ...testAgentQuery(probe), enabled: probe !== "", placeholderData: keepPreviousData });
  const shown = probe !== "" && test.data !== undefined;
  const hitRule = shown && test.data!.ruleIndex >= 0 ? settings.rules[test.data!.ruleIndex] : undefined;
  const fmtName = (f: SubFormat) => t(formatKey[f] ?? "subs.fmt.base64");

  let title: string;
  let sub = "";
  if (probe === "") title = t("sx.test.empty");
  else if (!test.data) title = test.isError ? errorText(test.error, t) : t("subs.test.busy");
  else if (test.data.ruleIndex >= 0) {
    title = t("sx.test.hit", { n: test.data.ruleIndex + 1, format: fmtName(test.data.format) });
    sub = hitRule ? t("sx.test.has", { text: hitRule.uaContains }) : "";
  } else {
    title = t("sx.test.rest", { format: fmtName(test.data.format) });
    sub = t(test.data.browser ? "sx.test.nobr" : "sx.test.noapp");
  }

  return (
    <Section tone="sky" icon={path.search} title={t("sx.test.title")} shot="subs-tester">
      <div className="sb-pad">
        <p className="pp-q1">{t("sx.test.lead")}</p>
        <input className="an-i mono sb-uain" type="text" aria-label={t("subs.test.ph")} placeholder={t("subs.test.ph")} value={ua} onChange={(e) => setUa(e.target.value)} autoComplete="off" spellCheck={false} />
        <div className="sb-ex" role="group" aria-label={t("subs.test.samples")}>
          {samples.map(([label, sample]) => (
            <button key={label} type="button" aria-pressed={ua === sample} onClick={() => setUa(sample)}>
              {label}
            </button>
          ))}
        </div>
        <div className={cx("sb-res", !shown ? "none" : "ok")} role="status">
          <Glyph kind={shown ? "ok" : "none"} />
          <span>
            <b>{title}</b>
            {sub && <em>{sub}</em>}
          </span>
        </div>
      </div>
    </Section>
  );
}
