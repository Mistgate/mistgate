import { useMemo } from "react";
import { useLang, useT, type Lang, type T } from "@/i18n";

/** Scales a byte count to the largest unit that keeps the number below `base` (1000 for traffic, 1024 for RAM and disk). */
export function scaleBytes(n: number, base: 1000 | 1024 = 1000): { value: number; unit: 0 | 1 | 2 | 3 | 4 } {
  let value = Math.max(0, n);
  let unit = 0;
  while (value >= base && unit < 4) {
    value /= base;
    unit++;
  }
  return { value, unit: unit as 0 | 1 | 2 | 3 | 4 };
}

const unitKeys = ["unit.B", "unit.KB", "unit.MB", "unit.GB", "unit.TB"] as const;

const numberFormats = new Map<string, Intl.NumberFormat>();
function numberFormat(lang: Lang, digits: number, fixed: boolean) {
  const key = `${lang}/${digits}/${fixed}`;
  let f = numberFormats.get(key);
  if (!f) {
    f = new Intl.NumberFormat(lang, { maximumFractionDigits: digits, minimumFractionDigits: fixed ? digits : 0 });
    numberFormats.set(key, f);
  }
  return f;
}

/** Local time of day, 24 hours: "14:06". */
function clockOf(unix: number, lang: Lang) {
  return new Date(unix * 1000).toLocaleTimeString(lang, { hour: "2-digit", minute: "2-digit", hour12: false });
}

const sameDay = (a: Date, b: Date) =>
  a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();

/** "28 сент.": the year only when it is not this one ("28 сент. 2025 г."). */
export function dayOf(unix: number, lang: Lang, now = new Date()) {
  const d = new Date(unix * 1000);
  return d.toLocaleDateString(lang, { day: "numeric", month: "short", ...(d.getFullYear() === now.getFullYear() ? {} : { year: "numeric" }) });
}

/** "28 сент., 18:15", the year as in dayOf. */
export function dateTimeOf(unix: number, lang: Lang, now = new Date()) {
  return `${dayOf(unix, lang, now)}, ${clockOf(unix, lang)}`;
}

/** "14:06" for today, "27 сент., 03:00" for any other day. `now` is injectable for tests. */
export function stampOf(unix: number, lang: Lang, now = new Date()) {
  return sameDay(new Date(unix * 1000), now) ? clockOf(unix, lang) : dateTimeOf(unix, lang, now);
}

/** The short "5 мин назад / 3 ч назад / 2 дн назад" of every list; "только что" under a minute. */
export function agoOf(t: (key: "common.justNow" | "users.ago.m" | "users.ago.h" | "users.ago.d", vars?: { n: number }) => string, unix: number, nowSec: number) {
  const s = Math.max(0, nowSec - unix);
  if (s < 60) return t("common.justNow");
  if (s < 3600) return t("users.ago.m", { n: Math.floor(s / 60) });
  if (s < 86400) return t("users.ago.h", { n: Math.floor(s / 3600) });
  return t("users.ago.d", { n: Math.floor(s / 86400) });
}

/** The pieces of a duration in seconds as [value, unit] of the coarsest sensible unit: days, hours or minutes. */
export function coarseDuration(seconds: number): [number, "d" | "h" | "m"] {
  const s = Math.max(0, seconds);
  if (s >= 86400) return [Math.floor(s / 86400), "d"];
  if (s >= 3600) return [Math.floor(s / 3600), "h"];
  return [Math.floor(s / 60), "m"];
}

/** A span of whole minutes the way a status line says it: "47 min" up to 90 minutes, then two units, "2 h 5 min", "2 d 3 h". */
export function minutesText(t: T, minutes: number): string {
  const m = Math.max(0, Math.floor(minutes) || 0);
  const unit = (n: number, u: "d" | "h" | "m") => `${n} ${t(`unit.${u}` as const)}`;
  if (m <= 90) return unit(m, "m");
  const d = Math.floor(m / 1440);
  const h = Math.floor((m % 1440) / 60);
  if (d > 0) return h > 0 ? `${unit(d, "d")} ${unit(h, "h")}` : unit(d, "d");
  return m % 60 > 0 ? `${unit(h, "h")} ${unit(m % 60, "m")}` : unit(h, "h");
}

export type Fmt = ReturnType<typeof makeFmt>;

export function makeFmt(lang: Lang, t: T) {
  const num = (v: number, digits = 1, fixed = false) => numberFormat(lang, digits, fixed).format(v);
  const bytes = (n: number, base: 1000 | 1024 = 1000, digits = 1) => {
    const { value, unit } = scaleBytes(n, base);
    return `${num(value, unit === 0 ? 0 : digits)} ${t(unitKeys[unit])}`;
  };
  /** Bits per second as Mbit/s with the unit: "12,4 Мбит/с". */
  const mbit = (bps: number) => `${num(bps / 1e6, bps >= 1e7 ? 0 : 1)} ${t("unit.mbit")}`;
  /** The bare number, for places where the unit sits elsewhere (node cards: "↓12,4"). */
  const mbitNum = (bps: number) => num(bps / 1e6, bps >= 1e7 ? 0 : 1);
  const ago = (unix: number, now = Date.now()) => agoOf(t, unix, Math.floor(now / 1000));
  const duration = (seconds: number) => {
    const [v, u] = coarseDuration(seconds);
    return `${v} ${t(`unit.${u}` as const)}`;
  };
  return {
    lang,
    num,
    bytes,
    mbit,
    mbitNum,
    ago,
    duration,
    clock: (unix: number) => clockOf(unix, lang),
    stamp: (unix: number) => stampOf(unix, lang),
    date: (unix: number) => dayOf(unix, lang),
    dateTime: (unix: number) => dateTimeOf(unix, lang),
    /** Country name in the UI language from an ISO 3166-1 alpha-2 code; the code itself when unknown. */
    country: (code: string) => {
      if (!code) return "";
      try {
        return new Intl.DisplayNames(lang, { type: "region" }).of(code.toUpperCase()) ?? code;
      } catch {
        return code;
      }
    },
  };
}

/** Number, size, speed and time formatting bound to the UI language. */
export function useFmt(): Fmt {
  const lang = useLang();
  const t = useT();
  return useMemo(() => makeFmt(lang, t), [lang, t]);
}
