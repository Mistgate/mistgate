import { ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { authorizedKeysFile } from "@/components/panel-ssh-key";
import { Fingerprint } from "@/components/ssh-login";
import { sshError } from "@/components/ssh-node-install";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Chip, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Callout } from "@/components/ui/notice";
import { StatusPill } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { Tip } from "@/components/ui/tooltip";
import type { NodeServerAccess, NodeServerKey } from "@/gen/mistgate/admin/v1/provisioning_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { provisioning } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";

/** The keys in a node's authorized_keys. Each read is an SSH login to the server (up to ~8 s): kept 5 minutes, not refetched on focus or retried on its own. */
export function useServerKeys(nodeId: string, enabled = true) {
  return useQuery({
    queryKey: ["node-server-keys", nodeId],
    queryFn: ({ signal }) => provisioning.listNodeServerKeys({ nodeId }, { signal }),
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
    retry: false,
    enabled,
  });
}

/** Puts the panel's key on the server and makes it the way in (a key already there is just taken into use): Install, Restore and Use. */
export const addPanelKey = (nodeId: string) => provisioning.addNodeServerKey({ nodeId, panelKey: true, confirm: true });

// What a refusal of the key calls means for the owner; anything else reads as the install wizard words it, including
// the real cause of a failed login with the saved access (host key changed, timeout, refused, broke off). A refused
// login keeps its own words here: it is the saved access that failed, not a password the owner just typed.
const keyCodes: Record<string, MessageKey> = {
  ssh_public_key_invalid: "node.sshKeys.error.invalid",
  ssh_public_key_weak: "node.sshKeys.error.weak",
  ssh_public_key_not_found: "node.sshKeys.error.notFound",
  ssh_key_last_panel_login: "node.sshKeys.error.lastLogin",
  ssh_panel_key_not_accepted: "node.sshKeys.error.notAccepted",
  authorized_keys_unsafe: "node.sshKeys.error.unsafe",
  authorized_keys_changed: "node.sshKeys.error.changed",
  authorized_keys_too_large: "node.sshKeys.error.tooLarge",
  ssh_authorized_keys_unavailable: "node.sshKeys.error.unavailable",
  ssh_authentication_failed: "node.sshKeys.error.auth",
  node_server_access_not_found: "node.sshKeys.error.noAccess",
  node_server_password_not_saved: "node.sshAccess.error.notSaved",
  node_retired: "node.sshAccess.error.retired",
};

/** The words for a key operation's failure: an error, or a bare code (AttachNodeServerAccess's panelKeyError). */
export function keyError(error: unknown, t: T, edge = false): string {
  const raw = typeof error === "string" ? error : ConnectError.from(error).rawMessage;
  if (Object.hasOwn(keyCodes, raw)) return t(keyCodes[raw]!);
  return typeof error === "string" ? t("node.sshKeys.error.unavailable") : sshError(error, t, edge);
}

const keyTypes: Record<string, string> = {
  "ssh-ed25519": "ED25519",
  "sk-ssh-ed25519@openssh.com": "ED25519-SK",
  "sk-ecdsa-sha2-nistp256@openssh.com": "ECDSA-SK",
};

/** ED25519, RSA 3072, ECDSA 256, ED25519-SK: the type as ssh-keygen -l names it. */
export function keyTypeLabel(type: string, bits: number): string {
  if (Object.hasOwn(keyTypes, type)) return keyTypes[type]!;
  if (type === "ssh-rsa") return `RSA ${bits}`;
  if (type.startsWith("ecdsa-")) return `ECDSA ${bits}`;
  return type.toUpperCase();
}

type Busy = "" | "panel" | "bind" | `unbind:${string}`;

/**
 * The SSH access card's keys block (live nodes only): what ~/.ssh/authorized_keys of the login holds, read over SSH when
 * the card opens. A row per key: its type, comment, fingerprint and marks (the panel key, options on the line, the same
 * key on several lines), and Unbind behind an inline confirm. The panel key gets its own states: missing from the file
 * (Restore) or there but not used (Use). Binding takes a public key; a pasted private key is refused here, never sent.
 */
export function SSHKeys({ nodeId, access }: { nodeId: string; access: NodeServerAccess }) {
  const t = useT();
  const toast = useToast();
  const guard = useStepUp();
  const qc = useQueryClient();
  const { edge } = useEdition();
  const keys = useServerKeys(nodeId);
  const [busy, setBusy] = useState<Busy>("");
  const [problem, setProblem] = useState<{ at: string; text: string } | null>(null);
  const [binding, setBinding] = useState(false);
  const [publicKey, setPublicKey] = useState("");
  const [confirming, setConfirming] = useState("");

  const list = keys.data?.keys ?? [];
  const panelInFile = list.some((k) => k.panelKey);
  const missing = keys.isSuccess && access.panelKeyFingerprint !== "" && !panelInFile;

  async function run<R>(what: Busy, at: string, call: () => Promise<R>, done: MessageKey | ((response: R) => MessageKey)) {
    setBusy(what);
    setProblem(null);
    try {
      const response = await guard(call);
      toast(t(typeof done === "function" ? done(response) : done));
      setConfirming("");
      return true;
    } catch (error) {
      if (!isStepUpCancelled(error)) setProblem({ at, text: keyError(error, t, edge) });
      return false;
    } finally {
      setBusy("");
      // a refusal can still mean the file or the access moved (changed meanwhile, the key stopped being used)
      void qc.invalidateQueries({ queryKey: ["node-server-keys", nodeId] });
      void qc.invalidateQueries({ queryKey: ["node-server-access"] });
    }
  }

  const usePanelKey = (at: string, done: MessageKey) => void run("panel", at, () => addPanelKey(nodeId), done);

  function typed(value: string) {
    // a private key never leaves the browser, and is not kept in the field either
    if (value.includes("PRIVATE KEY")) {
      setPublicKey("");
      setProblem({ at: "bind", text: t("node.sshKeys.privateRefused") });
      return;
    }
    if (problem?.at === "bind") setProblem(null);
    setPublicKey(value);
  }

  async function bind(e: FormEvent) {
    e.preventDefault();
    const line = publicKey.trim();
    if (!line || line.includes("PRIVATE KEY")) return;
    // a key the file already holds on a line with options stays as it is (the server appends no bare copy): say so
    const done = (r: { key?: NodeServerKey }) => (r.key?.restricted ? "node.sshKeys.alreadyRestricted" : "node.sshKeys.bound");
    if (await run("bind", "bind", () => provisioning.addNodeServerKey({ nodeId, publicKey: line, confirm: true }), done)) closeBind();
  }

  function closeBind() {
    setBinding(false);
    setPublicKey("");
    if (problem?.at === "bind") setProblem(null);
  }

  function unbind(key: NodeServerKey) {
    const panel = key.panelKey || key.fingerprint === access.panelKeyFingerprint;
    void run(
      `unbind:${key.fingerprint}`,
      key.fingerprint,
      () => provisioning.removeNodeServerKey({ nodeId, fingerprint: key.fingerprint, confirm: true, confirmPanelKey: panel }),
      panel && access.panelKeyFingerprint ? "node.sshKeys.panelUnbound" : "node.sshKeys.unbound",
    );
  }

  const problemAt = (at: string) =>
    problem?.at === at ? (
      <Callout kind="bad" alert>
        {problem.text}
      </Callout>
    ) : null;

  return (
    <div className="flex flex-col gap-3 border-t border-line pt-4">
      <div className="flex items-center gap-x-3 gap-y-2">
        <SectionLabel as="h3" icon="key" tone="sage" className="flex-none">
          {t("node.sshKeys.title")}
        </SectionLabel>
        <span dir="ltr" className="me-auto min-w-0 truncate font-mono text-[11px] text-muted">
          {authorizedKeysFile} · {access.username}
        </span>
        <IconButton variant="flat" aria-label={t("node.sshKeys.reload")} title={t("node.sshKeys.reload")} disabled={keys.isFetching || busy !== ""} onClick={() => void keys.refetch()}>
          <Icon name="refresh" size={14} className={keys.isFetching ? "animate-[mg-ui-spin_1s_linear_infinite]" : undefined} />
        </IconButton>
      </div>

      {keys.isPending ? (
        <div role="status" className="flex flex-col gap-3">
          {/* two still rows the shape of a key, while the panel logs in (no shimmer: it is waiting, not working harder) */}
          {[0, 1].map((i) => (
            <div key={i} aria-hidden className="flex flex-col gap-2 border-t border-line pt-3 first:border-t-0 first:pt-0">
              <div className="flex items-center gap-1.5">
                <span className="h-[22px] w-16 rounded-ctl bg-surface-2" />
                <span className={cx("h-3.5 rounded-sm bg-surface-2", i ? "w-20" : "w-28")} />
              </div>
              <span className="h-3 w-4/5 max-w-[340px] rounded-sm bg-surface-2" />
            </div>
          ))}
          <span className="text-xs text-muted">{t("node.sshKeys.loading")}</span>
        </div>
      ) : keys.isError ? (
        <Callout
          kind="bad"
          alert
          action={
            <Button variant="secondary" size="sm" disabled={keys.isFetching} onClick={() => void keys.refetch()}>
              {t("common.retry")}
            </Button>
          }
        >
          <b className="font-semibold">{t("node.sshKeys.loadFailed")}</b> {keyError(keys.error, t, edge)}
        </Callout>
      ) : (
        <>
          {missing && (
            <Callout
              kind="bad"
              action={
                <Button variant="secondary" size="sm" disabled={busy !== ""} onClick={() => usePanelKey("missing", "node.sshKeys.restored")}>
                  {busy === "panel" ? t("node.sshAccess.installingKey") : t("node.sshKeys.restore")}
                </Button>
              }
            >
              <b className="block font-semibold">{t("node.sshKeys.missing")}</b>
              <span className="text-muted">{t("node.sshKeys.missingHint", { file: authorizedKeysFile })}</span>
            </Callout>
          )}
          {problemAt("missing")}
          {list.length === 0 ? (
            <p className="text-[13px] text-muted">{t("node.sshKeys.empty")}</p>
          ) : (
            <ul className="flex flex-col">
              {list.map((key) => (
                <KeyRow
                  key={key.fingerprint}
                  item={key}
                  access={access}
                  busy={busy}
                  confirming={confirming === key.fingerprint}
                  onConfirm={(on) => {
                    setConfirming(on ? key.fingerprint : "");
                    if (problem?.at === key.fingerprint) setProblem(null);
                  }}
                  onUnbind={() => unbind(key)}
                  onUse={() => usePanelKey(key.fingerprint, "node.sshKeys.used")}
                  problem={problemAt(key.fingerprint)}
                />
              ))}
            </ul>
          )}
          {keys.data.otherLines > 0 && <p className="text-xs text-muted">{t.n("node.sshKeys.other", keys.data.otherLines)}</p>}
        </>
      )}

      {binding ? (
        <form data-bind-form onSubmit={(e) => void bind(e)} className="flex flex-col gap-3 rounded-card border border-line bg-surface-2 p-3">
          <div className="flex flex-col gap-1.5">
            <label htmlFor={`ssh-bind-${nodeId}`} className="text-xs font-semibold text-muted">
              {t("node.sshKeys.publicKey")}
            </label>
            <textarea
              id={`ssh-bind-${nodeId}`}
              dir="ltr"
              rows={3}
              value={publicKey}
              onChange={(e) => typed(e.target.value)}
              placeholder="ssh-ed25519 AAAA… alice@laptop"
              spellCheck={false}
              autoComplete="off"
              autoCapitalize="off"
              aria-describedby={`ssh-bind-hint-${nodeId}`}
              className="min-h-[88px] w-full resize-y rounded-field border border-line bg-surface p-3 font-mono text-xs leading-relaxed wrap-anywhere text-fg outline-none transition-colors duration-200 placeholder:text-faint focus:border-accent"
            />
            <p id={`ssh-bind-hint-${nodeId}`} className="text-xs leading-snug text-muted">
              {t("node.sshKeys.publicKeyHint")}
            </p>
          </div>
          {problemAt("bind")}
          <div className="flex flex-wrap gap-2">
            <Button type="submit" variant="primary" size="md" disabled={busy !== "" || publicKey.trim() === ""}>
              {busy === "bind" ? t("node.sshKeys.binding") : t("node.sshKeys.bindNow")}
            </Button>
            <Button variant="ghost" size="md" disabled={busy === "bind"} onClick={closeBind}>
              {t("common.cancel")}
            </Button>
          </div>
        </form>
      ) : (
        <div className="grid gap-2 md:flex md:flex-wrap">
          <Button variant="secondary" size="md" data-bind disabled={busy !== ""} onClick={() => setBinding(true)}>
            <Icon name="plus" size={14} />
            {t("node.sshKeys.bind")}
          </Button>
        </div>
      )}
    </div>
  );
}

/** One key: type and comment with its marks, Unbind at the end; the fingerprint under them; the confirm under that. */
function KeyRow({
  item,
  access,
  busy,
  confirming,
  onConfirm,
  onUnbind,
  onUse,
  problem,
}: {
  item: NodeServerKey;
  access: NodeServerAccess;
  busy: Busy;
  confirming: boolean;
  onConfirm: (on: boolean) => void;
  onUnbind: () => void;
  onUse: () => void;
  problem: ReactNode;
}) {
  const t = useT();
  const used = item.panelKey && access.panelKeyFingerprint !== "";
  const unused = item.panelKey && access.panelKeyFingerprint === "";
  const panel = item.panelKey || item.fingerprint === access.panelKeyFingerprint;
  // the server proves the saved password before it lets the panel key go; with none there is nothing to prove
  const onlyWay = panel && !access.passwordSaved;
  const unbinding = busy === `unbind:${item.fingerprint}`;

  return (
    <li className="flex flex-col gap-2 border-t border-line py-3 first:border-t-0 first:pt-0 last:pb-0">
      <div className="flex items-start gap-3">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5 pt-[3px]">
          <Chip mono className="text-fg">
            {keyTypeLabel(item.type, item.bits)}
          </Chip>
          {/* a comment is the server's text, cleaned by the panel: it wraps at its own spaces, mid-word only if it must */}
          <span className={cx("min-w-0 text-[13px] leading-snug break-words", item.comment ? "font-semibold text-fg" : "text-muted")}>
            {item.comment ? <bdi>{item.comment}</bdi> : t("node.sshKeys.noComment")}
          </span>
          {used && <StatusPill sm kind="ok" label={t("panelKey.title")} />}
          {unused && <StatusPill sm kind="warn" label={t("node.sshKeys.unused")} />}
          {item.restricted && (
            <Tip label={t("node.sshKeys.restrictedTip")}>
              <span tabIndex={0} className="rounded-xl outline-none focus-visible:ring-2 focus-visible:ring-accent">
                <StatusPill sm kind="off" label={t("node.sshKeys.restricted")} />
              </span>
            </Tip>
          )}
          {item.lines > 1 && <Chip>{t.n("node.sshKeys.lines", item.lines)}</Chip>}
        </div>
        {/* quiet in the list; the red belongs to the confirm under the row */}
        <Button variant="ghost" size="sm" data-unbind aria-expanded={confirming} disabled={busy !== ""} className="flex-none aria-expanded:bg-surface-2 aria-expanded:text-fg" onClick={() => onConfirm(!confirming)}>
          {t("node.sshKeys.unbind")}
        </Button>
      </div>
      {/* the fingerprint has the row's full width: on one line from ~400 px, else broken after "SHA256:" */}
      <div className="flex items-center gap-1">
        <Fingerprint value={item.fingerprint} className="min-w-0 text-muted" />
        <CopyFingerprint value={item.fingerprint} />
      </div>
      {unused && (
        <Callout
          kind="warn"
          action={
            <Button variant="secondary" size="sm" disabled={busy !== ""} onClick={onUse}>
              {busy === "panel" ? t("node.sshAccess.installingKey") : t("node.sshKeys.use")}
            </Button>
          }
        >
          {t("node.sshKeys.unusedHint")}
        </Callout>
      )}
      {confirming && (
        <div role="group" aria-label={t("node.sshKeys.unbindTitle")} className="mt-1 flex flex-col gap-2.5 rounded-ctl border border-line bg-surface-2 p-3">
          <p className="text-[13px] font-semibold text-fg">{t("node.sshKeys.unbindTitle")}</p>
          <p className="text-xs leading-snug">
            <Fingerprint value={item.fingerprint} className="text-fg" />
            {item.comment && <span className="text-muted"> {t("common.quoted", { name: item.comment })}</span>}
          </p>
          <p className="text-xs leading-snug text-pretty text-danger-text">
            {onlyWay ? t("node.sshKeys.unbindOnly") : panel && access.panelKeyFingerprint ? t("node.sshKeys.unbindPanel") : t("node.sshKeys.unbindWarn")}
          </p>
          {problem}
          <div className="flex flex-wrap gap-2">
            <Button variant="danger" size="sm" disabled={busy !== "" || onlyWay} onClick={onUnbind}>
              {unbinding ? t("node.sshKeys.unbinding") : t("node.sshKeys.unbindNow")}
            </Button>
            <Button variant="ghost" size="sm" disabled={unbinding} onClick={() => onConfirm(false)}>
              {t("common.cancel")}
            </Button>
          </div>
        </div>
      )}
      {!confirming && problem}
    </li>
  );
}

function CopyFingerprint({ value }: { value: string }) {
  const t = useT();
  const toast = useToast();
  const [done, setDone] = useState(false);
  const name = done ? t("common.copied") : t("node.sshKeys.copy");
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
    <IconButton variant="flat" aria-label={name} title={name} className="size-6 flex-none rounded-md" onClick={() => void copy()}>
      <Icon name={done ? "check" : "copy"} size={12} className={done ? "text-accent-text" : undefined} />
    </IconButton>
  );
}
