import { useQuery } from "@tanstack/react-query";
import { useMemo, useRef, useState } from "react";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { Platform } from "@/gen/mistgate/admin/v1/subscription_pb";
import { Button, buttonClass } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { useToast } from "@/components/ui/toast";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useCan } from "@/lib/health";
import { useLang } from "@/i18n";
import { useTx, type Tx } from "@/screens/users/t";
import { DefaultMark } from "@/components/brand";
import { catalogOf, kindKey, kinds, platformKey, platforms, presetName, presetSubtitle, previewUrl, transportKey, validDownload, type PlatformApp, type Settings } from "./model";
import { appOf, knownFor } from "./known-apps";
import { sbDemo } from "./demo-sb";
import { Grid, path, Section, Svg, Toggle } from "./parts";
import { dnsPresetsQuery, pickerUsersQuery, useSaveSettings } from "./queries";

type Options = { showAnnouncement: boolean; showSupport: boolean; showQr: boolean; allowDeviceSelfService: boolean; requirePagePassword: boolean; allowDnsChoice: boolean };
type LinkOffer = { presetIds: string[]; defaultPresetId: string };
type Draft = { apps: PlatformApp[]; options: Options; linkDns: LinkOffer };
const draftOf = (s: Settings): Draft => ({
  apps: s.apps,
  linkDns: { presetIds: s.linkDns?.presetIds ?? [], defaultPresetId: s.linkDns?.defaultPresetId ?? "" },
  options: { showAnnouncement: s.userPage?.showAnnouncement ?? true, showSupport: s.userPage?.showSupport ?? true, showQr: s.userPage?.showQr ?? true, allowDeviceSelfService: s.userPage?.allowDeviceSelfService ?? true, requirePagePassword: s.userPage?.requirePagePassword ?? true, allowDnsChoice: s.userPage?.allowDnsChoice ?? false },
});
const descMax = 80;
/** How many presets the page may offer for the apps on the link (the panel's limit). */
const maxOffered = 20;

/** "User page": the apps it recommends, what it shows, which DNS a person may pick, and the page itself in a phone frame. */
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
  const [open, setOpen] = useState(() => sbDemo.appOpen ?? -1);
  const dirty = JSON.stringify(d) !== JSON.stringify(base);
  const ok = d.apps.every((a) => a.name.trim() !== "" && validDownload(a.downloadUrl) && a.description.length <= descMax);

  const setApp = (i: number, patch: Partial<PlatformApp>) => setD((x) => ({ ...x, apps: x.apps.map((a, j) => (j === i ? { ...a, ...patch } : a)) }));
  // an app moves anywhere in the list: what matters to the page is the order among the apps of one platform, which a move keeps
  const moveTo = (i: number, to: number) => {
    if (to < 0 || to >= d.apps.length || to === i) return false;
    setD((x) => {
      const apps = [...x.apps];
      apps.splice(to, 0, apps.splice(i, 1)[0]!);
      return { ...x, apps };
    });
    return true;
  };
  const setOpt = (k: keyof Options, v: boolean) => setD((x) => ({ ...x, options: { ...x.options, [k]: v } }));

  function onSave() {
    save
      .mutateAsync((cur) => ({
        ...cur,
        apps: d.apps.map((a) => ({ ...a, name: a.name.trim(), downloadUrl: a.downloadUrl.trim(), addLinkTemplate: a.addLinkTemplate.trim(), description: a.description.trim() })),
        userPage: d.options,
        linkDns: d.linkDns,
      }))
      .then(() => {
        toast(t("subs.saved"));
      })
      .catch((e: unknown) => toast.error(errorText(e, t)));
  }

  return (
    <Grid
      main={
        <>
          <AppsSection d={d} t={t} open={open} setOpen={setOpen} setApp={setApp} moveTo={moveTo} onRemove={(i) => (setOpen(-1), setD((x) => ({ ...x, apps: x.apps.filter((_, j) => j !== i) })))} onAdd={(app) => (setOpen(d.apps.length), setD((x) => ({ ...x, apps: [...x.apps, app] })))} />
          <div className={cx("sb-save", (dirty || save.isPending) && "on")} role="status">
            <span>{t("subs.dirty")}</span>
            <Button variant="ghost" onClick={() => setD(base)} disabled={save.isPending}>
              {t("subs.discard")}
            </Button>
            <Button variant="primary" disabled={!ok || save.isPending || !dirty} onClick={onSave}>
              {t("subs.save")}
            </Button>
          </div>
        </>
      }
      side={
        <>
          <Section
            tone="lav"
            icon={path.eye}
            title={t("sx.page.what")}
            shot="subs-page-what"
            side={
              <PageLink picked={picked} can={can.run} />
            }
          >
            <div className="sb-tgs">
              <Toggle title={t("subs.page.opt.ann")} hint={t("sx.page.annHint")} checked={d.options.showAnnouncement} onChange={(on) => setOpt("showAnnouncement", on)} />
              <Toggle title={t("subs.page.opt.sup")} hint={t("sx.page.supHint")} checked={d.options.showSupport} onChange={(on) => setOpt("showSupport", on)} />
              <Toggle title={t("subs.page.opt.qr")} hint={t("sx.page.qrHint")} checked={d.options.showQr} onChange={(on) => setOpt("showQr", on)} />
              <Toggle title={t("subs.page.opt.password")} hint={t("subs.page.opt.passwordHint")} checked={d.options.requirePagePassword} onChange={(on) => setOpt("requirePagePassword", on)} />
              <Toggle title={t("awg.page.selfService")} hint={t("awg.page.selfServiceHint")} checked={d.options.allowDeviceSelfService} onChange={(on) => setOpt("allowDeviceSelfService", on)} />
              <Toggle title={t("subs.page.opt.dnsChoice")} hint={t("subs.page.opt.dnsChoiceHint")} checked={d.options.allowDnsChoice} onChange={(on) => setOpt("allowDnsChoice", on)} />
            </div>
          </Section>
          <LinkDnsSection offer={d.linkDns} on={d.options.allowDnsChoice} onChange={(linkDns) => setD((x) => ({ ...x, linkDns }))} />
          {can.run && <PhonePreview picked={picked} onPick={onPick} />}
        </>
      }
    />
  );
}

/** The button of the head of "What the page has": the page of the person, in a tab of its own. */
function PageLink({ picked, can }: { picked: string; can: boolean }) {
  const t = useTx();
  const users = useQuery({ ...pickerUsersQuery, enabled: can });
  const list = users.data ?? [];
  const id = list.some((u) => u.id === picked) ? picked : (list[0]?.id ?? "");
  if (!can || !id) return null;
  return (
    <a className={cx(buttonClass("secondary", "md"), "sb-btn")} href={previewUrl(document.baseURI, id)} target="_blank" rel="noreferrer">
      {t("sx.page.open")}
      <Svg d={path.arrowR} className="flip" />
    </a>
  );
}

// ---- the apps of the page

function AppsSection({ d, t, open, setOpen, setApp, moveTo, onRemove, onAdd }: { d: Draft; t: Tx; open: number; setOpen: (i: number) => void; setApp: (i: number, patch: Partial<PlatformApp>) => void; moveTo: (i: number, to: number) => boolean; onRemove: (i: number) => void; onAdd: (app: PlatformApp) => void }) {
  const list = useRef<HTMLDivElement>(null);
  const [drop, setDrop] = useState(-1);
  const apps = d.apps;

  // the grip: press and drag a card over the others (they step aside), let go to drop it; the keys do the same by one place
  const startDrag = (i: number) => (e: React.PointerEvent) => {
    if (e.button) return;
    e.preventDefault();
    const box = list.current;
    if (!box) return;
    const y0 = e.clientY;
    const begin = () => {
      const rows = [...box.querySelectorAll<HTMLElement>(".sb-ap")];
      const el = rows[i];
      if (!el) return;
      const rc = rows.map((r) => r.getBoundingClientRect());
      const h = rc[i]!.height;
      let to = i;
      el.classList.add("drag");
      box.classList.add("dragging");
      const mv = (ev: PointerEvent) => {
        const dy = Math.max(rc[0]!.top - rc[i]!.top, Math.min(rc[rows.length - 1]!.bottom - rc[i]!.bottom, ev.clientY - y0));
        el.style.transform = `translateY(${dy}px)`;
        const mid = rc[i]!.top + dy + h / 2;
        to = rc.findIndex((r) => mid < r.bottom);
        if (to < 0) to = rows.length - 1;
        rows.forEach((r, j) => {
          if (j !== i) r.style.transform = j > i && j <= to ? `translateY(${-h}px)` : j < i && j >= to ? `translateY(${h}px)` : "";
        });
      };
      const up = () => {
        window.removeEventListener("pointermove", mv);
        window.removeEventListener("pointerup", up);
        window.removeEventListener("pointercancel", up);
        box.classList.remove("dragging");
        rows.forEach((r) => (r.style.transform = ""));
        el.classList.remove("drag");
        if (moveTo(i, to)) {
          setDrop(to);
          setTimeout(() => setDrop(-1), 700);
        }
      };
      window.addEventListener("pointermove", mv);
      window.addEventListener("pointerup", up);
      window.addEventListener("pointercancel", up);
    };
    // an open card folds first, so the rows are of one height
    if (open >= 0) {
      setOpen(-1);
      setTimeout(begin, 40);
    } else begin();
  };
  const keyMove = (i: number) => (e: React.KeyboardEvent) => {
    const step = e.key === "ArrowUp" ? -1 : e.key === "ArrowDown" ? 1 : 0;
    if (!step) return;
    e.preventDefault();
    if (moveTo(i, i + step))
      setTimeout(() => {
        box()?.querySelectorAll<HTMLElement>(".sb-grip")[i + step]?.focus();
      }, 40);
  };
  const box = () => list.current;

  return (
    <Section tone="mint" icon={path.phone} title={t("subs.page.apps")} side={<span className="sb-cnt2 mono">{apps.length}</span>} shot="subs-page-apps">
      <p className="sb-lead">{t("sx.page.appsLead")}</p>
      {apps.length === 0 && <p className="sb-emp">{t("subs.page.appsNone")}</p>}
      <div className="sb-aps" ref={list}>
        {apps.map((a, i) => (
          <AppCard key={i} i={i} a={a} t={t} open={open === i} drop={drop === i} last={i === apps.length - 1} onToggle={() => setOpen(open === i ? -1 : i)} onChange={(patch) => setApp(i, patch)} onMove={(to) => moveTo(i, to)} onRemove={() => onRemove(i)} grip={{ onPointerDown: startDrag(i), onKeyDown: keyMove(i) }} />
        ))}
      </div>
      <AddApp onAdd={onAdd} t={t} />
      <p className="sb-vfoot">{t("subs.page.appTemplateHint", { url: "{url}", url_enc: "{url_enc}", name_enc: "{name_enc}" })}</p>
    </Section>
  );
}

function AppCard({ i, a, t, open, drop, last, onToggle, onChange, onMove, onRemove, grip }: { i: number; a: PlatformApp; t: Tx; open: boolean; drop: boolean; last: boolean; onToggle: () => void; onChange: (patch: Partial<PlatformApp>) => void; onMove: (to: number) => boolean; onRemove: () => void; grip: { onPointerDown: (e: React.PointerEvent) => void; onKeyDown: (e: React.KeyboardEvent) => void } }) {
  const platform = t(platformKey[a.platform] ?? "subs.platform.ios");
  const name = a.name || platform;
  const bad = !validDownload(a.downloadUrl);
  const n = String(a.description.length);
  return (
    <div className={cx("sb-ap", open && "open", drop && "drop")}>
      <div className="sb-apr">
        <button type="button" className="sb-grip" aria-label={t("sx.page.grip", { name, platform })} title={t("sx.page.gripTip")} {...grip}>
          <Svg d={path.grip} />
        </button>
        <button type="button" className="sb-aph" aria-expanded={open} onClick={onToggle}>
          <span className="sb-plat">{platform}</span>
          <i className="sb-mono" aria-hidden="true">
            {name.charAt(0).toUpperCase()}
          </i>
          <b>{name}</b>
          <em>{t(kindKey[a.kind] ?? "subs.kind.happ")}</em>
        </button>
        <button type="button" className="sb-recb" aria-pressed={a.recommended} onClick={() => onChange({ recommended: !a.recommended })}>
          {a.recommended ? t("subs.page.appRec") : t("sx.page.recMake")}
        </button>
        <button type="button" className="sb-chb" aria-label={t("sx.page.card", { name })} aria-expanded={open} onClick={onToggle}>
          <Svg d={path.chevR} className="sb-chev flip" />
        </button>
      </div>
      {open && (
        <div className="sb-apb">
          <label>
            <span>{t("subs.page.appName")}</span>
            <input className="an-i" type="text" value={a.name} aria-invalid={a.name.trim() === "" || undefined} maxLength={60} autoComplete="off" onChange={(e) => onChange({ name: e.target.value })} />
          </label>
          <label>
            <span>{t("subs.page.appDownload")}</span>
            <input className="an-i mono" type="text" value={a.downloadUrl} aria-invalid={bad || undefined} placeholder="https://…" autoComplete="off" spellCheck={false} inputMode="url" onChange={(e) => onChange({ downloadUrl: e.target.value })} />
            {bad && (
              <em role="alert" className="bad">
                {t("subs.page.badUrl")}
              </em>
            )}
          </label>
          <label>
            <span>{t("subs.page.appTemplate")}</span>
            <input className="an-i mono" type="text" value={a.addLinkTemplate} placeholder="happ://add/{url}" autoComplete="off" spellCheck={false} onChange={(e) => onChange({ addLinkTemplate: e.target.value })} />
          </label>
          <label className="w">
            <span>{t("subs.page.appDesc")}</span>
            <input className="an-i" type="text" value={a.description} maxLength={descMax} placeholder={t("subs.page.appDescPlaceholder")} autoComplete="off" onChange={(e) => onChange({ description: e.target.value })} />
            <em>
              {t("subs.page.appDescHint", { n, max: String(descMax) })}
            </em>
          </label>
          <div className="sb-apf">
            <button type="button" className="sb-dck" role="checkbox" aria-checked={a.recommended} onClick={() => onChange({ recommended: !a.recommended })}>
              <Check on={a.recommended} />
              <span>
                <b>{t("subs.page.appRec")}</b>
                <em>{t("subs.page.appRecHint")}</em>
              </span>
            </button>
            <span className="sb-mv">
              <button type="button" className="sb-ic" aria-label={t("subs.page.appUp", { name })} aria-disabled={i === 0} onClick={() => i > 0 && onMove(i - 1)}>
                <Svg d={path.up} />
              </button>
              <button type="button" className="sb-ic" aria-label={t("subs.page.appDown", { name })} aria-disabled={last} onClick={() => !last && onMove(i + 1)}>
                <Svg d={path.chevD} />
              </button>
              <Button className="sb-btn" aria-label={t("subs.page.appRemove", { name })} onClick={onRemove}>
                {t("sx.page.remove")}
              </Button>
            </span>
          </div>
        </div>
      )}
    </div>
  );
}

/** The box of a tick (the board's .fz-cb): drawn, the button around it is the checkbox. */
function Check({ on }: { on: boolean }) {
  return (
    <span className={cx("sb-cb", on && "on")} aria-hidden="true">
      <i>
        <Svg d={path.check} />
      </i>
    </span>
  );
}

const custom = "custom"; // the "known app" choice that leaves the new card blank

/** "Add app": a platform and either a known app (it fills the card) or a kind (a blank card). */
function AddApp({ onAdd, t }: { onAdd: (app: PlatformApp) => void; t: Tx }) {
  const [platform, setPlatform] = useState<Platform>(() => sbDemo.addPlatform ?? Platform.IOS);
  const [kind, setKind] = useState<App>(App.HAPP);
  const [known, setKnown] = useState(custom);
  const here = knownFor(platform);
  const pick = here.some((k) => k.id === known) ? known : custom; // a known app without a build on this platform falls back to a blank card
  const add = () => onAdd(appOf(pick, platform) ?? { platform, kind, name: "", downloadUrl: "", addLinkTemplate: "", description: "", recommended: false });
  return (
    <div className="sb-add" data-shot="subs-page-add">
      <Segmented variant="flat" className="sb-seg" aria-label={t("subs.page.addPlatform")} value={String(platform)} onValueChange={(v) => setPlatform(Number(v) as Platform)} options={platforms.map((p) => ({ value: String(p), label: t(platformKey[p]!) }))} />
      {pick === custom && <Segmented variant="flat" className="sb-seg" aria-label={t("subs.page.addKind")} value={String(kind)} onValueChange={(v) => setKind(Number(v) as App)} options={kinds.map((k) => ({ value: String(k), label: t(kindKey[k]!) }))} />}
      {here.length > 0 && (
        <div className="sb-known">
          <Select compact aria-label={t("subs.page.addKnown")} value={pick} onValueChange={setKnown} options={[{ value: custom, label: t("subs.page.knownCustom") }, ...here.map((k) => ({ value: k.id, label: k.name }))]} />
        </div>
      )}
      <Button className="sb-btn" onClick={add}>
        <Svg d={path.plus} />
        {t("subs.page.add")}
      </Button>
    </div>
  );
}

// ---- DNS for the apps on the link

/** The presets a person may pick for Happ and the other apps on the link, one DNS for all servers (SubscriptionSettings.link_dns). */
function LinkDnsSection({ offer, on, onChange }: { offer: LinkOffer; on: boolean; onChange: (o: LinkOffer) => void }) {
  const t = useTx();
  const lang = useLang();
  const presets = useQuery(dnsPresetsQuery);
  const data = presets.data;
  const catalog = useMemo(() => catalogOf(data?.providers ?? []), [data?.providers]);
  const byId = new Map((data?.presets ?? []).map((p) => [p.id, p]));
  const ids = offer.presetIds;
  const def = offer.defaultPresetId;
  const full = ids.length >= maxOffered;
  const setIds = (next: string[]) => onChange({ presetIds: next, defaultPresetId: next.includes(def) ? def : "" });
  const toggle = (id: string, yes: boolean) => setIds(yes ? [...ids, id] : ids.filter((y) => y !== id));
  // the offered ones first, in the owner's order, then the rest as the presets are listed
  const rows = data ? [...ids.flatMap((id) => byId.get(id) ?? []), ...data.presets.filter((p) => !ids.includes(p.id))] : [];
  const sum = !on ? t("sx.page.dnsOff") : def ? t("sx.page.dnsDef", { name: presetName(byId.get(def) ?? { id: def, name: def }, lang) }) : t("subs.dnsOpt.defNoneHint");

  return (
    <Section tone="sky" icon={path.globe} title={t("subs.linkDns.title")} side={<span className="sb-cnt2 mono">{t("sx.page.dnsN", { n: ids.length, max: maxOffered })}</span>} shot="subs-page-dns">
      <p className="sb-lead">{t("subs.linkDns.note")}</p>
      {presets.isError ? (
        <div className="sb-pad">
          <QueryError compact error={presets.error} onRetry={() => void presets.refetch()} />
        </div>
      ) : !data ? (
        <div className="sb-pad">
          <Pending compact />
        </div>
      ) : data.presets.length === 0 ? (
        <p className="sb-emp">{t("subs.dnsOpt.empty")}</p>
      ) : (
        <div className={cx("sb-dl", !on && "off")}>
          {rows.map((p) => {
            const at = ids.indexOf(p.id);
            const yes = at >= 0;
            const name = presetName(p, lang);
            const sub = presetSubtitle(p, (s) => t("subs.dns.sub.direct", { s }), catalog, (tr) => t(transportKey[tr]));
            return (
              <div className="sb-dr" key={p.id}>
                <button type="button" className="sb-dck" role="checkbox" aria-label={t("subs.dnsOpt.offer", { name })} aria-checked={yes} disabled={!yes && full} onClick={() => toggle(p.id, !yes)}>
                  <Check on={yes} />
                  <span>
                    <b>{name}</b>
                    {sub && <em>{sub}</em>}
                  </span>
                </button>
                {yes && (
                  <>
                    <span className="sb-mv">
                      <button type="button" className="sb-ic" aria-label={t("subs.dnsOpt.up", { name })} aria-disabled={at === 0} onClick={() => at > 0 && setIds(swap(ids, at, at - 1))}>
                        <Svg d={path.up} />
                      </button>
                      <button type="button" className="sb-ic" aria-label={t("subs.dnsOpt.down", { name })} aria-disabled={at === ids.length - 1} onClick={() => at < ids.length - 1 && setIds(swap(ids, at, at + 1))}>
                        <Svg d={path.chevD} />
                      </button>
                    </span>
                    <button type="button" className="sb-def" aria-pressed={def === p.id} aria-label={t("subs.dnsOpt.def", { name })} onClick={() => onChange({ presetIds: ids, defaultPresetId: def === p.id ? "" : p.id })}>
                      {def === p.id ? t("subs.dnsOpt.defShort") : t("sx.page.dnsMakeDef")}
                    </button>
                  </>
                )}
              </div>
            );
          })}
        </div>
      )}
      <p className="sb-vfoot">{sum}</p>
    </Section>
  );
}

const swap = (l: string[], a: number, b: number) => {
  const x = [...l];
  [x[a], x[b]] = [x[b]!, x[a]!];
  return x;
};

// ---- the page itself

/** The phone: the page the server renders for the chosen user, in an iframe that reloads when the saved settings change. */
function PhonePreview({ picked, onPick }: { picked: string; onPick: (id: string) => void }) {
  const t = useTx();
  const users = useQuery(pickerUsersQuery);
  const [reload, setReload] = useState(0);
  const list = users.data ?? [];
  const userId = list.some((u) => u.id === picked) ? picked : (list[0]?.id ?? "");
  const src = userId ? previewUrl(document.baseURI, userId) : "";

  return (
    <Section tone="mint" icon={<DefaultMark size={18} />} title={t("subs.page.previewAs")} shot="subs-page-phone">
      <div className="sb-pad sb-who2">
        {users.isPending ? (
          <div className="sb-skel" />
        ) : users.isError ? (
          <QueryError error={users.error} onRetry={() => void users.refetch()} />
        ) : list.length === 0 ? (
          <p className="pp-q1">{t("subs.page.noUsers")}</p>
        ) : (
          <Select aria-label={t("subs.page.user")} value={userId} onValueChange={onPick} options={list.map((u) => ({ value: u.id, label: u.name }))} />
        )}
        <IconButton aria-label={t("subs.page.reload")} title={t("subs.page.reload")} onClick={() => setReload((n) => n + 1)}>
          <Icon name="refresh" size={14} />
        </IconButton>
      </div>
      <div className="sb-phone">
        {src ? (
          // remounted when the user or the reload button change, and with the whole form when the saved settings do.
          // A sandboxed frame has no origin: the session cookie (SameSite) would not go with it and the preview would 401.
          // oxlint-disable-next-line react/iframe-missing-sandbox -- our own page, same origin, locked by its own CSP
          <iframe key={`${userId}:${reload}`} title={t("subs.page.frame")} src={src} referrerPolicy="no-referrer" className="size-full border-0" />
        ) : users.isPending ? (
          <Pending />
        ) : null}
      </div>
      <p className="pp-q1 sb-pad">{t("subs.page.previewNote")}</p>
    </Section>
  );
}
