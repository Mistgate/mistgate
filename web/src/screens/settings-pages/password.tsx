import { Code, ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { CodeField, TotpEnroll } from "@/components/auth-parts";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { Pending, QueryError } from "@/components/ui/query-error";
import { useToast } from "@/components/ui/toast";
import { useT, type T } from "@/i18n";
import { auth } from "@/lib/api";
import { demoQr } from "@/lib/qr-demo";
import { loginPattern, passwordTooLong, passwordTooShort, type TotpEnrolment } from "@/lib/credentials";
import { errorCode, errorText } from "@/lib/errors";
import { plain } from "@/lib/plain";
import { ICONS, CmdRow, Mono, Pb, Sec, Spin } from "./sx-bits";
import { SxWindow } from "./sx-dlg";
import { cx } from "@/lib/cx";

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

/** Too many wrong attempts: the server answers "exhausted" and the password stays shut for 15 minutes (the board's red line over the card). */
const lockedBy = (e: unknown) => ConnectError.from(e).code === Code.ResourceExhausted;

const passwordError = (pw: string, t: T) => (passwordTooShort(pw) ? t("auth.err.passwordShort") : passwordTooLong(pw) ? t("auth.err.passwordLong") : undefined);

/** What a scene says about the card: the lock over it, and a window that is open (the re-bind one holds a made-up secret). */
export type PasswordDemo = { lock?: boolean; window?: "change" | "change-err" | "change-busy" | "add" | "add-qr" | "rebind" };

const demoTotp: TotpEnrolment = { ceremonyId: "cer_demo", qr: demoQr, secret: "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP" };

/**
 * "Password and code" (the board's second card of Security): the spare way in next to the passkeys. Change the password, re-bind the
 * authenticator app, or (an admin with passkeys only) add the password login; and the way back from the panel server for a lost phone.
 * A refusal for too many wrong attempts puts the board's red line over the card.
 */
export function PasswordCard({ demo }: { demo?: PasswordDemo }) {
  const t = useT();
  const toast = useToast();
  const guard = useStepUp();
  const q = useQuery(passwordLoginQuery);
  const [open, setOpen] = useState<"change" | "add" | null>(demo?.window?.startsWith("change") ? "change" : demo?.window?.startsWith("add") ? "add" : null);
  const [rebind, setRebind] = useState<TotpEnrolment | null>(demo?.window === "rebind" ? demoTotp : null);
  const [locked, setLocked] = useState(!!demo?.lock);
  const pw = q.data;
  // the server's step-up comes first, then the new secret: the QR opens only once it is there
  const begin = useMutation({
    mutationFn: () => guard(() => auth.beginTotpEnrollment({})),
    onSuccess: (r) => setRebind({ ceremonyId: r.ceremonyId, qr: r.totpQr, secret: r.totpSecret }),
    onError: (e) => {
      if (isStepUpCancelled(e)) return;
      if (lockedBy(e)) setLocked(true);
      toast.error(failText(e, t));
    },
  });

  return (
    <Sec tone="sand" d={ICONS.lock} title={t("pw.title")} foot={pw?.available && pw.enabled ? t("pw.lockout") : undefined}>
      {locked && (
        <Pb tone="bad" className="sx-pbm" title={t("sx.sec.lock")}>
          {t("sx.sec.lock.body")}
        </Pb>
      )}
      {q.isPending && (
        <div className="sx-pad" aria-busy="true">
          <Pending compact />
        </div>
      )}
      {q.isError && (
        <div className="sx-pad">
          <QueryError compact error={q.error} onRetry={() => void q.refetch()} />
        </div>
      )}
      {pw && (
        <div className="sx-it sx-pw">
          {pw.enabled ? (
            <div>
              <b>{t("sx.sec.pw.on")}</b>
              <em>
                {t("pw.login")}: <Mono>{pw.login}</Mono>
              </em>
            </div>
          ) : (
            <div>
              <b>{pw.available ? t("pw.off") : t("pw.noKey")}</b>
            </div>
          )}
          <span className="ai-na">
            {pw.enabled && (
              <>
                <Button size="md" onClick={() => setOpen("change")}>
                  {t("pw.change")}
                </Button>
                <Button size="md" disabled={!pw.available || begin.isPending} onClick={() => begin.mutate()}>
                  {t("pw.rebind")}
                </Button>
              </>
            )}
            {!pw.enabled && pw.available && (
              <Button size="md" onClick={() => setOpen("add")}>
                {t("pw.add")}
              </Button>
            )}
          </span>
        </div>
      )}
      {pw?.available && (
        <div className="sx-rl">
          <span>{t("pw.lost")}</span>
          <CmdRow value={pw.enabled ? `mistgate auth reset-login ${pw.login}` : "mistgate auth reset-login"} />
        </div>
      )}
      {open === "change" && <ChangePassword login={pw?.login ?? ""} demo={demo?.window} onLocked={() => setLocked(true)} onClose={() => setOpen(null)} />}
      {open === "add" && <Enroll demo={demo?.window === "add-qr" ? "add-qr" : undefined} onLocked={() => setLocked(true)} onClose={() => setOpen(null)} />}
      {rebind && <Enroll begun={rebind} onLocked={() => setLocked(true)} onClose={() => setRebind(null)} />}
    </Sec>
  );
}

/** The window "Change password": the current one, a new one of 12 characters or more. Asks on the press, not on every key; the other sessions end. */
export function ChangePassword({ login, onClose, onLocked, demo }: { login: string; onClose: () => void; onLocked?: () => void; demo?: PasswordDemo["window"] }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState(demo === "change-err" ? "short" : "");
  const [e1, setE1] = useState(demo === "change-err" ? t("sx.sec.pw.cur.empty") : "");
  const [e2, setE2] = useState(demo === "change-err" ? t("auth.err.passwordShort") : "");
  const [fail, setFail] = useState<string | null>(null);

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
        setE1(t("pw.wrong"));
        setCurrent("");
        return;
      }
      if (lockedBy(e)) onLocked?.();
      setFail(failText(e, t));
    },
  });
  const busy = change.isPending || demo === "change-busy";

  function submit(e: FormEvent) {
    e.preventDefault();
    setFail(null);
    if (busy) return;
    const a = current ? "" : t("sx.sec.pw.cur.empty");
    const b = passwordError(next, t) ?? "";
    setE1(a);
    setE2(b);
    if (!a && !b) change.mutate();
  }

  return (
    <SxWindow
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("pw.changeTitle")}
      shot="password-change"
      footer={
        <>
          <Button size="lg" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="lg" type="submit" form="sx-pw" className={cx(busy && "sx-busy")} aria-busy={busy || undefined}>
            {busy && <Spin />}
            {busy ? t("sx.sec.pw.busy") : t("pw.change")}
          </Button>
        </>
      }
    >
      <form id="sx-pw" onSubmit={submit} className="contents">
        {/* tells a password manager whose password this is */}
        <input type="text" name="username" autoComplete="username" value={login} readOnly hidden />
        <p className="sx-dp">{t("pw.changeBody")}</p>
        {fail && <Pb tone="bad">{fail}</Pb>}
        <label className={cx("an-f", e1 && "err")}>
          <span>{t("pw.current")}</span>
          <input
            className="an-i"
            type="password"
            autoComplete="current-password"
            value={current}
            autoFocus
            aria-invalid={!!e1 || undefined}
            onChange={(e) => {
              setCurrent(e.target.value);
              setE1("");
            }}
          />
          <small>{e1}</small>
        </label>
        <label className={cx("an-f", e2 && "err")}>
          <span>{t("pw.new")}</span>
          <input
            className="an-i"
            type="password"
            autoComplete="new-password"
            value={next}
            aria-invalid={!!e2 || undefined}
            onChange={(e) => {
              setNext(e.target.value);
              setE2("");
            }}
          />
          <small>{e2 || t("auth.err.passwordShort")}</small>
        </label>
      </form>
    </SxWindow>
  );
}

/**
 * The authenticator app, the setup wizard's way: scan the QR (or type the key), then a code of it proves the scan. With
 * `begun` it re-binds the app of an existing password login; without, it first asks for the login and the password of a
 * new one (a passkey admin adding the spare way in).
 */
export function Enroll({ begun, onClose, onLocked, demo }: { begun?: TotpEnrolment; onClose: () => void; onLocked?: () => void; demo?: "add-qr" }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const adding = !begun;
  const [login, setLogin] = useState(demo ? "owner" : "");
  const [password, setPassword] = useState("");
  const [totp, setTotp] = useState<TotpEnrolment | null>(begun ?? (demo ? demoTotp : null));
  const [code, setCode] = useState("");
  const [badCode, setBadCode] = useState(false);
  const [fail, setFail] = useState<string | null>(null);
  const loginError = login && !loginPattern.test(login.trim()) ? t("auth.err.login") : undefined;
  const pwError = password ? passwordError(password, t) : undefined;

  const start = useMutation({
    mutationFn: () => guard(() => auth.beginTotpEnrollment({ login: login.trim(), password })),
    onSuccess: (r) => {
      setTotp({ ceremonyId: r.ceremonyId, qr: r.totpQr, secret: r.totpSecret });
      setCode("");
    },
    onError: (e) => {
      if (isStepUpCancelled(e)) return;
      if (lockedBy(e)) onLocked?.();
      setFail(failText(e, t));
    },
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
      if (lockedBy(e)) onLocked?.();
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
  const disabled = busy || (totp ? code.length < 6 : !login.trim() || !password || !!loginError || !!pwError);

  return (
    <SxWindow
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={adding ? t("pw.addTitle") : t("pw.rebindTitle")}
      shot={adding ? "password-add" : "password-rebind"}
      footer={
        <>
          <Button size="lg" disabled={busy} onClick={adding && totp ? () => setTotp(null) : onClose}>
            {adding && totp ? t("auth.back") : t("common.cancel")}
          </Button>
          <Button variant="primary" size="lg" type="submit" form="sx-totp" className={cx(busy && "sx-busy")} aria-busy={busy || undefined} disabled={disabled && !busy}>
            {busy && <Spin />}
            {busy ? t("auth.checking") : totp ? t("stepup.confirm") : t("auth.next")}
          </Button>
        </>
      }
    >
      <form id="sx-totp" onSubmit={submit} className="contents">
        <p className="sx-dp">{adding ? t("pw.addBody") : t("pw.rebindBody")}</p>
        {fail && <Pb tone="bad">{fail}</Pb>}
        {adding && (
          <>
            <label className={cx("an-f", loginError && "err")}>
              <span>{t("auth.login")}</span>
              <input className="an-i" value={login} maxLength={64} autoComplete="username" autoCapitalize="off" spellCheck={false} disabled={!!totp} autoFocus aria-invalid={!!loginError || undefined} onChange={(e) => setLogin(e.target.value)} />
              <small>{loginError ?? ""}</small>
            </label>
            {!totp && (
              <label className={cx("an-f", pwError && "err")}>
                <span>{t("auth.password")}</span>
                <input className="an-i" type="password" autoComplete="new-password" value={password} aria-invalid={!!pwError || undefined} onChange={(e) => setPassword(e.target.value)} />
                <small>{pwError ?? t("auth.setup.passwordHint")}</small>
              </label>
            )}
          </>
        )}
        {totp && (
          <>
            <TotpEnroll qr={totp.qr} secret={totp.secret} caption={t("auth.setup.totpScan")} />
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
      </form>
    </SxWindow>
  );
}
