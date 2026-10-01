import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { Platform } from "@/gen/mistgate/admin/v1/subscription_pb";
import { Card, Chip, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useCan } from "@/lib/health";
import { useTx } from "@/screens/users/t";
import { SwitchRow } from "@/screens/users/ui";
import { Pending, QueryError } from "@/components/ui/query-error";
import { kindKey, kinds, platformKey, platforms, previewUrl, type PlatformApp, type Settings } from "./model";
import { pickerUsersQuery, useSaveSettings } from "./queries";
import { inputCls, SaveBar } from "./ui";

type Options = { showAnnouncement: boolean; showSupport: boolean; showQr: boolean; allowDeviceSelfService: boolean; requirePagePassword: boolean };
type Draft = { apps: PlatformApp[]; options: Options };
const draftOf = (s: Settings): Draft => ({
  apps: s.apps,
  options: { showAnnouncement: s.userPage?.showAnnouncement ?? true, showSupport: s.userPage?.showSupport ?? true, showQr: s.userPage?.showQr ?? true, allowDeviceSelfService: s.userPage?.allowDeviceSelfService ?? true, requirePagePassword: s.userPage?.requirePagePassword ?? true },
});
const descMax = 80;
const validDownload = (u: string) => u.trim() === "" || /^https?:\/\/\S+$/i.test(u.trim());

/** "User page": the apps it recommends, what it shows, and a live preview in a phone frame. */
export function PageTab({ settings }: { settings: Settings }) {
  // keyed by what is saved, so a save (or a change from elsewhere) restarts the form from the server
  const [picked, setPicked] = useState(""); // the user to preview as survives a save
  return <PageForm key={JSON.stringify(draftOf(settings))} settings={settings} picked={picked} onPick={setPicked} />;
}

function PageForm({ settings, picked, onPick }: { settings: Settings; picked: string; onPick: (id: string) => void }) {
  const t = useTx();
  const toast = useToast();
  const save = useSaveSettings();
  const can = useCan(); // the preview shows the user's real link: helper or owner, as GetSubscriptionLink
  const base = draftOf(settings);
  const [d, setD] = useState(base);
  // nine apps make a long list: open on a wide screen (the phone stays next to it), folded on the phone
  const [appsOpen, setAppsOpen] = useState(() => window.matchMedia("(min-width: 1024px)").matches);
  const dirty = JSON.stringify(d) !== JSON.stringify(base);
  const ok = d.apps.every((a) => a.name.trim() !== "" && validDownload(a.downloadUrl) && a.description.length <= descMax);

  const setApp = (i: number, patch: Partial<PlatformApp>) => setD((x) => ({ ...x, apps: x.apps.map((a, j) => (j === i ? { ...a, ...patch } : a)) }));
  // an app only moves among the apps of its own platform: that is the order of its cards on the page
  const shift = (i: number, by: -1 | 1) =>
    setD((x) => {
      let j = i + by;
      while (j >= 0 && j < x.apps.length && x.apps[j]!.platform !== x.apps[i]!.platform) j += by;
      if (j < 0 || j >= x.apps.length) return x;
      const apps = [...x.apps];
      [apps[i], apps[j]] = [apps[j]!, apps[i]!];
      return { ...x, apps };
    });
  const canShift = (i: number, by: -1 | 1) => d.apps.some((a, j) => (by < 0 ? j < i : j > i) && a.platform === d.apps[i]!.platform);
  const setOpt = (k: keyof Options, v: boolean) => setD((x) => ({ ...x, options: { ...x.options, [k]: v } }));

  function onSave() {
    save
      .mutateAsync((cur) => ({
        ...cur,
        apps: d.apps.map((a) => ({ ...a, name: a.name.trim(), downloadUrl: a.downloadUrl.trim(), addLinkTemplate: a.addLinkTemplate.trim(), description: a.description.trim() })),
        userPage: d.options,
      }))
      .then(() => toast(t("subs.saved")))
      .catch((e: unknown) => toast.error(errorText(e, t)));
  }

  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex flex-col items-stretch gap-4 lg:flex-row lg:items-start">
        <div className="flex w-full min-w-0 flex-1 flex-col gap-3.5">
          <Card lg className="px-4 py-1">
            <SwitchRow label={t("subs.page.opt.ann")} hint={t("subs.page.opt.annHint")} checked={d.options.showAnnouncement} onCheckedChange={(on) => setOpt("showAnnouncement", on)} />
            <SwitchRow label={t("subs.page.opt.sup")} hint={t("subs.page.opt.supHint")} checked={d.options.showSupport} onCheckedChange={(on) => setOpt("showSupport", on)} />
            <SwitchRow label={t("subs.page.opt.qr")} hint={t("subs.page.opt.qrHint")} checked={d.options.showQr} onCheckedChange={(on) => setOpt("showQr", on)} />
            <SwitchRow label={t("subs.page.opt.password")} hint={t("subs.page.opt.passwordHint")} checked={d.options.requirePagePassword} onCheckedChange={(on) => setOpt("requirePagePassword", on)} />
            <SwitchRow label={t("awg.page.selfService")} hint={t("awg.page.selfServiceHint")} checked={d.options.allowDeviceSelfService} onCheckedChange={(on) => setOpt("allowDeviceSelfService", on)} />
          </Card>

          <Card lg className="flex min-w-0 flex-col gap-3 p-4">
            <button type="button" aria-expanded={appsOpen} onClick={() => setAppsOpen(!appsOpen)} className="flex w-full items-center gap-2.5 text-left">
              <SectionLabel as="span" className="flex-1" icon="phone" tone="mint">
                {t("subs.page.apps")}
              </SectionLabel>
              <span className="font-mono text-xs text-muted">{d.apps.length}</span>
              <Icon name="chevronRight" size={14} className={cx("text-muted transition-transform duration-300 ease-spring", appsOpen && "rotate-90")} />
            </button>
            {appsOpen && (
              <>
                <p className="-mt-1 text-xs leading-normal text-muted">{t("subs.page.appsHint")}</p>
                <p className="rounded-xl bg-surface-2 px-3 py-2 font-mono text-[11px] leading-relaxed text-muted">
                  {t("subs.page.appTemplateHint", { url: "{url}", url_enc: "{url_enc}", name_enc: "{name_enc}" })}
                </p>
                {d.apps.length === 0 && <p className="text-[13px] text-muted">{t("subs.page.appsNone")}</p>}
                <div className="flex flex-col gap-2.5">
                  {d.apps.map((a, i) => (
                    <AppRow key={i} app={a} t={t} onChange={(patch) => setApp(i, patch)} onShift={(by) => shift(i, by)} canShift={[canShift(i, -1), canShift(i, 1)]} onRemove={() => setD((x) => ({ ...x, apps: x.apps.filter((_, j) => j !== i) }))} />
                  ))}
                </div>
                <AddApp onAdd={(platform, kind) => setD((x) => ({ ...x, apps: [...x.apps, { platform, kind, name: "", downloadUrl: "", addLinkTemplate: "", description: "", recommended: false }] }))} />
              </>
            )}
          </Card>
        </div>

        {can.run && <PhonePreview picked={picked} onPick={onPick} />}
      </div>

      {dirty && <SaveBar busy={save.isPending} canSave={ok} onSave={onSave} onDiscard={() => setD(base)} />}
    </div>
  );
}

function AppRow({ app, t, onChange, onShift, canShift, onRemove }: { app: PlatformApp; t: ReturnType<typeof useTx>; onChange: (patch: Partial<PlatformApp>) => void; onShift: (by: -1 | 1) => void; canShift: [boolean, boolean]; onRemove: () => void }) {
  const name = app.name || t(platformKey[app.platform] ?? "subs.platform.ios");
  const badDownload = !validDownload(app.downloadUrl);
  const mini = "text-[11px] font-bold tracking-[0.06em] text-muted uppercase";
  const box = `${inputCls} h-9 text-xs`;
  return (
    <div className="flex flex-col gap-2 rounded-card bg-surface-2 p-3">
      <div className="flex items-center gap-2">
        <Chip className="bg-surface text-fg">{t(platformKey[app.platform] ?? "subs.platform.ios")}</Chip>
        <Chip className="bg-surface">{t(kindKey[app.kind] ?? "subs.kind.happ")}</Chip>
        <span className="flex-1" />
        <IconButton variant="flat" aria-label={t("subs.page.appUp", { name })} disabled={!canShift[0]} onClick={() => onShift(-1)}>
          <Icon name="arrowUp" size={14} />
        </IconButton>
        <IconButton variant="flat" aria-label={t("subs.page.appDown", { name })} disabled={!canShift[1]} onClick={() => onShift(1)}>
          <Icon name="arrowDown" size={14} />
        </IconButton>
        <IconButton variant="flat" aria-label={t("subs.page.appRemove", { name })} onClick={onRemove}>
          <Icon name="trash" size={14} />
        </IconButton>
      </div>
      <div className="grid gap-2 sm:grid-cols-[minmax(0,0.8fr)_minmax(0,1.4fr)]">
        <label className="flex min-w-0 flex-col gap-1">
          <span className={mini}>{t("subs.page.appName")}</span>
          <input aria-invalid={app.name.trim() === "" || undefined} className={box} value={app.name} onChange={(e) => onChange({ name: e.target.value })} maxLength={60} autoComplete="off" />
        </label>
        <label className="flex min-w-0 flex-col gap-1">
          <span className={mini}>{t("subs.page.appDownload")}</span>
          <input aria-invalid={badDownload || undefined} className={`${box} font-mono`} value={app.downloadUrl} placeholder="https://…" onChange={(e) => onChange({ downloadUrl: e.target.value })} autoComplete="off" spellCheck={false} inputMode="url" />
          {badDownload && (
            <span role="alert" className="text-[11px] leading-snug text-danger-text">
              {t("subs.page.badUrl")}
            </span>
          )}
        </label>
      </div>
      <label className="flex min-w-0 flex-col gap-1">
        <span className={mini}>{t("subs.page.appTemplate")}</span>
        <input className={`${box} font-mono`} value={app.addLinkTemplate} placeholder="happ://add/{url}" onChange={(e) => onChange({ addLinkTemplate: e.target.value })} autoComplete="off" spellCheck={false} />
      </label>
      <label className="flex min-w-0 flex-col gap-1">
        <span className={mini}>{t("subs.page.appDesc")}</span>
        <input className={box} value={app.description} maxLength={descMax} placeholder={t("subs.page.appDescPlaceholder")} onChange={(e) => onChange({ description: e.target.value })} autoComplete="off" />
        <span className="text-[11px] leading-snug text-muted">{t("subs.page.appDescHint", { n: String(app.description.length), max: String(descMax) })}</span>
      </label>
      <label className="flex items-center gap-2.5">
        <Switch checked={app.recommended} onCheckedChange={(on) => onChange({ recommended: on })} />
        <span className="flex min-w-0 flex-col">
          <span className="text-[13px] font-bold">{t("subs.page.appRec")}</span>
          <span className="text-[11px] leading-snug text-muted">{t("subs.page.appRecHint")}</span>
        </span>
      </label>
    </div>
  );
}

function AddApp({ onAdd }: { onAdd: (platform: Platform, kind: App) => void }) {
  const t = useTx();
  const [platform, setPlatform] = useState<Platform>(Platform.IOS);
  const [kind, setKind] = useState<App>(App.HAPP);
  return (
    <div className="flex flex-col gap-2 border-t border-line pt-3 sm:flex-row sm:items-end">
      <div className="flex flex-1 flex-col gap-1.5">
        <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("subs.page.addPlatform")}</span>
        <Select aria-label={t("subs.page.addPlatform")} value={String(platform)} onValueChange={(v) => setPlatform(Number(v) as Platform)} options={platforms.map((p) => ({ value: String(p), label: t(platformKey[p]!) }))} />
      </div>
      <div className="flex flex-1 flex-col gap-1.5">
        <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("subs.page.addKind")}</span>
        <Select aria-label={t("subs.page.addKind")} value={String(kind)} onValueChange={(v) => setKind(Number(v) as App)} options={kinds.map((k) => ({ value: String(k), label: t(kindKey[k]!) }))} />
      </div>
      <Button variant="secondary" size="lg" onClick={() => onAdd(platform, kind)}>
        <Icon name="plus" size={14} />
        {t("subs.page.add")}
      </Button>
    </div>
  );
}

/** The phone: the page the server renders for the chosen user, in an iframe that reloads when the saved settings change. */
function PhonePreview({ picked, onPick }: { picked: string; onPick: (id: string) => void }) {
  const t = useTx();
  const users = useQuery(pickerUsersQuery);
  const [reload, setReload] = useState(0);
  const list = users.data ?? [];
  const userId = list.some((u) => u.id === picked) ? picked : (list[0]?.id ?? "");
  const src = userId ? previewUrl(document.baseURI, userId) : "";

  return (
    <div className="flex w-full flex-none flex-col gap-2.5 lg:sticky lg:top-[72px] lg:w-[360px]">
      <div className="flex items-end gap-2">
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          <SectionLabel as="span" icon="code" tone="mint">
            {t("subs.page.previewAs")}
          </SectionLabel>
          {users.isPending ? (
            <div className="h-11 rounded-field bg-surface-2" />
          ) : users.isError ? (
            <QueryError error={users.error} onRetry={() => void users.refetch()} />
          ) : list.length === 0 ? (
            <p className="text-[13px] text-muted">{t("subs.page.noUsers")}</p>
          ) : (
            <Select aria-label={t("subs.page.user")} value={userId} onValueChange={onPick} options={list.map((u) => ({ value: u.id, label: u.name }))} />
          )}
        </div>
        <IconButton aria-label={t("subs.page.reload")} title={t("subs.page.reload")} className="mb-[5px]" onClick={() => setReload((n) => n + 1)}>
          <Icon name="refresh" size={14} />
        </IconButton>
      </div>
      <div className="mx-auto h-[720px] w-full max-w-[360px] overflow-hidden rounded-[36px] border border-line bg-surface">
        {src ? (
          // remounted when the user or the reload button change, and with the whole form when the saved settings do.
          // A sandboxed frame has no origin: the session cookie (SameSite) would not go with it and the preview would 401.
          // oxlint-disable-next-line react/iframe-missing-sandbox -- our own page, same origin, locked by its own CSP
          <iframe key={`${userId}:${reload}`} title={t("subs.page.frame")} src={src} referrerPolicy="no-referrer" className="size-full border-0" />
        ) : users.isPending ? (
          <Pending />
        ) : null}
      </div>
      <p className="text-center text-[11px] leading-snug text-muted">{t("subs.page.previewNote")}</p>
    </div>
  );
}
