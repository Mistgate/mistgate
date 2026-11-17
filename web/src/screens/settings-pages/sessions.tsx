import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { auth } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { plain } from "@/lib/plain";
import { describeUserAgent } from "@/lib/ua";
import { ICONS, Otg, Sec, SxGate } from "./sx-bits";
import { SxWindow } from "./sx-dlg";

export const sessionsQuery = queryOptions({
  queryKey: ["sessions"],
  queryFn: async ({ signal }) => plain(await auth.listSessions({}, { signal })),
  refetchInterval: 30_000,
});

/** What a scene says about the page: the window that ends the other sessions is open. */
export type SessionsDemo = { end?: boolean };

/**
 * Settings -> Sessions (the board's sxIs_ses): where this admin is signed in. The current one is marked; the others can be ended, one by
 * one or all at once (a window asks first for all of them). The list is the page's own call: the strip stays while it loads or fails.
 */
export function SessionsPage({ demo }: { demo?: SessionsDemo }) {
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
  const [confirming, setConfirming] = useState(!!demo?.end);
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
    <SxGate pending={q.isPending} error={q.isError} onRetry={() => void q.refetch()}>
      <div className="sx-one">
        <Sec
          tone="sky"
          d={ICONS.device}
          title={t("sess.title")}
          foot={others === 0 ? t("sx.ses.alone") : undefined}
          aside={
            others > 0 ? (
              <span className="ai-hb">
                <Button size="md" disabled={endOthers.isPending} onClick={() => setConfirming(true)}>
                  {t("sess.endOthers")}
                </Button>
              </span>
            ) : undefined
          }
        >
          <div className="sx-list">
            {sessions.map((s) => {
              const { browser, system } = describeUserAgent(s.userAgent);
              const name = [browser, system].filter(Boolean).join(" · ") || t("sess.unknown");
              return (
                <div key={s.id} className="sx-it sx-se">
                  <span className="ai-mono" aria-hidden>
                    {name.slice(0, 1)}
                  </span>
                  <div>
                    <b>
                      {name}
                      {s.current && <Otg className="now">{t("sess.thisOne")}</Otg>}
                    </b>
                    <em>{t("sess.meta", { started: fmt.dateTime(s.createdAtUnix), seen: fmt.ago(s.lastSeenAtUnix) })}</em>
                  </div>
                  <code className="mono">{s.ip}</code>
                  {!s.current && (
                    <Button size="md" disabled={end.isPending} onClick={() => end.mutate(s.id)}>
                      {t("sess.end")}
                    </Button>
                  )}
                </div>
              );
            })}
          </div>
        </Sec>
      </div>
      {confirming && (
        <SxWindow
          open
          onOpenChange={(o) => !o && !endOthers.isPending && setConfirming(false)}
          title={t.n("sess.endOthersTitle", others)}
          shot="sessions-end-others"
          footer={
            <>
              <Button size="lg" disabled={endOthers.isPending} onClick={() => setConfirming(false)}>
                {t("common.cancel")}
              </Button>
              <Button variant="primary" size="lg" className="sx-dng" disabled={endOthers.isPending} onClick={() => endOthers.mutate()}>
                {t("sess.endOthers")}
              </Button>
            </>
          }
        >
          <p className="sx-dp">{t("sess.endOthersBody")}</p>
        </SxWindow>
      )}
    </SxGate>
  );
}
