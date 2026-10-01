import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { Link, useRouter } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";
import {
  AuthFrame,
  CodeField,
  Heading,
  OtherWay,
  PasskeyBlock,
  PasswordField,
  StepDots,
  TotpEnroll,
} from "@/components/auth-parts";
import { useCaptcha } from "@/components/captcha";
import { Button, buttonClass } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { TextField } from "@/components/ui/text-field";
import { langs, setLang, useLang, useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { Code, ConnectError } from "@connectrpc/connect";
import { auth, errorKey, groups, webauthnSupported } from "@/lib/api";
import { useBrand } from "@/lib/brand";
import {
  beginPasswordSetup,
  finishPasswordSetup,
  loginPattern,
  passwordTooLong,
  passwordTooShort,
  type TotpEnrolment,
} from "@/lib/credentials";
import { devCase } from "@/lib/dev-case";
import { cx } from "@/lib/cx";
import { createPasskey } from "@/lib/passkey";
import { forgetSession } from "@/lib/session";

const langNames = { ru: "Русский", en: "English" } as const;
const steps = 3;

/** A fresh panel starts with an empty "Everyone" group (named in the setup language): profiles have a place to go, users a group to land in. */
async function ensureEveryoneGroup(name: string) {
  try {
    if ((await groups.listGroups({})).groups.length === 0) await groups.createGroup({ name });
  } catch {
    // not worth a word on the welcome screen: the Groups tab offers it again
  }
}

/** First run: language -> create the admin (passkey first, password + code as the alternative) -> done. */
export function SetupScreen() {
  const t = useT();
  const router = useRouter();
  // The one-time token lives in the URL fragment so it never reaches server logs.
  const [token] = useState(() => window.location.hash.slice(1).trim());
  const [step, setStep] = useState(devCase === "done" ? 2 : 0);

  if (!token && devCase === null) {
    return (
      <AuthFrame>
        <Heading title={t("auth.missingToken.title")}>{t("auth.missingToken.body")}</Heading>
        <Link to="/login" className={buttonClass("secondary", "lg", true)}>
          {t("auth.toLogin")}
        </Link>
      </AuthFrame>
    );
  }

  return (
    <AuthFrame viewKey={`setup-${step}`} aside={<StepDots step={step} total={steps} />}>
      {step === 0 && <LanguageStep onNext={() => setStep(1)} />}
      {step === 1 && <AdminStep token={token} onDone={() => (void ensureEveryoneGroup(t("users.everyone")), setStep(2))} />}
      {step === 2 && (
        <DoneStep
          // straight into the add-node window on the Overview, where the first-run checklist goes on
          onFirstNode={() => router.navigate({ to: "/", search: { add: 1 } })}
          onLater={() => router.navigate({ to: "/" })}
        />
      )}
    </AuthFrame>
  );
}

function LanguageStep({ onNext }: { onNext: () => void }) {
  const t = useT();
  const lang = useLang();
  const { name } = useBrand();
  return (
    <>
      <Heading title={t("auth.setup.welcomeTitle")}>{t("auth.setup.welcomeBody", { brand: name })}</Heading>
      <RadioGroup
        aria-label={t("header.language")}
        value={lang}
        onValueChange={(v) => setLang(v as (typeof langs)[number])}
        className="flex flex-col gap-2"
      >
        {langs.map((l) => (
          <Radio.Root
            key={l}
            value={l}
            className="flex h-14 cursor-pointer items-center gap-3 rounded-card border border-line bg-surface px-4 text-left transition-colors duration-200 data-checked:border-accent-line data-checked:bg-accent-soft"
          >
            <span className="w-6 font-mono text-xs font-bold text-muted">{l.toUpperCase()}</span>
            <b className="flex-1 text-[15px]">{langNames[l]}</b>
            <span
              aria-hidden
              className={cx(
                "box-border size-5 rounded-full border-[1.5px] transition-colors",
                l === lang ? "border-accent bg-accent" : "border-faint",
              )}
            />
          </Radio.Root>
        ))}
      </RadioGroup>
      <Button variant="primary" size="lg" full onClick={onNext}>
        {t("auth.next")}
      </Button>
    </>
  );
}

function AdminStep({ token, onDone }: { token: string; onDone: () => void }) {
  const t = useT();
  const supported = webauthnSupported() && devCase !== "nopasskey";
  const captcha = useCaptcha();
  const [login, setLogin] = useState("admin");
  const [alt, setAlt] = useState(!supported);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  // Password path, step 2: the server has hashed the password and issued the authenticator secret to scan.
  const [totp, setTotp] = useState<TotpEnrolment | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<MessageKey | null>(null);
  const [badCode, setBadCode] = useState(false);

  // The proto carries display_name for a passkey admin (the name shown in the panel and in the passkey list).
  async function createWithPasskey() {
    setBusy(true);
    setError(null);
    try {
      // the token is spent by Begin, win or lose: the widget starts over, and the passkey prompt can wait
      const begin = await auth
        .beginSetup({ setupToken: token, displayName: login.trim(), turnstileToken: captcha.token })
        .finally(captcha.reset);
      const credentialJson = await createPasskey(begin.optionsJson);
      await auth.finishSetup({ setupToken: token, ceremonyId: begin.ceremonyId, credentialJson });
      forgetSession();
      onDone();
    } catch (e) {
      if (!captcha.stale(e)) setError(errorKey(e, "setup"));
    }
    setBusy(false);
  }

  const loginError = !loginPattern.test(login.trim()) ? t("auth.err.login") : undefined;
  const passwordError = passwordTooShort(password) ? t("auth.err.passwordShort") : passwordTooLong(password) ? t("auth.err.passwordLong") : undefined;

  /** Step 1: login + password; the server answers with the authenticator secret as a QR code. */
  async function beginWithPassword(e: FormEvent) {
    e.preventDefault();
    if (loginError || passwordError) return;
    setBusy(true);
    setError(null);
    try {
      const begun = await beginPasswordSetup({
        setupToken: token,
        displayName: login.trim(),
        login: login.trim(),
        password,
        turnstileToken: captcha.token,
      }).finally(captcha.reset);
      setTotp(begun);
      setCode("");
      setBadCode(false);
    } catch (err) {
      if (!captcha.stale(err)) setError(errorKey(err, "setup"));
    }
    setBusy(false);
  }

  /** Step 2: the first code from the app proves the secret was scanned, then the admin exists and is signed in. */
  async function finishWithPassword(e: FormEvent) {
    e.preventDefault();
    if (!totp) return;
    setBusy(true);
    setError(null);
    setBadCode(false);
    try {
      await finishPasswordSetup(token, totp.ceremonyId, code);
      forgetSession();
      onDone();
    } catch (err) {
      // a wrong code can be retried on the same QR; a lost ceremony (too many tries, 5 minutes) starts over
      if (ConnectError.from(err).code === Code.InvalidArgument) {
        setBadCode(true);
        setCode("");
      } else setError(errorKey(err, "setup"));
    }
    setBusy(false);
  }

  return (
    <>
      <Heading title={t("auth.setup.adminTitle")}>{t("auth.setup.adminBody")}</Heading>
      <TextField
        label={t("auth.login")}
        value={login}
        onChange={(e) => setLogin(e.target.value)}
        maxLength={64}
        autoComplete="username"
        autoCapitalize="off"
        spellCheck={false}
        disabled={!!totp}
        error={alt && login ? loginError : undefined}
      />
      {error && <Notice tone="danger">{t(error)}</Notice>}
      {!supported && <Notice title={t("auth.noPasskeyTitle")}>{t("auth.noPasskeyBody")}</Notice>}
      {/* Begin carries the token; once the password path is on its code step the check has passed, and "back" asks again */}
      {!totp && captcha.widget}
      {!alt && (
        <PasskeyBlock
          label={busy ? t("auth.waitingPasskey") : t("auth.createPasskey")}
          hint={t("auth.setup.passkeyHint")}
          disabled={!supported || busy || !captcha.ready || !login.trim()}
          onClick={createWithPasskey}
        />
      )}
      {supported && (
        <OtherWay
          open={alt}
          label={alt ? t("auth.hideOtherWay") : t("auth.setup.otherWay")}
          onToggle={() => {
            setAlt((v) => !v);
            setTotp(null);
          }}
        />
      )}
      {alt && !totp && (
        <form onSubmit={beginWithPassword} className="screen-enter flex flex-col gap-3">
          <PasswordField
            label={t("auth.password")}
            value={password}
            onChange={setPassword}
            autoComplete="new-password"
            error={password ? passwordError : undefined}
            hint={t("auth.setup.passwordHint")}
          />
          <Button type="submit" variant="primary" size="lg" full disabled={busy || !captcha.ready || !!loginError || !!passwordError}>
            {busy ? t("auth.checking") : t("auth.next")}
          </Button>
        </form>
      )}
      {alt && totp && (
        <form onSubmit={finishWithPassword} className="screen-enter flex flex-col gap-3">
          <TotpEnroll uri={totp.uri} secret={totp.secret} caption={t("auth.setup.totpScan")} />
          <CodeField label={t("auth.code")} value={code} onChange={(v) => { setCode(v); setBadCode(false); }} error={badCode ? t("auth.badCode") : undefined} />
          <Button type="submit" variant="primary" size="lg" full disabled={busy || code.length < 6}>
            {busy ? t("auth.checking") : t("auth.setup.createAdmin")}
          </Button>
          <Button variant="ghost" size="md" full disabled={busy} onClick={() => setTotp(null)}>
            {t("auth.back")}
          </Button>
        </form>
      )}
    </>
  );
}

function DoneStep({ onFirstNode, onLater }: { onFirstNode: () => void; onLater: () => void }) {
  const t = useT();
  return (
    <>
      <Heading title={t("auth.setup.doneTitle")}>{t("auth.setup.doneBody")}</Heading>
      <button
        type="button"
        onClick={onFirstNode}
        className="card-hover flex flex-col gap-1.5 rounded-card border border-accent-line bg-accent-soft p-4 text-left"
      >
        <b className="text-[15px]">{t("auth.setup.addFirst")} ›</b>
        <span className="text-[13px] leading-snug text-muted">{t("auth.setup.addFirstBody")}</span>
      </button>
      <Button variant="ghost" size="lg" full onClick={onLater}>
        {t("auth.setup.later")}
      </Button>
    </>
  );
}
