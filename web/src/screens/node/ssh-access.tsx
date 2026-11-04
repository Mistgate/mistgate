import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { SectionLabel } from "@/components/ui/bits";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { errorText } from "@/lib/errors";
import { provisioning } from "@/lib/api";
import { SSHPassword } from "./ssh-password";

/** The node's saved SSH access (public metadata only), or null when the panel keeps none. */
export function useServerAccess(nodeId: string) {
  return useQuery({
    queryKey: ["node-server-access", nodeId],
    queryFn: async ({ signal }) => {
      const response = await provisioning.listNodeServerAccess({}, { signal });
      return response.access.find((item) => item.nodeId === nodeId) ?? null;
    },
    staleTime: 60_000,
  });
}

export function SSHAccessCard({ nodeId }: { nodeId: string }) {
  return <SSHAccessCardContent key={nodeId} nodeId={nodeId} />;
}

/**
 * Server tab, SSH access: where the panel logs in (address, login, the pinned server key), then a block per way in, each
 * under a hairline: the password (ssh-password.tsx). A retired node adds Forget at the end.
 */
function SSHAccessCardContent({ nodeId }: { nodeId: string }) {
  const t = useT();
  const toast = useToast();
  const guard = useStepUp();
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [confirmForget, setConfirmForget] = useState(false);
  const access = useServerAccess(nodeId);

  // Retiring keeps the access (the password may be one only the panel knows); forgetting it is the owner's explicit step.
  async function forget() {
    setBusy(true);
    try {
      await guard(() => provisioning.forgetNodeServerAccess({ nodeId }));
      toast(t("node.sshAccess.forgotten"));
      await qc.invalidateQueries({ queryKey: ["node-server-access", nodeId] });
    } catch (error) {
      if (!isStepUpCancelled(error)) toast.error(errorText(error, t));
    } finally {
      setBusy(false);
      setConfirmForget(false);
    }
  }

  if (!access.data && !access.isError) return null;

  return (
    <section className="flex flex-col gap-4 rounded-card-lg border border-line bg-surface p-4">
      <div className="flex flex-col gap-2">
        <SectionLabel as="h2" icon="server" tone="sky">
          {t("node.sshAccess.title")}
        </SectionLabel>
        {access.isError ? (
          <p role="alert" className="text-[13px] leading-snug text-danger-text">
            {t("node.sshAccess.loadFailed")}
          </p>
        ) : (
          <p className="text-[13px] leading-snug text-pretty text-muted">{t("node.sshAccess.body")}</p>
        )}
      </div>
      {access.data && (
        <>
          <dl className="grid grid-cols-2 gap-x-4 gap-y-3 text-[13px]">
            <div className="min-w-0">
              <dt className="text-xs text-muted">{t("node.sshAccess.host")}</dt>
              <dd className="mt-1 break-all">
                <span dir="ltr" className="font-mono">
                  {access.data.host}:{access.data.port}
                </span>
              </dd>
            </div>
            <div className="min-w-0">
              <dt className="text-xs text-muted">{t("node.sshAccess.username")}</dt>
              <dd className="mt-1 break-all">
                <span dir="ltr" className="font-mono">
                  {access.data.username}
                </span>
              </dd>
            </div>
            {access.data.fingerprint && (
              <div className="col-span-2 min-w-0">
                <dt className="text-xs text-muted">{t("node.sshAccess.serverKey")}</dt>
                <dd className="mt-1 text-xs leading-snug break-all text-muted">
                  <span dir="ltr" className="font-mono">
                    {access.data.fingerprint}
                  </span>
                </dd>
              </div>
            )}
          </dl>
          <SSHPassword nodeId={nodeId} access={access.data} />
          {access.data.nodeRetired && (
            <div className="flex flex-col gap-3 border-t border-line pt-4">
              <p className="text-[13px] leading-snug text-pretty text-muted">{t("node.sshAccess.retired")}</p>
              {confirmForget && <p className="text-[13px] leading-snug text-pretty text-danger-text">{t("node.sshAccess.forgetConfirm")}</p>}
              <div className="grid gap-2 md:flex md:flex-wrap">
                <Button variant="danger" size="md" disabled={busy} onClick={() => (confirmForget ? void forget() : setConfirmForget(true))}>
                  {confirmForget ? t("node.sshAccess.forgetNow") : t("node.sshAccess.forget")}
                </Button>
                {confirmForget && (
                  <Button variant="ghost" size="md" disabled={busy} onClick={() => setConfirmForget(false)}>
                    {t("common.cancel")}
                  </Button>
                )}
              </div>
            </div>
          )}
        </>
      )}
    </section>
  );
}
