import { ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { authorizedKeysFile } from "@/components/panel-ssh-key";
import { Fingerprint } from "@/components/ssh-login";
import { sshError } from "@/components/ssh-node-install";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Callout } from "@/components/ui/notice";
import { fieldClass } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { Tip } from "@/components/ui/tooltip";
import type { NodeServerAccess, NodeServerKey } from "@/gen/mistgate/admin/v1/provisioning_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { provisioning } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { Glyph } from "@/screens/overview-parts";
import { SvCard, SvHead } from "./server-parts";

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

// Any private key format (OpenSSH, PEM, PuTTY .ppk) is refused in the browser; a bind sends one public key line only.
// The bulk bind on the Nodes page (nodes-bulk-actions.tsx) checks with the same two.
export const privateKeyShape = /PRIVATE KEY|PuTTY-User-Key-File|Private-Lines|Private-MAC/i;
export const publicKeyLine =
  /^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/]+={0,3}( [^\r\n]*)?$/;

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
    if (privateKeyShape.test(value)) {
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
    if (!line || privateKeyShape.test(line)) return;
    // only one public key line is sent: anything else (two lines, a PuTTY file, a stray paste) stays in the browser
    if (!publicKeyLine.test(line)) return setProblem({ at: "bind", text: t("node.sshKeys.error.invalid") });
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
    <SvCard aria-label={t("node.sshKeys.title")}>
      <div className="flex items-start gap-3 pe-4">
        <div className="min-w-0 flex-1">
          <SvHead title={t("np.sv.keys.title")} hint={`${authorizedKeysFile} · ${access.username}`} mono />
        </div>
        <IconButton variant="flat" className="mt-3.5 flex-none" aria-label={t("node.sshKeys.reload")} title={t("node.sshKeys.reload")} disabled={keys.isFetching || busy !== ""} onClick={() => void keys.refetch()}>
          <Icon name="refresh" size={14} className={keys.isFetching ? "animate-[mg-ui-spin_1s_linear_infinite]" : undefined} />
        </IconButton>
      </div>

      {keys.isPending ? (
        <div role="status" className="flex flex-col gap-3 px-6 pb-4 max-[720px]:px-4">
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
        <Callout className="mx-6 mb-4 max-[720px]:mx-4"
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
              className="mx-6 mb-3 max-[720px]:mx-4"
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
          {problemAt("missing") && <div className="mx-6 mb-3 max-[720px]:mx-4">{problemAt("missing")}</div>}
          {list.length === 0 ? (
            <p className="border-t border-line px-6 py-3 text-[13px] text-muted max-[720px]:px-4">{t("node.sshKeys.empty")}</p>
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
          {keys.data.otherLines > 0 && <p className="border-t border-line px-6 py-3 text-xs text-muted max-[720px]:px-4">{t.n("node.sshKeys.other", keys.data.otherLines)}</p>}
        </>
      )}

      {binding ? (
        <form data-bind-form onSubmit={(e) => void bind(e)} className="sv-body line">
          <div className="flex flex-col gap-1.5">
            <label htmlFor={`ssh-bind-${nodeId}`} className="text-[13px] text-sec">
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
              className={cx(fieldClass, "min-h-[88px] resize-y p-3 font-mono text-xs leading-relaxed wrap-anywhere")}
            />
            <p id={`ssh-bind-hint-${nodeId}`} className="text-xs leading-4 text-muted">
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
        <div className="sv-foot">
          <button type="button" className="fz-ga" data-bind disabled={busy !== ""} onClick={() => setBinding(true)}>
            <Glyph d="M6 12h12M12 6v12" size={16} stroke={1.5} />
            {t("node.sshKeys.bind")}
          </button>
        </div>
      )}
    </SvCard>
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
    <li className="sv-key [&>*]:min-w-0">
      <span className="tag max-[720px]:col-span-full max-[720px]:justify-self-start">{keyTypeLabel(item.type, item.bits)}</span>
      <div>
        {/* a comment is the server's text, cleaned by the panel: it wraps at its own spaces, mid-word only if it must */}
        <b className="flex-wrap">
          {item.comment ? <bdi className="break-words">{item.comment}</bdi> : <span className="font-normal text-muted">{t("node.sshKeys.noComment")}</span>}
          {used && <em>{t("panelKey.title")}</em>}
          {unused && <em className="!bg-warn-soft !text-warn">{t("node.sshKeys.unused")}</em>}
          {item.restricted && (
            <Tip label={t("node.sshKeys.restrictedTip")}>
              <em tabIndex={0} className="rounded-md outline-none focus-visible:ring-2 focus-visible:ring-accent">
                {t("node.sshKeys.restricted")}
              </em>
            </Tip>
          )}
          {item.lines > 1 && <em>{t.n("node.sshKeys.lines", item.lines)}</em>}
        </b>
        {/* the fingerprint has the row's full width: on one line from ~400 px, else broken after "SHA256:" */}
        <span className="flex items-center gap-1 !overflow-visible">
          <Fingerprint value={item.fingerprint} className="min-w-0 !text-xs text-muted" />
          <CopyFingerprint value={item.fingerprint} />
        </span>
      </div>
      {/* quiet in the list; the red belongs to the confirm under the row */}
      <button type="button" className="sv-lk bg-transparent" data-unbind aria-expanded={confirming} disabled={busy !== ""} onClick={() => onConfirm(!confirming)}>
        {t("node.sshKeys.unbind")}
      </button>
      {unused && (
        <div className="col-span-full">
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
        </div>
      )}
      {confirming && (
        <div role="group" aria-label={t("node.sshKeys.unbindTitle")} className="col-span-full flex flex-col gap-2.5 rounded-xl border border-line bg-surface-2 p-3">
          <p className="text-[13px] font-medium text-fg">{t("node.sshKeys.unbindTitle")}</p>
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
      {!confirming && problem && <div className="col-span-full">{problem}</div>}
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
