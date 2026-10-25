import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { Code } from "@connectrpc/connect";
import type { DeviceConfig } from "@/gen/mistgate/admin/v1/device_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { LinkDeviceLimit } from "@/gen/mistgate/admin/v1/subscription_pb";
import { DeviceKind, QuotaReset, UserStatus, type AccessImpact, type NodeAccess, type ProfileRef } from "@/gen/mistgate/admin/v1/user_pb";
import { Card, DangerZone, PageTitle, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Pagination } from "@/components/ui/pagination";
import { EmptyState, Notice } from "@/components/ui/notice";
import { Select } from "@/components/ui/select";
import { StatusDot, kindTextClass } from "@/components/ui/status";
import { TextField } from "@/components/ui/text-field";
import { Infinite, Stepper } from "@/components/ui/stepper";
import { useToast } from "@/components/ui/toast";
import { dns, users } from "@/lib/api";
import { deviceConnParts, userConnectionQuery, type Part, type UserConnection } from "@/lib/connections";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { clampPage } from "@/lib/paging";
import { useFmt, type Fmt } from "@/lib/format";
import { useT } from "@/i18n";
import { alertIsAbout, alertTitle, alertWhy, alertsQuery, severityKind, severityWord, useCan } from "@/lib/health";
import { plain } from "@/lib/plain";
import { settingsQuery, useLinkAppNames } from "@/screens/subscriptions/queries";
import { DAY, GB, appsText, daysLeft, nowSec, shownKey, shownKind, shownStatus, shownText } from "./format";
import { DnsSelect, effectiveDnsText, useDnsLabel, useInheritedDns } from "./dns-select";
import { AddDeviceModal, DeviceConfigsModal, platformLabel, type DeviceRef } from "./awg-devices";
import { GroupChip, GroupEditModal, GroupLink, ImpactText, WaysOfGroup } from "./groups";
import { BlockedList, DeviceRow, LinkDeviceRows, RowMenu, SectionHead, Secondary, Seen, SharedRow, splitDevices, Tile } from "./link-devices";
import { LinkModal, linkTargetOf } from "./link-modal";
import { big, detailN, type BlockedN, type DetailN, type DeviceN as Device, type UserN as User } from "./model";
import { useGo } from "./nav";
import { groupsQuery, isCode, protocolsQuery } from "./rpc";
import { useTx, type Tx } from "./t";
import { Check, ConfirmModal, Panel, ResetSelect, SettingRow, SpeedPair, SwitchRow, TypeConfirmModal, useDraft } from "./ui";
import { Pending, QueryError } from "@/components/ui/query-error";

// the steppers of the Limits panel, wide enough for "50 Мбит/с" (the speed pair) so they all line up
const WIDE = "w-[150px]";

/** A change to a user in plain numbers; `write` turns the 64-bit ones into bigints for the wire. */
type Patch = Partial<{
  name: string;
  subscriptionName: string;
  groupId: string;
  quotaBytes: number;
  quotaReset: QuotaReset;
  expiresUnix: number;
  deviceLimit: number;
  speedLimitBps: number;
  deviceSpeedLimitBps: number;
  dnsPresetId: string;
  apps: { happ: boolean; amnezia: boolean };
  nodes: { all: boolean; nodeIds: string[] };
}>;
const write = ({ quotaBytes, expiresUnix, speedLimitBps, deviceSpeedLimitBps, ...rest }: Patch) => ({
  ...rest,
  ...(quotaBytes !== undefined && { quotaBytes: big(quotaBytes) }),
  ...(expiresUnix !== undefined && { expiresUnix: big(expiresUnix) }),
  ...(speedLimitBps !== undefined && { speedLimitBps: big(speedLimitBps) }),
  ...(deviceSpeedLimitBps !== undefined && { deviceSpeedLimitBps: big(deviceSpeedLimitBps) }),
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
  const dateText = new Intl.DateTimeFormat(t.locale, { day: "numeric", month: "long", ...(new Date(user.expiresUnix * 1000).getFullYear() !== new Date(now * 1000).getFullYear() && { year: "numeric" }) }).format(new Date(user.expiresUnix * 1000));
  const term = d === null ? t("users.never") : user.expiresUnix <= now ? t("users.status.expired") : `${t("users.until")} ${dateText} · ${t("users.days", { n: d })}`;

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
          <span className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[13px] text-muted">
            {appsText(user, t)} · <GroupChip id={user.groupId} name={user.groupName} color={user.groupColor} /> · {term}
          </span>
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
            blocked={data.blocked}
            nodeName={(id) => data.nodeAccess.find((n) => n.nodeId === id)?.nodeName ?? data.nodeTraffic.find((n) => n.nodeId === id)?.nodeName ?? id}
            profiles={data.profiles}
            actions={actions}
            onEditGroup={() => setGroupOpen(true)}
            autoAdd={addDevice}
            onAutoAddDone={() => void navigate({ to: "/users/$id", params: { id: user.id }, search: {}, replace: true } as never)}
          />
        </div>
        <div className="flex min-w-0 flex-col gap-3.5">
          <SubscriptionNamePanel user={user} actions={actions} />
          <LimitsPanel user={user} actions={actions} />
          <AccessPanel data={data} actions={actions} />
          <DnsChoicesPanel user={user} actions={actions} />
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

      {/* last and full width: the counts run long, and the cards above are what the owner changes */}
      <ConnectionPanel userId={user.id} devices={data.devices} nodeName={(id) => data.nodeAccess.find((n) => n.nodeId === id)?.nodeName ?? id} />

      <LinkModal target={linkOpen ? linkTargetOf(user, group) : null} onClose={() => setLinkOpen(false)} />
      <GroupEditModal group={group} open={groupOpen} onOpenChange={setGroupOpen} />
    </div>
  );
}

type Actions = ReturnType<typeof useUserActions>;

function SubscriptionNamePanel({ user, actions }: { user: User; actions: Actions }) {
  const t = useTx();
  const toast = useToast();
  const [draft, setDraft] = useState<{ server: string; value: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const name = draft?.server === user.subscriptionName ? draft.value : user.subscriptionName;
  const trimmed = name.trim();
  const unchanged = trimmed === user.subscriptionName;
  async function save() {
    setBusy(true);
    try {
      await actions.update({ subscriptionName: trimmed });
      toast(t("users.subscriptionNameSaved"));
    } catch {
      // actions.update displays the server error and keeps the draft available for correction.
    } finally {
      setBusy(false);
    }
  }

  return (
    <Panel title={t("users.subscriptionPage")} icon="person" tone="lavender">
      <TextField
        aria-label={t("users.subscriptionName")}
        label={t("users.subscriptionName")}
        hint={t("users.subscriptionNameHint")}
        value={name}
        onChange={(event) => setDraft({ server: user.subscriptionName, value: event.target.value })}
        maxLength={64}
        autoComplete="off"
        disabled={busy}
      />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="min-w-0 text-xs text-muted">{t("users.subscriptionNamePreview", { name: trimmed || user.name })}</p>
        <Button variant="secondary" disabled={busy || unchanged} onClick={() => void save()}>
          {t("users.subscriptionNameSave")}
        </Button>
      </div>
    </Panel>
  );
}

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

// ---- DNS picked on the user's page ----

/**
 * "DNS picked on the page": what the person picked on their page, for the apps on the link (one line) and per node. Nothing
 * picked, nothing shown. A pick that is no longer offered does nothing: the line says what applies instead. "Reset" (helper and
 * owner) removes them all.
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
  if (q.isError) return <QueryError compact error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data || (q.data.choices.length === 0 && !q.data.link)) return null;
  const link = q.data.link;

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
    <Panel title={t("subs.dnsChoice.title")} icon="dns" tone="sky">
      <p className="-mt-1 text-xs leading-snug text-muted">{t("subs.dnsChoice.hint")}</p>
      <ul className="flex flex-col">
        {link && (
          <li className="flex flex-col gap-0.5 border-t border-line py-2 text-[13px] first:border-t-0">
            <span className="flex min-w-0 items-baseline gap-2">
              <span className="text-xs font-bold">{t("subs.dnsChoice.link")}</span>
              <span className="min-w-0 flex-1 break-words">{label(link.presetId, link.presetName)}</span>
            </span>
            {!link.offered && <span className="text-[11px] leading-snug text-warn-text">{t("subs.dnsChoice.linkGone", { name: label(link.effectivePresetId, link.effectivePresetName) })}</span>}
          </li>
        )}
        {q.data.choices.map((c) => (
          <li key={c.nodeId} className="flex flex-col gap-0.5 border-t border-line py-2 text-[13px] first:border-t-0">
            <span className="flex min-w-0 items-baseline gap-2">
              <span className="font-mono text-xs font-bold">{c.nodeName}</span>
              <span className="min-w-0 flex-1 break-words">{label(c.presetId, c.presetName)}</span>
            </span>
            {!c.offered && <span className="text-[11px] leading-snug text-warn-text">{t("subs.dnsChoice.gone", { name: label(c.effectivePresetId, c.effectivePresetName) })}</span>}
          </li>
        ))}
      </ul>
      {can.run && (
        <div className="flex justify-end">
          <Button variant="secondary" onClick={() => setOpen(true)}>
            {t("subs.dnsChoice.reset")}
          </Button>
        </div>
      )}
      <ConfirmModal open={open} onOpenChange={setOpen} title={t("subs.dnsChoice.resetT")} description={t("subs.dnsChoice.resetBody", { name: user.name })} confirmLabel={t("subs.dnsChoice.reset")} danger onConfirm={reset} />
    </Panel>
  );
}

// ---- connection (the alerts the panel derives about this person) ----

/**
 * "Connection": the open ACCESS_ENDED and USER_CONNECTION alerts of this person, from the same query as Health (no new
 * call): what the panel sees, why, and since when. Nothing open, nothing shown (also while the alerts load or fail).
 */
/**
 * The person's connection: the alerts about them, then what the nodes counted of their connections in the last 24 hours, per
 * device and profile (lib/connections.ts). Nothing at all is shown while neither has anything to say.
 */
export function ConnectionPanel({ userId, devices = [], nodeName = (id) => id }: { userId: string; devices?: Device[]; nodeName?: (id: string) => string }) {
  const t = useT();
  const fmt = useFmt();
  const q = useQuery(alertsQuery);
  const conn = useQuery(userConnectionQuery(userId)).data;
  const rows = (q.data?.active ?? []).filter((a) => alertIsAbout(a, userId)).sort((a, b) => b.severity - a.severity);
  const counted = (conn?.rows.length ?? 0) > 0 || (conn?.nodesWithoutCounters.length ?? 0) > 0;
  if (rows.length === 0 && !counted) return null;
  const now = q.data?.nowUnix ?? nowSec();
  return (
    <Panel title={t("users.connection")} icon="pulse" tone="rose">
      {rows.length > 0 && (
        <ul className="flex flex-col">
          {rows.map((a) => {
            const kind = severityKind(a.severity);
            const why = alertWhy(t, fmt, a);
            return (
              <li key={a.id} className="flex flex-col gap-1 border-t border-line py-2.5 first:border-t-0 first:pt-0 last:pb-0">
                <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
                  <span className={cx("flex items-center gap-[7px] text-xs font-bold", kindTextClass[kind])}>
                    <StatusDot kind={kind} />
                    {t(severityWord(a.severity))}
                  </span>
                  <span className="min-w-40 flex-1 text-[13px] font-bold">{alertTitle(t, a)}</span>
                  <span className="font-mono text-[11px] text-muted">{t("hl.alerts.since", { duration: fmt.duration(Math.max(0, now - (a.openedUnix || a.firstSeenUnix))) })}</span>
                </div>
                {why && <p className="text-[13px] leading-normal text-pretty text-muted">{why}</p>}
              </li>
            );
          })}
        </ul>
      )}
      {conn && <ConnectionCounts conn={conn} devices={devices} nodeName={nodeName} separated={rows.length > 0} />}
    </Panel>
  );
}

const connPageSize = 6;

/** What the nodes counted of the person's connections: a line per device and profile, trouble in the warning colour. */
function ConnectionCounts({ conn, devices, nodeName, separated }: { conn: UserConnection; devices: Device[]; nodeName: (id: string) => string; separated: boolean }) {
  const t = useT();
  const tx = useTx();
  const fmt = useFmt();
  const [wanted, setPage] = useState(1);
  const page = clampPage(wanted, conn.rows.length, connPageSize);
  const name = (id: string) => {
    const d = devices.find((x) => x.id === id);
    if (!d) return t("conn.deviceGone");
    if (d.kind === DeviceKind.LINK_SHARED) return tx("users.dev.shared");
    return d.model || platformLabel(d.platform || "other", tx);
  };
  const missing = conn.nodesWithoutCounters.map(nodeName);
  return (
    <div data-testid="conn-counts" className={cx("flex flex-col gap-2", separated && "mt-2.5 border-t border-line pt-2.5")}>
      <span className="text-xs font-bold text-muted">{t("conn.title")}</span>
      {conn.rows.length === 0 && missing.length === 0 && <p className="text-[13px] text-muted">{t("conn.none")}</p>}
      {conn.rows.length > 0 && (
        <ul className="flex flex-col divide-y divide-line">
          {conn.rows.slice((page - 1) * connPageSize, page * connPageSize).map((r) => (
            <li key={`${r.deviceId}/${r.inboundId}`} data-testid="conn-row" className="flex flex-col gap-0.5 py-2 first:pt-0 last:pb-0">
              <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-[13px]">
                <b className="font-bold">{name(r.deviceId)}</b>
                <span className="text-muted">{t("conn.where", { node: r.nodeName || r.nodeId, profile: r.profileName })}</span>
                {r.lastHourUnix > 0 && <span className="ml-auto font-mono text-[11px] text-muted">{t("conn.last", { ago: fmt.ago(r.lastHourUnix) })}</span>}
              </div>
              <PartsLine parts={deviceConnParts(t, fmt, r)} />
            </li>
          ))}
        </ul>
      )}
      <Pagination total={conn.rows.length} page={page} size={connPageSize} onPage={setPage} label={t("conn.title")} className="pb-0" />
      {missing.length > 0 && <p className="text-xs leading-normal text-pretty text-muted">{t("conn.oldAgents", { nodes: missing.join(", ") })}</p>}
    </div>
  );
}

/** The facts of a counter line, joined by middle dots; trouble in the warning colour. */
export function PartsLine({ parts }: { parts: Part[] }) {
  return (
    <p className="text-xs leading-normal text-pretty text-muted">
      {parts.map((p, i) => (
        <span key={i}>
          {i > 0 && " · "}
          <span className={cx(p.warn && "font-semibold text-warn-text")}>{p.text}</span>
        </span>
      ))}
    </p>
  );
}

// ---- alert strip ----

function AlertStrip({ user, now, fmt, t, actions }: { user: User; now: number; fmt: Fmt; t: Tx; actions: Actions }) {
  // in the "only count" mode nothing is refused at the limit: the devices panel says so instead
  const mode = useQuery(settingsQuery).data?.settings.linkDeviceLimit;
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
  } else if (shownStatus(user) === "devices" && mode !== LinkDeviceLimit.TRACK) {
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

const trafficRows = 5; // the biggest node × protocol rows; the rest behind "Show all"

export function TrafficPanel({ data, fmt }: { data: DetailN; fmt: Fmt }) {
  const t = useTx();
  const user = data.user!;
  const protocols = useQuery(protocolsQuery);
  const protoName = (id: string) => protocols.data?.find((p) => p.id === id)?.displayName ?? id;
  const pct = user.quotaBytes > 0 ? user.usedBytes / user.quotaBytes : 0;
  const tone = pct > 1 ? "bg-danger" : pct >= 0.8 ? "bg-warn" : "bg-accent";
  const days = data.dailyTraffic;
  const max = Math.max(0, ...days.map((x) => x.bytes));
  const [allNodes, setAllNodes] = useState(false);
  const folds = data.nodeTraffic.length > trafficRows + 1; // one more row is not worth a button
  const nodeRows = folds && !allNodes ? data.nodeTraffic.slice(0, trafficRows) : data.nodeTraffic;

  return (
    <Panel title={t("users.traffic")} icon="traffic" tone="sky" aside={<span className="text-xs text-muted">{t(resetKey[user.quotaReset])}</span>}>
      <div className="flex items-baseline gap-2">
        <span className="font-mono text-2xl font-bold tracking-[-0.04em]">{fmt.bytes(user.usedBytes)}</span>
        <span className="text-[13px] text-muted">{user.quotaBytes > 0 ? t("users.ofQuota", { q: fmt.bytes(user.quotaBytes) }) : <Infinite label={t("users.unlimited")} />}</span>
      </div>
      <div className="h-1 rounded-sm bg-surface-2">
        <div className={cx("h-full rounded-sm", tone)} style={{ width: `${Math.min(100, pct * 100).toFixed(1)}%` }} />
      </div>
      {user.nextResetUnix > 0 && <span className="text-xs text-muted">{t("users.nextReset", { when: fmt.dateTime(user.nextResetUnix) })}</span>}
      <div role="img" aria-label={t("users.chartLabel")} className="flex h-16 items-end gap-1 pt-1.5">
        {days.map((x, i) => (
          <span
            key={x.dayUnix}
            title={t("common.labeled", { label: fmt.date(x.dayUnix), value: fmt.bytes(x.bytes) })}
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
          {nodeRows.map((r) => (
            <div key={r.nodeId + r.protocol} data-testid="traffic-row" className="flex h-[34px] items-center gap-2.5 border-t border-line text-[13px]">
              <Link to="/nodes/$id" params={{ id: r.nodeId }} className="min-w-10 font-bold hover:text-accent-text hover:underline">
                {r.nodeName}
              </Link>
              <span className="flex-1 text-muted">{protoName(r.protocol)}</span>
              <span className="font-mono text-xs">{fmt.bytes(r.bytes)}</span>
            </div>
          ))}
          {folds && (
            <button
              type="button"
              aria-expanded={allNodes}
              onClick={() => setAllNodes(!allNodes)}
              className="-ml-2 h-8 w-fit cursor-pointer rounded-ctl px-2 text-xs font-bold text-muted transition-colors duration-200 hover:bg-surface-2 hover:text-fg"
            >
              {allNodes ? t("users.trafficLess") : t("users.trafficAll", { n: data.nodeTraffic.length })}
            </button>
          )}
        </div>
      )}
    </Panel>
  );
}

// ---- devices ----

/**
 * The person's devices, in the two ways of the page, every row alike (link-devices.tsx: a tile, the name over one quiet
 * line, the status and the ⋯ on the right). "Apps on the link": a row per app with a device ID of its own, then the place
 * the apps without one share. "AmneziaVPN keys": a key per device, with its profile, when it last connected, "Show key"
 * and "Delete…" in its menu. A way the person does not have and has nothing in stays out. Blocked devices are folded at
 * the end. The count in the header is amber when the limit is full and red when the person is over it; in the "only
 * count" mode a person over it gets one quiet line on why nothing is refused.
 */
export function DevicesPanel({
  user,
  devices,
  blocked = [],
  nodeName = (id) => id,
  profiles,
  actions,
  onEditGroup,
  autoAdd,
  onAutoAddDone,
}: {
  user: User;
  devices: Device[];
  blocked?: BlockedN[];
  /** The name of a node by its id, for "online · de1". */
  nodeName?: (id: string) => string;
  profiles: ProfileRef[];
  actions: Actions;
  onEditGroup: () => void;
  /** Open "Add device" on mount (the link window sends the owner here). */
  autoAdd?: boolean;
  onAutoAddDone?: () => void;
}) {
  const t = useTx();
  const mode = useQuery(settingsQuery).data?.settings.linkDeviceLimit;
  // the device stays set while the dialog fades out, so its text does not go blank
  const [asking, setAsking] = useState<Device | null>(null);
  const [askOpen, setAskOpen] = useState(false);
  const [opened, setOpened] = useState<{ device: DeviceRef; initial?: DeviceConfig[] } | null>(null);
  const full = user.deviceLimit > 0 && user.devicesUsed >= user.deviceLimit;
  const over = user.deviceLimit > 0 && user.devicesUsed > user.deviceLimit;
  const awgProfiles = profiles.filter((p) => p.protocol === "awg");
  const canAdd = !!user.apps?.amnezia && awgProfiles.length > 0;
  const [adding, setAdding] = useState(!!autoAdd && canAdd && !full);
  const now = nowSec();
  const { link, shared, keys } = splitDevices(devices);
  const showSub = !!user.apps?.happ || link.length > 0 || shared.length > 0;
  const showKeys = !!user.apps?.amnezia || keys.length > 0;
  const nameOf = (d: Device) => d.model || platformLabel(d.platform, t);
  const refOf = (d: Device): DeviceRef => ({ id: d.id, label: d.model, platform: d.platform, profileName: d.awgProfileName, version: d.awgVersion, address: d.address, stale: d.stale });
  const ask = (d: Device) => {
    setAsking(d);
    setAskOpen(true);
  };
  const askingSub = asking !== null && !keys.includes(asking);

  return (
    <Panel
      title={t("users.devicesT")}
      icon="phone"
      tone="mint"
      aside={
        <span className={cx("font-mono text-xs", over ? "font-semibold text-danger-text" : full ? "text-warn-text" : "text-muted")}>
          {user.deviceLimit > 0 ? t("users.devCount", { n: user.devicesUsed, limit: user.deviceLimit }) : user.devicesUsed}
        </span>
      }
      // a container: on a narrow card a row's status keeps the servers and drops the word, a key's button its text
      className="@container pb-2"
    >
      {over && mode === LinkDeviceLimit.TRACK && (
        <p className="-mt-1 text-xs leading-snug text-pretty text-muted">
          {t("users.devTrack")}{" "}
          <Link to="/subscriptions" search={{ tab: "rules" }} hash="device-limit" className="font-bold whitespace-nowrap text-accent-text hover:underline">
            {t("users.devTrackLink")}
          </Link>
        </p>
      )}
      {!showSub && !showKeys && <p className="border-t border-line pt-3.5 pb-1 text-[13px] text-muted">{t("users.noDevices")}</p>}

      {showSub && (
        <section className="flex flex-col">
          <SectionHead title={t("users.devSubApps")} />
          {link.length === 0 && shared.length === 0 ? (
            <p className="py-2.5 text-xs text-muted">{t("users.devSubNever")}</p>
          ) : (
            <ul className="flex flex-col">
              <LinkDeviceRows devices={link} nodeName={nodeName} now={now} onChanged={actions.refresh} />
              {shared.map((d) => (
                <SharedRow key={d.id} d={d} mode={mode} now={now} onDisconnect={() => ask(d)} />
              ))}
            </ul>
          )}
        </section>
      )}

      {showKeys && (
        <section className="flex flex-col">
          <SectionHead
            title={t("users.devKeys")}
            hint={full && canAdd ? t("awg.dev.full") : undefined}
            action={
              canAdd && (
                <Button variant="secondary" size="sm" disabled={full} aria-label={t("awg.dev.add")} onClick={() => setAdding(true)}>
                  <Icon name="plus" size={12} />
                  <span className="@max-[400px]:sr-only">{t("awg.dev.add")}</span>
                </Button>
              )
            }
          />
          {!!user.apps?.amnezia && awgProfiles.length === 0 && (
            <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 py-2">
              <span className="min-w-0 flex-1 text-[11px] leading-snug text-muted">{t("users.awgMissing", { name: user.groupName })}</span>
              <button type="button" onClick={onEditGroup} className="text-xs font-bold text-accent-text transition-colors hover:text-fg">
                {t("users.groupEdit")}
              </button>
            </div>
          )}
          {keys.length === 0 ? (
            <p className="py-2.5 text-xs text-muted">{t("users.devKeysNone")}</p>
          ) : (
            <ul className="flex flex-col">
              {keys.map((d) => (
                <DeviceRow
                  key={d.id}
                  tile={<Tile icon="key" tone="mint" />}
                  name={nameOf(d)}
                  badge={d.stale && <span className="flex-none rounded-md border border-warn-line bg-warn-soft px-1.5 py-px text-[10px] font-extrabold text-warn-text">{t("awg.dev.stale")}</span>}
                  line={
                    <Secondary
                      text={
                        <>
                          {platformLabel(d.platform, t)} ·{" "}
                          <Link to="/profiles/$id" params={{ id: d.awgProfileId }} className="font-semibold text-fg hover:text-accent-text hover:underline">
                            {d.awgProfileName}
                          </Link>
                        </>
                      }
                    />
                  }
                  right={
                    <>
                      <Seen online={d.online} lastSeen={d.lastHandshakeUnix} never={t("users.devKeyNever")} t={t} now={now} />
                      <Button variant="secondary" size="sm" aria-label={t("users.devKeyShow")} onClick={() => setOpened({ device: refOf(d) })}>
                        <Icon name="key" size={12} />
                        <span className="@max-[400px]:sr-only">{t("users.devKeyShow")}</span>
                      </Button>
                    </>
                  }
                  menu={<RowMenu label={t("users.dev.menu", { name: nameOf(d) })} items={[{ label: t("users.dev.delete"), onClick: () => ask(d), danger: true }]} />}
                />
              ))}
            </ul>
          )}
        </section>
      )}

      <BlockedList blocked={blocked} onChanged={actions.refresh} />

      <ConfirmModal
        open={askOpen}
        onOpenChange={setAskOpen}
        title={askingSub ? t("users.devSubOffT") : t("users.revokeT")}
        description={askingSub ? t("users.devSubOffBody") : t("users.revokeBody", { model: asking ? nameOf(asking) : "" })}
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

export function LimitsPanel({ user, actions }: { user: User; actions: Actions }) {
  const t = useTx();
  const noop = () => {};
  const now = nowSec();

  const serverGb = user.quotaBytes === 0 ? 0 : Math.max(1, Math.round(user.quotaBytes / GB));
  const [gb, setGb] = useDraft(serverGb, (v) => actions.update({ quotaBytes: v * GB }));
  const [reset, setReset] = useDraft(user.quotaReset, (v) => actions.update({ quotaReset: v }), 0);

  // The term stepper moves the expiry date by whole days from where it is, so a click on "+" adds exactly 5 days.
  const serverDays = daysLeft(user.expiresUnix, now);
  const [days, setDays] = useDraft<number | null>(serverDays, (v) => {
    if (v === null) return actions.update({ expiresUnix: 0 });
    const from = user.expiresUnix === 0 ? now : user.expiresUnix;
    return actions.update({ expiresUnix: from + (v - (serverDays ?? 0)) * DAY });
  });

  const [devices, setDevices] = useDraft(user.deviceLimit, (v) => actions.update({ deviceLimit: v }));
  const [speed, setSpeed] = useDraft(user.speedLimitBps, (v) => actions.update({ speedLimitBps: v }));
  const [devSpeed, setDevSpeed] = useDraft(user.deviceSpeedLimitBps, (v) => actions.update({ deviceSpeedLimitBps: v }));

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
        <SettingRow label={t("users.resets")}>
          <div className="w-[170px]">
            <ResetSelect value={reset} onChange={setReset} />
          </div>
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
        <SpeedPair person={speed} device={devSpeed} onPerson={setSpeed} onDevice={setDevSpeed} />
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
          {data.profiles.length === 0 ? <span className="text-xs text-muted">{t("users.noProfiles")}</span> : <WaysOfGroup profiles={data.profiles} compact drops />}
          <p className="text-xs leading-normal text-pretty text-muted">
            {withNode(t("users.accessHint"), "group", <GroupLink id={user.groupId} name={user.groupName}>{t("common.quoted", { name: user.groupName })}</GroupLink>)}
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
