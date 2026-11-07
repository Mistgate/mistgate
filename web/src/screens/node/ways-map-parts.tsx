import { Popover } from "@base-ui/react/popover";
import { useState, type ComponentProps, type ReactNode } from "react";
import { Icon } from "@/components/ui/icons";
import { cx } from "@/lib/cx";

// Small parts of the route map (ways-graph.tsx): the status chip with its dot, and a popover that holds a menu or the details
// of a chip. Colour is only in the 6px dot; everything else is neutral.

const dotColor = { ok: "var(--accent-mint)", warn: "var(--warning)", bad: "var(--accent-rose)", cut: "var(--accent-rose)", off: "var(--faint)", busy: "var(--accent-sky)" };
export type ChipKind = keyof typeof dotColor;

export function Dot({ kind, className }: { kind: ChipKind; className?: string }) {
  return <span aria-hidden className={cx("size-1.5 shrink-0 rounded-full", className)} style={{ background: `color-mix(in oklch, ${dotColor[kind]} var(--wp, 100%), #000)` }} />;
}

const chipCls = "inline-flex h-[19px] shrink-0 items-center gap-1.5 rounded-full bg-surface-2 px-2 text-[11px] leading-none font-semibold whitespace-nowrap text-muted";

/** A status word: neutral, with a dot. */
export function Badge({ kind, children }: { kind: ChipKind; children: ReactNode }) {
  return (
    <span className={chipCls}>
      <Dot kind={kind} />
      {children}
    </span>
  );
}

/** A server or a relay on a card: a dot, its name, its port in muted mono; a button when it opens something. */
export function ServerChip({ kind, label, port, net, ...rest }: { kind: ChipKind; label: ReactNode; port?: number; net?: string } & ComponentProps<"button">) {
  return (
    <button type="button" {...rest} className={cx("inline-flex h-[24px] max-w-full items-center gap-1.5 rounded-full bg-surface-2 px-2.5 text-[12px] leading-none font-medium text-fg transition-colors hover:bg-[color-mix(in_oklch,var(--surface-2)_82%,var(--text))] focus-visible:outline-2", rest.className)}>
      <Dot kind={kind} />
      <span className="min-w-0 truncate">{label}</span>
      {port !== undefined && (
        <span className="font-mono text-[10.5px] font-normal text-muted">
          {net ? `${net}/` : ""}
          {port}
        </span>
      )}
    </button>
  );
}

const popupCls =
  "z-50 min-w-[200px] max-w-[300px] origin-(--transform-origin) rounded-card border border-line bg-surface p-1.5 text-[13px] shadow-(--shadow-toast) outline-none transition-[scale,opacity] duration-150 data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0";

/** A popover opened by `trigger`; `children` gets a close function. */
export function Pop({ trigger, children, label, triggerProps }: { trigger: ReactNode; children: (close: () => void) => ReactNode; label?: string; triggerProps?: ComponentProps<typeof Popover.Trigger> }) {
  const [open, setOpen] = useState(false);
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger aria-label={label} {...triggerProps}>
        {trigger}
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Positioner sideOffset={6} className="z-50">
          <Popover.Popup className={popupCls}>{children(() => setOpen(false))}</Popover.Popup>
        </Popover.Positioner>
      </Popover.Portal>
    </Popover.Root>
  );
}

/** A row of a popover menu. */
export function PopItem({ children, onClick, danger, disabled, title, icon }: { children: ReactNode; onClick?: () => void; danger?: boolean; disabled?: boolean; title?: string; icon?: ReactNode }) {
  return (
    <button
      type="button"
      disabled={disabled}
      title={title}
      onClick={onClick}
      className={cx("flex h-8 w-full items-center gap-2 rounded-lg px-2.5 text-start text-[13px] font-medium transition-colors hover:bg-surface-2 focus-visible:bg-surface-2 disabled:cursor-not-allowed disabled:opacity-45 disabled:hover:bg-transparent", danger ? "text-danger-text" : "text-fg")}
    >
      {icon && <span className="flex size-4 items-center justify-center text-muted">{icon}</span>}
      {children}
    </button>
  );
}

/** The ⋯ button of a card. */
export function MoreButton({ label, children }: { label: string; children: (close: () => void) => ReactNode }) {
  return (
    <Pop
      label={label}
      trigger={<Icon name="ellipsis" size={15} />}
      triggerProps={{ className: "flex size-6 shrink-0 items-center justify-center rounded-md text-muted hover:bg-surface-2 hover:text-fg data-popup-open:bg-surface-2 data-popup-open:text-fg" }}
    >
      {children}
    </Pop>
  );
}
