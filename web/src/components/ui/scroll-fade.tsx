import { useLayoutEffect, useRef, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "@/lib/cx";

/**
 * A scroll area of a fixed height that stays quiet: a thin scrollbar on hover or focus, and a soft fade at the top or
 * bottom only while there is more to scroll that way (`.scroll-soft` in index.css). Focusable, so the keyboard scrolls it.
 */
export function ScrollFade({ className, children, label, ...rest }: { label: string; children: ReactNode } & Omit<HTMLAttributes<HTMLDivElement>, "children">) {
  const ref = useRef<HTMLDivElement>(null);
  const inner = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const update = () => {
      el.dataset.top = el.scrollTop > 1 ? "1" : "0";
      el.dataset.bottom = el.scrollTop + el.clientHeight < el.scrollHeight - 1 ? "1" : "0";
    };
    update();
    el.addEventListener("scroll", update, { passive: true });
    const ro = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(update);
    ro?.observe(el);
    if (inner.current) ro?.observe(inner.current);
    return () => {
      el.removeEventListener("scroll", update);
      ro?.disconnect();
    };
  }, []);
  return (
    <div ref={ref} role="region" aria-label={label} tabIndex={0} className={cx("scroll-soft", className)} {...rest}>
      <div ref={inner}>{children}</div>
    </div>
  );
}
