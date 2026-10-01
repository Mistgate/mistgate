import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { Code } from "@connectrpc/connect";
import type { DeviceConfig } from "@/gen/mistgate/admin/v1/device_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { QuotaReset, UserStatus, type AccessImpact, type NodeAccess, type ProfileRef } from "@/gen/mistgate/admin/v1/user_pb";
import { Card, DangerZone, PageTitle, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon, IconChip } from "@/components/ui/icons";
import { EmptyState, Notice } from "@/components/ui/notice";
import { Select } from "@/components/ui/select";
import { StatusDot } from "@/components/ui/status";
import { Infinite, Stepper } from "@/components/ui/stepper";
import { useToast } from "@/components/ui/toast";
import { users } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt, type Fmt } from "@/lib/format";
import { useLinkAppNames } from "@/screens/subscriptions/queries";
import { DAY, GB, agoText, appsText, daysLeft, nowSec, shownKey, shownKind, shownStatus, shownText } from "./format";
import { DnsSelect, effectiveDnsText, useDnsLabel, useInheritedDns } from "./dns-select";
import { AddDeviceModal, DeviceConfigsModal, platformLabel, type DeviceRef } from "./awg-devices";
import { GroupEditModal, GroupLink, ImpactText, WaysOfGroup, wayMark } from "./groups";
import { LinkModal, linkTargetOf } from "./link-modal";
import { big, detailN, type DetailN, type DeviceN as Device, type UserN as User } from "./model";
import { useGo } from "./nav";
import { groupsQuery, isCode, protocolsQuery } from "./rpc";
import { useTx, type Tx } from "./t";
import { Check, ConfirmModal, Panel, SettingRow, SwitchRow, TypeConfirmModal, useDraft } from "./ui";
import { Pending, QueryError } from "@/components/ui/query-error";

const WIDE = "w-[130px]";

/** A change to a user in plain numbers; `write` turns the 64-bit ones into bigints for the wire. */
type Patch = Partial<{
  name: string;
  groupId: string;
  quotaBytes: number;
  expiresUnix: number;
  deviceLimit: number;
  speedLimitBps: number;
  dnsPresetId: string;
  apps: { happ: boolean; amnezia: boolean };
  nodes: { all: boolean; nodeIds: string[] };
}>;
const write = ({ quotaBytes, expiresUnix, speedLimitBps, ...rest }: Patch) => ({
  ...rest,
  ...(quotaBytes !== undefined && { quotaBytes: big(quotaBytes) }),
  ...(expiresUnix !== undefined && { expiresUnix: big(expiresUnix) }),
  ...(speedLimitBps !== undefined && { speedLimitBps: big(speedLimitBps) }),
});

export function UserScreen() {
  const { id = "" } = useParams({ strict: false }) as { id?: string };
  const t = useTx();
  const go = useGo();
  const q = useQuery({
    queryKey: ["users", "detail", id],
    queryFn: ({ signal }) => users.getUser({ userId: id }, { signal }).then(detailN),
    refetchInterval: 10_000,
    enabled: id !== "",
  });

  if (q.isPending) return <Pending />;
  if (q.isError) {
    if (isCode(q.error, Code.NotFound)) {
      return (
        <EmptyState
          title={t("users.notFound")}
          action={
            <Button variant="secondary" size="lg" onClick={() => go("/users")}>
              {t("users.back")}
            </Button>
          }
        >
          {t("users.notFoundBody")}
        </EmptyState>
      );
    }
    return <QueryError error={q.error} onRetry={() => void q.refetch()} />;
  }
  // keyed by user: going from one user to another (the palette does) must not carry unsaved drafts over
  return <UserDetail key={q.data.user.id} data={q.data} />;
}

/** Everything that changes a user goes through here: one call, then every cached list and detail refreshes. */
function useUserActions(user: User) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: ["users"] });
  /** A save that shows the server's reason on failure and rejects, so a draft control can drop its unsaved value. */
  async function update(patch: Patch) {
    try {
      await users.updateUser({ userId: user.id, ...write(patch) });
      await refresh();
    } catch (e) {
      toast.error(errorText(e, t));
      throw e;
    }
  }
  async function run(fn: () => Promise<unknown>, done?: string) {
    try {
      await fn();
      await refresh();
      if (done) toast(done);
    } catch (e) {
      toast.error(errorText(e, t));
    }
  }
  return {
    update,
    refresh,
    // disabling cuts the person off at once, so it can be undone for 8 s, as in the list's bulk bar
    setEnabled: async (enabled: boolean) => {
      try {
        await users.setUsersEnabled({ userIds: [user.id], enabled });
        await refresh();
        if (enabled) toast(t("users.enabledToast", { names: user.name }));
        else toast(t("users.disabledOne", { name: user.name }), { undo: () => void users.setUsersEnabled({ userIds: [user.id], enabled: true }).then(refresh) });
      } catch (e) {
        toast.error(errorText(e, t));
      }
    },
    extend: () => run(() => users.extendUsers({ userIds: [user.id], days: 30 }), t("users.extended", { names: user.name })),
    revoke: async (deviceId: string, done?: string) => {
      try {
        await users.revokeDevice({ deviceId });
        await refresh();
        toast(done ?? t("users.revoked"));
      } catch (e) {
        toast.error(errorText(e, t));
        throw e;
      }
    },
    resetTraffic: async () => {
      try {
        await users.resetUserTraffic({ userIds: [user.id] });
        await refresh();
        toast(t("users.resetTrafficDone", { name: user.name }));
      } catch (e) {
        toast.error(errorText(e, t));
        throw e;
      }
    },
    remove: async () => {
      try {
        await users.deleteUsers({ userIds: [user.id] });
        await refresh();
        toast(t("users.deleted", { name: user.name }));
      } catch (e) {
        toast.error(errorText(e, t));
        throw e;
      }
    },
  };
}

function UserDetail({ data }: { data: DetailN }) {
  const user = data.user!;
  const t = useTx();
  const fmt = useFmt();
  const go = useGo();
  const navigate = useNavigate();
  const actions = useUserActions(user);
  const [linkOpen, setLinkOpen] = useState(false);
  const [groupOpen, setGroupOpen] = useState(false);
  const groupList = useQuery(groupsQuery);
  const group = groupList.data?.find((g) => g.id === user.groupId);
  // "?add=device" (the link window, self-service off): open "Add device" once, then drop it from the address
  const addDevice = (useSearch({ strict: false }) as { add?: string }).add === "device";
  const now = nowSec();
  const shown = shownStatus(user);
  const disabled = user.status === UserStatus.DISABLED;
  const d = daysLeft(user.expiresUnix, now);
  const dateText = new Intl.DateTimeFormat(t.lang, { day: "numeric", month: "long", ...(new Date(user.expiresUnix * 1000).getFullYear() !== new Date(now * 1000).getFullYear() && { year: "numeric" }) }).format(new Date(user.expiresUnix * 1000));
  const term = d === null ? t("users.never") : user.expiresUnix <= now ? t("users.status.expired") : `${t("users.until")} ${dateText} · ${t("users.days", { n: d })}`;
  const meta = [appsText(user, t), user.groupName, term].filter(Boolean).join(" · ");

  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex flex-wrap items-start gap-3">
        <IconButton aria-label={t("users.back")} className="mt-0.5" onClick={() => go("/users")}>
          <Icon name="back" />
        </IconButton>
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <PageTitle className="leading-none break-words">{user.name}</PageTitle>
            <span className={cx("flex items-center gap-1.5 text-[13px] font-semibold", shownText[shown])}>
              <StatusDot kind={shownKind[shown]} />
              {t(shownKey[shown])}
            </span>
          </div>
          <span className="text-[13px] text-muted">{meta}</span>
        </div>
        <div className="flex w-full flex-wrap gap-1.5 md:w-auto">
          <Button variant={disabled ? "ghost" : "ghostDanger"} onClick={() => void actions.setEnabled(disabled)}>
            {t(disabled ? "users.enable" : "users.disable")}
          </Button>
          <Button variant="secondary" disabled={user.expiresUnix === 0} onClick={() => void actions.extend()}>
            {t("users.extend30")}
          </Button>
          <Button variant="primary" onClick={() => setLinkOpen(true)}>
            {t("users.linkQr")}
          </Button>
        </div>
      </div>

      <AlertStrip user={user} now={now} fmt={fmt} t={t} actions={actions} />

      <div className="grid items-start gap-3.5 md:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)]">
        <div className="flex min-w-0 flex-col gap-3.5">
          <TrafficPanel data={data} fmt={fmt} />
          <DevicesPanel
            user={user}
            devices={data.devices}
            profiles={data.profiles}
            actions={actions}
            onEditGroup={() => setGroupOpen(true)}
            autoAdd={addDevice}
            onAutoAddDone={() => void navigate({ to: "/users/$id", params: { id: user.id }, search: {}, replace: true } as never)}
          />
        </div>
        <div className="flex min-w-0 flex-col gap-3.5">
          <LimitsPanel user={user} actions={actions} />
          <AccessPanel data={data} actions={actions} />
          <DangerZone title={t("users.danger")}>
            <SettingRow label={t("users.resetTrafficT")} hint={t("users.resetTrafficHint")}>
              <ResetTrafficButton user={user} actions={actions} />
            </SettingRow>
            <SettingRow label={t("users.deleteT")} hint={t("users.deleteHint")}>
              <DeleteButton user={user} actions={actions} />
            </SettingRow>
          </DangerZone>
        </div>
      </div>

      <LinkModal target={linkOpen ? linkTargetOf(user, group) : null} onClose={() => setLinkOpen(false)} />
      <GroupEditModal group={group} open={groupOpen} onOpenChange={setGroupOpen} />
    </div>
  );
}

type Actions = ReturnType<typeof useUserActions>;

function ResetTrafficButton({ user, actions }: { user: User; actions: Actions }) {
  const t = useTx();
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant="secondary" disabled={user.usedBytes === 0} onClick={() => setOpen(true)}>
        {t("users.resetTraffic")}
      </Button>
      <ConfirmModal
        open={open}
        onOpenChange={setOpen}
        title={t("users.resetTrafficT")}
        description={t("users.resetTrafficBody", { name: user.name })}
        confirmLabel={t("users.resetTraffic")}
        danger
        onConfirm={() => actions.resetTraffic()}
      />
    </>
  );
}

function DeleteButton({ user, actions }: { user: User; actions: Actions }) {
  const t = useTx();
  const go = useGo();
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant="danger" onClick={() => setOpen(true)}>
        {t("users.delete")}
      </Button>
      <TypeConfirmModal
        open={open}
        onOpenChange={setOpen}
        match={user.name}
        title={t("users.deleteT")}
        description={t("users.deleteBody", { name: user.name })}
        confirmLabel={t("users.delete")}
        onConfirm={async () => {
          await actions.remove();
          go("/users", { replace: true });
        }}
      />
    </>
  );
}

// ---- alert strip ----

function AlertStrip({ user, now, fmt, t, actions }: { user: User; now: number; fmt: Fmt; t: Tx; actions: Actions }) {
  let text = "";
  let button = "";
  let onClick: () => void = () => {};
  let tone: "danger" | "warn" = "warn";
  if (user.status === UserStatus.EXPIRED) {
    const days = Math.floor((now - user.expiresUnix) / DAY);
    text = days < 1 ? t("users.alert.expiredToday") : t.n("users.alert.expired", days);
    button = t("users.extend30");
    onClick = () => void actions.extend();
    tone = "danger";
  } else if (user.status === UserStatus.LIMITED) {
    text = t("users.alert.quota", { q: fmt.bytes(user.quotaBytes) });
    button = t("users.alert.quotaBtn");
    onClick = () => void actions.update({ quotaBytes: user.quotaBytes + 50 * GB }).catch(() => {});
  } else if (shownStatus(user) === "devices") {
    text = t.n("users.alert.devices", user.deviceLimit);
    button = t("users.alert.devicesBtn");
    onClick = () => void actions.update({ deviceLimit: user.deviceLimit + 1 }).catch(() => {});
  }
  if (!text) return null;
  return (
    <div
      role="status"
      className={cx(
        "screen-enter flex flex-wrap items-center gap-3 rounded-field border bg-surface px-4 py-3",
        tone === "danger" ? "border-[color-mix(in_oklch,var(--danger)_45%,var(--border))]" : "border-[color-mix(in_oklch,var(--warning)_45%,var(--border))]",
      )}
    >
      <span className="min-w-[200px] flex-1 text-[13px] text-pretty">{text}</span>
      <Button onClick={onClick}>{button}</Button>
    </div>
  );
}

// ---- traffic ----

const resetKey = {
  [QuotaReset.UNSPECIFIED]: "users.reset.month",
  [QuotaReset.NONE]: "users.reset.none",
  [QuotaReset.DAY]: "users.reset.day",
  [QuotaReset.WEEK]: "users.reset.week",
  [QuotaReset.MONTH]: "users.reset.month",
  [QuotaReset.ROLLING_MONTH]: "users.reset.rolling",
} as const;

function TrafficPanel({ data, fmt }: { data: DetailN; fmt: Fmt }) {
  const t = useTx();
  const user = data.user!;
  const protocols = useQuery(protocolsQuery);
  const protoName = (id: string) => protocols.data?.find((p) => p.id === id)?.displayName ?? id;
  const pct = user.quotaBytes > 0 ? user.usedBytes / user.quotaBytes : 0;
  const tone = pct > 1 ? "bg-danger" : pct >= 0.8 ? "bg-warn" : "bg-accent";
  const days = data.dailyTraffic;
  const max = Math.max(0, ...days.map((x) => x.bytes));

  return (
    <Panel title={t("users.traffic")} icon="traffic" tone="sky" aside={<span className="text-xs text-muted">{t(resetKey[user.quotaReset])}</span>}>
      <div className="flex items-baseline gap-2">
        <span className="font-mono text-2xl font-bold tracking-[-0.04em]">{fmt.bytes(user.usedBytes)}</span>
        <span className="text-[13px] text-muted">{user.quotaBytes > 0 ? t("users.ofQuota", { q: fmt.bytes(user.quotaBytes) }) : <Infinite label={t("users.unlimited")} />}</span>
      </div>
      <div className="h-1 rounded-sm bg-surface-2">
        <div className={cx("h-full rounded-sm", tone)} style={{ width: `${Math.min(100, pct * 100).toFixed(1)}%` }} />
      </div>
      <div role="img" aria-label={t("users.chartLabel")} className="flex h-16 items-end gap-1 pt-1.5">
        {days.map((x, i) => (
          <span
            key={x.dayUnix}
            title={`${fmt.date(x.dayUnix)}: ${fmt.bytes(x.bytes)}`}
            className={cx("flex-1 rounded-t-sm", i === days.length - 1 ? "bg-accent" : "bg-[color-mix(in_oklch,var(--accent)_45%,var(--surface-2))]", x.bytes === 0 && "opacity-40")}
            style={{ height: `${max > 0 ? Math.max(4, Math.round((x.bytes / max) * 100)) : 4}%` }}
          />
        ))}
      </div>
      <div className="flex justify-between font-mono text-[10px] text-faint">
        <span>{t("users.d14")}</span>
        <span>{t("users.today")}</span>
      </div>
      {data.nodeTraffic.length > 0 && (
        <div className="flex flex-col">
          {data.nodeTraffic.map((r) => (
            <div key={r.nodeId + r.protocol} className="flex h-[34px] items-center gap-2.5 border-t border-line text-[13px]">
              <Link to="/nodes/$id" params={{ id: r.nodeId }} className="min-w-10 font-bold hover:text-accent-text hover:underline">
                {r.nodeName}
              </Link>
              <span className="flex-1 text-muted">{protoName(r.protocol)}</span>
              <span className="font-mono text-xs">{fmt.bytes(r.bytes)}</span>
            </div>
          ))}
        </div>
      )}
    </Panel>
  );
}

// ---- devices ----

/** A small header inside a panel: the tinted mark of the way, its name, a quiet line and an action on the right. */
function WayHead({ way, title, hint, action }: { way: "sub" | "awg"; title: string; hint?: string; action?: ReactNode }) {
  return (
    <div className="flex items-start gap-2.5 border-t border-line pt-3">
      <IconChip icon={wayMark[way].icon} tone={wayMark[way].tone} size={24} />
      {/* the title has a real basis: on a phone the action wraps under it, in line with the rows, instead of squeezing it */}
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2.5 gap-y-1.5">
        <div className="flex min-h-6 min-w-0 flex-[1_1_10rem] flex-col justify-center">
          <span className="text-[13px] font-bold">{title}</span>
          {hint && <span className="text-[11px] leading-snug text-muted">{hint}</span>}
        </div>
        {action}
      </div>
    </div>
  );
}

/**
 * The person's devices, in the two ways of the page. "Through the subscription": the apps on the link,
 * one row for all of them. "AmneziaVPN keys": a key per device, with its profile, when it last connected, the key itself
 * and "Delete". A way the person does not have and has nothing in stays out.
 */
export function DevicesPanel({
  user,
  devices,
  profiles,
  actions,
  onEditGroup,
  autoAdd,
  onAutoAddDone,
}: {
  user: User;
  devices: Device[];
  profiles: ProfileRef[];
  actions: Actions;
  onEditGroup: () => void;
  /** Open "Add device" on mount (the link window sends the owner here). */
  autoAdd?: boolean;
  onAutoAddDone?: () => void;
}) {
  const t = useTx();
  const linkApps = useLinkAppNames();
  // the device stays set while the dialog fades out, so its text does not go blank
  const [asking, setAsking] = useState<Device | null>(null);
  const [askOpen, setAskOpen] = useState(false);
  const [opened, setOpened] = useState<{ device: DeviceRef; initial?: DeviceConfig[] } | null>(null);
  const full = user.deviceLimit > 0 && user.devicesUsed >= user.deviceLimit;
  const awgProfiles = profiles.filter((p) => p.protocol === "awg");
  const canAdd = !!user.apps?.amnezia && awgProfiles.length > 0;
  const [adding, setAdding] = useState(!!autoAdd && canAdd && !full);
  const now = nowSec();
  const sub = devices.filter((d) => d.awgProfileId === "");
  const keys = devices.filter((d) => d.awgProfileId !== "");
  const showSub = !!user.apps?.happ || sub.length > 0;
  const showKeys = !!user.apps?.amnezia || keys.length > 0;
  const nameOf = (d: Device) => d.model || platformLabel(d.platform, t);
  const refOf = (d: Device): DeviceRef => ({ id: d.id, label: d.model, platform: d.platform, profileName: d.awgProfileName, version: d.awgVersion, address: d.address, stale: d.stale });
  const ask = (d: Device) => {
    setAsking(d);
    setAskOpen(true);
  };
  const askingSub = asking !== null && asking.awgProfileId === "";

  return (
    <Panel
      title={t("users.devicesT")}
      icon="phone"
      tone="mint"
      aside={
        <span className={cx("font-mono text-xs", full ? "text-warn-text" : "text-muted")}>
          {user.devicesUsed} / {user.deviceLimit}
        </span>
      }
      className="pb-2"
    >
      {!showSub && !showKeys && <p className="border-t border-line pt-3.5 pb-1 text-[13px] text-muted">{t("users.noDevices")}</p>}

      {showSub && (
        <section className="flex flex-col">
          <WayHead way="sub" title={t("users.devSub")} />
          {sub.length === 0 ? (
            <p className="py-2.5 pl-[34px] text-xs text-muted">{t("users.devSubNever")}</p>
          ) : (
            sub.map((d) => (
              <div key={d.id} className="flex min-h-[52px] items-center gap-3 py-1.5 pl-[34px]">
                <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <span className="flex items-center gap-2 text-[13px] font-bold">
                    <StatusDot kind={d.online ? "ok" : "off"} />
                    {t("users.devSubApps")}
                  </span>
                  <span className="text-xs leading-snug text-muted">
                    {t("users.devSubHint", { apps: linkApps })} · {d.online ? t("users.devKeyOnline") : d.lastSeenUnix > 0 ? t("users.devSubSeen", { when: agoText(d.lastSeenUnix, t, now) }) : t("users.devSubNever")}
                  </span>
                </div>
                <Button variant="ghost" size="sm" onClick={() => ask(d)}>
                  {t("users.devSubOff")}
                </Button>
              </div>
            ))
          )}
        </section>
      )}

      {showKeys && (
        <section className="flex flex-col">
          <WayHead
            way="awg"
            title={t("users.devKeys")}
            hint={full && canAdd ? t("awg.dev.full") : t("users.devKeysHint")}
            action={
              canAdd && (
                <Button variant="secondary" size="sm" disabled={full} onClick={() => setAdding(true)}>
                  <Icon name="plus" size={12} />
                  {t("awg.dev.add")}
                </Button>
              )
            }
          />
          {!!user.apps?.amnezia && awgProfiles.length === 0 && (
            <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 py-2 pl-[34px]">
              <span className="min-w-0 flex-1 text-[11px] leading-snug text-muted">{t("users.awgMissing", { name: user.groupName })}</span>
              <button type="button" onClick={onEditGroup} className="text-xs font-bold text-accent-text transition-colors hover:text-fg">
                {t("users.groupEdit")}
              </button>
            </div>
          )}
          {keys.length === 0 ? (
            <p className="py-2.5 pl-[34px] text-xs text-muted">{t("users.devKeysNone")}</p>
          ) : (
            keys.map((d) => (
              <div key={d.id} className="flex min-h-[52px] flex-wrap items-center gap-x-3 gap-y-1 py-1.5 pl-[34px]">
                <div className="flex min-w-0 flex-[1_1_200px] flex-col gap-0.5">
                  <span className="flex min-w-0 items-center gap-2 text-[13px] font-bold">
                    <StatusDot kind={d.online ? "ok" : "off"} />
                    <span className="truncate">{nameOf(d)}</span>
                    {d.stale && <span className="flex-none rounded-md border border-warn-line bg-warn-soft px-1.5 py-px text-[10px] font-extrabold text-warn-text">{t("awg.dev.stale")}</span>}
                  </span>
                  <span className="text-xs leading-snug break-words text-muted">
                    {platformLabel(d.platform, t)} ·{" "}
                    <Link to="/profiles/$id" params={{ id: d.awgProfileId }} className="font-semibold text-fg hover:text-accent-text hover:underline">
                      {d.awgProfileName}
                    </Link>{" "}
                    · {d.online ? t("users.devKeyOnline") : d.lastHandshakeUnix > 0 ? t("users.devKeySeen", { when: agoText(d.lastHandshakeUnix, t, now) }) : t("users.devKeyNever")}
                  </span>
                </div>
                <span className="flex gap-1">
                  <Button variant="secondary" size="sm" onClick={() => setOpened({ device: refOf(d) })}>
                    {t("users.devKeyShow")}
                  </Button>
                  <Button variant="ghostDanger" size="sm" onClick={() => ask(d)}>
                    {t("users.revoke")}
                  </Button>
                </span>
              </div>
            ))
          )}
        </section>
      )}

      <ConfirmModal
        open={askOpen}
        onOpenChange={setAskOpen}
        title={askingSub ? t("users.devSubOffT") : t("users.revokeT")}
        description={askingSub ? t("users.devSubOffBody", { apps: linkApps }) : t("users.revokeBody", { model: asking ? nameOf(asking) : "" })}
        confirmLabel={askingSub ? t("users.devSubOff") : t("users.revoke")}
        danger
        onConfirm={() => actions.revoke(asking!.id, askingSub ? t("users.devSubOffDone") : undefined)}
      />
      <AddDeviceModal
        open={adding}
        onOpenChange={(o) => {
          setAdding(o);
          if (!o) onAutoAddDone?.();
        }}
        userId={user.id}
        userName={user.name}
        profiles={awgProfiles}
        onCreated={(device, configs) => {
          void actions.refresh();
          setOpened({ device, initial: configs });
        }}
      />
      <DeviceConfigsModal device={opened?.device ?? null} initial={opened?.initial} onClose={() => setOpened(null)} onChanged={() => void actions.refresh()} />
    </Panel>
  );
}

// ---- limits ----

function LimitsPanel({ user, actions }: { user: User; actions: Actions }) {
  const t = useTx();
  const noop = () => {};
  const now = nowSec();

  const serverGb = user.quotaBytes === 0 ? 0 : Math.max(1, Math.round(user.quotaBytes / GB));
  const [gb, setGb] = useDraft(serverGb, (v) => actions.update({ quotaBytes: v * GB }));

  // The term stepper moves the expiry date by whole days from where it is, so a click on "+" adds exactly 5 days.
  const serverDays = daysLeft(user.expiresUnix, now);
  const [days, setDays] = useDraft<number | null>(serverDays, (v) => {
    if (v === null) return actions.update({ expiresUnix: 0 });
    const from = user.expiresUnix === 0 ? now : user.expiresUnix;
    return actions.update({ expiresUnix: from + (v - (serverDays ?? 0)) * DAY });
  });

  const [devices, setDevices] = useDraft(user.deviceLimit, (v) => actions.update({ deviceLimit: v }));
  const [speed, setSpeed] = useDraft(user.speedLimitBps, (v) => actions.update({ speedLimitBps: v }));
  const mbit = Math.round(speed / 1e6);

  const stepper = (what: string, text: ReactNode, value: number, set: (v: number) => void, by: number, min: number, max: number) => (
    <Stepper
      decrementLabel={t("users.less", { what })}
      incrementLabel={t("users.more", { what })}
      onDecrement={() => set(Math.max(min, value - by))}
      onIncrement={() => set(Math.min(max, value + by))}
      decrementDisabled={value <= min}
      incrementDisabled={value >= max}
    >
      {text}
    </Stepper>
  );

  return (
    <Panel title={t("users.limits")} icon="gauge" tone="sand" className="pb-1">
      <div className="-mt-1 flex flex-col">
        <SettingRow label={t("users.quota")}>
          <div className={WIDE}>{stepper(t("users.quota"), gb === 0 ? <Infinite label={t("users.unlimited")} /> : `${gb} ${t("users.unitGb")}`, gb, setGb, 10, 0, 10000)}</div>
        </SettingRow>
        <SettingRow label={t("users.term")}>
          <div className={WIDE}>
            {days === null ? (
              <Stepper decrementLabel={t("users.less", { what: t("users.term") })} incrementLabel={t("users.more", { what: t("users.term") })} onDecrement={() => setDays(30)} onIncrement={noop} incrementDisabled>
                <Infinite label={t("users.never")} />
              </Stepper>
            ) : (
              stepper(t("users.term"), days <= 0 ? t("users.expiredShort") : `${days} ${t("users.unitD")}`, days, setDays, 5, 0, 3650)
            )}
          </div>
        </SettingRow>
        <SettingRow label={t("users.devices")}>
          <div className={WIDE}>{stepper(t("users.devices"), String(devices), devices, setDevices, 1, 1, 100)}</div>
        </SettingRow>
        <SwitchRow
          label={t("users.speed")}
          hint={speed > 0 ? t("users.speedHint", { n: mbit }) : t("users.speedHintOff")}
          checked={speed > 0}
          onCheckedChange={(on) => setSpeed(on ? 20e6 : 0)}
        />
        {speed > 0 && (
          <SettingRow label={t("users.speed")}>
            <div className={WIDE}>{stepper(t("users.speed"), `${mbit} ${t("users.unitMbit")}`, mbit, (v) => setSpeed(v * 1e6), 5, 5, 10000)}</div>
          </SettingRow>
        )}
      </div>
    </Panel>
  );
}

// ---- access ----

/** `text` with the `{slot}` placeholder replaced by a node (a link inside a sentence). */
function withNode(text: string, slot: string, node: ReactNode): ReactNode {
  const i = text.indexOf(`{${slot}}`);
  if (i < 0) return text;
  return (
    <>
      {text.slice(0, i)}
      {node}
      {text.slice(i + slot.length + 2)}
    </>
  );
}

/**
 * "Access": the group (a new one is confirmed first with what the person loses and gets, from the server's dry run), the
 * group's profiles in the two ways (each a link to its page), the DNS preset, the app switches and the nodes (each a
 * link to its Profiles tab, where a node's own port and domain live).
 */
function AccessPanel({ data, actions }: { data: DetailN; actions: Actions }) {
  const t = useTx();
  const linkApps = useLinkAppNames();
  const toast = useToast();
  const user = data.user!;
  const [open, setOpen] = useState(true);
  const groupList = useQuery(groupsQuery);
  const protocols = useQuery(protocolsQuery);
  const protoName = (id: string) => protocols.data?.find((p) => p.id === id)?.displayName ?? id;
  const multi = (protocols.data?.length ?? 0) > 1;
  const inheritedDns = useInheritedDns(user.groupId);
  const dnsLabel = useDnsLabel();
  // kept after the dialog closes so its text does not go blank while it fades out
  const [changing, setChanging] = useState<{ to: Group; impact: AccessImpact | null } | null>(null);
  const [changeOpen, setChangeOpen] = useState(false);
  const [changeError, setChangeError] = useState<string | null>(null);

  const serverNodes = { all: !!user.nodes?.all, ids: [...(user.nodes?.nodeIds ?? [])].sort() };
  const [nodes, setNodes] = useDraft(serverNodes, (v) => actions.update({ nodes: { all: v.all, nodeIds: v.all ? [] : v.ids } }));
  const allIds = data.nodeAccess.map((n) => n.nodeId);
  const isOn = (id: string) => nodes.all || nodes.ids.includes(id);
  const flipAll = () => setNodes(nodes.all ? { all: false, ids: allIds } : { all: true, ids: nodes.ids });
  const flipNode = (id: string) => {
    const cur = nodes.all ? allIds : nodes.ids;
    setNodes({ all: false, ids: cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id] });
  };
  const selected = nodes.all ? allIds.length : nodes.ids.filter((id) => allIds.includes(id)).length;

  const flipApp = (which: "happ" | "amnezia", on: boolean) => {
    const apps = { happ: !!user.apps?.happ, amnezia: !!user.apps?.amnezia, [which]: on };
    if (!apps.happ && !apps.amnezia) return; // at least one app stays on
    void actions.update({ apps }).catch(() => {});
  };

  /** A new group in the select: ask the server what it does first, then confirm. */
  async function askChange(id: string) {
    const to = groupList.data?.find((g) => g.id === id);
    if (!to || id === user.groupId) return;
    try {
      const r = await users.updateUser({ userId: user.id, groupId: id, dryRun: true });
      setChangeError(null);
      setChanging({ to, impact: r.impact ?? null });
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

  const nodeMeta = (n: NodeAccess) => [n.location || n.countryCode, n.provider].filter(Boolean).join(" · ");
  const nodeNote = (n: NodeAccess) =>
    n.protocols.length === 0 ? t("users.nodeNothing") : multi && n.nodeProtocols.length === 1 ? t("users.onlyProto", { p: protoName(n.nodeProtocols[0]!) }) : "";
  const lost = changing?.impact?.lost.length ?? 0;

  return (
    <Card lg className="flex min-w-0 flex-col gap-3 p-4">
      <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className="flex w-full items-center gap-2.5 text-left">
        <SectionLabel as="span" className="flex-1" icon="key" tone="lavender">
          {t("users.access")}
        </SectionLabel>
        <Icon name="chevronRight" size={14} className={cx("text-muted transition-transform duration-300 ease-spring", open && "rotate-90")} />
      </button>
      {open && (
        <div className="flex flex-col gap-3">
          <SettingRow label={t("users.group")}>
            <div className="w-[170px] max-w-[50vw] [&>button>span:first-child]:min-w-0 [&>button>span:first-child]:truncate">
              {/* until the groups are here the select would show the raw id, so a blank box holds the place */}
              {groupList.data ? (
                <Select aria-label={t("users.group")} value={user.groupId} onValueChange={(v) => void askChange(v)} options={groupList.data.map((g) => ({ value: g.id, label: g.name }))} />
              ) : (
                <div className="h-11 rounded-field bg-surface-2" />
              )}
            </div>
          </SettingRow>
          {data.profiles.length === 0 ? <span className="text-xs text-muted">{t("users.noProfiles")}</span> : <WaysOfGroup profiles={data.profiles} compact />}
          <p className="text-xs leading-normal text-pretty text-muted">
            {withNode(t("users.accessHint"), "group", <GroupLink id={user.groupId} name={user.groupName}>«{user.groupName}»</GroupLink>)}
          </p>

          <div className="flex flex-col gap-2 border-t border-line pt-3">
            <span className="text-[13px] font-bold">{t("subs.dns.field")}</span>
            <DnsSelect value={user.dnsPresetId} inherited={inheritedDns} onChange={(id) => id !== user.dnsPresetId && void actions.update({ dnsPresetId: id }).catch(() => {})} />
            <span className="text-[11px] leading-snug text-muted">{effectiveDnsText(t, dnsLabel(user.effectiveDnsPresetId, user.effectiveDnsPresetName), user.dnsSource) || t("subs.dns.inheritHint")}</span>
          </div>

          <div className="flex flex-col border-t border-line pt-1">
            <SwitchRow label={linkApps} hint={t("users.happHint")} checked={!!user.apps?.happ} onCheckedChange={(on) => flipApp("happ", on)} />
            <SwitchRow label="AmneziaVPN" hint={t("users.awgHint")} checked={!!user.apps?.amnezia} onCheckedChange={(on) => flipApp("amnezia", on)} />
          </div>

          <div className="flex flex-col border-t border-line pt-1">
            <SwitchRow label={t("users.nodesAll")} hint={nodes.all ? t("users.nodesAllHint") : t("users.nodesSelHint", { a: selected, b: allIds.length })} checked={nodes.all} onCheckedChange={flipAll} className="min-h-[52px] first:border-t-0" />
            {data.nodeAccess.map((n) => {
              const on = isOn(n.nodeId);
              const usable = n.protocols.length > 0;
              const canToggle = usable || on;
              return (
                <div
                  key={n.nodeId}
                  className={cx("flex min-h-10 items-center gap-2.5 border-t border-line", canToggle ? "cursor-pointer" : "cursor-default", !usable ? "opacity-40" : nodes.all ? "opacity-70" : undefined)}
                  onClick={() => canToggle && flipNode(n.nodeId)}
                >
                  <Check checked={on} onCheckedChange={() => canToggle && flipNode(n.nodeId)} label={n.nodeName} disabled={!canToggle} />
                  <Link
                    to="/nodes/$id"
                    params={{ id: n.nodeId }}
                    search={{ tab: "profiles" }}
                    onClick={(e: { stopPropagation: () => void }) => e.stopPropagation()}
                    className="min-w-[34px] font-mono text-xs font-bold hover:text-accent-text hover:underline"
                  >
                    {n.nodeName}
                  </Link>
                  <span className="min-w-0 flex-1 truncate text-xs text-muted">{nodeMeta(n)}</span>
                  <span className="text-[11px] text-muted">{nodeNote(n)}</span>
                </div>
              );
            })}
            {!nodes.all && allIds.length > 0 && (
              <div className="flex gap-4 border-t border-line pt-2.5 text-xs font-bold">
                <button type="button" className="text-accent-text" onClick={() => setNodes({ all: false, ids: allIds })}>
                  {t("users.pickAll")}
                </button>
                <button type="button" className="text-muted" onClick={() => setNodes({ all: false, ids: [] })}>
                  {t("users.pickNone")}
                </button>
              </div>
            )}
          </div>
        </div>
      )}
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
    </Card>
  );
}
