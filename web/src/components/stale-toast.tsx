import { useEffect, useRef, useState } from "react";
import { useT } from "@/i18n";
import { reloadPage, useStaleVersion } from "@/lib/stale";
import { Button } from "./ui/button";
import { Icon } from "./ui/icons";
import { ToastCard, ToastCornerButton, ToastPill, toastCorner } from "./update-toast";

/**
 * "The panel was updated: reload", in the corner of every page with the update notice's card (update-toast.tsx, which steps
 * aside meanwhile), for a tab that outlived a panel update (lib/stale.ts): the old JS cannot be trusted with the new panel's
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
