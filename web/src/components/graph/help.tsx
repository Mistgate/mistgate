import { useEffect, useRef, type ReactNode } from "react";
import { Icon } from "@/components/ui/icons";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";

// The graph's two help cards, bottom-left: the legend (what the lines, ports and chips mean; its content is the screen's)
// and the shortcuts. One card at a time, opened from a small button each; Esc or a click outside closes it.

export type HelpTab = "legend" | "shortcuts";

/** Cmd on a Mac, Ctrl elsewhere: detected once. */
export const modKey: string = typeof navigator !== "undefined" && /Mac|iPhone|iPad/i.test(navigator.platform || navigator.userAgent || "") ? "⌘" : "Ctrl";

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="mg-kbd">{children}</kbd>;
}

/** A shortcut row: a fixed column for the keys, then a short label. */
export function KeyRow({ keys, children }: { keys: ReactNode; children: ReactNode }) {
  return (
    <li className="mg-keyrow">
      <span className="mg-keys">{keys}</span>
      <span className="text-muted">{children}</span>
    </li>
  );
}

/** A group of rows under a quiet subheading (also the legend's sections). */
export function HelpGroup({ title, children, className }: { title: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx("flex flex-col gap-1.5", className)}>
      <h3 className="text-[11px] font-semibold text-faint">{title}</h3>
      {children}
    </section>
  );
}

export function GraphShortcuts({ expand, extra }: { expand: boolean; extra?: ReactNode }) {
  const t = useT();
  const k = (s: string) => <Kbd key={s}>{s}</Kbd>;
  return (
    <div className="flex flex-col gap-3">
      <HelpGroup title={t("graph.k.view")}>
        <ul className="flex flex-col gap-1">
          <KeyRow keys={<><Kbd>{modKey}</Kbd><Kbd>{t("graph.key.scroll")}</Kbd></>}>{t("graph.k.zoom")}</KeyRow>
          <KeyRow keys={<Kbd>{t("graph.key.drag")}</Kbd>}>{t("graph.k.pan")}</KeyRow>
          <KeyRow keys={<Kbd>{t("graph.key.dblclick")}</Kbd>}>{t("graph.k.zoomHere")}</KeyRow>
          <KeyRow keys={k("F")}>{t("graph.k.fit")}</KeyRow>
          <KeyRow keys={k("0")}>100%</KeyRow>
          <KeyRow keys={<>{k("+")}{k("−")}</>}>{t("graph.k.zoom")}</KeyRow>
          {expand && <KeyRow keys={<>{k("Shift")}{k("F")}</>}>{t("graph.expand")}</KeyRow>}
          <KeyRow keys={k("Esc")}>{t("graph.k.close")}</KeyRow>
        </ul>
      </HelpGroup>
      <HelpGroup title={t("graph.k.select")}>
        <ul className="flex flex-col gap-1">
          <KeyRow keys={<Kbd>{t("graph.key.click")}</Kbd>}>{t("graph.k.select")}</KeyRow>
          <KeyRow keys={<>{k("Shift")}<Kbd>{t("graph.key.click")}</Kbd></>}>{t("graph.k.add")}</KeyRow>
          <KeyRow keys={<>{k("Shift")}<Kbd>{t("graph.key.drag")}</Kbd></>}>{t("graph.k.box")}</KeyRow>
          <KeyRow keys={<>{k(modKey)}{k("A")}</>}>{t("graph.k.all")}</KeyRow>
          <KeyRow keys={k("Tab")}>{t("graph.k.tab")}</KeyRow>
          <KeyRow keys={<Kbd>{t("graph.key.arrows")}</Kbd>}>{t("graph.k.move")}</KeyRow>
        </ul>
      </HelpGroup>
      {extra}
    </div>
  );
}

/** The two buttons and the open card. `open` is controlled; "" is closed. */
export function GraphHelp({ open, onOpen, legend, expand, extraShortcuts }: { open: HelpTab | ""; onOpen: (tab: HelpTab | "") => void; legend?: ReactNode; expand: boolean; extraShortcuts?: ReactNode }) {
  const t = useT();
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const off = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) onOpen("");
    };
    document.addEventListener("pointerdown", off, true);
    return () => document.removeEventListener("pointerdown", off, true);
  }, [open, onOpen]);
  const btn = (tab: HelpTab, label: string, icon: "info" | "terminal") => (
    <button type="button" className="mg-glegend-btn" aria-expanded={open === tab} aria-controls={open === tab ? "graph-help" : undefined} data-active={open === tab || undefined} onClick={() => onOpen(open === tab ? "" : tab)}>
      <Icon name={icon} size={13} />
      {label}
    </button>
  );
  return (
    <div ref={ref} className="mg-glegend" data-gui>
      {open && (
        <div id="graph-help" className="mg-glegend-body" role="region" aria-label={open === "legend" ? t("graph.legend") : t("graph.shortcuts")}>
          {open === "legend" ? legend : <GraphShortcuts expand={expand} extra={extraShortcuts} />}
        </div>
      )}
      <div className="flex gap-1.5">
        {legend && btn("legend", t("graph.legend"), "info")}
        {btn("shortcuts", t("graph.shortcuts"), "terminal")}
      </div>
    </div>
  );
}
