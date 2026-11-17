import { useRef, useState } from "react";
import { halo } from "@/components/fx/halo";
import { Button } from "@/components/ui/button";
import { FlagText } from "@/components/ui/flag";
import { Segmented } from "@/components/ui/segmented";
import { Stepper } from "@/components/ui/stepper";
import { useToast } from "@/components/ui/toast";
import { HappPing } from "@/gen/mistgate/admin/v1/subscription_pb";
import { errorText } from "@/lib/errors";
import { cx } from "@/lib/cx";
import { useTx, type Tx } from "@/screens/users/t";
import { ScrollFade } from "@/components/ui/scroll-fade";
import { announceMax, cutAnnounce, defaultNameTemplate, serverNames, validSupportLink, type Settings } from "./model";
import { useSaveSettings } from "./queries";
import { Grid, path, Row, Section, Toggle } from "./parts";

const DEFAULT_HOURS = 12;
const chips = [
  { ph: "{flag}", hint: "subs.ph.flag" },
  { ph: "{country}", hint: "subs.ph.country" },
  { ph: "{node}", hint: "subs.ph.node" },
  { ph: "{profile}", hint: "subs.ph.profile" },
] as const;

/** A server name as the panel sends it ends with " · 64%" (and a number when two names are alike): the preview sets the percent apart. */
function split(name: string): { name: string; pct: string } {
  const m = /^(.*?) · (\d+%)(?: (\d+))?$/u.exec(name);
  return m ? { name: m[1]! + (m[3] ? ` ${m[3]}` : ""), pct: m[2]! } : { name, pct: "" };
}

type Sample = { node: string; countryCode: string; profile: string; loadPercent?: number };

// Made-up servers for the preview of an install that runs none yet: two in one country, to show the number.
const madeUp: Sample[] = [
  { countryCode: "DE", node: "de1", profile: "hy2" },
  { countryCode: "DE", node: "de1", profile: "hy2 · WARP" },
  { countryCode: "NL", node: "nl1", profile: "hy2" },
];

type Data = { settings: Settings; effectiveTitle: string; samples?: Sample[]; sampleGroup?: string; namesLanguage?: string };

/**
 * "Names & texts": title, announcement, support link, update interval, the server name template and how Happ pings the
 * servers, with a live preview of the app.
 */
export function TextsTab({ data }: { data: Data }) {
  const s = data.settings;
  // keyed by what is saved: after a save (or a change from elsewhere) the form starts again from the server
  const key = JSON.stringify([s.title, s.announcement, s.supportUrl, s.updateIntervalHours, s.serverNameTemplate, s.happPing, s.happPingOnOpen]);
  return <TextsForm key={key} data={data} />;
}

function TextsForm({ data }: { data: Data }) {
  const t = useTx();
  const toast = useToast();
  const save = useSaveSettings();
  const s = data.settings;
  const base = {
    title: s.title,
    announcement: s.announcement,
    supportUrl: s.supportUrl,
    hours: s.updateIntervalHours || DEFAULT_HOURS,
    template: s.serverNameTemplate,
    ping: s.happPing ?? HappPing.UNSPECIFIED,
    pingOnOpen: !!s.happPingOnOpen,
  };
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
      .mutateAsync((cur) => ({
        ...cur,
        title: f.title.trim(),
        announcement: f.announcement.trim(),
        supportUrl: f.supportUrl.trim(),
        updateIntervalHours: f.hours,
        serverNameTemplate: f.template.trim(),
        happPing: f.ping,
        happPingOnOpen: f.pingOnOpen,
      }))
      .then(() => {
        toast(t("subs.saved"));
        halo("save");
      })
      .catch((e: unknown) => toast.error(errorText(e, t)));
  }

  const hint = (k: "subs.texts.pingHint.default" | "subs.texts.pingHint.tcp") => t(k);
  const n = [...f.announcement.trim()].length;
  const over = n > announceMax;

  return (
    <>
      <Grid
        main={
          <>
            <Section tone="lav" icon={path.lines} title={t("subs.texts.sub")} shot="subs-texts">
              <div className="pp-rows">
                <Row label={t("subs.texts.title")} hint={t("subs.texts.titleHint")}>
                  <input id="subs-title" className="an-i" type="text" aria-label={t("subs.texts.title")} value={f.title} placeholder={data.effectiveTitle} onChange={(e) => set("title", e.target.value)} maxLength={100} autoComplete="off" />
                </Row>
                <Row label={t("subs.texts.announce")} hint={t("subs.texts.announceHint")}>
                  <textarea id="subs-announce" className="an-i sb-ta" rows={3} aria-label={t("subs.texts.announce")} aria-describedby="subs-announce-count" value={f.announcement} placeholder={t("subs.texts.announcePh")} onChange={(e) => set("announcement", e.target.value)} maxLength={1000} />
                  <p id="subs-announce-count" className={cx("pp-q1 sb-cnt", over && "bad")}>
                    {t(over ? "subs.texts.announceOver" : "subs.texts.announceCount", { n, max: announceMax })}
                  </p>
                </Row>
                <Row label={t("subs.texts.support")} hint={t("subs.texts.supportHint")}>
                  <input id="subs-support" className="an-i mono" type="text" aria-label={t("subs.texts.support")} aria-invalid={!supportOk || undefined} value={f.supportUrl} placeholder="https://t.me/…" onChange={(e) => set("supportUrl", e.target.value)} autoComplete="off" spellCheck={false} inputMode="url" />
                  {!supportOk && (
                    <p className="pp-q1 bad" role="alert">
                      {t("subs.texts.supportBad")}
                    </p>
                  )}
                </Row>
                <Row label={t("subs.texts.interval")} hint={t("subs.texts.intervalHint")}>
                  <div className="sb-step">
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
                </Row>
              </div>
            </Section>

            <Section tone="sand" icon={path.sliders} title={t("subs.texts.servers")}>
              <div className="pp-rows">
                <Row label={t("subs.texts.name")} hint={t("sx.texts.nameHint")}>
                  <input id="subs-template" ref={tplRef} className="an-i mono" type="text" aria-label={t("subs.texts.name")} value={f.template} placeholder={defaultNameTemplate} onChange={(e) => set("template", e.target.value)} maxLength={100} autoComplete="off" spellCheck={false} />
                  <div className="sb-parts" role="group" aria-label={t("subs.texts.nameChips")}>
                    <span>{t("subs.texts.pieces")}</span>
                    {chips.map((c) => (
                      <button key={c.ph} type="button" className="mono" title={t(c.hint)} onClick={() => insert(c.ph)}>
                        {c.ph}
                      </button>
                    ))}
                  </div>
                </Row>
              </div>
            </Section>

            <Section tone="sky" icon={path.pulse} title={t("subs.texts.ping")}>
              <div className="pp-rows">
                <Row label={t("subs.texts.pingHow")} hint={hint(f.ping === HappPing.TCP ? "subs.texts.pingHint.tcp" : "subs.texts.pingHint.default")}>
                  <Segmented
                    className="pp-seg"
                    aria-label={t("subs.texts.pingHow")}
                    value={String(f.ping)}
                    onValueChange={(v) => set("ping", Number(v) as HappPing)}
                    options={[
                      { value: String(HappPing.UNSPECIFIED), label: t("subs.texts.ping.default") },
                      { value: String(HappPing.TCP), label: t("subs.texts.ping.tcp") },
                    ]}
                  />
                </Row>
              </div>
              <div className="sb-tgs">
                <Toggle title={t("subs.texts.pingOpen")} hint={f.ping === HappPing.UNSPECIFIED ? t("subs.texts.pingOpenOff") : t("subs.texts.pingOpenHint")} checked={f.pingOnOpen} disabled={f.ping === HappPing.UNSPECIFIED} onChange={(on) => set("pingOnOpen", on)} />
              </div>
            </Section>

            <div className={cx("sb-save", (dirty || save.isPending) && "on")} role="status">
              <span>{t("subs.dirty")}</span>
              <Button variant="ghost" onClick={() => setF(base)} disabled={save.isPending}>
                {t("subs.discard")}
              </Button>
              <Button variant="primary" disabled={!supportOk || save.isPending || !dirty} onClick={onSave}>
                {t("subs.save")}
              </Button>
            </div>
          </>
        }
        side={
          <SubscriptionPreview
            t={t}
            title={f.title.trim() || data.effectiveTitle}
            announcement={f.announcement.trim()}
            hours={f.hours}
            support={supportOk ? f.supportUrl.trim() : ""}
            template={f.template}
            samples={data.samples?.length ? data.samples : madeUp}
            group={data.samples?.length ? (data.sampleGroup ?? "") : ""}
            lang={data.namesLanguage || t.locale}
          />
        }
      />
    </>
  );
}

/**
 * The mock of how the subscription looks in Happ, drawn from the form as it stands: title, the announcement as Happ cuts it, update
 * interval, expiry and traffic, and the servers of the busiest group named by the template (made-up ones on an install that runs none
 * yet). Country codes keep the load percentage visible in Happ's narrow server rows.
 */
function SubscriptionPreview({ t, title, announcement, hours, support, template, samples, group, lang }: { t: Tx; title: string; announcement: string; hours: number; support: string; template: string; samples: Sample[]; group: string; lang: string }) {
  const regions = new Intl.DisplayNames([lang], { type: "region" });
  const names = serverNames(template, samples, (code) => (/^[A-Z]{2}$/i.test(code) ? code.toUpperCase() : regions.of(code) ?? code));
  const [now] = useState(() => Date.now());
  const until = new Intl.DateTimeFormat(t.locale, { day: "numeric", month: "long" }).format(new Date(now + 76 * 86_400_000));
  return (
    <Section tone="mint" icon={path.eye} title={t("subs.texts.preview")} className="sb-prev" shot="subs-preview">
      <div className="sb-ph">
        <div className="sb-pht">
          <b>{title}</b>
          <span>{t("subs.texts.pv.refresh", { n: hours })}</span>
        </div>
        {announcement && <p className="sb-pha">{cutAnnounce(announcement)}</p>}
        <span className="sb-phl">{t("subs.texts.pv.servers")}</span>
        <ScrollFade label={t("subs.texts.pv.servers")} className="max-h-[300px]">
          <div aria-live="polite">
            {names.map((name, i) => (
              <div className="sb-phr" key={i}>
                <span>
                  <FlagText text={split(name).name} size={13} />
                </span>
                {split(name).pct && <em className="mono">{split(name).pct}</em>}
              </div>
            ))}
          </div>
        </ScrollFade>
        <div className="sb-phf">
          <span className="mono">{t("subs.texts.pv.traffic")}</span>
          <span>{t("subs.texts.pv.until", { date: until })}</span>
        </div>
        {support && <div className="sb-phb">{t("subs.texts.pv.support")}</div>}
      </div>
      <p className="pp-q1 sb-pad">{group ? t("subs.texts.previewHintGroup", { group }) : t("subs.texts.previewHint")}</p>
    </Section>
  );
}
