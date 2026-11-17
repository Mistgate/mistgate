import { ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { Tip } from "@/components/ui/tooltip";
import { UserStatus, type ProfileRef } from "@/gen/mistgate/admin/v1/user_pb";
import type { GetTrustTunnelLinksResponse, TrustTunnelLink } from "@/gen/mistgate/trusttunnel/v1/trusttunnel_pb";
import { trustTunnel } from "@/lib/api";
import { useEdition } from "@/lib/edition";
import { cx } from "@/lib/cx";
import { errorCode, errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { useCan } from "@/lib/health";
import { DeviceRow, isTrustTunnelKey, RowMenu, Secondary, Tile } from "./link-devices";
import type { DeviceN as Device, UserN as User } from "./model";
import { useTx, type Tx } from "./t";
import { ConfirmModal } from "./ui";
import type { Actions } from "./user-actions";
import { CopyBtn, G, P, QrDialog } from "./user-bits";

/** The protocol id of the plugin. */
export const trustTunnelProtocol = "trusttunnel";

/** Whether the person's page shows TrustTunnel: it is on, their group runs a TrustTunnel profile, or they hold a key. */
export function trustTunnelShown(user: User, profiles: readonly ProfileRef[], devices: readonly Device[]): boolean {
  return !!user.apps?.trustTunnel || profiles.some((p) => p.protocol === trustTunnelProtocol) || devices.some(isTrustTunnelKey);
}

/** A refusal in the words of this block (the switch is off, the person is disabled or not active), else as usual; the device limit reads in errors.ts. */
function ttError(e: unknown, t: Tx): string {
  return t.opt(`tt.err.${errorCode(ConnectError.from(e).rawMessage)}`) ?? errorText(e, t);
}

/** Why a key is on hold: the person's status (disabled, term ended, quota used up), or the switch is off. null: not on hold. */
export function pauseOf(status: UserStatus, on: boolean): "off" | "disabled" | "expired" | "quota" | null {
  if (status === UserStatus.DISABLED) return "disabled";
  if (status === UserStatus.EXPIRED) return "expired";
  if (status === UserStatus.LIMITED) return "quota";
  return on ? null : "off";
}

/**
 * The TrustTunnel key of one person (the board's .tq card): the head with "Experimental", "On hold" and the switch; then their key (a
 * device that takes a place in the limit: revoke in its menu) and one row per tt:// link, a node and its marks ("IPv6", "via RU"), with
 * copy and a QR code for a phone. A node whose certificate is self-signed has a row with no link, only the reason. A person who is
 * disabled or whose term ended keeps the key, on hold, with no links. The link holds the key, so it is never printed: it is copied or
 * scanned. The owner and helpers see all this (policy_trusttunnel.go); an observer reads one line. The switch off with a key held asks first.
 */
export function TrustTunnelSection({
  user,
  hasKeyDevice,
  countryOf,
  onToggle,
  onChanged,
  actions,
  onAskReset,
}: {
  user: User;
  /** The person's devices hold a TrustTunnel key (even with the switch off). */
  hasKeyDevice: boolean;
  /** The country code of a node by its id, for the flag of a link. */
  countryOf: (nodeId: string) => string;
  /** The switch: on, or off (a held key is asked about first, here). */
  onToggle: (on: boolean) => Promise<unknown> | void;
  onChanged: () => void;
  /** What the "fix it" buttons do (enable, extend, raise the limit); without them the card only says why. */
  actions?: Pick<Actions, "setEnabled" | "extend" | "update">;
  /** The quota fix opens the page's "reset traffic" question. */
  onAskReset?: () => void;
}) {
  const t = useTx();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const can = useCan().run;
  const edition = useEdition();
  const unavailable = edition.edge || edition.off("trusttunnel");
  const on = !!user.apps?.trustTunnel;
  const key = ["users", "trusttunnel", user.id, on];
  // gcTime 0: the links hold the key's secret, so nothing keeps them once the card is gone
  const q = useQuery({ queryKey: key, queryFn: ({ signal }) => trustTunnel.getTrustTunnelLinks({ userId: user.id }, { signal }), enabled: can && !unavailable && (on || hasKeyDevice), gcTime: 0 });
  const [busy, setBusy] = useState(false);
  // the refusal of issuing a key, kept in the block (a toast goes away; "the limit is full: who holds the places" is worth staying)
  const [refused, setRefused] = useState("");
  const [askOpen, setAskOpen] = useState(false);
  const [askOff, setAskOff] = useState(false);

  async function run(call: () => Promise<{ trustTunnel?: GetTrustTunnelLinksResponse }>, done: string) {
    setBusy(true);
    setRefused("");
    try {
      const r = await call();
      if (r.trustTunnel) qc.setQueryData(key, r.trustTunnel);
      toast(done);
      onChanged(); // the key is a device of the person: their list changes
    } catch (e) {
      const text = ttError(e, t);
      setRefused(text);
      toast.error(text);
      throw e;
    } finally {
      setBusy(false);
    }
  }
  const issue = () => void run(() => trustTunnel.issueTrustTunnelKey({ userId: user.id }), t("tt.issued")).catch(() => {});
  const revoke = () => run(() => trustTunnel.revokeTrustTunnelKey({ userId: user.id }), t("tt.revoked"));

  const status = pauseOf(user.status, on);
  const active = user.status === UserStatus.ACTIVE;
  const holds = !!q.data?.hasKey;
  const full = user.deviceLimit > 0 && user.devicesUsed >= user.deviceLimit;
  const paused = holds && status !== null && status !== "off";
  const readonly = !can;
  const flipped = (v: boolean) => {
    if (v || (!hasKeyDevice && !holds)) return void onToggle(v);
    setAskOff(true);
  };

  // the message under the head: what is wrong, and the button that fixes it
  type Msg = { tone: "" | "warn" | "bad"; text: ReactNode; act?: { label: string; run: () => void; primary?: boolean }; link?: boolean };
  let msg: Msg | null = null;
  const fix = {
    disabled: { label: t("users.enable"), run: () => void actions?.setEnabled(true) },
    expired: { label: t("users.extend30"), run: () => void actions?.extend() },
    quota: { label: t("users.resetTraffic"), run: () => onAskReset?.() },
  };
  if (unavailable) msg = { tone: "", text: t("err.unavailable_in_edition", { feature: t("edition.feature.trusttunnel") }) };
  else if (readonly) msg = { tone: "", text: t("tt.readOnly") };
  else if (!on && !hasKeyDevice) msg = { tone: "", text: t("up.tt.offLinks") };
  else if (q.isPending) msg = { tone: "", text: t("common.loading") };
  else if (q.isError) msg = { tone: "bad", text: errorText(q.error, t) };
  else if (!holds && !on) msg = { tone: "", text: t("up.tt.offLinks") };
  else if (!holds && !active) {
    const why = status === "expired" || status === "quota" ? status : "disabled";
    msg = { tone: "", text: t(`tt.needActive.${why}`), act: actions || why === "quota" ? fix[why] : undefined };
  } else if (!holds && full) {
    msg = { tone: "warn", text: t("up.tt.full", { used: user.devicesUsed, limit: user.deviceLimit }), act: actions ? { label: t("up.raiseLimit", { n: user.deviceLimit + 1 }), run: () => void actions.update({ deviceLimit: user.deviceLimit + 1 }).catch(() => {}) } : undefined };
  } else if (!holds) msg = { tone: "", text: t("tt.noKey"), act: { label: t("tt.issue"), run: issue, primary: true } };
  else if (paused) msg = { tone: "warn", text: t(`tt.paused.${status}`) };
  else if (q.data?.links.length === 0) msg = { tone: "", text: t("tt.noLinks"), link: true };

  return (
    <section className={cx("up-card tq", paused && "paused")} data-testid="tt-section">
      <div className="up-chd tq-h">
        <span className="tq-g" aria-hidden>
          <G d={P.tunnel} size={16} />
        </span>
        <h3>{t("tt.title")}</h3>
        <span className="tq-exp">{t("up.tt.experimental")}</span>
        {paused && <span className="tq-st">{t("tt.paused")}</span>}
        {!readonly && !unavailable && <Switch className="tq-sw" aria-label={t("tt.toggle")} checked={on} onCheckedChange={flipped} />}
      </div>
      {holds && !readonly && (
        <ul>
          <DeviceRow
            className="tq-key"
            tile={<Tile d={P.key} />}
            name={t("tt.key")}
            line={<Secondary text={t("tt.keyLine", { date: fmt.date(Number(q.data!.keyIssuedUnix)) })} />}
            right={<span aria-hidden />}
            menu={<RowMenu label={t("tt.keyMenu")} items={[{ label: t("tt.revoke"), onClick: () => setAskOpen(true), danger: true, icon: P.ban }]} />}
          />
        </ul>
      )}
      {msg && (
        <div className={cx("tq-msg", msg.tone)}>
          <p>{msg.text}</p>
          {msg.act && (
            <Button variant={msg.act.primary ? "primary" : "secondary"} size="md" disabled={busy} onClick={msg.act.run}>
              {busy && msg.act.primary && <span aria-hidden className="size-3.5 animate-spin rounded-full border-[1.5px] border-current border-t-transparent" />}
              {msg.act.label}
            </Button>
          )}
          {msg.link && (
            <Link to="/profiles" search={{ protocol: trustTunnelProtocol } as never} className={buttonClass("secondary", "md")}>
              {t("tt.toProfiles")}
            </Link>
          )}
        </div>
      )}
      {refused && (
        <p className="up-empty text-danger-text" role="alert">
          {refused}
        </p>
      )}
      {holds && !paused && !readonly && q.data && q.data.links.length > 0 && (
        <div className="tq-sv">
          {q.data.links.map((l) => (
            <LinkRow key={`${l.nodeId}/${l.profileId}/${l.relay ? `via-${l.via}` : "direct"}`} l={l} cc={countryOf(l.nodeId)} t={t} />
          ))}
        </div>
      )}
      <ConfirmModal open={askOpen} onOpenChange={setAskOpen} title={t("tt.revokeT")} description={t("tt.revokeBody")} confirmLabel={t("tt.revoke")} danger onConfirm={revoke} />
      <ConfirmModal open={askOff} onOpenChange={setAskOff} title={t("tt.offT")} description={t("tt.offBody")} confirmLabel={t("tt.offDo")} danger onConfirm={async () => void (await onToggle(false))} />
    </section>
  );
}

/**
 * One tt:// link (the board's .tq-sv row): the node's flag and name with its marks (IPv6 on the exit, "via RU" for a relay), then
 * copy and a QR code. A node with a self-signed certificate has no link: the row names the reason and leads to the profile.
 */
function LinkRow({ l, cc, t }: { l: TrustTunnelLink; cc: string; t: Tx }) {
  const [qr, setQr] = useState(false);
  const noLink = l.url === "";
  return (
    <div>
      <span aria-hidden className="up-fl">
        <Flag code={cc} size={16} />
      </span>
      <b>{l.nodeName}</b>
      <span className="tq-mks">
        {l.hasIpv6 && (
          <Tip label={t("tt.ipv6Tip")}>
            <i tabIndex={0}>{t("tt.ipv6")}</i>
          </Tip>
        )}
        {l.relay && <i className="ru">{t("tt.via", { cc: l.via || "?" })}</i>}
      </span>
      {noLink ? (
        <>
          <span className="tq-need">{t(`tt.${l.note === "self_signed" ? "selfSigned" : "noLinkNote"}`)}</span>
          <Link to="/profiles/$id" params={{ id: l.profileId }} className={buttonClass("secondary", "md")} aria-label={`${t("tt.toProfile")}: ${l.profileName}`}>
            {t("tt.toProfile")}
          </Link>
          <small className="tq-hint">{t("tt.selfSignedHint")}</small>
        </>
      ) : (
        <>
          <CopyBtn value={l.url} label={t("up.copyLink")} ariaLabel={t("tt.copy")} note={t("tt.copied")} />
          <Button size="md" aria-label={t("tt.qr")} onClick={() => setQr(true)}>
            <G d={P.qr} size={16} />
            {t("up.tt.qr")}
          </Button>
          <QrDialog open={qr} onOpenChange={setQr} title={t("up.qrTitle", { node: l.nodeName })} text={t("tt.qrHint")} value={l.url} label={t("tt.qrLabel", { node: l.nodeName })} copyLabel={t("up.copyLink")} />
        </>
      )}
    </div>
  );
}
