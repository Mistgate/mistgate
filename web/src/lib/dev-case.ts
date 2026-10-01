/**
 * Development only: `?case=nopasskey|badtotp|locked` forces an auth screen into a state that is hard
 * to reach by hand (no WebAuthn, a wrong code, a lockout) so it can be looked at. Always null in a
 * production build, where the bundler drops the branches that read it.
 */
export const devCase: string | null = import.meta.env.DEV ? new URLSearchParams(location.search).get("case") : null;
