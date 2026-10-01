import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Pending, QueryError } from "@/components/ui/query-error";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { auth } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { plain } from "@/lib/plain";
import { describeUserAgent } from "@/lib/ua";

const sessionsQuery = queryOptions({
  queryKey: ["sessions"],
  queryFn: async ({ signal }) => plain(await auth.listSessions({}, { signal })),
  refetchInterval: 30_000,
});

/** Settings -> Sessions: where this admin is signed in. The current one is marked; the others can be ended. */
export function SessionsPage() {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const q = useQuery(sessionsQuery);
  const guard = useStepUp();
  const sessions = q.data?.sessions ?? [];
  const others = sessions.filter((s) => !s.current).length;

  const end = useMutation({
    mutationFn: (id: string) => guard(() => auth.endSession({ id })),
    onSuccess: () => {
      toast(t("sess.ended"));
      void qc.invalidateQueries({ queryKey: ["sessions"] });
    },
    onError: (e) => isStepUpCancelled(e) || toast.error(errorText(e, t)),
  });
  const [confirming, setConfirming] = useState(false);
  const endOthers = useMutation({
    mutationFn: () => guard(() => auth.endOtherSessions({})),
    onSuccess: () => {
      setConfirming(false);
      toast(t("sess.endedAll"));
      void qc.invalidateQueries({ queryKey: ["sessions"] });
    },
    onError: (e) => isStepUpCancelled(e) || toast.error(errorText(e, t)),
  });

  return (
    <section className="rounded-card-lg border border-line bg-surface px-4 py-1.5">
      <div className="flex items-center gap-2.5 py-2">
        <SectionLabel as="h2" className="flex-1" icon="phone" tone="mint">
          {t("sess.title")}
        </SectionLabel>
        {others > 0 && (
          <Button variant="ghostDanger" size="sm" disabled={endOthers.isPending} onClick={() => setConfirming(true)}>
            {t("sess.endOthers")}
          </Button>
        )}
      </div>
      {confirming && (
        <Modal
          open
          onOpenChange={(o) => !o && !endOthers.isPending && setConfirming(false)}
          title={t.n("sess.endOthersTitle", others)}
          description={t("sess.endOthersBody")}
          footer={
            <>
              <Button variant="ghost" size="md" disabled={endOthers.isPending} onClick={() => setConfirming(false)}>
                {t("common.cancel")}
              </Button>
              <Button variant="danger" size="md" disabled={endOthers.isPending} onClick={() => endOthers.mutate()}>
                {t("sess.endOthers")}
              </Button>
            </>
          }
        />
      )}
      {q.isPending && <Pending compact className="border-t border-line py-3.5" />}
      {q.isError && <QueryError compact className="border-t border-line py-3" error={q.error} onRetry={() => void q.refetch()} />}
      {sessions.map((s) => {
        const { browser, system } = describeUserAgent(s.userAgent);
        const name = [browser, system].filter(Boolean).join(" · ") || t("sess.unknown");
        return (
          <div key={s.id} className="flex min-h-[58px] flex-wrap items-center gap-3 border-t border-line py-2">
            <div className="flex min-w-[160px] flex-1 flex-col gap-0.5">
              <span className="flex items-center gap-2 text-[13px] font-bold">
                {name}
                {s.current && (
                  <span className="flex h-[18px] items-center rounded-[5px] bg-accent-soft px-1.5 text-[10px] font-bold text-accent-text">{t("sess.thisOne")}</span>
                )}
              </span>
              <span className="text-[11px] text-muted">
                {t("sess.meta", { started: fmt.dateTime(s.createdAtUnix), seen: fmt.ago(s.lastSeenAtUnix) })}
              </span>
            </div>
            <span className="font-mono text-xs text-muted">{s.ip}</span>
            {!s.current && (
              <Button variant="ghostDanger" size="sm" className="ml-auto" disabled={end.isPending} onClick={() => end.mutate(s.id)}>
                {t("sess.end")}
              </Button>
            )}
          </div>
        );
      })}
    </section>
  );
}
