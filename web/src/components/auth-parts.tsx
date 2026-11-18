import { useEffect, useId, useRef, useState, type ClipboardEvent, type KeyboardEvent, type ReactNode } from "react";
import { AnimatedMark } from "@/components/brand";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { QrCard } from "@/components/qr-code";
import type { QrBits } from "@/lib/qr-path";
import { Button } from "@/components/ui/button";
import { TextField } from "@/components/ui/text-field";
import { Glyph, glyphs, Halo, Spinner, WaveField } from "@/components/windows";
import { useT } from "@/i18n";
import { useBrand } from "@/lib/brand";
import { cx } from "@/lib/cx";

/**
 * The sign-in page of the board (pn.css .lg): a centred column 380 wide (brand, then the content, 20 apart) on a page that
 * keeps its dotted halo behind it, the line about the decoy site at the foot. Shared by sign-in, first-run setup and the
 * recovery page. Language and theme sit in the corner because nobody is signed in yet to reach Settings -> Interface.
 * `aside` is a slot at the right of the brand row (the wizard's progress dots); `wave` plays the board's wave over the page
 * (the sign-in that went through).
 */
export function AuthFrame({
  aside,
  children,
  viewKey,
  footer,
  wave,
}: {
  aside?: ReactNode;
  children: ReactNode;
  viewKey?: string;
  footer?: ReactNode;
  wave?: boolean;
}) {
  // the first view comes in with the column; a view that replaces it (a setup step, a result) comes in by itself
  const [firstView] = useState(viewKey);
  return (
    <div className="relative flex min-h-dvh flex-col items-center justify-center overflow-hidden px-6 pt-12 pb-24">
      <Halo className="left-1/2 top-1/2 h-[640px] w-[900px] -translate-x-1/2 -translate-y-[56%] opacity-30" />
      {wave && <WaveField />}
      <div className="absolute top-3 inset-e-3 z-10 flex items-center gap-2 md:top-4 md:inset-e-5">
        <LangToggle />
        <ThemeToggle />
      </div>
      <main className="relative flex w-full max-w-[380px] animate-[mg-up_0.5s_var(--e)_both] flex-col gap-5">
        {/* the brand row stays put across the setup steps: its logo plays its intro once, the content below re-enters */}
        <div className="flex items-center gap-2.5">
          <AuthBrand />
          <div className="flex-1" />
          {aside}
        </div>
        <div key={viewKey} className={cx("flex flex-col gap-5", viewKey !== firstView && "animate-[mg-up_0.3s_var(--e)_both]")}>
          {children}
        </div>
      </main>
      {footer && <footer className="absolute inset-x-0 bottom-[84px] px-6 text-center text-xs text-muted">{footer}</footer>}
    </div>
  );
}

/** Logo 40 and the wordmark at 20 (.lg-br): the second half of the word in the accent. */
function AuthBrand() {
  const {
    wordmark: [a, b],
  } = useBrand();
  return (
    <span className="flex items-center gap-2.5">
      <AnimatedMark size={40} mode="intro" />
      <span className="text-xl font-extrabold tracking-[-0.01em]">
        {a}
        <span className="text-accent-text transition-colors duration-500">{b}</span>
      </span>
    </span>
  );
}

export function StepDots({ step, total }: { step: number; total: number }) {
  const t = useT();
  return (
    <div role="img" aria-label={t("auth.step", { n: step + 1, total })} className="flex gap-1">
      {Array.from({ length: total }, (_, i) => (
        <span
          key={i}
          className={cx(
            "h-1.5 rounded-[3px] transition-[width,background-color] duration-[400ms] ease-spring",
            i === step ? "w-[18px]" : "w-1.5",
            i <= step ? "bg-accent-mark" : "bg-surface-2",
          )}
        />
      ))}
    </div>
  );
}

/**
 * The title (32/36, medium) and under it one quiet line (13, muted): the address for sign-in (`mono`), a sentence for the
 * other pages. `focus` takes the focus on mount: a view that replaced the button just pressed (a result, an error) is read
 * out from its title.
 */
export function Heading({ title, children, focus, mono }: { title: ReactNode; children?: ReactNode; focus?: boolean; mono?: boolean }) {
  const ref = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    if (focus) ref.current?.focus();
  }, [focus]);
  return (
    <div>
      <h1 ref={ref} tabIndex={focus ? -1 : undefined} className="text-[32px] leading-9 font-medium tracking-[-0.02em] outline-none max-[720px]:text-[28px]">
        {title}
      </h1>
      {children && <p className={cx("mt-2 text-[13px] text-pretty text-muted", mono && "font-mono font-medium tabular-nums")}>{children}</p>}
    </div>
  );
}

/**
 * The main button of the page (.lg-go: 48 high, a 12 radius, 15px). It turns while it waits (`busy`: a ring instead of the
 * glyph, no clicks, nothing dimmed) and is 40 % of itself while it cannot be used yet (`disabled`).
 */
export function GoButton({
  children,
  busy,
  disabled,
  onClick,
  type = "button",
  glyph,
  className,
}: {
  children: ReactNode;
  busy?: boolean;
  disabled?: boolean;
  onClick?: () => void;
  type?: "button" | "submit";
  /** The board's path of the glyph before the words (the fingerprint of a passkey); a ring replaces it while busy. */
  glyph?: string;
  className?: string;
}) {
  return (
    <Button
      type={type}
      variant="primary"
      size="lg"
      full
      disabled={disabled}
      onClick={onClick}
      className={cx("h-12! rounded-xl! text-[15px]! data-disabled:opacity-40!", busy && "pointer-events-none", className)}
    >
      {busy ? <Spinner /> : glyph && <Glyph d={glyph} />}
      {children}
    </Button>
  );
}

/** The line under a main button (.lg-n: 13, muted, centred); red when something failed. It is announced when it changes. */
export function GoNote({ children, bad }: { children: ReactNode; bad?: boolean }) {
  return (
    <p aria-live="polite" className={cx("text-center text-[13px] text-pretty", bad ? "text-danger-text" : "text-muted")}>
      {children}
    </p>
  );
}

/** The passkey button with its line (.lg-m): the fingerprint, the words, and under them what is going on or what went wrong. */
export function PasskeyBlock({
  label,
  hint,
  onClick,
  disabled,
  busy,
  bad,
}: {
  label: string;
  hint: string;
  onClick: () => void;
  disabled?: boolean;
  busy?: boolean;
  bad?: boolean;
}) {
  return (
    <div className="flex animate-[mg-up_0.3s_var(--e)_both] flex-col gap-3">
      <GoButton glyph={glyphs.fingerprint} busy={busy} disabled={disabled} onClick={onClick}>
        {label}
      </GoButton>
      <GoNote bad={bad}>{hint}</GoNote>
    </div>
  );
}

/** "---- another way ----" divider that shows or hides the password form (.lg-or). */
export function OtherWay({ open, label, onToggle }: { open: boolean; label: string; onToggle: () => void }) {
  return (
    <button
      type="button"
      aria-expanded={open}
      onClick={onToggle}
      className="flex items-center gap-3 text-[13px] text-sec transition-colors duration-150 before:h-0 before:flex-1 before:border-t before:border-line after:h-0 after:flex-1 after:border-t after:border-line hover:text-fg"
    >
      <span className="underline decoration-line underline-offset-4">{label}</span>
    </button>
  );
}

const digitsOnly = (s: string) => s.replace(/\D/g, "");

/**
 * Six digits, one box each (.otp: 52 high, mono 20, a short line after the third). The value is the digits typed so far; a
 * paste or the one-time-code autofill of a phone lands in the box it is made in and spreads over the next ones. The first box
 * carries the autofill hint. Backspace in an empty box takes the digit before it; the arrows walk the row.
 */
export function CodeField({
  value,
  onChange,
  label,
  error,
  hideLabel,
  autoFocus,
}: {
  value: string;
  onChange: (value: string) => void;
  label: string;
  error?: string;
  /** The window around it says what the boxes are for: the name stays for a screen reader only. */
  hideLabel?: boolean;
  autoFocus?: boolean;
}) {
  const t = useT();
  const id = useId();
  const boxes = useRef<(HTMLInputElement | null)[]>([]);
  const focusAt = (i: number) => {
    const el = boxes.current[Math.max(0, Math.min(5, i))];
    el?.focus();
    el?.select();
  };

  function type(i: number, raw: string) {
    const typed = digitsOnly(raw);
    // a gap is not possible: a digit goes to the first free box at the latest
    const at = Math.min(i, value.length);
    if (!typed) {
      onChange(value.slice(0, i) + value.slice(i + 1));
      return;
    }
    const next = typed.length === 1 ? value.slice(0, at) + typed + value.slice(at + 1) : (value.slice(0, at) + typed).slice(0, 6);
    onChange(next.slice(0, 6));
    focusAt(typed.length === 1 ? at + 1 : Math.min(5, at + typed.length));
  }
  function key(i: number, e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Backspace" && !e.currentTarget.value && i > 0) {
      e.preventDefault();
      onChange(value.slice(0, i - 1) + value.slice(i));
      focusAt(i - 1);
    } else if (e.key === "ArrowLeft") {
      e.preventDefault();
      focusAt(i - 1);
    } else if (e.key === "ArrowRight") {
      e.preventDefault();
      focusAt(i + 1);
    }
  }
  function paste(i: number, e: ClipboardEvent<HTMLInputElement>) {
    const typed = digitsOnly(e.clipboardData.getData("text"));
    if (typed.length < 2) return; // one digit is typed by the box itself
    e.preventDefault();
    type(i, typed);
  }

  const noteId = `${id}-note`;
  return (
    <div className="flex flex-col gap-1.5">
      <span id={`${id}-label`} className={hideLabel ? "sr-only" : "text-[13px] text-sec"}>
        {label}
      </span>
      <div role="group" aria-labelledby={`${id}-label`} aria-describedby={error ? noteId : undefined} dir="ltr" className="wn-otp">
        {Array.from({ length: 6 }, (_, i) => (
          <span key={i} className="contents">
            {i === 3 && <i aria-hidden />}
            <input
              ref={(el) => {
                boxes.current[i] = el;
              }}
              value={value[i] ?? ""}
              onChange={(e) => type(i, e.target.value)}
              onKeyDown={(e) => key(i, e)}
              onPaste={(e) => paste(i, e)}
              onFocus={(e) => e.currentTarget.select()}
              inputMode="numeric"
              autoComplete={i === 0 ? "one-time-code" : "off"}
              aria-label={t("auth.digit", { n: i + 1 })}
              aria-invalid={!!error}
              autoCapitalize="off"
              spellCheck={false}
              autoFocus={autoFocus && i === 0}
            />
          </span>
        ))}
      </div>
      {error && (
        <span id={noteId} role="alert" className="screen-enter text-xs leading-4 text-danger-text">
          {error}
        </span>
      )}
    </div>
  );
}

/** Password field: the field of the panel, masked, with the right autofill hint. */
export function PasswordField({
  value,
  onChange,
  label,
  autoComplete,
  error,
  hint,
}: {
  value: string;
  onChange: (value: string) => void;
  label: string;
  autoComplete: "current-password" | "new-password";
  error?: string;
  hint?: string;
}) {
  return <TextField type="password" label={label} value={value} onChange={(e) => onChange(e.target.value)} autoComplete={autoComplete} error={error} hint={hint} />;
}

/** QR + the secret to type by hand, for adding the account to an authenticator app. */
export function TotpEnroll({ qr, secret, caption }: { qr: QrBits | undefined; secret: string; caption: string }) {
  return (
    <div className="flex items-center gap-3.5 rounded-card border border-line bg-surface p-3.5">
      <QrCard code={qr} label={caption} size={152} />
      <div className="flex min-w-0 flex-col gap-1.5">
        <span className="text-[13px] leading-snug font-medium">{caption}</span>
        <span className="font-mono text-[11px] leading-snug break-all text-muted">{secret.replace(/(.{4})/g, "$1 ").trim()}</span>
      </div>
    </div>
  );
}

function mmss(ms: number) {
  const s = Math.max(0, Math.ceil(ms / 1000));
  return `${String(Math.floor(s / 60)).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
}

/** Lockout: danger card with a mm:ss countdown to `until` (epoch ms). Calls onExpire at zero. */
export function LockedCard({ until, onExpire }: { until: number; onExpire?: () => void }) {
  const t = useT();
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);
  const left = until - now;
  useEffect(() => {
    if (left <= 0) onExpire?.();
  }, [left, onExpire]);
  return (
    <div
      role="alert"
      className="flex flex-col items-center gap-3 rounded-card border border-[color-mix(in_oklch,var(--danger)_45%,var(--border))] bg-danger-soft p-5 text-center"
    >
      <span className="text-xl font-medium tracking-[-0.01em]">{t("auth.lockedTitle")}</span>
      <span className="font-mono text-[40px] leading-none font-medium tracking-[-0.04em] tabular-nums" aria-label={mmss(left)}>
        {mmss(left)}
      </span>
      <span className="text-[13px] leading-normal text-pretty text-muted">{t("auth.lockedBody")}</span>
    </div>
  );
}
