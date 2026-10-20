import type { Limiter } from "./limiter";
import type { NodeLink } from "./nodelink";
import type { PanelLink } from "./panellink";
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
  /** SHA-256 of the one-time owner recovery token. Set by `mistgate edge reset-login`; offered for 30 minutes on first start. */
  RECOVERY_TOKEN_SHA256?: string;
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

/**
 * What `init` reads from the environment. The single script has all of it; a role script (src/roles) has the part it was
 * given a binding for, so everything but the database and the key is optional: a role without ASSETS reads no files, one
 * without LIMITER refuses every guarded request (the panel fails closed on a rejected `limit`), one without NODELINK
 * has nobody to ask or poke.
 */
export type PanelEnv = Pick<Env, "DB" | "MASTER_KEY"> & Partial<Omit<Env, "DB" | "MASTER_KEY">>;

/** What a NodeLink object needs of its script's environment: the panel's link entrypoint, when it is not the Worker's own. */
export interface NodeLinkEnv {
  /**
   * The Go side of the link, bound as a service with `entrypoint = "PanelLink"` (the link role's script). Without it the
   * object falls back to the entrypoint of its own script (`ctx.exports.PanelLink`), which is the single-script layout.
   */
  PANEL_LINK?: Service<PanelLink>;
}

/**
 * The environment of the FRONT script (src/front.ts): the router of the split edition. It has no wasm and no secret. It
 * reads three settings from the database and passes each request to a role through a service binding; it owns the
 * Limiter and NodeLink objects (their classes are exported from it), so these never move.
 */
export interface FrontEnv extends NodeLinkEnv {
  DB: D1Database;
  /** The subscriptions, the user page and the decoy: everything that is not the admin or the link. */
  PUBLIC: Fetcher;
  /** The admin SPA, its API, sign-in and the first-run setup. Also what a request goes to while there is no instance. */
  ADMIN: Fetcher;
  /** The agent link: enrolment and the WebSocket upgrade that ends in a NodeLink object. */
  LINK: Fetcher;
  LIMITER: DurableObjectNamespace<Limiter>;
  NODELINK: DurableObjectNamespace<NodeLink>;
}

// The environments of the role scripts of the split edition (src/roles). The names are the binding names of the plan's table;
// each script gets only the bindings listed, which is also why each interface is smaller than Env. The Go roles all hold the
// same D1 database and MASTER_KEY: D1 has no per-table rights, so this narrows what a role can reach, not what it may read.

/** `<name>-public`: subscriptions, the user page, devices, logo and the decoy. The hot path. */
export interface PublicEnv {
  DB: D1Database;
  /** The user page (sub.html) only. */
  ASSETS: Fetcher;
  LIMITER: DurableObjectNamespace<Limiter>;
  /** The FRONT script's NodeLink class, by script_name: first credentials and quota changes poke the node. */
  NODELINK: DurableObjectNamespace<NodeLink>;
  MASTER_KEY: string;
}

/** `<name>-link`: the Go side of the agent link. No assets and no route: only FRONT and the NodeLink objects reach it. */
export interface LinkEnv {
  DB: D1Database;
  LIMITER: DurableObjectNamespace<Limiter>;
  /** The FRONT script's NodeLink class: the upgrade the panel marks is forwarded to the object of its node. */
  NODELINK: DurableObjectNamespace<NodeLink>;
  MASTER_KEY: string;
}

/** `<name>-admin`: the single script's environment minus the cron, with the Provisioner bound from `<name>-provision`. Creates the instance. */
export type AdminEnv = Env;

/** `<name>-jobs`: the cron. No assets, no limiter: nothing it runs is a guarded request. */
export interface JobsEnv {
  DB: D1Database;
  NODELINK: DurableObjectNamespace<NodeLink>;
  /** Woken while an SSH install is queued (see Env.PROVISIONER). */
  PROVISIONER?: DurableObjectNamespace<Provisioner>;
  MASTER_KEY: string;
}
