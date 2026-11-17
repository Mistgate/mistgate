import { Dialog } from "@base-ui/react/dialog";
import { Code, ConnectError } from "@connectrpc/connect";
import { createContext, use, useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { CodeField } from "@/components/auth-parts";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Glyph, glyphs, Halo, Spinner } from "@/components/windows";
import { StepUpRequiredSchema, type StepUpRequired } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import { auth, webauthnSupported } from "@/lib/api";
import { cx } from "@/lib/cx";
import { prefersReducedMotion } from "@/lib/effects";
import { errorText } from "@/lib/errors";
import { getPasskey } from "@/lib/passkey";
import "./step-up.css";

// Step-up re-authentication (auth.proto, BeginStepUp): adding or removing a passkey and ending other
// sessions need a sign-in factor proved in the last 5 minutes. The server answers PERMISSION_DENIED with a
// StepUpRequired detail; `guard` catches that, asks the admin for a passkey or an authenticator code in a
// window (the board's PnConfirm), and runs the call again. Wrap each RPC on its own (not a whole flow), so a
// second WebAuthn prompt never follows a retry. `what` is the line of the window's card ("Install a node on
// 203.0.113.24"): what the person is being asked to allow.

/** The admin closed the dialog instead of confirming. Not an error worth a toast. */
export class StepUpCancelled extends Error {
  readonly cancelled = true; // keeps it apart from a plain Error for type narrowing
  constructor() {
    super("step-up cancelled");
  }
}

export const isStepUpCancelled = (e: unknown) => e instanceof StepUpCancelled;

/** The StepUpRequired detail of a failed call, or null when the call failed for another reason. */
export function stepUpNeeded(e: unknown): StepUpRequired | null {
  const c = ConnectError.from(e);
  return c.code === Code.PermissionDenied ? (c.findDetails(StepUpRequiredSchema)[0] ?? null) : null;
}

type Guard = <T>(call: () => Promise<T>, what?: string) => Promise<T>;
const GuardContext = createContext<Guard>((call) => call());

/** `const guard = useStepUp(); await guard(() => auth.removePasskey({ id }))`. */
export const useStepUp = () => use(GuardContext);

type Ask = { need: StepUpRequired; what?: string; done: () => void; cancel: () => void };

export function StepUpProvider({ children }: { children: ReactNode }) {
  const [ask, setAsk] = useState<Ask | null>(null);
  // calls that fail at the same moment share one dialog
  const pending = useRef<Promise<void> | null>(null);

  const guard = useCallback<Guard>(async (call, what) => {
    try {
      return await call();
    } catch (e) {
      const need = stepUpNeeded(e);
      if (!need) throw e;
      pending.current ??= new Promise<void>((resolve, reject) => setAsk({ need, what, done: resolve, cancel: () => reject(new StepUpCancelled()) })).finally(() => {
        pending.current = null;
        setAsk(null);
      });
      await pending.current;
      return call();
    }
  }, []);

  return (
    <GuardContext value={guard}>
      {children}
      {ask && <StepUpDialog need={ask.need} what={ask.what} onDone={ask.done} onCancel={ask.cancel} />}
    </GuardContext>
  );
}

// How long the "Confirmed" mark stays before the call that waited for it goes on (the board: 900 ms; a calm one with reduced motion).
const okHold = () => (prefersReducedMotion() ? 250 : 900);

function StepUpDialog({ need, what, onDone, onCancel }: { need: StepUpRequired; what?: string; onDone: () => void; onCancel: () => void }) {
  const t = useT();
  const passkey = need.passkey && webauthnSupported();
  // the passkey first; the code when the admin has no passkey (or this browser cannot make one)
  const [mode, setMode] = useState<"key" | "code">(passkey || !need.totp ? "key" : "code");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState<"passkey" | "code" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState(false);
  const hold = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(hold.current), []);

  async function run(kind: "passkey" | "code", confirm: () => Promise<unknown>) {
    setBusy(kind);
    setError(null);
    try {
      await confirm();
      // the mark says "confirmed" for a moment, then the guarded call goes on
      setOk(true);
      hold.current = setTimeout(onDone, okHold());
    } catch (e) {
      const c = ConnectError.from(e);
      if (kind === "code" && c.code === Code.ResourceExhausted) setError(t("stepup.locked"));
      else if (kind === "code" && (c.code === Code.Unauthenticated || c.code === Code.PermissionDenied || c.code === Code.InvalidArgument)) {
        setError(t("stepup.badCode"));
        setCode("");
      } else setError(errorText(e, t));
      setBusy(null);
    }
  }

  const withPasskey = () =>
    run("passkey", async () => {
      const begin = await auth.beginStepUp({});
      const credentialJson = await getPasskey(begin.optionsJson);
      await auth.finishStepUp({ ceremonyId: begin.ceremonyId, credentialJson });
    });
  const withCode = (e?: FormEvent) => {
    e?.preventDefault();
    if (!busy && !ok && code.length === 6) void run("code", () => auth.finishStepUp({ totpCode: code }));
  };

  const view: ConfirmView = ok ? "ok" : mode === "code" ? (busy === "code" ? "codewait" : "code") : busy === "passkey" ? "wait" : error ? "err" : "idle";
  return (
    <ConfirmWindow
      view={view}
      what={what}
      error={error ?? undefined}
      code={code}
      onCode={setCode}
      canKey={passkey}
      canCode={need.totp}
      onKey={() => void withPasskey()}
      onSubmitCode={withCode}
      onSwap={() => {
        setMode(mode === "key" ? "code" : "key");
        setError(null);
        setCode("");
      }}
      onCancel={() => !busy && !ok && onCancel()}
    />
  );
}

export type ConfirmView = "idle" | "wait" | "err" | "code" | "codewait" | "ok";

const ghost = "rounded-xl! font-normal!";

/**
 * The window that asks for a fresh confirmation (the board's PnConfirm: .cfm): a ring with the fingerprint, what is being
 * confirmed in a card, the main button, and two equal quiet ones under it (the other way, cancel). While the passkey is
 * awaited the ring pulses; when it is done the ring fills and the tick draws itself; a failure turns the ring and the line
 * red. `canKey` / `canCode` say which ways this admin has: the one that is not offered leaves its button out.
 */
export function ConfirmWindow({
  view,
  what,
  error,
  code,
  onCode,
  canKey,
  canCode,
  onKey,
  onSubmitCode,
  onSwap,
  onCancel,
}: {
  view: ConfirmView;
  what?: string;
  error?: string;
  code: string;
  onCode: (code: string) => void;
  canKey: boolean;
  canCode: boolean;
  onKey: () => void;
  onSubmitCode: (e?: FormEvent) => void;
  onSwap: () => void;
  onCancel: () => void;
}) {
  const t = useT();
  const main = useRef<HTMLButtonElement>(null);
  const waiting = view === "wait" || view === "codewait";
  const done = view === "ok";
  const asCode = view === "code" || view === "codewait";
  const failed = !done && (view === "err" || !!error);
  const title = done ? t("win.confirm.okTitle") : failed ? t("win.confirm.errTitle") : t("stepup.title");
  const body = done ? t("win.confirm.okBody") : failed ? (error ?? "") : view === "wait" ? t("win.confirm.waitBody") : t("stepup.body");
  const other = asCode ? canKey : canCode;

  return (
    <Dialog.Root open onOpenChange={(o) => !o && onCancel()}>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-[55] animate-[wn-fade_0.25s_var(--e)_backwards] bg-(--scrim) transition-opacity duration-200 data-ending-style:opacity-0" />
        <Dialog.Viewport className="fixed inset-0 z-[55] flex justify-center overflow-y-auto px-6 pt-[120px] pb-6 max-[760px]:px-3 max-[760px]:pb-3 max-[720px]:pt-12">
          <Dialog.Popup
            initialFocus={asCode ? undefined : main}
            className={cx(
              "cfm relative h-fit w-full max-w-[400px] animate-[wn-dlg_0.3s_var(--e)_backwards] overflow-hidden rounded-sheet border border-line bg-surface px-7 pt-9 pb-6 text-center shadow-(--shadow-dlg) outline-none transition-[opacity,transform] duration-200 ease-out data-ending-style:translate-y-2 data-ending-style:opacity-0",
              failed && "err",
            )}
          >
            <Halo className={cx("-top-14 left-1/2 h-[260px] w-[420px] -translate-x-1/2 transition-opacity duration-[400ms] ease-(--e)", failed ? "opacity-[0.12]" : "opacity-40")} />
            <IconButton aria-label={t("common.cancel")} onClick={onCancel} className="absolute top-3 inset-e-3 z-[1]">
              <Icon name="x" />
            </IconButton>
            <div className="relative flex flex-col items-center gap-1.5">
              <div aria-hidden className={cx("cfm-o", waiting && "on", done && "ok")}>
                <i />
                <i />
                <span>
                  <Glyph d={done ? "M5 12.5l4.5 4.5L19 7.5" : glyphs.fingerprint} size={28} />
                </span>
              </div>
              <Dialog.Title className="text-[22px] leading-7 font-medium tracking-[-0.01em]">{title}</Dialog.Title>
              <Dialog.Description aria-live="polite" className={cx("mb-1 max-w-[300px] text-[13px]", failed ? "text-danger-text" : "text-sec")}>
                {body}
              </Dialog.Description>
              <div className="mt-[14px] mb-3 flex w-full items-center gap-3 rounded-xl border border-line bg-canvas px-3.5 py-3 text-start">
                <span aria-hidden className="grid size-8 flex-none place-items-center rounded-ctl bg-step text-sec">
                  <Glyph d={glyphs.swap} />
                </span>
                <div className="flex min-w-0 flex-col gap-0.5">
                  <span className="text-xs text-muted">{t("win.confirm.onBehalf")}</span>
                  <b className="font-medium">{what ?? t("win.confirm.whatDefault")}</b>
                </div>
              </div>

              {!asCode && !done && (
                <>
                  {canKey ? (
                    <Button
                      ref={main}
                      variant="primary"
                      size="lg"
                      full
                      onClick={onKey}
                      className={cx("h-12! gap-2.5! rounded-xl! text-[15px]!", waiting && "pointer-events-none")}
                    >
                      {waiting ? <Spinner /> : <Glyph d={glyphs.fingerprint} />}
                      {waiting ? t("auth.waitingPasskey") : failed ? t("win.retry") : t("stepup.passkey")}
                    </Button>
                  ) : null}
                  <ConfirmRow other={other} onSwap={onSwap} swapLabel={t("stepup.code")} onCancel={onCancel} />
                </>
              )}
              {asCode && !done && (
                <form onSubmit={onSubmitCode} className="flex w-full flex-col items-center gap-1.5">
                  <div className="w-full pb-1">
                    <CodeField label={t("stepup.code")} value={code} onChange={onCode} hideLabel autoFocus />
                  </div>
                  <Button
                    type="submit"
                    variant="primary"
                    size="lg"
                    full
                    disabled={code.length < 6}
                    className={cx("h-12! gap-2.5! rounded-xl! text-[15px]! data-disabled:opacity-40!", waiting && "pointer-events-none")}
                  >
                    {waiting && <Spinner />}
                    {waiting ? t("auth.checking") : t("stepup.confirm")}
                  </Button>
                  <ConfirmRow other={other} onSwap={onSwap} swapLabel={t("win.confirm.byPasskey")} onCancel={onCancel} />
                </form>
              )}
            </div>
          </Dialog.Popup>
        </Dialog.Viewport>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

/** The two equal quiet buttons under the main one (.cfm-r): the other way and cancel; alone, cancel takes the whole row. */
function ConfirmRow({ other, onSwap, swapLabel, onCancel }: { other: boolean; onSwap: () => void; swapLabel: string; onCancel: () => void }) {
  const t = useT();
  return (
    <div className={cx("mt-0.5 grid w-full gap-2", other ? "grid-cols-2" : "grid-cols-1")}>
      {other && (
        <Button type="button" variant="ghost" size="lg" className={ghost} onClick={onSwap}>
          {swapLabel}
        </Button>
      )}
      <Button type="button" variant="ghost" size="lg" className={ghost} onClick={onCancel}>
        {t("common.cancel")}
      </Button>
    </div>
  );
}
