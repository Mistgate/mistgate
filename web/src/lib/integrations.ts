import { Code, ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import type { StatusKind } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { ApprovalState, TokenProfile, type ListApiTokensResponse, type ListApprovalsResponse } from "@/gen/mistgate/admin/v1/integrations_pb";
import { useT, type T } from "@/i18n";
import { en, type MessageKey } from "@/i18n/en";
import { apiTokens, approvals } from "./api";
import { errorText } from "./errors";
import type { Fmt } from "./format";
import { plain, type Plain } from "./plain";
import { useIsOwner } from "./updates";

// Everything the Integrations screen and the sidebar badge read, plus the small pure helpers they share (token state,
// profile wording, the countdown of an approval, the words of a fact). The server sends keys and the panel's own short
// English facts (integrations.proto); the wording is in i18n/integrations.ts. Only the owner may call any of this.

export type Tokens = Plain<ListApiTokensResponse>;
export type Token = Tokens["tokens"][number];
/** `receivedMs` is the browser clock at the moment the answer arrived: with the server's `nowUnix` it makes a countdown that ignores a wrong local clock. */
export type Approvals = Plain<ListApprovalsResponse> & { receivedMs: number };
export type Approval = Approvals["approvals"][number];
export type Fact = Approval["facts"][number];

export const pollMs = 10_000;
export const tokenPollMs = 30_000;

export const tokensQuery = queryOptions({
  queryKey: ["api-tokens"],
  queryFn: async ({ signal }) => plain(await apiTokens.listApiTokens({}, { signal })),
  refetchInterval: tokenPollMs,
});

const approvalsOf = (awaitingOnly: boolean, historyLimit: number, key: string) =>
  queryOptions({
    queryKey: ["approvals", key],
    queryFn: async ({ signal }): Promise<Approvals> => ({ ...plain(await approvals.listApprovals({ awaitingOnly, historyLimit }, { signal })), receivedMs: Date.now() }),
    refetchInterval: pollMs,
  });

/** The page: what waits for the owner, then the recent history. */
export const approvalsQuery = approvalsOf(false, 30, "list");
/** The sidebar badge: only what waits. */
export const awaitingQuery = approvalsOf(true, 0, "awaiting");

/** How many changes wait for the owner's approval (0 for anyone else: the procedures are owner-only). Polls while the shell is mounted. */
export function useAwaitingApprovals(): number {
  const owner = useIsOwner();
  const q = useQuery({ ...awaitingQuery, enabled: owner });
  return owner ? (q.data?.awaiting ?? 0) : 0;
}

/** A clock for the countdowns. It ticks only while `active`, so a value read right after it turned on may be old (secondsLeft allows for that). */
export function useTick(active: boolean, everyMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const id = setInterval(() => setNow(Date.now()), everyMs);
    return () => clearInterval(id);
  }, [active, everyMs]);
  return now;
}

// ---------------------------------------------------------------------------------------------------
// Tokens

export type TokenState = "active" | "expired" | "revoked";

/** `now` is the server's clock (ListApiTokensResponse.nowUnix), so a wrong local clock cannot revive an expired token. */
export function tokenState(tok: Pick<Token, "revokedUnix" | "expiresUnix">, now: number): TokenState {
  if (tok.revokedUnix > 0) return "revoked";
  return tok.expiresUnix <= now ? "expired" : "active";
}

/** Live tokens first (the server's order, newest first, is kept), the rest apart. */
export function splitTokens(list: readonly Token[], now: number): { live: Token[]; old: Token[] } {
  const live: Token[] = [];
  const old: Token[] = [];
  for (const tok of list) (tokenState(tok, now) === "active" ? live : old).push(tok);
  return { live, old };
}

export const profileOptions = [TokenProfile.READONLY, TokenProfile.OPERATOR, TokenProfile.ADMIN] as const;

const profiles: Record<TokenProfile, { key: MessageKey; desc: MessageKey; cls: string }> = {
  [TokenProfile.UNSPECIFIED]: { key: "int.tok.profile.readonly", desc: "int.tok.profile.readonly.desc", cls: "bg-surface-2 text-muted" },
  [TokenProfile.READONLY]: { key: "int.tok.profile.readonly", desc: "int.tok.profile.readonly.desc", cls: "bg-surface-2 text-muted" },
  [TokenProfile.OPERATOR]: { key: "int.tok.profile.operator", desc: "int.tok.profile.operator.desc", cls: "bg-accent-soft text-accent-text" },
  [TokenProfile.ADMIN]: { key: "int.tok.profile.admin", desc: "int.tok.profile.admin.desc", cls: "bg-danger-soft text-danger-text" },
};
export const profileInfo = (p: TokenProfile) => profiles[p] ?? profiles[TokenProfile.UNSPECIFIED];

/** Lifetimes on offer; "never" does not exist (the panel refuses more than a year). */
export const ttlOptions = [30, 90, 180, 365] as const;
export const defaultTtl = 90;
export const defaultRate = 120;
export const maxRate = 600;
export const maxName = 64;

/** The name the panel will accept: 1 to 64 characters after trimming. */
export const validName = (name: string) => {
  const n = name.trim();
  return n.length > 0 && [...n].length <= maxName;
};

/** Requests per minute from the field: a whole number in 1..600, else null. */
export function parseRate(text: string): number | null {
  const s = text.trim();
  if (!/^\d{1,4}$/.test(s)) return null;
  const n = Number(s);
  return n >= 1 && n <= maxRate ? n : null;
}

// ---------------------------------------------------------------------------------------------------
// MCP connection help

export type Client = "code" | "desktop" | "other" | "stdio";
export const clients: readonly Client[] = ["code", "desktop", "other", "stdio"];

/** The admin address (with the secret prefix) and the MCP endpoint next to it; both derive from <base href>, nothing is hard-coded. */
export const adminUrl = () => new URL("./", document.baseURI).href;
export const mcpUrl = () => new URL("mcp", document.baseURI).href;

/** A ready snippet per client. The token is always the placeholder `<token>` (or a token file): the real one is shown once, elsewhere. */
export function snippet(client: Client, mcp: string, admin: string): string {
  const name = "mistgate";
  switch (client) {
    case "code":
      return `claude mcp add --transport http ${name} ${mcp} --header "Authorization: Bearer <token>"`;
    case "desktop":
      return JSON.stringify({ mcpServers: { [name]: { command: name, args: ["mcp", "--url", admin, "--token-file", "<path-to-token-file>"] } } }, null, 2);
    case "other":
      return JSON.stringify({ mcpServers: { [name]: { type: "http", url: mcp, headers: { Authorization: "Bearer <token>" } } } }, null, 2);
    default:
      return `${name} mcp --url ${admin} --token-file <path-to-token-file>`;
  }
}

// ---------------------------------------------------------------------------------------------------
// Approvals

const states: Record<ApprovalState, { kind: StatusKind; key: MessageKey }> = {
  [ApprovalState.UNSPECIFIED]: { kind: "off", key: "approval.state.expired" },
  [ApprovalState.AWAITING]: { kind: "warn", key: "approval.state.awaiting" },
  [ApprovalState.APPROVED]: { kind: "ok", key: "approval.state.approved" },
  [ApprovalState.REJECTED]: { kind: "off", key: "approval.state.rejected" },
  [ApprovalState.EXPIRED]: { kind: "off", key: "approval.state.expired" },
  [ApprovalState.APPLYING]: { kind: "busy", key: "approval.state.applying" },
  [ApprovalState.APPLIED]: { kind: "ok", key: "approval.state.applied" },
  [ApprovalState.FAILED]: { kind: "bad", key: "approval.state.failed" },
  [ApprovalState.CANCELLED]: { kind: "off", key: "approval.state.cancelled" },
};
export const approvalStateInfo = (s: ApprovalState) => states[s] ?? states[ApprovalState.UNSPECIFIED];

/** Seconds until the approval expires, by the server's clock plus the time since the answer arrived; never below 0. */
export function secondsLeft(a: Pick<Approval, "expiresUnix">, data: Pick<Approvals, "nowUnix" | "receivedMs">, nowMs: number): number {
  // a tick older than the answer (the clock was idle) counts as no time passed
  const serverNow = data.nowUnix + Math.max(0, nowMs - data.receivedMs) / 1000;
  return Math.max(0, Math.ceil(a.expiresUnix - serverNow));
}

/** "9:41". */
export const clockText = (seconds: number) => `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;

const hasKey = (k: string): k is MessageKey => Object.hasOwn(en, k);
const lookup = (t: T, key: string, vars?: Record<string, string | number>) => (hasKey(key) ? t(key, vars) : null);

/** The title of an MCP tool ("approval.tool.<tool>"); an unknown tool shows its code. */
export const toolTitle = (t: T, tool: string) => lookup(t, `approval.tool.${tool}`) ?? tool;
/** The label of a fact ("approval.fact.<key>"). */
export const factLabel = (t: T, key: string) => lookup(t, `approval.fact.${key}`) ?? key;
/** The note of a danger code ("approval.danger.<code>"). */
export const dangerText = (t: T, code: string) => lookup(t, `approval.danger.${code}`) ?? code;

// Values the panel words itself, matched on its English text. Anything from data (`untrusted`) or unknown stays as it came.
const knownValues: Record<string, MessageKey> = {
  "their connections end and their devices are dropped from the nodes": "approval.value.disable",
  "they can connect again": "approval.value.enable",
  "the node restarts its agent": "approval.value.restart",
  "the device is disconnected and must be set up again": "approval.value.device",
  pause: "approval.value.pause",
  resume: "approval.value.resume",
  cancel: "approval.value.cancel",
};

const yesNo = (t: T, v: string) => (v === "true" ? t("int.ap.yes") : v === "false" ? t("int.ap.no") : v);

/** What a fact says, in the UI language where the panel's own wording is known. */
export function factText(t: T, f: Pick<Fact, "key" | "value" | "untrusted">): string {
  if (f.untrusted) return f.value;
  if (f.key === "drops_sessions" || f.key === "recommended") return yesNo(t, f.value);
  if (f.key === "progress") {
    const m = /^(\d+) of (\d+) nodes finished$/.exec(f.value);
    if (m) return t("approval.value.progress", { done: m[1]!, total: m[2]! });
  }
  const key = knownValues[f.value];
  return key ? t(key) : f.value;
}

/** A piece of a fact as the owner reads it: the panel's words, or a value from data (a name) that goes on a plate of its own. */
export type Piece = string | { data: string };

const resetKey: Record<string, MessageKey> = {
  none: "users.reset.none",
  day: "users.reset.day",
  week: "users.reset.week",
  month: "users.reset.month",
  rolling_month: "users.reset.rolling",
};
const appsKey: Record<string, MessageKey> = {
  default: "approval.value.apps.both", // no choice given: both apps are on
  both: "approval.value.apps.both",
  happ: "approval.value.apps.happ",
  amnezia: "approval.value.apps.amnezia",
};

/** One side of a "from -> to" change, by the field it belongs to; the raw value where there is no better word. */
function changeSide(t: T, fmt: Fmt, key: string, v: string): string {
  const n = Number(v);
  switch (key) {
    case "quota":
      return n === 0 ? t("approval.value.unlimited") : fmt.bytes(n);
    case "quota_reset":
      return resetKey[v] ? t(resetKey[v]) : v;
    case "expires":
      return n === 0 ? t("approval.value.noTerm") : fmt.date(n);
    case "speed_limit":
      return n === 0 ? t("approval.value.noLimit") : fmt.mbit(n);
    case "apps":
      return appsKey[v] ? t(appsKey[v]) : v;
    case "nodes":
      return v === "all" ? t("approval.value.allNodes") : /^\d+$/.test(v) ? t.n("approval.value.nodes", n) : v;
    case "kind": // an app of the subscription page: happ, amnezia
      return lookup(t, `subs.kind.${v}`) ?? v;
    case "recommended":
      return yesNo(t, v);
  }
  return v;
}

/** "Node is unreachable · Critical": the health screen's title where it needs no values, else a word of its own. */
function alertWords(t: T, kind: string, severity: string): string {
  const title = `health.alert.${kind}.title`;
  const what = lookup(t, `approval.alert.${kind}`) ?? (hasKey(title) && !en[title].includes("{") ? t(title) : kind);
  return [what, lookup(t, `hl.sev.${severity}`) ?? severity].filter(Boolean).join(" · ");
}

/** A message with its values filled in, where each value from data becomes a plate of its own. */
function withPlates(template: string, vars: Record<string, string>, data: readonly string[]): Piece[] {
  const out: Piece[] = [];
  for (const s of template.split(/(\{\w+\})/)) {
    const name = /^\{(\w+)\}$/.exec(s)?.[1];
    const piece: Piece = name === undefined ? s : data.includes(name) ? { data: vars[name] ?? "" } : (vars[name] ?? "");
    const last = out.at(-1);
    if (typeof piece === "string" && typeof last === "string") out[out.length - 1] = last + piece;
    else if (piece !== "") out.push(piece);
  }
  return out;
}

/**
 * A fact in the UI language. A coded fact (ApprovalFact.code, its values in params) is worded here; a value from data
 * stays on a plate of its own, inside the words or instead of them; an unknown code reads as the panel wrote it.
 */
export function factWords(t: T, fmt: Fmt, f: Pick<Fact, "key" | "value" | "untrusted" | "code" | "params" | "untrustedParams">): Piece[] {
  if (f.untrusted) return [{ data: f.value }];
  const p = f.params ?? {};
  const num = (k: string) => Number(p[k] ?? 0);
  switch (f.code ?? "") {
    case "":
      return [factText(t, f)];
    case "change":
      // both sides from data (an app's link or description): each on a plate of its own
      if (f.untrustedParams?.length) return withPlates("{from} → {to}", p, f.untrustedParams);
      return [`${changeSide(t, fmt, f.key, p.from ?? "")} → ${changeSide(t, fmt, f.key, p.to ?? "")}`];
    case "quota":
      return [num("bytes") === 0 ? t("approval.value.unlimited") : `${fmt.bytes(num("bytes"))}, ${changeSide(t, fmt, "quota_reset", p.reset ?? "")}`];
    case "never":
      return [t("approval.value.noTerm")];
    case "days":
      return [t.n("approval.value.days", num("n"))];
    case "all":
      return [t("approval.value.allNodes")];
    case "some":
      return [t.n("approval.value.nodes", num("n"))];
    case "seconds":
      return [fmt.duration(num("n"))];
    case "time":
      return [num("unix") === 0 ? t("approval.value.never") : fmt.dateTime(num("unix"))];
    case "bytes":
      return [fmt.bytes(num("bytes"))];
    case "progress":
      return [t("approval.value.progress", { done: p.done ?? "", total: p.total ?? "" })];
    case "alert":
      return [alertWords(t, p.kind ?? "", p.severity ?? "")];
  }
  const apps = f.key === "apps" ? appsKey[f.code] : undefined;
  if (apps) return [t(apps)];
  if (f.key === "kind") return [changeSide(t, fmt, "kind", f.code)];
  if (f.key === "fix") {
    // the code is the doctor's fix id; restart_inbound names its profile, a name from data
    const full = `approval.value.fix.${f.code}`;
    // a profile's name is data whatever the server says about it
    if (p.profile !== undefined && hasKey(full)) return withPlates(t(full), p, [...(f.untrustedParams ?? []), "profile"]);
    return [lookup(t, `health.fix.${f.code}.label`) ?? f.value];
  }
  return [lookup(t, `approval.value.${f.code}`) ?? factText(t, f)];
}

/**
 * What came of an approved change, or why it failed, in the UI language (Approval.outcome_code). Null when the code has
 * no wording here: the UI then shows the panel's own line.
 */
export function outcomeText(t: T, fmt: Fmt, a: Pick<Approval, "state" | "outcomeCode" | "outcomeParams">): string | null {
  const code = a.outcomeCode;
  if (!code) return null;
  if (a.state === ApprovalState.FAILED) {
    const why = lookup(t, `approval.fail.${code}`);
    return why ? t("approval.failed", { text: why }) : null;
  }
  const key = `approval.outcome.${code}`;
  if (!hasKey(key)) return null;
  const p = a.outcomeParams ?? {};
  if (p.seconds !== undefined) return t(key, { time: fmt.duration(Number(p.seconds)) });
  return p.n !== undefined && en[key].includes("|") ? t.n(key, Number(p.n)) : t(key);
}

// ---------------------------------------------------------------------------------------------------
// Errors and changes. Create, revoke and approve each ask for a fresh step-up (useStepUp runs the dialog and repeats the call).

/** A failed call as one sentence: the panel's short English messages are replaced, the rest goes through errorText. */
export function callErrorText(e: unknown, t: T): string {
  switch (ConnectError.from(e).code) {
    case Code.ResourceExhausted:
      return t("int.err.limit");
    case Code.AlreadyExists:
      return t("int.err.nameTaken");
    case Code.NotFound:
      return t("int.err.gone");
    case Code.FailedPrecondition:
      return t("int.err.notWaiting");
    default:
      return errorText(e, t);
  }
}

export type NewToken = { name: string; profile: TokenProfile; ttlDays: number; rateLimitPerMin: number };

export function useTokenActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const refresh = () => qc.invalidateQueries({ queryKey: tokensQuery.queryKey });
  const fail = (e: unknown) => {
    if (!isStepUpCancelled(e)) toast.error(callErrorText(e, t));
  };

  // The secret goes to `onSecret` and nowhere else: it must not sit in the mutation cache after the dialog is closed.
  const create = useMutation({
    mutationFn: async ({ token, onSecret }: { token: NewToken; onSecret: (secret: string) => void }) => {
      const r = await guard(() => apiTokens.createApiToken(token));
      onSecret(r.secret);
      return r.token?.name ?? token.name;
    },
    onSuccess: (name) => toast(t("int.tok.created", { name })),
    onError: fail,
    onSettled: refresh,
  });
  const revoke = useMutation({
    mutationFn: async (tok: { id: string; name: string }) => {
      await guard(() => apiTokens.revokeApiToken({ id: tok.id }));
      return tok.name;
    },
    onSuccess: (name) => toast(t("int.tok.revoked", { name })),
    onError: fail,
    onSettled: refresh,
  });
  return { create, revoke };
}

export function useApprovalActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const settled = () => qc.invalidateQueries({ queryKey: ["approvals"] });
  const fail = (e: unknown) => {
    if (!isStepUpCancelled(e)) toast.error(callErrorText(e, t));
  };
  // a node_install approval also carries the owner's SSH password and the host key they confirmed
  const approve = useMutation({
    mutationFn: (req: { id: string; sshPassword?: string; confirmedFingerprint?: string }) => guard(() => approvals.approve(req)),
    onSuccess: () => toast(t("int.ap.approved")),
    onError: fail,
    onSettled: settled,
  });
  // refusing is always safe: no step-up
  const reject = useMutation({
    mutationFn: (id: string) => approvals.reject({ id }),
    onSuccess: () => toast(t("int.ap.rejected")),
    onError: fail,
    onSettled: settled,
  });
  return { approve, reject, busy: approve.isPending || reject.isPending };
}
