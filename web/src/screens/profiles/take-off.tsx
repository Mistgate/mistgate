import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { CopyButton } from "@/components/copy-button";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { StatusDot, StatusPill } from "@/components/ui/status";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { InboundState, type Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import type { ListNodesResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { profiles as profilesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { agentLinked, nodeKind } from "@/lib/node-status";
import type { Plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";
import { InboundErrorNote, states } from "@/screens/node/profiles";
import { alsoRemovesText } from "@/screens/node/relays";
import { useGo } from "@/screens/users/nav";
import { useTx } from "@/screens/users/t";
import { Check } from "@/screens/users/ui";

// Taking a profile off nodes, several at once: the nodes list of the profile page ("Where it runs") has a choosing mode,
// and the delete window of a profile that is on nodes lists the same rows with the same boxes. One RemoveProfileFromNodes call
// does it all: the panel removes the servers in one transaction (a node that is not connected drops the profile when it
// connects again), and with the profile deleted too when the list covers every node it is on.

type NodeRow = Plain<ListNodesResponse>["nodes"][number];
export type NodeOf = ReadonlyMap<string, NodeRow>;

/** The ticked servers. Not touched yet = every one (`all`) or none. A server that is gone from the list is not ticked any more. */
export function usePick(inbounds: readonly Inbound[], all: boolean) {
  const [touched, setTouched] = useState<string[] | null>(null);
  const present = new Set(inbounds.map((i) => i.id));
  const on = touched === null ? (all ? inbounds.map((i) => i.id) : []) : touched.filter((id) => present.has(id));
  return {
    on,
    has: (id: string) => on.includes(id),
    toggle: (id: string) => setTouched(on.includes(id) ? on.filter((x) => x !== id) : [...on, id]),
    allOn: inbounds.length > 0 && on.length === inbounds.length,
    setAll: (v: boolean) => setTouched(v ? inbounds.map((i) => i.id) : []),
    reset: () => setTouched(null),
  };
}
export type Pick = ReturnType<typeof usePick>;

/** One server of the profile: flag, node, port, where it runs. With `pick` the whole row is a box to tick. */
export function InboundRow({ inbound: i, node, pick }: { inbound: Inbound; node?: NodeRow; pick?: { on: boolean; toggle: () => void; disabled?: boolean } }) {
  const t = useTx();
  const s = states[i.state] ?? states[InboundState.UNSPECIFIED];
  return (
    <li
      className={cx("flex flex-col gap-1.5 py-1.5", pick && "-mx-1.5 cursor-pointer rounded-lg px-1.5 transition-colors hover:bg-surface-2", pick?.on && "bg-surface-2")}
      onClick={pick && !pick.disabled ? pick.toggle : undefined}
    >
      <div className="flex min-w-0 items-center gap-2">
        {pick && <Check checked={pick.on} onCheckedChange={pick.toggle} label={i.nodeName} disabled={pick.disabled} />}
        {pick && node && <StatusDot kind={nodeKind(node)} />}
        {node?.countryCode && <Flag code={node.countryCode} size={14} />}
        {pick ? (
          <b className="min-w-0 truncate text-[13px]">{i.nodeName}</b>
        ) : (
          <Link to="/nodes/$id" params={{ id: i.nodeId }} search={{ tab: "profiles" }} className="min-w-0 truncate text-[13px] font-bold text-accent-text hover:underline">
            {i.nodeName}
          </Link>
        )}
        {i.port > 0 && <span className="flex-none font-mono text-[11px] text-muted">:{i.port}</span>}
        {node?.location && !pick && <span className="hidden min-w-0 truncate text-xs text-muted sm:inline">{node.location}</span>}
        <span className="ms-auto flex-none">
          <StatusPill kind={s.kind} label={t(s.word)} sm />
        </span>
      </div>
      {i.lastError && <InboundErrorNote inbound={i} />}
    </li>
  );
}

/** "All nodes" with how many are ticked: the one line above the rows in the dialog and under the card's header. */
export function PickAll({ pick, total, className }: { pick: Pick; total: number; className?: string }) {
  const t = useTx();
  return (
    <div className={cx("flex min-h-9 cursor-pointer items-center gap-2.5 text-[13px] font-bold", className)} onClick={() => pick.setAll(!pick.allOn)}>
      <Check checked={pick.allOn} onCheckedChange={(v) => pick.setAll(v)} label={t("where.allNodes")} />
      {t("where.allNodes")}
      <span className="font-mono text-xs font-semibold text-muted">{t("where.pickedOf", { n: pick.on.length, m: total })}</span>
    </div>
  );
}

/** What the ticked nodes lose, in words: who is cut off, the relays that go with them, the nodes that are not connected now. */
function Consequences({ chosen, nodeOf, deleting }: { chosen: readonly Inbound[]; nodeOf: NodeOf; deleting?: boolean }) {
  const t = useTx();
  const relays = useQuery(relaysQuery("")).data ?? [];
  const ids = new Set(chosen.map((i) => i.id));
  const also = alsoRemovesText(t, relays.filter((r) => ids.has(r.inboundId)));
  const away = chosen.filter((i) => {
    const n = nodeOf.get(i.nodeId);
    return n && !agentLinked(n.status);
  }).length;
  return (
    <>
      <Notice tone="danger" className="items-start">
        <span className="flex flex-col gap-1">
          <span>{t(deleting ? "where.offBodyDelete" : "where.offBody")}</span>
          {also && <span>{also}</span>}
        </span>
      </Notice>
      {away > 0 && <Notice tone="quiet">{t.n("where.offOffline", away)}</Notice>}
    </>
  );
}

/** The call, and what the screens that show the profile must read again afterwards. */
export function useTakeOff(profileId: string) {
  const qc = useQueryClient();
  return async (nodeIds: string[], remove?: { name: string }) => {
    const res = await profilesApi.removeProfileFromNodes({ profileId, nodeIds, deleteProfile: !!remove, confirmName: remove?.name ?? "" });
    await Promise.all(["profiles", "nodes", "node", "groups", "users", "subs", "relays"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
    return res;
  };
}

/** The ticked nodes of the card, asked once more: what happens, and the red button. */
export function TakeOffDialog({ open, onOpenChange, profile, chosen, nodeOf, onDone }: { open: boolean; onOpenChange: (o: boolean) => void; profile: { id: string; name: string }; chosen: readonly Inbound[]; nodeOf: NodeOf; onDone: () => void }) {
  const t = useTx();
  const toast = useToast();
  const takeOff = useTakeOff(profile.id);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  async function run() {
    setBusy(true);
    setError(null);
    try {
      const res = await takeOff(chosen.map((i) => i.nodeId));
      toast(t.n("where.offDone", res.removed));
      onOpenChange(false);
      onDone();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open={open}
      onOpenChange={(o) => !busy && onOpenChange(o)}
      title={t("where.offTitle", { name: profile.name })}
      description={t("where.offNodes", { nodes: chosen.map((i) => i.nodeName).join(", ") })}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={busy || chosen.length === 0} onClick={() => void run()}>
            {t.n("where.offN", chosen.length)}
          </Button>
        </>
      }
    >
      <Consequences chosen={chosen} nodeOf={nodeOf} />
      {error != null && <Notice tone="danger">{errorText(error, t)}</Notice>}
    </Modal>
  );
}

/**
 * The delete window of a profile that is on nodes. Every node is ticked at first; with all of them ticked the red button takes
 * the profile off all and deletes it (the name typed, as for a person), with some ticked it only takes it off those and the
 * window stays with the nodes that are left.
 */
export function DeleteDeployedDialog({ open, onOpenChange, profile, inbounds }: { open: boolean; onOpenChange: (o: boolean) => void; profile: { id: string; name: string }; inbounds: readonly Inbound[] }) {
  const t = useTx();
  const toast = useToast();
  const go = useGo();
  const takeOff = useTakeOff(profile.id);
  const nodeOf: NodeOf = new Map((useQuery(nodesQuery).data?.nodes ?? []).map((n) => [n.id, n]));
  const pick = usePick(inbounds, true);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const chosen = inbounds.filter((i) => pick.has(i.id));
  const all = pick.allOn;
  const spaced = (s: string) => s.trim().replace(/\s+/g, " ");
  const typedOk = !all || spaced(typed) === spaced(profile.name);

  async function run() {
    setBusy(true);
    setError(null);
    try {
      const res = await takeOff(chosen.map((i) => i.nodeId), all ? { name: profile.name } : undefined);
      if (res.profileDeleted) {
        toast(t("profiles.deleted"));
        onOpenChange(false);
        go("/profiles", { replace: true });
      } else {
        // the nodes that are left stay in the window, all ticked again
        toast(t.n("where.offDone", res.removed));
        pick.reset();
        setTyped("");
      }
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (busy) return;
        if (!o) {
          setError(null);
          setTyped("");
        }
        onOpenChange(o);
      }}
      title={t("profiles.deleteT", { name: profile.name })}
      description={t.n("profiles.deployedOn", inbounds.length)}
      className="md:max-w-[560px]"
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={busy || chosen.length === 0 || !typedOk} onClick={() => void run()}>
            {all ? t("profiles.offAndDelete") : chosen.length === 0 ? t("where.offNone") : t.n("where.offN", chosen.length)}
          </Button>
        </>
      }
    >
      {/* first, so that a refusal is seen without scrolling the long window */}
      {error != null && <Notice tone="danger">{errorText(error, t)}</Notice>}
      <div className="flex flex-col gap-1">
        {inbounds.length > 1 && <PickAll pick={pick} total={inbounds.length} className="px-0" />}
        <ul className="flex max-h-[min(34vh,320px)] flex-col divide-y divide-line overflow-y-auto rounded-field border border-line px-3">
          {inbounds.map((i) => (
            <InboundRow key={i.id} inbound={i} node={nodeOf.get(i.nodeId)} pick={{ on: pick.has(i.id), toggle: () => pick.toggle(i.id), disabled: busy }} />
          ))}
        </ul>
      </div>
      {chosen.length > 0 && <Consequences chosen={chosen} nodeOf={nodeOf} deleting={all} />}
      {all && (
        <TextField
          aria-label={t("users.deleteType", { name: profile.name })}
          hint={
            <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span className="whitespace-pre-wrap">{t("users.deleteType", { name: profile.name })}</span>
              <CopyButton value={profile.name} />
            </span>
          }
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          disabled={busy}
          autoComplete="off"
          autoCapitalize="off"
          spellCheck={false}
        />
      )}
    </Modal>
  );
}
