import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { CopyButton } from "@/components/copy-button";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Segmented } from "@/components/ui/segmented";
import { useToast } from "@/components/ui/toast";
import type { Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import { AwgPrepareState, PrepareAwgOutcome, type GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { nodes as nodesApi } from "@/lib/api";
import { codedError } from "@/lib/coded-error";
import { doctorQuery } from "@/lib/health";
import type { Plain } from "@/lib/plain";
import { useTx } from "@/screens/users/t";
import { Panel } from "@/screens/users/ui";
import { kernelReadiness, prepDetailCodes, prepMinutes, prepReasonText } from "./awg-kernel";

const modes = ["auto", "kernel", "userspace"] as const;
export type AwgMode = (typeof modes)[number];
export const asMode = (v: string): AwgMode => (v === "kernel" || v === "userspace" ? v : "auto");

/**
 * The command that builds and loads the AmneziaWG kernel module on the node: what an agent that cannot do it by itself needs,
 * and the fallback for the rest. Without --yes it would only print the plan (cmd/mistgate-node/awgprep_linux.go).
 */
export const prepareKernelCommand = "mistgate-node awg prepare-kernel --yes";

/** What the node last reported as running AmneziaWG: the first AmneziaWG inbound that said anything. */
export function awgRunning(inbounds: readonly Plain<Inbound>[]) {
  const awg = inbounds.filter((i) => i.protocol === "awg");
  const a = awg.map((i) => i.awg).find((s) => s && s.backend);
  return { count: awg.length, backend: a?.backend ?? "", version: a?.backendVersion ?? "" };
}

/** Can this node's agent build the kernel module by itself? (An older one cannot: the UI shows the manual command.) */
export const canPrepare = (node: Plain<GetNodeResponse>["node"]) => !!node?.awgPrepare?.supported;

/** Saves the node's AmneziaWG backend (the same call the settings card and the add-profile dialog make) and refreshes the node. */
export function useSaveAwgBackend(nodeId: string) {
  const qc = useQueryClient();
  return async (mode: AwgMode) => {
    await nodesApi.updateNode({ nodeId, awgBackend: mode });
    await Promise.all([qc.invalidateQueries({ queryKey: ["node", nodeId] }), qc.invalidateQueries({ queryKey: ["nodes"] })]);
  };
}

/** The command, with a way to copy it. */
function Command() {
  return (
    <span className="flex flex-wrap items-center gap-2">
      <code className="rounded-lg bg-surface-2 px-2 py-1 font-mono text-[11px]">{prepareKernelCommand}</code>
      <CopyButton value={prepareKernelCommand} />
    </span>
  );
}

/** «Сделать вручную»: the fallback for a node whose agent builds the module by itself. */
function ManualDisclosure({ open = false }: { open?: boolean }) {
  const t = useTx();
  return (
    <details open={open} className="text-xs text-muted">
      <summary className="w-fit cursor-pointer select-none">{t("awg.prep.manual")}</summary>
      <div className="mt-2 flex flex-col gap-2">
        <span className="leading-normal text-pretty">{t("awg.prep.manualBody")}</span>
        <Command />
      </div>
    </details>
  );
}

/**
 * The three-way choice, what each one means, and what picking the kernel module involves: for an agent that builds it by
 * itself a line about that (and the manual way tucked away), for an older agent the command to run by hand.
 */
export function AwgBackendFields({ mode, onChange, label, auto = false }: { mode: AwgMode; onChange: (m: AwgMode) => void; label: string; auto?: boolean }) {
  const t = useTx();
  return (
    <>
      <Segmented
        aria-label={label}
        value={mode}
        onValueChange={(v) => onChange(asMode(v))}
        options={modes.map((m) => ({ value: m, label: t(`awg.backend.${m}`) }))}
        className="flex-wrap"
      />
      <p className="text-xs leading-normal text-pretty text-muted">{t(`awg.backend.${mode}Hint`)}</p>
      {mode === "kernel" &&
        (auto ? (
          <>
            <p className="text-xs leading-normal text-pretty text-muted">{t("awg.backend.kernelAuto")}</p>
            <ManualDisclosure />
          </>
        ) : (
          <Notice>
            <span className="flex flex-col gap-2">
              <span>{t("awg.backend.kernelPrepare")}</span>
              <Command />
            </span>
          </Notice>
        ))}
    </>
  );
}

/** A clock that ticks while `active`, for "running N min". */
function useNow(active: boolean, everyMs = 15_000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const id = setInterval(() => setNow(Date.now()), everyMs);
    return () => clearInterval(id);
  }, [active, everyMs]);
  return now;
}

/**
 * "AmneziaWG backend" of a node: userspace (amneziawg-go, works anywhere) or the kernel module (faster), or auto. Says what
 * runs now, as the node reported it. Picking the module on a node whose agent can build it asks the node first: a module
 * that is already loaded is just switched to; otherwise the admin confirms, the node builds it in the background while
 * AmneziaWG keeps running as it was, and the panel switches the node when the module is built and loaded (the setting
 * is never "kernel" before that). The card shows how it goes, and why it did not work.
 */
export function AwgBackendCard({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const node = data.node!;
  const save = useSaveAwgBackend(node.id);
  const saved = asMode(node.awgBackend);
  const prep = node.awgPrepare;
  const auto = canPrepare(node);
  const building = auto && prep?.state === AwgPrepareState.RUNNING;
  const [mode, setMode] = useState<AwgMode>(saved);
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [noAuto, setNoAuto] = useState<{ code: string; reason: string } | null>(null);
  const running = awgRunning(data.inbounds);
  const dirty = mode !== saved;
  const now = useNow(building);

  // the panel switched the node by itself (the module got ready): the choice follows what is saved
  const [seenSaved, setSeenSaved] = useState(saved);
  if (seenSaved !== saved) {
    setSeenSaved(saved);
    setMode(saved);
  }

  // While it builds, look at the node every few seconds; when it ends, the doctor and the events have news too.
  useEffect(() => {
    if (!building) return;
    const id = setInterval(() => void qc.invalidateQueries({ queryKey: ["node", node.id] }), 5000);
    return () => clearInterval(id);
  }, [building, node.id, qc]);
  const endState = prep?.state === AwgPrepareState.DONE || prep?.state === AwgPrepareState.FAILED ? prep.state : 0;
  useEffect(() => {
    if (!endState) return;
    void qc.invalidateQueries({ queryKey: ["health", "doctor"] });
    void qc.invalidateQueries({ queryKey: ["node-events", node.id] });
  }, [endState, node.id, qc]);

  const refresh = () => Promise.all([qc.invalidateQueries({ queryKey: ["node", node.id] }), qc.invalidateQueries({ queryKey: ["nodes"] })]);

  /** Asks the node (confirmed: starts the build) and acts on the answer. */
  async function askNode(confirmed: boolean) {
    const r = await nodesApi.prepareAwgKernel({ nodeId: node.id, confirm: confirmed });
    switch (r.outcome) {
      case PrepareAwgOutcome.READY: // the module is loaded: switching is just a setting
        await save("kernel");
        toast(t("awg.backend.saved"));
        break;
      case PrepareAwgOutcome.NEEDS_PREPARE:
        setConfirm(true);
        break;
      case PrepareAwgOutcome.STARTED:
        toast(t("awg.prep.started"));
        await refresh();
        break;
      case PrepareAwgOutcome.RUNNING:
        toast(t("awg.prep.alreadyRunning"));
        await refresh();
        break;
      case PrepareAwgOutcome.UNSUPPORTED:
        setNoAuto({ code: r.reasonCode, reason: r.reason });
        break;
    }
  }

  async function run(job: () => Promise<void>) {
    setBusy(true);
    setNoAuto(null);
    try {
      await job();
    } catch (e) {
      toast(codedError(e, t, "awg.err"));
    } finally {
      setBusy(false);
    }
  }

  function submit() {
    if (mode === "kernel" && auto) return run(() => askNode(false));
    return run(async () => {
      await save(mode);
      toast(t("awg.backend.saved"));
    });
  }

  function retry() {
    setMode("kernel");
    return run(() => askNode(false));
  }

  function startBuild() {
    setConfirm(false);
    return run(() => askNode(true));
  }

  return (
    <Panel title={t("awg.backend.title")} icon="mask" tone="sage">
      <p className="-mt-1 text-xs leading-normal text-pretty text-muted">{t("awg.backend.body")}</p>
      <AwgBackendFields mode={mode} onChange={setMode} label={t("awg.backend.title")} auto={auto} />
      {noAuto && (
        <Notice>
          <span className="flex flex-col gap-2">
            <span>{t("awg.prep.unsupported", { reason: prepReasonText(t, noAuto.code, noAuto.reason) })}</span>
            <Command />
          </span>
        </Notice>
      )}
      {auto && prep && <PrepStatus prep={prep} now={now} usingKernel={saved === "kernel"} busy={busy} onRetry={() => void retry()} />}
      <div className="flex flex-wrap items-center gap-3 border-t border-line pt-3">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="text-[11px] text-muted">{t("awg.backend.running")}</span>
          <span className="font-mono text-[13px] break-words">
            {running.backend ? `${running.backend} · ${running.version}` : running.count === 0 ? t("awg.backend.noInbounds") : t("awg.backend.notReported")}
          </span>
        </div>
        <Button variant="primary" size="md" disabled={!dirty || busy || (building && mode === "kernel")} onClick={() => void submit()}>
          {t("common.save")}
        </Button>
      </div>
      <Modal
        open={confirm}
        onOpenChange={setConfirm}
        title={t("awg.prep.confirmTitle", { name: node.name })}
        description={t("awg.prep.confirmBody")}
        footer={
          <>
            <Button variant="ghost" size="md" onClick={() => setConfirm(false)}>
              {t("common.cancel")}
            </Button>
            <Button variant="primary" size="md" disabled={busy} onClick={() => void startBuild()}>
              {t("awg.prep.confirmDo")}
            </Button>
          </>
        }
      />
    </Panel>
  );
}

/** «Готовлю модуль… (идёт N мин)» / «Модуль готов» / «Не удалось: …» with «Повторить». */
function PrepStatus({
  prep,
  now,
  usingKernel,
  busy,
  onRetry,
}: {
  prep: NonNullable<NonNullable<Plain<GetNodeResponse>["node"]>["awgPrepare"]>;
  now: number;
  usingKernel: boolean;
  busy: boolean;
  onRetry: () => void;
}) {
  const t = useTx();
  switch (prep.state) {
    case AwgPrepareState.RUNNING:
      return (
        <div role="status" className="flex flex-col gap-1 rounded-field border border-line bg-surface-2 px-3 py-2.5">
          <span className="flex items-center gap-2 text-[13px] font-bold">
            <span aria-hidden className="size-2 flex-none animate-[mg-ui-blink_1s_infinite] rounded-full bg-sage" />
            {t("awg.prep.running", { minutes: prepMinutes(prep.sinceUnix, now) })}
          </span>
          <span className="text-xs leading-normal text-pretty text-muted">{t("awg.prep.runningHint")}</span>
        </div>
      );
    case AwgPrepareState.DONE:
      return (
        <div role="status" className="flex flex-col gap-1 rounded-field border border-line bg-surface-2 px-3 py-2.5">
          <span className="text-[13px] font-bold">{t("awg.prep.done")}</span>
          <span className="text-xs leading-normal text-pretty text-muted">{t(usingKernel ? "awg.prep.doneUsed" : "awg.prep.doneIdle")}</span>
        </div>
      );
    case AwgPrepareState.FAILED:
      return (
        <Notice tone="danger">
          <span className="flex flex-col gap-1.5">
            <b>{t("awg.prep.failed", { reason: prepReasonText(t, prep.reasonCode, prep.reason) })}</b>
            {prep.reason && prepDetailCodes.has(prep.reasonCode) && <code className="font-mono text-[11px] break-words text-muted">{prep.reason}</code>}
            <span className="text-muted">{t("awg.prep.failedHint")}</span>
            <span>
              <Button variant="secondary" size="md" disabled={busy} onClick={onRetry}>
                {t("awg.prep.retry")}
              </Button>
            </span>
          </span>
        </Notice>
      );
  }
  return null;
}

/**
 * The backend question of the add-profile dialog, asked when the chosen profile is AmneziaWG: the node has ONE backend
 * for all its AmneziaWG profiles, so the first one decides. Same choice, same API as the settings card; the dialog saves
 * it together with the profile. Shows what runs now and whether the kernel module can be built on this node.
 */
export function AwgBackendChoice({ data, mode, onChange }: { data: Plain<GetNodeResponse>; mode: AwgMode; onChange: (m: AwgMode) => void }) {
  const t = useTx();
  const node = data.node!;
  const auto = canPrepare(node);
  const running = awgRunning(data.inbounds);
  const doctor = useQuery(doctorQuery(node.id));
  const item = doctor.data?.nodes.find((n) => n.nodeId === node.id)?.items.find((i) => i.id === "kernel_headers");
  const ready = kernelReadiness(item, data.facts?.virt ?? "");
  return (
    <div className="flex flex-col gap-2.5 rounded-field border border-line bg-surface-2 p-3.5">
      <div className="flex flex-col gap-0.5">
        <span className="text-[13px] font-bold">{t("node.awg.add.title")}</span>
        <span className="text-xs leading-normal text-pretty text-muted">{running.count === 0 ? t("node.awg.add.first") : t("node.awg.add.shared")}</span>
      </div>
      <AwgBackendFields mode={mode} onChange={onChange} label={t("awg.backend.title")} auto={auto} />
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted">{t("awg.backend.running")}</dt>
        <dd className="font-mono break-words">
          {running.backend ? `${running.backend} · ${running.version}` : running.count === 0 ? t("node.awg.add.nothing") : t("awg.backend.notReported")}
        </dd>
        <dt className="text-muted">{t("awg.backend.kernel")}</dt>
        <dd className={ready.state === "ready" ? "" : ready.state === "unchecked" ? "text-muted" : "text-warn-text"}>
          {ready.state === "missing"
            ? t(auto ? "node.awg.kernel.missingAuto" : "node.awg.kernel.missing", { missing: ready.missing || "?" })
            : ready.state === "container"
              ? t("node.awg.kernel.container", { virt: ready.virt })
              : t(`node.awg.kernel.${ready.state}`)}
        </dd>
      </dl>
    </div>
  );
}
