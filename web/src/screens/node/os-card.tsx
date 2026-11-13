import type { ReactNode } from "react";
import { kindTextClass } from "@/components/ui/status";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { osRows, osSystem, useOsUpdates, type OsRow } from "@/lib/os-updates";
import { OsNodeActions } from "@/screens/updates/os-actions";
import { OsKernels } from "@/screens/updates/os-kernels";
import { Panel } from "@/screens/users/ui";

// Node -> Server: the node's operating system (what the Updates page shows in its Server operating system table) and the
// buttons that install its security updates or reboot it. The data is the page's own query, found by node: no second poll.

function Fact({ label, children, tone }: { label: string; children: ReactNode; tone?: "warn" }) {
  return (
    <>
      <dt className="text-muted">{label}</dt>
      <dd className={cx("min-w-0 break-words", tone === "warn" && kindTextClass.warn)}>{children}</dd>
    </>
  );
}

export function OsFacts({ row }: { row: OsRow }) {
  const t = useT();
  const fmt = useFmt();
  const s = row.status;
  if (!s || s.error) return <p className="text-xs leading-normal text-pretty text-muted">{row.why ? t(row.why) : t("os.card.noStatus")}</p>;
  const next = s.kernelNewest && s.kernelNewest !== s.kernelRunning ? s.kernelNewest : "";
  const packages = s.rebootReasons.filter((r) => r !== "kernel");
  const why = s.rebootReasons.includes("kernel") && s.kernelNewest ? t("up.os.reboot.kernel", { version: s.kernelNewest }) : packages.length > 0 ? t("up.os.reboot.packages", { names: packages.slice(0, 3).join(", ") }) : "";
  const auto = s.autoUpdates === "on" || s.autoUpdates === "off" || s.autoUpdates === "not_installed" ? t(`os.auto.${s.autoUpdates}`) : "—";
  return (
    <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-xs">
      <Fact label={t("os.fact.system")}>{osSystem(s) || "—"}</Fact>
      <Fact label={t("os.fact.kernel")}>
        <span dir="ltr" className="block font-mono whitespace-nowrap">{s.kernelRunning || "—"}</span>
        {next && <span dir="ltr" className={cx("block font-mono whitespace-nowrap", kindTextClass.warn)}>→ {next}</span>}
      </Fact>
      <Fact label={t("os.fact.updates")} tone={s.security > 0 ? "warn" : undefined}>
        {s.pending === 0 ? t("up.os.updates.none") : s.security > 0 ? t("up.os.updates.security", { n: s.pending, s: s.security }) : t("up.os.updates.count", { n: s.pending })}
      </Fact>
      <Fact label={t("os.fact.reboot")} tone={s.rebootRequired ? "warn" : undefined}>
        {s.rebootRequired ? [t("os.fact.reboot.yes"), why].filter(Boolean).join(" · ") : t("os.fact.reboot.no")}
      </Fact>
      <Fact label={t("os.fact.lists")}>{s.listsUnix > 0 ? fmt.ago(s.listsUnix) : t("up.os.lists.none")}</Fact>
      <Fact label={t("os.fact.auto")}>{auto}</Fact>
    </dl>
  );
}

/** "Operating system" of a node. Absent on the Cloudflare edition (it has no such data), and while the page's list has not got the node. */
export function OsCard({ nodeId }: { nodeId: string }) {
  const t = useT();
  const lacking = useEdition().off("os_updates");
  const q = useOsUpdates(!lacking);
  if (lacking) return null;
  const row = q.data ? osRows(q.data).find((r) => r.nodeId === nodeId) : undefined;
  if (!row) return null;
  return (
    <Panel title={t("os.card.title")} icon="cpu" tone="mint">
      <p className="-mt-1 text-xs leading-normal text-pretty text-muted">{t("os.card.body")}</p>
      <OsFacts row={row} />
      <OsKernels row={row} />
      <div className="border-t border-line pt-3 empty:hidden">
        <OsNodeActions row={row} />
      </div>
    </Panel>
  );
}
