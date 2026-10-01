import { useQuery } from "@tanstack/react-query";
import { useRouter } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import {
  AuthFrame,
  CodeField,
  Heading,
  LockedCard,
  OtherWay,
  PasskeyBlock,
  PasswordField,
} from "@/components/auth-parts";
import { useCaptcha } from "@/components/captcha";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { TextField } from "@/components/ui/text-field";
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
    setBusy(true);
    setError(null);
    try {
      // the token is spent by Begin, win or lose: the widget starts over, and the passkey prompt can wait
      const begin = await auth.beginLogin({ turnstileToken: captcha.token }).finally(captcha.reset);
      const credentialJson = await getPasskey(begin.optionsJson);
      await auth.finishLogin({ ceremonyId: begin.ceremonyId, credentialJson });
      forgetSession();
      await router.navigate({ to: "/" });
    } catch (e) {
      if (!captcha.stale(e)) setError(errorKey(e, "login"));
      setBusy(false);
    }
  }

  async function signInAlt(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setAttemptsLeft(null);
    try {
      const r = await signInWithPassword({ login: login.trim(), password, code, turnstileToken: captcha.token }).finally(captcha.reset);
      if (r.kind === "ok") {
        forgetSession();
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
      <AuthFrame viewKey="locked">
        <LockedCard until={lockedUntil} onExpire={() => setLockedUntil(null)} />
      </AuthFrame>
    );
  }

  const codeError =
    attemptsLeft === null ? undefined : `${t("auth.badLogin")} ${t.n("auth.attemptsLeft", attemptsLeft)}`;

  return (
    <AuthFrame viewKey="login">
      <Heading title={t("auth.login.title")}>{t("auth.login.body", { host: location.host })}</Heading>
      {infoQuery.isError && !infoQuery.isFetching && (
        <Notice tone="danger">
          {t("err.network")}{" "}
          <button type="button" onClick={() => void infoQuery.refetch()} className="font-bold underline underline-offset-2">
            {t("common.retry")}
          </button>
        </Notice>
      )}
      {error && <Notice tone="danger">{t(error)}</Notice>}
      {!supported && (
        <Notice title={t("auth.noPasskeyTitle")}>{passwordLogin ? t("auth.noPasskeyBody") : t("auth.noPasskeyNoPassword")}</Notice>
      )}
      {captcha.widget}
      <PasskeyBlock
        label={busy && !alt ? t("auth.waitingPasskey") : t("auth.login.passkey")}
        hint={t("auth.login.passkeyHint")}
        disabled={!supported || busy || !captcha.ready}
        onClick={signIn}
      />
      {supported && passwordLogin && (
        <OtherWay
          open={alt}
          label={alt ? t("auth.hideOtherWay") : t("auth.login.otherWay")}
          onToggle={() => setAlt((v) => !v)}
        />
      )}
      {alt && passwordLogin && (
        <form onSubmit={signInAlt} className="screen-enter flex flex-col gap-3">
          <TextField
            label={t("auth.login")}
            value={login}
            onChange={(e) => setLogin(e.target.value)}
            autoComplete="username"
            autoCapitalize="off"
            spellCheck={false}
          />
          <PasswordField
            label={t("auth.password")}
            value={password}
            onChange={setPassword}
            autoComplete="current-password"
          />
          <CodeField label={t("auth.code")} value={code} onChange={(v) => { setCode(v); setAttemptsLeft(null); }} error={codeError} />
          <Button
            type="submit"
            variant="primary"
            size="lg"
            full
            disabled={busy || !captcha.ready || !login.trim() || !password || code.length < 6}
          >
            {busy ? t("auth.checking") : t("auth.login.submit")}
          </Button>
        </form>
      )}
    </AuthFrame>
  );
}
