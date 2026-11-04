import { ConnectError } from "@connectrpc/connect";
import { useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { PanelSSHKey, usePanelKeyInUse } from "@/components/panel-ssh-key";
import { Fingerprint, InstallPanelKeyBox, LoginSwitch, OwnKeyField, useSSHLogin } from "@/components/ssh-login";
import { loginError, sshError } from "@/components/ssh-node-install";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { StatusPill } from "@/components/ui/status";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { provisioning } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";

const usernamePattern = /^[A-Za-z_][A-Za-z0-9_.-]{0,31}$/;
const footer = "flex flex-col-reverse gap-2 border-t border-line pt-4 sm:flex-row sm:justify-between";
const footBtn = "max-sm:h-auto max-sm:min-h-9 max-sm:w-full max-sm:whitespace-normal max-sm:py-2";

/**
 * The SSH access card of a live node the panel keeps no access to (installed by command): what that means, and Connect
 * SSH access. `onAttached` gets the panel key's refusal when the access was saved without it.
 */
export function SSHAttach({ nodeId, name, address, onAttached }: { nodeId: string; name: string; address: string; onAttached: (panelKeyError: string) => void }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <section className="flex flex-col gap-4 rounded-card-lg border border-line bg-surface p-4">
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
          <SectionLabel as="h2" icon="server" tone="sky" className="me-auto">
            {t("node.sshAccess.title")}
          </SectionLabel>
          <StatusPill sm kind="off" label={t("node.sshAccess.none")} />
        </div>
        <p className="text-[13px] leading-snug font-semibold text-pretty text-fg">{t("node.sshAccess.noAccess")}</p>
        <p className="text-[13px] leading-snug text-pretty text-muted">{t("node.sshAccess.noAccessBody")}</p>
      </div>
      <div className="grid gap-2 md:flex">
        <Button variant="secondary" size="md" data-attach onClick={() => setOpen(true)}>
          <Icon name="plug" size={14} />
          {t("node.sshAccess.connect")}
        </Button>
      </div>
      {/* mounted only while open: closing drops whatever secret it held */}
      {open && <AttachDialog nodeId={nodeId} name={name} address={address} onClose={() => setOpen(false)} onAttached={onAttached} />}
    </section>
  );
}

/**
 * Connect SSH access, the install wizard's first two steps for a node that exists: the address and port (the node's
 * address filled in), then the server's host key to confirm, the login and the way in, the same three as the wizard's.
 * The panel logs in once, checks the server holds this node's identity, and saves the access (AttachNodeServerAccess).
 */
function AttachDialog({ nodeId, name, address, onClose, onAttached }: { nodeId: string; name: string; address: string; onClose: () => void; onAttached: (panelKeyError: string) => void }) {
  const t = useT();
  const toast = useToast();
  const guard = useStepUp();
  const qc = useQueryClient();
  const { edge } = useEdition();
  const keyInUse = usePanelKeyInUse();
  const login = useSSHLogin(keyInUse ? "key" : "password");
  const [step, setStep] = useState<"server" | "login">("server");
  const [host, setHost] = useState(address);
  const [port, setPort] = useState("22");
  const [fingerprint, setFingerprint] = useState("");
  const [algorithm, setAlgorithm] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [username, setUsername] = useState("root");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState("");
  const [howTo, setHowTo] = useState<boolean | null>(null);
  const portNumber = Number(port);
  const portValid = Number.isInteger(portNumber) && portNumber >= 1 && portNumber <= 65535;
  const userValid = usernamePattern.test(username.trim());

  async function readKey(e: FormEvent) {
    e.preventDefault();
    if (!host.trim() || !portValid) return;
    setBusy(true);
    setFailure("");
    try {
      const response = await provisioning.getSSHFingerprint({ host: host.trim(), port: portNumber });
      setFingerprint(response.fingerprint);
      setAlgorithm(response.algorithm);
      setConfirmed(false);
      setStep("login");
    } catch (error) {
      setFailure(sshError(error, t, edge));
    } finally {
      setBusy(false);
    }
  }

  async function connect(e: FormEvent) {
    e.preventDefault();
    if (!confirmed || !userValid || !login.ready) return;
    setBusy(true);
    setFailure("");
    try {
      const response = await guard(() =>
        provisioning.attachNodeServerAccess({
          nodeId,
          sshHost: host.trim(),
          sshPort: portNumber,
          fingerprint,
          username: username.trim(),
          ...login.request(),
          ...login.install(),
          confirm: true,
        }),
      );
      login.clearSecrets();
      toast(t("node.sshAccess.connected"));
      onAttached(response.panelKeyError);
      onClose();
      await qc.invalidateQueries({ queryKey: ["node-server-access"] });
    } catch (error) {
      if (isStepUpCancelled(error)) return;
      const code = ConnectError.from(error).rawMessage;
      if (code === "ssh_key_passphrase_required") return login.askPassphrase();
      if (code === "ssh_key_passphrase_invalid") login.setPassphrase("");
      setFailure(code === "node_identity_mismatch" ? t("node.sshAccess.error.identity") : loginError(error, login.method, username.trim(), t, edge));
      if (login.method === "key" && (code === "ssh_panel_key_not_accepted" || code === "ssh_authentication_failed")) setHowTo(true);
    } finally {
      login.setPassword(""); // a password is typed for one try, as in the wizard
      setBusy(false);
    }
  }

  return (
    <Modal open onOpenChange={(o) => !o && !busy && onClose()} title={t("node.sshAccess.connect")} description={t("node.sshAccess.connectBody", { name })} closeLabel={t("common.close")} wide>
      <div className="flex flex-col gap-4">
        {failure && <Notice tone="danger">{failure}</Notice>}
        {step === "server" ? (
          <form onSubmit={(e) => void readKey(e)} className="flex flex-col gap-4">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-[minmax(0,1fr)_132px]">
              <TextField
                label={t("node.ssh.host")}
                value={host}
                onChange={(e) => setHost(e.target.value)}
                placeholder="de1.example.com"
                mono
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
                hint={t("node.sshAccess.addressHint")}
                required
                autoFocus
              />
              <TextField
                label={t("node.ssh.port")}
                value={port}
                onChange={(e) => setPort(e.target.value)}
                type="number"
                min={1}
                max={65535}
                inputMode="numeric"
                hint={t("node.ssh.portHint")}
                required
              />
            </div>
            <div className={footer}>
              <Button variant="ghost" size="md" className={footBtn} onClick={onClose}>
                {t("common.cancel")}
              </Button>
              <Button type="submit" variant="primary" size="md" className={footBtn} disabled={busy || !host.trim() || !portValid}>
                {busy ? t("node.ssh.checkingFingerprint") : t("node.ssh.checkServer")}
              </Button>
            </div>
          </form>
        ) : (
          <form onSubmit={(e) => void connect(e)} className="flex flex-col gap-4">
            <div className="flex flex-col gap-1">
              <h3 className="text-base font-extrabold tracking-[-0.02em]">{t("node.ssh.fingerprintTitle")}</h3>
              <p className="text-sm leading-relaxed text-pretty text-muted">{t("node.ssh.fingerprintBody")}</p>
            </div>
            <div className="flex flex-col gap-1.5">
              <code className="block rounded-field border border-accent-line bg-accent-soft p-3 leading-relaxed text-accent-text select-all">
                <Fingerprint value={fingerprint} />
              </code>
              {algorithm && <span className="text-xs text-muted">{t("node.ssh.keyType", { algorithm })}</span>}
            </div>
            <label className="flex cursor-pointer items-start gap-2.5 rounded-field border border-line bg-surface-2 p-3 text-sm leading-relaxed text-fg">
              <input type="checkbox" checked={confirmed} onChange={(e) => setConfirmed(e.target.checked)} className="mt-0.5 size-4 flex-none accent-accent" />
              <span>{t("node.ssh.confirmKey")}</span>
            </label>
            <div role="group" aria-labelledby="ssh-attach-method" className="flex flex-col gap-3 border-t border-line pt-4">
              <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
                <span id="ssh-attach-method" className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">
                  {t("node.ssh.method")}
                </span>
                <LoginSwitch login={login} onPick={() => setFailure("")} />
              </div>
              <div className={cx("grid grid-cols-1 gap-3", login.method === "password" && "sm:grid-cols-2")}>
                <TextField
                  label={t("node.ssh.login")}
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  placeholder="root"
                  autoComplete="off"
                  maxLength={32}
                  hint={t("node.ssh.loginHint")}
                  error={username && !userValid ? t("node.ssh.loginError") : undefined}
                  required
                />
                {login.method === "password" && (
                  <TextField
                    label={t("node.ssh.password")}
                    value={login.password}
                    onChange={(e) => login.setPassword(e.target.value)}
                    type="password"
                    autoComplete="new-password"
                    maxLength={1024}
                    required
                  />
                )}
              </div>
              {login.method === "key" && <PanelSSHKey howTo={howTo ?? !keyInUse} onHowTo={setHowTo} className="rounded-card border border-line bg-surface-2 p-4" />}
              {login.method === "password" && <InstallPanelKeyBox login={login} />}
              {login.method === "own" && <OwnKeyField login={login} />}
            </div>
            <div className={footer}>
              <Button
                variant="ghost"
                size="md"
                className={footBtn}
                disabled={busy}
                onClick={() => {
                  setFailure("");
                  setStep("server");
                }}
              >
                {t("node.ssh.back")}
              </Button>
              <Button type="submit" variant="primary" size="md" className={footBtn} disabled={busy || !confirmed || !userValid || !login.ready}>
                {busy ? t("node.sshAccess.connecting") : t("node.sshAccess.connectNow")}
              </Button>
            </div>
          </form>
        )}
      </div>
    </Modal>
  );
}
