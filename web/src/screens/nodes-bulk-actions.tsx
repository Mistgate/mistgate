import { ConnectError } from "@connectrpc/connect";
import { queryOptions, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { usePanelSSHKey } from "@/components/panel-ssh-key";
import { Fingerprint } from "@/components/ssh-login";
import { Footer, TargetList as BaseTargetList, type PanelHead, type RowLink } from "@/components/bulk";
import { Chip } from "@/components/ui/bits";
import type { IconName, Tone } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Callout } from "@/components/ui/notice";
import type { StatusKind } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { DoctorStatus, type NodeDoctor } from "@/gen/mistgate/admin/v1/health_pb";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import type { NodeServerAccess } from "@/gen/mistgate/admin/v1/provisioning_pb";
import { BundleStatus, NodeUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { health, nodes as nodesApi, provisioning } from "@/lib/api";
import type { Batch, Outcome, Row, Skipped, Spec, Target } from "@/lib/batch";
import { useEdition } from "@/lib/edition";
import { errorText } from "@/lib/errors";
import { isAccepted } from "@/lib/health";
import { agentLinked } from "@/lib/node-status";
import { reasonText as portReason } from "@/lib/port-check";
import { plain, type Plain } from "@/lib/plain";
import { inActiveRollout, isActive, useUpdateActions, useUpdates, type Updates } from "@/lib/updates";
import { reasonText as decoyReasonText, decoyReasonKind } from "./node/decoy";
import type { NodeSearch } from "./node/tabs";
import { passwordError } from "./node/ssh-password";
import { addPanelKey, keyError, keyTypeLabel, privateKeyShape, publicKeyLine } from "./node/ssh-keys";
import { RolloutDialog } from "./updates/dialogs";

// The actions of the Nodes page's bulk bar (screens/nodes-bulk.tsx): which selected nodes each one takes and why the
// others sit out (worked out here, in the browser, from what the page already holds), the call per node and how its
// answer reads, and the confirms of the ones with consequences. The SSH ones go one node at a time (the panel holds one
// SSH session per node and logs in several times per change); the port check too (the sender answers busy otherwise);
// the doctor four at a time. No password ever reaches the browser here: a change generates it on the panel, and Reveal
// stays in the node's card.

type Node = Plain<NodeMsg>;

export type ActionId = "ports" | "doctor" | "agent" | "password" | "panelKey" | "bindKey" | "unbindKey" | "decoy";

type Meta = { icon: IconName; tone: Tone; label: MessageKey; title: MessageKey; done: MessageKey; danger?: boolean; ssh?: boolean; link?: NodeSearch };

/** One tone per meaning: access lavender, checks sky (as the Updates section), the decoy sage, a removal rose. */
export const actionMeta: Record<ActionId, Meta> = {
  ports: { icon: "pulse", tone: "sky", label: "bulk.ports", title: "bulk.title.ports", done: "bulk.done.ports", link: { tab: "ways", way: "direct" } },
  doctor: { icon: "shield", tone: "sky", label: "bulk.doctor", title: "bulk.title.doctor", done: "bulk.done.doctor", link: { tab: "doctor" } },
  agent: { icon: "refresh", tone: "sky", label: "bulk.agent", title: "bulk.agent", done: "bulk.agent" },
  password: { icon: "lock", tone: "lavender", label: "bulk.password", title: "bulk.title.password", done: "bulk.done.password", ssh: true, link: { tab: "server" } },
  panelKey: { icon: "key", tone: "lavender", label: "bulk.panelKey", title: "bulk.title.panelKey", done: "bulk.done.panelKey", ssh: true, link: { tab: "server" } },
  bindKey: { icon: "plus", tone: "lavender", label: "bulk.bindKey", title: "bulk.title.bindKey", done: "bulk.done.bindKey", ssh: true, link: { tab: "server" } },
  unbindKey: { icon: "x", tone: "rose", label: "bulk.unbindKey", title: "bulk.title.unbindKey", done: "bulk.done.unbindKey", ssh: true, danger: true, link: { tab: "server" } },
  decoy: { icon: "mask", tone: "sage", label: "bulk.decoy", title: "bulk.title.decoy", done: "bulk.done.decoy" },
};

/** How a batch of `action` heads its panel (the chip, the title, the summary) and when a row offers its node's own page. */
export function panelFor(t: T, action: ActionId): { head: PanelHead; open: (row: Row) => RowLink | null } {
  const m = actionMeta[action];
  return {
    head: { icon: m.icon, tone: m.tone, title: t(m.title), done: (n, ok) => t.n(m.done, n, { ok }), hint: m.ssh ? t("bulk.retryHint") : undefined },
    // a node's own page is offered for a call that finished well and has something to look at there
    open: (row) => (m.link && row.state === "ok" && row.link ? { to: "/nodes/$id", params: { id: row.id }, search: m.link } : null),
  };
}

/** The saved SSH access of every node, as the node cards and the install wizard read it (one key, one cache). */
export const accessQuery = queryOptions({
  queryKey: ["node-server-access"],
  queryFn: ({ signal }) => provisioning.listNodeServerAccess({}, { signal }),
  staleTime: 60_000,
});

type Access = Pick<NodeServerAccess, "nodeId" | "host" | "nodeRetired" | "passwordSaved" | "rotationPending" | "panelKeyFingerprint">;

/** What the rules read: the saved access by node and the Updates answer. Either may still be on its way. */
export type Ctx = { access?: ReadonlyMap<string, Access>; updates?: Updates };

const accessMap = (list: readonly Access[]) => new Map(list.map((a) => [a.nodeId, a]));

/** "Only by password": the panel can log in with a saved password and has no key there yet (the offer above the list). */
export const passwordOnly = (a: Access | undefined) => !!a && !a.nodeRetired && a.passwordSaved && a.panelKeyFingerprint === "";

/** Why a selected node sits an action out, or null when it takes part. */
export function skipOf(action: ActionId, n: Node, ctx: Ctx): MessageKey | null {
  const a = ctx.access?.get(n.id);
  const reachable = !!a && !a.nodeRetired;
  switch (action) {
    case "ports":
      if (!agentLinked(n.status)) return "bulk.skip.offline";
      return n.protocols.length === 0 ? "bulk.skip.noProfiles" : null;
    case "doctor":
      return agentLinked(n.status) ? null : "bulk.skip.offline";
    case "password":
      if (!reachable) return "bulk.skip.noAccess";
      return a.passwordSaved || a.rotationPending ? null : "bulk.skip.keyOnly";
    case "panelKey":
      if (!reachable) return "bulk.skip.noAccess";
      if (a.panelKeyFingerprint !== "") return "bulk.skip.hasPanelKey";
      return a.passwordSaved ? null : "bulk.skip.noAccess";
    case "bindKey":
    case "unbindKey":
      return reachable ? null : "bulk.skip.noAccess";
    case "agent": {
      const u = ctx.updates?.nodes.find((x) => x.nodeId === n.id);
      if (!u) return "bulk.skip.current";
      if (inActiveRollout(ctx.updates?.rollout, n.id) || u.state === NodeUpdateState.UPDATING) return "bulk.skip.updating";
      if (u.state === NodeUpdateState.OUTDATED) return null;
      if (u.state === NodeUpdateState.UNSUPPORTED || !u.supportsUpdate) return "bulk.skip.manual";
      if (u.state === NodeUpdateState.OFFLINE) return "bulk.skip.offline";
      return u.state === NodeUpdateState.UP_TO_DATE ? "bulk.skip.current" : "bulk.skip.updatesPage";
    }
    case "decoy":
      return null;
  }
}

/** The whole action stands still, whoever is selected: no signed release, or a rollout already running (the server takes one at a time). */
export function blockOf(action: ActionId, ctx: Ctx): MessageKey | null {
  if (action !== "agent") return null;
  if (isActive(ctx.updates?.rollout)) return "bulk.agent.rollout";
  return ctx.updates?.bundle?.status === BundleStatus.TRUSTED ? null : "bulk.agent.noBundle";
}

/** The selected nodes an action takes, in their order, and the ones it skips with their reason. */
export function split(action: ActionId, nodes: readonly Node[], ctx: Ctx, t: T): { targets: Target[]; skipped: Skipped[] } {
  const targets: Target[] = [];
  const skipped: Skipped[] = [];
  for (const n of nodes) {
    // an SSH action names the host it logs in to
    const host = (actionMeta[action].ssh && ctx.access?.get(n.id)?.host) || n.address;
    const why = skipOf(action, n, ctx);
    if (why) skipped.push({ id: n.id, name: n.name, host, note: t(why) });
    else targets.push({ id: n.id, name: n.name, host });
  }
  return { targets, skipped };
}

/** How many nodes an action takes and, when none or the whole action is held, why (the most common reason). */
export function availability(action: ActionId, nodes: readonly Node[], ctx: Ctx, t: T): { count: number; why?: string } {
  const block = blockOf(action, ctx);
  if (block) return { count: 0, why: t(block) };
  const reasons = nodes.map((n) => skipOf(action, n, ctx));
  const count = reasons.filter((r) => r === null).length;
  if (count > 0) return { count };
  const tally = new Map<MessageKey, number>();
  for (const r of reasons) if (r) tally.set(r, (tally.get(r) ?? 0) + 1);
  const top = [...tally].sort((a, b) => b[1] - a[1])[0]?.[0];
  return { count, why: top ? t(top) : undefined };
}

// ---------------------------------------------------------------------------------------------------
// How an answer reads

/** A refusal that is already in words (a check that could not run). */
class Said extends Error {}

type Verdict = Extract<StatusKind, "ok" | "warn" | "bad" | "off">;

type PortsAnswer = { errorCode: string; ports: readonly { verdict: string }[]; tcp: readonly { verdict: string }[] };

/** "3 ports fine, 1 lossy": UDP ok and TCP ours are fine, UDP lossy is lossy, everything else that was checked does not pass. */
export function portsOutcome(t: T, r: PortsAnswer): Outcome {
  if (r.errorCode) throw new Said(portReason(t, r.errorCode));
  const ok = r.ports.filter((p) => p.verdict === "ok").length + r.tcp.filter((p) => p.verdict === "ours").length;
  const lossy = r.ports.filter((p) => p.verdict === "lossy").length;
  const broken = r.ports.filter((p) => p.verdict === "broken").length + r.tcp.filter((p) => p.verdict !== "" && p.verdict !== "ours").length;
  const kind: Verdict = broken > 0 ? "bad" : lossy > 0 ? "warn" : ok > 0 ? "ok" : "off";
  const parts = [ok && t.n("bulk.ports.ok", ok), lossy && t.n("bulk.ports.lossy", lossy), broken && t.n("bulk.ports.broken", broken)].filter(Boolean);
  return { kind, label: t(`bulk.result.${kind}`), note: parts.join(", ") || t("bulk.ports.none"), link: true };
}

/** "2 failing, 1 warning": an accepted warning is normal for the node and not counted. */
export function doctorOutcome(t: T, d: Pick<Plain<NodeDoctor>, "agentSupported" | "hasReport" | "items"> | undefined): Outcome {
  if (!d?.agentSupported) return { kind: "off", label: t("bulk.result.off"), note: t("bulk.doctor.unsupported") };
  if (!d.hasReport) return { kind: "off", label: t("bulk.result.off"), note: t("bulk.doctor.noReport") };
  const fail = d.items.filter((i) => i.status === DoctorStatus.FAIL).length;
  const warn = d.items.filter((i) => i.status === DoctorStatus.WARN && !isAccepted(i)).length;
  const kind: Verdict = fail > 0 ? "bad" : warn > 0 ? "warn" : "ok";
  const parts = [fail && t.n("bulk.doctor.fail", fail), warn && t.n("bulk.doctor.warn", warn)].filter(Boolean);
  return { kind, label: t(`bulk.result.${kind}`), note: parts.join(", ") || t("bulk.doctor.ok"), link: true };
}

/** A node after the bulk decoy change, live from the nodes list: applied, on its way, or what keeps it. */
export function decoyOutcome(t: T, n: Pick<Node, "decoyReasonCode"> | undefined): Outcome {
  const code = n?.decoyReasonCode ?? "";
  if (code === "") return {};
  if (code === "pending") return { kind: "busy", label: t("bulk.state.running") };
  return { kind: decoyReasonKind(code), label: t("bulk.decoy.notApplied"), note: decoyReasonText(t, code) };
}

// ---------------------------------------------------------------------------------------------------
// Public keys

const fingerprintShape = /^SHA256:[A-Za-z0-9+/]{43}$/;

/** The SSH wire string at `at`: its bytes and where the next one starts; null past the end. */
function wire(b: Uint8Array, at: number): { bytes: Uint8Array; next: number } | null {
  if (at + 4 > b.length) return null;
  const len = new DataView(b.buffer, b.byteOffset + at, 4).getUint32(0);
  const next = at + 4 + len;
  return next > b.length ? null : { bytes: b.subarray(at + 4, next), next };
}

function keyBits(type: string, blob: Uint8Array): number {
  if (type.startsWith("ecdsa-sha2-nistp")) return Number(type.slice("ecdsa-sha2-nistp".length));
  if (type !== "ssh-rsa") return 256;
  // ssh-rsa: the type, the exponent, then the modulus, whose length is the key size
  const e = wire(blob, wire(blob, 0)?.next ?? blob.length);
  const n = e && wire(blob, e.next);
  if (!n) return 0;
  const m = n.bytes;
  let i = 0;
  while (i < m.length && m[i] === 0) i++;
  return i === m.length ? 0 : (m.length - i - 1) * 8 + (32 - Math.clz32(m[i]!));
}

/**
 * One public key line read in the browser: its type and size as ssh-keygen -l says them, its comment, and its SHA256
 * fingerprint (the hash of the decoded key, base64 without padding, the same as golang.org/x/crypto/ssh). Null for a line
 * that is not one public key, or whose key data does not name the type in front of it. `fingerprint` is "" where the
 * browser has no Web Crypto (a page not served over HTTPS).
 */
export async function sshKeyInfo(input: string): Promise<{ type: string; label: string; comment: string; fingerprint: string } | null> {
  const line = input.trim();
  if (privateKeyShape.test(line) || !publicKeyLine.test(line)) return null;
  const [, type = "", data = "", comment = ""] = line.match(/^(\S+) (\S+)(?: (.*))?$/) ?? [];
  let blob: Uint8Array<ArrayBuffer>;
  try {
    blob = Uint8Array.from(atob(data), (c) => c.charCodeAt(0));
  } catch {
    return null;
  }
  const head = wire(blob, 0);
  if (!head || new TextDecoder().decode(head.bytes) !== type) return null;
  const subtle = globalThis.crypto?.subtle;
  const digest = subtle ? new Uint8Array(await subtle.digest("SHA-256", blob)) : null;
  const fingerprint = digest ? `SHA256:${btoa(String.fromCharCode(...digest)).replace(/=+$/, "")}` : "";
  return { type, label: keyTypeLabel(type, keyBits(type, blob)), comment, fingerprint };
}

// ---------------------------------------------------------------------------------------------------
// Running

export type Extra = { publicKey?: string; fingerprint?: string };

/**
 * The rules' data (the access list and Updates, owner pages only) and `run`, which starts an action over the selected
 * nodes: an SSH one reads the access list fresh first, so a node whose access changed a minute ago is sorted by now.
 */
export function useBulkRun(batch: Batch, enabled: boolean) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const { edge } = useEdition();
  const list = useQuery({ ...accessQuery, enabled });
  const updates = useUpdates();
  const access = list.data?.access;
  const ctx: Ctx = { access: access ? accessMap(access) : undefined, updates: updates.data };

  function spec(action: ActionId, c: Ctx, extra: Extra): Spec {
    const viaKey = (id: string) => (c.access?.get(id)?.panelKeyFingerprint ?? "") !== "";
    const ssh = { limit: 1, error: (e: unknown) => keyError(e, t, edge), invalidate: [["node-server-access"], ["node-server-keys"]] };
    switch (action) {
      case "ports":
        return {
          action,
          limit: 1,
          call: async (id) => portsOutcome(t, plain(await nodesApi.checkPorts({ nodeId: id }))),
          error: (e) => (e instanceof Said ? e.message : errorText(e, t)),
          invalidate: [["nodes"], ["node"]],
        };
      case "doctor":
        return {
          action,
          limit: 4,
          call: async (id) => doctorOutcome(t, plain(await health.runDoctor({ nodeId: id })).nodes[0]),
          error: (e) => errorText(e, t),
          invalidate: [["health"], ["overview"], ["nodes"]],
        };
      case "password":
        return {
          ...ssh,
          action,
          // generated on the panel and never returned: the owner reveals each one in its card
          call: async (id) => {
            await provisioning.rotateNodeServerPassword({ nodeId: id, newPassword: "", generate: true, confirm: true });
            return { link: true };
          },
          error: (e, id) => passwordError(e, t, viaKey(id)),
          invalidate: [["node-server-access"]],
        };
      case "panelKey":
        return { ...ssh, action, call: async (id) => void (await addPanelKey(id)) };
      case "bindKey":
        return {
          ...ssh,
          action,
          call: async (id) => {
            const r = await provisioning.addNodeServerKey({ nodeId: id, publicKey: extra.publicKey ?? "", confirm: true });
            return r.key?.restricted ? { note: t("node.sshKeys.alreadyRestricted") } : undefined;
          },
        };
      case "unbindKey":
        return {
          ...ssh,
          action,
          // the panel key is never in a bulk unbind (the dialog refuses it): confirmPanelKey stays false
          call: async (id) => void (await provisioning.removeNodeServerKey({ nodeId: id, fingerprint: extra.fingerprint ?? "", confirm: true, confirmPanelKey: false })),
          error: (e) => (ConnectError.from(e).rawMessage === "ssh_public_key_not_found" ? { kind: "off", label: t("bulk.state.absent") } : keyError(e, t, edge)),
        };
      case "decoy":
      case "agent":
        // one server call for all of them, made by its dialog: the rows only show what came of it
        return { action, limit: 1, error: (e) => errorText(e, t), invalidate: [] };
    }
  }

  return {
    ctx,
    async run(action: ActionId, nodes: readonly Node[], extra: Extra = {}) {
      let c = ctx;
      if (actionMeta[action].ssh) {
        try {
          const fresh = await qc.fetchQuery({ ...accessQuery, staleTime: 0 });
          c = { ...ctx, access: accessMap(fresh.access) };
        } catch (e) {
          toast.error(errorText(e, t));
          return;
        }
      }
      const { targets, skipped } = split(action, nodes, c, t);
      batch.start(spec(action, c, extra), targets, skipped);
    },
  };
}

export type BulkRun = ReturnType<typeof useBulkRun>;

// ---------------------------------------------------------------------------------------------------
// Confirms

/** Who takes part and who sits out, under a confirm: "Nodes (3) · 1 hidden by the filter", the skipped with their reason. */
function TargetList({ targets, skipped, hidden = 0 }: { targets?: readonly Target[]; skipped: readonly Skipped[]; hidden?: number }) {
  const t = useT();
  return <BaseTargetList title={targets && t("bulk.list.targets", { n: targets.length })} note={hidden > 0 ? t.n("bulk.hidden", hidden) : undefined} targets={targets} skipped={skipped} />;
}

type DialogProps = { nodes: readonly Node[]; hidden: ReadonlySet<string>; run: BulkRun; onClose: () => void };

const hiddenOf = (targets: readonly Target[], hidden: ReadonlySet<string>) => targets.filter((x) => hidden.has(x.id)).length;

/** Change the SSH password on the selected nodes: generated per node, never shown here; each row of the result opens its card. */
export function PasswordDialog({ nodes, hidden, run, onClose }: DialogProps) {
  const t = useT();
  const { targets, skipped } = split("password", nodes, run.ctx, t);
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t.n("bulk.password.title", targets.length)}
      description={t("bulk.password.body")}
      footer={
        <Footer
          label={t("bulk.password.go")}
          disabled={targets.length === 0}
          onClose={onClose}
          go={() => {
            onClose();
            void run.run("password", nodes);
          }}
        />
      }
    >
      <Callout kind="warn">{t("bulk.password.warn")}</Callout>
      <TargetList targets={targets} skipped={skipped} hidden={hiddenOf(targets, hidden)} />
      <p className="text-xs leading-snug text-pretty text-muted">
        {t("bulk.pace")} {t("bulk.password.hint")}
      </p>
    </Modal>
  );
}

/** A public key line in, its type, comment and fingerprint shown before anything is sent; a private key never kept. */
function useKeyField() {
  const [value, setValue] = useState("");
  const [refused, setRefused] = useState(false);
  const [info, setInfo] = useState<Awaited<ReturnType<typeof sshKeyInfo>>>(null);
  useEffect(() => {
    let live = true;
    void sshKeyInfo(value).then((i) => live && setInfo(i));
    return () => {
      live = false;
    };
  }, [value]);
  return {
    value,
    info,
    refused,
    set(v: string) {
      // a private key never leaves the browser, and is not kept in the field either
      const priv = privateKeyShape.test(v);
      setRefused(priv);
      setValue(priv ? "" : v);
    },
  };
}

function KeyPreview({ info }: { info: { label: string; comment: string; fingerprint: string } }) {
  const t = useT();
  return (
    <div className="flex flex-col gap-1.5 rounded-field border border-line bg-surface-2 px-3.5 py-3">
      <div className="flex min-w-0 flex-wrap items-center gap-1.5">
        <Chip mono className="text-fg">
          {info.label}
        </Chip>
        <span className={info.comment ? "min-w-0 text-[13px] font-semibold break-words text-fg" : "text-[13px] text-muted"}>{info.comment ? <bdi>{info.comment}</bdi> : t("node.sshKeys.noComment")}</span>
      </div>
      {info.fingerprint && <Fingerprint value={info.fingerprint} className="text-muted" />}
    </div>
  );
}

function KeyField({ id, label, hint, value, onChange, problem }: { id: string; label: string; hint: string; value: string; onChange: (v: string) => void; problem?: string }) {
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className="text-xs font-semibold text-muted">
        {label}
      </label>
      <textarea
        id={id}
        dir="ltr"
        rows={3}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="ssh-ed25519 AAAA… alice@laptop"
        spellCheck={false}
        autoComplete="off"
        autoCapitalize="off"
        aria-describedby={`${id}-hint`}
        aria-invalid={problem ? true : undefined}
        className="min-h-[88px] w-full resize-y rounded-field border border-line bg-surface p-3 font-mono text-xs leading-relaxed wrap-anywhere text-fg outline-none transition-colors duration-200 placeholder:text-faint focus:border-accent"
      />
      <p id={`${id}-hint`} className="text-xs leading-snug text-muted">
        {hint}
      </p>
      {problem && (
        <Callout kind="bad" alert>
          {problem}
        </Callout>
      )}
    </div>
  );
}

/** Bind one public key on the selected nodes. Only a single public key line is sent; a private key or two lines stay here. */
export function BindDialog({ nodes, hidden, run, onClose }: DialogProps) {
  const t = useT();
  const key = useKeyField();
  const { targets, skipped } = split("bindKey", nodes, run.ctx, t);
  const typed = key.value.trim() !== "";
  const problem = key.refused ? t("node.sshKeys.privateRefused") : typed && !key.info ? t("node.sshKeys.error.invalid") : undefined;
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t.n("bulk.bind.title", targets.length)}
      footer={
        <Footer
          label={t("bulk.bind.go")}
          disabled={targets.length === 0 || !key.info}
          onClose={onClose}
          go={() => {
            if (!key.info) return;
            onClose();
            void run.run("bindKey", nodes, { publicKey: key.value.trim() });
          }}
        />
      }
    >
      <KeyField id="bulk-bind-key" label={t("node.sshKeys.publicKey")} hint={t("node.sshKeys.publicKeyHint")} value={key.value} onChange={key.set} problem={problem} />
      {key.info && <KeyPreview info={key.info} />}
      <TargetList targets={targets} skipped={skipped} hidden={hiddenOf(targets, hidden)} />
      <p className="text-xs leading-snug text-pretty text-muted">{t("bulk.pace")}</p>
    </Modal>
  );
}

/**
 * Unbind a key from the selected nodes by its public line or its fingerprint. The panel key is refused here (the server
 * would ask for its own confirmation, and losing it on many nodes at once is not a bulk decision).
 */
export function UnbindDialog({ nodes, hidden, run, onClose }: DialogProps) {
  const t = useT();
  const key = useKeyField();
  const panel = usePanelSSHKey().data?.fingerprint;
  const { targets, skipped } = split("unbindKey", nodes, run.ctx, t);
  const typed = key.value.trim();
  const fingerprint = fingerprintShape.test(typed) ? typed : (key.info?.fingerprint ?? "");
  const panelKeys = new Set([panel, ...[...(run.ctx.access?.values() ?? [])].map((a) => a.panelKeyFingerprint)].filter(Boolean));
  const isPanel = fingerprint !== "" && panelKeys.has(fingerprint);
  const problem = key.refused
    ? t("node.sshKeys.privateRefused")
    : isPanel
      ? t("bulk.unbind.panel")
      : typed !== "" && fingerprint === ""
        ? t("bulk.unbind.badFp")
        : undefined;
  const ready = fingerprint !== "" && !isPanel;
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t.n("bulk.unbind.title", targets.length)}
      footer={
        <Footer
          danger
          label={t("bulk.unbind.go")}
          disabled={targets.length === 0 || !ready}
          onClose={onClose}
          go={() => {
            if (!ready) return;
            onClose();
            void run.run("unbindKey", nodes, { fingerprint });
          }}
        />
      }
    >
      <KeyField id="bulk-unbind-key" label={t("bulk.unbind.label")} hint={t("bulk.unbind.hint")} value={key.value} onChange={key.set} problem={problem} />
      {key.info && !isPanel && <KeyPreview info={key.info} />}
      {ready && !key.info && (
        <div className="rounded-field border border-line bg-surface-2 px-3.5 py-3">
          <Fingerprint value={fingerprint} className="text-fg" />
        </div>
      )}
      <TargetList targets={targets} skipped={skipped} hidden={hiddenOf(targets, hidden)} />
      <p className="text-xs leading-snug text-pretty text-danger-text">{t("bulk.unbind.warn")}</p>
    </Modal>
  );
}

/** The Updates page's own rollout window over the selected outdated nodes; once it starts, the Updates page shows its course. */
export function AgentDialog({ nodes, run, onClose }: Omit<DialogProps, "hidden">) {
  const t = useT();
  const navigate = useNavigate();
  const actions = useUpdateActions();
  const d = run.ctx.updates;
  const bundle = d?.bundle;
  const { targets, skipped } = split("agent", nodes, run.ctx, t);
  if (!d || !bundle) return null;
  const ids = new Set(targets.map((x) => x.id));
  return (
    <RolloutDialog
      version={bundle.version}
      nodes={d.nodes.filter((n) => ids.has(n.nodeId))}
      busy={actions.busy}
      onClose={onClose}
      onStart={(nodeIds) =>
        void actions.start(nodeIds, { version: bundle.version, built: bundle.built }).then((ok) => {
          if (!ok) return;
          onClose();
          void navigate({ to: "/updates" });
        })
      }
    >
      {skipped.length > 0 && <TargetList skipped={skipped} />}
    </RolloutDialog>
  );
}

