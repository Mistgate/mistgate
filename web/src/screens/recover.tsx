import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { AuthFrame, Heading, TotpEnroll } from "@/components/auth-parts";
import { CopyButton } from "@/components/copy-button";
import { Button, buttonClass } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { TextField } from "@/components/ui/text-field";
import { useT } from "@/i18n";
import { recoverLogin, type RecoveryLoginResult } from "@/lib/api";
import { loginPattern } from "@/lib/credentials";
import { cx } from "@/lib/cx";

const newLinkCommand = "mistgate edge reset-login --file KEYFILE";

/** The link's fragment, `#recoveryToken=<token>[&login=<login>]`: the only place that knows its shape. */
function readFragment() {
  const fields = new URLSearchParams(window.location.hash.slice(1));
  return { token: fields.get("recoveryToken")?.trim() ?? "", login: fields.get("login")?.trim() ?? "" };
}

/** A value to take away (the password, the command for a new link): mono text, Copy under it. */
function CopyBox({ value, big }: { value: string; big?: boolean }) {
  return (
    <div className="flex flex-col gap-2 rounded-field border border-line bg-canvas p-3">
      <code className={cx("font-mono leading-snug font-semibold break-words text-fg select-all", big ? "text-[15px]" : "text-xs")}>{value}</code>
      <div className="flex justify-end">
        <CopyButton value={value} size="md" />
      </div>
    </div>
  );
}

const eyebrow = "text-[11px] font-bold tracking-[0.1em] text-muted uppercase";

/**
 * The page behind the link of `mistgate edge reset-login`. The one-time token (and the login, when the panel has several
 * admins) comes in the URL fragment, never in a server log or a query string; it leaves the address bar right after the
 * first paint, so a reload finds nothing. It is posted only on the owner's click, one request at a time, and kept in memory
 * for another try until the server calls it dead (403). The new password and authenticator secret live only in this
 * component's state, never in storage.
 */
export function RecoverScreen() {
  const t = useT();
  // read during render (both StrictMode passes see it), cleared from the address bar in the effect below
  const [link] = useState(readFragment);
  const [token, setToken] = useState(link.token);
  const [login, setLogin] = useState(link.login);
  const [askLogin, setAskLogin] = useState(link.login !== "");
  const [refusal, setRefusal] = useState<string | null>(null);
  const [result, setResult] = useState<RecoveryLoginResult | null>(null);
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);

  useEffect(() => {
    if (window.location.hash) window.history.replaceState(window.history.state, "", window.location.pathname + window.location.search);
  }, []);

  const loginError = login.trim() && !loginPattern.test(login.trim()) ? t("auth.err.login") : undefined;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (inFlight.current || !token || loginError) return;
    inFlight.current = true;
    setBusy(true);
    setRefusal(null);
    try {
      setResult(await recoverLogin(token, login.trim()));
      setToken("");
    } catch (err) {
      const code = (err as { code?: string }).code;
      if (code === "permission_denied") {
        setToken(""); // wrong, expired or used: the dead-link page
      } else if (code === "invalid_argument") {
        // a live token without a clear target: ask for the login (the server's own text is English and speaks CLI)
        setRefusal(t("auth.recover.whichAdmin"));
        setAskLogin(true);
      } else {
        // the limiter, a panel error or no answer: another click may work; a token spent meanwhile ends in the 403 above
        setRefusal(t(code === "resource_exhausted" ? "err.rateLimited" : code ? "err.generic" : "err.network"));
      }
    }
    inFlight.current = false;
    setBusy(false);
  }

  if (result) {
    return (
      <AuthFrame viewKey="recovered">
        <Heading title={t("auth.recover.doneTitle")} focus>
          {t("auth.recover.doneBody", { admin: result.adminDisplayName })}
        </Heading>
        <div className="flex flex-col gap-1.5">
          <span className={eyebrow}>{t("auth.login")}</span>
          <code className="rounded-field border border-line bg-canvas p-3 font-mono text-[13px] leading-snug font-semibold break-words text-fg select-all">
            {result.login}
          </code>
        </div>
        <div className="flex flex-col gap-1.5">
          <span className={eyebrow}>{t("auth.recover.password")}</span>
          <CopyBox value={result.password} big />
        </div>
        {result.totpUri && result.totpSecret && <TotpEnroll uri={result.totpUri} secret={result.totpSecret} caption={t("auth.setup.totpScan")} />}
        <Notice title={t("auth.recover.onceTitle")}>{t("auth.recover.onceBody")}</Notice>
        <div className="flex flex-col gap-1.5 rounded-card border border-line bg-surface p-4">
          <b className="text-[15px]">{t("auth.recover.nextTitle")}</b>
          <ol className="flex list-decimal flex-col gap-1 pl-5 text-[13px] leading-snug text-pretty text-muted">
            <li>{t("auth.recover.nextSignIn")}</li>
            <li>{t("auth.recover.nextPasskey")}</li>
          </ol>
          <span className="text-xs leading-snug text-muted">{t.n("auth.recover.sessions", result.sessionsEnded)}</span>
        </div>
        <Link to="/login" className={buttonClass("primary", "lg", true)}>
          {t("auth.toLogin")}
        </Link>
      </AuthFrame>
    );
  }

  if (!token) {
    return (
      <AuthFrame viewKey="dead-link">
        <div role="alert">
          <Heading title={t("auth.recover.linkTitle")} focus>
            {t("auth.recover.linkBody")}
          </Heading>
        </div>
        <CopyBox value={newLinkCommand} />
        <Link to="/login" className={buttonClass("secondary", "lg", true)}>
          {t("auth.toLogin")}
        </Link>
      </AuthFrame>
    );
  }

  return (
    <AuthFrame viewKey="recover">
      <Heading title={t("auth.recover.title")}>{t("auth.recover.body")}</Heading>
      <form onSubmit={submit} className="flex flex-col gap-3">
        {askLogin && (
          <TextField
            label={t("auth.login")}
            value={login}
            onChange={(e) => setLogin(e.target.value)}
            maxLength={64}
            autoComplete="username"
            autoCapitalize="off"
            spellCheck={false}
            hint={t("auth.recover.loginHint")}
            error={loginError}
          />
        )}
        {refusal && (
          <Notice tone="danger" title={t("auth.recover.refused")}>
            {refusal}
          </Notice>
        )}
        <Button type="submit" variant="primary" size="lg" full disabled={busy || !!loginError}>
          {busy ? t("auth.checking") : t("auth.recover.submit")}
        </Button>
      </form>
    </AuthFrame>
  );
}
