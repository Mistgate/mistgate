import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Segmented } from "@/components/ui/segmented";
import { OsRebootPolicy, OsRolloutMode } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import { minutesText } from "@/lib/format";
import { minutesUntil, type OsActions } from "@/lib/os-run";
import type { OsRow } from "@/lib/os-updates";
import { SwitchRow } from "@/screens/users/ui";

// The window before an OS update of one node: what will happen (packages, kernel, who is online), the one choice
// (reboot if required, or not), what to watch for, then the button. A reboot the node itself finds unsafe is refused by the
// node; there is no "reboot anyway" here.

export type OsDialogMode = "install" | "reboot";

/** One fact of a window, a plain line: no icon, so every text of the window starts on the same vertical. */
export function Fact({ children }: { children: ReactNode }) {
  return <li className="text-[13px] leading-snug text-pretty">{children}</li>;
}

/** A small heading over a group of controls, in the style of the other compact windows ("Order"). */
export function Heading({ id, children }: { id: string; children: ReactNode }) {
  return (
    <h3 id={id} className="text-[11px] font-bold tracking-wide text-muted uppercase">
      {children}
    </h3>
  );
}

/** "Reboot if required" as the app's own settings row (SwitchRow): its words, a one-line hint that follows the switch, and the switch. */
export function RebootRow({ on, onChange }: { on: boolean; onChange: (on: boolean) => void }) {
  const t = useT();
  // in a box of its own so the row's divider (it has one between rows of a list) does not draw above it
  return (
    <div>
      <SwitchRow label={t("osf.reboot.label")} hint={t(on ? "osf.reboot.on" : "osf.reboot.off")} checked={on} onCheckedChange={onChange} className="min-h-0! py-0.5" />
    </div>
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
      {choosePkgs && (
        <section aria-labelledby="osf-one-what" className="flex flex-col gap-1.5">
          <Heading id="osf-one-what">{t("osf.h.what")}</Heading>
          <Segmented
            variant="thumb"
            wrap
            aria-label={t("osf.one.what")}
            value={pkgs}
            onValueChange={setPkgs}
            options={[
              { value: "security", label: t("osf.seg.security") },
              { value: "all", label: t("osf.seg.all") },
            ]}
          />
        </section>
      )}
      {install && <RebootRow on={reboot === "if_required"} onChange={(on) => setReboot(on ? "if_required" : "never")} />}
      <ul className="flex flex-col gap-1.5">
        {!install && <Fact>{t("os.dlg.reboot.lead")}</Fact>}
        {install && (
          <Fact>
            {all ? t.n("osf.one.all.fact", pending) : t.n("os.dlg.security", security)}
            {!all && others > 0 && <span className="text-muted"> · {t.n("os.dlg.others", others)}</span>}
          </Fact>
        )}
        {newKernel && <Fact>{t("os.dlg.kernel", { from: s?.kernelRunning ?? "", to: s?.kernelNewest ?? "" })}</Fact>}
        {s?.rebootRequired ? (
          <Fact>{due}</Fact>
        ) : (
          !install && <Fact>{t("os.dlg.reboot.notNeeded")}</Fact>
        )}
        {install && (
          <Fact>
            {online > 0 ? t.n("os.dlg.online", online, { node: name }) : t("os.dlg.online.none", { node: name })}
            {online > 0 && willReboot && <span className="text-muted"> {t("os.dlg.online.drop")}</span>}
          </Fact>
        )}
      </ul>
      <p className="text-xs leading-snug text-pretty text-muted">{t("osf.fact.profiles")}</p>
    </Modal>
  );
}
