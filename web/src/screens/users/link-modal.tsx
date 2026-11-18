import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { Avatar, avatarIndex } from "@/components/ui/bits";
import { CopyButton } from "@/components/copy-button";
import { QrCode, QrPending } from "@/components/qr-code";
import { Button, buttonClass } from "@/components/ui/button";
import { IconChip } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { useToast } from "@/components/ui/toast";
import { users } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { settingsQuery, useLinkAppNames } from "@/screens/subscriptions/queries";
import type { UserN } from "./model";
import { G, P } from "./user-bits";
import { QueryError } from "@/components/ui/query-error";
import { plainText } from "@/i18n";
import { useTx } from "./t";

/**
 * Whom the window is for: the apps switched on, and (when known) what the person's page offers now (`access`) and the
 * group, so an empty page is said before the link goes out.
 */
export type LinkTarget = {
  id: string;
  name: string;
  happ: boolean;
  amnezia: boolean;
  /** Active and the page offers nothing: the red line. Absent = unknown, nothing is said. */
  access?: { happ: boolean; amnezia: boolean };
  active?: boolean;
  group?: { id: string; name: string; givesNothing: boolean };
};

export const linkTargetOf = (u: UserN, group?: Group): LinkTarget & { access: { happ: boolean; amnezia: boolean } } => ({
  id: u.id,
  name: u.name,
  happ: !!u.apps?.happ,
  amnezia: !!u.apps?.amnezia,
  access: { happ: u.accessHapp, amnezia: u.accessAmnezia },
  active: u.status === UserStatus.ACTIVE,
  group: { id: u.groupId, name: u.groupName, givesNothing: !group || (group.happNodes === 0 && group.amneziaNodes === 0) },
});

// The glyphs of the board's send window that the panel's sets do not draw the same way.
const D = {
  link: "M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1",
  share: "M12 15V4M8 8l4-4 4 4M5 13v6h14v-6",
  external: "M8 16L16 8M9 8h7v7",
  refresh: "M19 12a7 7 0 1 1-2.05-4.95M19.3 4.75v3.8h-3.8",
  chevron: "M9 6l6 6-6 6",
};
const glyph = (d: string) => <G d={d} size={16} stroke={1.5} />;

/**
 * "QR and send" (the board's send window): the job of this window is "send the friend what they need". The QR is for a phone in
 * the room, the two rows copy one value each, the primary button copies a ready-to-paste message (the link, and the page password
 * when the instance asks for one; Share where the browser has it), and "New link" (asks first, in the window) replaces the pair.
 * `initialUrl` and `initialPassword` are passed right after creating a user (the create call already returned them); otherwise both
 * are fetched when the window opens and never cached (gcTime 0): they are secrets.
 */
export function LinkModal({ target, initialUrl, initialPassword, onClose }: { target: LinkTarget | null; initialUrl?: string; initialPassword?: string; onClose: () => void }) {
  // the window is built anew for every opening (its state, the new link and "Copied", starts fresh); the last target is kept so the
  // title does not go blank during the closing animation
  const [last, setLast] = useState(target);
  const [opening, setOpening] = useState(0);
  if (target && (target.id !== last?.id || !last)) setLast(target);
  const [was, setWas] = useState(target !== null);
  if (was !== (target !== null)) {
    setWas(target !== null);
    if (target) setOpening(opening + 1);
  }
  const shown = target ?? last;
  if (!shown) return null;
  return <LinkWindow key={`${opening}-${shown.id}`} open={target !== null} target={shown} initialUrl={initialUrl} initialPassword={initialPassword} onClose={onClose} />;
}

function LinkWindow({ open, target, initialUrl, initialPassword, onClose }: { open: boolean; target: LinkTarget; initialUrl?: string; initialPassword?: string; onClose: () => void }) {
  const t = useTx();
  const toast = useToast();
  const apps = useLinkAppNames();
  const [rotated, setRotated] = useState<{ url: string; password: string } | null>(null);
  const [ask, setAsk] = useState(false);
  const [replaced, setReplaced] = useState(false);

  const fetched = useQuery({
    queryKey: ["users", "link", target.id],
    queryFn: async ({ signal }) => {
      const r = await users.getSubscriptionLink({ userId: target.id }, { signal });
      return { url: r.url, password: r.pagePassword };
    },
    enabled: !initialUrl,
    gcTime: 0,
    staleTime: 0,
  });
  const rotate = useMutation({
    mutationFn: async () => {
      const r = await users.getSubscriptionLink({ userId: target.id, rotate: true });
      return { url: r.url, password: r.pagePassword };
    },
    onSuccess: (r) => {
      setRotated(r);
      setAsk(false);
      setReplaced(true);
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  const url = rotated?.url ?? initialUrl ?? fetched.data?.url ?? "";
  // "" when the instance does not ask for a password (the setting is off): then there is nothing to show
  const password = rotated ? rotated.password : initialUrl ? (initialPassword ?? "") : (fetched.data?.password ?? "");

  // what the friend gets: both lines when there is a page password, else the bare link
  const message = password ? t("users.shareText", { url, password }) : url;
  const canShare = typeof navigator !== "undefined" && typeof navigator.share === "function";
  async function share() {
    try {
      await navigator.share({ text: plainText(message) });
    } catch (e) {
      // closing the share sheet is not an error; anything else falls back to the clipboard
      if (e instanceof DOMException && e.name === "AbortError") return;
      try {
        await navigator.clipboard.writeText(plainText(message));
        toast(t("users.copied"));
      } catch {
        toast(t("common.copyFailed"));
      }
    }
  }

  const keysOnly = target.amnezia && !target.happ;
  const error = fetched.isError && !initialUrl && !rotated;
  return (
    <Modal
      open={open}
      onOpenChange={(o) => !o && onClose()}
      title={target.name}
      description={target.happ && target.amnezia ? t("users.qrSubBoth", { apps }) : target.happ ? t("users.qrSubHapp", { apps }) : t("users.qrSubAwg")}
      lead={<Avatar name={target.name} index={avatarIndex(target.name)} size={28} fontSize={12} />}
      className="md:max-w-[480px]!"
      closeLabel={t("common.close")}
      shot="link-modal"
      footer={
        error ? undefined : (
          <>
            {url && (
              <a href={url} target="_blank" rel="noopener noreferrer" className="me-auto inline-flex items-center gap-1.5 text-sm text-fg underline decoration-line underline-offset-4 hover:decoration-current">
                {glyph(D.external)}
                {t("users.openPage")}
              </a>
            )}
            <Button size="lg" disabled={!url || rotate.isPending} aria-disabled={ask || undefined} className={cx("ms-auto", ask && "opacity-45")} onClick={() => setAsk(true)}>
              {glyph(D.refresh)}
              {t("users.rotate")}
            </Button>
          </>
        )
      }
    >
      {error ? (
        <QueryError error={fetched.error} onRetry={() => void fetched.refetch()} />
      ) : (
        <>
          <div className="mb-1 flex flex-col items-center gap-2">
            <div className="flex-none self-center">{url ? <QrCode value={url} label={t("users.qrLabel")} size={216} /> : <QrPending size={216} />}</div>
            {keysOnly && <span className="max-w-[260px] text-center text-xs leading-snug text-muted">{t("users.qrHintAwg")}</span>}
          </div>
          {keysOnly && <SelfService target={target} />}
          <Row icon={D.link} label={t("users.linkQr")} action={<CopyButton iconOnly variant="outline" size="md" label={t("users.copyUrl")} value={url} />}>
            <MiddleText text={url} />
          </Row>
          <Row
            icon={P.lock}
            set={!!password}
            label={t("users.pagePassword")}
            action={password ? <CopyButton iconOnly variant="outline" size="md" label={t("users.copyPassword")} value={password} /> : <span aria-hidden />}
          >
            {password ? (
              <code className="font-mono text-sm leading-[18px] font-medium tracking-[0.04em] break-all text-fg select-all">{password}</code>
            ) : (
              <span className="text-xs leading-[18px] text-muted">{t("ul.pw.none")}</span>
            )}
          </Row>
          <Nothing target={target} />
          <div className="mt-1 grid grid-cols-[minmax(0,1fr)_auto] gap-2">
            <CopyButton variant="primary" size="lg" full label={t("users.copyShare")} value={message} />
            {canShare && (
              // on a phone the word steps aside, so the long main button keeps its room
              <Button size="lg" disabled={!url} aria-label={t("users.share")} title={t("users.share")} className="@max-[420px]/win:w-10 @max-[420px]/win:px-0" onClick={() => void share()}>
                {glyph(D.share)}
                <span className="@max-[420px]/win:hidden">{t("users.share")}</span>
              </Button>
            )}
          </div>
          <p className="text-center text-sm leading-6 text-balance text-sec">{replaced ? t("users.sendDone") : password ? t("users.sendNotePw") : t("users.sendNote")}</p>
          <details className="group rounded-xl border border-line">
            <summary className="flex min-h-11 cursor-pointer items-center gap-2.5 rounded-xl px-3 text-[13px] text-sec [&::-webkit-details-marker]:hidden">
              <span className="text-muted">
                <G d={P.info} size={16} stroke={1.5} />
              </span>
              <span className="flex-1">{t("users.howT")}</span>
              <span className="text-muted transition-transform duration-300 ease-in-out group-open:rotate-90 rtl:-scale-x-100 rtl:group-open:-rotate-90">{glyph(D.chevron)}</span>
            </summary>
            <ol className="flex list-decimal flex-col gap-1.5 pt-0 pe-3 pb-3 ps-8 text-[13px] leading-[18px] text-sec">
              <li>{t("users.how1")}</li>
              <li>{t("users.how2")}</li>
              {target.amnezia && <li>{t("users.how3")}</li>}
            </ol>
          </details>
          {ask && <ReplaceAsk pw={!!password} busy={rotate.isPending} onNo={() => setAsk(false)} onYes={() => rotate.mutate()} />}
        </>
      )}
    </Modal>
  );
}

/** "Replace the link?" in the window itself, in red, with its own two buttons (the board's .us-sw); it scrolls into view when it opens. */
function ReplaceAsk({ pw, busy, onNo, onYes }: { pw: boolean; busy: boolean; onNo: () => void; onYes: () => void }) {
  const t = useTx();
  const el = useRef<HTMLDivElement>(null);
  useEffect(() => {
    el.current?.scrollIntoView?.({ block: "nearest" });
  }, []);
  return (
    <div ref={el} role="alert" className="grid grid-cols-[minmax(0,1fr)_auto_auto] items-center gap-2 rounded-xl bg-danger-soft p-3 @max-[400px]/win:grid-cols-[minmax(0,1fr)_auto]">
      <div className="flex min-w-0 flex-col gap-0.5 @max-[400px]/win:col-span-full">
        <b className="text-[13px] font-medium">{t("users.swT")}</b>
        <span className="text-xs leading-[17px] text-sec">{t(pw ? "users.swBodyPw" : "users.swBody")}</span>
      </div>
      <Button size="md" disabled={busy} onClick={onNo}>
        {t("users.cancel")}
      </Button>
      <Button size="md" variant="primary" disabled={busy} className="bg-danger! hover:bg-danger!" onClick={onYes}>
        {t("users.swYes")}
      </Button>
    </div>
  );
}

/**
 * An active person whose page gives nothing yet: said in red above the button, with the way to the group. Without it the
 * friend would open an empty page and the owner would hear it from them.
 */
function Nothing({ target }: { target: LinkTarget }) {
  const t = useTx();
  const a = target.access;
  if (!a || a.happ || a.amnezia || target.active === false || !target.group) return null;
  const g = target.group;
  return (
    <Notice tone="danger" className="items-start">
      <span className="flex flex-col items-start gap-2">
        <span>{t(g.givesNothing ? "users.linkNothing" : "users.linkNothingApps", { name: target.name, group: g.name })}</span>
        <Link to="/users/groups/$id" params={{ id: g.id }} className={buttonClass("secondary", "sm")}>
          {t("users.groupOpen", { name: g.name })}
        </Link>
      </span>
    </Notice>
  );
}

/**
 * A person of AmneziaVPN keys only: whether the page lets them make the key themselves (the subscription settings say),
 * and when it does not, the way to make one in the card.
 */
function SelfService({ target }: { target: LinkTarget }) {
  const t = useTx();
  const s = useQuery({ ...settingsQuery, retry: false });
  if (!s.data) return null;
  const on = s.data.settings.userPage?.allowDeviceSelfService ?? true; // absent = on (settings older than the switch)
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl bg-surface-2 py-2.5 pe-2.5 ps-3">
      <IconChip icon="key" tone="sage" size={28} />
      <span className="min-w-0 flex-1 text-xs leading-snug">{t(on ? "users.selfOn" : "users.selfOff")}</span>
      {!on && (
        <Link to="/users/$id" params={{ id: target.id }} search={{ add: "device" }} className={buttonClass("secondary", "sm")}>
          {t("awg.dev.add")}
        </Link>
      )}
    </div>
  );
}

/**
 * One value of the window (the board's .us-sr): a 32px glyph (green when the value is set), the label with the value under it,
 * and the copy button at the end. The third column stays when there is no button, as on the board.
 */
function Row({ icon, set, label, action, children }: { icon: string; set?: boolean; label: string; action: ReactNode; children: ReactNode }) {
  return (
    <div className="grid min-h-14 grid-cols-[32px_minmax(0,1fr)_auto] items-center gap-3 rounded-xl border border-line bg-canvas py-2 ps-3 pe-2">
      <span aria-hidden className={cx("grid size-8 place-items-center rounded-[9px]", set ? "bg-tone-mint-bg text-accent-text" : "bg-step text-sec")}>
        <G d={icon} size={16} stroke={1.5} />
      </span>
      <div className="flex min-w-0 flex-col gap-0.5 text-start">
        <b className="text-[13px] leading-[18px] font-medium">{label}</b>
        {children}
      </div>
      {action}
    </div>
  );
}

/** A long link cut in the middle: the start (the host) shrinks with an ellipsis, the last characters (the token) stay. */
function MiddleText({ text, keep = 10 }: { text: string; keep?: number }) {
  const cut = text.length > keep * 2 ? text.length - keep : text.length;
  return (
    <span title={text} dir="ltr" className="flex min-w-0 font-mono text-xs leading-[18px] font-medium text-sec select-all">
      <span className="min-w-0 truncate">{text.slice(0, cut)}</span>
      <span className="flex-none">{text.slice(cut)}</span>
    </span>
  );
}
