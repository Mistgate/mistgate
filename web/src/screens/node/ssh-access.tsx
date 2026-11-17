import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { authorizedKeysFile } from "@/components/panel-ssh-key";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Callout } from "@/components/ui/notice";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { errorText } from "@/lib/errors";
import { provisioning } from "@/lib/api";
import { useEdition } from "@/lib/edition";
import { SSHAttach } from "./ssh-attach";
import { Fingerprint } from "@/components/ssh-login";
import { addPanelKey, keyError, SSHKeys, useServerKeys } from "./ssh-keys";
import { SSHPassword } from "./ssh-password";
import { Facts, SvCard, SvHead } from "./server-parts";

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
    <>
      <SvCard aria-label={t("np.sv.ssh.title")}>
        <SvHead
          title={t("np.sv.ssh.title")}
          hint={
            access.isError ? (
              <span role="alert" className="text-danger-text">
                {t("node.sshAccess.loadFailed")}
              </span>
            ) : (
              t("np.sv.ssh.hint")
            )
          }
        />
        {attachedWithout ? (
          <Callout
            className="mx-6 mb-4 max-[720px]:mx-4"
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
        ) : (
          offerKey && (
            // a password-only node: the same key every node can take, offered calmly, not as a warning
            <div className="mx-6 mb-4 flex flex-col gap-3 rounded-xl border border-line bg-surface-2 p-3 max-[720px]:mx-4">
              <p className="text-[13px] leading-[18px] text-pretty text-fg">{t("node.sshAccess.keyPrompt")}</p>
              <Button variant="secondary" size="md" data-install-key disabled={installing} className="self-start max-sm:w-full" onClick={() => void installKey()}>
                <Icon name="key" size={14} />
                {installing ? t("node.sshAccess.installingKey") : t("node.sshAccess.installKey")}
              </Button>
            </div>
          )
        )}
        {problem && (
          <Callout className="mx-6 mb-4 max-[720px]:mx-4" kind="bad" alert>
            {problem}
          </Callout>
        )}
        {access.data && (
          <>
            <Facts
              className="sv-facts"
              items={[
                { l: t("node.sshAccess.host"), v: <span dir="ltr">{access.data.host}:{access.data.port}</span>, mono: true },
                { l: t("node.sshAccess.username"), v: <span dir="ltr">{access.data.username}</span>, mono: true },
                {
                  l: t("np.sv.ssh.login"),
                  v: access.data.panelKeyFingerprint ? t("np.sv.ssh.byKey") : t("node.sshAccess.passwordOnly"),
                  tone: access.data.panelKeyFingerprint && keyGone ? "warn" : undefined,
                },
                ...(access.data.fingerprint ? [{ l: t("node.sshAccess.serverKey"), v: <Fingerprint value={access.data.fingerprint} />, mono: true }] : []),
              ]}
            />
            <SSHPassword nodeId={nodeId} access={access.data} />
            {access.data.nodeRetired && (
              <div className="sv-body line">
                <p className="text-[13px] leading-[18px] text-pretty text-muted">{t("node.sshAccess.retired")}</p>
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
      </SvCard>
      {access.data && live && <SSHKeys nodeId={nodeId} access={access.data} />}
    </>
  );
}
