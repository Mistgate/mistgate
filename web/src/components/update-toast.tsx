import { useQuery } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import { useState } from "react";
import { useT } from "@/i18n";
import { basepath } from "@/lib/api";
import { cx } from "@/lib/cx";
import { readPref, writePref } from "@/lib/storage";
import { updateNotices, updatesQuery, useIsOwner, type UpdateNotice } from "@/lib/updates";
import { buttonClass } from "./ui/button";
import { Icon } from "./ui/icons";
import { IconButton } from "./ui/icon-button";

// The owner learns about a release without opening Updates: a card in the corner of every page but that one. The status
// is the one the panel already caches (GetUpdates; its own GitHub check runs every 10 minutes), so this asks the panel, never GitHub.
// Closing remembers the version it showed (a notice of another version shows again); collapsing turns the card into a pill.
// Both are per-browser conveniences in localStorage, best-effort like every preference (lib/storage.ts).

const pollMs = 3 * 60_000;
const collapsedKey = "update-toast-collapsed";
const dismissedKey = (kind: UpdateNotice["kind"]) => `update-toast-dismissed-${kind}`;

const vee = (v: string) => (/^v/i.test(v) ? v : `v${v}`);

export function UpdateToast() {
  const t = useT();
  const owner = useIsOwner();
  const path = useRouterState({ select: (s) => s.location.pathname }).replace(basepath.replace(/\/$/, ""), "");
  const onUpdates = path.startsWith("/updates"); // that page says it itself (and polls faster)
  const active = owner && !onUpdates;
  const { data } = useQuery({ ...updatesQuery, enabled: active, refetchInterval: pollMs });
  const [dismissed, setDismissed] = useState(() => ({ panel: readPref(dismissedKey("panel")), nodes: readPref(dismissedKey("nodes")) }));
  const [collapsed, setCollapsed] = useState(() => readPref(collapsedKey) === "1");

  if (!active) return null;
  const notice = updateNotices(data).find((n) => dismissed[n.kind] !== n.id);

  const collapse = (value: boolean) => {
    setCollapsed(value);
    writePref(collapsedKey, value ? "1" : null);
  };
  const close = (n: UpdateNotice) => {
    setDismissed((d) => ({ ...d, [n.kind]: n.id }));
    writePref(dismissedKey(n.kind), n.id);
  };

  // the live region stays in the page while it is empty, so what appears in it is announced
  return (
    <div
      role="status"
      aria-live="polite"
      className="pointer-events-none fixed inset-x-4 bottom-[88px] z-20 flex justify-end md:inset-x-auto md:right-5 md:bottom-5"
    >
      {notice &&
        (collapsed ? (
          <button
            type="button"
            onClick={() => collapse(false)}
            aria-label={`${t("up.toast.expand")}: ${vee(notice.version)}`}
            title={t("up.toast.expand")}
            className="pop-in pointer-events-auto flex h-11 max-w-full items-center gap-2.5 rounded-full border border-accent-line bg-surface pr-4 pl-3.5 font-mono text-xs font-bold shadow-(--shadow-toast) transition-transform duration-200 ease-spring active:scale-[0.97]"
          >
            <span aria-hidden className="size-2 flex-none animate-[mg-ui-blink_2s_infinite] rounded-full bg-accent" />
            <span className="truncate">{vee(notice.version)}</span>
          </button>
        ) : (
          <Card notice={notice} onCollapse={() => collapse(true)} onClose={() => close(notice)} />
        ))}
    </div>
  );
}

function Card({ notice, onCollapse, onClose }: { notice: UpdateNotice; onCollapse: () => void; onClose: () => void }) {
  const t = useT();
  const version = vee(notice.version);
  const panel = notice.kind === "panel";
  return (
    <section
      aria-label={t("up.toast.label")}
      className="screen-enter pointer-events-auto flex w-full min-w-0 flex-col gap-3.5 rounded-card-lg border border-accent-line bg-surface p-4 shadow-(--shadow-toast) md:w-[360px]"
    >
      <div className="flex items-start gap-3">
        <span aria-hidden className="grid size-9 flex-none place-items-center rounded-full bg-accent-soft text-accent-text">
          <Icon name="arrowUp" size={18} />
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-1 pt-px">
          <h2 className="text-[15px] leading-snug font-extrabold tracking-[-0.02em] text-balance break-words">
            {t(panel ? "up.toast.panelTitle" : "up.toast.nodesTitle", { version })}
          </h2>
          <p className="text-[13px] leading-snug text-pretty text-muted">
            {panel
              ? t(notice.installable ? "up.toast.panelText" : "up.toast.panelManual")
              : t("up.toast.nodesText", { n: notice.outdated, total: notice.total })}
          </p>
        </div>
        {/* 44px touch targets that sit in the corner without making the card taller */}
        <div className="-mt-2.5 -mr-2.5 flex flex-none">
          <IconButton variant="flat" aria-label={t("up.toast.collapse")} title={t("up.toast.collapse")} onClick={onCollapse} className="size-11 rounded-full">
            <Icon name="chevronDown" size={16} />
          </IconButton>
          <IconButton variant="flat" aria-label={t("up.toast.close")} title={t("up.toast.close")} onClick={onClose} className="size-11 rounded-full">
            <Icon name="x" size={14} />
          </IconButton>
        </div>
      </div>
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
    </section>
  );
}
