import type { Limiter } from "./limiter";
import type { NodeLink } from "./nodelink";
import type { Provisioner } from "./provisioner";

export interface Env {
  DB: D1Database;
  /** The admin SPA and the user page (web/dist), read only by the panel through readAsset(). */
  ASSETS: Fetcher;
  /** One Limiter object per (name, key): the panel's security limits (login bursts, page-password tries), shared by all isolates. */
  LIMITER: DurableObjectNamespace<Limiter>;
  /** One NodeLink object per node (idFromName(node id)): the agent's WebSocket and its session state; the Go panel runs the steps. */
  NODELINK: DurableObjectNamespace<NodeLink>;
  /**
   * The Provisioner object of the second script (mistgate-edge-provision, wrangler.provision.example.toml): it holds the SSH
   * sockets of node installs. Without it SSH installs answer 503 and the cron has nobody to wake.
   */
  PROVISIONER?: DurableObjectNamespace<Provisioner>;
  /** 32 random bytes as 64 hex characters (a Worker secret). */
  MASTER_KEY: string;
  /** Optional first-run settings, read by the panel only while the database is empty. */
  PUBLIC_URL?: string;
  ADMIN_HOST?: string;
  ADMIN_PREFIX?: string;
  SUB_PREFIX?: string;
  /**
   * Optional secret: the SHA-256 of a setup token the deployer chose, 64 lowercase hex characters. While no admin exists the
   * panel offers it as the setup token (valid 30 minutes from the first start) instead of printing a random link to the log.
   */
  SETUP_TOKEN_SHA256?: string;
}

/**
 * The environment of the second script (the Provisioner's). It holds the same D1 database and the same MASTER_KEY secret
 * as the main Worker (rotate the key in both), reaches the main script's Limiter and NodeLink objects by binding, and
 * has no assets, no setup token and no first-run settings: the main Worker creates the instance, this one reads it.
 */
export interface ProvisionEnv {
  DB: D1Database;
  LIMITER: DurableObjectNamespace<Limiter>;
  NODELINK: DurableObjectNamespace<NodeLink>;
  PROVISIONER: DurableObjectNamespace<Provisioner>;
  MASTER_KEY: string;
}
