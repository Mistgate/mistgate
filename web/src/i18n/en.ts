// English is the source of truth: ru.ts is type-checked against this shape.
// Use {name} placeholders for values; no hard-coded text in components. A message with several forms
// separated by "|" is a plural (see tn() in index.ts).
import { en as opsEn } from "./ops";
import { en as profilesEn } from "./profiles";
import { en as usersEn } from "./users";
import { en as securityEn } from "./security";
import { en as subsEn } from "./subs";
import { en as healthEn } from "./health";
import { en as updatesEn } from "./updates";
import { en as awgEn } from "./awg";
import { en as warpEn } from "./warp";
import { en as integrationsEn } from "./integrations";

// The shell, auth and navigation copy. The screens' own copy lives next to their owners: ops.ts (overview, nodes,
// settings), users.ts and profiles.ts; `en` below is all of it, so `MessageKey` covers every key.
const core = {
  "app.name": "Mistgate",
  "common.loading": "Loading…",
  "common.retry": "Try again",
  "common.undo": "Undo",

  "nav.overview": "Overview",
  "nav.nodes": "Nodes",
  "nav.users": "Users",
  "nav.profiles": "Profiles",
  "nav.subscriptions": "Subscriptions",
  "nav.health": "Health",
  "nav.updates": "Updates",
  "nav.integrations": "Integrations",
  "nav.settings": "Settings",
  "nav.more": "More",
  "nav.main": "Main navigation",
  "nav.skip": "Skip to content",
  "nav.group.setup": "SETUP",
  "nav.group.ops": "OPERATIONS",

  "section.overview.desc": "Fleet health at a glance, node cards, traffic and the latest events.",
  "section.nodes.desc": "Servers in your fleet, their protocols, load and the wizard to add a new one.",
  "section.users.desc": "People with access: apps, devices, traffic quotas and expiry dates.",
  "section.profiles.desc": "Ready-made protocol setups that you roll out to nodes and users.",
  "section.subscriptions.desc": "Subscription formats, delivery rules, mirrors and the page users see.",
  "section.health.desc": "Alerts, client-eye checks and the fleet doctor.",
  "section.updates.desc": "Panel and node agent versions, staged rollouts and rollback.",
  "section.integrations.desc": "API tokens for scripts, MCP for AI agents and the changes they ask you to approve.",
  "section.settings.desc": "Admins, security, backups and domains.",
  "section.empty": "Nothing here yet",

  "header.search": "Search nodes, users and sections",
  "header.language": "Language",
  "header.theme": "Theme",
  "header.theme.toLight": "Switch to light theme",
  "header.theme.toDark": "Switch to dark theme",
  "header.signOut": "Sign out",
  "header.version": "Panel version {version}",
  "header.source": "Source code",
  "more.theme": "Dark theme",
  "more.language": "Language",

  "health.none": "No nodes",
  "health.ok": "Fleet is healthy",
  "health.okShort": "All good",
  "health.problems": "{n} problem|{n} problems",

  "palette.title": "Command palette",
  "palette.placeholder": "Node, user, section…",
  "palette.empty": "Nothing found",
  "palette.searching": "Searching…",
  "palette.nodes": "NODES",
  "palette.users": "USERS",
  "palette.places": "SECTIONS",
  "palette.actions": "ACTIONS",
  "palette.addNode": "Add node",
  "palette.createUser": "New user",
  "palette.newToken": "New API token",
  "palette.newProfile": "New profile",
  "palette.approvals": "Approvals",

  "role.owner": "Owner",
  "role.helper": "Helper",
  "role.readonly": "Read-only",
  "role.unknown": "Admin",

  "status.ok": "Healthy",
  "status.warn": "Attention",
  "status.bad": "Broken",
  "status.off": "Off",
  "status.busy": "Updating",
  "status.blip": "Host blip",

  "settings.interface": "Interface",
  "settings.system": "System",
  "settings.admins": "Admins",
  "settings.security": "Security",
  "settings.sessions": "Sessions",
  "settings.audit": "Audit",
  "settings.backups": "Backups",
  "settings.domains": "Domains",
  "settings.nav": "Settings sections",
  "settings.language": "Language",
  "settings.theme": "Theme",
  "settings.theme.dark": "Dark",
  "settings.theme.light": "Light",
  "settings.accent": "Accent colour",
  "settings.accent.follow": "Like the panel",
  "settings.accent.own": "This device has a colour of its own",
  "settings.accent.reset": "Reset",
  "settings.accent.custom": "Custom",
  "settings.accent.mint": "Mint",
  "settings.accent.lavender": "Lavender",
  "settings.accent.sky": "Sky",
  "settings.accent.sand": "Sand",
  "settings.accent.rose": "Rose",
  "settings.accent.sage": "Sage",
  "settings.local": "These choices are kept on this device only.",
  "settings.device": "Only on this device",

  "auth.setup.welcomeTitle": "Hi!",
  "auth.setup.welcomeBody": "This is {brand} — a panel for your VPN servers. Pick a language first; you can change it later.",
  "auth.next": "Next",
  "auth.setup.adminTitle": "Create the admin",
  "auth.setup.adminBody":
    "A passkey is best: sign in with a fingerprint or Face ID, no passwords. The key lives only on your device.",
  "auth.login": "LOGIN",
  "auth.password": "PASSWORD",
  "auth.code": "AUTHENTICATOR CODE",
  "auth.createPasskey": "Create passkey",
  "auth.waitingPasskey": "Waiting for your device…",
  "auth.setup.passkeyHint": "The browser will ask for a fingerprint, face or PIN",
  "auth.setup.otherWay": "password + authenticator code",
  "auth.setup.totpScan": "Scan with Google Authenticator, 1Password or Aegis",
  "auth.setup.createAdmin": "Create admin",
  "auth.setup.doneTitle": "Done",
  "auth.setup.doneBody": "Admin created. What next?",
  "auth.setup.addFirst": "Add your first node",
  "auth.setup.addFirstBody": "You’ll need a server with Ubuntu or Debian and root over SSH. The panel gives you the install command.",
  "auth.setup.later": "Later, let me look around first",
  "auth.login.title": "Sign in to the panel",
  "auth.login.body": "admin · {host}",
  "auth.login.passkey": "Sign in with passkey",
  "auth.login.passkeyHint": "Fingerprint, Face ID or a security key",
  "auth.login.otherWay": "another way",
  "auth.hideOtherWay": "hide",
  "auth.login.submit": "Sign in",
  "auth.checking": "Checking…",
  "auth.noPasskeyTitle": "This browser can’t do passkeys.",
  "auth.noPasskeyBody": "Sign in with password and code — or open the panel in a recent Safari, Chrome or Firefox.",
  "auth.badCode": "That code didn’t work. Make sure the phone clock is set automatically.",
  "auth.attemptsLeft": "{n} attempt left|{n} attempts left",
  "auth.lockedTitle": "Too many attempts",
  "auth.lockedBody":
    "Sign-in is locked for 15 minutes. The owner got a Telegram bot alert — if it wasn’t you, they’ll see the IP and time.",
  "auth.missingToken.title": "This setup link is incomplete",
  "auth.missingToken.body":
    "Open the full link printed by `mistgate setup`, including the part after the # sign. If it has expired, run the command again.",
  "auth.toLogin": "Go to sign in",
  "auth.step": "Step {n} of {total}",

  "err.unsupported":
    "This browser cannot use passkeys. Open the panel over HTTPS in a current Chrome, Safari, Firefox or Edge.",
  "err.cancelled": "The passkey request was cancelled or timed out. Try again when you are ready.",
  "err.alreadyRegistered": "This device already has a passkey for this panel.",
  "err.origin":
    "The browser refused the passkey for this address. The panel domain has to match its WebAuthn settings.",
  "err.setupLink": "The setup link is invalid or has expired. Run `mistgate setup` to get a new one.",
  "err.notRecognised": "That passkey is not registered with this panel.",
  "err.rateLimited": "Too many attempts. Wait a minute and try again.",
  "err.captcha": "The Cloudflare check did not pass. Wait for it to finish and try again.",
  "err.network": "Cannot reach the panel. Check the connection and try again.",
  "err.generic": "Something went wrong. Try again.",
  "err.guard": "Cannot load the panel",

  // The panel's refusal codes (src/lib/errors.ts errorCodes): what happened and what to do.
  "err.name_taken": "This name is taken. Pick another one.",
  "err.group_not_empty": "Users still in this group: {users}. Move them to another group, then delete it.",
  "err.profile_deployed": "The profile is on nodes {nodes}. Remove it from them first.",
  "err.stale_version": "The profile was changed in another tab. Reload the page.",
  "err.node_offline": "The node is not connected. Try again when its agent is back.",
  "err.agent_too_old": "The node’s agent is too old for this. Update it on the Updates page.",
  "err.node_retired": "The node is retired from the fleet.",
  "err.sub_address_missing": "The subscription link address is not set. `mistgate setup` sets it on the panel’s server.",
  "err.preset_is_default": "This is the default preset. Make another one the default first.",
  "err.preset_builtin": "A built-in preset cannot be deleted.",

  // A profile field the server refused: "field.<path>.<code>" of its FieldError; a pair without an entry shows the server’s text.
  "field.port.out_of_range": "The port is 1 to 65535",
  "field.hop.from.invalid": "The port must be outside the hopping range",
  "field.hop.from.out_of_range": "The range starts between 1024 and 65535, clear of SSH and other well-known ports",
  "field.hop.to.too_wide": "The range holds at most 20,000 ports",
  "field.obfs.password.length": "The password is 8 to 128 characters",
  "field.sni.invalid": "This doesn’t look like a domain",
};

export type CoreMessages = typeof core;
export const en = { ...core, ...opsEn, ...usersEn, ...securityEn, ...profilesEn, ...subsEn, ...healthEn, ...updatesEn, ...awgEn, ...warpEn, ...integrationsEn };

export type Messages = typeof en;
export type MessageKey = keyof Messages;
