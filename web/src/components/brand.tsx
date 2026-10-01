import { useId, useMemo } from "react";
import badge from "@/assets/mistgate-badge.svg?raw";
import { useBrand } from "@/lib/brand";

/**
 * The built-in Mistgate badge. Inlined, not an <img>: its tones are derived from --mg-accent (bound to the
 * panel accent here), which an <img> would not see. Decorative.
 */
export function DefaultMark({ size = 30 }: { size?: number }) {
  // several badges on one page: every instance gets its own clip/mask ids
  const uid = useId().replace(/[^\w-]/g, "");
  const svg = useMemo(
    () =>
      badge
        .replace(/\b(mg-clip|mg-gap)\b/g, `$1-${uid}`)
        .replace(/ width="512" height="512"/, ` width="${size}" height="${size}"`),
    [uid, size],
  );
  return (
    <span
      aria-hidden
      className="block flex-none [&>svg]:block"
      style={{ width: size, height: size, ["--mg-accent" as string]: "var(--accent)" }}
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  );
}

/**
 * The instance logo: the built-in mark, or an uploaded one inlined (already sanitized by lib/brand) whose
 * three fills follow the accent through --logo-w / --logo-l / --logo-d. Decorative: the wordmark carries the name.
 */
export function Logo({ size = 30 }: { size?: number }) {
  const { logo } = useBrand();
  if (logo === null) return <DefaultMark size={size} />;
  return (
    <span
      aria-hidden
      className="block flex-none [&>svg]:block"
      style={{ width: size, height: size }}
      dangerouslySetInnerHTML={{ __html: logo }}
    />
  );
}

/**
 * The instance logo with motion (assets/mark-motion.css): `intro` plays once on mount and ends on the static badge,
 * `loading` is a gentle loop for a wait. The built-in badge moves part by part; an uploaded logo is never touched
 * inside, only its container fades and breathes. prefers-reduced-motion: the static logo, no motion at all.
 */
export function AnimatedMark({ size = 30, mode }: { size?: number; mode: "intro" | "loading" }) {
  const { logo } = useBrand();
  return (
    <span aria-hidden className="mg-motion block flex-none" data-mode={mode} data-kind={logo === null ? "builtin" : "custom"}>
      <Logo size={size} />
    </span>
  );
}

/** Live text in two parts; the second one is drawn in the accent. */
export function Wordmark({ size = 16 }: { size?: number }) {
  const {
    wordmark: [a, b],
  } = useBrand();
  return (
    <span className="leading-none font-extrabold tracking-[-0.03em]" style={{ fontSize: size }}>
      {a}
      <span className="text-accent-text transition-colors duration-500">{b}</span>
    </span>
  );
}

/** Logo + wordmark, as in the sidebar and the auth header. `motion` plays the logo's intro once (the sidebar stays still). */
export function Brand({ logoSize = 30, textSize = 16, motion }: { logoSize?: number; textSize?: number; motion?: "intro" }) {
  return (
    <span className="flex items-center gap-[9px]">
      {motion ? <AnimatedMark size={logoSize} mode={motion} /> : <Logo size={logoSize} />}
      <Wordmark size={textSize} />
    </span>
  );
}
