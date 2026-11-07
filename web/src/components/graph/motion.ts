// The pure parts of the graph's feel: how a wheel turns into a zoom, how the view eases toward its target, and how a
// released drag glides on. The component drives them from requestAnimationFrame and writes the transform directly.

/** One notch of a mouse wheel is a step of this many percent. */
export const NOTCH_STEP = 1.1;

/**
 * The scale factor of one wheel event. A notched wheel (large whole deltas) steps 10 % per notch; a trackpad pinch (many
 * small deltas) is continuous. Positive deltaY zooms out.
 */
export function wheelFactor(deltaY: number, deltaMode = 0): number {
  const px = deltaY * (deltaMode === 1 ? 16 : deltaMode === 2 ? 400 : 1);
  if (px === 0) return 1;
  if (Math.abs(px) >= 50 && Number.isInteger(px)) {
    const notches = Math.max(1, Math.round(Math.abs(px) / 100));
    return Math.pow(px > 0 ? 1 / NOTCH_STEP : NOTCH_STEP, notches);
  }
  return Math.exp(-px * 0.01);
}

/** Wheel events between two frames add up: the target scale after each, within the limits. */
export function accumulate(target: number, factor: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, target * factor));
}

/**
 * One frame of ease-out toward the target: exponential smoothing, which covers about 95 % of the way in 3 x `tau`
 * (45 ms: ~135 ms). Frame-rate independent.
 */
export function smoothStep(current: number, target: number, dtMs: number, tauMs = 45): number {
  const d = target - current;
  if (Math.abs(d) < 0.0005) return target;
  return current + d * (1 - Math.exp(-dtMs / tauMs));
}

/** The glide of a released drag: the velocity (px per ms) decays exponentially; one frame returns the new velocity and the move. */
export function inertiaStep(v: { x: number; y: number }, dtMs: number, tauMs = 280): { v: { x: number; y: number }; dx: number; dy: number } {
  const k = Math.exp(-dtMs / tauMs);
  // the distance covered in dt of a velocity decaying with k: v * tau * (1 - k)
  const f = tauMs * (1 - k);
  return { v: { x: v.x * k, y: v.y * k }, dx: v.x * f, dy: v.y * f };
}
export const glideStopped = (v: { x: number; y: number }, minSpeed = 0.02) => Math.hypot(v.x, v.y) < minSpeed;

/** The release velocity (px per ms) of a drag from its recent samples (the last 100 ms). */
export function releaseVelocity(samples: readonly { x: number; y: number; t: number }[], now: number, windowMs = 100): { x: number; y: number } {
  const recent = samples.filter((s) => now - s.t <= windowMs);
  if (recent.length < 2) return { x: 0, y: 0 };
  const a = recent[0]!;
  const b = recent[recent.length - 1]!;
  const dt = b.t - a.t;
  return dt <= 0 ? { x: 0, y: 0 } : { x: (b.x - a.x) / dt, y: (b.y - a.y) / dt };
}
