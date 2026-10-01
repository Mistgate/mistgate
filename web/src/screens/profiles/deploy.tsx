import { Code, ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { NodeStatus, type Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import { PrepareAwgOutcome, type ListNodesResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { StatusDot, StatusPill } from "@/components/ui/status";
import { TextField } from "@/components/ui/text-field";
import { nodes as nodesApi, profiles as profilesApi } from "@/lib/api";
import { codedError } from "@/lib/coded-error";
import { cx } from "@/lib/cx";
import { errorCode, errorVars } from "@/lib/errors";
import { nodeKind } from "@/lib/node-status";
import type { Plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import { asMode, AwgBackendFields, canPrepare, type AwgMode } from "@/screens/node/awg-backend";
import { useTx, type Tx } from "@/screens/users/t";
import { Check } from "@/screens/users/ui";

// "Put on nodes" from the profile page: tick nodes (or all of them), one button, one CreateInbound per node, and the
// result of each in the same window. What a node needs is asked only there: a domain where Let's Encrypt cannot work on
// an IP, another port where the one of the profile is taken, the AmneziaWG backend of a node's first AmneziaWG profile.
// A node that refuses does not undo the others.

type NodeRow = Plain<ListNodesResponse>["nodes"][number];

/** Why a node refused the profile, sorted by what the window can offer: a port field, a domain field, or just the reason. */
export type Refusal =
  | { kind: "port"; text: string; free?: string }
  | { kind: "domain"; text: string }
  | { kind: "already"; text: string }
  | { kind: "other"; text: string };

export const isIp = (address: string) => /^\d{1,3}(\.\d{1,3}){3}$/.test(address) || address.includes(":");

/**
 * Reads a CreateInbound refusal. The coded forms ("port_taken: port=443&profile=Main&free=8443", "acme_needs_domain:
 * address=…", "already_on_node") come first; the panel's older English sentences of the same refusals are recognised too.
 * Anything else reads as errorText does.
 */
export function refusalOf(e: unknown, t: Tx, node: { name: string; address: string }): Refusal {
  const msg = ConnectError.from(e).rawMessage;
  const code = errorCode(msg);
  const v = errorVars(msg);
  const old = /UDP port (\d+) is already used by profile "(.*)" on this node/.exec(msg);
  if (code === "port_taken" || old) {
    const port = v.port ?? old?.[1] ?? "";
    const profile = v.profile ?? old?.[2] ?? "";
    const free = v.free || undefined;
    return { kind: "port", free, text: t(free ? "where.portTakenFree" : "where.portTaken", { port, node: node.name, profile, free: free ?? "" }) };
  }
  if (code === "acme_needs_domain" || /host name is required for a Let's Encrypt certificate/.test(msg)) {
    return { kind: "domain", text: t("where.domainNeed", { node: node.name, address: v.address || node.address }) };
  }
  if (code === "already_on_node" || /already deployed on the node/.test(msg)) return { kind: "already", text: t("where.already") };
  if (/port override lies inside the hop range/.test(msg)) return { kind: "port", text: t("where.portPrompt", { node: node.name }) };
  return { kind: "other", text: codedError(e, t, "awg.err") };
}

type Row = { state: "idle" | "busy" | "ok" | "already" | "failed"; refusal?: Refusal; port: string; sni: string; backend?: AwgMode; showBackend?: boolean };

export type DeployProfile = {
  id: string;
  name: string;
  protocol: string;
  /** The certificate settings the window checks before the click (Hysteria2: tls_mode, sni). */
  tlsMode?: string;
  sni?: string;
};

export type DeploySummary = { ok: number; failed: number };

/**
 * The window. `preselect` ticks nodes when it opens; with `autorun` it puts the profile on them at once (the new-profile
 * form does that right after creating it). `onFinished` hears every run's outcome.
 */
export function DeployDialog({
  open,
  onOpenChange,
  profile,
  inbounds,
  preselect,
  autorun,
  onFinished,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  profile: DeployProfile;
  inbounds: readonly Pick<Inbound, "nodeId">[];
  preselect?: string[];
  autorun?: boolean;
  onFinished?: (s: DeploySummary) => void;
}) {
  const t = useTx();
  const [running, setRunning] = useState(false);
  return (
    <Modal
      open={open}
      onOpenChange={(o) => !running && onOpenChange(o)}
      title={t("where.nodeTitle", { name: profile.name })}
      description={t("where.nodeBody")}
      closeLabel={running ? undefined : t("common.close")}
      className="md:max-w-[560px]"
    >
      {open && (
        <DeployBody profile={profile} inbounds={inbounds} preselect={preselect} autorun={autorun} onRunning={setRunning} onFinished={onFinished} onClose={() => onOpenChange(false)} />
      )}
    </Modal>
  );
}

function DeployBody({
  profile,
  inbounds,
  preselect,
  autorun,
  onRunning,
  onFinished,
  onClose,
}: {
  profile: DeployProfile;
  inbounds: readonly Pick<Inbound, "nodeId">[];
  preselect?: string[];
  autorun?: boolean;
  onRunning: (r: boolean) => void;
  onFinished?: (s: DeploySummary) => void;
  onClose: () => void;
}) {
  const t = useTx();
  const qc = useQueryClient();
  const list = useQuery(nodesQuery);
  // the nodes that carried the profile when the window opened; one put on here stays listed with its result
  const [here] = useState(() => new Set(inbounds.map((i) => i.nodeId)));
  const candidates = (list.data?.nodes ?? []).filter((n) => n.status !== NodeStatus.RETIRED && !here.has(n.id));
  const [picked, setPicked] = useState<string[] | null>(preselect ?? null); // null = not touched yet
  const [rows, setRows] = useState<Record<string, Row>>({});
  const [phase, setPhase] = useState<"idle" | "running" | "done">("idle");
  // a run reads the fields as they were at the click (they are locked while it runs)
  const rowsRef = useRef(rows);
  useEffect(() => {
    rowsRef.current = rows;
  });

  const awg = profile.protocol === "awg";
  const rowOf = (n: NodeRow): Row => rows[n.id] ?? { state: "idle", port: "", sni: "", backend: asMode(n.awgBackend) };
  const patch = (id: string, p: Partial<Row>) => setRows((r) => ({ ...r, [id]: { ...(r[id] ?? { state: "idle", port: "", sni: "" }), ...p } }));
  // only a single node to put it on: it is ticked from the start
  const chosen = picked ?? (candidates.length === 1 ? [candidates[0]!.id] : []);
  const isPicked = (id: string) => chosen.includes(id);
  const firstAwg = (n: NodeRow) => awg && !n.protocols.includes("awg");
  const needsDomain = (n: NodeRow) => !awg && profile.tlsMode === "acme_domain" && !profile.sni && isIp(n.address);
  const done = (r: Row) => r.state === "ok" || r.state === "already";
  const todo = chosen.filter((id) => !done(rows[id] ?? { state: "idle", port: "", sni: "" }));
  const failed = chosen.filter((id) => rows[id]?.state === "failed");
  const allOn = candidates.length > 0 && candidates.every((n) => isPicked(n.id));

  const toggle = (id: string) => setPicked(isPicked(id) ? chosen.filter((x) => x !== id) : [...chosen, id]);
  const toggleAll = () => setPicked(allOn ? candidates.filter((n) => done(rowOf(n))).map((n) => n.id) : candidates.map((n) => n.id));

  async function run(ids: string[]) {
    setPicked(chosen);
    setPhase("running");
    onRunning(true);
    let ok = 0;
    let bad = 0;
    for (const id of ids) {
      const n = candidates.find((x) => x.id === id);
      if (!n) continue;
      const r = rowsRef.current[id] ?? { state: "idle" as const, port: "", sni: "", backend: asMode(n.awgBackend) };
      patch(id, { state: "busy", refusal: undefined });
      try {
        // a node has one AmneziaWG backend: a choice made for its first AmneziaWG profile is saved first
        const mode = r.backend ?? asMode(n.awgBackend);
        if (firstAwg(n) && mode !== asMode(n.awgBackend)) {
          if (mode === "kernel" && canPrepare(n)) {
            const plan = await nodesApi.prepareAwgKernel({ nodeId: id, confirm: false });
            if (plan.outcome !== PrepareAwgOutcome.READY) throw new ConnectError("kernel_not_ready", Code.FailedPrecondition);
          }
          await nodesApi.updateNode({ nodeId: id, awgBackend: mode });
        }
        await profilesApi.createInbound({ profileId: profile.id, nodeId: id, portOverride: r.port ? Number(r.port) : 0, tlsServerNameOverride: awg ? "" : r.sni.trim() });
        patch(id, { state: "ok" });
        ok++;
      } catch (e) {
        const ref = refusalOf(e, t, n);
        if (ref.kind === "already") {
          patch(id, { state: "already", refusal: ref });
          ok++;
        } else {
          patch(id, { state: "failed", refusal: ref, ...(ref.kind === "port" && ref.free ? { port: ref.free } : {}) });
          bad++;
        }
      }
    }
    // a profile on new nodes changes where it runs, what groups give and what people get
    await Promise.all(["profiles", "nodes", "node", "groups", "users", "subs"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
    setPhase("done");
    onRunning(false);
    onFinished?.({ ok, failed: bad });
  }

  // the new-profile form: put it on the ticked nodes at once, as soon as the list is here
  const started = useRef(false);
  useEffect(() => {
    if (!autorun || started.current || !list.data || chosen.length === 0) return;
    started.current = true;
    void run(chosen);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once, when the nodes arrive
  }, [autorun, list.data]);

  if (list.isPending) return <p className="text-sm text-muted">{t("common.loading")}</p>;
  if (candidates.length === 0) {
    return (
      <div className="flex flex-col gap-3">
        <Notice>{t("where.noNodeLeft")}</Notice>
        <div className="flex justify-end">
          <Button variant="ghost" onClick={onClose}>
            {t("common.close")}
          </Button>
        </div>
      </div>
    );
  }

  const busy = phase === "running";
  return (
    <div className="flex flex-col gap-3">
      {candidates.length > 1 && (
        <div className="flex min-h-10 cursor-pointer items-center gap-2.5 px-3 text-[13px] font-bold" onClick={() => !busy && toggleAll()}>
          <Check checked={allOn} onCheckedChange={toggleAll} label={t("where.allNodes")} disabled={busy} />
          {t("where.allNodes")}
          <span className="font-mono text-xs font-semibold text-muted">{candidates.length}</span>
        </div>
      )}
      <ul className="flex max-h-[min(52vh,440px)] flex-col gap-1.5 overflow-y-auto">
        {candidates.map((n) => {
          const r = rowOf(n);
          const on = isPicked(n.id);
          const ref = r.refusal;
          const askDomain = on && r.state !== "ok" && (ref?.kind === "domain" || (r.state === "idle" && needsDomain(n)));
          const askPort = on && r.state === "failed" && ref?.kind === "port";
          const askBackend = on && firstAwg(n) && (r.state === "idle" || r.state === "failed");
          const mode = r.backend ?? asMode(n.awgBackend);
          return (
            <li key={n.id} className={cx("rounded-field border bg-surface transition-colors", r.state === "failed" ? "border-[color-mix(in_oklch,var(--danger)_45%,var(--border))]" : on ? "border-accent-line" : "border-line")}>
              <div className="flex min-h-11 cursor-pointer items-center gap-2.5 px-3 py-1.5" onClick={() => !busy && !done(r) && toggle(n.id)}>
                <Check checked={on || done(r)} onCheckedChange={() => toggle(n.id)} label={n.name} disabled={busy || done(r)} />
                <StatusDot kind={nodeKind(n)} />
                <b className="min-w-0 truncate text-[13px]">{n.name}</b>
                {n.countryCode && <Flag code={n.countryCode} size={12} />}
                <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted">{n.address}</span>
                {r.state === "busy" && <StatusPill kind="busy" label={t("where.deploying")} sm />}
                {r.state === "ok" && <StatusPill kind="ok" label={t("where.deployedShort")} sm />}
                {r.state === "already" && <StatusPill kind="off" label={t("where.already")} sm />}
                {r.state === "failed" && <StatusPill kind="bad" label={t("where.failedShort")} sm />}
              </div>
              {(r.state === "ok" || askDomain || askPort || askBackend || (r.state === "failed" && ref?.kind === "other")) && (
                <div className="flex flex-col gap-2 border-t border-line px-3 py-2.5 text-xs leading-snug">
                  {r.state === "ok" && <span className="text-muted">{t("where.deployed")}</span>}
                  {r.state === "failed" && ref && ref.kind !== "domain" && <span className="text-danger-text">{ref.text}</span>}
                  {askDomain && (
                    <>
                      <span className={r.state === "failed" ? "text-danger-text" : "text-warn-text"}>{ref?.kind === "domain" ? ref.text : t("where.domainNeed", { node: n.name, address: n.address })}</span>
                      <TextField
                        aria-label={t("where.domainLabel", { node: n.name })}
                        placeholder="vpn.example.com"
                        value={r.sni}
                        onChange={(e) => patch(n.id, { sni: e.target.value })}
                        disabled={busy}
                        mono
                        autoCapitalize="off"
                        spellCheck={false}
                      />
                      <span className="text-muted">{t("where.domainOrSelf")}</span>
                    </>
                  )}
                  {askPort && (
                    <div className="flex flex-wrap items-end gap-2">
                      <TextField
                        label={t("where.portLabel", { node: n.name })}
                        value={r.port}
                        onChange={(e) => patch(n.id, { port: e.target.value.replace(/\D/g, "").slice(0, 5) })}
                        inputMode="numeric"
                        disabled={busy}
                        mono
                        className="w-36"
                      />
                      {ref?.kind === "port" && ref.free && r.port !== ref.free && (
                        <Button size="md" onClick={() => patch(n.id, { port: ref.free })}>
                          {t("where.portTakeFree", { free: ref.free })}
                        </Button>
                      )}
                    </div>
                  )}
                  {askBackend && (
                    <div className="flex flex-col gap-2">
                      <span className="flex flex-wrap items-center gap-x-2 text-muted">
                        {t("where.awgFirst", { node: n.name, mode: t(`awg.backend.${mode}`) })}
                        {!r.showBackend && (
                          <button type="button" className="font-bold text-accent-text hover:text-fg" onClick={() => patch(n.id, { showBackend: true })}>
                            {t("where.awgChange")}
                          </button>
                        )}
                      </span>
                      {r.showBackend && <AwgBackendFields mode={mode} onChange={(m) => patch(n.id, { backend: m })} label={t("awg.backend.title")} auto={canPrepare(n)} />}
                    </div>
                  )}
                </div>
              )}
            </li>
          );
        })}
      </ul>
      <div className="flex flex-wrap justify-end gap-2 pt-1">
        <Button variant="ghost" size="md" disabled={busy} onClick={onClose}>
          {phase === "done" ? t("common.close") : t("common.cancel")}
        </Button>
        {phase === "done" && todo.length === 0 ? (
          <Button variant="primary" size="md" onClick={onClose}>
            {t("common.done")}
          </Button>
        ) : (
          <Button variant="primary" size="md" disabled={busy || todo.length === 0} onClick={() => void run(todo)}>
            {busy ? t("where.deploying") : failed.length > 0 && failed.length === todo.length ? t.n("where.retryN", todo.length) : t.n("where.deployN", todo.length)}
          </Button>
        )}
      </div>
    </div>
  );
}
