import { Code, ConnectError } from "@connectrpc/connect";
import { createContext, use, useCallback, useRef, useState, type FormEvent, type ReactNode } from "react";
import { CodeField } from "@/components/auth-parts";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { StepUpRequiredSchema, type StepUpRequired } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import { auth, webauthnSupported } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { getPasskey } from "@/lib/passkey";

// Step-up re-authentication (auth.proto, BeginStepUp): adding or removing a passkey and ending other
// sessions need a sign-in factor proved in the last 5 minutes. The server answers PERMISSION_DENIED with a
// StepUpRequired detail; `guard` catches that, asks the admin for a passkey or an authenticator code in a
// dialog, and runs the call again. Wrap each RPC on its own (not a whole flow), so a second WebAuthn
// prompt never follows a retry.

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

type Guard = <T>(call: () => Promise<T>) => Promise<T>;
const GuardContext = createContext<Guard>((call) => call());

/** `const guard = useStepUp(); await guard(() => auth.removePasskey({ id }))`. */
export const useStepUp = () => use(GuardContext);

type Ask = { need: StepUpRequired; done: () => void; cancel: () => void };

export function StepUpProvider({ children }: { children: ReactNode }) {
  const [ask, setAsk] = useState<Ask | null>(null);
  // calls that fail at the same moment share one dialog
  const pending = useRef<Promise<void> | null>(null);

  const guard = useCallback<Guard>(async (call) => {
    try {
      return await call();
    } catch (e) {
      const need = stepUpNeeded(e);
      if (!need) throw e;
      pending.current ??= new Promise<void>((resolve, reject) => setAsk({ need, done: resolve, cancel: () => reject(new StepUpCancelled()) })).finally(() => {
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
      {ask && <StepUpDialog need={ask.need} onDone={ask.done} onCancel={ask.cancel} />}
    </GuardContext>
  );
}

function StepUpDialog({ need, onDone, onCancel }: { need: StepUpRequired; onDone: () => void; onCancel: () => void }) {
  const t = useT();
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState<"passkey" | "code" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const passkey = need.passkey && webauthnSupported();

  async function run(kind: "passkey" | "code", confirm: () => Promise<unknown>) {
    setBusy(kind);
    setError(null);
    try {
      await confirm();
      onDone();
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
  const withCode = (e: FormEvent) => {
    e.preventDefault();
    if (!busy && code.length === 6) void run("code", () => auth.finishStepUp({ totpCode: code }));
  };

  return (
    <Modal open onOpenChange={(o) => !o && !busy && onCancel()} title={t("stepup.title")} description={t("stepup.body")}>
      <div className="flex flex-col gap-3.5">
        {error && <Notice tone="danger">{error}</Notice>}
        {passkey && (
          <Button variant="primary" size="lg" full disabled={!!busy} onClick={() => void withPasskey()}>
            {busy === "passkey" ? t("auth.waitingPasskey") : t("stepup.passkey")}
          </Button>
        )}
        {need.totp && (
          <form onSubmit={withCode} className="flex flex-col gap-3">
            <CodeField label={t("stepup.code")} value={code} onChange={setCode} />
            <Button type="submit" variant={passkey ? "secondary" : "primary"} size="lg" full disabled={!!busy || code.length < 6}>
              {t("stepup.confirm")}
            </Button>
          </form>
        )}
        <div className="flex justify-end">
          <Button variant="ghost" size="md" disabled={!!busy} onClick={onCancel}>
            {t("common.cancel")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
