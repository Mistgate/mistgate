import { useQuery } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import { useEffect, useRef, useState, type KeyboardEventHandler, type ReactNode, type Ref } from "react";
import { useT } from "@/i18n";
import { basepath } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useStaleVersion } from "@/lib/stale";
import { readPref, writePref } from "@/lib/storage";
import { updateNotices, updatesQuery, useIsOwner, type UpdateNotice } from "@/lib/updates";
import { buttonClass } from "./ui/button";
import { Icon, type IconName } from "./ui/icons";
import { IconButton } from "./ui/icon-button";

// The owner learns about a release without opening Updates: a card in the corner of every page but that one. The status
// is the one the panel already caches (GetUpdates; its own GitHub check runs every 10 minutes), so this asks the panel, never GitHub.
// Closing remembers the version it showed (a notice of another version shows again); collapsing turns the card into a pill.
// Both are per-browser conveniences in localStorage, best-effort like every preference (lib/storage.ts).
// The corner, the card and the pill are the admin's one notice style: stale-toast.tsx ("the panel was updated: reload") uses them too.

/** Where a notice sits: bottom right on a desktop, full width above the dock on a phone. */
export const toastCorner = "pointer-events-none fixed inset-x-4 bottom-[88px] z-20 flex justify-end md:inset-x-auto md:right-5 md:bottom-5";

const pollMs = 3 * 60_000;
const collapsedKey = "update-toast-collapsed";
const dismissedKey = (kind: UpdateNotice["kind"]) => `update-toast-dismissed-${kind}`;

const vee = (v: string) => (/^v/i.test(v) ? v : `v${v}`);

export function UpdateToast() {
  const t = useT();
  const owner = useIsOwner();
  const path = useRouterState({ select: (s) => s.location.pathname }).replace(basepath.replace(/\/$/, ""), "");
  const onUpdates = path.startsWith("/updates"); // that page says it itself (and polls faster)
  // a stale tab reloads first (stale-toast.tsx takes the corner): what this tab would say comes from the old JS
  const stale = useStaleVersion() !== "";
  const active = owner && !onUpdates && !stale;
  const { data } = useQuery({ ...updatesQuery, enabled: active, refetchInterval: pollMs });
  const [dismissed, setDismissed] = useState(() => ({ panel: readPref(dismissedKey("panel")), nodes: readPref(dismissedKey("nodes")) }));
  const [collapsed, setCollapsed] = useState(() => readPref(collapsedKey) === "1");
  // the button that was pressed unmounts (card <-> pill, or the card goes away), and the focus would fall to the page top
  const pill = useRef<HTMLButtonElement>(null);
  const card = useRef<HTMLElement>(null);
  const refocus = useRef(false);
  useEffect(() => {
    if (!refocus.current) return;
    refocus.current = false;
    (pill.current ?? card.current ?? document.getElementById("main"))?.focus();
  });

  if (!active) return null;
  const notice = updateNotices(data).find((n) => dismissed[n.kind] !== n.id);

  const collapse = (value: boolean) => {
    refocus.current = true;
    setCollapsed(value);
    writePref(collapsedKey, value ? "1" : null);
  };
  const close = (n: UpdateNotice) => {
    refocus.current = true;
    setDismissed((d) => ({ ...d, [n.kind]: n.id }));
    writePref(dismissedKey(n.kind), n.id);
  };

  // the live region stays in the page while it is empty, so what appears in it is announced
  return (
    <div role="status" aria-live="polite" className={toastCorner}>
      {notice &&
        (collapsed ? (
          <ToastPill
            ref={pill}
            onClick={() => collapse(false)}
            label={t("common.labeled", { label: t("up.toast.expand"), value: vee(notice.version) })}
            title={t("up.toast.expand")}
            mark={<span aria-hidden className="size-2 flex-none animate-[mg-ui-blink_2s_infinite] rounded-full bg-accent" />}
          >
            {vee(notice.version)}
          </ToastPill>
        ) : (
          <Card ref={card} notice={notice} onCollapse={() => collapse(true)} onClose={() => close(notice)} />
        ))}
    </div>
  );
}

function Card({ ref, notice, onCollapse, onClose }: { ref: Ref<HTMLElement>; notice: UpdateNotice; onCollapse: () => void; onClose: () => void }) {
  const t = useT();
  const version = vee(notice.version);
  const panel = notice.kind === "panel";
  return (
    <ToastCard
      ref={ref}
      label={t("up.toast.label")}
      icon="arrowUp"
      title={t(panel ? "up.toast.panelTitle" : "up.toast.nodesTitle", { version })}
      text={panel ? t(notice.installable ? "up.toast.panelText" : "up.toast.panelManual") : t("up.toast.nodesText", { n: notice.outdated, total: notice.total })}
      corner={
        <>
          <ToastCornerButton icon="chevronDown" label={t("up.toast.collapse")} onClick={onCollapse} />
          <ToastCornerButton icon="x" label={t("up.toast.close")} onClick={onClose} />
        </>
      }
    >
      <div className={cx("grid gap-2", panel && notice.url ? "grid-cols-2" : "grid-cols-1")}>
        <Link to="/updates" className={buttonClass("primary", "lg", true)}>
          {t("up.toast.update")}
        </Link>
        {panel && notice.url && (
          <a href={notice.url} target="_blank" rel="noreferrer noopener" className={buttonClass("secondary", "lg", true)}>
            {t("up.toast.news")}
            <Icon name="externalLink" size={14} />
          </a>
        )}
      </div>
    </ToastCard>
  );
}

/** A notice card: an icon, a title, one line of text, the small buttons in its corner, and its actions under it. */
export function ToastCard({ ref, label, icon, title, text, corner, onKeyDown, children }: {
  ref: Ref<HTMLElement>;
  label: string;
  icon: IconName;
  title: string;
  text: string;
  corner: ReactNode;
  onKeyDown?: KeyboardEventHandler;
  children: ReactNode;
}) {
  return (
    <section
      ref={ref}
      tabIndex={-1}
      aria-label={label}
      onKeyDown={onKeyDown}
      className="screen-enter pointer-events-auto flex w-full min-w-0 flex-col gap-3.5 rounded-card-lg border border-accent-line bg-surface p-4 shadow-(--shadow-toast) md:w-[360px]"
    >
      <div className="flex items-start gap-3">
        <span aria-hidden className="grid size-9 flex-none place-items-center rounded-full bg-accent-soft text-accent-text">
          <Icon name={icon} size={18} />
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-1 pt-px">
          <h2 className="text-[15px] leading-snug font-extrabold tracking-[-0.02em] text-balance break-words">{title}</h2>
          <p className="text-[13px] leading-snug text-pretty text-muted">{text}</p>
        </div>
        {/* 44px touch targets that sit in the corner without making the card taller */}
        <div className="-mt-2.5 -mr-2.5 flex flex-none">{corner}</div>
      </div>
      {children}
    </section>
  );
}

export function ToastCornerButton({ icon, label, onClick }: { icon: IconName; label: string; onClick: () => void }) {
  return (
    <IconButton variant="flat" aria-label={label} title={label} onClick={onClick} className="size-11 rounded-full">
      <Icon name={icon} size={icon === "x" ? 14 : 16} />
    </IconButton>
  );
}

/** A collapsed notice: a pill with a mark and a short text that opens the card again. */
export function ToastPill({ ref, label, title, mark, onClick, children }: { ref: Ref<HTMLButtonElement>; label: string; title: string; mark: ReactNode; onClick: () => void; children: ReactNode }) {
  return (
    <button
      ref={ref}
      type="button"
      onClick={onClick}
      aria-label={label}
      title={title}
      className="pop-in pointer-events-auto flex h-11 max-w-full items-center gap-2.5 rounded-full border border-accent-line bg-surface pr-4 pl-3.5 font-mono text-xs font-bold shadow-(--shadow-toast) transition-transform duration-200 ease-spring active:scale-[0.97]"
    >
      {mark}
      <span className="truncate">{children}</span>
    </button>
  );
}
