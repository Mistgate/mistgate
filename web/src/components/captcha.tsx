import { useQuery } from "@tanstack/react-query";
import { useRef, useState, type ReactNode } from "react";
import { isCaptchaError } from "@/lib/api";
import { loginInfoQuery } from "@/lib/instance";
import { Turnstile, type TurnstileHandle } from "./turnstile";

export type Captcha = {
  /** Turnstile is on for this panel. Off means no widget, no Cloudflare script, and `ready` is always true. */
  on: boolean;
  /** The sign-in form may go ahead: there is no check, or it has passed and its token is fresh. */
  ready: boolean;
  /** The token to send as `turnstile_token` ("" when there is no check). */
  token: string;
  /** A token works once: call this after every call that carried it, whatever the outcome. */
  reset: () => void;
  /** Reloads the page when the panel demands a check this page was loaded without; true if it did. */
  stale: (e: unknown) => boolean;
  /** The 65 px block (null when off). Put it right above the buttons it gates. */
  widget: ReactNode;
};

/**
 * Turnstile for the sign-in and setup screens. GetLoginInfo says whether it is on and gives the site key;
 * the widget is rendered only then, so with it off nothing from Cloudflare is ever requested.
 */
export function useCaptcha(): Captcha {
  const info = useQuery(loginInfoQuery).data;
  const on = !!info?.turnstileEnabled;
  const [token, setToken] = useState<string | null>(null);
  const handle = useRef<TurnstileHandle>(null);
  return {
    on,
    ready: !on || token !== null,
    token: on ? (token ?? "") : "",
    reset: () => handle.current?.reset(),
    // The owner switched Turnstile on after this page loaded: the page has neither the widget nor the CSP
    // exception for it, and only a reload brings both.
    stale: (e) => {
      if (on || !isCaptchaError(e)) return false;
      window.location.reload();
      return true;
    },
    widget: on ? <Turnstile siteKey={info.turnstileSiteKey} onToken={setToken} ref={handle} /> : null,
  };
}
