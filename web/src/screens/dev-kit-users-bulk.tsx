import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { BatchPanel, BulkDock, PickMenu } from "@/components/bulk";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { useT } from "@/i18n";
import { groups as groupsApi, users as usersApi } from "@/lib/api";
import type { Job, Row } from "@/lib/batch";
import { meQuery } from "@/lib/session";
import { Dock, Sidebar } from "@/screens/shell";
import { availability, DeleteDialog, DisableDialog, EnableDialog, ExtendDialog, panelFor, ResetDialog, type ActionId, type UsersRun } from "@/screens/users/bulk-actions";
import { UsersBulkBar } from "@/screens/users/bulk";
import { UsersScreen } from "@/screens/users/list";
import type { UserN } from "@/screens/users/model";
import { useTx } from "@/screens/users/t";

// Development only. /dev-kit?only=bulk-users: the People tab's bulk bar drawn in place (no portal), on a desktop width and
// in a 390 px frame: 37 selected with 12 of them on other pages, its More menu, "Select" while it collects and when it has
// the numbers, the confirms (the term in its three ways, disable, enable, reset traffic, delete), a long batch folded to
// counters (500 people, two failed) and the end of a Disable with "Enable again". /dev-kit?only=bulk-users-page: the real
// People tab beside the shell's sidebar and dock, on made-up people whose calls are answered here.

const now = Math.floor(Date.now() / 1000);
const owner = { admin: { id: "adm_1", role: Role.OWNER, displayName: "Owner" }, version: "dev" };
const first = ["alice", "bob", "carol", "dave", "erin", "frank", "grace", "heidi", "ivan", "judy", "kate", "liam", "mona", "nils", "olga", "pavel", "quinn", "rosa", "sam", "tara"];
const pad = (i: number) => String(i).padStart(3, "0");

/** A made-up person: every sixth is off, every ninth expired, every fourth has no expiry, every fifth used nothing yet. */
const person = (i: number, over: Partial<UserN> = {}): UserN =>
  ({
    id: `usr_${pad(i)}`,
    name: i < first.length ? first[i]! : `${first[i % first.length]}${i}`,
    subscriptionName: "",
    groupId: i % 3 === 0 ? "grp_friends" : "grp_family",
    groupName: i % 3 === 0 ? "friends" : "family",
    groupColor: i % 3 === 0 ? "sand" : "lavender",
    status: i % 6 === 0 ? UserStatus.DISABLED : i % 9 === 4 ? UserStatus.EXPIRED : UserStatus.ACTIVE,
    apps: { happ: true, amnezia: i % 4 === 1, telegram: false },
    via: [],
    devicesUsed: i % 3,
    deviceLimit: 5,
    usedBytes: i % 5 === 0 ? 0 : 12e9 + i * 3e8,
    quotaBytes: i % 2 ? 200e9 : 0,
    expiresUnix: i % 4 === 0 ? 0 : i % 9 === 4 ? now - 86400 * 3 : now + 86400 * (9 + (i % 7) * 11),
    lastSeenUnix: i % 7 === 0 ? 0 : now - 600 * (i % 40),
    nextResetUnix: 0,
    speedLimitBps: 0,
    deviceSpeedLimitBps: 0,
    createdUnix: now - 86400 * 40,
    nodes: { all: true, nodeIds: [] },
    dnsPresetId: "",
    accessHapp: true,
    accessAmnezia: false,
    accessTelegram: false,
    online: i % 11 === 2,
    currentNodeId: "",
    currentNodeName: "",
    ...over,
  }) as unknown as UserN;

const crowd = Array.from({ length: 37 }, (_, i) => person(i));
const everyone = Array.from({ length: 120 }, (_, i) => person(i));

function cache() {
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false, enabled: false } } });
  c.setQueryData(meQuery.queryKey, owner as never);
  return c;
}

function Shot({ id, title, phone, tall, children }: { id: string; title: string; phone?: boolean; tall?: boolean; children: ReactNode }) {
  const [qc] = useState(cache);
  return (
    <section data-shot={id} className={`flex w-full flex-col gap-3 ${phone ? "max-w-[390px]" : ""}`}>
      <SectionLabel>{title}</SectionLabel>
      <QueryClientProvider client={qc}>
        <ToastProvider>
          {/* the room a menu needs above (or below) the bar it opens from */}
          <div className={tall ? "flex min-h-[420px] flex-col justify-end" : "flex flex-col"}>{children}</div>
        </ToastProvider>
      </QueryClientProvider>
    </section>
  );
}

const noop = () => {};

function Bar({ busy, toggle = "disable" }: { busy?: boolean; toggle?: "disable" | "enable" }) {
  const t = useTx();
  const avail = Object.fromEntries((["extend", "disable", "enable", "reset", "delete"] as ActionId[]).map((a) => [a, availability(a, crowd, t)])) as Record<ActionId, ReturnType<typeof availability>>;
  return <UsersBulkBar count={crowd.length} offPage={12} avail={avail} toggle={toggle} busy={!!busy} onAction={noop} onClear={noop} />;
}

const row = (i: number, state: Row["state"], over: Partial<Row> = {}): Row => ({ id: `usr_${pad(i)}`, name: person(i).name, host: "", state, ...over });

function useJobs() {
  const t = useT();
  // the term to a fixed date goes person by person, four at once: 500 people, two of them failed on the way
  const long: Job = {
    spec: { action: "until", limit: 4, error: () => "", call: async () => {} },
    running: true,
    stopping: false,
    rows: Array.from({ length: 500 }, (_, i) =>
      i === 120 || i === 301 ? row(i, "bad", { note: t("err.network") }) : i < 380 ? row(i, "ok") : i < 384 ? row(i, "running") : i < 497 ? row(i, "waiting") : row(i, "skipped", { note: t("bulk.people.skip.same") }),
    ),
  };
  // a Disable of 120 people: two were off already, one was gone by its turn
  const off: Job = {
    spec: { action: "disable", limit: 1, chunk: 200, error: () => "", many: async () => {} },
    running: false,
    stopping: false,
    rows: Array.from({ length: 120 }, (_, i) => (i === 7 || i === 8 ? row(i, "skipped", { note: t("bulk.people.skip.disabled") }) : i === 50 ? row(i, "ok", { kind: "off", label: t("bulk.people.gone") }) : row(i, "ok"))),
  };
  return { long, off };
}

/** A run that does nothing: the confirms' buttons close their window and no more. */
const kitRun = { run: noop, enableAgain: noop } as unknown as UsersRun;

/** A confirm behind an "Open" button (one window at a time); its own button does nothing here. */
function Confirm({ id, title, which }: { id: string; title: string; which: "extend" | "disable" | "enable" | "reset" | "delete" }) {
  const [on, setOn] = useState(false);
  const props = { people: crowd, run: kitRun, onClose: () => setOn(false) };
  return (
    <Shot id={id} title={title}>
      <div>
        <Button data-open={id} variant="secondary" onClick={() => setOn(true)}>
          Open
        </Button>
        {on && which === "extend" && <ExtendDialog {...props} />}
        {on && which === "disable" && <DisableDialog {...props} />}
        {on && which === "enable" && <EnableDialog {...props} people={everyone.filter((p) => p.status === UserStatus.DISABLED || p.status === UserStatus.EXPIRED).map((p, i) => (i % 2 ? { ...p, status: UserStatus.DISABLED } : { ...p, status: UserStatus.DISABLED, expiresUnix: now - 86400 }))} />}
        {on && which === "reset" && <ResetDialog {...props} />}
        {on && which === "delete" && <DeleteDialog {...props} />}
      </div>
    </Shot>
  );
}

/** "Select" with the list still being collected, and with its numbers. */
function PickShot({ id, title, loaded, phone }: { id: string; title: string; loaded: boolean; phone?: boolean }) {
  const t = useT();
  const ids = (keep: (u: UserN) => boolean) => (loaded ? everyone.filter(keep).map((u) => u.id) : undefined);
  return (
    <Shot id={id} title={title} phone={phone}>
      <div className="flex min-h-[340px] items-start gap-2">
        <PickMenu
          any
          onPick={noop}
          picks={[
            { label: t("bulk.people.pick.page"), ids: everyone.slice(0, 50).map((u) => u.id) },
            { label: t("bulk.people.pick.all"), ids: ids(() => true) },
            null,
            { label: t("bulk.people.pick.expired"), ids: ids((u) => u.status === UserStatus.EXPIRED) },
            { label: t("bulk.people.pick.disabled"), ids: ids((u) => u.status === UserStatus.DISABLED) },
            { label: t("bulk.people.pick.noAccess"), ids: ids((u) => u.id.endsWith("7")) },
            { label: t("bulk.people.pick.neverSeen"), ids: ids((u) => u.lastSeenUnix === 0) },
          ]}
        />
      </div>
    </Shot>
  );
}

export function UsersBulkKit() {
  const t = useT();
  const tx = useTx();
  const jobs = useJobs();
  const region = t("bulk.people.region");
  const enableAgain = (
    <Button variant="secondary" size="sm">
      <Icon name="check" size={12} />
      {t("bulk.people.undo")}
    </Button>
  );
  const frames = (phone: boolean) => (
    <>
      <Shot id={phone ? "ub-bar-phone" : "ub-bar"} title="37 selected, 12 of them on other pages" phone={phone} tall>
        <BulkDock float={false} region={region}>
          <Bar />
        </BulkDock>
      </Shot>
      <Shot id={phone ? "ub-bar-off-phone" : "ub-bar-off"} title="Most of them off: the bar offers Enable" phone={phone} tall>
        <BulkDock float={false} region={region}>
          <Bar toggle="enable" />
        </BulkDock>
      </Shot>
      <Shot id={phone ? "ub-running-phone" : "ub-running"} title="A long batch folded: 500 people, two failed" phone={phone}>
        <BulkDock float={false} wide region={region}>
          <BatchPanel job={jobs.long} rows={jobs.long.rows} {...panelFor(tx, "until")} fold={30} onStop={noop} onRetry={noop} onClose={noop} />
          <Bar busy />
        </BulkDock>
      </Shot>
      <Shot id={phone ? "ub-summary-phone" : "ub-summary"} title="The end of a Disable, with Enable again" phone={phone}>
        <BulkDock float={false} wide region={region}>
          <BatchPanel job={jobs.off} rows={jobs.off.rows} {...panelFor(tx, "disable")} fold={30} extra={enableAgain} onStop={noop} onRetry={noop} onClose={noop} />
          <Bar toggle="enable" />
        </BulkDock>
      </Shot>
    </>
  );
  return (
    // no viewport units here: a screenshot taken past the viewport resizes it for a moment, and the column must not follow
    <div className="mx-auto flex w-full max-w-[1000px] flex-col gap-8">
      {frames(false)}
      <div className="flex flex-wrap items-start gap-8">{frames(true)}</div>
      <PickShot id="ub-pick-loading" title="Select, while the list is collected" loaded={false} />
      <PickShot id="ub-pick" title="Select, with its numbers" loaded />
      <Confirm id="ub-extend" title="Confirm: the term" which="extend" />
      <Confirm id="ub-disable" title="Confirm: disable" which="disable" />
      <Confirm id="ub-enable" title="Confirm: enable" which="enable" />
      <Confirm id="ub-reset" title="Confirm: reset traffic" which="reset" />
      <Confirm id="ub-delete" title="Confirm: delete" which="delete" />
    </div>
  );
}

/** The calls of the People tab, answered here: dev only. */
function fakeCalls() {
  const api = usersApi as unknown as Record<string, unknown>;
  const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));
  const wire = (p: UserN) => ({
    ...p,
    usedBytes: BigInt(Math.round(p.usedBytes)),
    quotaBytes: BigInt(Math.round(p.quotaBytes)),
    expiresUnix: BigInt(p.expiresUnix),
    lastSeenUnix: BigInt(p.lastSeenUnix),
    nextResetUnix: 0n,
    speedLimitBps: 0n,
    deviceSpeedLimitBps: 0n,
    createdUnix: BigInt(p.createdUnix),
  });
  const find = (ids: string[]) => ids.flatMap((id) => everyone.find((p) => p.id === id) ?? []);
  api.listUsers = async (req: { pageSize: number; pageToken: string; offset?: number }) => {
    const start = req.pageToken ? Number(req.pageToken) : (req.offset ?? 0);
    const end = Math.min(everyone.length, start + (req.pageSize || 50));
    await wait(req.pageSize === 200 ? 700 : 120);
    return { users: everyone.slice(start, end).map(wire), nextPageToken: end < everyone.length ? String(end) : "", counts: { all: everyone.length, online: 9, expiring: 4, overQuota: 0, overDeviceLimit: 0, devicesUsed: 61, devicesOnline: 12 } };
  };
  const change = async (ids: string[], how: (p: UserN) => UserN) => {
    await wait(900);
    return { users: find(ids).map((p) => wire(how(p))) };
  };
  api.extendUsers = async ({ userIds, days }: { userIds: string[]; days: number }) => change(userIds, (p) => (p.expiresUnix > 0 ? { ...p, expiresUnix: p.expiresUnix + days * 86400 } : p));
  api.setUsersEnabled = async ({ userIds, enabled }: { userIds: string[]; enabled: boolean }) => change(userIds, (p) => ({ ...p, status: enabled ? UserStatus.ACTIVE : UserStatus.DISABLED }));
  api.resetUserTraffic = async ({ userIds }: { userIds: string[] }) => change(userIds, (p) => ({ ...p, usedBytes: 0 }));
  api.deleteUsers = async () => {
    await wait(900);
    return {};
  };
  api.updateUser = async ({ userId, expiresUnix }: { userId: string; expiresUnix?: bigint }) => {
    await wait(500);
    const p = find([userId])[0]!;
    return { user: wire({ ...p, expiresUnix: expiresUnix !== undefined ? Number(expiresUnix) : p.expiresUnix }) };
  };
  (groupsApi as unknown as Record<string, unknown>).listGroups = async () => ({
    groups: [
      { id: "grp_family", name: "family", color: "lavender", profileIds: [], userCount: 80, dnsPresetId: "", happNodes: 3, amneziaNodes: 1 },
      { id: "grp_friends", name: "friends", color: "sand", profileIds: [], userCount: 40, dnsPresetId: "", happNodes: 2, amneziaNodes: 0 },
    ],
  });
}

/** The real People tab in the shell's frame: sidebar, the page, the dock (the bar takes its place on a phone). */
export function UsersBulkPage() {
  const [qc] = useState(() => {
    fakeCalls();
    const c = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } });
    c.setQueryData(meQuery.queryKey, owner as never);
    c.setQueryData(["integrations", "awaiting"], { awaiting: 0 });
    return c;
  });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <div className="min-h-dvh md:ps-56">
          <Sidebar />
          <main className="px-4 pt-4 pb-24 md:px-[clamp(24px,4vw,64px)] md:pt-6 md:pb-8">
            <div className="mx-auto max-w-[1360px]">
              <UsersScreen />
            </div>
          </main>
          <Dock />
        </div>
      </ToastProvider>
    </QueryClientProvider>
  );
}
