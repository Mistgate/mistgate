import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { Card, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Select } from "@/components/ui/select";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useTx, type Tx } from "@/screens/users/t";
import { useDebounced } from "@/screens/users/ui";
import { formatKey, move, ruleFormats, type Rule, type Settings } from "./model";
import { testAgentQuery, useSaveSettings } from "./queries";
import { Lead } from "./ui";

// Client User-Agents to try: the chip label and what it fills in.
const samples = [
  ["Happ", "Happ/3.2.1 (iPhone; iOS 18.0)"],
  ["AmneziaVPN", "AmneziaVPN/4.8.2 Android"],
  ["Mihomo", "mihomo/1.18.9"],
  ["Safari", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1"],
] as const;

/** "Who gets what": which format a User-Agent gets. The rules on the left, a tester beside them. Every change is saved at once (the order is the rules). */
export function RulesTab({ settings }: { settings: Settings }) {
  const t = useTx();
  const toast = useToast();
  const save = useSaveSettings();
  const rules = settings.rules;
  const [ua, setUa] = useState<string>(samples[2][1]);
  const [adding, setAdding] = useState(false);
  const probe = useDebounced(ua.trim(), 300);
  const test = useQuery({ ...testAgentQuery(probe), enabled: probe !== "", placeholderData: keepPreviousData });
  const shown = probe !== "" && test.data !== undefined;
  const hit = shown && test.data!.ruleIndex >= 0 ? test.data!.ruleIndex : -1;
  const fmtName = (f: SubFormat) => t(formatKey[f] ?? "subs.fmt.base64");
  const fmtOption = fmtName;

  const run = (change: (s: Settings) => Settings, done?: string, undo?: () => void) =>
    save.mutateAsync(change).then(
      () => {
        if (done) toast(done, undo && { undo });
      },
      (e: unknown) => {
        toast.error(errorText(e, t));
        throw e;
      },
    );

  let result: string;
  if (probe === "") result = t("subs.test.empty");
  else if (!test.data) result = test.isError ? errorText(test.error, t) : t("subs.test.busy");
  else if (test.data.ruleIndex >= 0) result = t("subs.test.match", { n: test.data.ruleIndex + 1, format: fmtName(test.data.format) });
  else result = t(test.data.browser ? "subs.test.noneBrowser" : "subs.test.noneOther");

  return (
    <div className="flex flex-col gap-4">
      <Lead>{t("subs.rules.lead")}</Lead>
      <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
        <Card lg className="flex min-w-0 flex-col gap-3 p-4">
          <div className="flex flex-wrap items-center gap-3">
            <div className="flex min-w-[220px] flex-1 flex-col gap-0.5">
              <SectionLabel icon="filter" tone="sage">
                {t("subs.rules.title")}
              </SectionLabel>
              <span className="text-xs leading-snug text-muted">{t("subs.rules.hint")}</span>
            </div>
            {rules.length > 0 && !adding && (
              <Button variant="secondary" onClick={() => setAdding(true)}>
                <Icon name="plus" size={14} />
                {t("subs.rules.add")}
              </Button>
            )}
          </div>

          {adding && <AddRule rules={rules} t={t} fmtOption={fmtOption} onCancel={() => setAdding(false)} onAdd={(r) => run((s) => ({ ...s, rules: [...s.rules, r] }), t("subs.rules.added")).then(() => setAdding(false))} />}

          {rules.length === 0 && !adding && (
            <div className="flex flex-col items-center gap-2 rounded-card bg-surface-2 px-5 py-7 text-center">
              <span aria-hidden className="grid size-11 place-items-center rounded-xl bg-surface text-muted">
                <Icon name="link" size={20} />
              </span>
              <b className="text-sm">{t("subs.rules.noneT")}</b>
              <p className="max-w-[420px] text-xs leading-normal text-muted">{t("subs.rules.noneBody")}</p>
              <Button variant="primary" size="lg" className="mt-1" onClick={() => setAdding(true)}>
                <Icon name="plus" size={14} />
                {t("subs.rules.addFirst")}
              </Button>
            </div>
          )}

          <div className="flex flex-col gap-1">
            <RuleList
              rules={rules}
              hit={hit}
              t={t}
              fmtOption={fmtOption}
              onMove={(from, to) => void run((s) => ({ ...s, rules: move(s.rules, from, to) }), t("subs.rules.moved")).catch(() => {})}
              onEdit={(i, patch) => run((s) => ({ ...s, rules: s.rules.map((r, j) => (j === i ? { ...r, ...patch } : r)) }), t("subs.saved"))}
              onRemove={(i) => {
                const gone = rules[i]!;
                // Undo puts it back where it was (or last, if the list got shorter meanwhile)
                const restore = () => void run((s) => ({ ...s, rules: [...s.rules.slice(0, i), gone, ...s.rules.slice(i)] }), t("subs.rules.restored")).catch(() => {});
                void run((s) => ({ ...s, rules: s.rules.filter((_, j) => j !== i) }), t("subs.rules.removedName", { ua: gone.uaContains }), restore).catch(() => {});
              }}
            />
            <div className={cx("flex min-h-11 flex-wrap items-center gap-x-3 gap-y-1 rounded-xl border border-dashed px-3 py-2 transition-colors duration-200", shown && hit < 0 ? "border-accent-line bg-accent-soft" : "border-line")}>
              <Icon name="arrowDown" size={14} className="text-faint" />
              <b className="text-[13px]">{t("subs.rules.fallback")}</b>
              <span className="min-w-0 flex-1 text-xs text-muted">{t("subs.rules.fallbackV")}</span>
            </div>
          </div>
        </Card>

        <Card lg className="flex min-w-0 flex-col gap-3 p-4 lg:sticky lg:top-[72px]">
          <div className="flex flex-col gap-0.5">
            <SectionLabel icon="code" tone="mint">
              {t("subs.test")}
            </SectionLabel>
            <span className="text-xs leading-snug text-muted">{t("subs.test.hint")}</span>
          </div>
          <input
            aria-label={t("subs.test.ph")}
            placeholder={t("subs.test.ph")}
            value={ua}
            onChange={(e) => setUa(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            className="h-10 w-full min-w-0 rounded-field border border-line bg-canvas px-3 font-mono text-xs text-fg outline-none transition-colors duration-200 focus:border-accent"
          />
          <div role="group" aria-label={t("subs.test.samples")} className="flex flex-wrap items-center gap-1.5">
            <span className="text-[11px] text-muted">{t("subs.test.samples")}:</span>
            {samples.map(([label, sample]) => (
              <button
                key={label}
                type="button"
                onClick={() => setUa(sample)}
                className={cx("h-[26px] rounded-[13px] px-2.5 text-[11px] font-bold transition-colors", ua === sample ? "bg-accent-soft text-fg" : "bg-surface-2 text-muted hover:text-fg")}
              >
                {label}
              </button>
            ))}
          </div>
          <div role="status" className={cx("flex items-start gap-2.5 rounded-xl px-3 py-2.5 text-[13px] leading-snug", hit >= 0 || (shown && hit < 0) ? "bg-accent-soft" : "bg-surface-2 text-muted")}>
            <Icon name={shown ? "check" : "info"} size={15} className="mt-px text-accent-text" />
            <span className="min-w-0">{result}</span>
          </div>
        </Card>
      </div>
    </div>
  );
}

type ListProps = { rules: Rule[]; hit: number; t: Tx; fmtOption: (f: SubFormat) => string; onMove: (from: number, to: number) => void; onEdit: (i: number, patch: Partial<Rule>) => Promise<unknown>; onRemove: (i: number) => void };

function RuleList({ rules, hit, t, fmtOption, onMove, onEdit, onRemove }: ListProps) {
  const [drag, setDrag] = useState<number | null>(null);
  const [over, setOver] = useState<number | null>(null);
  return (
    <>
      {rules.map((r, i) => (
        <RuleRow
          key={`${i}:${r.uaContains}:${r.format}`}
          rule={r}
          i={i}
          total={rules.length}
          hit={hit === i}
          over={over === i && drag !== null && drag !== i}
          t={t}
          fmtOption={fmtOption}
          taken={rules.filter((_, j) => j !== i).map((x) => x.uaContains.toLowerCase())}
          onDragStart={() => setDrag(i)}
          onDragOver={() => setOver(i)}
          onDrop={() => {
            if (drag !== null) onMove(drag, i);
            setDrag(null);
            setOver(null);
          }}
          onDragEnd={() => {
            setDrag(null);
            setOver(null);
          }}
          onMove={onMove}
          onEdit={onEdit}
          onRemove={onRemove}
        />
      ))}
    </>
  );
}

type RowProps = {
  rule: Rule;
  i: number;
  total: number;
  hit: boolean;
  over: boolean;
  t: Tx;
  fmtOption: (f: SubFormat) => string;
  taken: string[];
  onDragStart: () => void;
  onDragOver: () => void;
  onDrop: () => void;
  onDragEnd: () => void;
  onMove: (from: number, to: number) => void;
  onEdit: (i: number, patch: Partial<Rule>) => Promise<unknown>;
  onRemove: (i: number) => void;
};

function RuleRow({ rule, i, total, hit, over, t, fmtOption, taken, onDragStart, onDragOver, onDrop, onDragEnd, onMove, onEdit, onRemove }: RowProps) {
  const row = useRef<HTMLDivElement>(null);
  const [text, setText] = useState(rule.uaContains);
  const n = i + 1;
  // an empty text would match every client, a repeated one never fires: both go back to the saved text
  const commit = () => {
    const v = text.trim();
    if (v === rule.uaContains) return;
    if (v === "" || taken.includes(v.toLowerCase())) setText(rule.uaContains);
    else void onEdit(i, { uaContains: v }).catch(() => setText(rule.uaContains));
  };
  return (
    <div
      ref={row}
      onDragOver={(e) => {
        e.preventDefault();
        onDragOver();
      }}
      onDrop={(e) => {
        e.preventDefault();
        onDrop();
      }}
      className={cx(
        "flex min-h-[52px] flex-wrap items-center gap-x-2.5 gap-y-1.5 rounded-xl border px-2 py-1.5 transition-colors duration-200",
        hit ? "border-accent-line bg-accent-soft" : "border-line bg-surface",
        over && "border-t-accent",
      )}
    >
      <span
        draggable
        role="img"
        aria-label={t("subs.rules.drag")}
        title={t("subs.rules.drag")}
        onDragStart={(e) => {
          e.dataTransfer.effectAllowed = "move";
          e.dataTransfer.setData("text/plain", String(i));
          if (row.current) e.dataTransfer.setDragImage(row.current, 12, 20);
          onDragStart();
        }}
        onDragEnd={onDragEnd}
        className="flex h-7 w-5 flex-none cursor-grab items-center justify-center text-faint active:cursor-grabbing"
      >
        <Icon name="grip" size={16} strokeWidth={3} />
      </span>
      <span className="grid size-5 flex-none place-items-center rounded-md bg-surface-2 font-mono text-[11px] font-bold text-muted">{n}</span>
      <input
        aria-label={t("subs.rules.uaLabel", { n })}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter") e.currentTarget.blur();
          if (e.key === "Escape") setText(rule.uaContains);
        }}
        autoComplete="off"
        spellCheck={false}
        className="h-[30px] min-w-[120px] flex-1 rounded-lg border border-line bg-canvas px-2.5 font-mono text-xs text-fg outline-none transition-colors duration-200 focus:border-accent"
      />
      <span aria-hidden className="hidden text-faint sm:inline">→</span>
      <div className="w-[170px] max-w-full">
        <Select
          compact
          aria-label={t("subs.rules.formatLabel", { n })}
          value={String(rule.format)}
          onValueChange={(v) => void onEdit(i, { format: Number(v) as SubFormat }).catch(() => {})}
          options={ruleFormats.map((f) => ({ value: String(f), label: fmtOption(f) }))}
        />
      </div>
      <span className="flex-1" />
      <IconButton aria-label={t("subs.rules.up", { n })} variant="flat" disabled={i === 0} onClick={() => onMove(i, i - 1)}>
        <Icon name="arrowUp" size={14} />
      </IconButton>
      <IconButton aria-label={t("subs.rules.down", { n })} variant="flat" disabled={i === total - 1} onClick={() => onMove(i, i + 1)}>
        <Icon name="arrowDown" size={14} />
      </IconButton>
      <IconButton aria-label={t("subs.rules.remove", { n })} variant="flat" onClick={() => onRemove(i)}>
        <Icon name="trash" size={14} />
      </IconButton>
    </div>
  );
}

function AddRule({ rules, t, fmtOption, onAdd, onCancel }: { rules: Rule[]; t: Tx; fmtOption: (f: SubFormat) => string; onAdd: (r: Rule) => Promise<unknown>; onCancel: () => void }) {
  const [ua, setUa] = useState("");
  const [format, setFormat] = useState<SubFormat>(SubFormat.BASE64_URIS);
  const [busy, setBusy] = useState(false);
  const v = ua.trim();
  const dup = v !== "" && rules.some((r) => r.uaContains.toLowerCase() === v.toLowerCase());
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
    <form
      className="screen-enter flex flex-col gap-3 rounded-card bg-surface-2 p-3"
      onSubmit={(e) => {
        e.preventDefault();
        void add();
      }}
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start">
        <TextField className="sm:flex-1" mono autoFocus label={t("subs.rules.addUa")} placeholder={t("subs.rules.addPh")} value={ua} onChange={(e) => setUa(e.target.value)} error={dup ? t("subs.rules.dup") : undefined} hint={t("subs.rules.addHint")} autoComplete="off" spellCheck={false} />
        <div className="flex flex-col gap-1.5 sm:w-[210px]">
          <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("subs.rules.addFormat")}</span>
          <Select aria-label={t("subs.rules.formatLabel", { n: rules.length + 1 })} value={String(format)} onValueChange={(x) => setFormat(Number(x) as SubFormat)} options={ruleFormats.map((f) => ({ value: String(f), label: fmtOption(f) }))} />
        </div>
      </div>
      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={onCancel}>
          {t("subs.rules.cancel")}
        </Button>
        <Button type="submit" variant="primary" disabled={v === "" || dup || busy}>
          {t("subs.rules.add")}
        </Button>
      </div>
    </form>
  );
}
