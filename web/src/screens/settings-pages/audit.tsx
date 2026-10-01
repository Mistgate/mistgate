import { Code, ConnectError } from "@connectrpc/connect";
import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { Icon } from "@/components/ui/icons";
import { EmptyState, Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { AuditKind, AuditSource } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { auth } from "@/lib/api";
import { actorLabel, describeAudit, isFailure } from "@/lib/audit";
import { cx } from "@/lib/cx";
import { plain } from "@/lib/plain";
import { useFmt } from "@/lib/format";

type Source = "all" | "panel" | "bot" | "mcp" | "api";
const sources: Record<Source, AuditSource> = {
  all: AuditSource.UNSPECIFIED,
  panel: AuditSource.PANEL,
  bot: AuditSource.BOT,
  mcp: AuditSource.MCP,
  api: AuditSource.API,
};
const sourceLabels: Record<Source, MessageKey> = { all: "audit.all", panel: "audit.panel", bot: "audit.bot", mcp: "audit.mcp", api: "audit.api" };

type Kind = "all" | "signin" | "changes" | "failures";
const kinds: Record<Kind, AuditKind> = {
  all: AuditKind.UNSPECIFIED,
  signin: AuditKind.SIGN_IN,
  changes: AuditKind.CHANGES,
  failures: AuditKind.FAILURES,
};

// Source badges: WEB grey, BOT sky, MCP accent, API sand.
const badge: Record<AuditSource, { label: string; cls: string }> = {
  [AuditSource.UNSPECIFIED]: { label: "—", cls: "bg-surface-2 text-muted" },
  [AuditSource.PANEL]: { label: "WEB", cls: "bg-surface-2 text-muted" },
  [AuditSource.BOT]: { label: "BOT", cls: "bg-[color-mix(in_oklch,var(--accent-sky)_var(--_soft-pct),var(--bg))] text-[color-mix(in_oklch,var(--accent-sky)_var(--_text-pct),#000)]" },
  [AuditSource.MCP]: { label: "MCP", cls: "bg-accent-soft text-accent-text" },
  [AuditSource.API]: { label: "API", cls: "bg-[color-mix(in_oklch,var(--accent-sand)_var(--_soft-pct),var(--bg))] text-[color-mix(in_oklch,var(--accent-sand)_var(--_text-pct),#000)]" },
};

/**
 * Settings -> Audit (owner only): what was done, by whom and from where. What kind (sign-ins, changes, failures) and
 * which source, newest first, cursor paging. A failed row is red with a cross, in words, never the server's English.
 */
export function AuditPage() {
  const t = useT();
  const fmt = useFmt();
  const [kind, setKind] = useState<Kind>("all");
  const [source, setSource] = useState<Source>("all");
  const q = useInfiniteQuery({
    queryKey: ["audit", source, kind],
    queryFn: async ({ pageParam, signal }) =>
      plain(await auth.listAudit({ source: sources[source], kind: kinds[kind], beforeId: BigInt(pageParam), pageSize: 50 }, { signal })),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.nextBeforeId > 0 ? last.nextBeforeId : undefined),
    retry: (n, e) => ConnectError.from(e).code !== Code.PermissionDenied && n < 2,
  });
  const entries = q.data?.pages.flatMap((p) => p.entries) ?? [];
  const denied = q.isError && ConnectError.from(q.error).code === Code.PermissionDenied;
  const filtered = kind !== "all" || source !== "all";

  const kindLabels: Record<Kind, MessageKey> = { all: "audit.kind.all", signin: "audit.kind.signin", changes: "audit.kind.changes", failures: "audit.kind.failures" };

  return (
    <>
      <div className="flex flex-wrap items-center gap-2.5">
        <FilterChips
          aria-label={t("audit.kindFilter")}
          className="min-w-0 flex-1"
          value={kind}
          onValueChange={setKind}
          options={(Object.keys(kindLabels) as Kind[]).map((k) => ({ value: k, label: t(kindLabels[k]) }))}
        />
        <div className="w-full sm:w-[170px]">
          <Select
            compact
            aria-label={t("audit.filter")}
            value={source}
            onValueChange={(v) => setSource(v as Source)}
            options={(Object.keys(sourceLabels) as Source[]).map((s) => ({ value: s, label: t(sourceLabels[s]) }))}
          />
        </div>
      </div>
      {denied && <Notice>{t("audit.ownerOnly")}</Notice>}
      {q.isError && !denied && <QueryError error={q.error} onRetry={() => void q.refetch()} />}
      {q.isPending && <Pending />}
      {q.data && entries.length === 0 && (
        <div className="rounded-card-lg border border-dashed border-line">
          <EmptyState title={filtered ? t("audit.noneKind") : t("audit.none")} />
        </div>
      )}
      {entries.length > 0 && (
        <div className="rounded-card-lg border border-line bg-surface px-4 py-1">
          {entries.map((e, i) => {
            const b = badge[e.source as AuditSource] ?? badge[AuditSource.UNSPECIFIED];
            const failed = isFailure(e.result);
            return (
              <div
                key={e.id}
                className={cx(
                  "grid min-h-12 items-center gap-x-3 gap-y-1 py-1.5 text-[13px] max-[1099px]:grid-cols-[minmax(0,1fr)_auto] min-[1100px]:grid-cols-[170px_minmax(0,1fr)_110px_112px]",
                  i > 0 && "border-t border-line",
                )}
              >
                <span className="flex min-w-0 items-center gap-2">
                  <span className={cx("flex h-5 flex-none items-center rounded-md px-[7px] font-mono text-[10px] font-bold", b.cls)}>{b.label}</span>
                  <b className="truncate">{actorLabel(t, e)}</b>
                </span>
                <span className={cx("flex min-w-0 items-start gap-1.5 text-pretty max-[1099px]:order-3 max-[1099px]:col-span-2", failed && "text-danger-text")}>
                  {failed && <Icon name="x" size={13} strokeWidth={3} className="mt-[3px] block flex-none" />}
                  <span className="min-w-0">{describeAudit(t, e)}</span>
                </span>
                <span className="truncate font-mono text-[11px] text-muted max-[1099px]:order-2">{e.ip}</span>
                <span className="text-right font-mono text-[11px] text-muted max-[1099px]:order-1">{fmt.stamp(e.timeUnix)}</span>
              </div>
            );
          })}
        </div>
      )}
      {q.hasNextPage && (
        <div className="flex justify-center">
          <Button variant="secondary" size="md" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
            {t("common.showMore")}
          </Button>
        </div>
      )}
    </>
  );
}
