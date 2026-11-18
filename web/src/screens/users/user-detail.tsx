import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useEffect, useState, type ReactNode } from "react";
import { Code } from "@connectrpc/connect";
import { Button, buttonClass } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/notice";
import { Tabs, type TabItem } from "@/components/ui/tabs";
import { Pending, QueryError } from "@/components/ui/query-error";
import { LinkDeviceLimit } from "@/gen/mistgate/admin/v1/subscription_pb";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { Avatar } from "@/components/ui/bits";
import { cx } from "@/lib/cx";
import { useEffectsEnabled } from "@/lib/effects";
import { sameYear } from "@/lib/format";
import { settingsQuery } from "@/screens/subscriptions/queries";
import { GB, agoText, avatarIndex, daysLeft, nowSec, shownStatus } from "./format";
import { GroupEditModal } from "./groups";
import { LinkModal, linkTargetOf } from "./link-modal";
import { detailN, type DetailN } from "./model";
import { AppLink, useGo } from "./nav";
import { groupsQuery, isCode } from "./rpc";
import { useTx, type Tx } from "./t";
import { useReadonly, useUserActions, type Actions } from "./user-actions";
import { AccessTab } from "./user-access";
import { G, GroupTag, P } from "./user-bits";
import { DevicesPanel } from "./user-devices";
import { LogsTab } from "./user-logs";
import { OverviewTab } from "./user-overview";
import { DeleteModal, ResetTrafficModal, SettingsTab } from "./user-settings";
import { users } from "@/lib/api";
import "./user.css";

export const userTabs = ["overview", "devices", "access", "logs", "settings"] as const;
export type UserTab = (typeof userTabs)[number];

/**
 * The page of one person (the board's PnUser): the head (avatar, name and state, group, term, places, online, the two quick actions), a
 * banner when something stops them, and five tabs: Overview, Devices, Access and limits, Logs, Settings. The tab is in the address
 * (`?tab=devices`; the Overview has none); the old `#client-logs` and `?add=device` still lead where they led.
 */
export function UserScreen() {
  const { id = "" } = useParams({ strict: false }) as { id?: string };
  const t = useTx();
  const go = useGo();
  // the name of a person just deleted: the page says so and stops asking for them
  const [gone, setGone] = useState<string | null>(null);
  const q = useQuery({
    queryKey: ["users", "detail", id],
    queryFn: ({ signal }) => users.getUser({ userId: id }, { signal }).then(detailN),
    refetchInterval: 10_000,
    enabled: id !== "" && gone === null,
  });

  if (gone !== null) return <GoneCard name={gone} />;
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
  return <UserDetail key={q.data.user.id} data={q.data} onDeleted={() => setGone(q.data.user.name)} />;
}

/** What the page says once the person is deleted (the board's .up-gone): the link and the keys no longer work, and the way back. */
export function GoneCard({ name }: { name: string }) {
  const t = useTx();
  const fx = useEffectsEnabled();
  return (
    <div className={cx("up", fx && "up-fx")}>
      <AppLink to="/users" className="up-back">
        <G d={P.back} size={16} />
        {t("up.back")}
      </AppLink>
      <section className="up-card up-gone" role="status">
        <span className="g" aria-hidden>
          <G d={P.check} size={24} />
        </span>
        <b>{t("up.gone", { name })}</b>
        <span>{t("up.goneBody")}</span>
        <AppLink to="/users" className={cx(buttonClass("secondary", "lg"), "up-gone-a")}>
          {t("up.goneBack")}
        </AppLink>
      </section>
    </div>
  );
}

const dayDate = (t: Tx, unix: number, withYear: boolean) => new Intl.DateTimeFormat(t.locale, { day: "numeric", month: "long", ...(withYear && { year: "numeric" }) }).format(new Date(unix * 1000));

/** What stops the person, as the board's banner says it: a title, a line, and what puts it right. */
function bannerOf(data: DetailN, t: Tx, now: number, tracked: boolean, a: { actions: Actions; askReset: () => void; openLink: () => void; readonly: boolean }) {
  const user = data.user!;
  const never = user.devicesUsed === 0 && user.lastSeenUnix === 0 && user.usedBytes === 0;
  type Act = { label: string; run: () => void; primary?: boolean };
  const w = (act: Act[]) => (a.readonly ? [] : act);
  if (user.status === UserStatus.DISABLED)
    return { tone: "crit", title: t("up.ban.off"), body: t("up.ban.offBody"), acts: w([{ label: t("users.enable"), run: () => void a.actions.setEnabled(true) }]) };
  if (user.status === UserStatus.EXPIRED) {
    const days = Math.floor((now - user.expiresUnix) / 86400);
    return { tone: "crit", title: days < 1 ? t("users.alert.expiredToday") : t.n("up.ban.expired", days), body: t("up.ban.expiredBody"), acts: w([{ label: t("users.extend30"), run: () => void a.actions.extend(), primary: true }]) };
  }
  if (user.status === UserStatus.LIMITED) {
    return {
      tone: "crit",
      title: t("up.ban.quota"),
      body: t("up.ban.quotaBody", { used: String(Math.round((user.usedBytes / GB) * 10) / 10), q: Math.round(user.quotaBytes / GB), date: user.nextResetUnix > 0 ? dayDate(t, user.nextResetUnix, false) : "—" }),
      acts: w([
        { label: t("users.resetTraffic"), run: a.askReset, primary: true },
        { label: t("users.alert.quotaBtn"), run: () => void a.actions.update({ quotaBytes: user.quotaBytes + 50 * GB }).catch(() => {}) },
      ]),
    };
  }
  if (never) return { tone: "", title: t("up.ban.never"), body: t("up.ban.neverBody"), acts: a.readonly ? [] : [{ label: t("users.linkQr"), run: a.openLink, primary: true }] };
  if (shownStatus(user) === "devices" && !tracked)
    return { tone: "warn", title: t("up.ban.full", { n: user.deviceLimit }), body: t("up.ban.fullBody"), acts: w([{ label: t("up.raiseLimit", { n: user.deviceLimit + 1 }), run: () => void a.actions.update({ deviceLimit: user.deviceLimit + 1 }).catch(() => {}), primary: true }]) };
  return null;
}

/**
 * One person's page on data it is given. `scene` (the dev kit) fixes the tab in the page's own state, so a scene's address names it and
 * a click on a tab does not leave the scene.
 */
export function UserDetail({ data, scene, onDeleted }: { data: DetailN; scene?: { tab: UserTab }; onDeleted?: () => void }) {
  const user = data.user!;
  const t = useTx();
  const navigate = useNavigate();
  const go = useGo();
  const fx = useEffectsEnabled();
  const actions = useUserActions(user);
  const readonly = useReadonly();
  const [linkOpen, setLinkOpen] = useState(false);
  const [groupOpen, setGroupOpen] = useState(false);
  const [resetOpen, setResetOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const groupList = useQuery(groupsQuery);
  const group = groupList.data?.find((g) => g.id === user.groupId);
  const mode = useQuery(settingsQuery).data?.settings.linkDeviceLimit;
  const search = useSearch({ strict: false }) as { tab?: string; add?: string };
  // "?add=device" (the link window, self-service off): the Devices tab with "Add device" open, once; the address then drops it
  const addDevice = search.add === "device";
  const hashLogs = typeof window !== "undefined" && window.location.hash === "#client-logs";
  const [sceneTab, setSceneTab] = useState<UserTab>(scene?.tab ?? "overview");
  const tab: UserTab = scene ? sceneTab : addDevice ? "devices" : userTabs.includes(search.tab as UserTab) ? (search.tab as UserTab) : hashLogs ? "logs" : "overview";
  const open = (next: UserTab) =>
    scene ? setSceneTab(next) : void navigate({ to: "/users/$id", params: { id: user.id }, search: next === "overview" ? {} : { tab: next }, replace: true, hash: next === "logs" && hashLogs ? "client-logs" : undefined } as never);
  // an old link to the logs (#client-logs) lands on their tab, the address says so
  useEffect(() => {
    if (!scene && hashLogs && search.tab !== "logs") open("logs");
    // the address is the input; opening must not loop
    // oxlint-disable-next-line react-hooks/exhaustive-deps
  }, [hashLogs, search.tab]);

  const now = nowSec();
  const shown = shownStatus(user);
  const isNew = user.devicesUsed === 0 && user.lastSeenUnix === 0 && user.usedBytes === 0;
  const disabled = user.status === UserStatus.DISABLED;
  const d = daysLeft(user.expiresUnix, now);
  const pill = disabled ? { c: "", w: t("users.status.disabled") } : user.status === UserStatus.EXPIRED ? { c: "crit", w: t("up.pill.expired") } : user.status === UserStatus.LIMITED ? { c: "crit", w: t("up.pill.quota") } : isNew ? { c: "", w: t("up.pill.never") } : shown === "devices" ? { c: "warn", w: t("users.status.devices") } : { c: "ok", w: t("users.status.active") };
  const term = d === null ? t("users.never") : user.expiresUnix <= now ? t.n("up.termEnded", Math.max(1, Math.floor((now - user.expiresUnix) / 86400))) : `${t("users.until")} ${dayDate(t, user.expiresUnix, !sameYear(new Date(user.expiresUnix * 1000), new Date(now * 1000), t.locale))} · ${t("users.days", { n: d })}`;
  const termCls = d !== null && user.expiresUnix <= now ? "up-crit" : d !== null && d <= 7 ? "up-warn" : "";
  const limit = user.deviceLimit;
  const nodeName = (id: string) => data.nodeAccess.find((n) => n.nodeId === id)?.nodeName ?? id;
  const onlineText = user.online ? `${t("up.onlineNow")}${user.currentNodeName ? ` · ${user.currentNodeName}` : ""}` : user.lastSeenUnix > 0 ? agoText(user.lastSeenUnix, t, now) : t("up.neverBeen");

  const ban = bannerOf(data, t, now, mode === LinkDeviceLimit.TRACK, { actions, askReset: () => setResetOpen(true), openLink: () => setLinkOpen(true), readonly });
  const banner = ban && (
    <div className={cx("up-ban", ban.tone)} role="status">
      <span className="g" aria-hidden>
        <G d={P.warn} size={20} />
      </span>
      <div>
        <b>{ban.title}</b>
        <span>{ban.body}</span>
      </div>
      {ban.acts.length > 0 && (
        <div className="up-acts">
          {ban.acts.map((x) => (
            <Button key={x.label} variant={x.primary ? "primary" : "secondary"} size="md" onClick={x.run}>
              {x.label}
            </Button>
          ))}
        </div>
      )}
    </div>
  );
  const body = (c: ReactNode) => (
    <div className="flex flex-col gap-8">
      {banner}
      {c}
    </div>
  );
  const items: TabItem[] = [
    { value: "overview", label: t("up.tab.overview"), content: body(<OverviewTab data={data} onTab={(x) => open(x)} onQr={() => setLinkOpen(true)} />) },
    {
      value: "devices",
      label: (
        <>
          {t("up.tab.devices")}
          {user.devicesUsed > 0 && <span className={cx("up-n", limit > 0 && user.devicesUsed >= limit && "warn")}>{user.devicesUsed}</span>}
        </>
      ),
      content: body(
        <DevicesPanel
          user={user}
          devices={data.devices}
          blocked={data.blocked}
          nodeName={nodeName}
          nodeCountry={(id) => data.nodeAccess.find((n) => n.nodeId === id)?.countryCode ?? ""}
          profiles={data.profiles}
          actions={actions}
          onEditGroup={() => setGroupOpen(true)}
          autoAdd={addDevice}
          onAutoAddDone={() => void navigate({ to: "/users/$id", params: { id: user.id }, search: { tab: "devices" }, replace: true } as never)}
          onAskReset={() => setResetOpen(true)}
        />,
      ),
    },
    { value: "access", label: t("up.tab.access"), content: body(<AccessTab data={data} actions={actions} />) },
    { value: "logs", label: t("up.tab.logs"), content: body(<LogsTab user={user} devices={data.devices} />) },
    { value: "settings", label: t("up.tab.settings"), content: body(<SettingsTab user={user} actions={actions} onAskReset={() => setResetOpen(true)} onAskDelete={() => setDeleteOpen(true)} />) },
  ];

  return (
    <div className={cx("up", fx && "up-fx")} data-testid="user-page">
      <AppLink to="/users" className="up-back">
        <G d={P.back} size={16} />
        {t("up.back")}
      </AppLink>
      <header className="up-h">
        <Avatar name={user.name} index={avatarIndex(user.name)} size={48} />
        <div className="up-ht">
          <div className="up-h1">
            <h1>{user.name}</h1>
            <span className={cx("up-pill", pill.c)}>
              <i aria-hidden />
              {pill.w}
            </span>
          </div>
          <div className="up-meta">
            <GroupTag id={user.groupId} name={user.groupName} color={user.groupColor} />
            <span className={termCls}>{term}</span>
            <span>{limit > 0 ? t("up.devMeta", { n: user.devicesUsed, limit }) : t("up.devMetaNo", { n: user.devicesUsed })}</span>
            <span className={cx("up-on", user.online && "on")}>
              <i aria-hidden />
              {onlineText}
            </span>
          </div>
        </div>
        {!readonly && (
          <div className="up-ha">
            <Button size="lg" disabled={user.expiresUnix === 0} onClick={() => void actions.extend()}>
              {t("users.extend30")}
            </Button>
            <Button size="lg" variant={disabled ? "primary" : "secondary"} onClick={() => void actions.setEnabled(disabled)}>
              {t(disabled ? "users.enable" : "users.disable")}
            </Button>
          </div>
        )}
      </header>
      <Tabs className="up-tabs" aria-label={t("up.tabs")} items={items} value={tab} onValueChange={(v) => open(v as UserTab)} />

      <LinkModal target={linkOpen ? linkTargetOf(user, group) : null} onClose={() => setLinkOpen(false)} />
      <GroupEditModal group={group} open={groupOpen} onOpenChange={setGroupOpen} />
      <ResetTrafficModal user={user} actions={actions} open={resetOpen} onOpenChange={setResetOpen} />
      <DeleteModal user={user} actions={actions} open={deleteOpen} onOpenChange={setDeleteOpen} onDeleted={() => (onDeleted ? onDeleted() : go("/users", { replace: true }))} />
    </div>
  );
}

