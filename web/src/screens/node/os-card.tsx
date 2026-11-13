import type { ReactNode } from "react";
import { kindTextClass } from "@/components/ui/status";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { leftoverText, lockNowText } from "@/lib/os-fleet";
import { osRows, osSystem, osUpdatesLine, useOsUpdates, type OsRow } from "@/lib/os-updates";
import { OsNodeActions } from "@/screens/updates/os-actions";
import { OsKernels } from "@/screens/updates/os-kernels";
import { SecurityNames } from "@/screens/updates/os-security";
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

export function OsFacts({ row, namesOpen }: { row: OsRow; /** The dev kit shows the names of the security packages open. */ namesOpen?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const s = row.status;
  if (!s || s.error) return <p className="text-xs leading-normal text-pretty text-muted">{row.why ? t(row.why) : t("os.card.noStatus")}</p>;
  const next = s.kernelNewest && s.kernelNewest !== s.kernelRunning ? s.kernelNewest : "";
  const packages = s.rebootReasons.filter((r) => r !== "kernel");
  const why = s.rebootReasons.includes("kernel") && s.kernelNewest ? t("up.os.reboot.kernel", { version: s.kernelNewest }) : packages.length > 0 ? t("up.os.reboot.packages", { names: packages.slice(0, 3).join(", ") }) : "";
  const updates = osUpdatesLine(t, s);
  // who is installing packages right now, and what an automatic update left behind: calm lines under the count
  const lock = s.apt === "locked" ? lockNowText(t, s, row.nowUnix ?? 0) : "";
  const leftover = leftoverText(t, s);
  const auto = s.autoUpdates === "on" || s.autoUpdates === "off" || s.autoUpdates === "not_installed" ? t(`os.auto.${s.autoUpdates}`) : "—";
  return (
    <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-xs">
      <Fact label={t("os.fact.system")}>{osSystem(s) || "—"}</Fact>
      <Fact label={t("os.fact.kernel")}>
        <span dir="ltr" className="block font-mono whitespace-nowrap">{s.kernelRunning || "—"}</span>
        {next && <span dir="ltr" className={cx("block font-mono whitespace-nowrap", kindTextClass.warn)}>→ {next}</span>}
      </Fact>
      <Fact label={t("os.fact.updates")} tone={updates.warn ? "warn" : undefined}>
        {updates.main}
        {updates.note && <span className="block text-muted">{updates.note}</span>}
        <SecurityNames status={s} node={row.name ?? ""} defaultOpen={namesOpen} />
        {lock && <span className="block text-muted">{lock}</span>}
        {leftover && <span className="block text-muted">{leftover}</span>}
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
export function OsCard({ nodeId, namesOpen }: { nodeId: string; namesOpen?: boolean }) {
  const t = useT();
  const lacking = useEdition().off("os_updates");
  const q = useOsUpdates(!lacking);
  if (lacking) return null;
  const row = q.data ? osRows(q.data).find((r) => r.nodeId === nodeId) : undefined;
  if (!row) return null;
  return (
    <Panel title={t("os.card.title")} icon="cpu" tone="mint">
      <p className="-mt-1 text-xs leading-normal text-pretty text-muted">{t("os.card.body")}</p>
      <OsFacts row={row} namesOpen={namesOpen} />
      <OsKernels row={row} />
      <div className="border-t border-line pt-3 empty:hidden">
        <OsNodeActions row={row} />
      </div>
    </Panel>
  );
}
