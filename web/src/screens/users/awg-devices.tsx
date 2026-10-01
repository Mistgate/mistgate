import { useEffect, useState } from "react";
import { QrCode } from "@/components/qr-code";
import { Chip } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Select } from "@/components/ui/select";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import type { DeviceConfig } from "@/gen/mistgate/admin/v1/device_pb";
import type { ProfileRef } from "@/gen/mistgate/admin/v1/user_pb";
import { devices as devicesApi, users } from "@/lib/api";
import { downloadText, guessPlatform, platforms } from "@/lib/awg";
import { codedError } from "@/lib/coded-error";
import { errorText } from "@/lib/errors";
import { useTx, type Tx } from "./t";
import { ConfirmModal } from "./ui";

/** A sentence for a failed device call: the panel's FAILED_PRECONDITION codes get their own words, the rest the usual ones. */
export const deviceError = (e: unknown, t: Tx) => codedError(e, t, "awg.err");

export const platformLabel = (p: string, t: Tx) => t.opt(`awg.platform.${p}`) ?? p;

/** What identifies one AWG device in the dialogs and the lists. */
export type DeviceRef = {
  id: string;
  label: string;
  platform: string;
  profileName: string;
  version: string;
  address: string;
  stale: boolean;
};

// ---- add ----

/** "Add a device": the profile (when the group has several), the platform, a name. The configs come back at once. */
export function AddDeviceModal({
  open,
  onOpenChange,
  userId,
  userName,
  profiles,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  userId: string;
  userName: string;
  profiles: ProfileRef[];
  onCreated: (device: DeviceRef, configs: DeviceConfig[]) => void;
}) {
  const t = useTx();
  const toast = useToast();
  const [profileId, setProfileId] = useState("");
  const [platform, setPlatform] = useState<string>(() => guessPlatform(navigator.userAgent, navigator.maxTouchPoints));
  const [label, setLabel] = useState("");
  const [busy, setBusy] = useState(false);
  const chosen = profiles.find((p) => p.id === profileId)?.id ?? profiles[0]?.id ?? "";

  async function submit() {
    if (!chosen || busy) return;
    setBusy(true);
    try {
      const res = await devicesApi.createAwgDevice({ userId, profileId: chosen, platform, label: label.trim() });
      const d = res.device!;
      onOpenChange(false);
      setLabel("");
      onCreated({ id: d.id, label: d.model, platform: d.platform, profileName: d.awgProfileName, version: d.awgVersion, address: d.address, stale: d.stale }, res.configs);
    } catch (e) {
      toast.error(deviceError(e, t));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onOpenChange={onOpenChange}
      title={t("awg.add.title", { name: userName })}
      description={t("awg.add.body")}
      footer={
        <>
          <Button variant="ghost" size="md" onClick={() => onOpenChange(false)}>
            {t("users.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={busy || !chosen} onClick={() => void submit()}>
            {t("awg.add.do")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3.5"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        {profiles.length === 0 ? (
          <Notice>{t("awg.add.noProfiles")}</Notice>
        ) : (
          profiles.length > 1 && (
            <div className="flex flex-col gap-1.5">
              <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("awg.add.profile")}</span>
              <Select aria-label={t("awg.add.profile")} value={chosen} onValueChange={setProfileId} options={profiles.map((p) => ({ value: p.id, label: p.name }))} />
              <span className="text-xs leading-snug text-muted">{t("awg.add.profileHint")}</span>
            </div>
          )
        )}
        <div className="flex flex-col gap-1.5">
          <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("awg.add.platform")}</span>
          <Select aria-label={t("awg.add.platform")} value={platform} onValueChange={setPlatform} options={platforms.map((p) => ({ value: p, label: platformLabel(p, t) }))} />
        </div>
        <TextField label={t("awg.add.label")} hint={t("awg.add.labelHint")} value={label} onChange={(e) => setLabel(e.target.value)} maxLength={40} placeholder={platformLabel(platform, t)} autoComplete="off" />
        {/* a form with one text field submits on Enter only with a submit button in it */}
        <button type="submit" hidden />
      </form>
    </Modal>
  );
}

// ---- configs ----

type Loaded = { state: "loading" } | { state: "error"; error: unknown } | { state: "ready"; configs: DeviceConfig[] };

/**
 * The configs of one device: a QR code of the .conf (one code for AmneziaWG and AmneziaVPN), the file, the vpn:// key,
 * the client versions and notes of that node. Read once when the dialog opens (the panel notes that the device got the
 * current epoch) and kept in this component only: the private key never enters the query cache.
 */
export function DeviceConfigsModal({
  device,
  initial,
  onClose,
  onChanged,
}: {
  device: DeviceRef | null;
  /** Configs the call that opened the dialog already holds (a new or rotated device). */
  initial?: DeviceConfig[];
  onClose: () => void;
  /** The device was rotated or revoked: the lists behind the dialog refresh. */
  onChanged: () => void;
}) {
  const t = useTx();
  // kept after the dialog closes so its text does not go blank while it fades out
  const [last, setLast] = useState(device);
  if (device && device !== last) setLast(device);
  const d = device ?? last;
  return (
    <Modal open={!!device} onOpenChange={(o) => !o && onClose()} title={d ? d.label || platformLabel(d.platform, t) : ""} description={d ? `${d.profileName} · AWG ${d.version}` : undefined} className="md:max-w-[640px]" closeLabel={t("common.close")}>
      {device && <ConfigsBody key={device.id} device={device} initial={initial} onClose={onClose} onChanged={onChanged} />}
    </Modal>
  );
}

function ConfigsBody({ device, initial, onClose, onChanged }: { device: DeviceRef; initial?: DeviceConfig[]; onClose: () => void; onChanged: () => void }) {
  const t = useTx();
  const toast = useToast();
  const [data, setData] = useState<Loaded>(initial ? { state: "ready", configs: initial } : { state: "loading" });
  const [node, setNode] = useState("");
  const [qrFailed, setQrFailed] = useState(false);
  const [rotateOpen, setRotateOpen] = useState(false);
  const [revokeOpen, setRevokeOpen] = useState(false);

  useEffect(() => {
    if (initial) return;
    let live = true;
    devicesApi
      .getDeviceConfigs({ deviceId: device.id })
      .then((r) => live && setData({ state: "ready", configs: r.configs }))
      .catch((error: unknown) => live && setData({ state: "error", error }));
    return () => {
      live = false;
    };
  }, [device.id, initial]);

  const configs = data.state === "ready" ? data.configs : [];
  const cfg = configs.find((c) => c.nodeId === node) ?? configs[0];

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      toast(t("common.copied"));
    } catch {
      toast(t("common.copyFailed"));
    }
  }

  async function rotate() {
    try {
      const r = await devicesApi.rotateDeviceKeys({ deviceId: device.id });
      setData({ state: "ready", configs: r.configs });
      setQrFailed(false);
      onChanged();
      toast(t("awg.cfg.rotated"));
    } catch (e) {
      toast.error(deviceError(e, t));
      throw e;
    }
  }

  async function revoke() {
    try {
      await users.revokeDevice({ deviceId: device.id });
    } catch (e) {
      toast.error(errorText(e, t));
      throw e;
    }
    onChanged();
    toast(t("users.revoked"));
    onClose();
  }

  if (data.state === "loading") return <p className="py-6 text-center text-sm text-muted">{t("common.loading")}</p>;
  if (data.state === "error") return <Notice tone="danger">{deviceError(data.error, t)}</Notice>;
  if (!cfg) return <Notice>{t("awg.cfg.none")}</Notice>;

  const nodeOptions = configs.map((c) => ({ value: c.nodeId, label: c.nodeName }));
  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex flex-wrap gap-1.5">
        <Chip mono>{device.address || "—"}</Chip>
        <Chip>{t("awg.cfg.keyInside")}</Chip>
      </div>
      {cfg.stale && <Notice>{t("awg.cfg.stale")}</Notice>}

      <div className="flex flex-col items-center gap-4 md:flex-row md:items-start">
        <div className="flex flex-none flex-col items-center gap-2">
          {qrFailed ? (
            <p className="max-w-[220px] rounded-xl bg-surface-2 p-3 text-center text-xs leading-snug text-muted">{t("awg.cfg.qrTooLong")}</p>
          ) : (
            <QrCode value={cfg.conf} label={t("awg.cfg.qrLabel")} size={232} onFail={() => setQrFailed(true)} />
          )}
          <span className="max-w-[240px] text-center text-[11px] leading-snug text-muted">{t("awg.cfg.qrHint")}</span>
        </div>
        <div className="flex w-full min-w-0 flex-1 flex-col gap-2.5">
          {nodeOptions.length > 1 ? (
            <div className="flex flex-col gap-1.5">
              <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("awg.cfg.node")}</span>
              <Select aria-label={t("awg.cfg.node")} value={cfg.nodeId} onValueChange={(v) => { setNode(v); setQrFailed(false); }} options={nodeOptions} />
            </div>
          ) : (
            <span className="flex items-center gap-2 text-[13px] font-bold">
              {cfg.countryCode && <Flag code={cfg.countryCode} />}
              {cfg.nodeName}
            </span>
          )}
          <Button variant="primary" size="md" full onClick={() => downloadText(cfg.confFilename || "awg.conf", cfg.conf)}>
            {t("awg.cfg.download")}
          </Button>
          <Button variant="secondary" size="md" full onClick={() => void copy(cfg.vpnKey)}>
            {t("awg.cfg.copyKey")}
          </Button>
          <Button variant="ghost" size="md" full onClick={() => void copy(cfg.conf)}>
            {t("awg.cfg.copyConf")}
          </Button>
          <span className="text-[11px] leading-snug text-muted">{t("awg.cfg.keyHint")}</span>
        </div>
      </div>

      {cfg.warnings.map((w) => (
        <Notice key={w}>{t.opt(`awg.cfgWarn.${w}`) ?? w}</Notice>
      ))}
      {cfg.minClients.length > 0 && (
        <div className="flex flex-col gap-1.5">
          <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("awg.cfg.minClients")}</span>
          <div className="flex flex-wrap gap-1.5">
            {cfg.minClients.map((c) => (
              <Chip key={c.app} mono>
                {c.app} {c.minVersion}+
              </Chip>
            ))}
          </div>
        </div>
      )}

      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-line pt-3">
        <div className="flex gap-1.5">
          <Button variant="secondary" size="sm" onClick={() => setRotateOpen(true)}>
            {t("awg.cfg.rotate")}
          </Button>
          <Button variant="danger" size="sm" onClick={() => setRevokeOpen(true)}>
            {t("users.revoke")}
          </Button>
        </div>
        <Button variant="ghost" size="md" onClick={onClose}>
          {t("common.close")}
        </Button>
      </div>

      <ConfirmModal open={rotateOpen} onOpenChange={setRotateOpen} title={t("awg.rotate.title")} description={t("awg.rotate.body")} confirmLabel={t("awg.cfg.rotate")} onConfirm={rotate} />
      <ConfirmModal open={revokeOpen} onOpenChange={setRevokeOpen} title={t("users.revokeT")} description={t("users.revokeBody", { model: device.label || platformLabel(device.platform, t) })} confirmLabel={t("users.revoke")} danger onConfirm={revoke} />
    </div>
  );
}
