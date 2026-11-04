import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { authorizedKeysFile } from "@/components/panel-ssh-key";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { SectionLabel } from "@/components/ui/bits";
import { Icon, IconChip } from "@/components/ui/icons";
import { Callout } from "@/components/ui/notice";
import { StatusPill } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { errorText } from "@/lib/errors";
import { provisioning } from "@/lib/api";
import { useEdition } from "@/lib/edition";
import { SSHAttach } from "./ssh-attach";
import { Fingerprint } from "@/components/ssh-login";
import { addPanelKey, keyError, SSHKeys, useServerKeys } from "./ssh-keys";
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

/** `attach`: a live node's name and address; with it, a node the panel keeps no access to offers Connect SSH access. */
export function SSHAccessCard({ nodeId, attach }: { nodeId: string; attach?: { name: string; address: string } }) {
  return <SSHAccessCardContent key={nodeId} nodeId={nodeId} attach={attach} />;
}

/**
 * Server tab, SSH access: where the panel logs in (address, login, the pinned server key) and how (the panel key or the
 * password only, in the header, with a calm way to add the key to a password-only node), then a block per way in, each
 * under a hairline: the password (ssh-password.tsx) and, on a live node, the keys (ssh-keys.tsx). A retired node adds
 * Forget at the end. A live node with no saved access shows how to connect it (ssh-attach.tsx).
 */
function SSHAccessCardContent({ nodeId, attach }: { nodeId: string; attach?: { name: string; address: string } }) {
  const t = useT();
  const toast = useToast();
  const guard = useStepUp();
  const qc = useQueryClient();
  const { edge } = useEdition();
  const [busy, setBusy] = useState(false);
  const [confirmForget, setConfirmForget] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [problem, setProblem] = useState("");
  // an attach that saved the password access but could not add the panel key says why, once, under the header
  const [attachedWithout, setAttachedWithout] = useState("");
  const access = useServerAccess(nodeId);
  const live = !!access.data && !access.data.nodeRetired;
  const keys = useServerKeys(nodeId, live);
  const passwordOnly = !!access.data && access.data.passwordSaved && access.data.panelKeyFingerprint === "";
  // the key already in the file but unused has its own Use in the keys block
  const offerKey = live && passwordOnly && !keys.data?.keys.some((k) => k.panelKey);
  const keyGone = !!keys.data && !!access.data?.panelKeyFingerprint && !keys.data.keys.some((k) => k.panelKey);

  async function installKey() {
    setInstalling(true);
    setProblem("");
    try {
      await guard(() => addPanelKey(nodeId));
      toast(t("node.sshAccess.keyInstalled"));
      setAttachedWithout("");
    } catch (error) {
      if (!isStepUpCancelled(error)) setProblem(keyError(error, t, edge));
    } finally {
      setInstalling(false);
      void qc.invalidateQueries({ queryKey: ["node-server-keys", nodeId] });
      void qc.invalidateQueries({ queryKey: ["node-server-access"] });
    }
  }

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

  if (!access.data && !access.isError) {
    return access.isSuccess && attach ? <SSHAttach nodeId={nodeId} name={attach.name} address={attach.address} onAttached={setAttachedWithout} /> : null;
  }

  return (
    <section className="flex flex-col gap-4 rounded-card-lg border border-line bg-surface p-4">
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <SectionLabel as="h2" icon="server" tone="sky" className="me-auto">
            {t("node.sshAccess.title")}
          </SectionLabel>
          {/* how the panel gets in: its key (verified at install or after; amber once the file read shows it gone), or the
              saved password alone */}
          {access.data &&
            (access.data.panelKeyFingerprint ? (
              <StatusPill sm kind={keyGone ? "warn" : "ok"} label={t("panelKey.title")} />
            ) : (
              <StatusPill sm kind="off" label={t("node.sshAccess.passwordOnly")} />
            ))}
        </div>
        {access.isError ? (
          <p role="alert" className="text-[13px] leading-snug text-danger-text">
            {t("node.sshAccess.loadFailed")}
          </p>
        ) : (
          <p className="text-[13px] leading-snug text-pretty text-muted">{t("node.sshAccess.body")}</p>
        )}
      </div>
      {attachedWithout ? (
        <Callout
          kind="warn"
          action={
            offerKey && (
              <Button variant="secondary" size="sm" data-install-key disabled={installing} onClick={() => void installKey()}>
                {installing ? t("node.sshAccess.installingKey") : t("node.sshAccess.installKey")}
              </Button>
            )
          }
        >
          {t("node.sshAccess.keyNotAdded", { reason: keyError(attachedWithout, t, edge) })}
        </Callout>
      ) : offerKey && (
        // a password-only node: the same key every node can take, offered calmly, not as a warning
        <div className="flex flex-col gap-3 rounded-card border border-line bg-surface-2 p-3">
          <div className="flex items-start gap-2.5">
            <IconChip icon="key" tone="sky" />
            <p className="min-w-0 text-[13px] leading-snug text-pretty text-fg">{t("node.sshAccess.keyPrompt")}</p>
          </div>
          <Button variant="secondary" size="md" data-install-key disabled={installing} className="max-sm:w-full sm:ms-8 sm:self-start" onClick={() => void installKey()}>
            <Icon name="key" size={14} />
            {installing ? t("node.sshAccess.installingKey") : t("node.sshAccess.installKey")}
          </Button>
        </div>
      )}
      {problem && (
        <Callout kind="bad" alert>
          {problem}
        </Callout>
      )}
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
                <dd className="mt-1 text-muted">
                  <Fingerprint value={access.data.fingerprint} />
                </dd>
              </div>
            )}
          </dl>
          <SSHPassword nodeId={nodeId} access={access.data} />
          {live && <SSHKeys nodeId={nodeId} access={access.data} />}
          {access.data.nodeRetired && (
            <div className="flex flex-col gap-3 border-t border-line pt-4">
              <p className="text-[13px] leading-snug text-pretty text-muted">{t("node.sshAccess.retired")}</p>
              {confirmForget && (
                <div className="flex flex-col gap-1.5 text-[13px] leading-snug text-pretty text-danger-text">
                  {access.data.passwordSaved && <p>{t("node.sshAccess.forgetConfirm")}</p>}
                  {access.data.panelKeyFingerprint && <p>{t("node.sshAccess.forgetKey", { file: authorizedKeysFile })}</p>}
                </div>
              )}
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
