import type { CSSProperties } from "react";
import { useEffectsEnabled } from "@/lib/effects";
import "./flourish.css";

/**
 * The end of an action on a card: a wave from (x, y) of the card, in px, and two glints along its edge. Put it inside the card (position: relative)
 * when the action is done and take it out a few seconds later; it renders nothing with the reveal animation off.
 */
export function Flourish({ x, y }: { x: number; y: number }) {
  const fx = useEffectsEnabled();
  if (!fx) return null;
  return (
    <>
      <div className="fl-wv" aria-hidden style={{ "--fl-x": `${x}px`, "--fl-y": `${y}px` } as CSSProperties}>
        <i />
      </div>
      <span className="fl-eg a" aria-hidden />
      <span className="fl-eg b" aria-hidden />
    </>
  );
}

/** The look that goes round the edge of a card while something is checked (the board's dr-loop). */
export function EdgeLoop() {
  const fx = useEffectsEnabled();
  return fx ? <span className="fl-loop" aria-hidden /> : null;
}
