import { OsStepState, OsUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { lockNowText } from "@/lib/os-fleet";
import { osRunOf } from "@/lib/os-run";
import { osRows, osSystem, osUpdatesLine, useOsUpdates, type OsRow } from "@/lib/os-updates";
import { Glyph, glyphs } from "@/screens/overview-parts";
import { OsNodeActions } from "@/screens/updates/os-actions";
import { OsKernels } from "@/screens/updates/os-kernels";
import { SecurityNames } from "@/screens/updates/os-security";
import { Facts, SvCard } from "./server-parts";

// Node -> Server -> System: the node's operating system (what the Updates page shows in its Server operating system table) as one
// card (the board's .sv-os): an icon and a sentence on how the system is, its facts, and the buttons that install the security
// updates, reboot the node or look for updates. The data is the page's own query, found by node: no second poll.

const refresh = "M20 12a8 8 0 1 1-2.3-5.6M20 4v4h-4";

/** The facts of a node's system as the board's grid: system, kernel (and the one the next reboot starts), updates, reboot, lists, automatic updates. */
export function OsFacts({ row, namesOpen }: { row: OsRow; /** The dev kit shows the names of the security packages open. */ namesOpen?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const s = row.status;
  if (!s || s.error) return <p className="px-6 pb-5 text-[13px] leading-[18px] text-muted max-[720px]:px-4">{row.why ? t(row.why) : t("os.card.noStatus")}</p>;
  const next = s.kernelNewest && s.kernelNewest !== s.kernelRunning ? s.kernelNewest : "";
  const packages = s.rebootReasons.filter((r) => r !== "kernel");
  const why = s.rebootReasons.includes("kernel") && s.kernelNewest ? t("up.os.reboot.kernel", { version: s.kernelNewest }) : packages.length > 0 ? t("up.os.reboot.packages", { names: packages.slice(0, 3).join(", ") }) : "";
  const updates = osUpdatesLine(t, s);
  // who is installing packages right now: a calm line under the count
  const lock = s.apt === "locked" ? lockNowText(t, s, row.nowUnix ?? 0) : "";
  const auto = s.autoUpdates === "on" || s.autoUpdates === "off" || s.autoUpdates === "not_installed" ? t(`os.auto.${s.autoUpdates}`) : "—";
  return (
    <Facts
      className="sv-facts"
      items={[
        { l: t("os.fact.system"), v: osSystem(s) || "—" },
        {
          l: t("os.fact.kernel"),
          mono: true,
          v: (
            <>
              <span dir="ltr" className="block">{s.kernelRunning || "—"}</span>
              {next && (
                <span dir="ltr" className="block text-warn">
                  → {next}
                </span>
              )}
            </>
          ),
        },
        {
          l: t("os.fact.updates"),
          tone: updates.warn ? "warn" : undefined,
          v: (
            <>
              {updates.main}
              {updates.note && <span className="block text-[13px] text-muted">{updates.note}</span>}
              <SecurityNames status={s} node={row.name ?? ""} defaultOpen={namesOpen} />
              {lock && <span className="block text-[13px] text-muted">{lock}</span>}
            </>
          ),
        },
        { l: t("os.fact.reboot"), tone: s.rebootRequired ? "warn" : undefined, v: s.rebootRequired ? [t("os.fact.reboot.yes"), why].filter(Boolean).join(" · ") : t("os.fact.reboot.no") },
        { l: t("os.fact.lists"), v: s.listsUnix > 0 ? fmt.ago(s.listsUnix) : t("up.os.lists.none") },
        { l: t("os.fact.auto"), v: auto },
      ]}
    />
  );
}

/** How the system is, in one sentence and an icon: fine, updates wait, a reboot is due, a job runs, or what the panel cannot read. */
function useVerdict(row: OsRow) {
  const t = useT();
  const fmt = useFmt();
  const s = row.status;
  const run = osRunOf(row);
  const running = run.kind === "running" ? run.step.state : undefined;
  const sys = osSystem(s);
  const sub = (parts: (string | false | undefined)[]) => parts.filter(Boolean).join(" · ");
  if (running === OsStepState.REBOOTING) return { tone: "run", d: refresh, title: t("np.sv.os.rebooting"), sub: t("np.sv.os.rebootingSub") } as const;
  if (running !== undefined) return { tone: "run", d: refresh, title: t("np.sv.os.installing"), sub: t("np.sv.os.installingSub") } as const;
  if (!s || s.error) return { tone: "idle", d: glyphs.warn, title: t("np.sv.os.unknown"), sub: row.why ? t(row.why) : t("os.card.noStatus") } as const;
  const checked = s.listsUnix > 0 ? t("np.sv.os.checked", { ago: fmt.ago(s.listsUnix) }) : "";
  if (s.rebootRequired || row.state === OsUpdateState.REBOOT_REQUIRED) {
    return { tone: "warn", d: glyphs.warn, title: t("np.sv.os.reboot"), sub: s.kernelNewest && s.kernelNewest !== s.kernelRunning ? t("np.sv.os.rebootKernel", { version: s.kernelNewest }) : sub([sys, checked]) } as const;
  }
  if (s.security > 0) return { tone: "warn", d: glyphs.warn, title: t("np.sv.os.updates", { n: s.pending, s: s.security }), sub: t("np.sv.os.updatesSub") } as const;
  return { tone: "ok", d: glyphs.check, title: t("np.sv.os.ok"), sub: sub([sys, s.kernelRunning && t("np.sv.os.kernel", { version: s.kernelRunning }), checked]) } as const;
}

/** "Operating system" of a node. Absent on the Cloudflare edition (it has no such data), and while the page's list has not got the node. */
export function OsCard({ nodeId, namesOpen }: { nodeId: string; namesOpen?: boolean }) {
  const lacking = useEdition().off("os_updates");
  const q = useOsUpdates(!lacking);
  if (lacking) return null;
  const row = q.data ? osRows(q.data).find((r) => r.nodeId === nodeId) : undefined;
  if (!row) return null;
  return <OsCardBody row={row} namesOpen={namesOpen} />;
}

function OsCardBody({ row, namesOpen }: { row: OsRow; namesOpen?: boolean }) {
  const t = useT();
  const v = useVerdict(row);
  return (
    <SvCard className={cx("sv-os", v.tone === "run" ? "run" : v.tone === "warn" ? "warn" : "ok")} aria-label={t("os.card.title")}>
      <div className="sv-oh">
        <span className={cx("dr-ic", v.tone === "warn" || v.tone === "run" ? v.tone : undefined)} aria-hidden>
          <Glyph d={v.d} size={24} stroke={1.5} />
        </span>
        <div className="dr-h">
          <h3>{v.title}</h3>
          <span>{v.sub}</span>
        </div>
      </div>
      <OsFacts row={row} namesOpen={namesOpen} />
      <div className="px-6 pb-4 empty:hidden max-[720px]:px-4">
        <OsKernels row={row} />
      </div>
      <div className="sv-act px-6 py-4 empty:hidden max-[720px]:px-4">
        <OsNodeActions row={row} size="lg" />
      </div>
    </SvCard>
  );
}
