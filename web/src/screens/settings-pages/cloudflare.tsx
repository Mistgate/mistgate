import { ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Turnstile } from "@/components/turnstile";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import type { GetSecuritySettingsResponse } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { auth } from "@/lib/api";
import { errorCode, errorText } from "@/lib/errors";
import { plain, type Plain } from "@/lib/plain";

type Stored = Plain<GetSecuritySettingsResponse>;

const securityQuery = queryOptions({
  queryKey: ["security-settings"],
  queryFn: async ({ signal }) => plain(await auth.getSecuritySettings({}, { signal })),
});

// What the server accepts (auth.proto, UpdateSecuritySettingsRequest).
const siteKeyPattern = /^[A-Za-z0-9_-]*$/;
export const maxSiteKey = 128;
export const maxSecret = 256;

/** Why the draft cannot be saved, as a message key, or null when it can. */
export function draftProblem(d: { enabled: boolean; siteKey: string; secret: string }, stored: Pick<Stored, "turnstileSecretSet">) {
  if (!siteKeyPattern.test(d.siteKey) || d.siteKey.length > maxSiteKey) return "cf.siteKeyError" as const;
  if (d.secret.length > maxSecret) return "cf.secretError" as const;
  if (d.enabled && (!d.siteKey || (!stored.turnstileSecretSet && !d.secret))) return "cf.needKeys" as const;
  return null;
}

/** The server's refusals of a key check (UpdateSecuritySettings), as our sentences. */
export const proofErrors: Record<string, MessageKey> = {
  turnstile_secret_rejected: "cf.err.secret",
  turnstile_token_rejected: "cf.err.token",
  turnstile_unreachable: "cf.err.unreachable",
  turnstile_test_required: "cf.err.required",
};

/**
 * Whether this document may load Cloudflare's script. The server's CSP allows it while the check is on, and on the
 * Security page itself when that page is loaded afresh (the one place where new keys are tried with the check off).
 */
export function cloudflareAllowed(checkOn: boolean, served = servedPath()) {
  return checkOn || served === new URL("settings/security", document.baseURI).pathname;
}

/** The path this document was loaded from (the router changes location.pathname, not the document's CSP). */
function servedPath() {
  const nav = typeof performance.getEntriesByType === "function" ? performance.getEntriesByType("navigation")[0] : undefined;
  return new URL(nav?.name || location.href).pathname;
}

/** Settings -> Security -> "Sign-in": the Cloudflare check. Owner only (the server refuses everyone else). */
export function CloudflareCard() {
  const q = useQuery(securityQuery);
  // "?turnstile=on": the page was opened afresh to switch the check on (see cloudflareAllowed), so the switch starts on
  const [startOn] = useState(() => new URLSearchParams(location.search).get("turnstile") === "on");
  useEffect(() => {
    if (startOn) history.replaceState(history.state, "", location.pathname + location.hash);
  }, [startOn]);
  if (!q.data)
    // the card keeps its place while it loads and when the load fails, with the reason and "Try again"
    return (
      <section className="rounded-card-lg border border-line bg-surface px-4 py-1">
        <CardHead />
        {q.isError ? <QueryError compact className="border-t border-line py-3" error={q.error} onRetry={() => void q.refetch()} /> : <Pending compact className="border-t border-line py-3.5" />}
      </section>
    );
  const s = q.data;
  // a saved change starts the draft over from what the server now holds
  return <CloudflareForm key={`${s.turnstileEnabled}|${s.turnstileSiteKey}|${s.turnstileSecretSet}`} stored={s} startOn={startOn} />;
}

function CardHead() {
  const t = useT();
  return (
    <div className="flex min-h-12 items-center py-2">
      <SectionLabel as="h2" icon="shield" tone="lavender">
        {t("cf.card")}
      </SectionLabel>
    </div>
  );
}

type Update = { turnstileEnabled?: boolean; turnstileSiteKey?: string; turnstileSecretKey?: string; turnstileTestToken?: string };

function CloudflareForm({ stored, startOn }: { stored: Stored; startOn: boolean }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const [enabled, setEnabled] = useState(stored.turnstileEnabled || startOn);
  const [siteKey, setSiteKey] = useState(stored.turnstileSiteKey);
  const [secret, setSecret] = useState("");
  // the check of new keys: the widget runs with the new site key, and the server tries its token with the new secret
  const [proving, setProving] = useState(false);
  const [proofError, setProofError] = useState<string | null>(null);
  const [removing, setRemoving] = useState(false);

  const dirty = enabled !== stored.turnstileEnabled || siteKey !== stored.turnstileSiteKey || secret !== "";
  const problem = draftProblem({ enabled, siteKey, secret }, stored);
  // keys that leave the check on are saved only with a token they made (the server insists, see UpdateSecuritySettings)
  const needsProof = enabled && (!stored.turnstileEnabled || siteKey !== stored.turnstileSiteKey || secret !== "");

  const save = useMutation({
    mutationFn: (req: Update) => guard(() => auth.updateSecuritySettings(req)),
    onSuccess: (r, req) => {
      setProving(false);
      setRemoving(false);
      qc.setQueryData(securityQuery.queryKey, plain(r));
      // the sign-in page reads its Turnstile settings from the public login info
      void qc.invalidateQueries({ queryKey: ["login-info"] });
      toast(req.turnstileSecretKey === "" ? t("cf.removed") : r.turnstileEnabled ? t("cf.saved") : t("cf.savedOff"));
    },
    onError: (e) => {
      setProving(false); // a token works once: the next try is a fresh check
      if (isStepUpCancelled(e)) return;
      const key = proofErrors[errorCode(ConnectError.from(e).rawMessage)];
      if (key) setProofError(t(key));
      else toast.error(errorText(e, t));
    },
  });

  /** An edit starts the check over: it has to run with the keys that are saved. */
  function edited() {
    setProving(false);
    setProofError(null);
  }

  function draft(token?: string): Update {
    return {
      turnstileEnabled: enabled !== stored.turnstileEnabled ? enabled : undefined,
      turnstileSiteKey: siteKey !== stored.turnstileSiteKey ? siteKey : undefined,
      turnstileSecretKey: secret ? secret : undefined,
      turnstileTestToken: token,
    };
  }

  function toggle(on: boolean) {
    // the check needs Cloudflare's script, which this page may not load: open the Security page afresh, switched on
    if (on && !cloudflareAllowed(stored.turnstileEnabled)) {
      location.assign(new URL("settings/security?turnstile=on", document.baseURI));
      return;
    }
    setEnabled(on);
    edited();
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (!dirty || problem || save.isPending) return;
    setProofError(null);
    if (needsProof) setProving(true);
    else save.mutate(draft());
  }

  return (
    <section className="rounded-card-lg border border-line bg-surface px-4 py-1">
      <CardHead />
      <form onSubmit={submit}>
        <label className="flex min-h-[56px] cursor-pointer items-center gap-3 border-t border-line">
          <span className="flex min-w-0 flex-1 flex-col gap-0.5">
            <span className="text-[13px] font-bold">{t("cf.title")}</span>
            <span className="text-[11px] leading-snug text-muted">{t("cf.hint")}</span>
          </span>
          <Switch checked={enabled} onCheckedChange={toggle} />
        </label>
        {enabled && (
          <div className="screen-enter flex flex-col gap-3.5 border-t border-line py-3.5">
            <div className="grid gap-3.5 md:grid-cols-2">
              <TextField
                label={t("cf.siteKey")}
                value={siteKey}
                onChange={(e) => {
                  setSiteKey(e.target.value.trim());
                  edited();
                }}
                maxLength={maxSiteKey}
                mono
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
                error={siteKeyPattern.test(siteKey) ? undefined : t("cf.siteKeyError")}
                hint={t("cf.siteKeyHint")}
              />
              <TextField
                label={
                  <span className="flex items-center gap-2">
                    {t("cf.secretKey")}
                    <span className={stored.turnstileSecretSet ? "tone-ok tone-text" : "text-faint"}>· {stored.turnstileSecretSet ? t("cf.secretSet") : t("cf.secretNotSet")}</span>
                  </span>
                }
                type="password"
                className="[&_input::placeholder]:font-sans"
                value={secret}
                onChange={(e) => {
                  setSecret(e.target.value);
                  edited();
                }}
                maxLength={maxSecret}
                mono
                autoComplete="new-password"
                placeholder={stored.turnstileSecretSet ? t("cf.secretKeep") : t("cf.secretPaste")}
                hint={t("cf.secretHint")}
              />
            </div>
            <Notice>{t("cf.warn")}</Notice>
            {stored.turnstileSecretSet && (
              <div>
                <Button variant="ghostDanger" size="sm" disabled={save.isPending} onClick={() => setRemoving(true)}>
                  {t("cf.secretRemove")}
                </Button>
              </div>
            )}
          </div>
        )}
        {proving && (
          <div className="screen-enter flex flex-col gap-2.5 border-t border-line py-3.5">
            <p className="text-xs leading-normal text-pretty text-muted">{save.isPending ? t("cf.proving") : t("cf.proveHint")}</p>
            <Turnstile
              key={siteKey}
              siteKey={siteKey}
              onToken={(token) => token && !save.isPending && save.mutate(draft(token))}
              // 1101xx: the site key is unknown; 1102xx: it does not allow this domain
              onFail={(code) => code.startsWith("110") && setProofError(t("cf.err.sitekey"))}
            />
          </div>
        )}
        {proofError && <Notice tone="danger" className="mb-3">{proofError}</Notice>}
        {(dirty || problem) && (
          <div className="screen-enter flex items-center gap-3 border-t border-line py-2.5">
            <span className={problem === "cf.needKeys" ? "min-w-0 flex-1 text-xs text-muted" : "min-w-0 flex-1 text-xs text-danger-text"}>
              {problem && problem !== "cf.siteKeyError" ? t(problem) : ""}
            </span>
            {proving ? (
              <Button variant="ghost" size="md" disabled={save.isPending} onClick={() => setProving(false)}>
                {t("common.cancel")}
              </Button>
            ) : (
              <Button type="submit" variant="primary" size="md" disabled={!dirty || !!problem || save.isPending}>
                {needsProof ? (stored.turnstileEnabled ? t("cf.proveSave") : t("cf.prove")) : t("common.save")}
              </Button>
            )}
          </div>
        )}
      </form>
      {removing && (
        <Modal
          open
          onOpenChange={(o) => !o && !save.isPending && setRemoving(false)}
          title={t("cf.removeTitle")}
          description={t("cf.removeBody")}
          footer={
            <>
              <Button variant="ghost" size="md" disabled={save.isPending} onClick={() => setRemoving(false)}>
                {t("common.cancel")}
              </Button>
              <Button variant="danger" size="md" disabled={save.isPending} onClick={() => save.mutate({ turnstileSecretKey: "" })}>
                {t("cf.secretRemove")}
              </Button>
            </>
          }
        />
      )}
    </section>
  );
}
