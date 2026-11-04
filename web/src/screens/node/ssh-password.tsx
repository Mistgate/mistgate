import { ConnectError } from "@connectrpc/connect";
import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { authorizedKeysFile } from "@/components/panel-ssh-key";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Callout } from "@/components/ui/notice";
import { Segmented } from "@/components/ui/segmented";
import { StatusGlyph, StatusPill } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import type { NodeServerAccess } from "@/gen/mistgate/admin/v1/provisioning_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { provisioning } from "@/lib/api";
import { errorText } from "@/lib/errors";

// The panel's own floor for a password the owner types (provision/service.go, RotateNodeServerPassword).
const minLength = 12;

// What a refusal of RotateNodeServerPassword or RevealNodeServerPassword means for the owner; anything else reads as errorText.
const refusals: Record<string, MessageKey> = {
  ssh_password_change_unverified: "node.sshAccess.error.unverified",
  ssh_rotation_recovery_required: "node.sshAccess.error.recovery",
  ssh_authentication_failed: "node.sshAccess.error.auth",
  node_retired: "node.sshAccess.error.retired",
  node_server_password_not_saved: "node.sshAccess.error.notSaved",
  "new password must differ from the current password": "node.sshAccess.error.same",
};

/** `viaKey`: the panel logs in with its key first, so a refused login means the key, not the password. */
export function passwordError(error: unknown, t: T, viaKey = false): string {
  const raw = ConnectError.from(error).rawMessage;
  if (viaKey && raw === "ssh_authentication_failed") return t("node.sshAccess.error.authKey", { file: authorizedKeysFile });
  return Object.hasOwn(refusals, raw) ? t(refusals[raw]!) : errorText(error, t);
}

type Mode = "generate" | "own";

/**
 * The password of a node's saved SSH access: its state, Reveal (step-up, audited) and, on a live node, Change: the panel
 * generates a password or takes the owner's, changes it on the server, logs in with it, and the new one is shown through
 * the same reveal. A shown password lives in this component's state only, and goes after 60 s, when the settings tab or
 * the browser tab is left, or when the page is hidden. A node installed by the panel key may keep no password: then
 * there is nothing to reveal and Change is "Set password" (through the key).
 */
export function SSHPassword({ nodeId, access }: { nodeId: string; access: NodeServerAccess }) {
  const t = useT();
  const toast = useToast();
  const guard = useStepUp();
  const qc = useQueryClient();
  const [password, setPassword] = useState("");
  // set only when an interrupted change could not be checked: then both are shown, labelled unverified
  const [pendingPassword, setPendingPassword] = useState("");
  const [passwordNodeId, setPasswordNodeId] = useState("");
  const [visible, setVisible] = useState(false);
  const [fresh, setFresh] = useState(false); // the password the change just set
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(false);
  const [mode, setMode] = useState<Mode>("generate");
  const [own, setOwn] = useState("");
  const [changing, setChanging] = useState(false);
  const [problem, setProblem] = useState("");
  const blockRef = useRef<HTMLDivElement>(null);
  const expiryTimer = useRef<number | undefined>(undefined);
  const revealEpoch = useRef(0);
  const showingPassword = visible && passwordNodeId === nodeId;
  const retired = access.nodeRetired;
  const saved = access.passwordSaved;
  const viaKey = access.panelKeyFingerprint !== "";
  // an interrupted change can be revealed even with no password saved before it: both candidates come back
  const canReveal = saved || access.rotationPending;
  const refresh = () => qc.invalidateQueries({ queryKey: ["node-server-access", nodeId] });

  // gone (another node, another page), hidden, or on a settings tab that is not shown
  const inactive = useCallback(() => {
    if (!blockRef.current) return true;
    const panel = blockRef.current.closest<HTMLElement>('[role="tabpanel"]');
    return document.visibilityState === "hidden" || !!panel?.hidden || panel?.getAttribute("aria-hidden") === "true";
  }, []);

  const clearSecret = useCallback(() => {
    revealEpoch.current += 1;
    if (expiryTimer.current !== undefined) {
      window.clearTimeout(expiryTimer.current);
      expiryTimer.current = undefined;
    }
    setVisible(false);
    setFresh(false);
    setPassword("");
    setPendingPassword("");
    setPasswordNodeId("");
    setBusy(false);
  }, []);

  useEffect(() => {
    const panel = blockRef.current?.closest<HTMLElement>('[role="tabpanel"]');
    const clearIfInactive = () => {
      if (inactive()) clearSecret();
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
  }, [clearSecret, inactive]);

  async function reveal(justChanged = false) {
    const requestedNodeId = nodeId;
    const epoch = ++revealEpoch.current;
    setBusy(true);
    try {
      const response = await guard(() => provisioning.revealNodeServerPassword({ nodeId: requestedNodeId }));
      // a reveal settles an interrupted change when the server answers: the state pill follows
      if (access.rotationPending) void refresh();
      if (epoch !== revealEpoch.current || inactive()) return;
      setPassword(response.password);
      setPendingPassword(response.unverified ? response.pendingPassword : "");
      setPasswordNodeId(requestedNodeId);
      setFresh(justChanged && !response.unverified);
      setVisible(true);
      if (expiryTimer.current !== undefined) window.clearTimeout(expiryTimer.current);
      expiryTimer.current = window.setTimeout(() => {
        expiryTimer.current = undefined;
        if (epoch === revealEpoch.current) clearSecret();
      }, 60_000);
    } catch (error) {
      if (epoch === revealEpoch.current && !isStepUpCancelled(error)) toast.error(passwordError(error, t, viaKey));
    } finally {
      if (epoch === revealEpoch.current) setBusy(false);
    }
  }

  // No timeout of our own: the panel logs in, runs chpasswd and logs in again, which can take ~40 s.
  async function change(e: FormEvent) {
    e.preventDefault();
    const generate = mode === "generate";
    const newPassword = generate ? "" : own;
    if (!generate && newPassword.length < minLength) return;
    const first = !saved;
    setOwn(""); // the typed password never outlives the request
    setProblem("");
    clearSecret(); // a shown password is about to be stale
    setChanging(true);
    try {
      await guard(() => provisioning.rotateNodeServerPassword({ nodeId, newPassword, generate, confirm: true }));
      setChanging(false);
      setOpen(false);
      toast(t(first ? "node.sshAccess.set" : "node.sshAccess.changed"));
      void refresh();
      // the change returns no password: the owner sees it through the (audited) reveal, unless they have left
      if (!inactive()) await reveal(true);
    } catch (error) {
      setChanging(false);
      if (isStepUpCancelled(error)) return;
      void refresh(); // an unconfirmed change leaves it pending, a retired node loses the button
      // unconfirmed: the state pill turns to "Change not confirmed" and its line says what to do, so the form just closes
      if (ConnectError.from(error).rawMessage === "ssh_password_change_unverified") setOpen(false);
      else setProblem(passwordError(error, t, viaKey));
    }
  }

  function close() {
    setOpen(false);
    setOwn("");
    setProblem("");
  }

  const fields = [
    {
      id: `ssh-password-${nodeId}`,
      value: password,
      label: pendingPassword ? t("node.sshAccess.unverifiedCurrent") : fresh ? t("node.sshAccess.newPassword") : t("node.sshAccess.password"),
    },
    ...(pendingPassword ? [{ id: `ssh-pending-password-${nodeId}`, value: pendingPassword, label: t("node.sshAccess.unverifiedPending") }] : []),
  ];

  return (
    <div ref={blockRef} className="flex flex-col gap-3 border-t border-line pt-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <SectionLabel as="h3" icon="lock" tone="lavender" className="me-auto">
          {t("node.sshAccess.passwordTitle")}
        </SectionLabel>
        <div className="flex flex-wrap items-center gap-1.5">
          {saved && access.passwordGenerated && (
            <span data-tone="lavender" className="tone-chip inline-flex h-[22px] items-center rounded-xl px-2 text-[11px] font-bold whitespace-nowrap">
              {t("node.sshAccess.generatedPill")}
            </span>
          )}
          {access.rotationPending ? (
            <StatusPill sm kind="warn" label={t("node.sshAccess.notConfirmed")} />
          ) : saved ? (
            <StatusPill sm kind="ok" label={t("node.sshAccess.verified")} />
          ) : (
            <StatusPill sm kind="off" label={t("node.sshAccess.notSaved")} />
          )}
        </div>
      </div>
      {access.rotationPending ? (
        // with both candidates on screen their own line explains the state
        !(showingPassword && pendingPassword) && <p className="text-[13px] leading-snug text-pretty text-fg">{t("node.sshAccess.pendingHint")}</p>
      ) : !saved ? (
        !retired && <p className="text-[13px] leading-snug text-pretty text-muted">{t("node.sshAccess.notSavedHint")}</p>
      ) : (
        (access.passwordGenerated || !access.passwordLogin) && (
          <div className="flex flex-col gap-1.5">
            {access.passwordGenerated && <p className="text-[13px] leading-snug text-pretty text-muted">{t("node.sshAccess.generated")}</p>}
            {/* passwordLogin means something only next to a saved password: false there is a fact, not a missing one */}
            {!access.passwordLogin && <p className="text-[13px] leading-snug text-pretty text-muted">{t("node.sshAccess.loginClosed")}</p>}
          </div>
        )
      )}
      {/* one under the other, full width, on the phone: two auto-width rows of different lengths read as ragged */}
      {(canReveal || !retired) && (
        <div className="grid gap-2 md:flex md:flex-wrap">
          {canReveal && (
            <Button variant="secondary" size="md" disabled={busy || changing} onClick={() => (showingPassword ? clearSecret() : void reveal())}>
              <Icon name={showingPassword ? "eyeOff" : "eye"} size={14} />
              {busy ? t("common.loading") : showingPassword ? t("node.sshAccess.hide") : t("node.sshAccess.show")}
            </Button>
          )}
          {!retired && (
            <Button variant="secondary" size="md" aria-expanded={open} disabled={changing} className="aria-expanded:bg-surface-2" onClick={() => (open ? close() : setOpen(true))}>
              <Icon name={saved ? "refresh" : "plus"} size={14} />
              {t(saved ? "node.sshAccess.change" : "node.sshAccess.setPassword")}
            </Button>
          )}
        </div>
      )}
      {!retired && open && changing && (
        <div role="status" className="flex items-start gap-3 rounded-card border border-line bg-surface-2 p-3">
          <span className="pt-0.5">
            <StatusGlyph kind="busy" />
          </span>
          <div className="flex min-w-0 flex-col gap-1">
            <p className="text-[13px] font-semibold">{t(saved ? "node.sshAccess.changing" : "node.sshAccess.setting")}</p>
            <p className="text-xs leading-snug text-pretty text-muted">{t("node.sshAccess.changingHint")}</p>
          </div>
        </div>
      )}
      {!retired && open && !changing && (
        <form onSubmit={(e) => void change(e)} className="flex flex-col gap-3 rounded-card border border-line bg-surface-2 p-3">
          <Segmented
            variant="thumb"
            aria-label={t("node.sshAccess.changeMode")}
            value={mode}
            onValueChange={(m) => {
              setMode(m);
              setOwn(""); // a typed password does not wait hidden behind Generate
              setProblem("");
            }}
            options={[
              { value: "generate", label: t("node.sshAccess.modeGenerate") },
              { value: "own", label: t("node.sshAccess.modeOwn") },
            ]}
          />
          {mode === "generate" ? (
            <p className="text-xs leading-snug text-pretty text-muted">{t("node.sshAccess.generateHint")}</p>
          ) : (
            <div className="flex flex-col gap-1.5">
              <label htmlFor={`ssh-new-password-${nodeId}`} className="text-xs font-semibold text-muted">
                {t("node.sshAccess.ownLabel")}
              </label>
              <input
                id={`ssh-new-password-${nodeId}`}
                type="password"
                dir="ltr"
                autoComplete="new-password"
                spellCheck={false}
                maxLength={1024}
                aria-describedby={`ssh-new-password-hint-${nodeId}`}
                value={own}
                onChange={(e) => setOwn(e.currentTarget.value)}
                className="h-11 w-full min-w-0 rounded-field border border-line bg-surface px-3.5 font-mono text-sm text-fg outline-none transition-colors duration-200 focus:border-accent"
              />
              <p id={`ssh-new-password-hint-${nodeId}`} className="text-xs leading-snug text-pretty text-muted">
                {t("node.sshAccess.ownHint")}
              </p>
            </div>
          )}
          {problem && (
            <Callout kind="bad" alert>
              {problem}
            </Callout>
          )}
          <div className="flex flex-wrap gap-2">
            <Button type="submit" variant="primary" size="md" disabled={mode === "own" && own.length < minLength}>
              {t(saved ? "node.sshAccess.changeNow" : "node.sshAccess.setNow")}
            </Button>
            <Button variant="ghost" size="md" onClick={close}>
              {t("common.cancel")}
            </Button>
          </div>
        </form>
      )}
      {showingPassword && (
        <div className="flex flex-col gap-2">
          {pendingPassword && <Callout kind="warn">{t("node.sshAccess.unverified")}</Callout>}
          {fields.map((field) => (
            <PasswordField key={field.id} {...field} fresh={fresh} />
          ))}
          <p className="text-xs leading-snug text-pretty text-muted">{fresh ? t("node.sshAccess.newPasswordHint") : t("node.sshAccess.revealHint")}</p>
        </div>
      )}
    </div>
  );
}

/** A shown password: mono, left to right, a square copy button inside the field's end (the check shows for 2 s once copied). */
function PasswordField({ id, label, value, fresh }: { id: string; label: string; value: string; fresh: boolean }) {
  const t = useT();
  const toast = useToast();
  const [done, setDone] = useState(false);
  const name = done ? t("common.copied") : t("common.copy");

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setDone(true);
      window.setTimeout(() => setDone(false), 2000);
    } catch {
      toast(t("common.copyFailed"));
    }
  }

  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className="text-xs font-semibold text-muted">
        {label}
      </label>
      <div
        className={`flex h-11 items-center gap-2 rounded-field border bg-surface ps-3.5 pe-1 transition-colors duration-200 focus-within:border-accent ${fresh ? "border-accent-line" : "border-line"}`}
      >
        <input
          id={id}
          type="text"
          value={value}
          readOnly
          dir="ltr"
          autoComplete="off"
          spellCheck={false}
          className="h-full min-w-0 flex-1 bg-transparent font-mono text-sm text-fg outline-none"
        />
        <IconButton variant="field" aria-label={name} title={name} onClick={() => void copy()}>
          <Icon name={done ? "check" : "copy"} size={16} className={done ? "text-accent-text" : undefined} />
        </IconButton>
      </div>
    </div>
  );
}
