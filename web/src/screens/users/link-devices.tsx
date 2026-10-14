import { Menu } from "@base-ui/react/menu";
import { useState, type ReactNode } from "react";
import { DeviceKind } from "@/gen/mistgate/admin/v1/user_pb";
import { LinkDeviceLimit } from "@/gen/mistgate/admin/v1/subscription_pb";
import { Icon, type IconName } from "@/components/ui/icons";
import { StatusDot } from "@/components/ui/status";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { Tip } from "@/components/ui/tooltip";
import { devices as devicesApi, users } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import { deviceError, platformLabel } from "./awg-devices";
import { agoText } from "./format";
import type { BlockedN, DeviceN as Device } from "./model";
import { useTx, type Tx } from "./t";
import { ConfirmModal } from "./ui";

/**
 * The person's devices by what they are: apps on the link with a device ID of their own, the one place the apps without a
 * device ID share, and AmneziaVPN keys. A device of an older panel has no kind: a key has its profile, the rest is the
 * shared place (before device IDs every app on the link was that one device).
 */
export function splitDevices(devices: Device[]) {
  const isKey = (d: Device) => d.kind === DeviceKind.KEY || d.awgProfileId !== "";
  return {
    link: devices.filter((d) => !isKey(d) && d.kind === DeviceKind.LINK),
    shared: devices.filter((d) => !isKey(d) && d.kind !== DeviceKind.LINK),
    keys: devices.filter(isKey),
  };
}

const platformIcon: Record<string, IconName> = { ios: "phone", android: "android", windows: "monitor", macos: "laptop", linux: "terminal" };

/** The name a device goes by: its label (a rename writes it over the model), else the model, else the platform. */
export const deviceName = (d: { model: string; platform: string }, t: Tx) => d.model || platformLabel(d.platform || "other", t);

/** "iOS 18.1 · Happ 3.4.2": the OS version and the app, as the device last said them ("Happ/3.4.2" reads "Happ 3.4.2"). */
const appLine = (d: { osVersion?: string; client: string }) => [d.osVersion, d.client.replace("/", " ")].filter(Boolean).join(" · ");

/** The platform as a quiet glyph that names itself to a screen reader. */
function PlatformIcon({ platform, t, faint }: { platform: string; t: Tx; faint?: boolean }) {
  return <Icon name={platformIcon[platform] ?? "phone"} size={16} role="img" aria-hidden={false} aria-label={platformLabel(platform || "other", t)} className={cx("flex-none", faint ? "text-faint" : "text-muted")} />;
}

/** What a row keeps out of sight: since when and when it last updated, all-time traffic, the fingerprint. One fact per line. */
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
function Secondary({ text, details }: { text: string; details: string[] }) {
  if (details.length === 0) return <span className="min-w-0 truncate text-xs leading-snug text-muted">{text}</span>;
  return (
    <Tip label={<Lines lines={details} />}>
      <span tabIndex={0} aria-label={[text, ...details].filter(Boolean).join(". ")} className="min-w-0 cursor-default truncate rounded-sm text-xs leading-snug text-muted">
        {text || "—"}
      </span>
    </Tip>
  );
}

const itemClass = "flex min-h-10 cursor-pointer items-center rounded-ctl px-2.5 py-1.5 text-[13px] font-semibold outline-none select-none data-highlighted:bg-surface-2";

type Item = { label: string; onClick: () => void; danger?: boolean };

/** The ⋯ of a row: the row's details on top (quiet, for a touch screen), then its actions. */
function RowMenu({ label, details = [], items }: { label: string; details?: string[]; items: Item[] }) {
  return (
    <Menu.Root>
      <Menu.Trigger
        aria-label={label}
        className="inline-flex size-8 flex-none items-center justify-center rounded-ctl text-muted transition-colors duration-200 hover:bg-surface-2 hover:text-fg data-popup-open:bg-surface-2 data-popup-open:text-fg"
      >
        <Icon name="ellipsis" size={18} />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner sideOffset={6} align="end" className="z-50 outline-none">
          <Menu.Popup className="max-w-[min(300px,calc(100vw-32px))] min-w-52 origin-(--transform-origin) rounded-card border border-line bg-surface p-1.5 shadow-(--shadow-toast) outline-none transition-[scale,opacity] duration-150 data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0">
            {details.length > 0 && (
              <div className="mb-1 border-b border-line px-2.5 pt-1 pb-2 text-[11px] leading-snug text-muted">
                <Lines lines={details} />
              </div>
            )}
            {items.map((i) => (
              <Menu.Item key={i.label} className={cx(itemClass, i.danger ? "text-danger-text" : "text-fg")} onClick={i.onClick}>
                {i.label}
              </Menu.Item>
            ))}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}

/** A device row: the platform, the name with the status on its right, one quiet line with its own right end, the ⋯. */
function Row({ icon, name, status, line, lineEnd, menu, wrapName }: { icon: ReactNode; name: string; status?: ReactNode; line: ReactNode; lineEnd?: ReactNode; menu: ReactNode; wrapName?: boolean }) {
  return (
    <li className="flex min-h-[52px] items-center gap-2.5 border-t border-line py-2 pl-[34px] first:border-t-0">
      {icon}
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex min-w-0 items-baseline gap-2">
          <span className={cx("min-w-0 text-[13px] font-bold", wrapName ? "break-words" : "truncate")}>{name}</span>
          {status && <span className="ml-auto flex-none text-xs">{status}</span>}
        </span>
        <span className="flex min-w-0 items-center gap-2">
          {line}
          {lineEnd && <span className="ml-auto flex-none">{lineEnd}</span>}
        </span>
      </div>
      {menu}
    </li>
  );
}

/** Online: the dot, the word and the servers; otherwise how long ago, quiet. */
function Seen({ online, nodes, lastSeen, t, now }: { online: boolean; nodes: string[]; lastSeen: number; t: Tx; now: number }) {
  if (!online) return <span className="text-muted">{lastSeen > 0 ? agoText(lastSeen, t, now) : t("users.devKeyNever")}</span>;
  return (
    <span className="flex items-center gap-1.5">
      <span className="tone-ok tone-text flex items-center gap-1.5 font-semibold">
        <StatusDot kind="ok" />
        {t("users.devKeyOnline")}
      </span>
      {nodes.length > 0 && <span className="font-mono text-muted">· {nodes.join(", ")}</span>}
    </span>
  );
}

type Doing = { what: "rename" | "delete" | "block"; d: Device };

/**
 * The apps on the link, one row per device: the platform, the name, online on which servers (or how long ago), and one
 * quiet line, "iOS 18.1 · Happ 3.4.2". Since when, all-time traffic and the fingerprint wait in a tooltip and in the menu.
 * Only a device on two servers at once (the sign of a copied config) gets a badge. The menu renames, deletes (the app may
 * come back with new keys) or blocks (it may not, until unblocked).
 */
export function LinkDeviceRows({ devices, nodeName, now, onChanged }: { devices: Device[]; nodeName: (id: string) => string; now: number; onChanged: () => Promise<unknown> }) {
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
        return (
          <Row
            key={d.id}
            icon={<PlatformIcon platform={d.platform} t={t} />}
            name={n}
            status={<Seen online={d.online} nodes={nodes} lastSeen={d.lastSeenUnix} t={t} now={now} />}
            line={<Secondary text={appLine(d) || platformLabel(d.platform || "other", t)} details={details} />}
            lineEnd={
              nodes.length >= 2 && (
                <Tip label={t.n("users.dev.twoNodesTip", nodes.length)}>
                  <span tabIndex={0} className="block cursor-default rounded-md border border-warn-line bg-warn-soft px-1.5 py-px text-[10px] font-extrabold whitespace-nowrap text-warn-text">
                    {t.n("users.dev.twoNodes", nodes.length)}
                  </span>
                </Tip>
              )
            }
            menu={
              <RowMenu
                label={t("users.dev.menu", { name: n })}
                details={details}
                items={[
                  { label: t("users.dev.rename"), onClick: () => start("rename", d) },
                  { label: t("users.dev.delete"), onClick: () => start("delete", d) },
                  { label: t("users.dev.block"), onClick: () => start("block", d), danger: true },
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
 * The place the apps without a device ID share, in one row: the app that fetched last and what the mode does with such
 * apps (they share one place; in the strict mode they cannot connect), when they were last seen, "Disconnect" in the menu.
 */
export function SharedRow({ d, mode, now, onDisconnect }: { d: Device; mode: LinkDeviceLimit | undefined; now: number; onDisconnect: () => void }) {
  const t = useTx();
  const name = t("users.dev.shared");
  const what = mode === LinkDeviceLimit.STRICT ? t("users.dev.sharedStrict") : t("users.dev.sharedShare");
  return (
    <Row
      icon={<Icon name="link" size={16} className="flex-none text-muted" />}
      name={name}
      wrapName
      status={<Seen online={d.online} nodes={[]} lastSeen={d.lastSeenUnix} t={t} now={now} />}
      line={<span className="min-w-0 truncate text-xs leading-snug text-muted">{[d.client.replace("/", " "), what].filter(Boolean).join(" · ")}</span>}
      menu={<RowMenu label={t("users.dev.menu", { name })} items={[{ label: t("users.dev.disconnect"), onClick: onDisconnect }]} />}
    />
  );
}

/** "Blocked (N)", folded: what each blocked device was and since when; "Unblock" in its menu (the app may register again, with new keys). */
export function BlockedList({ blocked, onChanged }: { blocked: BlockedN[]; onChanged: () => Promise<unknown> }) {
  const t = useTx();
  const fmt = useFmt();
  const toast = useToast();
  const [open, setOpen] = useState(false);
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
    <section className="flex flex-col border-t border-line pt-1">
      <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className="flex min-h-10 items-center gap-2 text-left text-xs font-bold text-muted transition-colors hover:text-fg">
        <Icon name="chevronRight" size={14} className={cx("flex-none transition-transform duration-300 ease-spring", open && "rotate-90")} />
        {t("users.dev.blockedList", { n: blocked.length })}
      </button>
      {open && (
        <ul className="flex flex-col">
          {blocked.map((b) => {
            const n = deviceName(b, t);
            const details = b.fingerprint ? [t("users.dev.fp", { fp: b.fingerprint })] : [];
            return (
              <Row
                key={b.id}
                icon={<PlatformIcon platform={b.platform} t={t} faint />}
                name={n}
                line={<Secondary text={[appLine(b), b.blockedUnix > 0 && t("users.dev.blockedAt", { date: fmt.date(b.blockedUnix) })].filter(Boolean).join(" · ")} details={details} />}
                menu={<RowMenu label={t("users.dev.menu", { name: n })} details={details} items={[{ label: t("users.dev.unblock"), onClick: () => void unblock(b.id) }]} />}
              />
            );
          })}
        </ul>
      )}
    </section>
  );
}
