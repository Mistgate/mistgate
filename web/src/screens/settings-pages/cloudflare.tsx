import { ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Turnstile } from "@/components/turnstile";
import { Button } from "@/components/ui/button";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import type { GetSecuritySettingsResponse } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { auth } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorCode, errorText } from "@/lib/errors";
import { plain, type Plain } from "@/lib/plain";
import { Foot, ICONS, Mono, Pb, Sec, slots, Spin, Swr } from "./sx-bits";
import { SxWindow } from "./sx-dlg";

type Stored = Plain<GetSecuritySettingsResponse>;

export const securityQuery = queryOptions({
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

/** What a scene says about the card: the window that removes the secret key, and the proof of new keys (drawn without Cloudflare's own script). */
export type CloudflareDemo = { removing?: boolean; proving?: boolean };

/**
 * Settings -> Security -> "Sign-in" (the board's third card): the Cloudflare check at the sign-in form. Owner only (the server refuses
 * everyone else). The switch, then the keys; new keys are tried with a check of Cloudflare's own before they are saved.
 */
export function CloudflareCard({ demo }: { demo?: CloudflareDemo }) {
  const t = useT();
  const q = useQuery(securityQuery);
  // "?turnstile=on": the page was opened afresh to switch the check on (see cloudflareAllowed), so the switch starts on
  const [startOn] = useState(() => new URLSearchParams(location.search).get("turnstile") === "on");
  useEffect(() => {
    if (startOn) history.replaceState(history.state, "", location.pathname + location.hash);
  }, [startOn]);
  if (!q.data)
    // the card keeps its place while it loads and when the load fails, with the reason and "Try again"
    return (
      <Sec tone="sky" d={ICONS.cloud} title={t("cf.card")}>
        <div className="sx-pad">{q.isError ? <QueryError compact error={q.error} onRetry={() => void q.refetch()} /> : <Pending compact />}</div>
      </Sec>
    );
  const s = q.data;
  // a saved change starts the draft over from what the server now holds
  return <CloudflareForm key={`${s.turnstileEnabled}|${s.turnstileSiteKey}|${s.turnstileSecretSet}`} stored={s} startOn={startOn} demo={demo} />;
}

type Update = { turnstileEnabled?: boolean; turnstileSiteKey?: string; turnstileSecretKey?: string; turnstileTestToken?: string };

function CloudflareForm({ stored, startOn, demo }: { stored: Stored; startOn: boolean; demo?: CloudflareDemo }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const [enabled, setEnabled] = useState(stored.turnstileEnabled || startOn);
  const [siteKey, setSiteKey] = useState(stored.turnstileSiteKey);
  const [secret, setSecret] = useState(demo?.proving ? "not-a-real-secret" : "");
  // the check of new keys: the widget runs with the new site key, and the server tries its token with the new secret
  const [proving, setProving] = useState(!!demo?.proving);
  const [proofError, setProofError] = useState<string | null>(null);
  const [removing, setRemoving] = useState(!!demo?.removing);

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

  // the board's yellow line: the check is on, and nothing yet to ask Cloudflare with
  const noSecret = enabled && !stored.turnstileSecretSet && !secret;
  const siteOk = siteKeyPattern.test(siteKey);
  return (
    <Sec tone="sky" d={ICONS.cloud} title={t("cf.card")} foot={slots(t("sx.cf.foot"), { cmd: <Mono>mistgate auth turnstile off</Mono> })}>
      <form onSubmit={submit}>
        <Swr label={t("cf.title")} hint={t("cf.hint")}>
          <Switch className="fz-sw" checked={enabled} onCheckedChange={toggle} aria-label={t("cf.title")} />
        </Swr>
        {enabled && (
          <div className="sx-form">
            {noSecret && <Pb tone="part">{t("sx.cf.nosecret")}</Pb>}
            <label className={cx("an-f", !siteOk && "err")}>
              <span>{t("cf.siteKey")}</span>
              <input
                className="an-i mono"
                type="text"
                value={siteKey}
                maxLength={maxSiteKey}
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
                aria-invalid={!siteOk || undefined}
                onChange={(e) => {
                  setSiteKey(e.target.value.trim());
                  edited();
                }}
              />
              <small>{siteOk ? t("cf.siteKeyHint") : t("cf.siteKeyError")}</small>
            </label>
            <div className="an-f">
              <span id="sx-cf-sl">{stored.turnstileSecretSet ? `${t("cf.secretKey")} · ${t("cf.secretSet")}` : t("cf.secretKey")}</span>
              <input
                aria-labelledby="sx-cf-sl"
                className="an-i mono"
                type="password"
                value={secret}
                maxLength={maxSecret}
                autoComplete="new-password"
                placeholder={stored.turnstileSecretSet ? t("cf.secretKeep") : "0x4AAA…"}
                onChange={(e) => {
                  setSecret(e.target.value);
                  edited();
                }}
              />
              <small>{t("cf.secretHint")}</small>
              {stored.turnstileSecretSet && (
                <button type="button" className="an-lk ig-lk sx-lkl" disabled={save.isPending} onClick={() => setRemoving(true)}>
                  {t("cf.secretRemove")}
                </button>
              )}
            </div>
            {proving && (
              <div className="sx-proof">
                <p>{save.isPending ? t("cf.proving") : t("cf.proveHint")}</p>
                {demo?.proving ? (
                  <div className="an-code">
                    <code className="mono">{t("cf.idle")}</code>
                  </div>
                ) : (
                  <Turnstile
                    key={siteKey}
                    siteKey={siteKey}
                    onToken={(token) => token && !save.isPending && save.mutate(draft(token))}
                    // 1101xx: the site key is unknown; 1102xx: it does not allow this domain
                    onFail={(code) => code.startsWith("110") && setProofError(t("cf.err.sitekey"))}
                  />
                )}
              </div>
            )}
            {proofError && <Pb tone="bad">{proofError}</Pb>}
          </div>
        )}
        {(dirty || problem) && (
          <Foot className="sx-tk">
            <em className={cx(problem && problem !== "cf.needKeys" && problem !== "cf.siteKeyError" && "bad")}>{problem && problem !== "cf.siteKeyError" ? t(problem) : ""}</em>
            {proving ? (
              <Button size="md" disabled={save.isPending} onClick={() => setProving(false)}>
                {t("common.cancel")}
              </Button>
            ) : (
              <Button type="submit" variant="primary" size="md" disabled={!dirty || !!problem || save.isPending}>
                {save.isPending && <Spin />}
                {needsProof ? (stored.turnstileEnabled ? t("cf.proveSave") : t("cf.prove")) : t("common.save")}
              </Button>
            )}
          </Foot>
        )}
      </form>
      {removing && (
        <SxWindow
          open
          onOpenChange={(o) => !o && !save.isPending && setRemoving(false)}
          title={t("cf.removeTitle")}
          shot="cloudflare-secret-remove"
          footer={
            <>
              <Button size="lg" disabled={save.isPending} onClick={() => setRemoving(false)}>
                {t("common.cancel")}
              </Button>
              <Button variant="primary" size="lg" className="sx-dng" disabled={save.isPending} onClick={() => save.mutate({ turnstileSecretKey: "" })}>
                {t("cf.secretRemove")}
              </Button>
            </>
          }
        >
          <p className="sx-dp">{t("cf.removeBody")}</p>
        </SxWindow>
      )}
    </Sec>
  );
}
