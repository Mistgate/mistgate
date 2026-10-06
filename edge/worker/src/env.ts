import type { Limiter } from "./limiter";

export interface Env {
  DB: D1Database;
  /** The admin SPA and the user page (web/dist), read only by the panel through readAsset(). */
  ASSETS: Fetcher;
  /** One Limiter object per (name, key): the panel's security limits (login bursts, page-password tries), shared by all isolates. */
  LIMITER: DurableObjectNamespace<Limiter>;
  /** 32 random bytes as 64 hex characters (a Worker secret). */
  MASTER_KEY: string;
  /** Optional first-run settings, read by the panel only while the database is empty. */
  PUBLIC_URL?: string;
  ADMIN_HOST?: string;
  ADMIN_PREFIX?: string;
  SUB_PREFIX?: string;
}