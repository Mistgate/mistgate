import { Tabs as BaseTabs } from "@base-ui/react/tabs";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { cx } from "@/lib/cx";

export type TabItem = { value: string; label: ReactNode; badge?: number; content: ReactNode };

/**
 * Underlined tabs (node detail, health, subscriptions). A red badge carries a problem count. A strip wider than the screen
 * scrolls: the chosen tab is brought into view (also when the page opens on it from a link), and a fade at the right edge
 * says there is more while there is.
 */
export function Tabs({
  items,
  value,
  onValueChange,
  className,
  ...rest
}: {
  items: readonly TabItem[];
  value: string;
  onValueChange: (value: string) => void;
  "aria-label": string;
  className?: string;
}) {
  const list = useRef<HTMLDivElement>(null);
  const [more, setMore] = useState(false);
  const index = items.findIndex((it) => it.value === value);

  // the chosen tab in view: on the first paint (a link that opens a far tab) and on every change, not on a re-render
  useEffect(() => {
    const el = list.current;
    const active = el?.querySelectorAll<HTMLElement>('[role="tab"]')[index];
    if (!el || !active) return;
    const start = active.offsetLeft;
    const end = start + active.offsetWidth;
    if (start < el.scrollLeft) el.scrollLeft = Math.max(0, start - 16);
    else if (end > el.scrollLeft + el.clientWidth) el.scrollLeft = end - el.clientWidth + 16;
  }, [index]);

  // the fade at the right edge while there is more to scroll to
  useEffect(() => {
    const el = list.current;
    if (!el) return;
    const update = () => setMore(el.scrollLeft + el.clientWidth < el.scrollWidth - 1);
    update();
    el.addEventListener("scroll", update, { passive: true });
    window.addEventListener("resize", update);
    return () => {
      el.removeEventListener("scroll", update);
      window.removeEventListener("resize", update);
    };
  }, []);

  return (
    <BaseTabs.Root value={value} onValueChange={(v) => onValueChange(String(v))} className={className}>
      <BaseTabs.List ref={list} className="no-scrollbar relative flex overflow-x-auto border-b border-line" {...rest}>
        {items.map((it) => (
          <BaseTabs.Tab
            key={it.value}
            value={it.value}
            className={cx(
              "flex h-[38px] flex-none items-center gap-1.5 px-3 text-[13px] font-bold whitespace-nowrap text-muted transition-colors data-active:text-fg focus-visible:-outline-offset-2",
            )}
          >
            {it.label}
            {!!it.badge && (
              <span className="flex h-4 min-w-4 items-center justify-center rounded-lg bg-danger-soft px-1 font-mono text-[10px] text-danger-text">
                {it.badge}
              </span>
            )}
          </BaseTabs.Tab>
        ))}
        <span
          aria-hidden
          className={cx(
            "pointer-events-none sticky right-0 -ml-10 w-10 flex-none bg-linear-to-l from-canvas to-transparent transition-opacity duration-200",
            more ? "opacity-100" : "opacity-0",
          )}
        />
        <BaseTabs.Indicator className="absolute bottom-[-1px] left-(--active-tab-left) h-0.5 w-(--active-tab-width) translate-x-0 rounded-[1px] bg-accent transition-[left,width] duration-300 ease-spring" />
      </BaseTabs.List>
      {items.map((it) => (
        <BaseTabs.Panel key={it.value} value={it.value} className="screen-enter pt-4 outline-none">
          {it.content}
        </BaseTabs.Panel>
      ))}
    </BaseTabs.Root>
  );
}
