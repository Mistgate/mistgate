import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useState, type ReactNode } from "react";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { SectionLabel } from "@/components/ui/bits";
import { Button, buttonClass } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon, IconChip, type IconName, type Tone } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { useToast } from "@/components/ui/toast";
import { users } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { settingsQuery, useLinkAppNames } from "@/screens/subscriptions/queries";
import type { UserN } from "./model";
import { ConfirmModal } from "./ui";
import { QueryError } from "@/components/ui/query-error";
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

/**
 * QR code as a crisp SVG on a white card (scanners need the light quiet zone in both themes). The encoder
 * (qrcode-generator) loads on first use, so it stays out of the main bundle.
 */
function LinkQr({ value, label }: { value: string; label: string }) {
  const [cells, setCells] = useState<{ n: number; d: string } | null>(null);
  useEffect(() => {
    let live = true;
    void import("qrcode-generator").then(({ default: qrcode }) => {
      const qr = qrcode(0, "M");
      qr.addData(value);
      qr.make();
      const n = qr.getModuleCount();
      let d = "";
      for (let y = 0; y < n; y++) for (let x = 0; x < n; x++) if (qr.isDark(y, x)) d += `M${x} ${y}h1v1h-1z`;
      if (live) setCells({ n, d });
    });
    return () => {
      live = false;
    };
  }, [value]);
  const n = cells?.n ?? 29;
  return (
    <div className="flex-none self-center rounded-card border border-line bg-white p-3.5">
      <svg role="img" aria-label={label} viewBox={`0 0 ${n} ${n}`} shapeRendering="crispEdges" className="block size-[208px] max-w-full">
        {cells && <path d={cells.d} fill="#0c0c0e" />}
      </svg>
    </div>
  );
}

/**
 * "Link & QR": the job of this window is "send the friend what they need". One primary button copies a ready-to-paste
 * message (link, plus the page password when the instance asks for one; Share where the browser has it), the QR is
 * for a phone in the room, the rows copy one value each, and "New link" (asks first) replaces the pair. `initialUrl` and `initialPassword` are passed right after creating a user (the create call
 * already returned them); otherwise both are fetched when the modal opens and never cached (gcTime 0): they are secrets.
 */
export function LinkModal({ target, initialUrl, initialPassword, onClose }: { target: LinkTarget | null; initialUrl?: string; initialPassword?: string; onClose: () => void }) {
  const t = useTx();
  const apps = useLinkAppNames();
  // the body is mounted by the dialog only while it is open, so its state (new link, "Copied") starts fresh every
  // time; the last target is kept so the title does not go blank during the closing animation
  const [last, setLast] = useState(target);
  if (target && target.id !== last?.id) setLast(target);
  const shown = target ?? last;
  return (
    <Modal
      open={target !== null}
      onOpenChange={(o) => !o && onClose()}
      title={shown?.name ?? ""}
      description={shown?.happ && shown.amnezia ? t("users.qrSubBoth", { apps }) : shown?.happ ? t("users.qrSubHapp", { apps }) : t("users.qrSubAwg")}
      className="md:max-w-[440px]!"
      closeLabel={t("common.close")}
    >
      {shown && <LinkBody target={shown} initialUrl={initialUrl} initialPassword={initialPassword} />}
    </Modal>
  );
}

type Copied = "link" | "password" | "both" | null;

function LinkBody({ target, initialUrl, initialPassword }: { target: LinkTarget; initialUrl?: string; initialPassword?: string }) {
  const t = useTx();
  const toast = useToast();
  const [rotated, setRotated] = useState<{ url: string; password: string } | null>(null);
  const [askRotate, setAskRotate] = useState(false);
  const [copied, setCopied] = useState<Copied>(null);

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
      toast(t("users.rotated"));
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  const url = rotated?.url ?? initialUrl ?? fetched.data?.url ?? "";
  // "" when the instance does not ask for a password (the setting is off): then there is nothing to show
  const password = rotated ? rotated.password : initialUrl ? (initialPassword ?? "") : (fetched.data?.password ?? "");

  async function copy(text: string, what: Exclude<Copied, null>) {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(what);
      setTimeout(() => setCopied(null), 2000);
    } catch {
      toast(t("users.copyFailed"));
    }
  }

  // what the friend gets: both lines when there is a page password, else the bare link
  const message = password ? t("users.shareText", { url, password }) : url;
  const canShare = typeof navigator !== "undefined" && typeof navigator.share === "function";
  async function share() {
    try {
      await navigator.share({ text: message });
    } catch (e) {
      // closing the share sheet is not an error; anything else falls back to the clipboard
      if (!(e instanceof DOMException && e.name === "AbortError")) void copy(message, "both");
    }
  }

  if (fetched.isError && !initialUrl && !rotated) return <QueryError error={fetched.error} onRetry={() => void fetched.refetch()} />;
  const keysOnly = target.amnezia && !target.happ;
  return (
    <>
      <div className="flex flex-col items-center gap-2">
        <LinkQr value={url || " "} label={t("users.qrLabel")} />
        {keysOnly && <span className="max-w-[260px] text-center text-xs leading-snug text-muted">{t("users.qrHintAwg")}</span>}
      </div>
      {keysOnly && <SelfService target={target} />}
      <div className="flex flex-col gap-2">
        <ValueRow icon="link" tone="mint" label={t("users.linkQr")} copyLabel={t("users.copyUrl")} done={copied === "link"} disabled={!url} onCopy={() => void copy(url, "link")}>
          <MiddleText text={url} />
        </ValueRow>
        {password && (
          <ValueRow icon="lock" tone="lavender" label={t("users.pagePassword")} copyLabel={t("users.copyPassword")} done={copied === "password"} onCopy={() => void copy(password, "password")}>
            <span className="font-mono text-lg leading-tight font-bold tracking-[0.14em] break-all select-all">{password}</span>
          </ValueRow>
        )}
      </div>
      <div className="flex flex-col gap-2.5">
        <Nothing target={target} />
        <div className="flex gap-2">
          <Button variant="primary" size="lg" className="flex-1" disabled={!url} onClick={() => void copy(message, "both")}>
            <Icon name={copied === "both" ? "check" : "copy"} size={16} />
            {copied === "both" ? t("users.copied") : t("users.copyShare")}
          </Button>
          {canShare && (
            <Button variant="secondary" size="lg" disabled={!url} onClick={() => void share()} aria-label={t("users.share")} title={t("users.share")}>
              <Icon name="share" size={16} />
              <span className="max-[439px]:hidden">{t("users.share")}</span>
            </Button>
          )}
        </div>
        <p className="text-center text-xs leading-normal text-balance text-muted">{t("users.qrHint")}</p>
      </div>
      <span role="status" className="sr-only">
        {copied ? t("users.copied") : ""}
      </span>
      <details className="group rounded-xl border border-line">
        <summary className="flex h-10 cursor-pointer items-center gap-2 rounded-xl px-3 text-[13px] font-bold text-muted hover:text-fg [&::-webkit-details-marker]:hidden">
          <Icon name="info" size={14} />
          {t("users.howT")}
          <Icon name="chevronRight" size={14} className="ml-auto transition-transform duration-200 group-open:rotate-90" />
        </summary>
        <ul className="flex flex-col gap-2.5 px-3 pt-0.5 pb-3">
          {password && <HowItem icon="lock" tone="lavender" text={t("users.howPw")} />}
          <HowItem icon="refresh" tone="rose" text={password ? t("users.howRotatePw") : t("users.howRotate")} />
          {target.amnezia && <HowItem icon="key" tone="sage" text={t("users.qrAwgSelf", { setting: t("awg.page.selfService") })} />}
        </ul>
      </details>
      <div className="flex items-center gap-2 border-t border-line pt-3">
        {url && (
          <a href={url} target="_blank" rel="noopener noreferrer" className="inline-flex h-9 items-center gap-1.5 rounded-ctl px-3 text-[13px] font-bold text-accent-text hover:bg-surface-2">
            <Icon name="externalLink" size={14} />
            {t("users.openPage")}
          </a>
        )}
        <Button variant="ghostDanger" className="ml-auto" disabled={!url || rotate.isPending} onClick={() => setAskRotate(true)}>
          <Icon name="refresh" size={14} />
          {t("users.rotate")}
        </Button>
      </div>
      <ConfirmModal
        open={askRotate}
        onOpenChange={setAskRotate}
        title={t("users.rotateT")}
        description={password ? t("users.rotateBodyPw") : t("users.rotateBody")}
        confirmLabel={t("users.rotateDo")}
        danger
        onConfirm={() => rotate.mutateAsync()}
      />
    </>
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
        <Link to="/users" search={{ tab: "groups", group: g.id }} className={buttonClass("secondary", "sm")}>
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
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl bg-surface-2 py-2.5 pr-2.5 pl-3">
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

/** One value on a surface-2 field (label above it, a square copy button on the right); the check shows once copied. */
function ValueRow({
  icon,
  tone,
  label,
  copyLabel,
  done,
  disabled,
  onCopy,
  children,
}: {
  icon: IconName;
  tone: Tone;
  label: string;
  copyLabel: string;
  done: boolean;
  disabled?: boolean;
  onCopy: () => void;
  children: ReactNode;
}) {
  const t = useTx();
  const name = done ? t("users.copied") : copyLabel;
  return (
    <div className="flex items-center gap-3 rounded-xl bg-surface-2 py-2.5 pr-2.5 pl-3">
      <IconChip icon={icon} tone={tone} size={28} />
      <div className="flex min-w-0 flex-1 flex-col gap-1 text-left">
        <SectionLabel as="span">{label}</SectionLabel>
        {children}
      </div>
      <IconButton variant="field" aria-label={name} title={name} disabled={disabled} onClick={onCopy}>
        <Icon name={done ? "check" : "copy"} size={16} className={done ? "text-accent-text" : undefined} />
      </IconButton>
    </div>
  );
}

/** A long link cut in the middle: the start (the host) shrinks with an ellipsis, the last characters (the token) stay. */
function MiddleText({ text, keep = 10 }: { text: string; keep?: number }) {
  const cut = text.length > keep * 2 ? text.length - keep : text.length;
  return (
    <span title={text} className="flex min-w-0 font-mono text-xs text-fg select-all">
      <span className="min-w-0 truncate">{text.slice(0, cut)}</span>
      <span className="flex-none">{text.slice(cut)}</span>
    </span>
  );
}

function HowItem({ icon, tone, text }: { icon: IconName; tone: Tone; text: string }) {
  return (
    <li className="flex items-start gap-2.5 text-xs leading-normal text-muted">
      <IconChip icon={icon} tone={tone} size={22} />
      <span className="min-w-0 pt-[3px]">{text}</span>
    </li>
  );
}
