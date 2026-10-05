import { useRef, useState } from "react";
import { Card, SectionLabel } from "@/components/ui/bits";
import { FlagText } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { Stepper } from "@/components/ui/stepper";
import { useToast } from "@/components/ui/toast";
import { errorText } from "@/lib/errors";
import { cx } from "@/lib/cx";
import { useTx, type Tx } from "@/screens/users/t";
import { announceMax, cutAnnounce, defaultNameTemplate, serverNames, validSupportLink, type Settings } from "./model";
import { useSaveSettings } from "./queries";
import { FieldBlock, inputCls, Lead, SaveBar } from "./ui";

const DEFAULT_HOURS = 12;
const chips = [
  { ph: "{flag}", hint: "subs.ph.flag" },
  { ph: "{country}", hint: "subs.ph.country" },
  { ph: "{node}", hint: "subs.ph.node" },
  { ph: "{profile}", hint: "subs.ph.profile" },
] as const;

type Sample = { node: string; countryCode: string; profile: string; loadPercent?: number };

// Made-up servers for the preview of an install that runs none yet: two in one country, to show the number.
const madeUp: Sample[] = [
  { countryCode: "DE", node: "de1", profile: "hy2" },
  { countryCode: "DE", node: "de1", profile: "hy2 · WARP" },
  { countryCode: "NL", node: "nl1", profile: "hy2" },
];

type Data = { settings: Settings; effectiveTitle: string; samples?: Sample[]; sampleGroup?: string; namesLanguage?: string };

/** "Names & texts": title, announcement, support link, update interval and the server name template, with a live preview of the app. */
export function TextsTab({ data }: { data: Data }) {
  const s = data.settings;
  // keyed by what is saved: after a save (or a change from elsewhere) the form starts again from the server
  const key = JSON.stringify([s.title, s.announcement, s.supportUrl, s.updateIntervalHours, s.serverNameTemplate]);
  return <TextsForm key={key} data={data} />;
}

function TextsForm({ data }: { data: Data }) {
  const t = useTx();
  const toast = useToast();
  const save = useSaveSettings();
  const s = data.settings;
  const base = { title: s.title, announcement: s.announcement, supportUrl: s.supportUrl, hours: s.updateIntervalHours || DEFAULT_HOURS, template: s.serverNameTemplate };
  const [f, setF] = useState(base);
  const set = <K extends keyof typeof base>(k: K, v: (typeof base)[K]) => setF((x) => ({ ...x, [k]: v }));
  const tplRef = useRef<HTMLInputElement>(null);

  const dirty = JSON.stringify(f) !== JSON.stringify(base);
  const supportOk = validSupportLink(f.supportUrl);

  function insert(ph: string) {
    const el = tplRef.current;
    const at = el?.selectionStart ?? f.template.length;
    const to = el?.selectionEnd ?? at;
    set("template", f.template.slice(0, at) + ph + f.template.slice(to));
    requestAnimationFrame(() => {
      el?.focus();
      el?.setSelectionRange(at + ph.length, at + ph.length);
    });
  }

  function onSave() {
    save
      .mutateAsync((cur) => ({ ...cur, title: f.title.trim(), announcement: f.announcement.trim(), supportUrl: f.supportUrl.trim(), updateIntervalHours: f.hours, serverNameTemplate: f.template.trim() }))
      .then(() => toast(t("subs.saved")))
      .catch((e: unknown) => toast.error(errorText(e, t)));
  }

  return (
    <div className="flex flex-col gap-4">
      <Lead>{t("subs.texts.lead")}</Lead>
      <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,680px)_minmax(320px,400px)]">
        <div className="flex min-w-0 flex-col gap-4">
          <Card lg className="flex flex-col px-4 py-4">
            <SectionLabel className="pb-3" icon="text" tone="sand">
              {t("subs.texts.sub")}
            </SectionLabel>
            <FieldBlock label={t("subs.texts.title")} htmlFor="subs-title" hint={t("subs.texts.titleHint")} className="border-t-0! pt-0!">
              <input id="subs-title" className={inputCls} value={f.title} placeholder={data.effectiveTitle} onChange={(e) => set("title", e.target.value)} maxLength={100} autoComplete="off" />
            </FieldBlock>
            <FieldBlock label={t("subs.texts.announce")} htmlFor="subs-announce" hint={<AnnounceHint t={t} text={f.announcement} />}>
              <textarea
                id="subs-announce"
                rows={2}
                value={f.announcement}
                placeholder={t("subs.texts.announcePh")}
                onChange={(e) => set("announcement", e.target.value)}
                maxLength={1000}
                aria-describedby="subs-announce-count"
                className="min-h-[64px] w-full resize-y rounded-field border border-line bg-canvas px-3 py-2.5 text-[13px] leading-normal text-fg outline-none transition-colors duration-200 placeholder:text-faint focus:border-accent"
              />
            </FieldBlock>
            <FieldBlock label={t("subs.texts.support")} htmlFor="subs-support" hint={t("subs.texts.supportHint")} error={supportOk ? undefined : t("subs.texts.supportBad")}>
              <input id="subs-support" className={`${inputCls} font-mono`} aria-invalid={!supportOk || undefined} value={f.supportUrl} placeholder="https://t.me/…" onChange={(e) => set("supportUrl", e.target.value)} autoComplete="off" spellCheck={false} inputMode="url" />
            </FieldBlock>
            <div className="flex min-h-[60px] items-center gap-3 border-t border-line pt-3.5">
              <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="text-[13px] font-bold">{t("subs.texts.interval")}</span>
                <span className="text-[11px] leading-snug text-muted">{t("subs.texts.intervalHint")}</span>
              </div>
              <div className="w-[130px]">
                <Stepper
                  decrementLabel={t("users.less", { what: t("subs.texts.interval") })}
                  incrementLabel={t("users.more", { what: t("subs.texts.interval") })}
                  onDecrement={() => set("hours", Math.max(1, f.hours - 1))}
                  onIncrement={() => set("hours", Math.min(72, f.hours + 1))}
                  decrementDisabled={f.hours <= 1}
                  incrementDisabled={f.hours >= 72}
                >
                  {t("subs.texts.hours", { n: f.hours })}
                </Stepper>
              </div>
            </div>
          </Card>

          <Card lg className="flex flex-col px-4 py-4">
            <SectionLabel className="pb-3" icon="server" tone="sky">
              {t("subs.texts.servers")}
            </SectionLabel>
            <FieldBlock label={t("subs.texts.name")} htmlFor="subs-template" hint={<FlagText text={t("subs.texts.nameHint")} size={10} />} className="border-t-0! pt-0!">
              <input ref={tplRef} id="subs-template" className={`${inputCls} font-mono`} value={f.template} placeholder={defaultNameTemplate} onChange={(e) => set("template", e.target.value)} maxLength={100} autoComplete="off" spellCheck={false} />
              <div role="group" aria-label={t("subs.texts.nameChips")} className="flex flex-wrap items-center gap-1.5">
                <span className="text-[11px] text-muted">{t("subs.texts.pieces")}:</span>
                {chips.map((c) => (
                  <button
                    key={c.ph}
                    type="button"
                    title={t(c.hint)}
                    onClick={() => insert(c.ph)}
                    className="h-[26px] rounded-[13px] bg-surface-2 px-2.5 font-mono text-[11px] text-muted transition-colors hover:bg-accent-soft hover:text-fg"
                  >
                    {c.ph}
                  </button>
                ))}
              </div>
            </FieldBlock>
          </Card>
        </div>

        <SubscriptionPreview
          t={t}
          title={f.title.trim() || data.effectiveTitle}
          announcement={f.announcement.trim()}
          hours={f.hours}
          support={supportOk ? f.supportUrl.trim() : ""}
          template={f.template}
          samples={data.samples?.length ? data.samples : madeUp}
          group={data.samples?.length ? (data.sampleGroup ?? "") : ""}
          lang={data.namesLanguage || t.lang}
        />
      </div>

      {dirty && <SaveBar busy={save.isPending} canSave={supportOk} onSave={onSave} onDiscard={() => setF(base)} />}
    </div>
  );
}

/** The announcement's hint with how much of it Happ shows: "120 / 200", in the warning tone past the limit. */
function AnnounceHint({ t, text }: { t: Tx; text: string }) {
  const n = [...text.trim()].length;
  const over = n > announceMax;
  return (
    <span className="flex flex-col gap-0.5">
      <span>{t("subs.texts.announceHint")}</span>
      <span id="subs-announce-count" className={cx("tabular-nums", over && "font-bold text-warn-text")}>
        {t(over ? "subs.texts.announceOver" : "subs.texts.announceCount", { n, max: announceMax })}
      </span>
    </span>
  );
}

/**
 * A mock of how the subscription looks in Happ, drawn from the form as it stands: title, the announcement as Happ cuts
 * it, update interval, expiry and traffic, and the servers of the busiest group named by the template (made-up ones on an
 * install that runs none yet). Country codes keep the load percentage visible in Happ's narrow server rows.
 */
function SubscriptionPreview({ t, title, announcement, hours, support, template, samples, group, lang }: { t: Tx; title: string; announcement: string; hours: number; support: string; template: string; samples: Sample[]; group: string; lang: string }) {
  const regions = new Intl.DisplayNames([lang], { type: "region" });
  const names = serverNames(template, samples, (code) => (/^[A-Z]{2}$/i.test(code) ? code.toUpperCase() : regions.of(code) ?? code));
  const [now] = useState(() => Date.now());
  const until = new Intl.DateTimeFormat(t.lang, { day: "numeric", month: "long" }).format(new Date(now + 76 * 86_400_000));
  return (
    <Card lg className="flex min-w-0 flex-col gap-3 p-4 lg:sticky lg:top-[72px]">
      <SectionLabel icon="phone" tone="mint">
        {t("subs.texts.preview")}
      </SectionLabel>
      <div className="flex flex-col gap-3 rounded-[22px] border border-line bg-canvas p-3.5">
        <div className="flex flex-col gap-0.5">
          <b className="truncate text-base tracking-[-0.02em]">{title}</b>
          <span className="flex items-center gap-1.5 text-[11px] text-muted">
            <Icon name="refresh" size={11} />
            {t("subs.texts.pv.refresh", { n: hours })}
          </span>
        </div>
        {announcement && (
          <div className="flex items-start gap-2 rounded-xl border border-warn-line bg-warn-soft px-3 py-2 text-xs leading-snug">
            <Icon name="bell" size={14} className="mt-px text-warn-text" />
            <span className="min-w-0 break-words whitespace-pre-line">{cutAnnounce(announcement)}</span>
          </div>
        )}
        <div className="flex flex-col gap-1.5">
          <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("subs.texts.pv.servers")}</span>
          <div aria-live="polite" className="flex flex-col gap-1.5">
            {names.map((name, i) => (
              <div key={i} className="flex h-10 items-center gap-2.5 rounded-xl border border-line bg-surface px-3">
                <span className="min-w-0 flex-1 truncate text-[13px] font-semibold">
                  <FlagText text={name} size={14} />
                </span>
                <span aria-hidden className="size-1.5 flex-none rounded-full bg-ok" />
              </div>
            ))}
          </div>
        </div>
        <div className="flex flex-col gap-1.5 border-t border-line pt-3">
          <div className="flex items-baseline gap-2 text-xs">
            <span className="flex-1 font-mono font-bold">{t("subs.texts.pv.traffic")}</span>
            <span className="text-muted">{t("subs.texts.pv.until", { date: until })}</span>
          </div>
          <div className="h-[3px] overflow-hidden rounded-sm bg-surface-2">
            <div className="h-full w-[12%] rounded-sm bg-accent" />
          </div>
        </div>
        {support && (
          <span className="inline-flex h-8 items-center gap-1.5 self-start rounded-ctl border border-line bg-surface px-3 text-xs font-bold">
            <Icon name="link" size={13} />
            {t("subs.texts.pv.support")}
          </span>
        )}
      </div>
      <p className="text-[11px] leading-snug text-muted">{group ? t("subs.texts.previewHintGroup", { group }) : t("subs.texts.previewHint")}</p>
    </Card>
  );
}
