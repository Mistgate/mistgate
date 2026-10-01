import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { CopyButton } from "@/components/copy-button";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { StatusPill, kindTextClass } from "@/components/ui/status";
import { NodeUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { Icon } from "@/components/ui/icons";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import { canRollbackNode, canUpdateNode, lastUpdateView, manualCommands, nodeStateKey, nodeStateKind, type NodeUpdate, type Updates } from "@/lib/updates";

const colsOwner = "md:grid-cols-[minmax(150px,1.1fr)_minmax(150px,1fr)_150px_minmax(200px,1.5fr)_170px]";
const colsView = "md:grid-cols-[minmax(150px,1.1fr)_minmax(150px,1fr)_150px_minmax(200px,1.5fr)]";

/** An agent that can update but has no crash-loop guard in its service file: only the 5-minute self-rollback protects it. */
const unguarded = (n: NodeUpdate) => n.supportsUpdate && !n.crashGuard;

function Version({ node, fmt }: { node: NodeUpdate; fmt: Fmt }) {
  const t = useT();
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <span className="truncate font-mono text-xs">{node.version || "—"}</span>
      <span className="text-[11px] text-muted">{node.built ? t("up.built", { date: fmt.date(node.built) }) : ""}</span>
    </div>
  );
}

function LastUpdate({ node, fmt }: { node: NodeUpdate; fmt: Fmt }) {
  const t = useT();
  const v = lastUpdateView(t, node.lastUpdate);
  const at = node.lastUpdate?.atUnix;
  return (
    <div className="flex min-w-0 flex-col gap-0.5 text-xs">
      <span className={cx("text-pretty", v.kind === "off" || v.kind === "ok" ? "text-muted" : kindTextClass[v.kind])}>{v.head}</span>
      {v.detail && <span className="leading-snug text-pretty text-muted">{v.detail}</span>}
      {at ? <span className="text-[11px] text-faint">{fmt.ago(at)}</span> : null}
    </div>
  );
}

const manual = (n: NodeUpdate) => n.state === NodeUpdateState.UNSUPPORTED;

function State({ node, open, onToggle }: { node: NodeUpdate; open: boolean; onToggle: () => void }) {
  const t = useT();
  return (
    <div className="flex flex-col items-start gap-1">
      <StatusPill kind={nodeStateKind(node.state)} label={t(nodeStateKey(node.state))} sm />
      {unguarded(node) && <span className={cx("text-[11px] font-semibold", kindTextClass.warn)}>{t("up.noGuard")}</span>}
      {manual(node) && (
        <button
          type="button"
          aria-expanded={open}
          onClick={onToggle}
          className="-mx-1 flex min-h-7 items-center gap-1 rounded-ctl px-1 text-xs font-bold text-accent-text hover:bg-surface-2"
        >
          {t("up.howto.toggle")}
          <Icon name="chevronRight" size={12} className={cx("block flex-none transition-transform duration-200", open ? "-rotate-90" : "rotate-90")} />
        </button>
      )}
    </div>
  );
}

function Command({ value }: { value: string }) {
  return (
    <div className="flex flex-wrap items-start gap-2">
      <code className="min-w-0 flex-[1_1_260px] rounded-field border border-line bg-surface px-2.5 py-2 font-mono text-xs leading-relaxed break-all text-fg select-all">
        {value}
      </code>
      <CopyButton value={value} />
    </div>
  );
}

/** The manual update of one node, as two commands to copy: put the release binary on it, run its install. */
function HowTo({ data, node }: { data: Updates; node: NodeUpdate }) {
  const t = useT();
  const c = manualCommands(data, node);
  return (
    <div className="flex flex-col gap-3 rounded-field border border-line bg-canvas p-3.5">
      <ol className="flex flex-col gap-3">
        <li className="flex flex-col gap-1.5">
          <span className="text-xs font-bold">{t("up.howto.copy")}</span>
          {c.copy ? <Command value={c.copy} /> : <p className="text-xs leading-snug text-pretty text-muted">{t("up.howto.noFile", { file: c.file })}</p>}
          {c.copy && <span className="text-[11px] leading-snug text-muted">{t(c.guessedArch ? "up.howto.copyHintArch" : "up.howto.copyHint")}</span>}
        </li>
        <li className="flex flex-col gap-1.5">
          <span className="text-xs font-bold">{t("up.howto.install")}</span>
          <Command value={c.install} />
        </li>
      </ol>
      <p className="text-[11px] leading-snug text-pretty text-muted">{t("up.howto.after")}</p>
    </div>
  );
}

function Actions({ node, allowUpdate, onUpdate, onRollback }: { node: NodeUpdate; allowUpdate: boolean; onUpdate: () => void; onRollback: () => void }) {
  const t = useT();
  return (
    <div className="flex flex-wrap items-center gap-1.5 md:justify-end">
      {canUpdateNode(node) && (
        <Button variant="secondary" size="md" disabled={!allowUpdate} aria-label={t("up.action.updateAria", { name: node.name })} onClick={onUpdate}>
          {t("up.action.update")}
        </Button>
      )}
      {canRollbackNode(node) && (
        <Button variant="ghost" size="md" aria-label={t("up.action.rollbackAria", { name: node.name })} onClick={onRollback}>
          {t("up.action.rollback")}
        </Button>
      )}
    </div>
  );
}

function Name({ node }: { node: NodeUpdate }) {
  const t = useT();
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <Link to="/nodes/$id" params={{ id: node.nodeId }} className="truncate text-sm font-bold hover:underline">
        {node.name}
      </Link>
      <span className="text-[11px] text-muted">{t("up.nodes.online", { n: node.onlineUsers })}</span>
    </div>
  );
}

/**
 * Every node with its version, update state and the last thing that happened to it. The buttons are the owner's: "Update"
 * sends a rollout of that one node (a retry for a rolled back or failed one), "Roll back" asks the agent for its previous binary.
 */
export function NodeTable({
  nodes,
  data,
  owner,
  allowUpdate,
  onUpdate,
  onRollback,
}: {
  nodes: NodeUpdate[];
  /** The page's data: the bundle and the panel's dist folder the manual commands are built from. */
  data: Updates;
  owner: boolean;
  /** False while a rollout runs or without a trusted bundle: the server would refuse, so the button is off. */
  allowUpdate: boolean;
  onUpdate: (n: NodeUpdate) => void;
  onRollback: (n: NodeUpdate) => void;
}) {
  const t = useT();
  const fmt = useFmt();
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const toggle = (id: string) =>
    setOpen((prev) => {
      const next = new Set(prev);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  const heads = [t("up.nodes.col.node"), t("up.nodes.col.version"), t("up.nodes.col.state"), t("up.nodes.col.last")];
  const byHand = nodes.filter(manual).map((n) => n.name);
  const noGuard = nodes.filter(unguarded).map((n) => n.name);

  return (
    <section className="flex flex-col gap-3">
      <SectionLabel as="h2" icon="network" tone="sky">
        {t("up.nodes.title")}
      </SectionLabel>
      {nodes.length === 0 ? (
        <div className="rounded-card-lg border border-dashed border-line px-6 py-8 text-center text-[13px] text-muted">{t("up.nodes.empty")}</div>
      ) : (
        <>
          <div className="hidden overflow-hidden rounded-card-lg border border-line bg-surface md:block">
            <div className={cx("grid h-10 items-center gap-4 border-b border-line px-5 text-xs font-semibold text-muted", owner ? colsOwner : colsView)}>
              {heads.map((h) => (
                <span key={h}>{h}</span>
              ))}
              {owner && <span />}
            </div>
            {nodes.map((n) => (
              <div key={n.nodeId} className="border-b border-line last:border-b-0">
                <div className={cx("grid min-h-[64px] items-center gap-4 px-5 py-2.5 text-[13px]", owner ? colsOwner : colsView)}>
                  <Name node={n} />
                  <Version node={n} fmt={fmt} />
                  <State node={n} open={open.has(n.nodeId)} onToggle={() => toggle(n.nodeId)} />
                  <LastUpdate node={n} fmt={fmt} />
                  {owner && <Actions node={n} allowUpdate={allowUpdate} onUpdate={() => onUpdate(n)} onRollback={() => onRollback(n)} />}
                </div>
                {open.has(n.nodeId) && (
                  <div className="px-5 pb-4">
                    <HowTo data={data} node={n} />
                  </div>
                )}
              </div>
            ))}
          </div>
          <div className="flex flex-col gap-2 md:hidden">
            {nodes.map((n) => (
              <div key={n.nodeId} className="flex flex-col gap-3 rounded-card border border-line bg-surface p-3.5">
                <div className="flex items-start justify-between gap-2">
                  <Name node={n} />
                  <State node={n} open={open.has(n.nodeId)} onToggle={() => toggle(n.nodeId)} />
                </div>
                <div className="grid grid-cols-2 gap-3">
                  <Version node={n} fmt={fmt} />
                  <LastUpdate node={n} fmt={fmt} />
                </div>
                {open.has(n.nodeId) && <HowTo data={data} node={n} />}
                {owner && (canUpdateNode(n) || canRollbackNode(n)) && <Actions node={n} allowUpdate={allowUpdate} onUpdate={() => onUpdate(n)} onRollback={() => onRollback(n)} />}
              </div>
            ))}
          </div>
        </>
      )}
      {byHand.length > 0 && (
        <Notice title={t("up.manual.title", { names: byHand.join(", ") })}>
          {t("up.manual.body")}
        </Notice>
      )}
      {noGuard.length > 0 && (
        <Notice title={t("up.guard.title", { names: noGuard.join(", ") })}>
          {t("up.guard.body")}
        </Notice>
      )}
    </section>
  );
}
