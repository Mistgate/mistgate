import { useEffect, useState, type ReactNode } from "react";
import { Brand } from "@/components/brand";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { QrCode } from "@/components/qr-code";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";

/**
 * Centred column (max 380) shared by sign-in and first-run setup: brand row with an optional slot on
 * the right (the wizard's progress dots), then the content. Language and theme sit in the corner
 * because nobody is signed in yet to reach Settings -> Interface.
 */
export function AuthFrame({ aside, children, viewKey }: { aside?: ReactNode; children: ReactNode; viewKey?: string }) {
  return (
    <div className="relative flex min-h-dvh flex-col items-center px-5 py-10 md:px-10">
      <div className="absolute top-3 right-3 flex items-center gap-2 md:top-4 md:right-5">
        <LangToggle />
        <ThemeToggle />
      </div>
      <div className="my-auto flex w-full max-w-[380px] flex-none flex-col gap-[18px]">
        {/* the brand row stays put across the setup steps: its logo plays its intro once, the content below re-enters */}
        <div className="screen-enter flex items-center gap-2.5">
          <Brand logoSize={41} textSize={20} motion="intro" />
          <div className="flex-1" />
          {aside}
        </div>
        <div key={viewKey} className="screen-enter flex flex-col gap-[18px]">
          {children}
        </div>
      </div>
    </div>
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
            i <= step ? "bg-accent" : "bg-surface-2",
          )}
        />
      ))}
    </div>
  );
}

export function Heading({ title, children }: { title: ReactNode; children?: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <h1 className="text-[26px] leading-tight font-extrabold tracking-[-0.04em]">{title}</h1>
      {children && <p className="text-sm leading-normal text-pretty text-muted">{children}</p>}
    </div>
  );
}

/** Primary passkey button with its one-line hint underneath. */
export function PasskeyBlock({
  label,
  hint,
  onClick,
  disabled,
}: {
  label: string;
  hint: string;
  onClick: () => void;
  disabled?: boolean;
}) {
  return (
    <div className="flex flex-col gap-2.5">
      <Button variant="primary" size="lg" full disabled={disabled} onClick={onClick}>
        {label}
      </Button>
      <span className="text-center text-xs leading-snug text-pretty text-muted">{hint}</span>
    </div>
  );
}

/** "---- another way ----" divider that shows or hides the password form. */
export function OtherWay({ open, label, onToggle }: { open: boolean; label: string; onToggle: () => void }) {
  return (
    <button
      type="button"
      aria-expanded={open}
      onClick={onToggle}
      className="flex items-center justify-center gap-2 text-[13px] font-bold text-muted"
    >
      <span aria-hidden className="h-px flex-1 bg-line" />
      {label}
      <span aria-hidden className="h-px flex-1 bg-line" />
    </button>
  );
}

/** Six digits, mono 22px, tracked wide. Numeric keypad and one-time-code autofill on phones. */
export function CodeField({
  value,
  onChange,
  label,
  error,
}: {
  value: string;
  onChange: (value: string) => void;
  label: string;
  error?: string;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor="otp-code" className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">
        {label}
      </label>
      <input
        id="otp-code"
        value={value}
        onChange={(e) => onChange(e.target.value.replace(/\D/g, "").slice(0, 6))}
        inputMode="numeric"
        autoComplete="one-time-code"
        maxLength={6}
        placeholder="000000"
        aria-invalid={!!error}
        aria-describedby={error ? "otp-error" : undefined}
        className={cx(
          "h-[52px] w-full rounded-field border bg-surface px-4 text-center font-mono text-[22px] font-bold tracking-[0.4em] text-fg outline-none transition-colors duration-200 focus:border-accent",
          error ? "border-danger" : "border-line",
        )}
      />
      {error && (
        <span id="otp-error" className="screen-enter text-xs leading-snug text-danger-text">
          {error}
        </span>
      )}
    </div>
  );
}

/** Password field: same look as TextField, masked, with the right autofill hint. */
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
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor="password" className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">
        {label}
      </label>
      <input
        id="password"
        type="password"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        autoComplete={autoComplete}
        aria-invalid={!!error}
        aria-describedby={error || hint ? "password-note" : undefined}
        className={cx(
          "h-11 w-full rounded-field border bg-surface px-3.5 text-sm font-medium text-fg outline-none transition-colors duration-200 focus:border-accent",
          error ? "border-danger" : "border-line",
        )}
      />
      {(error || hint) && (
        <span id="password-note" className={cx("text-xs leading-snug", error ? "screen-enter text-danger-text" : "text-muted")}>
          {error ?? hint}
        </span>
      )}
    </div>
  );
}

/** QR + the secret to type by hand, for adding the account to an authenticator app. */
export function TotpEnroll({ uri, secret, caption }: { uri: string; secret: string; caption: string }) {
  return (
    <div className="flex items-center gap-3.5 rounded-card border border-line bg-surface p-3.5">
      <QrCode value={uri} label={caption} />
      <div className="flex min-w-0 flex-col gap-1.5">
        <span className="text-[13px] leading-snug font-bold">{caption}</span>
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
      className="flex flex-col items-center gap-3 rounded-card-lg border border-[color-mix(in_oklch,var(--danger)_45%,var(--border))] bg-danger-soft p-5 text-center"
    >
      <span className="text-[19px] font-extrabold tracking-[-0.03em]">{t("auth.lockedTitle")}</span>
      <span className="font-mono text-[40px] leading-none font-bold tracking-[-0.04em]" aria-label={mmss(left)}>
        {mmss(left)}
      </span>
      <span className="text-[13px] leading-normal text-pretty text-muted">{t("auth.lockedBody")}</span>
    </div>
  );
}
