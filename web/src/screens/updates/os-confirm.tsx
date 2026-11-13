import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Icon, type IconName } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { OsRebootPolicy, OsRolloutMode } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import { minutesText } from "@/lib/format";
import { minutesUntil, type OsActions } from "@/lib/os-run";
import type { OsRow } from "@/lib/os-updates";

// The window before an OS update of one node: what will happen (packages, kernel, who is online), the one choice
// (reboot if required, or not), what to watch for, then the button. A reboot the node itself finds unsafe is refused by the
// node; there is no "reboot anyway" here.

export type OsDialogMode = "install" | "reboot";

export function Fact({ icon, children }: { icon: IconName; children: ReactNode }) {
  return (
    <li className="flex items-start gap-2.5 text-[13px] leading-snug">
      <Icon name={icon} size={15} className="mt-0.5 flex-none text-muted" aria-hidden />
      <span className="text-pretty">{children}</span>
    </li>
  );
}

/** One of a few choices with its words under it (`aside`: a quiet line after them); the long labels wrap instead of squeezing a segmented control. */
export function Choice({ value, label, hint, aside }: { value: string; label: string; hint: string; aside?: string }) {
  return (
    <Radio.Root
      value={value}
      className="flex cursor-pointer items-start gap-2.5 rounded-field border border-line bg-surface-2 p-3 text-start transition-colors duration-200 data-checked:border-accent-line data-checked:bg-surface"
    >
      <span aria-hidden className="mt-0.5 grid size-4 flex-none place-items-center rounded-full border border-line bg-surface">
        <Radio.Indicator className="size-2 rounded-full bg-accent" />
      </span>
      <span className="flex min-w-0 flex-col gap-0.5">
        <span className="flex flex-wrap items-baseline justify-between gap-x-3">
          <span className="text-[13px] font-bold">{label}</span>
          {aside && <span className="text-[11px] font-semibold text-muted">{aside}</span>}
        </span>
        <span className="text-xs leading-snug text-pretty text-muted">{hint}</span>
      </span>
    </Radio.Root>
  );
}

export function OsConfirmDialog({ row, mode, actions, onClose, initialPkgs }: { row: OsRow; mode: OsDialogMode; actions: OsActions; onClose: () => void; /** The dev kit opens the window on "All packages". */ initialPkgs?: "security" | "all" }) {
  const t = useT();
  const s = row.status;
  const name = row.name ?? "";
  const install = mode === "install";
  // a node that was rebooted a short while ago refuses another reboot for now: the install then asks for none, as the safe default
  const rebootWait = minutesUntil(row.rebootPossibleUnix ?? 0, row.nowUnix ?? 0);
  const [reboot, setReboot] = useState<"if_required" | "never">(rebootWait > 0 ? "never" : "if_required");
  const security = s?.security ?? 0;
  const pending = s?.pending ?? 0;
  // what to install: only the security updates, or every waiting package; a node with only ordinary ones waiting has nothing else to choose
  const choosePkgs = install && pending > security && (security > 0 || s?.apt === "half_configured");
  const [pkgs, setPkgs] = useState<"security" | "all">(initialPkgs ?? (security === 0 && pending > 0 ? "all" : "security"));
  const all = install && pkgs === "all";
  const online = row.online ?? 0;
  const others = Math.max(0, pending - security);
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
        mode: install ? (all ? OsRolloutMode.ALL : OsRolloutMode.SECURITY) : OsRolloutMode.REBOOT_ONLY,
        reboot: install ? (reboot === "never" ? OsRebootPolicy.NEVER : OsRebootPolicy.IF_REQUIRED) : OsRebootPolicy.ALWAYS,
      })
      .then((ok) => ok && onClose());

  return (
    <Modal
      open
      shot="os-confirm"
      onOpenChange={(o) => !o && !actions.busy && onClose()}
      title={t(all ? "osf.one.title.all" : install ? "os.dlg.install.title" : "os.dlg.reboot.title", { node: name })}
      description={all ? t("osf.one.lead.all") : install ? t("os.dlg.install.lead") : undefined}
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
      {install && rebootWait > 0 && <Notice>{t("os.dlg.warn.rebootWait", { node: name, time: minutesText(t, rebootWait) })}</Notice>}
      {willReboot && s?.awgModuleNext === "missing" && <Notice>{t("os.dlg.warn.awgMissing")}</Notice>}
      {install && s?.apt === "half_configured" && <Notice>{t("os.dlg.warn.half", { names: s.halfConfigured.slice(0, 3).join(", ") })}</Notice>}
      {install && (s?.held.length ?? 0) > 0 && <Notice tone="quiet">{t("os.dlg.warn.held", { names: s?.held.slice(0, 3).join(", ") ?? "" })}</Notice>}
      <ul className="flex flex-col gap-2">
        {!install && <Fact icon="shield">{t("os.dlg.reboot.lead")}</Fact>}
        {install && (
          <Fact icon="shield">
            {all ? t.n("osf.one.all.fact", pending) : t.n("os.dlg.security", security)}
            {!all && others > 0 && <span className="block text-xs text-muted">{t.n("os.dlg.others", others)}</span>}
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

      {choosePkgs && (
        <RadioGroup value={pkgs} onValueChange={(v) => setPkgs(v as typeof pkgs)} aria-label={t("osf.one.what")} className="flex flex-col gap-2">
          <Choice value="security" label={t("osf.scope.security")} hint={t.n("osf.one.security.hint", security)} />
          <Choice value="all" label={t("osf.scope.all")} hint={t.n("osf.one.all.hint", pending)} />
        </RadioGroup>
      )}
      {install && (
        <RadioGroup value={reboot} onValueChange={(v) => setReboot(v as typeof reboot)} aria-label={t("os.dlg.after")} className="flex flex-col gap-2">
          <Choice value="if_required" label={t("os.dlg.reboot.ifRequired")} hint={t("os.dlg.reboot.ifRequired.hint")} />
          <Choice value="never" label={t("os.dlg.reboot.never")} hint={t("os.dlg.reboot.never.hint")} />
        </RadioGroup>
      )}

    </Modal>
  );
}
