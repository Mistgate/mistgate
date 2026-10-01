import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { AccentPicker } from "@/components/accent-picker";
import { DefaultMark, Logo } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Card, SectionLabel } from "@/components/ui/bits";
import { Segmented } from "@/components/ui/segmented";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { langs, setLang, useLang, useT } from "@/i18n";
import { instance as instanceApi } from "@/lib/api";
import type { Instance } from "@/gen/mistgate/admin/v1/instance_pb";
import { plain, type Plain } from "@/lib/plain";
import { sanitizeLogo } from "@/lib/brand";
import { errorText } from "@/lib/errors";
import { applyBrand, instanceQuery, loginInfoQuery } from "@/lib/instance";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { followInstanceAccent, isHex, useAccentChoice } from "@/lib/accent";

const maxLogoBytes = 64 * 1024;
const namePattern = /^[\p{L}\p{N} ._-]{1,24}$/u;
const tailPattern = /^[\p{L}\p{N} ._-]{0,24}$/u;

/**
 * Two cards that say whose choice is whose: "only on this device" (language, theme, accent; the first accent is "like the
 * panel", which forgets this device's own colour), then (owner only) "for everyone" (the brand of the installation).
 */
export function InterfacePage() {
  const t = useT();
  const lang = useLang();
  const theme = useTheme();
  const me = useQuery(meQuery);
  const { own } = useAccentChoice();
  const row = "flex min-h-14 items-center gap-3 border-t border-line";
  return (
    <>
      <Card lg className="px-4 pt-1">
        <div className="flex min-h-12 items-center">
          <SectionLabel as="h2" icon="phone" tone="mint">
            {t("settings.device")}
          </SectionLabel>
        </div>
        <div className={row}>
          <span className="flex-1 text-[13px] font-bold">{t("settings.language")}</span>
          <Segmented
            aria-label={t("settings.language")}
            value={lang}
            onValueChange={setLang}
            options={langs.map((l) => ({ value: l, label: l === "ru" ? "Русский" : "English" }))}
          />
        </div>
        <div className={row}>
          <span className="flex-1 text-[13px] font-bold">{t("settings.theme")}</span>
          <Segmented
            aria-label={t("settings.theme")}
            value={theme}
            onValueChange={setTheme}
            options={[
              { value: "dark", label: t("settings.theme.dark") },
              { value: "light", label: t("settings.theme.light") },
            ]}
          />
        </div>
        <div className="flex flex-col gap-3.5 border-t border-line py-4">
          <span className="text-[13px] font-bold">{t("settings.accent")}</span>
          <AccentPicker follow />
          {own && (
            <p className="flex flex-wrap items-center gap-x-1.5 text-xs text-muted">
              {t("settings.accent.own")}
              <span aria-hidden>·</span>
              <button type="button" onClick={followInstanceAccent} className="font-bold text-accent-text hover:underline">
                {t("settings.accent.reset")}
              </button>
            </p>
          )}
        </div>
        <p className="border-t border-line py-3 text-xs leading-normal text-muted">{t("settings.local")}</p>
      </Card>
      {me.data?.admin?.role === Role.OWNER && <BrandBlock />}
    </>
  );
}

type Logo = { kind: "keep" } | { kind: "remove" } | { kind: "new"; svg: string; preview: string };

function BrandBlock() {
  const q = useQuery(instanceQuery);
  // the form starts from the stored brand once it has arrived
  return q.data?.instance ? <BrandForm stored={q.data.instance} /> : null;
}

function BrandForm({ stored }: { stored: Plain<Instance> }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const file = useRef<HTMLInputElement>(null);
  const [head, setHead] = useState(stored.brandHead);
  const [tail, setTail] = useState(stored.brandTail);
  const [accent, setAccent] = useState(stored.accent);
  const [language, setLanguage] = useState(stored.language === "ru" ? "ru" : "en");
  const [logo, setLogo] = useState<Logo>({ kind: "keep" });
  const [logoError, setLogoError] = useState<string | null>(null);

  const headError = namePattern.test(head) ? undefined : t("brand.nameError");
  const tailError = tailPattern.test(tail) ? undefined : t("brand.nameError");
  const dirty =
    head !== stored.brandHead ||
    tail !== stored.brandTail ||
    accent.toLowerCase() !== stored.accent.toLowerCase() ||
    language !== stored.language ||
    logo.kind !== "keep";
  const valid = !headError && !tailError && isHex(accent);

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
      toast(t("common.saved"));
      setLogo({ kind: "keep" });
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
    if (f.size > maxLogoBytes) return setLogoError(t("brand.logoTooBig"));
    const svg = await f.text();
    const preview = sanitizeLogo(svg);
    if (!preview) return setLogoError(t("brand.logoInvalid"));
    setLogo({ kind: "new", svg, preview });
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (dirty && valid) save.mutate();
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-3.5">
      <Card lg className="flex flex-col gap-4 p-4">
        <div className="flex flex-col gap-1.5">
          <SectionLabel as="h2" icon="tag" tone="sage">
            {t("brand.title")}
          </SectionLabel>
          <p className="text-xs leading-normal text-pretty text-muted">{t("brand.hint")}</p>
        </div>

        <div className="flex items-center gap-3 rounded-field border border-line bg-canvas p-3.5" aria-label={t("brand.preview")}>
          <BrandMark logo={logo} />
          <span className="text-xl leading-none font-extrabold tracking-[-0.03em]">
            {head}
            <span className="text-accent-text" style={{ color: isHex(accent) ? accent : undefined }}>
              {tail}
            </span>
          </span>
        </div>

        <div className="grid gap-3.5 md:grid-cols-2">
          <TextField label={t("brand.head")} value={head} onChange={(e) => setHead(e.target.value)} maxLength={24} error={headError} autoComplete="off" />
          <TextField label={t("brand.tail")} value={tail} onChange={(e) => setTail(e.target.value)} maxLength={24} error={tailError} hint={t("brand.tailHint")} autoComplete="off" />
        </div>

        <div className="flex flex-col gap-2">
          <SectionLabel icon="upload" tone="mint">
            {t("brand.logo")}
          </SectionLabel>
          <div className="flex flex-wrap items-center gap-2">
            <input ref={file} type="file" accept=".svg,image/svg+xml" className="sr-only" tabIndex={-1} onChange={(e) => { void pick(e.target.files?.[0]); e.target.value = ""; }} />
            <Button variant="secondary" size="md" onClick={() => file.current?.click()}>
              <Icon name="upload" size={14} />
              {t("brand.upload")}
            </Button>
            {(logo.kind === "new" || (logo.kind === "keep" && stored.hasLogo)) && (
              <Button variant="ghost" size="md" onClick={() => { setLogo(stored.hasLogo && logo.kind === "new" ? { kind: "keep" } : { kind: "remove" }); setLogoError(null); }}>
                {logo.kind === "new" && stored.hasLogo ? t("brand.undoLogo") : t("brand.removeLogo")}
              </Button>
            )}
          </div>
          <p className={logoError ? "text-xs text-danger-text" : "text-xs text-muted"}>{logoError ?? t("brand.logoHint")}</p>
        </div>

        <div className="flex flex-col gap-3.5 border-t border-line pt-4">
          <span className="flex flex-col gap-0.5">
            <span className="text-[13px] font-bold">{t("brand.accent")}</span>
            <span className="text-xs text-muted">{t("brand.accentHint")}</span>
          </span>
          <AccentPicker value={accent} onChange={setAccent} />
        </div>

        <div className="flex items-center gap-3 border-t border-line pt-4">
          <span className="flex min-w-0 flex-1 flex-col gap-0.5">
            <span className="text-[13px] font-bold">{t("brand.language")}</span>
            <span className="text-xs text-muted">{t("brand.languageHint")}</span>
          </span>
          <Segmented
            aria-label={t("brand.language")}
            value={language}
            onValueChange={setLanguage}
            options={langs.map((l) => ({ value: l, label: l === "ru" ? "Русский" : "English" }))}
          />
        </div>

        <div className="flex justify-end gap-2 border-t border-line pt-4">
          <Button type="submit" variant="primary" size="md" disabled={!dirty || !valid || save.isPending}>
            {t("common.save")}
          </Button>
        </div>
      </Card>
    </form>
  );
}

/** The logo at 41px: the draft if one was picked, the built-in mark if the custom one is being removed, else the current one. */
function BrandMark({ logo }: { logo: Logo }) {
  if (logo.kind === "keep") return <Logo size={41} />;
  if (logo.kind === "remove") return <DefaultMark size={41} />;
  return <span aria-hidden className="block size-[41px] flex-none [&>svg]:block" dangerouslySetInnerHTML={{ __html: logo.preview }} />;
}
