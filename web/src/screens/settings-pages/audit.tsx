import { Code, ConnectError } from "@connectrpc/connect";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { FilterChips } from "@/components/ui/chips";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { CursorPagination } from "@/components/ui/pagination";
import { Segmented } from "@/components/ui/segmented";
import { AuditKind, AuditSource } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { auth } from "@/lib/api";
import { actorLabel, describeAudit, isFailure } from "@/lib/audit";
import { cx } from "@/lib/cx";
import { useCursorPaging } from "@/lib/paging";
import { plain } from "@/lib/plain";
import { useFmt } from "@/lib/format";
import { G } from "@/screens/users/user-bits";
import { ICONS, SxGate } from "./sx-bits";
import "./settings-b.css";

const pageSizes = [25, 50, 100] as const;

export type Source = "all" | "panel" | "bot" | "mcp" | "api" | "assistant";
const sources: Record<Source, AuditSource> = {
  all: AuditSource.UNSPECIFIED,
  panel: AuditSource.PANEL,
  bot: AuditSource.BOT,
  mcp: AuditSource.MCP,
  api: AuditSource.API,
  assistant: AuditSource.ASSISTANT,
};
// the board's chips say the source by its mark, as the badge of a row does (only "all" is a word)
const sourceMarks: Record<Exclude<Source, "all">, string> = { panel: "WEB", bot: "BOT", mcp: "MCP", api: "API", assistant: "AI" };

export type Kind = "all" | "signin" | "changes" | "failures";
const kinds: Record<Kind, AuditKind> = {
  all: AuditKind.UNSPECIFIED,
  signin: AuditKind.SIGN_IN,
  changes: AuditKind.CHANGES,
  failures: AuditKind.FAILURES,
};
const kindLabels: Record<Kind, MessageKey> = { all: "audit.kind.all", signin: "audit.kind.signin", changes: "audit.kind.changes", failures: "audit.kind.failures" };

// The mark of a row's source, and its colour: WEB grey, BOT sky, MCP and AI lavender, API sand.
const badge: Record<AuditSource, { label: string; cls: string }> = {
  [AuditSource.UNSPECIFIED]: { label: "—", cls: "" },
  [AuditSource.PANEL]: { label: "WEB", cls: "web" },
  [AuditSource.BOT]: { label: "BOT", cls: "bot" },
  [AuditSource.MCP]: { label: "MCP", cls: "mcp" },
  [AuditSource.ASSISTANT]: { label: "AI", cls: "ai" },
  [AuditSource.API]: { label: "API", cls: "api" },
};

/** What a scene says about the page: the filters it starts with. */
export type AuditDemo = { kind?: Kind; source?: Source };

/**
 * Settings -> Audit (the board's sxIs_aud, owner only): what was done, by whom and from where. What kind (sign-ins, changes, failures) and
 * which source, newest first, a page of the log at a time by cursor. A failed row is red with a cross, in words, never the server's English.
 */
export function AuditPage({ demo }: { demo?: AuditDemo }) {
  const t = useT();
  const fmt = useFmt();
  const [kind, setKind] = useState<Kind>(demo?.kind ?? "all");
  const [source, setSource] = useState<Source>(demo?.source ?? "all");
  // a page of the log at a time, newest first; where it starts (`before`) is in the URL
  const paging = useCursorPaging({ sizes: pageSizes, defaultSize: 50 });
  const q = useQuery({
    queryKey: ["audit", source, kind, paging.before, paging.size],
    queryFn: async ({ signal }) => plain(await auth.listAudit({ source: sources[source], kind: kinds[kind], beforeId: BigInt(paging.before), pageSize: paging.size }, { signal })),
    // the old page stays on screen while the next one loads
    placeholderData: keepPreviousData,
    retry: (n, e) => ConnectError.from(e).code !== Code.PermissionDenied && n < 2,
  });
  const entries = q.data?.entries ?? [];
  const older = q.data && q.data.nextBeforeId > 0 ? q.data.nextBeforeId : undefined;
  const denied = q.isError && ConnectError.from(q.error).code === Code.PermissionDenied;
  const filtered = kind !== "all" || source !== "all";
  const clear = () => {
    setKind("all");
    setSource("all");
    paging.reset();
  };

  if (denied)
    return (
      <section className="card us-none hl-calm" role="alert">
        <span className="g" aria-hidden>
          <G d={ICONS.lock} size={20} stroke={1.5} />
        </span>
        <b>{t("audit.ownerOnly")}</b>
      </section>
    );

  return (
    <SxGate pending={q.isPending} error={q.isError} onRetry={() => void q.refetch()}>
      <div className="sx-af">
        <Segmented
          aria-label={t("audit.kindFilter")}
          value={kind}
          onValueChange={(k) => {
            setKind(k);
            paging.reset();
          }}
          options={(Object.keys(kindLabels) as Kind[]).map((k) => ({ value: k, label: t(kindLabels[k]) }))}
        />
        <FilterChips
          aria-label={t("audit.filter")}
          className="sx-as"
          value={source}
          onValueChange={(s) => {
            setSource(s);
            paging.reset();
          }}
          options={(Object.keys(sources) as Source[]).map((s) => ({ value: s, label: s === "all" ? t("audit.all") : sourceMarks[s] }))}
        />
      </div>
      {q.data && entries.length === 0 && !paging.hasNewer && (
        <section className="card us-none hl-calm rc-emp">
          <span className="g" aria-hidden>
            <G d={ICONS.history} size={20} stroke={1.5} />
          </span>
          <b>{t("sx.aud.none")}</b>
          {filtered && (
            <>
              <span>{t("sx.aud.nomatch")}</span>
              <Button size="lg" onClick={clear}>
                {t("sx.aud.reset")}
              </Button>
            </>
          )}
        </section>
      )}
      {(entries.length > 0 || paging.hasNewer) && (
        <section className="card sx-aud-card">
          <div className={cx("sx-aud", q.isPlaceholderData && "wait")} aria-busy={q.isPlaceholderData || undefined}>
            {entries.map((e) => {
              const b = badge[e.source as AuditSource] ?? badge[AuditSource.UNSPECIFIED];
              const failed = isFailure(e.result);
              return (
                <div key={e.id} className={cx("sx-ar", failed && "fail")}>
                  <i className={cx("sx-src mono", b.cls)}>{b.label}</i>
                  <span className="sx-aw">
                    {failed && <Icon name="x" size={12} strokeWidth={3} className="me-1 inline-block align-[-1px]" />}
                    <b>{actorLabel(t, e)}</b> {describeAudit(t, e)}
                  </span>
                  <code className="mono">{e.ip}</code>
                  <em>{fmt.stamp(e.timeUnix)}</em>
                </div>
              );
            })}
          </div>
          <div className="sx-aud-pg">
            <CursorPagination
              flush={entries.length === 0}
              busy={q.isPlaceholderData}
              label={t("settings.audit")}
              size={paging.size}
              sizes={pageSizes}
              onSize={paging.setSize}
              cursor={{ before: paging.before, count: entries.length, hasOlder: older !== undefined, hasNewer: paging.hasNewer, toNewest: paging.newerToFirst, index: paging.index, onOlder: () => older !== undefined && paging.older(older), onNewer: paging.newer }}
            />
          </div>
        </section>
      )}
    </SxGate>
  );
}
