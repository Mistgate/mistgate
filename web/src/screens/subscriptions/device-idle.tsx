import { useState } from "react";
import { useToast } from "@/components/ui/toast";
import { errorText } from "@/lib/errors";
import { useTx } from "@/screens/users/t";
import type { Settings } from "./model";
import { path, Row, Section, Toggle } from "./parts";
import { useSaveSettings } from "./queries";

/** The panel's own default (the settings it sends always carry one; an older answer without it reads as this). */
export const idleDefaultDays = 30;
export const idleMinDays = 7;
export const idleMaxDays = 365;

/** Days without a connection after which a device stops taking a place; 0 = never. */
export const idleDays = (s: Pick<Settings, "deviceIdleAfterDays">) => s.deviceIdleAfterDays ?? idleDefaultDays;

/** A whole number of days the panel accepts, or null. */
export function parseIdleDays(text: string): number | null {
  const n = Number(text.trim());
  return text.trim() !== "" && Number.isInteger(n) && n >= idleMinDays && n <= idleMaxDays ? n : null;
}

/**
 * "Inactive devices", beside the device limit mode: a device with no connection for N days does not take a place in the limit.
 * The switch saves at once (off = 0; on = 30 days); the number saves when the field is left or Enter is pressed, and says what
 * is wrong with a number the panel would refuse. Nothing is deleted: the sentence under it says what stays.
 */
export function DeviceIdleCard({ settings }: { settings: Settings }) {
  const t = useTx();
  const toast = useToast();
  const save = useSaveSettings();
  const days = idleDays(settings);
  const on = days > 0;
  // what is typed, until it is saved; null = show the saved number
  const [draft, setDraft] = useState<string | null>(null);
  const shown = draft ?? String(on ? days : idleDefaultDays);
  const valid = parseIdleDays(shown);

  const apply = (v: number) =>
    save.mutateAsync((cur) => ({ ...cur, deviceIdleAfterDays: v })).then(
      () => {
        setDraft(null);
        toast(t("subs.saved"));
      },
      (e: unknown) => toast.error(errorText(e, t)),
    );
  const commit = () => {
    if (draft === null || valid === null) return;
    if (valid === days) return setDraft(null);
    void apply(valid);
  };

  return (
    <section id="device-idle">
      <Section tone="sky" icon={path.pulse} title={t("subs.idle.title")} shot="subs-device-idle">
        <div className="sb-tgs">
          <Toggle title={t("subs.idle.aria")} hint={on ? t.n("subs.idle.on", days) : t("subs.idle.off")} checked={on} disabled={save.isPending} onChange={(v) => void apply(v ? idleDefaultDays : 0)} />
        </div>
        {on && (
          <div className="pp-rows">
            <Row label={t("subs.idle.days")} hint={t("subs.idle.hint")}>
              <input
                className="an-i mono sb-days"
                type="number"
                inputMode="numeric"
                aria-label={t("subs.idle.days")}
                aria-invalid={(draft !== null && valid === null) || undefined}
                min={idleMinDays}
                max={idleMaxDays}
                step={1}
                value={shown}
                disabled={save.isPending}
                onChange={(e) => setDraft(e.target.value)}
                onBlur={commit}
                onKeyDown={(e) => e.key === "Enter" && commit()}
              />
              {draft !== null && valid === null && (
                <p className="pp-q1 bad" role="alert">
                  {t("subs.idle.invalid")}
                </p>
              )}
            </Row>
          </div>
        )}
        {!on && <p className="sb-lead sb-lead-end">{t("subs.idle.hint")}</p>}
      </Section>
    </section>
  );
}
