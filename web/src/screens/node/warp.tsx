import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Chip } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { StatusDot, StatusPill } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { warp as warpApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { useNow } from "@/lib/time";
import { ageParts, probeLook, safeHttpUrl, sourceWord, warpPill, warpTrouble, type ProbeLook } from "@/lib/warp";
import { attentionWords, describeWarpError, warpCheckFailed } from "@/lib/warp-error";
import { Panel } from "@/screens/users/ui";
import { QueryError } from "@/components/ui/query-error";
import { useTx } from "@/screens/users/t";
import { warpError, warpQuery, type WarpData } from "./warp-model";
import { DeleteWarpDialog, ImportWarpDialog, PauseWarpDialog, RegisterWarpDialog } from "./warp-dialogs";

function useAge() {
  const t = useTx();
  const now = Math.floor(useNow(5_000) / 1000);
  return (unix: number) => {
    const a = ageParts(unix, now);
    if (!a) return t("warp.never");
    if (a.unit === "s") return t("users.now");
    return t(`users.ago.${a.unit}` as "users.ago.m", { n: a.n });
  };
}

/** "4 s ago" for a check (the checks come every 5-30 s, so "now" would say nothing), minutes and more as everywhere else. */
function useCheckedAgo() {
  const t = useTx();
  const age = useAge();
  const now = Math.floor(useNow(5_000) / 1000);
  return (unix: number) => {
    const a = ageParts(unix, now);
    return a?.unit === "s" ? t("warp.secAgo", { n: a.n }) : age(unix);
  };
}

/** How long the card glows after a link brought the owner to it (#warp). */
const glowMs = 2000;

/**
 * WARP of a node: the account (registered here or imported), its state and probes, and what the owner can do with
 * it. The panel talks to Cloudflare only when a button here is pressed; the card says so and links the terms.
 */
export function WarpCard(props: { nodeId: string; nodeName: string; retired: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  // every "Open WARP" links here (?tab=settings#warp): bring the card into view and light it up for a moment
  const [glow, setGlow] = useState(() => window.location.hash === "#warp");
  useEffect(() => {
    if (window.location.hash !== "#warp") return;
    ref.current?.scrollIntoView?.({ block: "start", behavior: "smooth" });
    const id = window.setTimeout(() => setGlow(false), glowMs);
    return () => window.clearTimeout(id);
  }, []);
  return (
    <div
      id="warp"
      ref={ref}
      data-glow={glow || undefined}
      className={cx("min-w-0 scroll-mt-4 rounded-card-lg transition-shadow duration-500", glow && "shadow-[0_0_0_2px_var(--accent),0_0_0_6px_var(--accent-soft)]")}
    >
      <WarpCardBody {...props} />
    </div>
  );
}

type WarpDialog = "register" | "again" | "import" | "delete" | "pause" | null;

function WarpCardBody({ nodeId, nodeName, retired }: { nodeId: string; nodeName: string; retired: boolean }) {
  const t = useTx();
  const q = useQuery(warpQuery(nodeId));
  const [dialog, setDialog] = useState<WarpDialog>(null);
  const d = q.data;
  // a poll that failed keeps the card it had: only a card with nothing to show says so
  if (!d)
    return (
      <Panel title="WARP" icon="bolt" tone="sky">
        {q.isError ? <QueryError error={q.error} onRetry={() => void q.refetch()} /> : <p className="text-sm text-muted">{t("common.loading")}</p>}
      </Panel>
    );
  const close = (o: boolean) => !o && setDialog(null);
  return (
    <Panel title="WARP" icon="bolt" tone="sky" aside={d.account ? <SourceChips d={d} /> : undefined}>
      {d.account ? (
        <Account d={d} nodeId={nodeId} retired={retired} onDialog={setDialog} />
      ) : (
        <Empty d={d} retired={retired} onRegister={() => setDialog("register")} onImport={() => setDialog("import")} />
      )}
      <Honest tosUrl={d.tosUrl} />
      <RegisterWarpDialog open={dialog === "register" || dialog === "again"} again={dialog === "again"} onOpenChange={close} nodeId={nodeId} tosUrl={d.tosUrl} onImport={() => setDialog("import")} />
      <ImportWarpDialog open={dialog === "import"} onOpenChange={close} nodeId={nodeId} />
      <PauseWarpDialog open={dialog === "pause"} onOpenChange={close} nodeId={nodeId} nodeName={nodeName} inbounds={d.inbounds} />
      <DeleteWarpDialog open={dialog === "delete"} onOpenChange={close} nodeId={nodeId} nodeName={nodeName} hasToken={!!d.account?.hasToken} />
    </Panel>
  );
}

function SourceChips({ d }: { d: WarpData }) {
  const t = useTx();
  const a = d.account!;
  return (
    <span className="flex flex-wrap justify-end gap-1.5">
      <Chip>{t(sourceWord[a.source])}</Chip>
      {a.accountType && <Chip mono>{a.accountType}</Chip>}
      {d.health?.colo && <Chip mono>{d.health.colo}</Chip>}
    </span>
  );
}

function Empty({ d, retired, onRegister, onImport }: { d: WarpData; retired: boolean; onRegister: () => void; onImport: () => void }) {
  const t = useTx();
  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <StatusPill kind="off" label={t("warp.state.none")} sm />
      </div>
      <p className="text-[13px] leading-normal text-pretty">{t("warp.empty")}</p>
      {!d.agentSupports && (
        <Notice>
          {t("warp.agentOld")}{" "}
          <Link to="/updates" className="font-bold underline decoration-dotted underline-offset-2">
            {t("warp.openUpdates")}
          </Link>
        </Notice>
      )}
      <div className="flex flex-wrap gap-2">
        <Button variant="primary" size="md" disabled={retired || !d.agentSupports} onClick={onRegister}>
          {t("warp.enable")}
        </Button>
        <Button variant="secondary" size="md" disabled={retired || !d.agentSupports} onClick={onImport}>
          {t("warp.import")}
        </Button>
      </div>
    </div>
  );
}

function Account({ d, nodeId, retired, onDialog }: { d: WarpData; nodeId: string; retired: boolean; onDialog: (dialog: WarpDialog) => void }) {
  const t = useTx();
  const fmt = useFmt();
  const age = useAge();
  const checkedAgo = useCheckedAgo();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const a = d.account!;
  const h = d.health;
  const [busy, setBusy] = useState<"" | "refresh" | "resume" | "restart">("");
  const now = Math.floor(useNow(30_000) / 1000);
  // the account decides "paused" (the node may not have reported it yet); the node's own state decides the rest
  const state = !a.enabled ? WarpState.DISABLED : (h?.state ?? WarpState.UNKNOWN);
  const trouble = warpTrouble(d);
  const refreshAll = () => Promise.all([qc.invalidateQueries({ queryKey: ["warp", nodeId] }), qc.invalidateQueries({ queryKey: ["node", nodeId] })]);

  async function run(what: "refresh" | "resume" | "restart", call: () => Promise<unknown>, done: (r: unknown) => string) {
    setBusy(what);
    try {
      const r = await guard(call);
      await refreshAll();
      toast(done(r));
    } catch (e) {
      if (!isStepUpCancelled(e)) toast.error(warpError(e, t));
    } finally {
      setBusy("");
    }
  }

  const probe = (label: string, look: ProbeLook) => (
    <span className="flex items-center gap-1.5 text-xs">
      <StatusDot kind={look.kind} />
      <span className="text-muted">{label}</span>
      <b className={look.kind === "warn" ? "text-warn-text" : look.kind === "bad" ? "text-danger-text" : undefined}>
        {look.word ? t(look.word) : "—"}
        {look.failure && ` · ${look.httpStatus !== null ? t(look.failure, { status: look.httpStatus }) : t(look.failure)}`}
        {!look.failure && look.ms !== null && ` · ${fmt.num(look.ms / 1000, 1)} ${t("warp.sec")}`}
      </b>
    </span>
  );
  const fact = (label: string, value: ReactNode) => (
    <div className="flex min-w-0 flex-col gap-[3px]">
      <span className="text-[11px] text-muted">{label}</span>
      {/* wraps on a phone rather than cutting the traffic or the endpoint off */}
      <span className="font-mono text-[13px] break-words">{value}</span>
    </div>
  );
  const reported = h && h.reportedUnix > 0;
  // What the LATEST check found (the agent clears last_error when a check passes): never the last success, never a stale failure.
  // A paused account has no checks; an agent that predates the per-probe results gives only last_error and the two flags.
  const live = !!h && a.enabled;
  const checked = !!h && h.checkedUnix > 0;
  const failing = live && (warpCheckFailed(h.lastError) || h.probeCloudflare?.ok === false || h.probeOther?.ok === false);
  const pill = warpPill(state, failing);
  const words = live ? describeWarpError(t, h.lastError) : null;
  const attention = d.needsAttention ? attentionWords(t, d.attentionReason) : null;
  const checkedUnix = checked ? h.checkedUnix : reported ? h.reportedUnix : 0;
  // the tunnel is down and the account is alive: say for how long, in one line, above the agent's own words
  // ("0 min" says nothing: under a minute it is just "failing")
  const downFor = trouble?.kind === "down" ? now - trouble.since : 0;
  const downLine =
    trouble?.kind === "down"
      ? [
          trouble.since <= 0 ? t("warp.down.never") : downFor < 60 ? t("warp.down.now") : t("warp.down.since", { duration: fmt.duration(downFor) }),
          trouble.checks > 0 ? `(${t.n("warp.down.checks", trouble.checks)})` : "",
        ]
          .filter(Boolean)
          .join(" ")
      : "";
  // what only a curious owner needs: under "Details", not next to the status
  const details: [string, ReactNode][] = [];
  if (h?.backend) details.push([t("warp.detail.backend"), h.backend]);
  if (reported && h.warpFlag) details.push([t("warp.detail.flag"), `warp=${h.warpFlag}`]);
  if (a.useReserved) details.push([t("warp.detail.reserved"), t("warp.detail.reservedOn")]);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <StatusPill kind={pill.kind} label={t(pill.word)} sm />
      </div>

      {d.needsAttention && !(d.attentionReason === "down_after_ladder" && downLine) && (
        <Notice tone="danger" title={t("warp.attention")}>
          {attention ?? t("warp.attentionBody")}
          {!attention && d.attentionReason && <span className="mt-1 block font-mono text-[11px] break-words text-muted">{d.attentionReason}</span>}
          {/* revoked: what the node saw, small; its own advice would argue with "Register again" */}
          {trouble?.kind === "revoked" && words?.head && <span className="mt-1 block text-muted">{words.head}</span>}
        </Notice>
      )}
      {a.hasToken === false && <p className="text-[11px] leading-snug text-muted">{t("warp.noToken")}</p>}
      {d.applyError && <Notice tone="danger">{d.applyError}</Notice>}
      {!d.applyError && d.pendingApply && <Notice>{t("warp.pending")}</Notice>}
      {!d.agentSupports && <Notice>{t("warp.agentOld")}</Notice>}
      {(words || downLine) && trouble?.kind !== "revoked" && (
        <Notice tone={state === WarpState.DOWN || state === WarpState.UNAVAILABLE ? "danger" : "warn"} title={downLine || words?.head || undefined}>
          {downLine && words?.head && <span className="block">{words.head}</span>}
          {words?.more}
          {downLine && d.attentionReason === "down_after_ladder" && <span className="mt-1 block">{t("warp.attentionBody")}</span>}
          {words?.ladder && <span className="mt-1 block text-muted">{words.ladder}</span>}
          {words?.raw && <span className="mt-1 block font-mono text-[11px] break-words text-muted">{words.raw}</span>}
        </Notice>
      )}

      <div className="grid grid-cols-2 gap-x-4 gap-y-3 sm:grid-cols-3">
        {fact(t("warp.handshake"), reported ? age(h.lastHandshakeUnix) : "—")}
        {fact(t("warp.endpoint"), h?.endpoint || a.endpointV4 || "—")}
        {fact(t("warp.traffic"), reported ? `↓ ${fmt.bytes(h.rxBytes)} · ↑ ${fmt.bytes(h.txBytes)}` : "—")}
      </div>
      <div className="flex flex-wrap items-center gap-x-5 gap-y-1.5">
        {probe(t("warp.probe.a"), reported ? probeLook(h.probeCloudflare, h.probeCloudflareOk, checked) : probeLook(undefined, undefined, false))}
        {probe(t("warp.probe.b"), reported ? probeLook(h.probeOther, h.probeOtherOk, checked) : probeLook(undefined, undefined, false))}
        {reported && h.consecutiveFailures > 0 && !downLine && <span className="text-xs text-warn-text">{t.n("warp.failures", h.consecutiveFailures)}</span>}
      </div>
      {checkedUnix > 0 && <p className="text-[11px] text-muted">{t("warp.checked", { ago: checkedAgo(checkedUnix) })}</p>}
      {details.length > 0 && (
        <details className="group text-xs">
          <summary className="flex w-fit cursor-pointer list-none items-center gap-1 font-bold text-muted select-none hover:text-fg [&::-webkit-details-marker]:hidden">
            {t("warp.details")}
            <span aria-hidden className="transition-transform duration-200 group-open:rotate-90">›</span>
          </summary>
          <dl className="mt-2 grid grid-cols-[max-content_minmax(0,1fr)] gap-x-3 gap-y-1.5">
            {details.map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="text-muted">{k}</dt>
                <dd>
                  <Chip mono>{v}</Chip>
                </dd>
              </div>
            ))}
          </dl>
        </details>
      )}

      <div className="flex flex-wrap gap-2 border-t border-line pt-3">
        {/* Always offered while there is an account: a slow but "up" exit needs them too, and without them the only way to a
            fresh device was Delete + Register, which leaves the node without WARP in between. The trouble that calls for one
            makes it the primary button. */}
        <Button variant={trouble?.kind === "revoked" ? "primary" : "secondary"} size="sm" disabled={!!busy || retired} onClick={() => onDialog("again")}>
          {t("warp.reregister")}
        </Button>
        {a.enabled && trouble?.kind !== "revoked" && (
          <Button
            variant={trouble?.kind === "down" ? "primary" : "secondary"}
            size="sm"
            disabled={!!busy || retired}
            onClick={() =>
              void run("restart", () => warpApi.restartWarp({ nodeId }), (r) => t((r as { confirmed?: boolean }).confirmed === false ? "warp.restartedUnconfirmed" : "warp.restarted"))
            }
          >
            {busy === "restart" ? t("warp.restarting") : t("warp.restart")}
          </Button>
        )}
        <Button
          variant="secondary"
          size="sm"
          disabled={!!busy || retired || !a.hasToken}
          title={a.hasToken ? undefined : t("warp.noToken")}
          onClick={() => void run("refresh", () => warpApi.refreshWarp({ nodeId }), () => t("warp.refreshed"))}
        >
          {t("warp.refresh")}
        </Button>
        {a.enabled ? (
          <Button variant="secondary" size="sm" disabled={!!busy || retired} onClick={() => onDialog("pause")}>
            {t("warp.pause")}
          </Button>
        ) : (
          <Button
            variant={trouble ? "secondary" : "primary"}
            size="sm"
            disabled={!!busy || retired}
            onClick={() => void run("resume", () => warpApi.setWarpEnabled({ nodeId, enabled: true }), () => t("warp.resumed"))}
          >
            {t("warp.resume")}
          </Button>
        )}
        <Button variant="danger" size="sm" disabled={!!busy} onClick={() => onDialog("delete")} className="sm:ml-auto">
          {t("warp.delete")}
        </Button>
      </div>
    </div>
  );
}

/** The plain statement under the card: whose account this is, what it is shared with and what Cloudflare may do. */
function Honest({ tosUrl }: { tosUrl: string }) {
  const t = useTx();
  const url = safeHttpUrl(tosUrl);
  return (
    <div className="flex flex-col gap-1.5 border-t border-line pt-3 text-[11px] leading-snug text-muted">
      <p className="text-pretty">{t("warp.honest")}</p>
      {url && (
        <a href={url} target="_blank" rel="noopener noreferrer" className="w-fit font-bold text-accent-text underline decoration-dotted underline-offset-2">
          {t("warp.tosLink")}
        </a>
      )}
    </div>
  );
}
