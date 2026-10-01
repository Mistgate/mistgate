import type { ReactNode } from "react";
import { DnsServerKind, DnsTransport, type DnsCategory } from "@/gen/mistgate/admin/v1/dns_pb";
import { Icon, type IconName } from "@/components/ui/icons";
import { cx } from "@/lib/cx";
import { presetLook, shortAddress, type DnsServerN } from "./model";

/** The square glyph of a preset: a pastel tile that hints at what the preset is for (map, globe, shield, family…). */
export function PresetGlyph({ preset, size = 40, className }: { preset: { id: string; category?: DnsCategory }; size?: number; className?: string }) {
  const { glyph, tone } = presetLook(preset);
  const style =
    tone === "muted"
      ? { width: size, height: size }
      : {
          width: size,
          height: size,
          background: `color-mix(in oklch, var(--accent-${tone}) 16%, var(--surface))`,
          color: `color-mix(in oklch, var(--accent-${tone}) var(--_text-pct), #000)`,
        };
  return (
    <span aria-hidden className={cx("grid flex-none place-items-center rounded-xl", tone === "muted" && "bg-surface-2 text-muted", className)} style={style}>
      {glyph === "letter" ? <span className="text-[19px] leading-none font-extrabold">Я</span> : <Icon name={glyph as IconName} size={Math.round(size * 0.5)} />}
    </span>
  );
}

/** Small uppercase pill (built-in, direct): neutral, or accent for the state that matters. */
export function Pill({ children, accent, icon, className }: { children: ReactNode; accent?: boolean; icon?: IconName; className?: string }) {
  return (
    <span
      className={cx(
        "inline-flex h-[20px] flex-none items-center gap-1 rounded-md px-1.5 text-[10px] font-bold tracking-wide whitespace-nowrap uppercase",
        accent ? "border border-accent-line bg-accent-soft text-accent-text" : "bg-surface-2 text-muted",
        className,
      )}
    >
      {icon && <Icon name={icon} size={10} strokeWidth={3} />}
      {children}
    </span>
  );
}

const kindIcon: Partial<Record<DnsServerKind, IconName>> = { [DnsServerKind.PLAIN]: "server", [DnsServerKind.DOH]: "lock", [DnsServerKind.DOT]: "shield" };
const transportIcon: Partial<Record<DnsTransport, IconName>> = { [DnsTransport.PLAIN]: "server", [DnsTransport.DOH]: "lock", [DnsTransport.DOT]: "shield" };

/** A resolver as a chip: the icon tells the transport (plain / DoH lock / DoT shield), the text is what the server is. */
export function ServerChip({ icon, text, title }: { icon: IconName; text: string; title: string }) {
  return (
    <span title={title} className="inline-flex h-6 max-w-full min-w-0 items-center gap-1.5 rounded-ctl bg-surface-2 px-2 font-mono text-[11px] text-muted">
      <Icon name={icon} size={11} />
      <span className="truncate">{text}</span>
    </span>
  );
}

/** The chip of a custom server: its own kind and address, a DoH URL cut to its host. */
export const customChip = (server: DnsServerN, kindLabel: string) => ({ icon: kindIcon[server.kind] ?? "server", text: shortAddress(server.kind, server.address), title: `${kindLabel}: ${server.address}` });
/** The icon a catalog server gets on a card: the transport of its preset. */
export const transportChipIcon = (t: DnsTransport): IconName => transportIcon[t] ?? "server";

/** "N users" as a chip: a people icon and the count; grey and quiet at zero. */
export function UsersChip({ count, label }: { count: number; label: string }) {
  return (
    <span title={label} aria-label={label} className={cx("inline-flex h-[22px] flex-none items-center gap-1 rounded-ctl px-2 font-mono text-[11px] font-bold", count > 0 ? "bg-accent-soft text-accent-text" : "bg-surface-2 text-faint")}>
      <Icon name="people" size={12} />
      {count}
    </span>
  );
}
