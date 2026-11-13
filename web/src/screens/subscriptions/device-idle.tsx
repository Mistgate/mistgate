import { useState } from "react";
import { halo } from "@/components/fx/halo";
import { Card, SectionLabel } from "@/components/ui/bits";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { errorText } from "@/lib/errors";
import { useTx } from "@/screens/users/t";
import type { Settings } from "./model";
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
        halo("save");
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
      <Card lg className="flex min-w-0 flex-col gap-3 p-4">
        <div className="flex items-start gap-4">
          <div className="flex min-w-0 flex-1 flex-col gap-1">
            <SectionLabel icon="clock" tone="mint" as="h2">
              {t("subs.idle.title")}
            </SectionLabel>
            <p className="text-xs leading-snug text-pretty text-muted">{on ? t.n("subs.idle.on", days) : t("subs.idle.off")}</p>
          </div>
          <Switch checked={on} disabled={save.isPending} onCheckedChange={(v) => void apply(v ? idleDefaultDays : 0)} aria-label={t("subs.idle.aria")} className="mt-0.5" />
        </div>
        {on && (
          <div className="w-64 max-w-full">
            <TextField
              label={t("subs.idle.days")}
              type="number"
              inputMode="numeric"
              min={idleMinDays}
              max={idleMaxDays}
              step={1}
              value={shown}
              disabled={save.isPending}
              error={draft !== null && valid === null ? t("subs.idle.invalid") : undefined}
              onChange={(e) => setDraft(e.target.value)}
              onBlur={commit}
              onKeyDown={(e) => e.key === "Enter" && commit()}
            />
          </div>
        )}
        <p className="text-xs leading-snug text-pretty text-muted">{t("subs.idle.hint")}</p>
      </Card>
    </section>
  );
}
