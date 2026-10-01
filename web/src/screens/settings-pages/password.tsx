import { Code, ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { CodeField, TotpEnroll } from "@/components/auth-parts";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { useT, type T } from "@/i18n";
import { auth } from "@/lib/api";
import { loginPattern, passwordTooLong, passwordTooShort, type TotpEnrolment } from "@/lib/credentials";
import { errorCode, errorText } from "@/lib/errors";
import { plain } from "@/lib/plain";
import { CopyRow } from "./domains";

/** GetPasswordLogin: whether the signed-in admin can sign in with a login, a password and an authenticator code. */
export const passwordLoginQuery = queryOptions({
  queryKey: ["password-login"],
  queryFn: async ({ signal }) => plain(await auth.getPasswordLogin({}, { signal })),
});

/** The coded refusals of these calls (auth.proto), as our sentences; anything else reads as errorText says. */
function failText(e: unknown, t: T) {
  const c = ConnectError.from(e);
  switch (errorCode(c.rawMessage)) {
    case "wrong_password":
      return t("pw.wrong");
    case "login_taken":
      return t("pw.loginTaken");
    case "enrollment_expired":
      return t("pw.expired");
    case "no_master_key":
      return t("pw.noKey");
  }
  return c.code === Code.ResourceExhausted ? t("pw.locked") : errorText(e, t);
}

const passwordError = (pw: string, t: T) =>
  passwordTooShort(pw) ? t("auth.err.passwordShort") : passwordTooLong(pw) ? t("auth.err.passwordLong") : undefined;

/**
 * Settings -> Security -> "Password and code": the spare way in next to the passkeys. Change the password, re-bind the
 * authenticator app, or (an admin with passkeys only) add the password login; and the way back from the panel server
 * for a lost phone.
 */
export function PasswordCard() {
  const t = useT();
  const toast = useToast();
  const guard = useStepUp();
  const q = useQuery(passwordLoginQuery);
  const [open, setOpen] = useState<"change" | "add" | null>(null);
  const [rebind, setRebind] = useState<TotpEnrolment | null>(null);
  const pw = q.data;
  // the server's step-up comes first, then the new secret: the QR opens only once it is there
  const begin = useMutation({
    mutationFn: () => guard(() => auth.beginTotpEnrollment({})),
    onSuccess: (r) => setRebind({ ceremonyId: r.ceremonyId, uri: r.totpUri, secret: r.totpSecret }),
    onError: (e) => isStepUpCancelled(e) || toast.error(failText(e, t)),
  });

  return (
    <section className="rounded-card-lg border border-line bg-surface px-4 py-1.5">
      <div className="flex min-h-12 items-center py-2">
        <SectionLabel as="h2" icon="key" tone="lavender">
          {t("pw.title")}
        </SectionLabel>
      </div>
      {q.isPending && <Pending compact className="border-t border-line py-3.5" />}
      {q.isError && <QueryError compact className="border-t border-line py-3" error={q.error} onRetry={() => void q.refetch()} />}
      {pw && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2.5 border-t border-line py-3.5">
          <p className="min-w-[200px] flex-1 text-[13px] leading-normal text-pretty">
            {pw.enabled ? (
              <>
                {t("pw.on")}{" "}
                <span className="text-muted">
                  {t("pw.login")}: <b className="font-mono text-fg">{pw.login}</b>
                </span>
              </>
            ) : (
              <span className="text-muted">{pw.available ? t("pw.off") : t("pw.noKey")}</span>
            )}
          </p>
          <div className="flex flex-wrap gap-2">
            {pw.enabled && (
              <>
                <Button variant="secondary" size="sm" onClick={() => setOpen("change")}>
                  {t("pw.change")}
                </Button>
                <Button variant="secondary" size="sm" disabled={!pw.available || begin.isPending} onClick={() => begin.mutate()}>
                  {t("pw.rebind")}
                </Button>
              </>
            )}
            {!pw.enabled && pw.available && (
              <Button variant="secondary" size="sm" onClick={() => setOpen("add")}>
                {t("pw.add")}
              </Button>
            )}
          </div>
        </div>
      )}
      {pw?.available && (
        <div className="flex flex-col gap-2 border-t border-line py-3.5">
          <span className="text-xs leading-normal text-pretty text-muted">{t("pw.lost")}</span>
          <CopyRow value={pw.enabled ? `mistgate auth reset-login ${pw.login}` : "mistgate auth reset-login"} />
          {pw.enabled && <span className="text-xs leading-normal text-pretty text-muted">{t("pw.lockout")}</span>}
        </div>
      )}
      {open === "change" && pw && <ChangePassword login={pw.login} onClose={() => setOpen(null)} />}
      {open === "add" && <Enroll onClose={() => setOpen(null)} />}
      {rebind && <Enroll begun={rebind} onClose={() => setRebind(null)} />}
    </section>
  );
}

export function ChangePassword({ login, onClose }: { login: string; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [wrong, setWrong] = useState(false);
  const [fail, setFail] = useState<string | null>(null);
  const nextError = next ? passwordError(next, t) : undefined;

  const change = useMutation({
    mutationFn: () => guard(() => auth.changePassword({ currentPassword: current, newPassword: next })),
    onSuccess: (r) => {
      onClose();
      toast(r.endedSessions ? t("pw.changedEnded", { n: r.endedSessions }) : t("pw.changed"));
      void qc.invalidateQueries({ queryKey: ["sessions"] });
    },
    onError: (e) => {
      if (isStepUpCancelled(e)) return;
      if (errorCode(ConnectError.from(e).rawMessage) === "wrong_password") {
        setWrong(true);
        setCurrent("");
      } else setFail(failText(e, t));
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    setFail(null);
    if (current && next && !nextError && !change.isPending) change.mutate();
  }

  return (
    <Modal open onOpenChange={(o) => !o && !change.isPending && onClose()} title={t("pw.changeTitle")} description={t("pw.changeBody")}>
      <form onSubmit={submit} className="flex flex-col gap-3.5">
        {/* tells a password manager whose password this is */}
        <input type="text" name="username" autoComplete="username" value={login} readOnly hidden />
        {fail && <Notice tone="danger">{fail}</Notice>}
        <TextField
          label={t("pw.current")}
          type="password"
          autoComplete="current-password"
          value={current}
          onChange={(e) => {
            setCurrent(e.target.value);
            setWrong(false);
          }}
          error={wrong ? t("pw.wrong") : undefined}
          autoFocus
        />
        <TextField
          label={t("pw.new")}
          type="password"
          autoComplete="new-password"
          value={next}
          onChange={(e) => setNext(e.target.value)}
          error={nextError}
          hint={t("auth.err.passwordShort")}
        />
        <div className="flex justify-end gap-2">
          <Button variant="ghost" size="md" disabled={change.isPending} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" variant="primary" size="md" disabled={change.isPending || !current || !next || !!nextError}>
            {change.isPending ? t("auth.checking") : t("pw.change")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/**
 * The authenticator app, the setup wizard's way: scan the QR (or type the key), then a code of it proves the scan. With
 * `begun` it re-binds the app of an existing password login; without, it first asks for the login and the password of a
 * new one (a passkey admin adding the spare way in).
 */
export function Enroll({ begun, onClose }: { begun?: TotpEnrolment; onClose: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const adding = !begun;
  const [login, setLogin] = useState("");
  const [password, setPassword] = useState("");
  const [totp, setTotp] = useState<TotpEnrolment | null>(begun ?? null);
  const [code, setCode] = useState("");
  const [badCode, setBadCode] = useState(false);
  const [fail, setFail] = useState<string | null>(null);
  const loginError = login && !loginPattern.test(login.trim()) ? t("auth.err.login") : undefined;
  const pwError = password ? passwordError(password, t) : undefined;

  const start = useMutation({
    mutationFn: () => guard(() => auth.beginTotpEnrollment({ login: login.trim(), password })),
    onSuccess: (r) => {
      setTotp({ ceremonyId: r.ceremonyId, uri: r.totpUri, secret: r.totpSecret });
      setCode("");
    },
    onError: (e) => isStepUpCancelled(e) || setFail(failText(e, t)),
  });
  const finish = useMutation({
    mutationFn: () => auth.finishTotpEnrollment({ ceremonyId: totp!.ceremonyId, totpCode: code }),
    onSuccess: (r) => {
      onClose();
      if (adding) toast(t("pw.added"));
      else toast(r.endedSessions ? t("pw.reboundEnded", { n: r.endedSessions }) : t("pw.rebound"));
      void qc.invalidateQueries({ queryKey: passwordLoginQuery.queryKey });
      void qc.invalidateQueries({ queryKey: ["sessions"] });
    },
    onError: (e) => {
      // a wrong code can be retried on the same QR; a lost ceremony (too many tries, 5 minutes) starts over
      if (errorCode(ConnectError.from(e).rawMessage) === "invalid_code") {
        setBadCode(true);
        setCode("");
        return;
      }
      if (!adding) {
        onClose(); // the QR on screen is dead: the card's button starts a fresh one
        toast.error(failText(e, t));
        return;
      }
      setFail(failText(e, t));
      setTotp(null);
    },
  });
  const busy = start.isPending || finish.isPending;

  function submit(e: FormEvent) {
    e.preventDefault();
    setFail(null);
    if (busy) return;
    if (totp) {
      setBadCode(false);
      if (code.length === 6) finish.mutate();
    } else if (login.trim() && password && !loginError && !pwError) start.mutate();
  }

  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={adding ? t("pw.addTitle") : t("pw.rebindTitle")}
      description={adding ? t("pw.addBody") : t("pw.rebindBody")}
    >
      <form onSubmit={submit} className="flex flex-col gap-3.5">
        {fail && <Notice tone="danger">{fail}</Notice>}
        {adding && (
          <>
            <TextField
              label={t("auth.login")}
              value={login}
              onChange={(e) => setLogin(e.target.value)}
              maxLength={64}
              autoComplete="username"
              autoCapitalize="off"
              spellCheck={false}
              disabled={!!totp}
              error={loginError}
              autoFocus
            />
            {!totp && (
              <TextField
                label={t("auth.password")}
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                error={pwError}
                hint={t("auth.setup.passwordHint")}
              />
            )}
          </>
        )}
        {totp && (
          <>
            <TotpEnroll uri={totp.uri} secret={totp.secret} caption={t("auth.setup.totpScan")} />
            <CodeField
              label={t("pw.codeLabel")}
              value={code}
              onChange={(v) => {
                setCode(v);
                setBadCode(false);
              }}
              error={badCode ? t("auth.badCode") : undefined}
            />
          </>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" size="md" disabled={busy} onClick={adding && totp ? () => setTotp(null) : onClose}>
            {adding && totp ? t("auth.back") : t("common.cancel")}
          </Button>
          <Button
            type="submit"
            variant="primary"
            size="md"
            disabled={busy || (totp ? code.length < 6 : !login.trim() || !password || !!loginError || !!pwError)}
          >
            {busy ? t("auth.checking") : totp ? t("stepup.confirm") : t("auth.next")}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
