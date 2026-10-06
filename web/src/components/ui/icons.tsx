import type { SVGProps } from "react";
import { cx } from "@/lib/cx";

// Inline stroke icons on a 24 grid, stroke 2.2, round caps and joins.
const paths = {
  search: "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14Z M20 20l-4-4",
  back: "M15 5l-7 7 7 7",
  chevronRight: "M9 5l7 7-7 7",
  chevronDown: "M6 9l6 6 6-6",
  plus: "M12 5v14M5 12h14",
  ellipsis: "M5 12h.01M12 12h.01M19 12h.01",
  check: "M5 12.5l4.5 4.5L19 7.5",
  x: "M6 6l12 12M18 6L6 18",
  copy: "M9 9h10v11H9zM5 15V4h10",
  upload: "M12 16V4M12 4L8 8M12 4l4 4M4 20h16",
  share: "M12 15V4M12 4L8 8M12 4l4 4M6 11H5v9h14v-9h-1",
  externalLink: "M14 4h6v6M20 4l-9 9M18 14v5H5V6h5",
  trash: "M5 7h14M10 7V4h4v3M7 7l1 13h8l1-13",
  eye: "M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z",
  eyeOff: "M3 3l18 18M10.6 5.1A10 10 0 0 1 12 5c6.5 0 10 7 10 7a17 17 0 0 1-3.2 4.2M6.6 6.6A17 17 0 0 0 2 12s3.5 7 10 7c1.7 0 3.2-.4 4.6-1M9.9 9.9a3 3 0 0 0 4.2 4.2",
  refresh: "M20 12a8 8 0 1 1-2.4-5.7M20 4v5h-5",
  arrowUp: "M12 19V5M6 11l6-6 6 6",
  arrowDown: "M12 5v14M6 13l6 6 6-6",
  grip: "M9 6h.01M15 6h.01M9 12h.01M15 12h.01M9 18h.01M15 18h.01",
  globe: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.7 3.8 5.7 3.8 9s-1.3 6.3-3.8 9c-2.5-2.7-3.8-5.7-3.8-9S9.5 5.7 12 3z",
  map: "M9 4L3 6.5V20l6-2.5 6 2.5 6-2.5V4l-6 2.5L9 4zM9 4v13.5M15 6.5V20",
  shield: "M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6l7-3zM9 12l2.2 2.2L15.5 10",
  shieldOff: "M5 6.6V11c0 4.5 3 8 7 10 1.4-.6 2.6-1.5 3.6-2.7M8.8 4.4L12 3l7 3v5c0 1.6-.4 3-1 4.3M3 3l18 18",
  bugShield: "M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6l7-3zM12 10c1.1 0 2 .9 2 2v1.6c0 1.1-.9 2-2 2s-2-.9-2-2V12c0-1.1.9-2 2-2zM10 12.4H8.6M14 12.4h1.4M10 14.8H8.8M14 14.8h1.2M10.4 10.6l-.9-1M13.6 10.6l.9-1",
  family: "M8 10a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM3 20v-2a4 4 0 0 1 4-4h2a4 4 0 0 1 4 4v2M17.5 13a2.3 2.3 0 1 0 0-4.6 2.3 2.3 0 0 0 0 4.6zM16 20v-1a3 3 0 0 1 3-3h.5a2.5 2.5 0 0 1 1.5.5",
  people: "M15 20v-1.5a4 4 0 0 0-4-4H7a4 4 0 0 0-4 4V20M9 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM21 20v-1.5a4 4 0 0 0-3-3.9M15.5 4.2a3.5 3.5 0 0 1 0 6.6",
  lock: "M6 11h12v9H6zM8.5 11V8a3.5 3.5 0 0 1 7 0v3",
  server: "M4 5h16v5H4zM4 14h16v5H4zM7.5 7.5h.01M7.5 16.5h.01",
  info: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 11v5M12 8h.01",
  link: "M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1",
  bell: "M6 16V11a6 6 0 0 1 12 0v5l1.5 2h-15L6 16zM10 21h4",
  clock: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7v5l3 2",
  // section identity: what a card header or a preset card is about (IconChip)
  person: "M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM4 21v-1a6 6 0 0 1 6-6h4a6 6 0 0 1 6 6v1",
  tag: "M3 12V4h8l10 10-8 8L3 12zM7.5 8h.01",
  layers: "M12 3l9 5-9 5-9-5 9-5zM3 13l9 5 9-5",
  mask: "M4 6c3 1 5 1 8 0 3 1 5 1 8 0v6c0 4-3.5 7-8 8-4.5-1-8-4-8-8V6zM8.5 11h.01M15.5 11h.01M9 15c1.5 1 4.5 1 6 0",
  sliders: "M4 7h10M18 7h2M4 17h2M10 17h10M14 5v4M8 15v4",
  network: "M12 7a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM5 21a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM19 21a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM12 7v5M12 12l-7 5M12 12l7 5",
  code: "M8 8l-5 4 5 4M16 8l5 4-5 4M14 5l-4 14",
  warn: "M12 4l9 16H3L12 4zM12 10v4M12 17h.01",
  traffic: "M8 4v16M8 4L4 8M8 4l4 4M16 20V4M16 20l-4-4M16 20l4-4",
  gauge: "M4 17a8 8 0 1 1 16 0M12 17l4-5M7 17h.01",
  key: "M8 19a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM11 12l9-9M16 7l3 3M14 9l2 2",
  phone: "M7 3h10v18H7zM11 18h2",
  calendar: "M4 6h16v14H4zM4 10h16M8 3v4M16 3v4",
  list: "M8 6h12M8 12h12M8 18h12M4 6h.01M4 12h.01M4 18h.01",
  pulse: "M3 12h4l3-7 4 14 3-7h4",
  cpu: "M7 7h10v10H7zM9 3v4M15 3v4M9 17v4M15 17v4M3 9h4M3 15h4M17 9h4M17 15h4",
  memory: "M3 8h18v8H3zM7 11v2M11 11v2M15 11v2M7 19v-3M12 19v-3M17 19v-3",
  disk: "M4 7c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 7v10c0 1.7 3.6 3 8 3s8-1.3 8-3V7M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3",
  bolt: "M13 3L5 14h6l-1 7 8-11h-6l1-7z",
  gear: "M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5.3 5.3l2.1 2.1M16.6 16.6l2.1 2.1M5.3 18.7l2.1-2.1M16.6 7.4l2.1-2.1",
  terminal: "M4 5h16v14H4zM8 10l3 2-3 2M13 14h3",
  text: "M4 6h16M4 10h16M4 14h10M4 18h7",
  filter: "M4 5h16l-6 8v6l-4-2v-4L4 5z",
  palette: "M12 3a9 9 0 1 0 0 18c1.1 0 2-.9 2-2 0-.6-.3-1-.5-1.4-.3-.4-.5-.8-.5-1.3 0-1.1.9-2 2-2h2.3A3.7 3.7 0 0 0 21 10.6C21 6.4 17 3 12 3zM7.5 11h.01M10 7.5h.01M14.5 7.5h.01",
  plug: "M9 3v4M15 3v4M6 7h12v4a6 6 0 0 1-12 0zM12 17v4",
  dns: "M4 6h16v5H4zM4 14h16v5H4zM8 8.5h.01M8 16.5h.01M12 8.5h4M12 16.5h4",
  sparkle: "M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8L12 3z",
  video: "M3 6h12v12H3zM15 10l6-3v10l-6-3",
} as const;

export type IconName = keyof typeof paths;

export function Icon({ name, size = 16, ...rest }: { name: IconName; size?: number } & SVGProps<SVGSVGElement>) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2.2}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className="block flex-none"
      {...rest}
    >
      <path d={paths[name]} />
    </svg>
  );
}

/**
 * One pastel per meaning, the same everywhere (the sidebar's six): identity and access lavender, protocol and traffic
 * sky, basics and limits sand, disguise and profiles sage, preview and devices mint, danger rose. A tone names a
 * section, never a state: ok / warn / danger keep their own colours, glyphs and words.
 */
export type Tone = "lavender" | "sky" | "sand" | "sage" | "mint" | "rose";

/** A tinted square with a tone-stroked icon, in front of a section label or on a card. Decorative, so aria-hidden. */
export function IconChip({ icon, tone, size = 22, className }: { icon: IconName; tone: Tone; size?: number; className?: string }) {
  return (
    <span aria-hidden data-tone={tone} className={cx("id-chip", className)} style={{ width: size, height: size }}>
      <Icon name={icon} size={Math.round(size * 0.6)} />
    </span>
  );
}

// Sections and their pastel. Light theme darkens each through --_text-pct.
export const navPaths = {
  overview: ["M3 3h8v10H3zM13 3h8v6h-8zM13 13h8v8h-8zM3 17h8v4H3z", "lavender"],
  nodes: ["M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01M12 7h4M12 17h4", "sky"],
  users: [
    "M15 20v-1.5a4 4 0 0 0-4-4H7a4 4 0 0 0-4 4V20M9 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM21 20v-1.5a4 4 0 0 0-3-3.9M15.5 4.2a3.5 3.5 0 0 1 0 6.6",
    "sand",
  ],
  profiles: ["M4 6h9M17 6h3M4 12h3M11 12h9M4 18h11M19 18h1M15 4v4M9 10v4M17 16v4", "sage"],
  subscriptions: [
    "M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1",
    "mint",
  ],
  health: ["M3 12h4l3-7 4 14 3-7h4", "rose"],
  updates: ["M20 12a8 8 0 1 1-2.4-5.7M20 4v5h-5", "sky"],
  integrations: ["M9 3v4M15 3v4M6 7h12v4a6 6 0 0 1-12 0zM12 17v4", "lavender"],
  settings: [
    "M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5.3 5.3l2.1 2.1M16.6 16.6l2.1 2.1M5.3 18.7l2.1-2.1M16.6 7.4l2.1-2.1",
    "sand",
  ],
  more: ["M5 12h.01M12 12h.01M19 12h.01M5 6h.01M12 6h.01M19 6h.01M5 18h.01M12 18h.01M19 18h.01", "muted"],
} as const;

export type NavIconName = keyof typeof navPaths;

export function NavIcon({ name, size = 16, dim }: { name: NavIconName; size?: number; dim?: boolean }) {
  const [d, tone] = navPaths[name];
  const color = tone === "muted" ? "var(--muted)" : `color-mix(in oklch, var(--accent-${tone}) var(--_text-pct), #000)`;
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke={color}
      strokeWidth={2.2}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
      className="block flex-none"
      style={{ opacity: dim ? 0.8 : 1 }}
    >
      <path d={d} />
    </svg>
  );
}
