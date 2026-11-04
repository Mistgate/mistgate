import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { IconChip } from "@/components/ui/icons";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { provisioning } from "@/lib/api";
import { useEdition } from "@/lib/edition";
import { readPref, writePref } from "@/lib/storage";
import { addPanelKey, keyError } from "./node/ssh-keys";

type State = "waiting" | "running" | "ok" | "bad";
type Row = { nodeId: string; name: string; host: string; state: State; reason?: string };

const states: Record<State, { kind: StatusKind; word: MessageKey }> = {
  waiting: { kind: "off", word: "nodes.panelKey.waiting" },
  running: { kind: "busy", word: "nodes.panelKey.installing" },
  ok: { kind: "ok", word: "nodes.panelKey.installed" },
  bad: { kind: "bad", word: "nodes.panelKey.failed" },
};

// the nodes the owner hid the offer for (sorted ids): a node that turns password-only later brings it back
const hiddenPref = "mistgate.panelKeyBulk.hidden";

/**
 * Nodes page, owner only: when live nodes log in with a saved password only, a calm offer to put the panel key on all of
 * them. Only on the owner's click: one step-up, then AddNodeServerKey(panel key) node by node, never two at once (each
 * is an SSH session of several seconds), each row saying how it went; a summary at the end, the access list refreshed.
 */
export function PanelKeyBulk() {
  const t = useT();
  const guard = useStepUp();
  const qc = useQueryClient();
  const { edge } = useEdition();
  const list = useQuery({
    queryKey: ["node-server-access"],
    queryFn: ({ signal }) => provisioning.listNodeServerAccess({}, { signal }),
    staleTime: 60_000,
  });
  const [hidden, setHidden] = useState(() => readPref(hiddenPref) ?? "");
  const [rows, setRows] = useState<Row[] | null>(null);
  const candidates = (list.data?.access ?? []).filter((a) => !a.nodeRetired && a.passwordSaved && a.panelKeyFingerprint === "");
  const ids = candidates.map((a) => a.nodeId).sort().join(",");

  if (!rows && (candidates.length === 0 || hidden === ids)) return null;

  const set = (i: number, row: Partial<Row>) => setRows((r) => r && r.map((x, j) => (j === i ? { ...x, ...row } : x)));

  async function start() {
    const queue: Row[] = candidates.map((a) => ({ nodeId: a.nodeId, name: a.nodeName, host: a.host, state: "waiting" }));
    setRows(queue);
    for (const [i, row] of queue.entries()) {
      set(i, { state: "running" });
      try {
        await guard(() => addPanelKey(row.nodeId));
        set(i, { state: "ok" });
      } catch (error) {
        if (isStepUpCancelled(error)) {
          // the owner said no: what ran stays listed, the rest never starts
          setRows((r) => (r && i > 0 ? r.slice(0, i) : null));
          break;
        }
        set(i, { state: "bad", reason: keyError(error, t, edge) });
      }
    }
    void qc.invalidateQueries({ queryKey: ["node-server-access"] });
    void qc.invalidateQueries({ queryKey: ["node-server-keys"] });
  }

  function hide() {
    writePref(hiddenPref, ids);
    setHidden(ids);
  }

  if (!rows) {
    return (
      <section data-bulk className="flex flex-wrap items-center gap-x-3 gap-y-3 rounded-card border border-line bg-surface px-3.5 py-3">
        <div className="flex min-w-[240px] flex-1 items-start gap-2.5">
          <IconChip icon="key" tone="sky" />
          <div className="flex min-w-0 flex-col gap-0.5">
            <p className="text-[13px] leading-snug font-bold text-pretty">{t.n("nodes.panelKey.banner", candidates.length)}</p>
            <p className="text-xs leading-snug text-pretty text-muted">{t("nodes.panelKey.body")}</p>
          </div>
        </div>
        <div className="flex flex-wrap gap-2 max-sm:w-full">
          <Button variant="secondary" size="md" data-bulk-start className="max-sm:flex-1" onClick={() => void start()}>
            {t("nodes.panelKey.action")}
          </Button>
          <Button variant="ghost" size="md" onClick={hide}>
            {t("nodes.panelKey.hide")}
          </Button>
        </div>
      </section>
    );
  }

  const done = rows.filter((r) => r.state === "ok" || r.state === "bad").length;
  const ok = rows.filter((r) => r.state === "ok").length;
  const finished = done === rows.length;
  return (
    <section data-bulk aria-live="polite" className="flex flex-col gap-3 rounded-card border border-line bg-surface px-3.5 py-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <IconChip icon="key" tone="sky" />
        <p className="me-auto min-w-0 text-[13px] leading-snug font-bold text-pretty">
          {finished ? t.n("nodes.panelKey.summary", rows.length, { ok }) : t("nodes.panelKey.running", { done, n: rows.length })}
        </p>
        {finished && (
          <Button variant="secondary" size="sm" onClick={() => setRows(null)}>
            {t("nodes.panelKey.close")}
          </Button>
        )}
      </div>
      <ul className="flex flex-col">
        {rows.map((row) => {
          const s = states[row.state];
          return (
            <li key={row.nodeId} className="flex flex-col gap-1 border-t border-line py-2.5 last:pb-0">
              <div className="flex items-center gap-3">
                <div className="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-2 gap-y-0.5">
                  <b className="text-[13px] font-bold">{row.name}</b>
                  <span dir="ltr" className="min-w-0 truncate font-mono text-xs text-muted">
                    {row.host}
                  </span>
                </div>
                <StatusPill sm kind={s.kind} label={t(s.word)} />
              </div>
              {row.reason && <p className="text-xs leading-snug text-pretty text-danger-text">{row.reason}</p>}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
