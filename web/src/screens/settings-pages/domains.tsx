import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { CopyButton } from "@/components/copy-button";
import { SectionLabel } from "@/components/ui/bits";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import { instanceQuery } from "@/lib/instance";
import { meQuery } from "@/lib/session";

/** A value to copy (an address, a command): mono text in a field-like row, "Copy" on the right. */
export function CopyRow({ value }: { value: string }) {
  return (
    <div className="flex items-center gap-2 rounded-field border border-line bg-canvas py-1.5 pr-1.5 pl-3">
      {/* wraps at a space where it can ("reset-login" stays whole), anywhere only when it must (a long address) */}
      <span className="min-w-0 flex-1 font-mono text-xs leading-snug [overflow-wrap:anywhere] select-all">{value}</span>
      <CopyButton value={value} />
    </div>
  );
}

function Labeled({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{label}</span>
      {children}
    </div>
  );
}

/** Settings -> Domains: where the admin and the subscription links live. Read-only: `mistgate setup` sets both once, and nothing changes them. */
export function DomainsPage() {
  const t = useT();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  // the server fills the addresses for the owner only
  const q = useQuery({ ...instanceQuery, enabled: owner });
  const i = q.data?.instance;
  const [before, after] = t("set.domains.change").split("{cmd}");
  return (
    <section className="flex flex-col gap-3.5 rounded-card-lg border border-line bg-surface p-4">
      <SectionLabel as="h2" icon="globe" tone="sky">
        {t("set.domains.title")}
      </SectionLabel>
      {!owner && <p className="text-[13px] text-muted">{t("set.domains.ownerOnly")}</p>}
      {owner && q.isPending && <Pending compact />}
      {owner && q.isError && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {i && (
        <>
          <Labeled label={t("set.domains.admin")}>
            <CopyRow value={i.adminUrl} />
          </Labeled>
          <Labeled label={t("set.domains.sub")}>
            {i.subscriptionBase ? <CopyRow value={i.subscriptionBase} /> : <p className="text-xs text-muted">{t("set.domains.subNone")}</p>}
          </Labeled>
          <div className="flex flex-col gap-1 border-t border-line pt-3.5 text-xs leading-normal text-pretty text-muted">
            <p>{t("set.domains.secret")}</p>
            <p>
              {before}
              <code className="font-mono font-semibold text-fg">mistgate setup</code>
              {after}
            </p>
          </div>
        </>
      )}
    </section>
  );
}
