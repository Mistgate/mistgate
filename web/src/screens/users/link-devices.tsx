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

/** The glyph of a device: a phone, an Android phone, a tablet (an iPad), a laptop (Mac), a desktop (Windows), Linux, or any other. */
export function platformGlyph(d: { platform: string; model: string; osVersion?: string }): IconName {
  if (d.platform === "ios") return /ipad/i.test(`${d.model} ${d.osVersion ?? ""}`) ? "tablet" : "phone";
  return ({ android: "android", macos: "laptop", windows: "monitor", linux: "terminal" } as Record<string, IconName>)[d.platform] ?? "globe";
}

/**
 * The tile in front of a device row, as on the person's page: a rounded square in the soft tint of the way (sky for the
 * link, mint for the keys) with the glyph in the tone's dark shade; grey for a blocked device.
 */
export function Tile({ icon, tone, muted }: { icon: IconName; tone: "sky" | "mint"; muted?: boolean }) {
  return (
    <span aria-hidden data-tone={muted ? undefined : tone} style={{ borderRadius: 11 }} className={cx("grid size-9 flex-none place-items-center", muted ? "bg-surface-2 text-faint" : "tone-chip")}>
      <Icon name={icon} size={18} />
    </span>
  );
}

/** A quiet label over a group of rows ("APPS ON THE LINK"), with an action on its right and a muted line under it. */
export function SectionHead({ title, hint, action }: { title: string; hint?: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5 border-t border-line pt-3">
      <div className="flex min-h-7 items-center gap-2">
        <h3 className="min-w-0 flex-1 text-[11px] font-bold tracking-[0.06em] text-muted uppercase">{title}</h3>
        {action}
      </div>
      {hint && <p className="text-[11px] leading-snug text-muted">{hint}</p>}
    </div>
  );
}

/**
 * A device row, the same for every kind: the tile, the name (and a badge for a real problem) over one quiet line, and one
 * block on the right, centred on the tile: the status, a button when the row has one, the ⋯.
 */
export function DeviceRow({ tile, name, badge, line, right, menu, wrapName }: { tile: ReactNode; name: string; badge?: ReactNode; line: ReactNode; right?: ReactNode; menu: ReactNode; wrapName?: boolean }) {
  return (
    <li className="flex min-h-[60px] items-center gap-3 border-t border-line py-2.5 first:border-t-0">
      {tile}
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex min-w-0 items-center gap-2">
          <span className={cx("min-w-0 text-[13px] leading-tight font-bold", wrapName ? "break-words" : "truncate")}>{name}</span>
          {badge}
        </span>
        {line}
      </div>
      <div className="flex flex-none items-center gap-1.5">
        {right}
        {menu}
      </div>
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
  const cls = "min-w-0 truncate text-xs leading-snug text-muted";
  if (details.length === 0) return <span className={cls}>{text}</span>;
  return (
    <Tip label={<Lines lines={details} />}>
      <span tabIndex={0} aria-label={[text, ...details].filter(Boolean).join(". ")} className={cx(cls, "cursor-default rounded-sm")}>
        {text || "—"}
      </span>
    </Tip>
  );
}

/**
 * The status on the right of a row: online with the dot, the word and the servers (two of them are the sign of a copied
 * config, which the tooltip says), or how long ago, quiet. In a narrow card the word steps aside for the servers.
 */
export function Seen({ online, nodes = [], lastSeen, never, t, now }: { online: boolean; nodes?: string[]; lastSeen: number; never?: string; t: Tx; now: number }) {
  if (!online) return <span className="text-xs whitespace-nowrap text-muted">{lastSeen > 0 ? agoText(lastSeen, t, now) : (never ?? "—")}</span>;
  const status = (
    <span className="flex items-center gap-1.5 text-xs whitespace-nowrap">
      <span className="tone-ok tone-text flex items-center gap-1.5 font-semibold">
        <StatusDot kind="ok" />
        <span className={cx(nodes.length > 0 && "@max-[400px]:sr-only")}>{t("users.devKeyOnline")}</span>
      </span>
      {nodes.length > 0 && (
        <span className="text-muted">
          <span aria-hidden className="@max-[400px]:hidden">
            ·{" "}
          </span>
          {nodes.join(", ")}
        </span>
      )}
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

const itemClass = "flex min-h-10 cursor-pointer items-center rounded-ctl px-2.5 py-1.5 text-[13px] font-semibold outline-none select-none data-highlighted:bg-surface-2";

type Item = { label: string; onClick: () => void; danger?: boolean };

/** The ⋯ of a row: the row's details on top (quiet, for a touch screen), then its actions. */
export function RowMenu({ label, details = [], items }: { label: string; details?: string[]; items: Item[] }) {
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

/** The name a device goes by: its label (a rename writes it over the model), else the model, else the platform. */
export const deviceName = (d: { model: string; platform: string }, t: Tx) => d.model || platformLabel(d.platform || "other", t);

type Doing = { what: "rename" | "delete" | "block"; d: Device };

/**
 * The apps on the link, one row per device: the tile of its platform, the name over "iOS 18.1 · Happ 3.4.2", and online on
 * which servers (or how long ago) on the right. Since when, all-time traffic and the fingerprint wait in a tooltip and in
 * the menu. The menu renames, deletes (the app may come back with new keys) or blocks (it may not, until unblocked).
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
          <DeviceRow
            key={d.id}
            tile={<Tile icon={platformGlyph(d)} tone="sky" />}
            name={n}
            line={<Secondary text={appLine(d) || platformLabel(d.platform || "other", t)} details={details} />}
            right={<Seen online={d.online} nodes={nodes} lastSeen={d.lastSeenUnix} t={t} now={now} />}
            menu={
              <RowMenu
                label={t("users.dev.menu", { name: n })}
                details={nodes.length >= 2 ? [t.n("users.dev.twoNodesTip", nodes.length), ...details] : details}
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
      tile={<Tile icon="link" tone="sky" />}
      name={name}
      wrapName
      line={<Secondary text={[appOf(d.client), what].filter(Boolean).join(" · ")} />}
      right={<Seen online={d.online} lastSeen={d.lastSeenUnix} t={t} now={now} />}
      menu={<RowMenu label={t("users.dev.menu", { name })} items={[{ label: t("users.dev.disconnect"), onClick: onDisconnect }]} />}
    />
  );
}

/** "Blocked (N)", folded: what each blocked device was and since when, its tile in grey; "Unblock" in its menu (the app may register again, with new keys). */
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
    <section className="flex flex-col border-t border-line pt-1.5">
      <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className="flex min-h-9 items-center gap-1.5 text-left text-[11px] font-bold tracking-[0.06em] text-muted uppercase transition-colors hover:text-fg">
        <Icon name="chevronRight" size={13} className={cx("flex-none transition-transform duration-300 ease-spring", open && "rotate-90")} />
        {t("users.dev.blockedList", { n: blocked.length })}
      </button>
      {open && (
        <ul className="flex flex-col">
          {blocked.map((b) => {
            const n = deviceName(b, t);
            const details = b.fingerprint ? [t("users.dev.fp", { fp: b.fingerprint })] : [];
            return (
              <DeviceRow
                key={b.id}
                tile={<Tile icon={platformGlyph(b)} tone="sky" muted />}
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
