import { useLayoutEffect, useRef } from "react";
import { prefersReducedMotion, useEffectKind, useEffectsEnabled } from "@/lib/effects";
import { settle, unwatch, watch } from "./scramble";
import "./scramble.css";

/**
 * A number (or any short text) that shuffles into place when it shows and whenever it changes (components/fx/scramble.ts).
 * It is part of the "Reveal animation" (the page-opening kind): with that off, or with reduced motion, it is the plain text.
 * `once`: it plays the first time it shows and never again; a figure that polling changes is just set (the Nodes list).
 * The element is an image with the value as its label, so a screen reader reads the value, never the shuffle.
 */
export function Scramble({ value, className, once }: { value: string; className?: string; once?: boolean }) {
  const ref = useRef<HTMLSpanElement>(null);
  const main = useEffectsEnabled();
  const kind = useEffectKind("pages");
  const on = main && kind && !prefersReducedMotion();
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (on) watch(el, value, once);
    else {
      unwatch(el);
      settle(el, value);
    }
  }, [value, on, once]);
  useLayoutEffect(() => {
    const el = ref.current;
    return () => {
      if (el) unwatch(el);
    };
  }, []);
  return <span ref={ref} role="img" aria-label={value} className={className} />;
}
