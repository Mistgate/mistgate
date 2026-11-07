import { useId, useState, type CSSProperties } from "react";
import { Icon } from "@/components/ui/icons";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";

/**
 * A field for a secret (a password, a key, a token) that goes straight to the panel's own call and never through the
 * assistant. A password box with a show/hide toggle; no browser autofill or saving (`new-password` is what browsers do not
 * offer to save a login for); paste works; the value is the caller's state, and the caller clears it as soon as it is sent.
 * Under it a lock and a muted line saying where the value goes.
 */
export function SecureField({ label, value, onChange, placeholder, maxLength = 1024, mono = true, hint, disabled, autoFocus, multiline }: { label: string; value: string; onChange: (v: string) => void; placeholder?: string; maxLength?: number; mono?: boolean; hint?: string; disabled?: boolean; autoFocus?: boolean; multiline?: boolean }) {
  const t = useT();
  const id = useId();
  const [shown, setShown] = useState(false);
  return (
    <div className="flex flex-col gap-1.5" data-slot="secure-field">
      <label htmlFor={id} className="text-xs font-medium text-muted">
        {label}
      </label>
      <div className="flex items-center rounded-xl border border-line bg-canvas transition-colors focus-within:border-accent-line">
        <span aria-hidden className="grid size-10 flex-none place-items-center text-muted">
          <Icon name="lock" size={14} />
        </span>
        {multiline ? (
          <textarea
            id={id}
            value={value}
            disabled={disabled}
            rows={4}
            placeholder={placeholder}
            maxLength={maxLength}
            onChange={(e) => onChange(e.target.value)}
            autoComplete="off"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            data-lpignore="true"
            data-1p-ignore="true"
            dir="ltr"
            style={shown ? undefined : ({ WebkitTextSecurity: "disc" } as CSSProperties)}
            className={cx("min-h-20 min-w-0 flex-1 resize-y bg-transparent py-2.5 pe-1 text-[13px] outline-none placeholder:text-faint", mono && "font-mono")}
          />
        ) : (
        <input
          id={id}
          type={shown ? "text" : "password"}
          value={value}
          disabled={disabled}
          autoFocus={autoFocus}
          placeholder={placeholder}
          maxLength={maxLength}
          onChange={(e) => onChange(e.target.value)}
          autoComplete="new-password"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          data-lpignore="true"
          data-1p-ignore="true"
          dir="ltr"
          className={cx("h-10 min-w-0 flex-1 bg-transparent pe-1 text-[13.5px] outline-none placeholder:text-faint", mono && "font-mono")}
        />
        )}
        <button type="button" aria-label={shown ? t("ai.secure.hide") : t("ai.secure.show")} aria-pressed={shown} title={shown ? t("ai.secure.hide") : t("ai.secure.show")} onClick={() => setShown((v) => !v)} className="grid size-10 flex-none place-items-center text-muted transition-colors hover:text-fg">
          <Icon name={shown ? "eyeOff" : "eye"} size={15} />
        </button>
      </div>
      <span className="flex items-start gap-1.5 text-[11.5px] leading-snug text-muted">
        <Icon name="lock" size={11} className="mt-0.5 flex-none" />
        {hint ?? t("ai.secure.note")}
      </span>
    </div>
  );
}
