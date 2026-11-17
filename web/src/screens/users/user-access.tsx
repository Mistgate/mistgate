import { Checkbox } from "@base-ui/react/checkbox";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo, useState, type ReactNode } from "react";
import { DefaultMark } from "@/components/brand";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Notice } from "@/components/ui/notice";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { Segmented } from "@/components/ui/segmented";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { QuotaReset, type AccessImpact, type NodeAccess, type ProfileRef } from "@/gen/mistgate/admin/v1/user_pb";
import { dns, groups as groupsApi, users } from "@/lib/api";
import { clientApiQuery } from "@/lib/clientapi";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useCan } from "@/lib/health";
import { plain } from "@/lib/plain";
import { FormatDrops } from "@/screens/profiles/formats";
import { telegramOnly, trustTunnelOnly } from "@/screens/profiles/protocol-meta";
import { catalogOf, presetName, presetSubtitle, transportKey } from "@/screens/subscriptions/model";
import { dnsPresetsQuery, useLinkAppList, useLinkAppNames } from "@/screens/subscriptions/queries";
import { effectiveDnsText, useDnsLabel, useInheritedDns } from "./dns-select";
import { GB, daysLeft, nowSec } from "./format";
import { ImpactText, wayOf } from "./groups";
import type { DetailN, UserN as User } from "./model";
import { groupsQuery, protocolsQuery } from "./rpc";
import { useTx } from "./t";
import { ConfirmModal, ResetSelect, resetCode } from "./ui";
import type { Actions } from "./user-actions";
import { useReadonly } from "./user-actions";
import { G, GroupDot, P, Rad, SetHead } from "./user-bits";

// ---- small controls of the board ----

/** The board's stepper (.sv-step): 36 px, "−", the number, "+". */
function UpStep({ value, onChange, min, max, label }: { value: number; onChange: (v: number) => void; min: number; max: number; label: string }) {
  const t = useTx();
  return (
    <div className="up-step" role="group" aria-label={label}>
      <button type="button" aria-label={t("users.less", { what: label })} disabled={value <= min} onClick={() => onChange(Math.max(min, value - 1))}>
        <G d={P.minus} size={16} />
      </button>
      <b>{value}</b>
      <button type="button" aria-label={t("users.more", { what: label })} disabled={value >= max} onClick={() => onChange(Math.min(max, value + 1))}>
        <G d={P.plus} size={16} />
      </button>
    </div>
  );
}

/** A segment of a few ready numbers and "Other…" with a field: a person's quota or speed need not be one of the ready ones (an agent sets any). */
function SegNum({ value, presets, unit, max, onChange, label }: { value: number; presets: { v: number; label: string }[]; unit: string; max: number; onChange: (v: number) => void; label: string }) {
  const t = useTx();
  const known = presets.some((p) => p.v === value);
  const [other, setOther] = useState(false);
  const custom = other || !known;
  return (
    <div className="flex flex-col items-end gap-2 max-md:items-stretch">
      <Segmented
        aria-label={label}
        className="up-seg"
        value={custom ? "other" : String(value)}
        onValueChange={(v) => {
          if (v === "other") return setOther(true);
          setOther(false);
          onChange(Number(v));
        }}
        options={[...presets.map((p) => ({ value: String(p.v), label: p.label })), { value: "other", label: t("up.other") }]}
      />
      {custom && (
        <label className="up-gb">
          <input
            type="number"
            inputMode="numeric"
            min={0}
            max={max}
            aria-label={`${label}, ${unit}`}
            value={value}
            onChange={(e) => onChange(Math.min(max, Math.max(0, Math.floor(Number(e.target.value) || 0))))}
          />
          <span className="text-xs text-muted">{unit}</span>
        </label>
      )}
    </div>
  );
}

// ---- limits ----

type Lim = { quota: number; reset: QuotaReset; devices: number; speed: number; devSpeed: number };
const limOf = (u: User): Lim => ({
  quota: u.quotaBytes === 0 ? 0 : Math.max(1, Math.round(u.quotaBytes / GB)),
  reset: u.quotaReset === QuotaReset.UNSPECIFIED ? QuotaReset.MONTH : u.quotaReset,
  devices: u.deviceLimit,
  speed: Math.round(u.speedLimitBps / 1e6),
  devSpeed: Math.round(u.deviceSpeedLimitBps / 1e6),
});

/**
 * "Limits" (the board's .sv-c): the traffic, the speeds (experimental), the places and the term, each a row with what it says and
 * what changes it. The values are a draft: a bar under the rows says "not saved" and carries Undo and Save (one call with what changed).
 * The term buttons act at once. A limit below the places in use is said before it is saved: nobody is cut off, the new ones are refused.
 */
export function LimitsPanel({ user, actions }: { user: User; actions: Actions }) {
  const t = useTx();
  const readonly = useReadonly();
  const server = limOf(user);
  const [draft, setDraft] = useState<Lim | null>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const lim = draft ?? server;
  const dirty = draft !== null && JSON.stringify(draft) !== JSON.stringify(server);
  const set = (o: Partial<Lim>) => {
    setDone(false);
    setDraft({ ...lim, ...o });
  };
  const now = nowSec();
  const d = daysLeft(user.expiresUnix, now);
  const used = user.devicesUsed;

  async function save() {
    if (!draft || busy) return;
    setBusy(true);
    try {
      await actions.update({
        ...(draft.quota !== server.quota && { quotaBytes: draft.quota * GB }),
        ...(draft.reset !== server.reset && { quotaReset: draft.reset }),
        ...(draft.devices !== server.devices && { deviceLimit: draft.devices }),
        ...(draft.speed !== server.speed && { speedLimitBps: draft.speed * 1e6 }),
        ...(draft.devSpeed !== server.devSpeed && { deviceSpeedLimitBps: draft.devSpeed * 1e6 }),
      });
      setDraft(null);
      setDone(true);
      window.setTimeout(() => setDone(false), 2400);
    } catch {
      // actions.update shows the server's reason and the draft stays for a correction
    } finally {
      setBusy(false);
    }
  }

  const row = (title: ReactNode, hint: ReactNode, own: boolean, control: ReactNode) => (
    <div>
      <div>
        <b>{title}</b>
        <span className={own ? "own" : ""}>{hint}</span>
      </div>
      {control}
    </div>
  );
  const speedPresets = [
    { v: 0, label: t("users.unlimited") },
    { v: 20, label: "20" },
    { v: 50, label: "50" },
    { v: 100, label: `100 ${t("users.unitMbit")}` },
  ];
  const exp = <span className="up-tag">{t("users.experimental")}</span>;

  return (
    <section className="up-card" data-testid="limits">
      <SetHead title={t("users.limits")} line={t("up.limitsLine")} />
      <fieldset disabled={readonly} className="up-lim">
        {row(
          t("users.quota"),
          t(`users.resetHint.${resetCode(lim.reset)}`) && `${t(`users.resetOpt.${resetCode(lim.reset)}`)} · ${t(`users.resetHint.${resetCode(lim.reset)}`)}`,
          lim.quota !== server.quota,
          <SegNum label={t("users.quota")} unit={t("users.unitGb")} max={10000} value={lim.quota} presets={[{ v: 50, label: `50 ${t("users.unitGb")}` }, { v: 100, label: `100 ${t("users.unitGb")}` }, { v: 300, label: `300 ${t("users.unitGb")}` }, { v: 0, label: t("users.unlimited") }]} onChange={(v) => set({ quota: v })} />,
        )}
        {row(t("users.resets"), t("up.resetsHint"), lim.reset !== server.reset, <div className="w-[170px] max-md:w-full"><ResetSelect value={lim.reset} onChange={(v) => set({ reset: v })} /></div>)}
        {row(
          <>
            {t("users.speedUser")}
            {exp}
          </>,
          lim.speed === server.speed ? t("users.speedUserHint") : t("up.ownValue"),
          lim.speed !== server.speed,
          <SegNum label={t("users.speedUser")} unit={t("users.unitMbit")} max={10000} value={lim.speed} presets={speedPresets} onChange={(v) => set({ speed: v })} />,
        )}
        {row(
          <>
            {t("users.speedDevice")}
            {exp}
          </>,
          lim.devSpeed === server.devSpeed ? t("users.speedDeviceHint") : t("up.ownValue"),
          lim.devSpeed !== server.devSpeed,
          <SegNum label={t("users.speedDevice")} unit={t("users.unitMbit")} max={10000} value={lim.devSpeed} presets={[{ v: 0, label: t("users.unlimited") }, { v: 10, label: "10" }, { v: 20, label: "20" }, { v: 50, label: `50 ${t("users.unitMbit")}` }]} onChange={(v) => set({ devSpeed: v })} />,
        )}
        {row(t("users.devices"), t("up.devicesHint", { used }), lim.devices !== server.devices, <UpStep label={t("users.devices")} value={lim.devices} min={1} max={100} onChange={(v) => set({ devices: v })} />)}
        {row(
          t("users.term"),
          d === null ? t("users.never") : user.expiresUnix <= now ? t("users.expiredShort") : t("users.days", { n: d }),
          false,
          <div className="flex flex-wrap gap-2">
            <Button size="md" disabled={readonly} onClick={() => void actions.extend()}>
              {t("up.plus30")}
            </Button>
            <Button size="md" disabled={readonly} onClick={() => void actions.extendBy(90)}>
              {t("up.plus90")}
            </Button>
            <Button size="md" disabled={readonly || user.expiresUnix === 0} onClick={() => void actions.update({ expiresUnix: 0 }).catch(() => {})}>
              {t("users.never")}
            </Button>
          </div>,
        )}
      </fieldset>
      <div className={cx("up-save", (busy || dirty) && "on", done && !dirty && "ok on")} role="status">
        <span>{busy ? t("up.saving") : dirty ? (lim.devices < used ? t("up.limBelow") : t("up.unsaved")) : t("up.saved")}</span>
        <Button size="md" disabled={busy} onClick={() => setDraft(null)}>
          {t("up.undo")}
        </Button>
        <Button variant="primary" size="md" disabled={busy || !dirty} onClick={() => void save()}>
          {busy && <span aria-hidden className="size-3.5 animate-spin rounded-full border-[1.5px] border-current border-t-transparent" />}
          {t("up.save")}
        </Button>
      </div>
    </section>
  );
}

// ---- DNS ----

/**
 * "Chosen on the person's page" (the board's row under the DNS): what the person picked on their page for the apps on the link (one line)
 * and per node; a pick no longer offered does nothing and the line says what applies instead. "Reset" (helper and owner) removes them all.
 */
export function DnsChoicesPanel({ user, actions }: { user: Pick<User, "id" | "name">; actions: Pick<Actions, "refresh"> }) {
  const t = useTx();
  const toast = useToast();
  const can = useCan();
  const label = useDnsLabel();
  const [open, setOpen] = useState(false);
  const q = useQuery({
    queryKey: ["users", "dns-choices", user.id],
    queryFn: ({ signal }) => dns.getUserDnsChoices({ userId: user.id }, { signal }).then((r) => plain(r)),
  });
  if (q.isError) return <p className="up-empty">{errorText(q.error, t)}</p>;
  if (!q.data) return null;
  const link = q.data.link;
  const picks = q.data.choices.length + (link ? 1 : 0);
  const lines = [
    link && `${t("subs.dnsChoice.link")}: ${label(link.presetId, link.presetName)}`,
    ...q.data.choices.map((c) => `${c.nodeName}: ${label(c.presetId, c.presetName)}`),
  ].filter(Boolean) as string[];
  const gone = [
    link && !link.offered && t("subs.dnsChoice.linkGone", { name: label(link.effectivePresetId, link.effectivePresetName) }),
    ...q.data.choices.filter((c) => !c.offered).map((c) => t("subs.dnsChoice.gone", { name: label(c.effectivePresetId, c.effectivePresetName) })),
  ].filter(Boolean) as string[];

  async function reset() {
    try {
      await dns.resetUserDnsChoices({ userId: user.id });
      await actions.refresh();
      toast(t("subs.dnsChoice.resetDone", { name: user.name }));
    } catch (e) {
      toast.error(errorText(e, t));
      throw e;
    }
  }
  return (
    <div className="up-sv-row">
      <div>
        <b>{t("subs.dnsChoice.title")}</b>
        <span>{picks === 0 ? t("up.dnsNone") : lines.join(" · ")}</span>
        {gone.map((g) => (
          <span key={g} className="up-warn">
            {g}
          </span>
        ))}
      </div>
      {can.run && (
        <Button size="md" disabled={picks === 0} onClick={() => setOpen(true)}>
          {t("subs.dnsChoice.reset")}
        </Button>
      )}
      <ConfirmModal open={open} onOpenChange={setOpen} title={t("subs.dnsChoice.resetT")} description={t("subs.dnsChoice.resetBody", { name: user.name })} confirmLabel={t("subs.dnsChoice.reset")} danger onConfirm={reset} />
    </div>
  );
}

const INHERIT = "inherit";

/** "DNS" (the board's .up-dnsc): the preset as a list with what is inside under each name, when it reaches the apps, and what the person picked themselves. */
function DnsCard({ user, actions }: { user: User; actions: Actions }) {
  const t = useTx();
  const readonly = useReadonly();
  const [open, setOpen] = useState(false);
  const presets = useQuery(dnsPresetsQuery);
  const inherited = useInheritedDns(user.groupId);
  const label = useDnsLabel();
  const apps = useLinkAppList();
  const list = presets.data?.presets ?? [];
  const catalog = useMemo(() => catalogOf(presets.data?.providers ?? []), [presets.data?.providers]);
  const sub = (p: (typeof list)[number]) => presetSubtitle(p, (s) => t("subs.dns.sub.direct", { s }), catalog, (tr) => t(transportKey[tr]));
  const inh = list.find((p) => p.id === inherited.id);
  const options = [
    { value: INHERIT, name: t(inherited.fromGroup ? "subs.dns.inheritGroup" : "subs.dns.inherit", { name: inh ? presetName(inh, t.lang) : "…" }), line: inh ? sub(inh) : "" },
    ...list.map((p) => ({ value: p.id, name: presetName(p, t.lang), line: sub(p) })),
  ];
  const shown = user.dnsPresetId && list.some((p) => p.id === user.dnsPresetId) ? user.dnsPresetId : INHERIT;
  const cur = options.find((o) => o.value === shown) ?? options[0]!;
  const applies = effectiveDnsText(t, label(user.effectiveDnsPresetId, user.effectiveDnsPresetName), user.dnsSource);
  return (
    <section className="up-card up-dnsc">
      <SetHead title={t("subs.dns.field")} line={t("up.dnsLine")} />
      <div className="up-pad">
        {presets.data ? (
          <>
            <button type="button" className="up-sel" aria-expanded={open} aria-haspopup="listbox" aria-label={t("subs.dns.field")} disabled={readonly} onClick={() => setOpen(!open)}>
              <div>
                <b>{cur.name}</b>
                <span>{cur.line}</span>
              </div>
              <G d={P.down} />
            </button>
            {open && (
              <div className="up-sell" role="radiogroup" aria-label={t("subs.dns.field")}>
                {options.map((o) => (
                  <button
                    key={o.value}
                    type="button"
                    role="radio"
                    aria-checked={o.value === shown}
                    onClick={() => {
                      setOpen(false);
                      const id = o.value === INHERIT ? "" : o.value;
                      if (id !== user.dnsPresetId) void actions.update({ dnsPresetId: id }).catch(() => {});
                    }}
                  >
                    <Rad />
                    <div>
                      <b>{o.name}</b>
                      <span>{o.line}</span>
                    </div>
                  </button>
                ))}
              </div>
            )}
          </>
        ) : (
          <div className="h-[52px] rounded-xl bg-step" />
        )}
        <p className="up-nt">
          <G d={P.check} size={14} />
          <span>{applies || t("subs.dns.inheritHint")}</span>
        </p>
        <p className="up-nt">
          <G d={P.clock} size={14} />
          <span>{apps.length > 0 ? t("subs.dns.delivery", { apps: apps.join(", ") }) : t("subs.dns.deliveryPlain")}</span>
        </p>
      </div>
      <DnsChoicesPanel user={user} actions={actions} />
    </section>
  );
}

// ---- what the person gets ----

type AppKey = "happ" | "amnezia" | "telegram" | "mistgate";

/** The group select (the board's .up-gsel): the person's group, a search from the seventh group on, and "New group" that copies the current one. */
function GroupPick({ user, onPick }: { user: User; onPick: (g: Group) => void }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const readonly = useReadonly();
  const list = useQuery(groupsQuery);
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [making, setMaking] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const all = list.data ?? [];
  const cur = all.find((g) => g.id === user.groupId);
  const shown = all.filter((g) => !q || g.name.toLowerCase().includes(q.toLowerCase()));
  const dup = all.some((g) => g.name.toLowerCase() === name.trim().toLowerCase());
  const ok = name.trim().length >= 2 && !dup;
  const sub = (g: Group) => `${t.n("up.groupPeople", g.userCount)} · ${t.n("up.groupWays", g.profileIds.length)}`;

  // a new group is a copy of this one (its ways, its DNS, its disabled nodes): the person moves into it at once, nothing changes for them
  async function create() {
    if (!ok || busy || !cur) return;
    setBusy(true);
    try {
      const r = await groupsApi.createGroup({ name: name.trim(), profileIds: cur.profileIds, dnsPresetId: cur.dnsPresetId, color: "", disabledNodeIds: cur.disabledNodeIds });
      await users.updateUser({ userId: user.id, groupId: r.group!.id });
      await qc.invalidateQueries({ queryKey: ["groups"] });
      await qc.invalidateQueries({ queryKey: ["users"] });
      toast(t("users.groupChanged", { name: user.name, group: r.group!.name }));
      setOpen(false);
      setMaking(false);
      setName("");
    } catch (e) {
      toast.error(errorText(e, t));
    } finally {
      setBusy(false);
    }
  }

  if (!list.data) return <div className="h-[52px] rounded-xl bg-step" />;
  return (
    <>
      <button type="button" className="up-sel" aria-expanded={open} aria-haspopup="listbox" aria-label={t("users.group")} disabled={readonly} onClick={() => (setOpen(!open), setQ(""), setMaking(false))}>
        <div>
          <b>
            <GroupDot id={user.groupId} color={user.groupColor} />
            {user.groupName}
          </b>
          <span>{cur ? sub(cur) : ""}</span>
        </div>
        <G d={P.down} />
      </button>
      {open && (
        <div className="up-sell up-gl2" role="radiogroup" aria-label={t("users.group")}>
          {all.length > 6 && (
            <label className="up-gq">
              <G d={P.search} size={16} />
              <input type="text" placeholder={t("up.findGroup")} aria-label={t("up.findGroup")} autoComplete="off" value={q} onChange={(e) => setQ(e.target.value)} />
            </label>
          )}
          <div className="up-gsc">
            {shown.map((g) => (
              <button
                key={g.id}
                type="button"
                role="radio"
                aria-checked={g.id === user.groupId}
                onClick={() => {
                  setOpen(false);
                  if (g.id !== user.groupId) onPick(g);
                }}
              >
                <Rad />
                <div>
                  <b>
                    <GroupDot id={g.id} color={g.color} />
                    {g.name}
                  </b>
                  <span>{sub(g)}</span>
                </div>
              </button>
            ))}
            {shown.length === 0 && <p className="up-empty">{t("up.noGroup")}</p>}
          </div>
          {!making ? (
            <button
              type="button"
              className="up-gnew"
              onClick={() => {
                setMaking(true);
                setName(shown.length === 0 ? q : "");
              }}
            >
              <span className="g" aria-hidden>
                <G d={P.plus} size={12} />
              </span>
              <div>
                <b>{q && shown.length === 0 ? t("up.newGroupNamed", { name: q }) : t("up.newGroup")}</b>
                <span>{t("up.newGroupHint", { name: user.groupName })}</span>
              </div>
            </button>
          ) : (
            <div className="up-gnf">
              <label className={cx("up-f", name && !ok && "bad")}>
                <span>{t("up.newGroupName")}</span>
                <input
                  className="up-i"
                  type="text"
                  autoFocus
                  autoComplete="off"
                  spellCheck={false}
                  placeholder={t("up.newGroupPh")}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void create();
                    if (e.key === "Escape") setMaking(false);
                  }}
                />
                <small>{dup ? t("up.groupDup") : t("up.newGroupCopy", { name: user.groupName, who: user.name })}</small>
              </label>
              <div>
                <Button size="md" onClick={() => setMaking(false)}>
                  {t("users.cancel")}
                </Button>
                <Button variant="primary" size="md" disabled={!ok || busy} onClick={() => void create()}>
                  {t("up.newGroupDo")}
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
    </>
  );
}

/** What a way gives: the profiles of the group, by the app that takes them (tags), the board's colours. */
function waysOf(profiles: ProfileRef[], protocols: ReturnType<typeof useProtocols>) {
  const isTg = (p: ProfileRef) => {
    const apps = protocols?.find((x) => x.id === p.protocol)?.apps;
    return apps ? telegramOnly(apps) : p.protocol === "mtproto";
  };
  const isTt = (p: ProfileRef) => {
    const apps = protocols?.find((x) => x.id === p.protocol)?.apps;
    return apps ? trustTunnelOnly(apps) : p.protocol === "trusttunnel";
  };
  const rest = profiles.filter((p) => !isTg(p) && !isTt(p));
  return { sub: rest.filter((p) => wayOf(p.protocol, protocols) === "sub"), awg: rest.filter((p) => wayOf(p.protocol, protocols) === "awg"), tg: profiles.filter(isTg) };
}
const useProtocols = () => useQuery(protocolsQuery).data;

/** One line of "what the person gets" (the board's .up-gt): the tile, the way, what it is, the tags of what comes through it, the switch. */
function GetRow({ app, state, icon, mark, title, line, tags, on, off, disabled, onFlip, extra }: { app: string; state?: string; icon?: string; mark?: ReactNode; title: string; line: string; tags: ProfileRef[]; on: boolean; off?: boolean; disabled?: boolean; onFlip: (on: boolean) => void; extra?: ReactNode }) {
  return (
    <div className={cx("up-gt", off && "off")} data-app={app} data-state={state}>
      <span className={cx("g", !!mark && "mg")} aria-hidden>
        {mark ?? <G d={icon!} />}
      </span>
      <div>
        <b>{title}</b>
        <span>{line}</span>
        {tags.length > 0 && (
          <span className="up-tags">
            {tags.map((p) => (
              <Link key={p.id} to="/profiles/$id" params={{ id: p.id }} className="rounded-chip hover:brightness-110">
                <ProtocolTag protocol={p.protocol} label={p.name} />
              </Link>
            ))}
          </span>
        )}
        {extra}
      </div>
      <Switch aria-label={title} checked={on} disabled={disabled} onCheckedChange={onFlip} />
    </div>
  );
}

/**
 * "What the person gets" (the board's .sv-c): the group (a new one is confirmed first with what the person loses and gets, from the
 * server's dry run), the ways as switches (the subscription apps, AmneziaVPN keys, the Telegram proxy, the Mistgate app) with what comes
 * through each, and a line on what is switched off. At least one of the link and the keys stays on. TrustTunnel has its own switch on the
 * Devices tab, beside its key.
 */
export function GivesCard({ data, actions }: { data: DetailN; actions: Actions }) {
  const t = useTx();
  const toast = useToast();
  const readonly = useReadonly();
  const linkApps = useLinkAppNames();
  const protocols = useProtocols();
  const api = useQuery(clientApiQuery);
  const user = data.user!;
  // kept after the dialog closes so its text does not go blank while it fades out
  const [changing, setChanging] = useState<{ to: Group; impact: AccessImpact | null } | null>(null);
  const [changeOpen, setChangeOpen] = useState(false);
  const [changeError, setChangeError] = useState<string | null>(null);
  const w = waysOf(data.profiles, protocols);
  const apps = { happ: !!user.apps?.happ, amnezia: !!user.apps?.amnezia, telegram: !!user.apps?.telegram, mistgate: user.apps?.mistgate !== false };

  const flip = (which: AppKey, on: boolean) => {
    const next = { ...apps, [which]: on };
    if (!next.happ && !next.amnezia) return; // at least one of the two link/key apps stays on
    void actions.update({ apps: next }).catch(() => {});
  };
  async function askChange(g: Group) {
    try {
      const r = await users.updateUser({ userId: user.id, groupId: g.id, dryRun: true });
      setChangeError(null);
      setChanging({ to: g, impact: r.impact ?? null });
      setChangeOpen(true);
    } catch (e) {
      toast.error(errorText(e, t));
    }
  }
  async function change() {
    if (!changing) return;
    setChangeError(null);
    try {
      await users.updateUser({ userId: user.id, groupId: changing.to.id });
    } catch (e) {
      setChangeError(errorText(e, t));
      throw e;
    }
    await actions.refresh();
    toast(t("users.groupChanged", { name: user.name, group: changing.to.name }));
  }
  const lost = changing?.impact?.lost.length ?? 0;
  const offN = (["happ", "amnezia", "telegram", "mistgate"] as const).filter((k) => !apps[k]).length;
  const globalOff = api.data ? !api.data.enabled : false;
  const mgLocked = !apps.happ;
  const mgShown = apps.mistgate && apps.happ;
  const drops = w.sub.filter((p) => (p as { formats?: { ok: boolean }[] }).formats?.some((f) => !f.ok));

  return (
    <section className="up-card" data-testid="gives">
      <SetHead title={t("up.givesTitle")} line={t("up.givesLine")} />
      <div className="up-pad">
        <GroupPick user={user} onPick={(g) => void askChange(g)} />
      </div>
      {data.profiles.length === 0 && <p className="up-empty">{t("users.noProfiles")}</p>}
      <GetRow app="link" icon={P.link} title={linkApps} line={t("up.gets.sub")} tags={w.sub} on={apps.happ} off={!apps.happ} disabled={readonly} onFlip={(on) => flip("happ", on)} extra={drops.length > 0 && <span className="up-drops">{drops.map((p) => <FormatDrops key={p.id} formats={(p as ProfileRef).formats} t={t} />)}</span>} />
      <GetRow app="amnezia" icon={P.key} title="AmneziaVPN" line={t("up.gets.awg")} tags={w.awg} on={apps.amnezia} off={!apps.amnezia} disabled={readonly} onFlip={(on) => flip("amnezia", on)} />
      <GetRow app="telegram" icon={P.plane} title={t("tgproxy.toggle")} line={t("up.gets.tg")} tags={w.tg} on={apps.telegram} off={!apps.telegram} disabled={readonly} onFlip={(on) => flip("telegram", on)} />
      <GetRow
        app="mistgate"
        state={mgLocked ? "locked" : apps.mistgate ? "on" : "off"}
        mark={<DefaultMark size={22} />}
        title={t("users.mistgate")}
        line={mgLocked ? t("users.mistgateNeedsLink") : !apps.mistgate ? t("users.mistgateOffHint") : t("users.mistgateHint")}
        tags={[...w.awg, ...w.sub.filter((p) => p.protocol === "hysteria2")]}
        on={mgShown}
        off={!mgShown}
        disabled={readonly || mgLocked}
        onFlip={(on) => flip("mistgate", on)}
        extra={
          globalOff && (
            <Link to="/subscriptions" search={{ tab: "rules" }} hash="client-api" title={t("users.mistgateGlobalOffTitle")} className="up-more">
              <G d={P.info} size={12} />
              {t("users.mistgateGlobalOff")}
            </Link>
          )
        }
      />
      <p className="up-nt up-ntp">
        <G d={P.info} size={14} />
        <span>
          {offN > 0 && `${t("up.gets.off", { n: offN })} `}
          {t("up.gets.note", { group: user.groupName })}
        </span>
      </p>
      <ConfirmModal
        open={changeOpen}
        onOpenChange={setChangeOpen}
        title={t("users.groupChangeT", { name: user.name, group: changing?.to.name ?? "" })}
        confirmLabel={t("users.groupChangeDo")}
        danger={lost > 0}
        onConfirm={change}
      >
        {changing?.impact ? (
          <Notice className="items-start">
            <ImpactText impact={changing.impact} who={user.name} />
          </Notice>
        ) : (
          <p className="text-[13px] leading-normal text-muted">{t("users.impactSame")}</p>
        )}
        {changeError && <Notice tone="danger">{changeError}</Notice>}
      </ConfirmModal>
    </section>
  );
}

// ---- nodes ----

/** The nodes of a person (the board's .up-nd): "All nodes" (the new ones join by themselves) and a row per node with the profiles that work there for them. */
export function NodesCard({ data, actions }: { data: DetailN; actions: Actions }) {
  const t = useTx();
  const readonly = useReadonly();
  const user = data.user!;
  const protocols = useProtocols();
  const protoName = (id: string) => protocols?.find((p) => p.id === id)?.displayName ?? id;
  const [pending, setPending] = useState<{ server: string; v: { all: boolean; ids: string[] } } | null>(null);
  const server = { all: !!user.nodes?.all, ids: [...(user.nodes?.nodeIds ?? [])].sort() };
  const sk = JSON.stringify(server);
  const nodes = pending && pending.server === sk ? pending.v : server;
  const allIds = data.nodeAccess.map((n) => n.nodeId);
  const isOn = (id: string) => nodes.all || nodes.ids.includes(id);
  const save = (v: { all: boolean; ids: string[] }) => {
    setPending({ server: sk, v });
    void actions.update({ nodes: { all: v.all, nodeIds: v.all ? [] : v.ids } }).catch(() => setPending(null));
  };
  const flipAll = () => save(nodes.all ? { all: false, ids: allIds } : { all: true, ids: nodes.ids });
  const flipNode = (id: string) => {
    const cur = nodes.all ? allIds : nodes.ids;
    save({ all: false, ids: cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id] });
  };
  const usableN = data.nodeAccess.filter((n) => n.protocols.length > 0).length;
  const selected = nodes.all ? usableN : nodes.ids.filter((id) => data.nodeAccess.find((n) => n.nodeId === id)?.protocols.length).length;
  const meta = (n: NodeAccess) => [n.location || n.countryCode, n.provider].filter(Boolean).join(" · ");
  return (
    <section className="up-card" data-testid="nodes-card">
      <SetHead title={t("up.nodesTitle")} line={nodes.all ? t("up.nodesAllLine", { a: usableN, b: allIds.length }) : t("up.nodesSomeLine", { a: selected, b: allIds.length })} />
      <div className="up-gt up-all">
        <div>
          <b>{t("users.nodesAll")}</b>
          <span>{t("up.nodesAllHint")}</span>
        </div>
        <Switch aria-label={t("users.nodesAll")} checked={nodes.all} disabled={readonly} onCheckedChange={flipAll} />
      </div>
      {data.nodeAccess.map((n) => {
        const on = isOn(n.nodeId);
        const usable = n.protocols.length > 0;
        const can = !readonly && !nodes.all && (usable || on);
        return (
          <div key={n.nodeId} className={cx("up-nd", !usable && "none", nodes.all && "lock")} onClick={() => can && flipNode(n.nodeId)}>
            <Checkbox.Root checked={on && usable} disabled={!can} aria-label={n.nodeName} className="up-cb" onClick={(e) => e.stopPropagation()} onCheckedChange={() => flipNode(n.nodeId)}>
              <i>
                <G d={P.check} size={14} stroke={2.5} />
              </i>
            </Checkbox.Root>
            <span aria-hidden className="up-fl">
              <Flag code={n.countryCode} size={14} />
            </span>
            <Link to="/nodes/$id" params={{ id: n.nodeId }} search={{ tab: "profiles" }} onClick={(e: { stopPropagation: () => void }) => e.stopPropagation()} className="font-mono font-medium hover:text-accent-text hover:underline">
              {n.nodeName}
            </Link>
            <span>{meta(n)}</span>
            <span className="up-tags">
              {n.protocols.map((p) => (
                <ProtocolTag key={p} protocol={p} label={protoName(p)} />
              ))}
              {!usable && <em>{t("up.nodeNone")}</em>}
            </span>
          </div>
        );
      })}
      {!nodes.all && allIds.length > 0 && !readonly && (
        <div className="flex gap-4 border-t border-line px-6 py-3 text-xs font-medium max-md:px-4">
          <button type="button" className="text-accent-text" onClick={() => save({ all: false, ids: allIds })}>
            {t("users.pickAll")}
          </button>
          <button type="button" className="text-muted" onClick={() => save({ all: false, ids: [] })}>
            {t("users.pickNone")}
          </button>
        </div>
      )}
    </section>
  );
}

/** The Access tab: limits and DNS on the left; what the person gets and their nodes on the right. */
export function AccessTab({ data, actions }: { data: DetailN; actions: Actions }) {
  const user = data.user!;
  return (
    <div className="up-g up-g2">
      <div className="col">
        <LimitsPanel user={user} actions={actions} />
        <DnsCard user={user} actions={actions} />
      </div>
      <div className="col">
        <GivesCard data={data} actions={actions} />
        <NodesCard data={data} actions={actions} />
      </div>
    </div>
  );
}

/** The two cards that decide what the person receives (older scenes drew them as one panel). */
export function AccessPanel({ data, actions }: { data: DetailN; actions: Actions }) {
  return (
    <div className="up-one">
      <GivesCard data={data} actions={actions} />
      <NodesCard data={data} actions={actions} />
    </div>
  );
}

