import { ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { QrCode } from "@/components/qr-code";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { StatusPill } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { Tip } from "@/components/ui/tooltip";
import { UserStatus, type ProfileRef } from "@/gen/mistgate/admin/v1/user_pb";
import type { GetTrustTunnelLinksResponse, TrustTunnelLink } from "@/gen/mistgate/trusttunnel/v1/trusttunnel_pb";
import { trustTunnel } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { errorCode, errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { useCan } from "@/lib/health";
import { DeviceRow, isTrustTunnelKey, RowMenu, Secondary, SectionHead, Tile } from "./link-devices";
import type { DeviceN as Device, UserN as User } from "./model";
import { useTx, type Tx } from "./t";
import { ConfirmModal, SwitchRow } from "./ui";

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
 * The TrustTunnel key of one person, among their devices: off, a line and the switch; on, their key (a device that takes a place
 * in the limit: revoke in its menu) and one row per tt:// link, a node and a profile, marked "IPv6" or "via RU", with copy and a QR
 * code for a phone. A node whose certificate is self-signed has a row with no link, only the reason. A person who is disabled or
 * whose term ended keeps the key, on hold, with no links. The link holds the key, so it is never printed: it is copied or scanned.
 * The owner and helpers see all this (policy_trusttunnel.go); an observer reads one line.
 */
export function TrustTunnelSection({
  user,
  hasKeyDevice,
  countryOf,
  onTurnOn,
  onChanged,
}: {
  user: User;
  /** The person's devices hold a TrustTunnel key (even with the switch off). */
  hasKeyDevice: boolean;
  /** The country code of a node by its id, for the flag of a link. */
  countryOf: (nodeId: string) => string;
  onTurnOn: () => void;
  onChanged: () => void;
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

  return (
    <section className="flex flex-col" data-testid="tt-section">
      <SectionHead title={t("tt.title")} />
      {unavailable ? (
        <Notice tone="quiet" className="my-2.5" title={t("err.unavailable_in_edition", { feature: t("edition.feature.trusttunnel") })} />
      ) : !can ? (
        <p className="py-2.5 text-xs leading-snug text-muted">{t("tt.readOnly")}</p>
      ) : !on && !hasKeyDevice ? (
        <SwitchRow label={t("tt.toggle")} hint={t("tt.off")} checked={false} onCheckedChange={(v) => v && onTurnOn()} />
      ) : q.isPending ? (
        <p className="py-2.5 text-xs text-muted">{t("common.loading")}</p>
      ) : q.isError ? (
        <p className="py-2.5 text-xs text-danger-text">{errorText(q.error, t)}</p>
      ) : !q.data.hasKey && !on ? (
        <SwitchRow label={t("tt.toggle")} hint={t("tt.off")} checked={false} onCheckedChange={(v) => v && onTurnOn()} />
      ) : !q.data.hasKey ? (
        <div className="flex flex-col gap-2 py-2.5">
          {active ? (
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
              <span className="min-w-0 flex-1 text-xs leading-snug text-muted">{t("tt.noKey")}</span>
              <Button size="sm" disabled={busy} onClick={issue}>
                <Icon name="key" size={12} />
                {t("tt.issue")}
              </Button>
            </div>
          ) : (
            <span className="text-xs leading-snug text-muted">{t(`tt.needActive.${status === "expired" || status === "quota" ? status : "disabled"}`)}</span>
          )}
          {refused && <Notice tone="danger">{refused}</Notice>}
        </div>
      ) : (
        <ul className="flex flex-col">
          <DeviceRow
            tile={<Tile icon="tunnel" tone="sage" muted={status !== null} />}
            name={t("tt.key")}
            badge={status !== null ? <StatusPill kind="off" label={t("tt.paused")} sm /> : undefined}
            line={<Secondary text={t("tt.keyLine", { date: fmt.date(Number(q.data.keyIssuedUnix)) })} />}
            menu={<RowMenu label={t("tt.keyMenu")} items={[{ label: t("tt.revoke"), onClick: () => setAskOpen(true), danger: true }]} />}
          />
          {status !== null ? (
            <li className="border-t border-line py-2.5 text-xs leading-snug text-muted">{t(`tt.paused.${status}`)}</li>
          ) : (
            <>
              {q.data.links.map((l) => (
                <LinkRow key={`${l.nodeId}/${l.profileId}/${l.relay ? `via-${l.via}` : "direct"}`} l={l} cc={countryOf(l.nodeId)} t={t} />
              ))}
              {q.data.links.length === 0 && (
                <li className="border-t border-line py-2.5 text-xs leading-snug text-muted">
                  {t("tt.noLinks")}{" "}
                  <Link to="/profiles" search={{ protocol: trustTunnelProtocol } as never} className="font-medium whitespace-nowrap text-accent-text hover:underline">
                    {t("tt.toProfiles")}
                  </Link>
                </li>
              )}
            </>
          )}
        </ul>
      )}
      <ConfirmModal open={askOpen} onOpenChange={setAskOpen} title={t("tt.revokeT")} description={t("tt.revokeBody")} confirmLabel={t("tt.revoke")} danger onConfirm={revoke} />
    </section>
  );
}

/**
 * One tt:// link: the node's flag and name with its marks (IPv6 on the exit, "via RU" for a relay), the profile, and copy and a
 * QR code. A node with a self-signed certificate has no link: the row names the reason and leads to the profile.
 */
function LinkRow({ l, cc, t }: { l: TrustTunnelLink; cc: string; t: Tx }) {
  const toast = useToast();
  const [qr, setQr] = useState(false);
  const noLink = l.url === "";
  const copy = () => navigator.clipboard.writeText(l.url).then(() => toast(t("tt.copied")), () => toast.error(t("err.generic")));
  return (
    <li className="flex flex-col border-t border-line first:border-t-0">
      <div className="flex min-h-[60px] items-center gap-3 py-2.5">
        <span aria-hidden className="grid size-9 flex-none place-items-center rounded-chip bg-surface-2">
          <Flag code={cc} size={16} />
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5">
            <b className="truncate text-[13px] leading-tight">{l.nodeName}</b>
            {l.hasIpv6 && (
              <Tip label={t("tt.ipv6Tip")}>
                <span tabIndex={0} className="flex-none cursor-default rounded-md bg-surface-2 px-1.5 text-[11px] leading-4 font-medium text-muted">
                  {t("tt.ipv6")}
                </span>
              </Tip>
            )}
            {l.relay && <span className="flex-none rounded-md bg-surface-2 px-1.5 text-[11px] leading-4 font-medium text-muted">{t("tt.via", { cc: l.via || "?" })}</span>}
          </span>
          {noLink ? (
            <span className="text-xs leading-snug text-warn-text">{t(`tt.${l.note === "self_signed" ? "selfSigned" : "noLinkNote"}`)}</span>
          ) : (
            <span className="truncate text-xs leading-snug text-muted">{l.profileName}</span>
          )}
        </div>
        {noLink ? (
          <Link
            to="/profiles/$id"
            params={{ id: l.profileId }}
            className="flex-none rounded-ctl px-2 text-xs font-bold text-accent-text transition-colors hover:text-fg"
            aria-label={`${t("tt.toProfile")}: ${l.profileName}`}
          >
            {t("tt.toProfile")}
          </Link>
        ) : (
          <div className="flex flex-none items-center gap-1">
            <IconButton variant="flat" aria-label={t("tt.copy")} onClick={() => void copy()}>
              <Icon name="copy" size={15} />
            </IconButton>
            <IconButton variant="flat" aria-label={t("tt.qr")} aria-expanded={qr} className={cx(qr && "bg-surface-2 text-fg")} onClick={() => setQr(!qr)}>
              <Icon name="qr" size={15} />
            </IconButton>
          </div>
        )}
      </div>
      {noLink && <p className="pb-2.5 ps-12 text-[11px] leading-snug text-muted">{t("tt.selfSignedHint")}</p>}
      {qr && !noLink && (
        <div className="screen-enter flex flex-wrap items-center gap-x-4 gap-y-2 pb-3 ps-12">
          <QrCode value={l.url} label={t("tt.qrLabel", { node: l.nodeName })} size={176} />
          <p className="max-w-64 text-xs leading-snug text-pretty text-muted">{t("tt.qrHint")}</p>
        </div>
      )}
    </li>
  );
}
