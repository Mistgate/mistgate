import { useQuery } from "@tanstack/react-query";
import { CopyButton } from "@/components/copy-button";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import { instanceQuery } from "@/lib/instance";
import { meQuery } from "@/lib/session";
import { CmdRow, ICONS, Mono, Pb, Row, Rows, Sec, slots, SxGate } from "./sx-bits";

/**
 * A value to copy (an address, a command): mono text in a field-like row, "Copy" on the right. The old look, kept for the pages that
 * are not redrawn yet (Security, Backups, and the restore window, which is drawn in a portal outside the settings root); the new
 * look is `CmdRow` of sx-bits.
 */
export function CopyRow({ value }: { value: string }) {
  return (
    <div className="flex items-center gap-2 rounded-field border border-line bg-canvas py-1.5 pe-1.5 ps-3">
      {/* wraps at a space where it can ("reset-login" stays whole), anywhere only when it must (a long address) */}
      <span className="min-w-0 flex-1 font-mono text-xs leading-snug [overflow-wrap:anywhere] select-all">{value}</span>
      <CopyButton value={value} />
    </div>
  );
}

/**
 * Settings -> Domains (the board's sxIs_dom): where the admin and the subscription links live. Read-only: `mistgate setup` sets both once,
 * and nothing changes them. The server fills the addresses for the owner only.
 */
export function DomainsPage() {
  const t = useT();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const q = useQuery({ ...instanceQuery, enabled: owner });
  const i = q.data?.instance;
  return (
    <SxGate pending={owner && q.isPending} error={owner && q.isError && !i} onRetry={() => void q.refetch()}>
      <div className="sx-one">
        <Sec tone="sky" d={ICONS.globe} title={t("sx.dom")} foot={i ? slots(t("sx.dom.foot"), { cmd: <Mono>mistgate setup</Mono> }) : undefined}>
          {!owner && <p className="sb-emp">{t("set.domains.ownerOnly")}</p>}
          {i && (
            <>
              <Rows>
                <Row label={t("sx.dom.admin")}>
                  <CmdRow value={i.adminUrl} />
                </Row>
                <Row label={t("sx.dom.sub")}>{i.subscriptionBase ? <CmdRow value={i.subscriptionBase} /> : <p className="sx-none">{t("set.domains.subNone")}</p>}</Row>
              </Rows>
              <Pb tone="part" className="sx-pbm">
                {t("sx.dom.warn")}
              </Pb>
            </>
          )}
        </Sec>
      </div>
    </SxGate>
  );
}
