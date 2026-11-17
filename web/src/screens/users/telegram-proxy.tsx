import { ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { DeviceKind, type GetTelegramLinksResponse, type ProfileRef, type TelegramProxyLink } from "@/gen/mistgate/admin/v1/user_pb";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { Tip } from "@/components/ui/tooltip";
import { users } from "@/lib/api";
import { errorCode, errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { DeviceRow, RowMenu, Secondary, Tile } from "./link-devices";
import type { DeviceN as Device, UserN as User } from "./model";
import { useTx, type Tx } from "./t";
import { useReadonly } from "./user-actions";
import { CardHead, CopyBtn, G, P, QrDialog } from "./user-bits";
import { ConfirmModal } from "./ui";

/** Whether the person's page shows the Telegram proxy: it is on, their group runs an MTProto profile, or they hold a key. */
export function telegramShown(user: User, profiles: readonly ProfileRef[], devices: readonly Device[]): boolean {
  return !!user.apps?.telegram || profiles.some((p) => p.protocol === "mtproto") || devices.some((d) => d.kind === DeviceKind.TELEGRAM_KEY);
}

/** A refusal in the words of this block: Telegram off, the person disabled, no key to re-issue; anything else as usual. */
function tgError(e: unknown, t: Tx): string {
  return t.opt(`tgproxy.err.${errorCode(ConnectError.from(e).rawMessage)}`) ?? errorText(e, t);
}

/**
 * The Telegram proxy of one person, among their devices: off, a line and the switch; on, their key (a device that is not
 * counted in the limit: re-issue and revoke in its menu) and one row per tg://proxy link, a node and a profile, marked
 * "IPv6" or "via RU" for the twins of a node's link, with "Open in Telegram", copy and a QR code for a phone. The link holds
 * the key, so it is never printed: it is opened, copied or scanned.
 */
export function TelegramSection({
  user,
  countryOf,
  onTurnOn,
  onChanged,
}: {
  user: User;
  /** The country code of a node by its id, for the flag of a link. */
  countryOf: (nodeId: string) => string;
  onTurnOn: () => void;
  onChanged: () => void;
}) {
  const t = useTx();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const readonly = useReadonly();
  const on = !!user.apps?.telegram;
  const key = ["users", "telegram", user.id, on];
  const q = useQuery({ queryKey: key, queryFn: ({ signal }) => users.getTelegramLinks({ userId: user.id }, { signal }), enabled: on });
  const [busy, setBusy] = useState(false);
  // kept after the dialog closes, so its words do not change while it fades out
  const [ask, setAsk] = useState<"rotate" | "revoke">("rotate");
  const [askOpen, setAskOpen] = useState(false);

  async function run(call: () => Promise<{ proxy?: GetTelegramLinksResponse }>, done: string) {
    setBusy(true);
    try {
      const r = await call();
      if (r.proxy) qc.setQueryData(key, r.proxy);
      toast(done);
      onChanged(); // the key is a device of the person: their list changes
    } catch (e) {
      toast.error(tgError(e, t));
      throw e;
    } finally {
      setBusy(false);
    }
  }
  const issue = () => void run(() => users.issueTelegramKey({ userId: user.id }), t("tgproxy.issued")).catch(() => {});
  const confirm = () =>
    ask === "rotate" ? run(() => users.rotateTelegramKey({ userId: user.id }), t("tgproxy.rotated")) : run(() => users.revokeTelegramKey({ userId: user.id }), t("tgproxy.revoked"));
  const askFor = (what: "rotate" | "revoke") => {
    setAsk(what);
    setAskOpen(true);
  };

  const issued = q.data?.hasKey ? fmt.date(Number(q.data.keyIssuedUnix)) : "";

  return (
    <section className="up-card" data-testid="tg-section">
      <CardHead title={t("tgproxy.title")}>
        {on && issued ? <span className="up-lc up-r">{t("up.tgIssued", { date: issued })}</span> : <span className="up-r" />}
        {!on && !readonly && <Switch aria-label={t("tgproxy.toggle")} checked={false} onCheckedChange={(v) => v && onTurnOn()} />}
      </CardHead>
      {!on ? (
        <p className="up-empty">{t("tgproxy.off")}</p>
      ) : q.isPending ? (
        <p className="up-empty">{t("common.loading")}</p>
      ) : q.isError ? (
        <p className="up-empty text-danger-text">{errorText(q.error, t)}</p>
      ) : !q.data.hasKey ? (
        <div className="tq-msg">
          <p>{t("tgproxy.noKey")}</p>
          {!readonly && (
            <Button variant="primary" disabled={busy} onClick={issue}>
              <G d={P.key} size={16} />
              {t("tgproxy.issue")}
            </Button>
          )}
        </div>
      ) : (
        <>
          <ul>
            <DeviceRow
              tile={<Tile d={P.plane} />}
              name={t("tgproxy.key")}
              line={<Secondary text={t("tgproxy.keyLine", { date: fmt.date(Number(q.data.keyIssuedUnix)) })} />}
              right={<span aria-hidden />}
              menu={
                readonly ? undefined : (
                  <RowMenu
                    label={t("tgproxy.keyMenu")}
                    items={[
                      { label: t("tgproxy.rotate"), onClick: () => askFor("rotate"), icon: P.key },
                      { label: t("tgproxy.revoke"), onClick: () => askFor("revoke"), danger: true, icon: P.ban },
                    ]}
                  />
                )
              }
            />
          </ul>
          <div className="up-tg">
            {q.data.links.map((l) => (
              <LinkRow key={`${l.nodeId}/${l.profileId}/${l.ipv6 ? "6" : l.relay ? `via-${l.via}` : "4"}`} l={l} cc={countryOf(l.nodeId)} t={t} />
            ))}
            {q.data.links.length === 0 && (
              <p className="up-empty" style={{ gridColumn: "1 / -1", paddingInline: 0 }}>
                {t("tgproxy.noLinks")}{" "}
                <Link to="/profiles" search={{ protocol: "mtproto" } as never} className="up-lk">
                  {t("tgproxy.toProfiles")}
                </Link>
              </p>
            )}
          </div>
        </>
      )}
      <ConfirmModal
        open={askOpen}
        onOpenChange={setAskOpen}
        title={t(ask === "rotate" ? "tgproxy.rotateT" : "tgproxy.revokeT")}
        description={t(ask === "rotate" ? "tgproxy.rotateBody" : "tgproxy.revokeBody")}
        confirmLabel={t(ask === "rotate" ? "tgproxy.rotate" : "tgproxy.revoke")}
        danger
        onConfirm={confirm}
      />
    </section>
  );
}

/** One tg://proxy link (the board's .up-tg row): the node's flag and name, the profile with the twin's mark, and the three ways to hand it over. */
function LinkRow({ l, cc, t }: { l: TelegramProxyLink; cc: string; t: Tx }) {
  const [qr, setQr] = useState(false);
  const marks = [l.ipv6 && "IPv6", l.relay && t("tgproxy.via", { cc: l.via || "?" })].filter((x): x is string => !!x);
  return (
    <div>
      <span aria-hidden className="up-fl">
        <Flag code={cc} size={16} />
      </span>
      <b>{l.nodeName}</b>
      <span>{[l.profileName, ...marks].join(" · ")}</span>
      <span className="flex items-center gap-1.5">
        <Tip label={t("tgproxy.open")}>
          <a href={l.url} aria-label={t("tgproxy.open")} className="up-dotsb">
            <G d={P.plane} size={16} />
          </a>
        </Tip>
        <CopyBtn value={l.url} label={t("tgproxy.copy")} ariaLabel={t("tgproxy.copy")} note={t("tgproxy.copied")} />
        <Button size="md" aria-label={t("tgproxy.qr")} aria-expanded={qr} onClick={() => setQr(true)}>
          <G d={P.qr} size={16} />
        </Button>
      </span>
      <QrDialog open={qr} onOpenChange={setQr} title={t("up.qrTitle", { node: l.nodeName })} text={t("tgproxy.qrHint")} value={l.url} label={t("tgproxy.qrLabel", { node: l.nodeName })} copyLabel={t("up.copyLink")} />
    </div>
  );
}
