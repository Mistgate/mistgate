import type { ReactNode } from "react";
import { cx } from "@/lib/cx";

// Country flags as inline SVG. Windows browsers draw a regional-indicator pair ("🇩🇪") as two letters, so the admin
// UI never relies on the emoji: the subscriptions still carry real emoji (Happ draws them), the admin draws these.
// 30x20 boxes, simplified where a flag is detailed. A code that is not here falls back to its two letters.

type Stripes = { h?: readonly string[]; v?: readonly string[]; w?: readonly number[] };

const stripes: Record<string, Stripes> = {
  DE: { h: ["#000", "#d00", "#ffce00"] },
  NL: { h: ["#ae1c28", "#fff", "#21468b"] },
  RU: { h: ["#fff", "#0039a6", "#d52b1e"] },
  AT: { h: ["#ed2939", "#fff", "#ed2939"] },
  LU: { h: ["#ed2939", "#fff", "#00a1de"] },
  EE: { h: ["#0072ce", "#000", "#fff"] },
  LT: { h: ["#fdb913", "#006a44", "#c1272d"] },
  LV: { h: ["#9e3039", "#fff", "#9e3039"], w: [2, 1, 2] },
  HU: { h: ["#cd2a3e", "#fff", "#436f4d"] },
  BG: { h: ["#fff", "#00966e", "#d62612"] },
  UA: { h: ["#0057b7", "#ffd700"] },
  PL: { h: ["#fff", "#dc143c"] },
  ES: { h: ["#aa151b", "#f1bf00", "#aa151b"], w: [1, 2, 1] },
  AM: { h: ["#d90012", "#0033a0", "#f2a800"] },
  FR: { v: ["#0055a4", "#fff", "#ef4135"] },
  IT: { v: ["#009246", "#fff", "#ce2b37"] },
  IE: { v: ["#169b62", "#fff", "#ff883e"] },
  RO: { v: ["#002b7f", "#fcd116", "#ce1126"] },
  BE: { v: ["#000", "#fdda24", "#ef3340"] },
};

const W = 30;
const H = 20;

function bands(s: Stripes): ReactNode {
  const dir = s.h ?? s.v!;
  const weights = s.w ?? dir.map(() => 1);
  const total = weights.reduce((a, b) => a + b, 0);
  let at = 0;
  return dir.map((color, i) => {
    const size = ((s.h ? H : W) * weights[i]!) / total;
    const node = s.h ? <rect key={i} x={0} y={at} width={W} height={size + 0.05} fill={color} /> : <rect key={i} x={at} y={0} width={size + 0.05} height={H} fill={color} />;
    at += size;
    return node;
  });
}

/** A Nordic cross: field colour, cross colour, optional inner cross colour. */
const cross = (field: string, outer: string, inner?: string) => (
  <>
    <rect width={W} height={H} fill={field} />
    <rect x={8} width={5} height={H} fill={outer} />
    <rect y={7.5} width={W} height={5} fill={outer} />
    {inner && (
      <>
        <rect x={9.5} width={2} height={H} fill={inner} />
        <rect y={9} width={W} height={2} fill={inner} />
      </>
    )}
  </>
);

const special: Record<string, ReactNode> = {
  FI: cross("#fff", "#003580"),
  SE: cross("#006aa7", "#fecc02"),
  DK: cross("#c8102e", "#fff"),
  NO: cross("#ba0c2f", "#fff", "#00205b"),
  CH: (
    <>
      <rect width={W} height={H} fill="#d52b1e" />
      <rect x={13} y={4} width={4} height={12} fill="#fff" />
      <rect x={9} y={8} width={12} height={4} fill="#fff" />
    </>
  ),
  JP: (
    <>
      <rect width={W} height={H} fill="#fff" />
      <circle cx={15} cy={10} r={6} fill="#bc002d" />
    </>
  ),
  KZ: (
    <>
      <rect width={W} height={H} fill="#00afca" />
      <circle cx={15} cy={10} r={4} fill="#fec50c" />
    </>
  ),
  TR: (
    <>
      <rect width={W} height={H} fill="#e30a17" />
      <circle cx={11.5} cy={10} r={5} fill="#fff" />
      <circle cx={13} cy={10} r={4} fill="#e30a17" />
      <circle cx={18} cy={10} r={1.6} fill="#fff" />
    </>
  ),
  CZ: (
    <>
      <rect width={W} height={H / 2} fill="#fff" />
      <rect y={H / 2} width={W} height={H / 2} fill="#d7141a" />
      <path d={`M0 0L15 10L0 ${H}z`} fill="#11457e" />
    </>
  ),
  US: (
    <>
      <rect width={W} height={H} fill="#fff" />
      {[0, 2, 4, 6, 8, 10, 12].map((i) => (
        <rect key={i} y={(i * H) / 13} width={W} height={H / 13} fill="#b22234" />
      ))}
      <rect width={13} height={(7 * H) / 13} fill="#3c3b6e" />
    </>
  ),
  GB: (
    <>
      <rect width={W} height={H} fill="#012169" />
      <path d={`M0 0L${W} ${H}M${W} 0L0 ${H}`} stroke="#fff" strokeWidth={4} />
      <path d={`M0 0L${W} ${H}M${W} 0L0 ${H}`} stroke="#c8102e" strokeWidth={1.6} />
      <path d={`M${W / 2} 0V${H}M0 ${H / 2}H${W}`} stroke="#fff" strokeWidth={6} />
      <path d={`M${W / 2} 0V${H}M0 ${H / 2}H${W}`} stroke="#c8102e" strokeWidth={3.4} />
    </>
  ),
};

/** True when the flag is drawn here (otherwise the two letters are shown). */
export const hasFlag = (code: string) => code.toUpperCase() in stripes || code.toUpperCase() in special;

/** A small rounded flag of an ISO 3166 country code; an unknown code shows its letters in a grey tile. `size` is the height. */
export function Flag({ code, size = 16, className }: { code: string; size?: number; className?: string }) {
  const c = code.toUpperCase();
  const width = Math.round((size * 3) / 2);
  const art = c in special ? special[c] : c in stripes ? bands(stripes[c]!) : null;
  if (!art) {
    return (
      <span aria-hidden className={cx("inline-flex flex-none items-center justify-center rounded-[3px] bg-surface-2 font-mono font-bold text-muted", className)} style={{ width, height: size, fontSize: Math.max(7, Math.round(size * 0.5)) }}>
        {c.slice(0, 2)}
      </span>
    );
  }
  return (
    <svg aria-hidden width={width} height={size} viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" className={cx("block flex-none rounded-[3px] shadow-[0_0_0_1px_rgb(128_128_128/0.35)]", className)}>
      {art}
    </svg>
  );
}

const pair = /([\u{1F1E6}-\u{1F1FF}]{2})/u;
const letters = (flag: string) => String.fromCharCode(...[...flag].map((ch) => ch.codePointAt(0)! - 0x1f1e6 + 65));

/** Text in which every flag emoji is swapped for the drawn flag, inline ("🇩🇪 de1" becomes [flag] de1). */
export function FlagText({ text, size = 14 }: { text: string; size?: number }) {
  return (
    <>
      {text.split(pair).map((part, i) => (i % 2 === 1 ? <Flag key={i} code={letters(part)} size={size} className="mr-1 inline-block align-[-2px]" /> : part))}
    </>
  );
}
