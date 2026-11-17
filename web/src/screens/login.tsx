import { useQuery } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { useState, type FormEvent, type ReactNode } from "react";
import { AuthFrame, CodeField, GoButton, GoNote, Heading, LockedCard, OtherWay, PasskeyBlock, PasswordField } from "@/components/auth-parts";
import { useCaptcha } from "@/components/captcha";
import { TurnstileRow } from "@/components/turnstile";
import { Notice } from "@/components/ui/notice";
import { TextField } from "@/components/ui/text-field";
import { Tick } from "@/components/windows";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { auth, errorKey, webauthnSupported } from "@/lib/api";
import { signInWithPassword } from "@/lib/credentials";
import { devCase } from "@/lib/dev-case";
import { loginInfoQuery } from "@/lib/instance";
import { getPasskey } from "@/lib/passkey";
import { forgetSession } from "@/lib/session";

export function LoginScreen() {
  const t = useT();
  const router = useRouter();
  const supported = webauthnSupported() && devCase !== "nopasskey";
  // GetLoginInfo was fetched before this page rendered (the root route waits for it, once); here it gets two more tries.
  // The password form is offered when some admin can sign in with one, and while that is not known.
  const infoQuery = useQuery({ ...loginInfoQuery, retry: 2 });
  const info = infoQuery.data;
  const captcha = useCaptcha();
  const passwordLogin = !info || info.passwordLogin || devCase !== null;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<MessageKey | null>(null);
  // the sign-in went through: the button turns into a confirmation while the panel loads
  const [done, setDone] = useState(false);
  // Without passkey support the password form is the only way in, so it starts open.
  const [alt, setAlt] = useState((!supported && passwordLogin) || devCase === "badtotp");
  const [login, setLogin] = useState("");
  const [password, setPassword] = useState(devCase === "badtotp" ? "hunter2hunter2" : "");
  const [code, setCode] = useState(devCase === "badtotp" ? "481209" : "");
  const [attemptsLeft, setAttemptsLeft] = useState<number | null>(devCase === "badtotp" ? 2 : null);
  const [lockedUntil, setLockedUntil] = useState<number | null>(() =>
    devCase === "locked" ? Date.now() + (14 * 60 + 59) * 1000 : null,
  );

  async function signIn() {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      // the token is spent by Begin, win or lose: the widget starts over, and the passkey prompt can wait
      const begin = await auth.beginLogin({ turnstileToken: captcha.token }).finally(captcha.reset);
      const credentialJson = await getPasskey(begin.optionsJson);
      await auth.finishLogin({ ceremonyId: begin.ceremonyId, credentialJson });
      forgetSession();
      setDone(true);
      await router.navigate({ to: "/" });
    } catch (e) {
      if (!captcha.stale(e)) setError(errorKey(e, "login"));
      setBusy(false);
    }
  }

  async function signInAlt(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    setAttemptsLeft(null);
    try {
      const r = await signInWithPassword({ login: login.trim(), password, code, turnstileToken: captcha.token }).finally(captcha.reset);
      if (r.kind === "ok") {
        forgetSession();
        setDone(true);
        await router.navigate({ to: "/" });
        return;
      }
      if (r.kind === "failed") {
        setAttemptsLeft(r.attemptsLeft);
        setCode("");
      } else {
        setLockedUntil(r.until);
      }
    } catch (err) {
      if (!captcha.stale(err)) setError(errorKey(err, "login"));
    }
    setBusy(false);
  }

  if (lockedUntil !== null) {
    return (
      <AuthFrame viewKey="locked" footer={t("win.login.decoy")}>
        <LockedCard until={lockedUntil} onExpire={() => setLockedUntil(null)} />
      </AuthFrame>
    );
  }

  return (
    <LoginPage
      view={done ? "done" : alt ? "password" : "key"}
      host={location.host}
      supported={supported}
      passwordLogin={passwordLogin}
      onRetryInfo={infoQuery.isError && !infoQuery.isFetching ? () => void infoQuery.refetch() : undefined}
      captcha={captcha.widget}
      captchaOn={captcha.on}
      captchaReady={captcha.ready}
      busy={busy}
      error={error ? t(error) : undefined}
      codeError={attemptsLeft === null ? undefined : `${t("auth.badLogin")} ${t.n("auth.attemptsLeft", attemptsLeft)}`}
      login={login}
      password={password}
      code={code}
      onLogin={setLogin}
      onPassword={setPassword}
      onCode={(v) => {
        setCode(v);
        setAttemptsLeft(null);
      }}
      onPasskey={() => void signIn()}
      onSubmit={signInAlt}
      onToggle={() => setAlt((v) => !v)}
    />
  );
}

export type LoginPageProps = {
  /** The passkey button, the password form, or the sign-in that went through. */
  view: "key" | "password" | "done";
  host: string;
  supported: boolean;
  passwordLogin: boolean;
  /** The page asked for the login info and got nothing: says so, with a way to ask again. */
  onRetryInfo?: () => void;
  /** The check of the page (Turnstile's row or widget), null when the panel has none. */
  captcha: ReactNode;
  captchaOn: boolean;
  captchaReady: boolean;
  /** Waiting for the passkey (key view) or for the password check (password view). */
  busy: boolean;
  error?: string;
  codeError?: string;
  login: string;
  password: string;
  code: string;
  onLogin: (v: string) => void;
  onPassword: (v: string) => void;
  onCode: (v: string) => void;
  onPasskey: () => void;
  onSubmit: (e: FormEvent) => void;
  onToggle: () => void;
};

/**
 * The sign-in page as the board draws it (PnLogin): the brand, the title with the address, the check, the main button with its
 * line, "another way" under it, the line about the decoy site at the foot. The same page is the password form (with the six boxes
 * of the code) and, once the sign-in went through, the confirmation with the bar of the panel loading under it.
 */
export function LoginPage(p: LoginPageProps) {
  const t = useT();
  const footer = t("win.login.decoy");
  const head = (
    <Heading title={t("auth.login.title")} mono>
      {t("auth.login.body", { host: p.host })}
    </Heading>
  );

  if (p.view === "done") {
    return (
      <AuthFrame viewKey="login" footer={footer} wave>
        {head}
        {p.captchaOn && <TurnstileRow status="ok" label={t("win.cf.passed")} />}
        <div className="flex animate-[mg-up_0.3s_var(--e)_both] flex-col gap-3">
          <div role="status" className="wn-confirm flex h-12 w-full items-center justify-center gap-2.5 rounded-xl border border-accent-line bg-accent-soft text-[15px] font-medium">
            <Tick state="done" />
            {t("win.login.done")}
          </div>
          <div aria-hidden className="h-0.5 overflow-hidden rounded-[1px] bg-step">
            <i className="wn-fill block h-full w-full rounded-[1px] bg-accent-mark" />
          </div>
          <GoNote>{t("win.login.opening")}</GoNote>
        </div>
      </AuthFrame>
    );
  }

  const alt = p.view === "password";
  const waiting = p.busy && !alt;
  const failed = !!p.error && !p.busy;

  return (
    <AuthFrame viewKey="login" footer={footer}>
      {head}
      {p.onRetryInfo && (
        <Notice tone="danger">
          {t("err.network")}{" "}
          <button type="button" onClick={p.onRetryInfo} className="font-medium underline underline-offset-2">
            {t("common.retry")}
          </button>
        </Notice>
      )}
      {!p.supported && (
        <Notice title={t("auth.noPasskeyTitle")}>{p.passwordLogin ? t("auth.noPasskeyBody") : t("auth.noPasskeyNoPassword")}</Notice>
      )}
      {p.captcha}
      {!alt && p.supported && (
        <PasskeyBlock
          label={waiting ? t("win.login.waiting") : failed ? t("win.retry") : t("auth.login.passkey")}
          hint={waiting ? t("win.login.confirmInBrowser") : failed ? p.error! : t("auth.login.passkeyHint")}
          bad={failed}
          busy={waiting}
          disabled={!p.captchaReady}
          onClick={p.onPasskey}
        />
      )}
      {alt && p.passwordLogin && (
        <form onSubmit={p.onSubmit} className="flex animate-[mg-up_0.3s_var(--e)_both] flex-col gap-3">
          <TextField
            label={t("auth.login")}
            value={p.login}
            onChange={(e) => p.onLogin(e.target.value)}
            autoComplete="username"
            autoCapitalize="off"
            spellCheck={false}
          />
          <PasswordField label={t("auth.password")} value={p.password} onChange={p.onPassword} autoComplete="current-password" />
          <CodeField label={t("auth.code")} value={p.code} onChange={p.onCode} error={p.codeError} />
          <GoButton type="submit" busy={p.busy} disabled={!p.captchaReady || !p.login.trim() || !p.password || p.code.length < 6}>
            {p.busy ? t("auth.checking") : t("auth.login.submit")}
          </GoButton>
          <GoNote bad={failed}>{failed ? p.error! : t("win.login.codeHint")}</GoNote>
        </form>
      )}
      {p.supported && p.passwordLogin && (
        <OtherWay open={alt} label={alt ? t("win.login.byPasskey") : t("auth.login.otherWay")} onToggle={p.onToggle} />
      )}
    </AuthFrame>
  );
}
