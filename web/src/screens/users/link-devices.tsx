import { Menu } from "@base-ui/react/menu";
import { useState, type ReactNode } from "react";
import { DeviceKind } from "@/gen/mistgate/admin/v1/user_pb";
import { LinkDeviceLimit } from "@/gen/mistgate/admin/v1/subscription_pb";
import { Button } from "@/components/ui/button";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { Tip } from "@/components/ui/tooltip";
import { devices as devicesApi, users } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import { badgeOf, openRequestOf } from "@/lib/client-debug";
import { deviceError, platformLabel } from "./awg-devices";
import { LogsBadge, type LogsCtl } from "./client-logs";
import { agoText } from "./format";
import type { BlockedN, DeviceN as Device } from "./model";
import { useTx, type Tx } from "./t";
import { ConfirmModal } from "./ui";
import { CardHead, G, P } from "./user-bits";

/** The person's TrustTunnel key among their devices: a KEY device that holds this protocol (an AmneziaVPN key has an AWG profile instead). */
export const isTrustTunnelKey = (d: Pick<Device, "kind" | "protocols" | "awgProfileId">) => d.kind === DeviceKind.KEY && (d.protocols ?? []).includes("trusttunnel") && !d.awgProfileId;

/**
 * The person's devices by what they are: apps on the link with a device ID of their own, the one place the apps without a
 * device ID share, AmneziaVPN keys, the Telegram key (a device of its own kind, shown in the Telegram block) and the TrustTunnel key
 * (a KEY device like an AmneziaVPN one, told apart by its protocol, and shown in the TrustTunnel block). A device of an older panel
 * has no kind: a key has its profile, the rest is the shared place (before device IDs every app on the link was that one device).
 */
export function splitDevices(devices: Device[]) {
  const isTrustTunnel = isTrustTunnelKey;
  const isKey = (d: Device) => !isTrustTunnel(d) && (d.kind === DeviceKind.KEY || d.awgProfileId !== "");
  const isTelegram = (d: Device) => d.kind === DeviceKind.TELEGRAM_KEY;
  return {
    link: devices.filter((d) => !isKey(d) && !isTrustTunnel(d) && d.kind === DeviceKind.LINK),
    shared: devices.filter((d) => !isKey(d) && !isTelegram(d) && !isTrustTunnel(d) && d.kind !== DeviceKind.LINK),
    keys: devices.filter(isKey),
    telegram: devices.filter(isTelegram),
    trustTunnel: devices.filter(isTrustTunnel),
  };
}

/** The glyph of a device on the board's tile: a phone (iOS, Android), a tablet (an iPad), a screen for the rest. */
export function platformGlyph(d: { platform: string; model: string; osVersion?: string }): string {
  if (d.platform === "ios") return /ipad/i.test(`${d.model} ${d.osVersion ?? ""}`) ? P.tablet : P.phone;
  return d.platform === "android" ? P.phone : P.pc;
}

/** The tile in front of a device row (the board's .g): a 36 px rounded square in the step colour; a dashed ring for the place the apps without an ID share. */
export function Tile({ d, muted }: { d: string; muted?: boolean }) {
  return (
    <span aria-hidden data-muted={muted ? "" : undefined} className="g">
      <G d={d} />
    </span>
  );
}

/**
 * A device row, the same for every kind (the board's .up-dv): the tile, the name (and a badge for a real problem) over one quiet
 * line, then the grid's own cells (`right`: the status, a button), then the ⋯ at the end. `open` is a part under the row (a key's nodes).
 */
export function DeviceRow({ tile, name, badge, line, right, menu, wrapName, className, open }: { tile: ReactNode; name: ReactNode; badge?: ReactNode; line: ReactNode; right?: ReactNode; menu?: ReactNode; wrapName?: boolean; className?: string; open?: ReactNode }) {
  return (
    <li className={cx("up-dv", className)}>
      {tile}
      <div>
        <b className={cx(wrapName && "whitespace-normal")}>
          {name}
          {badge}
        </b>
        {line}
      </div>
      {right}
      {menu !== undefined && menu !== null ? menu : <span aria-hidden />}
      {open}
    </li>
  );
}

const osNames: Record<string, string> = { ios: "iOS", android: "Android", windows: "Windows", macos: "macOS", linux: "Linux" };

/** The app of a User-Agent token, name and version: "Happ/3.4.2" and "Happ/6.0.0/ios/2609301456616" read "Happ 3.4.2", "Happ 6.0.0". */
const appOf = (client: string) => client.split("/").slice(0, 2).join(" ");

/** "iOS 18.1 · Happ 3.4.2": the system with its version and the app, as the device last said them (as the person's page says it). */
const appLine = (d: { platform?: string; osVersion?: string; client: string }) => {
  const os = osNames[d.platform ?? ""] ?? "";
  const v = d.osVersion ?? "";
  const sys = v && os && !v.toLowerCase().startsWith(os.toLowerCase()) ? `${os} ${v}` : v;
  return [sys, appOf(d.client)].filter(Boolean).join(" · ");
};

/** What a link row keeps out of sight: since when and the last update, all-time traffic, the fingerprint. One fact per line. */
function detailsOf(d: Device, t: Tx, fmt: Fmt, now: number): string[] {
  return [
    [d.firstSeenUnix > 0 && t("users.dev.since", { date: fmt.date(d.firstSeenUnix) }), d.lastSeenUnix > 0 && t("users.dev.updated", { when: agoText(d.lastSeenUnix, t, now) })].filter(Boolean).join(" · "),
    t("common.labeled", { label: t("users.dev.traffic"), value: fmt.bytes(d.usedBytes) }),
    d.fingerprint && t("users.dev.fp", { fp: d.fingerprint }),
  ].filter((x): x is string => !!x);
}

const Lines = ({ lines }: { lines: string[] }) => (
  <span className="flex flex-col gap-0.5">
    {lines.map((l) => (
      <span key={l}>{l}</span>
    ))}
  </span>
);

/**
 * The quiet second line of a row. Its details show on hover and keyboard focus (the row menu repeats them, for a touch
 * screen); a screen reader hears the line and the details at once.
 */
export function Secondary({ text, details = [] }: { text: ReactNode; details?: string[] }) {
  if (details.length === 0) return <span>{text}</span>;
  return (
    <Tip label={<Lines lines={details} />}>
      <span tabIndex={0} aria-label={[text, ...details].filter(Boolean).join(". ")} className="cursor-default rounded-sm">
        {text || "—"}
      </span>
    </Tip>
  );
}

/**
 * The status on the right of a row (the board's .up-seen): the dot and "online" with the servers (two of them are the sign of a
 * copied config, which the tooltip says), or how long ago, quiet. In a narrow row the word steps aside for the servers.
 */
export function Seen({ online, nodes = [], lastSeen, never, t, now }: { online: boolean; nodes?: string[]; lastSeen: number; never?: string; t: Tx; now: number }) {
  if (!online)
    return (
      <span className={cx("up-seen", lastSeen <= 0 && !never && "none")}>
        <i aria-hidden />
        {lastSeen > 0 ? agoText(lastSeen, t, now) : (never ?? "—")}
      </span>
    );
  const status = (
    <span className="up-seen on">
      <i aria-hidden />
      <span>
        {t("users.devKeyOnline")}
        {nodes.length > 0 && <> · {nodes.join(", ")}</>}
      </span>
    </span>
  );
  if (nodes.length < 2) return status;
  const why = t.n("users.dev.twoNodesTip", nodes.length);
  return (
    <Tip label={why}>
      <span tabIndex={0} aria-label={`${t("users.devKeyOnline")} · ${nodes.join(", ")}. ${why}`} className="cursor-default rounded-sm">
        {status}
      </span>
    </Tip>
  );
}

type Item = { label: string; onClick: () => void; danger?: boolean; icon?: string };

/** The ⋯ of a row (the board's menu): the row's details on top (quiet, for a touch screen), then its actions with their glyphs. */
export function RowMenu({ label, details = [], items }: { label: string; details?: string[]; items: Item[] }) {
  return (
    <div className="up-mw">
      <Menu.Root>
        <Menu.Trigger aria-label={label} className="up-dotsb">
          <G d={P.dots} stroke={2.8} />
        </Menu.Trigger>
        <Menu.Portal>
          <Menu.Positioner sideOffset={6} align="end" className="z-50 outline-none">
            <Menu.Popup className="up-mn">
              {details.map((l, i) => (
                <p key={l} className={cx(i === details.length - 1 && "last")}>
                  {l}
                </p>
              ))}
              {items.map((i) => (
                <Menu.Item key={i.label} className={cx(i.danger && "risk")} onClick={i.onClick}>
                  {i.icon && <G d={i.icon} size={16} />}
                  {i.label}
                </Menu.Item>
              ))}
            </Menu.Popup>
          </Menu.Positioner>
        </Menu.Portal>
      </Menu.Root>
    </div>
  );
}

/**
 * A small label after the name of a device the Mistgate app connected through its own API (it holds a key of its own, so the
 * servers come to the app, not by the link): quiet grey; amber when the person's link has changed since and the app will
 * connect again on its own. The tooltip says what each means; the label is also the focusable text a screen reader reads.
 */
export function EnrolBadge({ d, t }: { d: Pick<Device, "enrolled" | "enrolLinkStale">; t: Tx }) {
  if (!d.enrolled) return null;
  const stale = d.enrolLinkStale;
  const label = t(stale ? "users.dev.linkChanged" : "users.dev.byKey");
  const tip = t(stale ? "users.dev.linkChangedTip" : "users.dev.byKeyTip");
  return (
    <Tip label={tip}>
      <span
        tabIndex={0}
        aria-label={`${label}. ${tip}`}
        className={cx("up-badge cursor-default", stale && "warn")}
      >
        {label}
      </span>
    </Tip>
  );
}

/**
 * "Inactive since 12 Oct": the device has not connected for the number of days Subscriptions sets, so it takes no place in the
 * limit. Not an alarm: the grey-blue pill of a state that is only quiet, with the whole explanation in its tooltip.
 */
export function IdleBadge({ d, t }: { d: Pick<Device, "idle" | "idleSinceUnix">; t: Tx }) {
  const fmt = useFmt();
  if (!d.idle) return null;
  const label = t("users.dev.idleSince", { date: fmt.date(d.idleSinceUnix) });
  const tip = t("users.dev.idleTip");
  return (
    <Tip label={tip}>
      <span tabIndex={0} aria-label={`${label}. ${tip}`} className="up-badge idle cursor-default">
        {label}
      </span>
    </Tip>
  );
}

/** The line under an inactive device's details: it takes no place (visible, not only in the tooltip). */
export const IdleNote = ({ d, t }: { d: Pick<Device, "idle">; t: Tx }) => (d.idle ? <span>{t("users.dev.idleNote")}</span> : null);

/** The name a device goes by: its label (a rename writes it over the model), else the model, else the platform. */
export const deviceName = (d: { model: string; platform: string }, t: Tx) => d.model || platformLabel(d.platform || "other", t);

type Doing = { what: "rename" | "delete" | "block"; d: Device };

/**
 * The apps on the link, one row per device: the tile of its platform, the name over "iOS 18.1 · Happ 3.4.2", and online on
 * which servers (or how long ago) on the right. Since when, all-time traffic and the fingerprint wait in a tooltip and in
 * the menu. The menu renames, deletes (the app may come back with new keys) or blocks (it may not, until unblocked). For the
 * owner on the VPS edition (`logs` is set), a device of the Mistgate app (it holds a key) can also be asked for its logs
 * there, and says in a badge that it was asked, answered or refused.
 */
export function LinkDeviceRows({ devices, nodeName, now, onChanged, logs }: { devices: Device[]; nodeName: (id: string) => string; now: number; onChanged: () => Promise<unknown>; logs?: LogsCtl }) {
  const t = useTx();
  const fmt = useFmt();
  const toast = useToast();
  // the device stays set while a dialog fades out, so its text does not go blank
  const [doing, setDoing] = useState<Doing | null>(null);
  const [open, setOpen] = useState(false);
  const [label, setLabel] = useState("");
  const start = (what: Doing["what"], d: Device) => {
    setDoing({ what, d });
    setLabel(d.model);
    setOpen(true);
  };

  async function run(call: () => Promise<unknown>, done: string) {
    try {
      await call();
      await onChanged();
      toast(done);
    } catch (e) {
      toast.error(deviceError(e, t));
      throw e;
    }
  }
  const name = doing ? deviceName(doing.d, t) : "";
  const trimmed = label.trim();

  return (
    <>
      {devices.map((d) => {
        const n = deviceName(d, t);
        const nodes = d.online ? d.onlineNodeIds.map(nodeName) : [];
        const details = detailsOf(d, t, fmt, now);
        const asked = logs && d.enrolled ? openRequestOf(logs.logs, d.id) : undefined;
        const logItem = !logs || !d.enrolled ? [] : asked ? [{ label: t("clientlogs.cancelAsk"), onClick: () => logs.cancel(asked.id) }] : [{ label: t("clientlogs.ask"), onClick: () => logs.ask({ id: d.id, name: n }) }];
        return (
          <DeviceRow
            key={d.id}
            className={cx(d.idle && "idle")}
            tile={<Tile d={platformGlyph(d)} />}
            name={n}
            badge={
              <>
                <IdleBadge d={d} t={t} />
                <EnrolBadge d={d} t={t} />
                {logs && d.enrolled && <LogsBadge log={badgeOf(logs.logs, d.id)} ctl={logs} />}
              </>
            }
            line={
              <>
                <Secondary text={appLine(d) || platformLabel(d.platform || "other", t)} details={details} />
                <IdleNote d={d} t={t} />
              </>
            }
            right={<Seen online={d.online} nodes={nodes} lastSeen={d.lastSeenUnix} t={t} now={now} />}
            menu={
              <RowMenu
                label={t("users.dev.menu", { name: n })}
                details={nodes.length >= 2 ? [t.n("users.dev.twoNodesTip", nodes.length), ...details] : details}
                items={[
                  ...logItem,
                  { label: t("users.dev.rename"), onClick: () => start("rename", d), icon: P.pencil },
                  { label: t("users.dev.delete"), onClick: () => start("delete", d), icon: P.trash },
                  { label: t("users.dev.block"), onClick: () => start("block", d), danger: true, icon: P.ban },
                ]}
              />
            }
          />
        );
      })}

      <ConfirmModal
        open={open && doing?.what === "rename"}
        onOpenChange={setOpen}
        title={t("users.dev.renameT")}
        confirmLabel={t("users.dev.renameDo")}
        confirmDisabled={trimmed === "" || trimmed === doing?.d.model}
        onConfirm={() => run(() => devicesApi.renameDevice({ deviceId: doing!.d.id, label: trimmed }), t("users.dev.renamed"))}
      >
        <TextField label={t("users.dev.renameLabel")} hint={t("users.dev.renameHint")} value={label} onChange={(e) => setLabel(e.target.value)} maxLength={40} autoComplete="off" autoFocus />
      </ConfirmModal>
      <ConfirmModal
        open={open && doing?.what === "delete"}
        onOpenChange={setOpen}
        title={t("users.dev.deleteT", { name })}
        description={t("users.dev.deleteBody")}
        confirmLabel={t("users.revoke")}
        danger
        onConfirm={() => run(() => users.revokeDevice({ deviceId: doing!.d.id }), t("users.revoked"))}
      />
      <ConfirmModal
        open={open && doing?.what === "block"}
        onOpenChange={setOpen}
        title={t("users.dev.blockT", { name })}
        description={t("users.dev.blockBody")}
        confirmLabel={t("users.dev.blockDo")}
        danger
        onConfirm={() => run(() => users.revokeDevice({ deviceId: doing!.d.id, block: true }), t("users.dev.blocked"))}
      />
    </>
  );
}

/**
 * The place the apps without a device ID share, in one row: the link tile, the app that fetched last and what the mode
 * does with such apps (they share one place; in the strict mode they cannot connect), when they were last seen, and
 * "Disconnect" in the menu.
 */
export function SharedRow({ d, mode, now, onDisconnect }: { d: Device; mode: LinkDeviceLimit | undefined; now: number; onDisconnect: () => void }) {
  const t = useTx();
  const name = t("users.dev.shared");
  const what = mode === LinkDeviceLimit.STRICT ? t("users.dev.sharedStrict") : t("users.dev.sharedShare");
  return (
    <DeviceRow
      tile={<Tile d={P.link} />}
      className={cx("anon", d.idle && "idle")}
      name={name}
      wrapName
      badge={<IdleBadge d={d} t={t} />}
      line={
        <>
          <Secondary text={[appOf(d.client), what].filter(Boolean).join(" · ")} />
          <IdleNote d={d} t={t} />
        </>
      }
      right={<Seen online={d.online} lastSeen={d.lastSeenUnix} t={t} now={now} />}
      menu={<RowMenu label={t("users.dev.menu", { name })} items={[{ label: t("users.dev.disconnect"), onClick: onDisconnect }]} />}
    />
  );
}

/**
 * "Blocked": what each blocked device was and since when, its tile in grey; "Unblock" at the end of the row (the app may register
 * again, with new keys). The rows of the card the Devices tab draws; nothing when there are none.
 */
export function BlockedList({ blocked, onChanged }: { blocked: BlockedN[]; onChanged: () => Promise<unknown> }) {
  const t = useTx();
  const fmt = useFmt();
  const toast = useToast();
  if (blocked.length === 0) return null;

  async function unblock(id: string) {
    try {
      await users.unblockDevice({ deviceId: id });
      await onChanged();
      toast(t("users.dev.unblocked"));
    } catch (e) {
      toast.error(deviceError(e, t));
    }
  }

  return (
    <section className="up-card up-blk">
      <CardHead title={t("up.blocked")}>
        <span className="up-lc up-r">{t("up.blockedHint")}</span>
      </CardHead>
      <ul>
        {blocked.map((b) => {
          const n = deviceName(b, t);
          const details = b.fingerprint ? [t("users.dev.fp", { fp: b.fingerprint })] : [];
          return (
            <DeviceRow
              key={b.id}
              tile={<Tile d={P.ban} muted />}
              name={n}
              line={<Secondary text={[appLine(b), b.blockedUnix > 0 && t("users.dev.blockedAt", { date: fmt.date(b.blockedUnix) })].filter(Boolean).join(" · ")} details={details} />}
              right={<span aria-hidden />}
              menu={
                <Button size="md" onClick={() => void unblock(b.id)} aria-label={`${t("users.dev.unblock")}: ${n}`}>
                  {t("users.dev.unblock")}
                </Button>
              }
            />
          );
        })}
      </ul>
    </section>
  );
}
