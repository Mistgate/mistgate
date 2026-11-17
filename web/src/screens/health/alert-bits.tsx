import { AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { alertIpv6, type Alert } from "@/lib/health";
import { Gl, P, type DotKind } from "./page-parts";
import "./health.css";

// The small parts the rows of the Health page and its history share.

/** A link from Reachability (/health#alert-<id>, a history row's #alert-<id>-<closed>) opens what it points at. */
export const targeted = (ids: readonly string[]) => typeof location !== "undefined" && ids.some((id) => location.hash === `#${id}`);

/** The pill of a verdict in the board's words: a critical one is red, a warning half amber, a note the quiet grey ring. */
export function pillOf(sev: AlertSeverity): { kind: "bad" | "part" | ""; glyph: DotKind } {
  if (sev === AlertSeverity.CRITICAL) return { kind: "bad", glyph: "bad" };
  if (sev === AlertSeverity.INFO) return { kind: "", glyph: "none" };
  return { kind: "part", glyph: "part" };
}

/**
 * The IPv6 side of a country alert, after its title: "IPv6" on the IPv6 alert itself, "reachable over IPv6" on an IPv4 one whose
 * node the IPv6 rounds reach, "IPv6 entry on" on one whose node has its IPv6 entry on. Nothing on any other alert.
 */
export function Ipv6Tag({ alert, className }: { alert: Alert; className?: string }) {
  const t = useT();
  const v6 = alertIpv6(alert);
  if (v6 === "alert") return <span className={cx("hl-tag mono", className)}>IPv6</span>;
  if (v6 === "open" || v6 === "entry")
    return (
      <span className={cx("hl-tag ok", className)}>
        <Gl d={P.check} />
        {t(v6 === "open" ? "hl.alerts.v6open" : "hl.alerts.v6entry")}
      </span>
    );
  return null;
}
