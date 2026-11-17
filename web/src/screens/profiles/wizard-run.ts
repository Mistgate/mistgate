// The last step of the wizard: the profile is made, put into the chosen groups and on the chosen nodes, one row for each, and
// every node's inbound is followed until the node says it runs. The calls are the old form's: CreateProfile, then the groups'
// own calls, then CreateInbound per node (the AmneziaWG backend of a node's first AmneziaWG profile is saved first). A node that
// refuses does not undo the others; a row that failed can be tried again with the port or the domain typed in it.

import { Code, ConnectError } from "@connectrpc/connect";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { markFresh } from "@/components/fx/registry";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { PrepareAwgOutcome } from "@/gen/mistgate/admin/v1/node_pb";
import { groups as groupsApi, nodes as nodesApi, profiles } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { inboundErrorKind, inboundErrorText } from "@/lib/inbound-error";
import { portNotes } from "@/lib/port-check";
import { asMode } from "@/screens/node/awg-backend";
import { fieldErrors } from "@/screens/users/rpc";
import { useTx } from "@/screens/users/t";
import { refusalOf, type Refusal } from "./deploy";
import type { Fix } from "./wizard-check";

export type Phase = "wait" | "put" | "apply" | "ok" | "bad" | "off";

/** What a failed row can be mended with: a port, a domain, or nothing but another try. */
export type Mend = "port" | "domain" | "";

export type RunNode = {
  id: string;
  name: string;
  phase: Phase;
  text: string;
  inboundId?: string;
  /** The row's fields: the port the server proved free, what was typed. */
  fix: Fix;
  mend: Mend;
  free?: string;
  lossy?: string;
  notes?: string[];
  since: number;
};

export type RunState = {
  profile: { phase: "busy" | "ok" | "bad"; text: string; id: string; fields?: boolean };
  groups: { phase: "none" | "wait" | "busy" | "ok" | "bad"; names: string[]; text: string };
  nodes: RunNode[];
};

/** The node the run puts the profile on: what the window of "Put on nodes" knew of it. */
export type RunTarget = { id: string; name: string; address: string; linked: boolean; awgMode: string; firstAwg: boolean; fix: Fix; canPrepare: boolean };

export type RunInput = {
  protocol: string;
  name: string;
  settingsJson: string;
  groups: { id: string; name: string; profileIds: string[] }[];
  /** No group exists yet and the owner wants "Everyone": it is made with the profile in it. */
  everyone: boolean;
  nodes: RunTarget[];
};

export const emptyRun = (nodes: { id: string; name: string }[] = [], groupNames: string[] = []): RunState => ({
  profile: { phase: "busy", text: "", id: "" },
  groups: { phase: groupNames.length > 0 ? "wait" : "none", names: groupNames, text: "" },
  nodes: nodes.map((n) => ({ id: n.id, name: n.name, phase: "wait", text: "", fix: {}, mend: "", since: 0 })),
});

/** Everything has an answer: the profile is made (or refused), each node runs, failed, or waits for a node that is not on line. */
export const runDone = (r: RunState) => r.profile.phase === "bad" || (r.profile.phase === "ok" && r.groups.phase !== "busy" && r.groups.phase !== "wait" && r.nodes.every((n) => n.phase === "ok" || n.phase === "bad" || n.phase === "off"));

/** How far the run is, 0..100: the profile, the groups, and two steps for each node (put on, answered). */
export function progressOf(r: RunState): number {
  const total = 2 + 2 * r.nodes.length;
  const units = (r.profile.phase === "busy" ? 0 : 1) + (r.groups.phase === "busy" || r.groups.phase === "wait" ? 0 : 1) + r.nodes.reduce((a, n) => a + (n.phase === "wait" ? 0 : n.phase === "put" ? 0.5 : n.phase === "apply" ? 1 : 2), 0);
  return Math.min(100, Math.round((units / total) * 100));
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const POLL_MS = 1500;
/** A node that has not answered in this long keeps its row as "still applying"; the profile's page goes on following it. */
const PATIENCE_MS = 120_000;

/** The run: `start` makes everything, `retry` tries one node's row again. The state is the screen's whole fifth step. */
export function useRun(onCreated: (profileId: string) => void) {
  const t = useTx();
  const qc = useQueryClient();
  const [run, setRun] = useState<RunState>(() => emptyRun());
  const live = useRef(run);
  const patch = (f: (r: RunState) => RunState) => {
    live.current = f(live.current);
    setRun(live.current);
  };
  const row = (id: string, p: Partial<RunNode>) => patch((r) => ({ ...r, nodes: r.nodes.map((n) => (n.id === id ? { ...n, ...p } : n)) }));
  const input = useRef<RunInput | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => void (alive.current = false);
  }, []);

  const refresh = async () => {
    const keys = ["profiles", "nodes", "node", "groups", "users", "subs"];
    await Promise.all(keys.map((k) => qc.invalidateQueries({ queryKey: [k] })));
  };

  /** One node: its AmneziaWG backend if it is the first AmneziaWG there, then the inbound. The answer of the node comes from `follow`. */
  async function put(profileId: string, n: RunTarget, protocol: string, fix: Fix) {
    row(n.id, { phase: "put", text: "", since: Date.now(), fix });
    try {
      const mode = fix.backend ?? asMode(n.awgMode);
      if (protocol === "awg" && n.firstAwg && mode !== asMode(n.awgMode)) {
        if (mode === "kernel" && n.canPrepare) {
          const plan = await nodesApi.prepareAwgKernel({ nodeId: n.id, confirm: false });
          if (plan.outcome !== PrepareAwgOutcome.READY) throw new ConnectError("kernel_not_ready", Code.FailedPrecondition);
        }
        await nodesApi.updateNode({ nodeId: n.id, awgBackend: mode });
      }
      const added = await profiles.createInbound({
        profileId,
        nodeId: n.id,
        portOverride: fix.port ? Number(fix.port) : 0,
        tlsServerNameOverride: protocol === "awg" ? "" : (fix.sni ?? "").trim(),
        ...(fix.anyway && { allowLossyPort: true }),
      });
      if (added.inbound) markFresh("inbound", added.inbound.id);
      row(n.id, { phase: n.linked ? "apply" : "off", inboundId: added.inbound?.id, text: n.linked ? "" : t("wiz.res.offline"), notes: portNotes(t, added.warnings), mend: "" });
    } catch (e) {
      const ref: Refusal = refusalOf(e, t, n, protocol);
      if (ref.kind === "already") return row(n.id, { phase: "ok", text: t("wiz.run.ok") });
      row(n.id, { phase: "bad", text: ref.text, mend: ref.kind === "port" ? "port" : ref.kind === "domain" ? "domain" : "", free: ref.kind === "port" ? ref.free : undefined, lossy: ref.kind === "port" ? ref.lossy : undefined, fix: ref.kind === "port" && ref.free ? { ...fix, port: ref.free } : fix });
    }
  }

  /** Reads the profile's inbounds once: a node that runs is "ok", one that failed says why. Returns whether any row still waits. */
  async function follow(profileId: string, protocol: string): Promise<boolean> {
    const got = await profiles.getProfile({ profileId });
    for (const n of live.current.nodes) {
      if (n.phase !== "apply") continue;
      const i = got.inbounds.find((x) => x.nodeId === n.id);
      if (i?.state === InboundState.ACTIVE) row(n.id, { phase: "ok", text: t("wiz.run.ok") });
      else if (i?.state === InboundState.FAILED) {
        const kind = i.lastError ? inboundErrorKind(i.lastError) : "other";
        row(n.id, { phase: "bad", inboundId: i.id, text: t("wiz.run.failed", { why: inboundErrorText(t, i.lastError, { port: i.port, protocol }) }), mend: kind === "port" ? "port" : kind === "cert" ? "domain" : "" });
      } else if (Date.now() - n.since > PATIENCE_MS) row(n.id, { phase: "off", text: t("wiz.run.slow") });
    }
    return live.current.nodes.some((n) => n.phase === "apply");
  }

  async function watch(profileId: string, protocol: string) {
    try {
      while (alive.current && (await follow(profileId, protocol))) await sleep(POLL_MS);
    } catch {
      /* the page of the profile goes on following it */
    }
    await refresh();
  }

  async function start(inp: RunInput) {
    input.current = inp;
    patch(() => ({ ...emptyRun(inp.nodes, inp.groups.map((g) => g.name)), groups: { phase: inp.groups.length > 0 || inp.everyone ? "wait" : "none", names: inp.everyone ? [t("users.everyone")] : inp.groups.map((g) => g.name), text: "" } }));
    let id: string;
    try {
      const res = await profiles.createProfile({ protocol: inp.protocol, name: inp.name.trim(), settingsJson: inp.settingsJson });
      id = res.profile!.id;
      onCreated(id);
      markFresh("profile", id);
      patch((r) => ({ ...r, profile: { phase: "ok", text: "", id } }));
    } catch (e) {
      // the form went wrong: the settings or the name; the row says it and "Fix" goes back to them
      const fe = fieldErrors(e);
      const text = fe.length > 0 ? fe.map((x) => x.message).join(". ") : errorText(e, t);
      patch((r) => ({ ...r, profile: { phase: "bad", text, id: "", fields: true }, groups: { ...r.groups, phase: "none" }, nodes: [] }));
      return;
    }
    // the groups decide who gets it: cheap, and before the nodes
    if (inp.everyone || inp.groups.length > 0) {
      patch((r) => ({ ...r, groups: { ...r.groups, phase: "busy" } }));
      try {
        if (inp.everyone) await groupsApi.createGroup({ name: t("users.everyone"), profileIds: [id] });
        for (const g of inp.groups) await groupsApi.updateGroup({ groupId: g.id, profileIds: { values: [...g.profileIds, id] } });
        patch((r) => ({ ...r, groups: { ...r.groups, phase: "ok" } }));
      } catch (e) {
        patch((r) => ({ ...r, groups: { ...r.groups, phase: "bad", text: errorText(e, t) } })); // the profile is there: the rest of the way goes on
      }
    }
    await refresh();
    for (const n of inp.nodes) if (alive.current) await put(id, n, inp.protocol, n.fix);
    if (live.current.nodes.length > 0) await watch(id, inp.protocol);
  }

  /** One row again: an inbound that failed on the node is changed (its port, its domain), one that was refused is asked for again. */
  async function retry(nodeId: string, fix: Fix) {
    const inp = input.current;
    const n = live.current.nodes.find((x) => x.id === nodeId);
    if (!inp || !n) return;
    const id = live.current.profile.id;
    if (n.inboundId) {
      row(nodeId, { phase: "put", text: "", since: Date.now(), fix });
      try {
        await profiles.updateInbound({ inboundId: n.inboundId, ...(fix.port && { portOverride: Number(fix.port) }), ...(fix.sni && { tlsServerNameOverride: fix.sni.trim() }), enabled: true, ...(fix.anyway && { allowLossyPort: true }) });
        row(nodeId, { phase: "apply", text: "" });
      } catch (e) {
        const ref = refusalOf(e, t, inp.nodes.find((x) => x.id === nodeId) ?? { name: n.name, address: "" }, inp.protocol);
        row(nodeId, { phase: "bad", text: ref.text, mend: ref.kind === "port" ? "port" : ref.kind === "domain" ? "domain" : "" });
      }
    } else {
      const target = inp.nodes.find((x) => x.id === nodeId);
      if (target) await put(id, target, inp.protocol, fix);
    }
    await watch(id, inp.protocol);
  }

  return { run, start, retry };
}
