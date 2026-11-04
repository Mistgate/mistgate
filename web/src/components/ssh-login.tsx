import { useEffect, useId, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Callout } from "@/components/ui/notice";
import { Segmented } from "@/components/ui/segmented";
import { StatusPill } from "@/components/ui/status";
import { TextField } from "@/components/ui/text-field";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";

/** How the panel logs in to a server: its own key, the server password, or the owner's own private key (one install only). */
export type LoginMethod = "key" | "password" | "own";

type OwnKey = { text: string; name: string };

// a private key file is a few KiB at most (RSA 4096 in OpenSSH form is ~3.4 KiB)
const maxKeyBytes = 16 * 1024;

/**
 * The login a form holds: the method and the secrets that go with it. The secrets live in this state only, never in a
 * query cache, a URL, a log or storage; picking another method drops them, and so does unmounting (closing the window).
 * The passphrase is asked only after the server said the key needs one, and a new key forgets that it did.
 */
export function useSSHLogin(defaultMethod: LoginMethod) {
  const [chosen, setChosen] = useState<LoginMethod | null>(null);
  const method = chosen ?? defaultMethod;
  const [password, setPassword] = useState("");
  const [ownKey, setOwnKeyState] = useState<OwnKey | null>(null);
  const [passphrase, setPassphrase] = useState("");
  const [needsPassphrase, setNeedsPassphrase] = useState(false);
  // a password install adds the panel key unless the owner unticks it; an own-key install always does (the server forces it)
  const [installPanelKey, setInstallPanelKey] = useState(true);

  const clearSecrets = () => {
    setPassword("");
    setOwnKeyState(null);
    setPassphrase("");
    setNeedsPassphrase(false);
  };
  const setOwnKey = (key: OwnKey | null) => {
    setOwnKeyState(key);
    setPassphrase("");
    setNeedsPassphrase(false);
  };

  return {
    method,
    /** The owner picks a method: whatever secret the last one held goes. */
    pick: (m: LoginMethod) => {
      setChosen(m);
      clearSecrets();
    },
    /** Holds the method just proven (the default no longer moves under it), keeping its secrets. */
    hold: () => setChosen(method),
    password,
    setPassword,
    ownKey,
    setOwnKey,
    passphrase,
    setPassphrase,
    needsPassphrase,
    askPassphrase: () => setNeedsPassphrase(true),
    installPanelKey,
    setInstallPanelKey,
    clearSecrets,
    ready: method === "key" || (method === "password" ? password !== "" : !!ownKey && (!needsPassphrase || passphrase !== "")),
    /** The login part of a request: exactly one way in, the passphrase only once the server asked for it. */
    request: () =>
      method === "own"
        ? { password: "", usePanelKey: false, privateKey: ownKey?.text ?? "", ...(needsPassphrase ? { privateKeyPassphrase: passphrase } : {}) }
        : { password: method === "password" ? password : "", usePanelKey: method === "key" },
    /** installPanelKey for Start, Retry and Attach: a key login needs none. */
    install: () => (method === "key" ? {} : { installPanelKey: method === "own" || installPanelKey }),
  };
}

export type SSHLogin = ReturnType<typeof useSSHLogin>;

/** A key fingerprint in mono, left to right: it breaks after "SHA256:" first, then only where a phone needs it. */
export function Fingerprint({ value, className }: { value: string; className?: string }) {
  const at = value.indexOf(":") + 1;
  return (
    // 11 px on a phone: the 43 characters after "SHA256:" then fit one line beside the copy button
    <span dir="ltr" className={cx("font-mono text-[11px] leading-snug wrap-anywhere sm:text-xs", className)}>
      {value.slice(0, at)}
      <wbr />
      {value.slice(at)}
    </span>
  );
}

/** Panel key | Password | Own key: the owner's own key is the last option and never the default. */
export function LoginSwitch({ login, onPick, className }: { login: SSHLogin; onPick?: () => void; className?: string }) {
  const t = useT();
  return (
    <Segmented
      variant="thumb"
      aria-label={t("node.ssh.method")}
      value={login.method}
      onValueChange={(m) => {
        login.pick(m);
        onPick?.();
      }}
      options={[
        { value: "key", label: t("panelKey.title") },
        { value: "password", label: t("node.ssh.methodPassword") },
        { value: "own", label: t("node.ssh.methodOwn") },
      ]}
      className={cx("w-full sm:w-[360px]", className)}
    />
  );
}

/** "Also add the panel key to the server" under a password: on by default, with why in one line. */
export function InstallPanelKeyBox({ login }: { login: SSHLogin }) {
  const t = useT();
  return (
    <label className="flex cursor-pointer items-start gap-2.5 rounded-field border border-line bg-surface-2 p-3 text-sm leading-relaxed text-fg">
      <input
        type="checkbox"
        data-install-panel-key
        checked={login.installPanelKey}
        onChange={(e) => login.setInstallPanelKey(e.target.checked)}
        className="mt-0.5 size-4 flex-none accent-accent"
      />
      <span className="flex min-w-0 flex-col gap-0.5">
        <span>{t("node.ssh.installPanelKey")}</span>
        <span className="text-xs leading-snug text-pretty text-muted">{t("node.ssh.installPanelKeyHint")}</span>
      </span>
    </label>
  );
}

/**
 * The owner's own private key for one login: the warning (the panel's key at the hoster is safer), a field that takes a
 * dropped key file or a pasted key and never shows it back, and the passphrase once the server asked for it. `warn`
 * false drops the warning where it was already read (the check step, a retry).
 */
export function OwnKeyField({ login, warn = true, note }: { login: SSHLogin; warn?: boolean; note?: string }) {
  const t = useT();
  const id = useId();
  const file = useRef<HTMLInputElement>(null);
  const [problem, setProblem] = useState("");
  const [over, setOver] = useState(false);
  // a file still being read when the owner picks another method (the field goes) must not bring the key back
  const gone = useRef(false);
  useEffect(() => {
    gone.current = false;
    return () => {
      gone.current = true;
    };
  }, []);

  function take(text: string, name = "") {
    setProblem("");
    if (text.length > maxKeyBytes || !text.includes("PRIVATE KEY")) {
      // a .pub line is the usual slip: say so instead of "not a key"
      setProblem(t(/^\s*(ssh-|ecdsa-|sk-)/.test(text) ? "node.ssh.ownKeyPublic" : "node.ssh.ownKeyNotKey"));
      return;
    }
    login.setOwnKey({ text, name });
  }
  async function takeFile(f: File | undefined) {
    if (!f) return;
    if (f.size > maxKeyBytes) return setProblem(t("node.ssh.ownKeyNotKey"));
    const text = await f.text();
    if (!gone.current) take(text, f.name);
  }

  return (
    <div className="flex flex-col gap-3">
      {warn && <Callout kind="warn">{t("node.ssh.ownWarn")}</Callout>}
      {note && <p className="text-xs leading-snug text-pretty text-muted">{note}</p>}
      <div className="flex flex-col gap-1.5">
        <label htmlFor={id} className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">
          {t("node.ssh.ownKey")}
        </label>
        {login.ownKey ? (
          // loaded: what it is, never what it says
          <div className="flex min-h-11 items-center gap-2.5 rounded-field border border-line bg-surface py-1.5 ps-3 pe-1.5">
            <StatusPill sm kind="ok" label={t("node.ssh.ownKeyLoaded")} />
            {login.ownKey.name && (
              <span dir="ltr" className="min-w-0 truncate font-mono text-xs text-muted">
                {login.ownKey.name}
              </span>
            )}
            <Button variant="ghost" size="sm" className="ms-auto" onClick={() => login.setOwnKey(null)}>
              {t("node.ssh.ownKeyRemove")}
            </Button>
          </div>
        ) : (
          <div className="relative">
            {/* always empty: a paste or a drop lands in state at once, so the key is never on screen */}
            <textarea
              id={id}
              value=""
              onChange={(e) => take(e.target.value)}
              onDragOver={() => setOver(true)}
              onDragLeave={() => setOver(false)}
              onDrop={(e) => {
                setOver(false);
                const f = e.dataTransfer.files[0];
                if (!f) return; // dropped text arrives as a change
                e.preventDefault();
                void takeFile(f);
              }}
              rows={2}
              placeholder={t("node.ssh.ownKeyDrop")}
              spellCheck={false}
              autoComplete="off"
              autoCapitalize="off"
              aria-describedby={`${id}-hint`}
              aria-invalid={!!problem || undefined}
              className={cx(
                "block min-h-[76px] w-full resize-none rounded-field border border-dashed bg-surface py-3 ps-3.5 pe-[132px] text-sm leading-snug text-fg outline-none transition-colors duration-200 placeholder:text-muted focus:border-solid focus:border-accent",
                over ? "border-accent bg-accent-soft" : problem ? "border-danger" : "border-line",
              )}
            />
            <Button variant="secondary" size="sm" className="absolute end-2.5 top-1/2 -translate-y-1/2" onClick={() => file.current?.click()}>
              <Icon name="upload" size={12} />
              {t("node.ssh.ownKeyFile")}
            </Button>
            <input
              ref={file}
              type="file"
              hidden
              onChange={(e) => {
                void takeFile(e.target.files?.[0]);
                e.target.value = ""; // the same file again is a new pick
              }}
            />
          </div>
        )}
        <p id={`${id}-hint`} role={problem ? "alert" : undefined} className={cx("text-xs leading-snug text-pretty", problem ? "text-danger-text" : "text-muted")}>
          {problem || t("node.ssh.ownKeyHint")}
        </p>
      </div>
      {login.needsPassphrase && (
        <TextField
          label={t("node.ssh.passphrase")}
          value={login.passphrase}
          onChange={(e) => login.setPassphrase(e.target.value)}
          type="password"
          autoComplete="off"
          maxLength={1024}
          hint={t("node.ssh.passphraseHint")}
          autoFocus
          required
        />
      )}
    </div>
  );
}
