import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { LinkDeviceLimit } from "@/gen/mistgate/admin/v1/subscription_pb";
import { useToast } from "@/components/ui/toast";
import { errorText } from "@/lib/errors";
import { userCountQuery } from "@/lib/queries";
import { useTx, type Key } from "@/screens/users/t";
import { ConfirmModal } from "@/screens/users/ui";
import type { Settings } from "./model";
import { path, Section } from "./parts";
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
      () => {
        toast(t("subs.saved"));
      },
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
      // the dialog names how many people lose their apps: ask the panel now, not what it said up to 30 s ago
      void qc.fetchQuery({ ...settingsQuery, staleTime: 0 }).then(
        () => ask(v),
        (e: unknown) => toast.error(errorText(e, t)),
      );
      return;
    }
    if (v === LinkDeviceLimit.ENFORCE && mode !== LinkDeviceLimit.STRICT && over > 0) return ask(v);
    void apply(v).catch(() => {});
  }
  const strict = asking === LinkDeviceLimit.STRICT;

  return (
    <section id="device-limit">
      <Section tone="rose" icon={path.monitor} title={t("subs.devLimit.title")} shot="subs-device-limit">
        <div className="sb-pad">
          <RadioGroup aria-label={t("subs.devLimit.title")} value={String(mode)} onValueChange={(v) => pick(Number(v) as LinkDeviceLimit)} disabled={save.isPending} className="pf-opt pp-cards sb-lim3">
            {modes.map((m) => (
              <Radio.Root key={m.value} value={String(m.value)}>
                <span className="nx-rad" aria-hidden="true" aria-checked={String(m.value) === String(mode)}>
                  <i />
                </span>
                <div>
                  <b>{t(m.label)}</b>
                  <span>{t(m.hint)}</span>
                </div>
              </Radio.Root>
            ))}
          </RadioGroup>
        </div>
      </Section>
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
