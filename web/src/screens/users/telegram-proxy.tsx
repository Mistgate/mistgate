import { ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { DeviceKind, type GetTelegramLinksResponse, type ProfileRef, type TelegramProxyLink } from "@/gen/mistgate/admin/v1/user_pb";
import { QrCode } from "@/components/qr-code";
import { Button, buttonClass } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { useToast } from "@/components/ui/toast";
import { users } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorCode, errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { DeviceRow, RowMenu, SectionHead, Secondary, Tile } from "./link-devices";
import type { DeviceN as Device, UserN as User } from "./model";
import { useTx, type Tx } from "./t";
import { ConfirmModal, SwitchRow } from "./ui";

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

  return (
    <section className="flex flex-col">
      <SectionHead title={t("tgproxy.title")} />
      {!on ? (
        <SwitchRow label={t("tgproxy.toggle")} hint={t("tgproxy.off")} checked={false} onCheckedChange={(v) => v && onTurnOn()} />
      ) : q.isPending ? (
        <p className="py-2.5 text-xs text-muted">{t("common.loading")}</p>
      ) : q.isError ? (
        <p className="py-2.5 text-xs text-danger-text">{errorText(q.error, t)}</p>
      ) : !q.data.hasKey ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 py-2.5">
          <span className="min-w-0 flex-1 text-xs leading-snug text-muted">{t("tgproxy.noKey")}</span>
          <Button size="sm" disabled={busy} onClick={issue}>
            <Icon name="key" size={12} />
            {t("tgproxy.issue")}
          </Button>
        </div>
      ) : (
        <ul className="flex flex-col">
          <DeviceRow
            tile={<Tile icon="plane" tone="rose" />}
            name={t("tgproxy.key")}
            line={<Secondary text={t("tgproxy.keyLine", { date: fmt.date(Number(q.data.keyIssuedUnix)) })} />}
            menu={
              <RowMenu
                label={t("tgproxy.keyMenu")}
                items={[
                  { label: t("tgproxy.rotate"), onClick: () => askFor("rotate") },
                  { label: t("tgproxy.revoke"), onClick: () => askFor("revoke"), danger: true },
                ]}
              />
            }
          />
          {q.data.links.map((l) => (
            <LinkRow key={`${l.nodeId}/${l.profileId}/${l.ipv6 ? "6" : l.relay ? `via-${l.via}` : "4"}`} l={l} cc={countryOf(l.nodeId)} t={t} />
          ))}
          {q.data.links.length === 0 && (
            <li className="border-t border-line py-2.5 text-xs leading-snug text-muted">
              {t("tgproxy.noLinks")}{" "}
              <Link to="/profiles" search={{ protocol: "mtproto" } as never} className="font-bold whitespace-nowrap text-accent-text hover:underline">
                {t("tgproxy.toProfiles")}
              </Link>
            </li>
          )}
        </ul>
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

/** One tg://proxy link: the node's flag and name with the twin's mark, the profile, and the three ways to hand it over. */
function LinkRow({ l, cc, t }: { l: TelegramProxyLink; cc: string; t: Tx }) {
  const toast = useToast();
  const [qr, setQr] = useState(false);
  const marks = [l.ipv6 && "IPv6", l.relay && t("tgproxy.via", { cc: l.via || "?" })].filter((x): x is string => !!x);
  const copy = () => navigator.clipboard.writeText(l.url).then(() => toast(t("tgproxy.copied")), () => toast.error(t("err.generic")));
  return (
    <li className="flex flex-col border-t border-line first:border-t-0">
      <div className="flex min-h-[60px] items-center gap-3 py-2.5">
        <span aria-hidden className="grid size-9 flex-none place-items-center rounded-[11px] bg-surface-2">
          <Flag code={cc} size={16} />
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex min-w-0 items-center gap-1.5">
            <b className="truncate text-[13px] leading-tight">{l.nodeName}</b>
            {marks.map((m) => (
              <span key={m} className="flex-none rounded-md bg-surface-2 px-1.5 py-px text-[10px] font-extrabold text-muted">
                {m}
              </span>
            ))}
          </span>
          <span className="truncate text-xs leading-snug text-muted">{l.profileName}</span>
        </div>
        <div className="flex flex-none items-center gap-1">
          <a href={l.url} className={buttonClass("secondary", "sm")}>
            <Icon name="plane" size={12} />
            <span className="@max-[420px]:sr-only">{t("tgproxy.open")}</span>
          </a>
          <IconButton variant="flat" aria-label={t("tgproxy.copy")} onClick={() => void copy()}>
            <Icon name="copy" size={15} />
          </IconButton>
          <IconButton variant="flat" aria-label={t("tgproxy.qr")} aria-expanded={qr} className={cx(qr && "bg-surface-2 text-fg")} onClick={() => setQr(!qr)}>
            <Icon name="qr" size={15} />
          </IconButton>
        </div>
      </div>
      {qr && (
        <div className="screen-enter flex flex-wrap items-center gap-x-4 gap-y-2 pb-3 pl-12">
          <QrCode value={l.url} label={t("tgproxy.qrLabel", { node: l.nodeName })} size={140} />
          <p className="max-w-[260px] text-xs leading-snug text-pretty text-muted">{t("tgproxy.qrHint")}</p>
        </div>
      )}
    </li>
  );
}
