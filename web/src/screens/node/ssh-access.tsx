import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { SectionLabel } from "@/components/ui/bits";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { errorText } from "@/lib/errors";
import { provisioning } from "@/lib/api";

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

function SSHAccessCardContent({ nodeId }: { nodeId: string }) {
  const t = useT();
  const toast = useToast();
  const guard = useStepUp();
  const qc = useQueryClient();
  const [password, setPassword] = useState("");
  // set only when an interrupted change could not be checked: then both are shown, labelled unverified
  const [pendingPassword, setPendingPassword] = useState("");
  const [passwordNodeId, setPasswordNodeId] = useState("");
  const [visible, setVisible] = useState(false);
  const [busy, setBusy] = useState(false);
  const [confirmForget, setConfirmForget] = useState(false);
  const cardRef = useRef<HTMLElement>(null);
  const expiryTimer = useRef<number | undefined>(undefined);
  const revealEpoch = useRef(0);
  const showingPassword = visible && passwordNodeId === nodeId;
  const access = useServerAccess(nodeId);

  const clearSecret = useCallback(() => {
    revealEpoch.current += 1;
    if (expiryTimer.current !== undefined) {
      window.clearTimeout(expiryTimer.current);
      expiryTimer.current = undefined;
    }
    setVisible(false);
    setPassword("");
    setPendingPassword("");
    setPasswordNodeId("");
    setBusy(false);
  }, []);

  useEffect(() => {
    const accessLoaded = access.data !== undefined || access.isError;
    if (!accessLoaded) return;
    const panel = cardRef.current?.closest<HTMLElement>('[role="tabpanel"]');
    const clearIfInactive = () => {
      if (document.visibilityState === "hidden" || panel?.hidden || panel?.getAttribute("aria-hidden") === "true") {
        clearSecret();
      }
    };
    const observer = panel ? new MutationObserver(clearIfInactive) : undefined;
    if (panel) observer?.observe(panel, { attributes: true, attributeFilter: ["hidden", "aria-hidden"] });
    document.addEventListener("visibilitychange", clearIfInactive);
    window.addEventListener("pagehide", clearSecret);
    return () => {
      revealEpoch.current += 1;
      observer?.disconnect();
      document.removeEventListener("visibilitychange", clearIfInactive);
      window.removeEventListener("pagehide", clearSecret);
      if (expiryTimer.current !== undefined) window.clearTimeout(expiryTimer.current);
      expiryTimer.current = undefined;
    };
  }, [access.data, access.isError, clearSecret]);

  async function reveal() {
    const requestedNodeId = nodeId;
    const epoch = ++revealEpoch.current;
    setBusy(true);
    try {
      const response = await guard(() => provisioning.revealNodeServerPassword({ nodeId: requestedNodeId }));
      const panel = cardRef.current?.closest<HTMLElement>('[role="tabpanel"]');
      if (
        epoch !== revealEpoch.current ||
        document.visibilityState === "hidden" ||
        panel?.hidden ||
        panel?.getAttribute("aria-hidden") === "true"
      ) {
        return;
      }
      setPassword(response.password);
      setPendingPassword(response.unverified ? response.pendingPassword : "");
      setPasswordNodeId(requestedNodeId);
      setVisible(true);
      if (expiryTimer.current !== undefined) window.clearTimeout(expiryTimer.current);
      expiryTimer.current = window.setTimeout(() => {
        expiryTimer.current = undefined;
        if (epoch === revealEpoch.current) clearSecret();
      }, 60_000);
    } catch (error) {
      if (epoch === revealEpoch.current && !isStepUpCancelled(error)) toast.error(errorText(error, t));
    } finally {
      if (epoch === revealEpoch.current) setBusy(false);
    }
  }

  // Retiring keeps the access (the password may be one only the panel knows); forgetting it is the owner's explicit step.
  async function forget() {
    setBusy(true);
    try {
      await guard(() => provisioning.forgetNodeServerAccess({ nodeId }));
      clearSecret();
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
    <section ref={cardRef} className="rounded-card-lg border border-line bg-panel p-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionLabel as="h2" icon="server" tone="sky">
          {t("node.sshAccess.title")}
        </SectionLabel>
        {access.data && (
          <Button variant="secondary" size="md" disabled={busy} onClick={() => (showingPassword ? clearSecret() : void reveal())}>
            {busy ? t("common.loading") : showingPassword ? t("node.sshAccess.hide") : t("node.sshAccess.show")}
          </Button>
        )}
      </div>
      {access.isError ? (
        <p role="alert" className="mt-3 text-[13px] text-danger">
          {t("node.sshAccess.loadFailed")}
        </p>
      ) : access.data ? (
        <>
          <p className="mt-2 text-[13px] leading-snug text-muted">{t("node.sshAccess.body")}</p>
          {access.data.passwordGenerated && <p className="mt-2 text-[13px] leading-snug text-muted">{t("node.sshAccess.generated")}</p>}
          <dl className="mt-3 grid gap-2 text-[13px] sm:grid-cols-2">
            <div>
              <dt className="text-xs text-muted">{t("node.sshAccess.host")}</dt>
              <dd className="mt-0.5 break-all font-mono">{access.data.host}:{access.data.port}</dd>
            </div>
            <div>
              <dt className="text-xs text-muted">{t("node.sshAccess.username")}</dt>
              <dd className="mt-0.5 break-all font-mono">{access.data.username}</dd>
            </div>
          </dl>
          {showingPassword && (
            <div className="mt-3 flex flex-col gap-1.5">
              {pendingPassword && <p className="text-[13px] leading-snug text-danger">{t("node.sshAccess.unverified")}</p>}
              {[
                { id: `ssh-password-${nodeId}`, value: password, label: pendingPassword ? t("node.sshAccess.unverifiedCurrent") : t("node.sshAccess.password") },
                ...(pendingPassword ? [{ id: `ssh-pending-password-${nodeId}`, value: pendingPassword, label: t("node.sshAccess.unverifiedPending") }] : []),
              ].map((field) => (
                <div key={field.id} className="flex flex-col gap-1.5">
                  <label htmlFor={field.id} className="text-xs font-semibold text-muted">
                    {field.label}
                  </label>
                  <input
                    id={field.id}
                    type="text"
                    value={field.value}
                    readOnly
                    autoComplete="off"
                    spellCheck={false}
                    className="min-h-11 rounded-xl border border-line bg-inset px-3 font-mono text-sm text-main outline-none focus-visible:ring-2 focus-visible:ring-accent"
                  />
                </div>
              ))}
              <p className="text-xs leading-snug text-muted">{t("node.sshAccess.revealHint")}</p>
            </div>
          )}
          {access.data.nodeRetired && (
            <div className="mt-3 flex flex-col gap-2 border-t border-line pt-3">
              <p className="text-[13px] leading-snug text-muted">{t("node.sshAccess.retired")}</p>
              {confirmForget && <p className="text-[13px] leading-snug text-danger">{t("node.sshAccess.forgetConfirm")}</p>}
              <div className="flex flex-wrap gap-2">
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
      ) : null}
    </section>
  );
}
