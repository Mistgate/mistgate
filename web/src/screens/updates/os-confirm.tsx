import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Icon, type IconName } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { OsRebootPolicy, OsRolloutMode } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import type { OsActions } from "@/lib/os-run";
import type { OsRow } from "@/lib/os-updates";

// The window before an OS update of one node: what will happen (packages, kernel, who is online), the one choice
// (reboot if required, or not), what to watch for, then the button. A reboot the node itself finds unsafe is refused by the
// node; there is no "reboot anyway" here.

export type OsDialogMode = "install" | "reboot";

function Fact({ icon, children }: { icon: IconName; children: ReactNode }) {
  return (
    <li className="flex items-start gap-2.5 text-[13px] leading-snug">
      <Icon name={icon} size={15} className="mt-0.5 flex-none text-muted" aria-hidden />
      <span className="text-pretty">{children}</span>
    </li>
  );
}

/** One of two choices with its words under it; the long labels wrap instead of squeezing a segmented control. */
function Choice({ value, label, hint }: { value: string; label: string; hint: string }) {
  return (
    <Radio.Root
      value={value}
      className="flex cursor-pointer items-start gap-2.5 rounded-field border border-line bg-surface-2 p-3 text-start transition-colors duration-200 data-checked:border-accent-line data-checked:bg-surface"
    >
      <span aria-hidden className="mt-0.5 grid size-4 flex-none place-items-center rounded-full border border-line bg-surface">
        <Radio.Indicator className="size-2 rounded-full bg-accent" />
      </span>
      <span className="flex min-w-0 flex-col gap-0.5">
        <span className="text-[13px] font-bold">{label}</span>
        <span className="text-xs leading-snug text-pretty text-muted">{hint}</span>
      </span>
    </Radio.Root>
  );
}

export function OsConfirmDialog({ row, mode, actions, onClose }: { row: OsRow; mode: OsDialogMode; actions: OsActions; onClose: () => void }) {
  const t = useT();
  const s = row.status;
  const name = row.name ?? "";
  const install = mode === "install";
  const [reboot, setReboot] = useState<"if_required" | "never">("if_required");
  const online = row.online ?? 0;
  const security = s?.security ?? 0;
  const others = Math.max(0, (s?.pending ?? 0) - security);
  const newKernel = !!s?.kernelNewest && !!s.kernelRunning && s.kernelNewest !== s.kernelRunning;
  const packages = (s?.rebootReasons ?? []).filter((r) => r !== "kernel");
  // why a reboot is already due
  const due = !s?.rebootRequired
    ? ""
    : s.rebootReasons.includes("kernel") && s.kernelNewest
      ? t("os.dlg.rebootNeeded.kernel", { version: s.kernelNewest })
      : packages.length > 0
        ? t("os.dlg.rebootNeeded.packages", { names: packages.slice(0, 3).join(", ") })
        : t("os.dlg.rebootNeeded.plain");
  const willReboot = !install || reboot === "if_required";

  const go = () =>
    void actions
      .start({
        nodeId: row.nodeId ?? "",
        name,
        mode: install ? OsRolloutMode.SECURITY : OsRolloutMode.REBOOT_ONLY,
        reboot: install ? (reboot === "never" ? OsRebootPolicy.NEVER : OsRebootPolicy.IF_REQUIRED) : OsRebootPolicy.ALWAYS,
      })
      .then((ok) => ok && onClose());

  return (
    <Modal
      open
      shot="os-confirm"
      onOpenChange={(o) => !o && !actions.busy && onClose()}
      title={t(install ? "os.dlg.install.title" : "os.dlg.reboot.title", { node: name })}
      description={install ? t("os.dlg.install.lead") : undefined}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={actions.busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          {/* a reboot cuts people off: the red button, like the other destructive windows; installing is the usual blue */}
          <Button variant={install ? "primary" : "danger"} size="md" disabled={actions.busy} onClick={go}>
            {t(install ? "os.dlg.go.install" : "os.dlg.go.reboot")}
          </Button>
        </>
      }
    >
      {/* a reboot starts with what it does to people, large; the warnings come next: below a long list on a phone they would be scrolled out of sight */}
      {!install && (
        <p className="text-base leading-snug font-extrabold text-pretty">
          {online > 0 ? t.n("os.dlg.reboot.lead.online", online, { node: name }) : t("os.dlg.online.none", { node: name })}
        </p>
      )}
      {install && s?.autoReboot && <Notice>{t("os.dlg.warn.autoReboot")}</Notice>}
      {willReboot && s?.awgModuleNext === "missing" && <Notice>{t("os.dlg.warn.awgMissing")}</Notice>}
      {install && s?.apt === "half_configured" && <Notice>{t("os.dlg.warn.half", { names: s.halfConfigured.slice(0, 3).join(", ") })}</Notice>}
      {install && (s?.held.length ?? 0) > 0 && <Notice tone="quiet">{t("os.dlg.warn.held", { names: s?.held.slice(0, 3).join(", ") ?? "" })}</Notice>}
      <ul className="flex flex-col gap-2">
        {!install && <Fact icon="shield">{t("os.dlg.reboot.lead")}</Fact>}
        {install && (
          <Fact icon="shield">
            {t.n("os.dlg.security", security)}
            {others > 0 && <span className="block text-xs text-muted">{t.n("os.dlg.others", others)}</span>}
          </Fact>
        )}
        {newKernel && <Fact icon="cpu">{t("os.dlg.kernel", { from: s?.kernelRunning ?? "", to: s?.kernelNewest ?? "" })}</Fact>}
        {s?.rebootRequired ? (
          <Fact icon="refresh">{due}</Fact>
        ) : (
          !install && <Fact icon="refresh">{t("os.dlg.reboot.notNeeded")}</Fact>
        )}
        {install && (
          <Fact icon="people">
            {online > 0 ? t.n("os.dlg.online", online, { node: name }) : t("os.dlg.online.none", { node: name })}
            {online > 0 && willReboot && <span className="block text-xs text-muted">{t("os.dlg.online.drop")}</span>}
          </Fact>
        )}
        {install && <Fact icon="disk">{t("os.dlg.fact.kernels")}</Fact>}
        <Fact icon="pulse">{t("os.dlg.fact.profiles")}</Fact>
      </ul>

      {install && (
        <RadioGroup value={reboot} onValueChange={(v) => setReboot(v as typeof reboot)} aria-label={t("os.dlg.after")} className="flex flex-col gap-2">
          <Choice value="if_required" label={t("os.dlg.reboot.ifRequired")} hint={t("os.dlg.reboot.ifRequired.hint")} />
          <Choice value="never" label={t("os.dlg.reboot.never")} hint={t("os.dlg.reboot.never.hint")} />
        </RadioGroup>
      )}

    </Modal>
  );
}
