import { QueryCache, QueryClient, queryOptions } from "@tanstack/react-query";
import { assetUrl, auth, isUnauthenticated } from "./api";
import { isTransient } from "./errors";

// The shell polls every 10 s (no websockets yet). A poll that finds the session gone sends the admin to
// the sign-in screen; the router registers how, so this module does not import it.
let onSignedOut: () => void = () => {};
export const setSignedOutHandler = (fn: () => void) => {
  onSignedOut = fn;
};

export const queryClient = new QueryClient({
  queryCache: new QueryCache({
    onError: (e, query) => {
      // "me" is asked on purpose by the route guards and answers Unauthenticated when signed out.
      if (query.queryKey[0] !== "me" && isUnauthenticated(e)) onSignedOut();
    },
  }),
  defaultOptions: {
    // A verdict from the server (denied, not found, bad request) does not change by asking again; a dropped connection may.
    queries: { retry: (count, e) => count < 2 && isTransient(e), staleTime: 5_000 },
  },
});

// The signed-in admin. Unauthenticated means "go to /login", so never retry it.
export const meQuery = queryOptions({
  queryKey: ["me"],
  queryFn: () => auth.me({}),
  retry: false,
  staleTime: Infinity,
});

/** Forget the cached session so the next route guard asks the server again. */
export function forgetSession() {
  queryClient.removeQueries({ queryKey: meQuery.queryKey });
  // whether setup is open or a password admin exists changes with who just signed in or out
  void queryClient.invalidateQueries({ queryKey: ["login-info"] });
}

/**
 * Goes to the sign-in screen with a full page load, not a router transition. The admin CSP allows Cloudflare's
 * Turnstile script only while the check is on, and a document keeps the CSP it was loaded with: a page that
 * was loaded before the owner switched the check on could not draw the widget.
 */
export function goToSignIn() {
  window.location.assign(assetUrl("login"));
}

/** Ends the session, then goes to the sign-in screen even if the server call failed. */
export function useSignOut() {
  return async () => {
    try {
      await auth.logout({});
    } finally {
      goToSignIn();
    }
  };
}
