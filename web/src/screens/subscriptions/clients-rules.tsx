import { useQuery } from "@tanstack/react-query";
import { useId, useState } from "react";
import { Card, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { StatusPill } from "@/components/ui/status";
import { Switch } from "@/components/ui/switch";
import { useIsOwner } from "@/lib/updates";
import { clientCompatQuery, coreName, cores, platformName, platforms, ruleKey, rulesEqual, sameRule, useClientCompatSave, type CompatRule } from "@/lib/clientcompat";
import { useTx, type Tx } from "@/screens/users/t";
import { ConfirmModal } from "@/screens/users/ui";
import { inputCls, SaveBar } from "./ui";

// The columns of the table once the card is wide enough; narrower, a row stacks into a small form.
const cols = "@min-[720px]:grid-cols-[minmax(0,1fr)_72px_120px_120px_80px_96px]";
const versionCls = `${inputCls} max-w-24 font-mono`;
const selectCls = "h-10! rounded-field! text-[13px]!";

/** Only digits and dots: a version, not a sentence. */
const versionOnly = (v: string) => v.replace(/[^0-9.]/g, "").slice(0, 32);
/** The name an app writes in its User-Agent before "/", the way the table keeps it. */
const appId = (v: string) => v.toLowerCase().replace(/[\s/]/g, "").slice(0, 40);

/**
 * "Minimum versions": the table the verdicts come from, the built-in rows plus the owner's edits. Folded by default so the
 * tab is not a wall. The owner edits the fields (a row that differs from the built-in one has a dot and "Revert"; a row of
 * their own has "Remove"), then saves the whole table once; anybody else reads it.
 */
export function MinimumsCard({ initialOpen = false }: { initialOpen?: boolean }) {
  const t = useTx();
  const q = useQuery(clientCompatQuery);
  const save = useClientCompatSave();
  const [open, setOpen] = useState(initialOpen);
  const [draft, setDraft] = useState<CompatRule[] | null>(null);
  const [resetting, setResetting] = useState(false);
  const bodyId = useId();
  const base = q.data?.rules;
  const defaults = q.data?.defaults ?? [];
  const rules = draft ?? base ?? [];
  const dirty = !!draft && !!base && !rulesEqual(draft, base);
  const owner = useIsOwner();

  const edit = (fn: (cur: CompatRule[]) => CompatRule[]) => setDraft((d) => fn(d ?? base ?? []));
  const patch = (key: string, p: Partial<CompatRule>) => edit((cur) => cur.map((r) => (ruleKey(r) === key ? { ...r, ...p } : r)));
  const onSave = () => draft && save.mutate(draft, { onSuccess: () => setDraft(null) });

  return (
    <div className="flex flex-col gap-3.5">
      <Card lg className="flex min-w-0 flex-col gap-3 p-4">
        <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
          <div className="flex min-w-0 flex-col gap-1">
            <SectionLabel icon="sliders" tone="sand" as="h2">
              {t("cc.min.title")}
            </SectionLabel>
            <p className="text-xs leading-snug text-pretty text-muted">{t("cc.min.sub")}</p>
          </div>
          {q.data && (
            <Button variant="secondary" size="sm" aria-expanded={open} aria-controls={bodyId} onClick={() => setOpen((o) => !o)}>
              {open ? t("cc.min.hide") : t("cc.min.show", { n: rules.length })}
            </Button>
          )}
        </div>
        {q.isPending && <Pending compact />}
        {q.isError && !q.data && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
        {q.data && open && (
          <div id={bodyId} className="@container flex flex-col gap-3">
            <ul className="-mx-4 flex flex-col border-y border-line">
              <li aria-hidden className={`hidden h-10 items-center gap-x-3 px-4 text-[11px] font-bold tracking-[0.06em] text-muted uppercase @min-[720px]:grid ${cols}`}>
                <span>{t("cc.min.col.app")}</span>
                <span>{t("cc.min.col.core")}</span>
                <span>{t("cc.min.col.chrome")}</span>
                <span>{t("cc.min.col.other")}</span>
                <span>{t("cc.min.col.broken")}</span>
                <span />
              </li>
              {rules.map((r) => (
                <RuleRow
                  key={ruleKey(r)}
                  t={t}
                  rule={r}
                  def={defaults.find((d) => ruleKey(d) === ruleKey(r))}
                  owner={owner}
                  onPatch={(p) => patch(ruleKey(r), p)}
                  onRevert={(d) => patch(ruleKey(r), { core: d.core, unsupported: d.unsupported, minChrome: d.minChrome, minOther: d.minOther })}
                  onRemove={() => edit((cur) => cur.filter((x) => ruleKey(x) !== ruleKey(r)))}
                />
              ))}
            </ul>
            {owner ? (
              <>
                <AddRow t={t} rules={rules} onAdd={(r) => edit((cur) => [...cur, r])} />
                <div className="flex justify-end">
                  <Button variant="ghost" size="sm" disabled={!draft && !!base && rulesEqual(base, defaults)} onClick={() => setResetting(true)}>
                    {t("cc.min.resetAll")}
                  </Button>
                </div>
              </>
            ) : (
              <p className="text-xs text-muted">{t("cc.min.ownerOnly")}</p>
            )}
          </div>
        )}
      </Card>
      {dirty && <SaveBar busy={save.isPending} canSave onSave={onSave} onDiscard={() => setDraft(null)} />}
      <ConfirmModal
        open={resetting}
        onOpenChange={setResetting}
        title={t("cc.min.resetT")}
        description={t("cc.min.resetBody")}
        confirmLabel={t("cc.min.resetDo")}
        danger
        onConfirm={async () => {
          await save.mutateAsync(defaults);
          setDraft(null);
        }}
      />
    </div>
  );
}

function RuleRow({ t, rule, def, owner, onPatch, onRevert, onRemove }: { t: Tx; rule: CompatRule; def?: CompatRule; owner: boolean; onPatch: (p: Partial<CompatRule>) => void; onRevert: (d: CompatRule) => void; onRemove: () => void }) {
  const name = rule.title || rule.app;
  const changed = !!def && !sameRule(rule, def);
  const mine = !def;
  const platform = rule.platform ? platformName(rule.platform) : t("cc.min.any");
  const cell = "order-3 flex min-w-0 flex-col gap-1 @min-[720px]:order-none";
  const label = "text-[11px] font-bold text-muted @min-[720px]:hidden";
  const version = (value: string, field: "minChrome" | "minOther", aria: string) =>
    owner ? (
      <input
        type="text"
        inputMode="decimal"
        autoComplete="off"
        spellCheck={false}
        aria-label={aria}
        placeholder="?"
        value={value}
        onChange={(e) => onPatch({ [field]: versionOnly(e.target.value) })}
        className={versionCls}
      />
    ) : value ? (
      <span className="font-mono text-[13px]">{value}</span>
    ) : (
      <span title={t("cc.min.unknownVer")} className="font-mono text-[13px] text-faint">
        ?
      </span>
    );

  return (
    <li className={`grid grid-cols-3 items-end gap-x-3 gap-y-2.5 border-t border-line px-4 py-3 first:border-t-0 @min-[720px]:items-center ${cols}`}>
      <div className="order-1 col-span-2 flex min-w-0 flex-col gap-0.5 @min-[720px]:order-none @min-[720px]:col-span-1">
        <span className="flex min-w-0 items-center gap-2 text-[13px] font-bold">
          {(changed || mine) && <span role="img" aria-label={t(mine ? "cc.min.own" : "cc.min.changed")} title={t(mine ? "cc.min.own" : "cc.min.changed")} className="size-2 flex-none rounded-full bg-accent" />}
          <span className="truncate">{name}</span>
        </span>
        <span className="truncate text-xs text-muted">
          {platform}
          <span className="font-mono @min-[720px]:hidden"> · {coreName(rule.core)}</span>
        </span>
      </div>
      <span className="hidden font-mono text-xs text-muted @min-[720px]:block">{coreName(rule.core)}</span>
      <div className={cell}>
        <span className={label}>{t("cc.min.col.chrome")}</span>
        {version(rule.minChrome, "minChrome", t("cc.min.fieldChrome", { app: name }))}
      </div>
      <div className={cell}>
        <span className={label}>{t("cc.min.col.other")}</span>
        {version(rule.minOther, "minOther", t("cc.min.fieldOther", { app: name }))}
      </div>
      <div className={cell}>
        <span className={label}>{t("cc.min.col.broken")}</span>
        {owner ? (
          <Switch checked={rule.unsupported} onCheckedChange={(v) => onPatch({ unsupported: v })} aria-label={t("cc.min.fieldBroken", { app: name })} />
        ) : rule.unsupported ? (
          <span>
            <StatusPill kind="bad" sm label={t("cc.state.unsupported")} />
          </span>
        ) : (
          <span className="text-faint">—</span>
        )}
      </div>
      <div className="order-2 flex min-w-0 items-center justify-end @min-[720px]:order-none">
        {owner && changed && def && (
          <Button variant="ghost" size="sm" onClick={() => onRevert(def)}>
            {t("cc.min.revert")}
          </Button>
        )}
        {owner && mine && (
          <Button variant="ghostDanger" size="sm" onClick={onRemove}>
            {t("cc.min.remove")}
          </Button>
        )}
      </div>
    </li>
  );
}

/** "Add an app": the name it writes in its User-Agent, the platform and the core; the versions are filled in the table. */
function AddRow({ t, rules, onAdd }: { t: Tx; rules: readonly CompatRule[]; onAdd: (r: CompatRule) => void }) {
  const [name, setName] = useState("");
  const [platform, setPlatform] = useState("any");
  const [core, setCore] = useState("unknown");
  const id = useId();
  const app = appId(name);
  const platformId = platform === "any" ? "" : platform;
  const dup = app !== "" && rules.some((r) => ruleKey(r) === ruleKey({ app, platform: platformId }));
  const add = () => {
    if (!app || dup) return;
    onAdd({ app, platform: platformId, core, unsupported: false, minChrome: "", minOther: "", title: app });
    setName("");
  };
  const lbl = "text-[11px] font-bold text-muted";
  return (
    <form
      className="flex flex-col gap-2 border-t border-line pt-3.5"
      onSubmit={(e) => {
        e.preventDefault();
        add();
      }}
    >
      <span className="text-[13px] font-bold">{t("cc.min.add")}</span>
      <div className="grid grid-cols-2 items-end gap-2 @min-[720px]:grid-cols-[minmax(0,1fr)_150px_150px_auto]">
        <div className="col-span-2 flex min-w-0 flex-col gap-1 @min-[720px]:col-span-1">
          <label htmlFor={id} className={lbl}>
            {t("cc.min.addName")}
          </label>
          <input id={id} type="text" autoComplete="off" autoCapitalize="off" spellCheck={false} placeholder="happ" value={name} onChange={(e) => setName(appId(e.target.value))} aria-invalid={dup || undefined} className={`${inputCls} font-mono`} />
        </div>
        <div className="flex min-w-0 flex-col gap-1">
          <span className={lbl}>{t("cc.min.addPlatform")}</span>
          <Select
            aria-label={t("cc.min.addPlatform")}
            className={selectCls}
            value={platform}
            onValueChange={setPlatform}
            options={[{ value: "any", label: t("cc.min.any") }, ...platforms.map((p) => ({ value: p, label: platformName(p) }))]}
          />
        </div>
        <div className="flex min-w-0 flex-col gap-1">
          <span className={lbl}>{t("cc.min.addCore")}</span>
          <Select aria-label={t("cc.min.addCore")} className={selectCls} value={core} onValueChange={setCore} options={cores.map((c) => ({ value: c, label: coreName(c) }))} />
        </div>
        <Button type="submit" variant="secondary" disabled={!app || dup} className="col-span-2 h-10! @min-[720px]:col-span-1">
          {t("cc.min.addDo")}
        </Button>
      </div>
      {dup ? (
        <span role="alert" className="text-[11px] leading-snug text-danger-text">
          {t("cc.min.dup")}
        </span>
      ) : (
        <span className="text-[11px] leading-snug text-muted">{t("cc.min.addHint")}</span>
      )}
    </form>
  );
}
