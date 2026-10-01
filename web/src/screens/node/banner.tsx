import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useAddNode } from "@/components/add-node";
import { CopyButton } from "@/components/copy-button";
import { Button } from "@/components/ui/button";
import { Banner } from "@/components/ui/notice";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind } from "@/gen/mistgate/admin/v1/health_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import type { Plain } from "@/lib/plain";
import { useFmt } from "@/lib/format";
import { alertWhy } from "@/lib/health";
import { inboundErrorText } from "@/lib/inbound-error";
import { nodeKind, useNodeStatus } from "@/lib/node-status";
import { useNow } from "@/lib/time";
import { useRunChecks } from "@/screens/health/history";
import { nodeAlertsQuery } from "./overview-parts";
import { usePortHolders } from "./profiles";

/** What an admin runs over SSH when the agent does not answer: restart it and show why it stopped. */
export const agentRestartCommand = "systemctl restart mistgate-node && journalctl -u mistgate-node -n 50 --no-pager";

/**
 * What is going on with the node, in a sentence, with what to do about it: the cause and an action that works from here
 * (restart the profiles while the agent is on the line, copy the command when it is not).
 */
export function StateBanner({ data, tab, onTab, onRestart }: { data: Plain<GetNodeResponse>; tab: string; onTab: (tab: string) => void; onRestart: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const st = useNodeStatus();
  const addNode = useAddNode();
  const run = useRunChecks(data.node?.id ?? "");
  const [hidden, setHidden] = useState<string | null>(null);
  const now = useNow();
  const node = data.node!;
  const alerts = useQuery({ ...nodeAlertsQuery(node.id), enabled: node.status === NodeStatus.NO_TRAFFIC });
  const holders = usePortHolders(node.id, node.reason?.code === "inbound_failed");
  const reason = node.reason;
  const params = reason?.params ?? {};
  const key = `${node.status}/${reason?.code ?? ""}`;
  if (hidden === key) return null;

  const doctor = (
    <Button variant="secondary" size="md" onClick={() => onTab("doctor")}>
      {t("node.doctor")}
    </Button>
  );

  switch (node.status) {
    case NodeStatus.DOWN: {
      const silent = node.lastSeenUnix > 0 ? Math.floor(now / 1000) - node.lastSeenUnix : Number(params.minutes ?? 0) * 60;
      const last = node.lastSeenUnix > 0 ? new Date(node.lastSeenUnix * 1000) : null;
      const today = last && last.toDateString() === new Date(now).toDateString();
      const time = !last ? "—" : today ? t("node.banner.today", { time: fmt.clock(node.lastSeenUnix) }) : fmt.dateTime(node.lastSeenUnix);
      // the hoster first, then the agent: the command restarts it and shows its last lines
      return (
        <section className="tone-bad tint screen-enter flex flex-col gap-2.5 rounded-card px-4 py-3.5">
          <h2 className="text-[15px] font-extrabold tracking-[-0.02em]">{t("node.banner.down", { duration: fmt.duration(Math.max(60, silent)) })}</h2>
          <p className="text-[13px] leading-snug text-pretty text-muted">{t("node.banner.downText", { time })}</p>
          <div className="flex flex-wrap items-center gap-2">
            <code className="min-w-0 flex-[1_1_320px] rounded-field border border-line bg-surface px-3 py-2 font-mono text-xs leading-snug break-all">{agentRestartCommand}</code>
            <CopyButton value={agentRestartCommand} label={t("node.banner.copyCommand")} size="md" variant="primary" />
          </div>
        </section>
      );
    }
    case NodeStatus.NO_TRAFFIC: {
      // the same diagnosis as the Health page gives, when its alert is open
      const alert = alerts.data?.active.find((a) => a.kind === AlertKind.NO_TRAFFIC);
      const why = alert ? alertWhy(t, fmt, alert) : "";
      return (
        <Banner
          kind="bad"
          title={t("node.banner.noTraffic")}
          actions={
            <>
              <Button variant="primary" size="md" onClick={onRestart} disabled={data.inbounds.length === 0}>
                {t("node.restart")}
              </Button>
              {run.can && (
                <Button variant="secondary" size="md" onClick={run.run} disabled={run.running}>
                  {t("node.banner.checkNow")}
                </Button>
              )}
            </>
          }
        >
          {why || t("node.banner.noTrafficText")}
        </Banner>
      );
    }
    case NodeStatus.BLIP:
      return (
        <Banner
          kind="blip"
          title={t("node.banner.blip")}
          actions={
            <Button variant="secondary" size="md" onClick={() => setHidden(key)}>
              {t("node.banner.gotIt")}
            </Button>
          }
        >
          {t("node.banner.blipText", { minutes: params.minutes ?? "?" })}
        </Banner>
      );
    case NodeStatus.UPDATING:
      return (
        <Banner kind="busy" title={t("node.banner.updating")}>
          {t("node.banner.updatingText")}
        </Banner>
      );
    case NodeStatus.PENDING: {
      const expired = data.enrollmentExpiresUnix > 0 && data.enrollmentExpiresUnix * 1000 < now;
      return (
        <Banner
          kind="off"
          title={t("node.banner.pending")}
          actions={
            <Button variant="primary" size="md" onClick={() => addNode({ id: node.id, name: node.name })}>
              {t("node.banner.newCommand")}
            </Button>
          }
        >
          {expired ? t("node.banner.pendingExpired") : t("node.banner.pendingText", { time: fmt.clock(data.enrollmentExpiresUnix) })}
        </Banner>
      );
    }
    case NodeStatus.RETIRED:
      return (
        <Banner kind="off" title={t("node.banner.retired")}>
          {t("node.banner.retiredText")}
        </Banner>
      );
    case NodeStatus.ONLINE: {
      // no profile that runs: users get nothing from this node (the panel says no_profiles; an older one says nothing). The
      // Profiles tab says it itself, with the same button.
      const enabled = data.inbounds.filter((i) => i.state !== InboundState.DISABLED).length;
      if ((!reason && enabled === 0) || reason?.code === "no_profiles")
        return tab === "profiles" ? null : (
          <Banner
            kind="warn"
            title={t("node.banner.noProfiles", { name: node.name })}
            actions={
              <Button variant="primary" size="md" onClick={() => onTab("profiles")}>
                {t("node.profiles.add")}
              </Button>
            }
          >
            {t("node.banner.noProfilesText")}
          </Banner>
        );
      if (!reason) return null;
      if (reason.code === "inbound_failed") {
        const failed = data.inbounds.filter((i) => i.state === InboundState.FAILED);
        const first = data.inbounds.find((i) => i.id === params.inbound) ?? failed[0];
        const profile = first?.profileName ?? params.profile ?? "";
        const error = params.error || first?.lastError || "";
        // "of how many" counts the enabled profiles, as the server's reason does: one switched off is not missed
        return (
          <Banner
            kind="bad"
            title={failed.length > 1 ? t.n("node.banner.failedMany", failed.length, { total: enabled }) : profile ? t("node.banner.failedOne", { profile }) : st.word(node)}
            actions={
              <Button variant="primary" size="md" onClick={() => onTab("profiles")}>
                {t("node.banner.openProfiles")}
              </Button>
            }
          >
            {inboundErrorText(t, error, { port: first?.port, process: first ? holders[first.id] : undefined })}
          </Banner>
        );
      }
      return (
        <Banner kind={nodeKind(node)} title={st.word(node)} actions={reason.code === "doctor_fail" ? doctor : undefined}>
          {st.reason(reason)}
        </Banner>
      );
    }
    default:
      return null;
  }
}
