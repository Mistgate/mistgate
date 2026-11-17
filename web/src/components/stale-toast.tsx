import { useEffect, useRef, useState, type KeyboardEventHandler, type ReactNode, type Ref } from "react";
import { useT } from "@/i18n";
import { reloadPage, useStaleVersion } from "@/lib/stale";
import { Button } from "./ui/button";
import { IconButton } from "./ui/icon-button";
import { Icon, type IconName } from "./ui/icons";

/** Where the notice sits: bottom right on a desktop, full width on a phone, in both above the assistant's button. */
const toastCorner = "pointer-events-none fixed inset-x-4 bottom-[172px] z-20 flex justify-end md:inset-x-auto md:inset-e-8 md:bottom-24";

/**
 * "The panel was updated: reload", in the corner of every page, for a tab that outlived a panel update (lib/stale.ts): the old JS cannot be trusted with the new panel's
 * schemas. Collapsing it (the chevron, or Esc inside it) leaves a pill for this page load, never nothing; after a reload that
 * is still stale the card is back. Nothing at all while the tab is current.
 */
export function StaleToast() {
  const version = useStaleVersion();
  // the live region stays in the page while it is empty, so what appears in it is announced
  return (
    <div role="status" aria-live="polite" className={toastCorner}>
      {version && <StaleToastView version={version} />}
    </div>
  );
}

export function StaleToastView({ version }: { version: string }) {
  const t = useT();
  const v = /^v/i.test(version) ? version : `v${version}`;
  const [collapsed, setCollapsed] = useState(false);
  // the pressed control unmounts (card <-> pill): the focus goes to what replaced it, not to the top of the page
  const pill = useRef<HTMLButtonElement>(null);
  const card = useRef<HTMLElement>(null);
  const refocus = useRef(false);
  useEffect(() => {
    if (!refocus.current) return;
    refocus.current = false;
    (pill.current ?? card.current)?.focus();
  });
  const collapse = (value: boolean) => {
    refocus.current = true;
    setCollapsed(value);
  };

  if (collapsed) {
    return (
      <ToastPill
        ref={pill}
        onClick={() => collapse(false)}
        label={t("common.labeled", { label: t("stale.expand"), value: v })}
        title={t("stale.expand")}
        mark={<Icon name="refresh" size={14} className="flex-none text-accent-text" />}
      >
        {v}
      </ToastPill>
    );
  }
  const title = t("stale.title", { version: v });
  return (
    <ToastCard
      ref={card}
      label={title}
      icon="refresh"
      title={title}
      text={t("stale.msg")}
      corner={<ToastCornerButton icon="chevronDown" label={t("up.toast.collapse")} onClick={() => collapse(true)} />}
      onKeyDown={(e) => {
        if (e.key !== "Escape") return;
        e.stopPropagation();
        collapse(true);
      }}
    >
      <Button variant="primary" size="lg" full onClick={reloadPage}>
        {t("stale.reload")}
      </Button>
    </ToastCard>
  );
}

/** A notice card: an icon, a title, one line of text, the small buttons in its corner, and its actions under it. */
function ToastCard({ ref, label, icon, title, text, corner, onKeyDown, children }: {
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
        <div className="-mt-2.5 -me-2.5 flex flex-none">{corner}</div>
      </div>
      {children}
    </section>
  );
}

function ToastCornerButton({ icon, label, onClick }: { icon: IconName; label: string; onClick: () => void }) {
  return (
    <IconButton variant="flat" aria-label={label} title={label} onClick={onClick} className="size-11 rounded-full">
      <Icon name={icon} size={icon === "x" ? 14 : 16} />
    </IconButton>
  );
}

/** A collapsed notice: a pill with a mark and a short text that opens the card again. */
function ToastPill({ ref, label, title, mark, onClick, children }: { ref: Ref<HTMLButtonElement>; label: string; title: string; mark: ReactNode; onClick: () => void; children: ReactNode }) {
  return (
    <button
      ref={ref}
      type="button"
      onClick={onClick}
      aria-label={label}
      title={title}
      className="pop-in pointer-events-auto flex h-11 max-w-full items-center gap-2.5 rounded-full border border-accent-line bg-surface pe-4 ps-3.5 font-mono text-xs font-medium shadow-(--shadow-toast) transition-transform duration-200 ease-spring active:scale-[0.97]"
    >
      {mark}
      <span className="truncate">{children}</span>
    </button>
  );
}
