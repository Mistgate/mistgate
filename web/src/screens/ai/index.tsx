import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { EditionNotice } from "@/components/edition-notice";
import { RunStatus } from "@/gen/mistgate/tasks/v1/tasks_pb";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import { settingsQuery } from "@/lib/assistant";
import { useEdition } from "@/lib/edition";
import { approvalsQuery } from "@/lib/integrations";
import { meQuery } from "@/lib/session";
import { tasksQuery } from "@/lib/tasks";
import { useIsOwner } from "@/lib/updates";
import { ApprovalsTab } from "./approvals";
import { ErrCard, ICONS, OwnerOnly, Skeleton } from "./bits";
import { MemoryTab } from "./memory";
import { AssistantTab } from "./settings";
import { TasksTab } from "./tasks";
import { TokensTab } from "./tokens";
import type { AiTab } from "./tabs";
import "./ai.css";

/** What a scene of the dev kit puts in place of what the page would find out by itself (the open tab). */
export type AiScene = { tab?: AiTab };

/**
 * /ai (ADR 0014), as the board draws it (PnAI): the assistant (its providers and settings, the owner's), what it remembers and the
 * conversations (each admin's own), the scheduled tasks (ADR 0017), MCP with the API tokens for outside agents, and the changes waiting
 * for the owner's decision. A number on a tab says how much waits there.
 */
export function AiScreen() {
  return <AiView />;
}

/** The tabs under the title: a number on a tab says how much waits there. */
function TabStrip({ tabs, value, label, onPick }: { tabs: { value: AiTab; label: string; n: number }[]; value: AiTab; label: string; onPick: (v: AiTab) => void }) {
  const pick = (i: number) => {
    const next = tabs[(i + tabs.length) % tabs.length];
    if (!next) return;
    onPick(next.value);
    document.getElementById(`aip-tab-${next.value}`)?.focus();
  };
  return (
    <div role="tablist" aria-label={label} className="sb-tabs">
      {tabs.map((x, i) => (
        <button
          key={x.value}
          id={`aip-tab-${x.value}`}
          type="button"
          role="tab"
          aria-selected={x.value === value}
          aria-controls="aip-tabpanel"
          tabIndex={x.value === value ? 0 : -1}
          onClick={() => onPick(x.value)}
          onKeyDown={(e) => {
            const step = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
            if (step === 0) return;
            e.preventDefault();
            pick(i + step);
          }}
        >
          {x.label}
          {x.n > 0 && <span className="bdg">{x.n}</span>}
        </button>
      ))}
    </div>
  );
}

export function AiView({ scene }: { scene?: AiScene }) {
  const t = useT();
  const owner = useIsOwner();
  const edition = useEdition();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { tab?: AiTab; plan?: string; task?: string };
  const role = useQuery(meQuery).data?.admin?.role;
  const off = edition.off("assistant");
  const [sceneTab, setSceneTab] = useState<AiTab | undefined>(scene?.tab);
  const tab = sceneTab ?? search.tab ?? (owner ? "assistant" : "memory");
  const approvals = useQuery({ ...approvalsQuery, enabled: owner });
  const settings = useQuery({ ...settingsQuery, enabled: owner && !off });
  // the tasks are read by the owner and the helpers; the tab's number counts the ones whose last run found something or failed
  const tasks = useQuery({ ...tasksQuery, enabled: (role === Role.OWNER || role === Role.HELPER) && !off });
  const waiting = approvals.data?.awaiting ?? 0;
  const bad = (tasks.data?.tasks ?? []).filter((x) => x.lastRun && (x.lastRun.status === RunStatus.PROBLEM || x.lastRun.status === RunStatus.FAILED)).length;
  const go = (next: AiTab) => (scene ? setSceneTab(next) : void navigate({ to: "/ai", search: next === "assistant" ? {} : { tab: next }, replace: true } as never));

  const sum = waiting > 0 ? t.n("int.sub.awaiting", waiting) : !owner ? t("ai.sub") : settings.isPending && !off ? t("aip.sum.load") : settings.isError ? t("aip.sum.err") : settings.data?.settings?.enabled ? t("aip.sum.on") : t("aip.sum.off");

  const ownerOnly = <OwnerOnly title={t("ai.ownerOnly")} icon={ICONS.spark} />;
  // the Cloudflare edition has no assistant yet (it comes with MCP, in a Worker of its own): its tabs say so
  const noAssistant = <EditionNotice feature="assistant" />;
  const body =
    tab === "assistant"
      ? off
        ? noAssistant
        : owner
          ? <AssistantTab />
          : ownerOnly
      : tab === "memory"
        ? off
          ? noAssistant
          : <MemoryTab />
        : tab === "tasks"
          ? <TasksTab focus={search.task} />
          : tab === "mcp"
            ? owner
              ? <TokensTab />
              : ownerOnly
            : owner
              ? approvals.isPending
                ? <Skeleton />
                : approvals.isError && !approvals.data
                  ? <ErrCard onRetry={() => void approvals.refetch()} />
                  : approvals.data && <ApprovalsTab data={approvals.data} focus={search.plan} />
              : ownerOnly;

  const tabs: { value: AiTab; label: string; n: number }[] = [
    { value: "assistant", label: t("aip.tab.assistant"), n: 0 },
    { value: "memory", label: t("aip.tab.memory"), n: 0 },
    { value: "tasks", label: t("aip.tab.tasks"), n: bad },
    { value: "mcp", label: t("aip.tab.mcp"), n: 0 },
    { value: "approvals", label: t("aip.tab.approvals"), n: waiting },
  ];

  return (
    <div className="aip">
      <header className="sb-h">
        <h1>{t("aip.title")}</h1>
        <p>{sum}</p>
      </header>
      <TabStrip tabs={tabs} value={tab} label={t("aip.tabs")} onPick={go} />
      <div id="aip-tabpanel" role="tabpanel" aria-labelledby={`aip-tab-${tab}`} key={tab}>
        {body}
      </div>
    </div>
  );
}
