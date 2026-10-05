/**
 * The value "Use" puts into the capacity field: the measured download rounded the way a plan is written, because a test
 * is an estimate and "937" would only look more exact than it is. Under 10 as it is, under 100 to 5, under 1000 to 10, above to 50.
 * At least 1: 0 means "unknown" and would switch the percentages off.
 */
export function roundMbps(measured: number): number {
  const step = measured < 10 ? 1 : measured < 100 ? 5 : measured < 1000 ? 10 : 50;
  return Math.min(1_000_000, Math.max(1, Math.round(measured / step) * step));
}
