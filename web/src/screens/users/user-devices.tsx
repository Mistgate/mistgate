import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import type { DeviceConfig } from "@/gen/mistgate/admin/v1/device_pb";
import { LinkDeviceLimit } from "@/gen/mistgate/admin/v1/subscription_pb";
import { type ProfileRef } from "@/gen/mistgate/admin/v1/user_pb";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { cx } from "@/lib/cx";
import { useReadonly, type Actions } from "./user-actions";
import { useLinkAppNames, settingsQuery } from "@/screens/subscriptions/queries";
import { AddDeviceModal, DeviceConfigsModal, platformLabel, type DeviceRef } from "./awg-devices";
import { useClientLogs } from "./client-logs";
import { BlockedList, DeviceRow, IdleBadge, IdleNote, LinkDeviceRows, RowMenu, Secondary, Seen, SharedRow, Tile, splitDevices } from "./link-devices";
import { nowSec } from "./format";
import type { BlockedN, DeviceN as Device, UserN as User } from "./model";
import { TelegramSection, telegramShown } from "./telegram-proxy";
import { useTx } from "./t";
import { TrustTunnelSection, trustTunnelShown } from "./trusttunnel-key";
import { ConfirmModal } from "./ui";
import { ConnectionCounts } from "./user-connection";
import { CardHead, P } from "./user-bits";

/**
 * The Devices tab: the places taken (dots and the sentence), the apps on the link, the AmneziaVPN keys, TrustTunnel, the blocked ones,
 * the Telegram proxy and what the nodes saw (user-connection.tsx). Every device row is alike (link-devices.tsx: the tile, the name over
 * one quiet line, the status, the ⋯). A way the person does not have and has nothing in is left out; "in the only count mode" a person
 * over the limit gets one quiet line on why nothing is refused. Logs for support live on their own tab, but a device of the Mistgate app
 * can still be asked for them from its menu here.
 */
export function DevicesPanel({
  user,
  devices,
  blocked = [],
  nodeName = (id) => id,
  nodeCountry = () => "",
  profiles,
  actions,
  onEditGroup,
  autoAdd,
  onAutoAddDone,
  onAskReset,
}: {
  user: User;
  devices: Device[];
  blocked?: BlockedN[];
  /** The name of a node by its id, for "online · de1". */
  nodeName?: (id: string) => string;
  /** The country code of a node by its id, for the flag of a link. */
  nodeCountry?: (id: string) => string;
  profiles: ProfileRef[];
  actions: Actions;
  onEditGroup: () => void;
  /** Open "Add device" on mount (the link window sends the owner here). */
  autoAdd?: boolean;
  onAutoAddDone?: () => void;
  /** The quota fix of the TrustTunnel card opens the page's "reset traffic" question. */
  onAskReset?: () => void;
}) {
  const t = useTx();
  const apps = useLinkAppNames();
  const readonly = useReadonly();
  const mode = useQuery(settingsQuery).data?.settings.linkDeviceLimit;
  // logs for support: the owner's, on the VPS edition (ctl is undefined for anyone else and where there are none)
  const { ctl: logs, dialogs: logDialogs } = useClientLogs(user.id);
  // the device stays set while the dialog fades out, so its text does not go blank
  const [asking, setAsking] = useState<Device | null>(null);
  const [askOpen, setAskOpen] = useState(false);
  const [opened, setOpened] = useState<{ device: DeviceRef; initial?: DeviceConfig[] } | null>(null);
  const used = user.devicesUsed;
  const limit = user.deviceLimit;
  const full = limit > 0 && used >= limit;
  const over = limit > 0 && used > limit;
  const awgProfiles = profiles.filter((p) => p.protocol === "awg");
  const canAdd = !readonly && !!user.apps?.amnezia && awgProfiles.length > 0;
  const [adding, setAdding] = useState(!!autoAdd && canAdd && !full);
  const now = nowSec();
  const { link, shared, keys, trustTunnel } = splitDevices(devices);
  const showSub = !!user.apps?.happ || link.length > 0 || shared.length > 0;
  const showKeys = !!user.apps?.amnezia || keys.length > 0;
  const showTg = telegramShown(user, profiles, devices);
  const showTt = trustTunnelShown(user, profiles, devices);
  const nameOf = (d: Device) => d.model || platformLabel(d.platform, t);
  const refOf = (d: Device): DeviceRef => ({ id: d.id, label: d.model, platform: d.platform, profileName: d.awgProfileName, version: d.awgVersion, address: d.address, stale: d.stale });
  const ask = (d: Device) => {
    setAsking(d);
    setAskOpen(true);
  };
  const askingSub = asking !== null && !keys.includes(asking);
  const dots = Math.min(limit, 12);

  return (
    <div className="up-one">
      <div className={cx("up-sum", full && "full")}>
        {limit > 0 && (
          <span aria-hidden className="up-dots">
            {Array.from({ length: dots }, (_, i) => (
              <i key={i} className={i < used ? (full ? "full" : "on") : ""} />
            ))}
          </span>
        )}
        <b>
          <span className="font-mono">{limit > 0 ? t("users.devCount", { n: used, limit }) : used}</span> {t("up.slotsTaken")}
        </b>
        <span>
          {limit > 0 && (full ? t("up.slotsNone") : t("up.slotsFree", { n: limit - used }))} {t("up.slotsNote")}
          {user.devicesIdle > 0 && <> {t.n("users.devIdle", user.devicesIdle)}.</>}
        </span>
        {/* in the "only count" mode nothing is refused at the limit: the line says why */}
        {over && mode === LinkDeviceLimit.TRACK && (
          <span>
            {t("users.devTrack")}{" "}
            <Link to="/subscriptions" search={{ tab: "rules" }} hash="device-limit" className="up-lk">
              {t("users.devTrackLink")}
            </Link>
          </span>
        )}
      </div>

      {!showSub && !showKeys && !showTg && !showTt && (
        <section className="up-card">
          <p className="up-empty pt-4">{t("users.noDevices")}</p>
        </section>
      )}

      {showSub && (
        <section className="up-card">
          <CardHead title={t("users.devSubApps")}>
            <span className="up-lc up-r">{apps}</span>
          </CardHead>
          {link.length === 0 && shared.length === 0 ? (
            <p className="up-empty">{t("up.appsNone")}</p>
          ) : (
            <ul>
              <LinkDeviceRows devices={link} nodeName={nodeName} now={now} onChanged={actions.refresh} logs={logs} />
              {shared.map((d) => (
                <SharedRow key={d.id} d={d} mode={mode} now={now} onDisconnect={() => ask(d)} />
              ))}
            </ul>
          )}
        </section>
      )}

      {showKeys && (
        <section className="up-card">
          <CardHead title={t("users.devKeys")}>
            {full && canAdd && <span className="up-lc">{t("awg.dev.full")}</span>}
            {canAdd && (
              <Button size="md" className="up-r" data-testid="add-key" disabled={full} aria-label={t("awg.dev.add")} onClick={() => setAdding(true)}>
                {t("awg.dev.add")}
              </Button>
            )}
          </CardHead>
          {!!user.apps?.amnezia && awgProfiles.length === 0 && (
            <div className="tq-msg warn">
              <p>{t("users.awgMissing", { name: user.groupName })}</p>
              <Button size="md" onClick={onEditGroup}>
                {t("users.groupEdit")}
              </Button>
            </div>
          )}
          {keys.length === 0 ? (
            <p className="up-empty">{t("up.keysNone")}</p>
          ) : (
            <ul>
              {keys.map((d) => (
                <DeviceRow
                  key={d.id}
                  className={cx("up-key", d.idle && "idle")}
                  tile={<Tile d={P.key} />}
                  name={nameOf(d)}
                  badge={
                    <>
                      <IdleBadge d={d} t={t} />
                      {d.stale && <span className="up-badge warn">{t("awg.dev.stale")}</span>}
                    </>
                  }
                  line={
                    <>
                      <Secondary
                        text={
                          <>
                            {platformLabel(d.platform, t)} ·{" "}
                            <Link to="/profiles/$id" params={{ id: d.awgProfileId }} className="text-fg hover:text-accent-text hover:underline">
                              {d.awgProfileName}
                            </Link>
                          </>
                        }
                      />
                      <IdleNote d={d} t={t} />
                    </>
                  }
                  right={
                    <>
                      {d.awgVersion ? <ProtocolTag protocol={d.awgVersion.startsWith("2") ? "awg-old" : "awg"} label={`AWG ${d.awgVersion}`} /> : <span aria-hidden />}
                      <Seen online={d.online} lastSeen={d.lastHandshakeUnix} never={t("users.devKeyNever")} t={t} now={now} />
                      <Button size="md" aria-label={t("users.devKeyShow")} onClick={() => setOpened({ device: refOf(d) })}>
                        {t("users.devKeyShow")}
                      </Button>
                    </>
                  }
                  menu={<RowMenu label={t("users.dev.menu", { name: nameOf(d) })} items={[{ label: t("users.dev.delete"), onClick: () => ask(d), danger: true, icon: P.trash }]} />}
                />
              ))}
            </ul>
          )}
        </section>
      )}

      {showTt && (
        <TrustTunnelSection
          user={user}
          hasKeyDevice={trustTunnel.length > 0}
          countryOf={nodeCountry}
          onToggle={(on) => actions.update({ apps: { happ: !!user.apps?.happ, amnezia: !!user.apps?.amnezia, telegram: !!user.apps?.telegram, trustTunnel: on } }).catch(() => {})}
          onChanged={() => void actions.refresh()}
          actions={readonly ? undefined : actions}
          onAskReset={onAskReset}
        />
      )}

      <BlockedList blocked={blocked} onChanged={actions.refresh} />

      {showTg && (
        <TelegramSection
          user={user}
          countryOf={nodeCountry}
          onTurnOn={() => void actions.update({ apps: { happ: !!user.apps?.happ, amnezia: !!user.apps?.amnezia, telegram: true } }).catch(() => {})}
          onChanged={() => void actions.refresh()}
        />
      )}

      <ConnectionCounts userId={user.id} devices={devices} nodeName={nodeName} />
      {logDialogs}

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
    </div>
  );
}
