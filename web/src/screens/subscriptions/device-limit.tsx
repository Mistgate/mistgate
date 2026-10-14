import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { LinkDeviceLimit } from "@/gen/mistgate/admin/v1/subscription_pb";
import { Card, SectionLabel } from "@/components/ui/bits";
import { useToast } from "@/components/ui/toast";
import { errorText } from "@/lib/errors";
import { userCountQuery } from "@/lib/queries";
import { useTx, type Key } from "@/screens/users/t";
import { ConfirmModal } from "@/screens/users/ui";
import type { Settings } from "./model";
import { settingsQuery, useSaveSettings } from "./queries";

const modes: { value: LinkDeviceLimit; label: Key; hint: Key }[] = [
  { value: LinkDeviceLimit.TRACK, label: "subs.devLimit.track", hint: "subs.devLimit.trackHint" },
  { value: LinkDeviceLimit.ENFORCE, label: "subs.devLimit.enforce", hint: "subs.devLimit.enforceHint" },
  { value: LinkDeviceLimit.STRICT, label: "subs.devLimit.strict", hint: "subs.devLimit.strictHint" },
];

/**
 * "Device limit for apps on the link": only count, limit, or strict, one short line each. A pick is saved at once, except
 * the two that cut somebody off: strict asks first with how many people use apps without a device ID now (they stop
 * connecting), and "limit" asks while people are over their limit (they keep their devices, a new one is refused). The
 * details live in those dialogs, not on the page. The user card links here with #device-limit.
 */
export function DeviceLimitCard({ settings }: { settings: Settings }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const save = useSaveSettings();
  const shared = useQuery(settingsQuery).data?.sharedLinkUsers ?? 0;
  const over = useQuery(userCountQuery).data?.counts?.overDeviceLimit ?? 0;
  const mode = settings.linkDeviceLimit ?? LinkDeviceLimit.UNSPECIFIED;
  // the mode being confirmed stays set while the dialog fades out
  const [asking, setAsking] = useState<LinkDeviceLimit | null>(null);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (window.location.hash === "#device-limit") document.getElementById("device-limit")?.scrollIntoView({ block: "center" });
  }, []);

  const apply = (v: LinkDeviceLimit) =>
    save.mutateAsync((cur) => ({ ...cur, linkDeviceLimit: v })).then(
      () => toast(t("subs.saved")),
      (e: unknown) => {
        toast.error(errorText(e, t));
        throw e;
      },
    );
  const ask = (v: LinkDeviceLimit) => {
    setAsking(v);
    setOpen(true);
  };
  function pick(v: LinkDeviceLimit) {
    if (v === mode) return;
    if (v === LinkDeviceLimit.STRICT) {
      // the count is the panel's of a moment ago: ask again while the dialog is open
      void qc.invalidateQueries({ queryKey: settingsQuery.queryKey });
      return ask(v);
    }
    if (v === LinkDeviceLimit.ENFORCE && mode !== LinkDeviceLimit.STRICT && over > 0) return ask(v);
    void apply(v).catch(() => {});
  }
  const strict = asking === LinkDeviceLimit.STRICT;

  return (
    <section id="device-limit">
      <Card lg className="flex min-w-0 flex-col gap-3 p-4">
        <SectionLabel icon="phone" tone="mint" as="h2">
          {t("subs.devLimit.title")}
        </SectionLabel>
        <RadioGroup aria-label={t("subs.devLimit.title")} value={String(mode)} onValueChange={(v) => pick(Number(v) as LinkDeviceLimit)} disabled={save.isPending} className="grid gap-1.5 md:grid-cols-3">
          {modes.map((m) => (
            <Radio.Root
              key={m.value}
              value={String(m.value)}
              className="group flex cursor-pointer items-start gap-3 rounded-field border border-line bg-surface p-3 text-left transition-colors duration-200 data-checked:border-accent-line data-checked:bg-accent-soft data-disabled:cursor-default"
            >
              <span aria-hidden className="mt-0.5 grid size-4 flex-none place-items-center rounded-full border-[1.5px] border-faint group-data-checked:border-accent">
                <Radio.Indicator className="size-2 rounded-full bg-accent" />
              </span>
              <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="text-[13px] font-bold">{t(m.label)}</span>
                <span className="text-xs leading-snug text-pretty text-muted">{t(m.hint)}</span>
              </span>
            </Radio.Root>
          ))}
        </RadioGroup>
      </Card>
      <ConfirmModal
        open={open}
        onOpenChange={setOpen}
        title={t(strict ? "subs.devLimit.strictT" : "subs.devLimit.enforceT")}
        description={strict ? undefined : t.n("subs.devLimit.enforceBody", over)}
        confirmLabel={t(strict ? "subs.devLimit.strictDo" : "subs.devLimit.enforceDo")}
        danger={strict}
        onConfirm={() => apply(asking!)}
      >
        {strict && (
          <div className="flex flex-col gap-2 text-[13px] leading-normal text-pretty">
            <p className={shared > 0 ? "font-semibold text-fg" : "text-muted"}>{shared > 0 ? t.n("subs.devLimit.strictBody", shared) : t("subs.devLimit.strictNone")}</p>
            <p className="text-muted">{t("subs.devLimit.strictMore")}</p>
          </div>
        )}
      </ConfirmModal>
    </section>
  );
}
