import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type CSSProperties, type FormEvent } from "react";
import { DefaultMark } from "@/components/brand";
import { originOf, playWave } from "@/components/fx/wave";
import { Button } from "@/components/ui/button";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { Instance } from "@/gen/mistgate/admin/v1/instance_pb";
import { isLang, langNames, langs, setLang, useLang, useT, type Lang } from "@/i18n";
import { accentPresets, followInstanceAccent, isHex, setAccent, useAccent, useAccentChoice } from "@/lib/accent";
import { instance as instanceApi } from "@/lib/api";
import { sanitizeLogo, useBrand } from "@/lib/brand";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { setEffectsEnabled, useEffectsEnabled } from "@/lib/effects";
import { errorText } from "@/lib/errors";
import { applyBrand, instanceQuery, loginInfoQuery } from "@/lib/instance";
import { plain, type Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { setTheme, themeNow, useTheme } from "@/lib/theme";
import { ICONS, Mono, Pb, Row, Rows, Sec, slots, Spin, SxGate } from "./sx-bits";
import "./settings-if.css";

const maxLogoBytes = 64 * 1024;
const namePattern = /^[\p{L}\p{N} ._-]{1,24}$/u;
const tailPattern = /^[\p{L}\p{N} ._-]{0,24}$/u;

type Logo = { kind: "keep" } | { kind: "remove" } | { kind: "new"; svg: string; preview: string };

/** What a scene starts the page with (the board's states that come from a click: a logo that is too big, one just uploaded, an edit not saved yet). */
export type InterfaceDemo = { logo?: "big" | "bad" | "busy" | { svg: string; preview: string }; tail?: string; saving?: boolean; saved?: boolean };

/**
 * Settings -> Interface (the board's sxIs_if): a preview of the menu, the sign-in and the friends page in the colours that are chosen,
 * then two cards side by side: what is only on this device (language, theme, accent, the reveal animation) and, drawn for every
 * role but changed by the owner only, what is for everyone (the name, the logo, the accent and the language of a new browser).
 */
export function InterfacePage({ demo }: { demo?: InterfaceDemo } = {}) {
  const q = useQuery(instanceQuery);
  const stored = q.data?.instance;
  return (
    <SxGate pending={q.isPending} error={q.isError && !stored} onRetry={() => void q.refetch()}>
      {stored && <InterfaceBody stored={stored} demo={demo} />}
    </SxGate>
  );
}

function InterfaceBody({ stored, demo }: { stored: Plain<Instance>; demo?: InterfaceDemo }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const file = useRef<HTMLInputElement>(null);
  const [head, setHead] = useState(stored.brandHead);
  const [tail, setTail] = useState(demo?.tail ?? stored.brandTail);
  const [accent, setGlobalAccent] = useState(stored.accent);
  const [language, setLanguage] = useState(isLang(stored.language) ? stored.language : "en");
  const [logo, setLogo] = useState<Logo>(typeof demo?.logo === "object" ? { kind: "new", ...demo.logo } : { kind: "keep" });
  const [reading, setReading] = useState(demo?.logo === "busy");
  const [logoError, setLogoError] = useState<"big" | "bad" | null>(demo?.logo === "big" || demo?.logo === "bad" ? demo.logo : null);
  const [saved, setSaved] = useState(!!demo?.saved);

  const headBad = !namePattern.test(head);
  const nameError = head.trim() === "" ? t("sx.if.name.empty") : headBad || !tailPattern.test(tail) ? t("brand.nameError") : "";
  const dirty =
    head !== stored.brandHead ||
    tail !== stored.brandTail ||
    accent.toLowerCase() !== stored.accent.toLowerCase() ||
    language !== stored.language ||
    logo.kind !== "keep";
  const valid = !nameError && isHex(accent);

  const save = useMutation({
    mutationFn: () =>
      instanceApi.updateInstance({
        brandHead: head !== stored.brandHead ? head : undefined,
        brandTail: tail !== stored.brandTail ? tail : undefined,
        accent: accent.toLowerCase() !== stored.accent.toLowerCase() ? accent.toLowerCase() : undefined,
        language: language !== stored.language ? language : undefined,
        logoSvg: logo.kind === "new" ? logo.svg : logo.kind === "remove" ? "" : undefined,
      }),
    onSuccess: (r) => {
      const i = r.instance!;
      toast(t("sx.if.toast"));
      setLogo({ kind: "keep" });
      setSaved(true);
      setTimeout(() => setSaved(false), 2000);
      void applyBrand(i, i.logoVersion);
      qc.setQueryData(instanceQuery.queryKey, plain(r));
      qc.setQueryData(loginInfoQuery.queryKey, (old) =>
        old ? { ...old, brandHead: i.brandHead, brandTail: i.brandTail, accent: i.accent, language: i.language, hasLogo: i.hasLogo } : old,
      );
    },
    onError: (e) => toast.error(errorText(e, t)),
  });

  async function pick(f: File | undefined) {
    if (!f) return;
    setLogoError(null);
    if (f.size > maxLogoBytes) return setLogoError("big");
    setReading(true);
    try {
      const svg = await f.text();
      const preview = sanitizeLogo(svg);
      if (!preview) return setLogoError("bad");
      setLogo({ kind: "new", svg, preview });
    } finally {
      setReading(false);
    }
  }

  const pending = save.isPending || !!demo?.saving;
  function submit(e: FormEvent) {
    e.preventDefault();
    if (owner && dirty && valid && !pending) save.mutate();
  }

  const shown = isHex(accent) ? accent : stored.accent;
  return (
    <>
      <Preview head={head} tail={tail} accent={shown} logo={logo} />
      <div className="sx-one sx-if">
        <DeviceCard />
        <form onSubmit={submit} className="contents">
          <Sec tone="mint" d={ICONS.people} title={t("sx.if.all")}>
            <Rows className={cx("sx-all", !owner && "ro")}>
              <fieldset className="contents" disabled={!owner}>
                <Row label={t("sx.if.name")} hint={t("sx.if.name.hint")}>
                  <div className="sx-nm">
                    <label className="an-f">
                      <input className="an-i" type="text" value={head} maxLength={24} autoComplete="off" spellCheck={false} aria-label={t("sx.if.name.a1")} aria-invalid={!!nameError && (head.trim() === "" || headBad)} onChange={(e) => setHead(e.target.value)} />
                    </label>
                    <input className="an-i sx-n2" type="text" value={tail} maxLength={24} autoComplete="off" spellCheck={false} aria-label={t("sx.if.name.a2")} aria-invalid={!tailPattern.test(tail)} style={{ "--g": shown } as CSSProperties} onChange={(e) => setTail(e.target.value)} />
                  </div>
                  <small className="sx-err" role={nameError ? "alert" : undefined}>
                    {nameError}
                  </small>
                </Row>
                <Row label={t("sx.if.logo")} hint={slots(t("sx.if.logo.hint"), { l: <Mono>#cba5fa</Mono>, d: <Mono>#693fc2</Mono> })}>
                  <div className="sx-logo">
                    <input ref={file} type="file" accept=".svg,image/svg+xml" className="sr-only" tabIndex={-1} onChange={(e) => { void pick(e.target.files?.[0]); e.target.value = ""; }} />
                    <Button size="md" disabled={reading || !owner} onClick={() => file.current?.click()}>
                      {reading && <Spin />}
                      {reading ? t("sx.if.logo.busy") : t("sx.if.logo.up")}
                    </Button>
                    {logo.kind === "new" && <em>{t("sx.if.logo.new")}</em>}
                    {(logo.kind === "new" || (logo.kind === "keep" && stored.hasLogo)) && (
                      <button type="button" className="an-lk" onClick={() => { setLogo(stored.hasLogo && logo.kind === "new" ? { kind: "keep" } : { kind: "remove" }); setLogoError(null); }}>
                        {logo.kind === "new" && stored.hasLogo ? t("brand.undoLogo") : t("brand.removeLogo")}
                      </button>
                    )}
                  </div>
                  {logoError === "big" && <Pb tone="bad" title={t("sx.if.logo.big")}>{t("sx.if.logo.big.body")}</Pb>}
                  {logoError === "bad" && <Pb tone="bad" title={t("brand.logoInvalid")} />}
                </Row>
                <Row label={t("settings.accent")} hint={t("sx.if.gacc.hint")}>
                  <Swatches value={accent} onPick={setGlobalAccent} base={shown} />
                </Row>
                <Row label={t("sx.if.glang")} hint={t("sx.if.glang.hint")}>
                  <LangSelect value={language} onChange={(l) => setLanguage(l)} label={t("sx.if.glang")} />
                </Row>
              </fieldset>
            </Rows>
            {owner ? (
              <div className="sx-save">
                {dirty && !pending && <em>{t("sx.if.dirty")}</em>}
                <Button type="submit" variant="primary" size="lg" className={cx(pending && "sx-busy", saved && !dirty && "sx-done")} aria-disabled={!dirty || !valid || pending || undefined}>
                  {pending && <Spin />}
                  {pending ? t("sx.if.saving") : saved && !dirty ? t("sx.if.saved") : t("common.save")}
                </Button>
              </div>
            ) : (
              <p className="sb-vfoot">{t("sx.if.ro")}</p>
            )}
          </Sec>
        </form>
      </div>
    </>
  );
}

/** "How it looks": the menu of this device, the sign-in and the friends page, drawn with what the cards below hold (before it is saved). */
function Preview({ head, tail, accent, logo }: { head: string; tail: string; accent: string; logo: Logo }) {
  const t = useT();
  const brand = useBrand();
  const word = (
    <b>
      {head}
      <i>{tail}</i>
    </b>
  );
  const mark = (
    <span aria-hidden className="sx-mk">
      {logo.kind === "new" ? (
        <span className="block size-6 flex-none [&>svg]:block" dangerouslySetInnerHTML={{ __html: logo.preview }} />
      ) : logo.kind === "keep" && brand.logo !== null ? (
        <span className="block size-6 flex-none [&>svg]:block" dangerouslySetInnerHTML={{ __html: brand.logo }} />
      ) : (
        <DefaultMark size={24} />
      )}
    </span>
  );
  const g = { "--g": accent } as CSSProperties;
  return (
    <section className="card sx-pv" aria-label={t("sx.if.pv")}>
      <div className="sx-pvc sx-pv-menu">
        <span className="sx-pvt">{t("sx.if.pv.menu")}</span>
        <div className="sx-mock">
          <div className="sx-mb">
            {mark}
            {word}
          </div>
          <div className="sx-mn on">
            <i className="sx-b" style={{ width: "56%" }} />
          </div>
          <div className="sx-mn">
            <i className="sx-b" style={{ width: "42%" }} />
          </div>
          <div className="sx-mn">
            <i className="sx-b" style={{ width: "64%" }} />
          </div>
        </div>
      </div>
      <div className="sx-pvc sx-pv-g" style={g}>
        <span className="sx-pvt">{t("sx.if.pv.login")}</span>
        <div className="sx-mock sx-mlog">
          <span className={cx("sx-ml", logo.kind === "new" && "new")}>{mark}</span>
          {word}
          <i className="sx-b f" style={{ width: "100%" }} />
          <i className="sx-b f" style={{ width: "100%" }} />
          <i className="sx-b btn-a" />
        </div>
      </div>
      <div className="sx-pvc sx-pv-g" style={g}>
        <span className="sx-pvt">{t("sx.if.pv.friends")}</span>
        <div className="sx-mock sx-mfr">
          <div className="sx-mb">
            {mark}
            {word}
          </div>
          <i className="sx-b" style={{ width: "72%" }} />
          <i className="sx-b" style={{ width: "48%" }} />
          <div className="sx-mrow">
            <i className="sx-b btn-a" />
            <i className="sx-b btn-q" />
          </div>
        </div>
      </div>
    </section>
  );
}

function LangSelect({ value, onChange, label }: { value: Lang; onChange: (l: Lang) => void; label: string }) {
  return (
    <div className="sx-ddw">
      <Select aria-label={label} value={value} onValueChange={(v) => isLang(v) && onChange(v)} options={langs.map((l) => ({ value: l, label: langNames[l] }))} className="h-9!" />
    </div>
  );
}

/** The card of what is only on this device: nothing here is sent to the panel. */
function DeviceCard() {
  const t = useT();
  const lang = useLang();
  const theme = useTheme();
  const accent = useAccent();
  const choice = useAccentChoice();
  const effects = useEffectsEnabled();
  const edition = useEdition();
  const fxSwitch = useRef<HTMLElement>(null);
  return (
    <Sec tone="lav" d={ICONS.device} title={t("settings.device")} foot={t("sx.if.local")}>
      <Rows>
        <Row label={t("settings.language")}>
          <LangSelect value={lang} onChange={setLang} label={t("settings.language")} />
        </Row>
        <Row label={t("settings.theme")}>
          <Segmented
            aria-label={t("settings.theme")}
            value={theme}
            onValueChange={setTheme}
            className="w-full [&>*]:flex-1"
            options={[
              { value: "dark", label: t("settings.theme.dark") },
              { value: "light", label: t("settings.theme.light") },
            ]}
          />
        </Row>
        <Row label={t("settings.accent")}>
          <Swatches value={accent} onPick={setAccent} base={choice.instance} follow={{ on: !choice.own, color: choice.instance, onPick: followInstanceAccent }} />
        </Row>
        {!edition.edge && (
          <div className="pp-row sx-anr">
            <div className="pp-lb">
              <b>{t("settings.fx")}</b>
              <span>{t("sx.if.fx.hint")}</span>
            </div>
            <div className="pp-ct sx-end">
              <Switch
                ref={fxSwitch}
                className="fz-sw"
                checked={effects}
                onCheckedChange={(on) => {
                  setEffectsEnabled(on);
                  // as the board's switch does: turned on, it sends the wave from itself
                  if (on) playWave(originOf(fxSwitch.current), { light: themeNow() === "light" });
                }}
                aria-label={t("settings.fx")}
              />
            </div>
          </div>
        )}
      </Rows>
    </Sec>
  );
}

const presetName = { mint: "mint", lavender: "lavender", sky: "sky", sand: "sand", rose: "rose", sage: "sage" } as const;

/**
 * The accent as the board draws it: pills of 32px with a dot of the colour, "like the panel" first where the accent may follow the
 * panel's, the six colours, and "Custom" with a field for six hex digits. The chosen pill takes a line and a wash of its colour.
 * `base` is what "like the panel" and an empty custom pill stand for.
 */
function Swatches({ value, onPick, base, follow }: { value: string; onPick: (hex: string) => void; base: string; follow?: { on: boolean; color: string; onPick: () => void } }) {
  const t = useT();
  const [ownPick, setOwnPick] = useState(false);
  const [draft, setDraft] = useState<string | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const following = !!follow?.on && !ownPick;
  const preset = following ? undefined : accentPresets.find((p) => p.color === value.toLowerCase());
  const ownOn = !following && (ownPick || !preset);
  const typed = draft ?? (ownOn ? value.slice(1) : "");
  const bad = draft !== null && draft !== "" && !isHex("#" + draft);
  return (
    <div className="sx-sws" role="radiogroup" aria-label={t("settings.accent")}>
      {follow && (
        <button type="button" className="sx-swt auto" role="radio" aria-checked={following} style={{ "--c": follow.color } as CSSProperties} onClick={() => { setOwnPick(false); setDraft(null); follow.onPick(); }}>
          <i aria-hidden />
          {t("settings.accent.follow")}
        </button>
      )}
      {accentPresets.map((p) => (
        <button key={p.id} type="button" className="sx-swt" role="radio" aria-checked={preset?.id === p.id && !ownPick} style={{ "--c": p.color } as CSSProperties} onClick={() => { setOwnPick(false); setDraft(null); onPick(p.color); }}>
          <i aria-hidden />
          {t(`settings.accent.${presetName[p.id]}`)}
        </button>
      ))}
      {/* "#b8acf2" is one left-to-right token, in Persian too */}
      <label className={cx("sx-swt sx-own", bad && "err")} dir="ltr" aria-checked={ownOn} style={{ "--c": isHex("#" + typed) ? "#" + typed : base } as CSSProperties} onClick={() => { if (!ownOn) { setOwnPick(true); onPick(isHex(value) ? value : base); } input.current?.focus(); }}>
        <i aria-hidden />
        <span dir="auto">{t("settings.accent.custom")}</span>
        <span className="mono">#</span>
        <input
          ref={input}
          className="mono"
          type="text"
          maxLength={6}
          placeholder="7dd3a0"
          autoComplete="off"
          spellCheck={false}
          aria-label={t("sx.if.acc.hex")}
          aria-invalid={bad}
          value={typed}
          onChange={(e) => {
            const v = e.target.value.replace(/[^0-9a-f]/gi, "").slice(0, 6);
            setDraft(v);
            setOwnPick(true);
            if (isHex("#" + v)) onPick("#" + v.toLowerCase());
          }}
          onBlur={() => setDraft(null)}
        />
      </label>
    </div>
  );
}
