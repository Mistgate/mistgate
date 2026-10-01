import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { StatusDot } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { health } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { quoteNames } from "@/lib/doctor-detail";
import { fixErrorText, fixLabel, lookup, restartConsequence, useCan, type DoctorItem, type FixRequest } from "@/lib/health";
import { plain, type Plain } from "@/lib/plain";
import type { FixPlan } from "@/gen/mistgate/admin/v1/health_pb";

// The two-step fix (health.proto ApplyFix): ask the node what it would do (dry run), show that, and only a click on
// "Apply" performs it with the one-time plan id. While it runs the row says "Fixing…", afterwards "Fixed".

export type FixTarget = FixRequest & {
  /** Identifies the row that shows the progress (node + check + inbound). */
  key: string;
  nodeId: string;
  nodeName: string;
  /** The doctor item this fix belongs to, so a list can go on showing the row as "Fixed" after the item left the report. */
  item?: DoctorItem;
  /** The profile a restart_inbound restarts, by name: the button and the dialog say it. */
  profile?: string;
};

type Phase = { phase: "busy" | "done"; target: FixTarget };
type Dialog =
  | { target: FixTarget; state: "asking" }
  | { target: FixTarget; state: "ready"; plan: Plain<FixPlan>; planId: string }
  | { target: FixTarget; state: "error"; error: string };

const doneFor = 30_000; // "Fixed" stays on the row until the re-check report replaces it

export type FixFlow = {
  start: (target: FixTarget) => void;
  phase: (key: string) => "idle" | "busy" | "done";
  /** Fixes that are running or finished a moment ago. */
  pending: () => FixTarget[];
  modal: ReactNode;
};

export function useFixFlow(): FixFlow {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [dlg, setDlg] = useState<Dialog | null>(null);
  const [phases, setPhases] = useState<Record<string, Phase>>({});
  const seq = useRef(0); // a dry run that comes back after the dialog was closed is ignored
  const timers = useRef<number[]>([]);
  useEffect(() => () => timers.current.forEach(clearTimeout), []);

  const setPhase = useCallback((key: string, p: Phase | null) => {
    setPhases((prev) => {
      const next = { ...prev };
      if (p) next[key] = p;
      else delete next[key];
      return next;
    });
  }, []);

  const refresh = useCallback(() => {
    for (const k of ["health", "overview", "nodes", "node"]) void qc.invalidateQueries({ queryKey: [k] });
  }, [qc]);

  const start = useCallback(
    (target: FixTarget) => {
      const mine = ++seq.current;
      setDlg({ target, state: "asking" });
      health
        .applyFix({ nodeId: target.nodeId, fixId: target.fixId, params: target.params, dryRun: true })
        .then((r) => {
          const res = plain(r);
          if (mine !== seq.current || !res.plan) return;
          setDlg({ target, state: "ready", plan: res.plan, planId: res.planId });
        })
        .catch((e) => {
          if (mine === seq.current) setDlg({ target, state: "error", error: errorText(e, t) });
        });
    },
    [t],
  );

  const close = useCallback(() => {
    seq.current++;
    setDlg(null);
  }, []);

  const confirm = async (d: Extract<Dialog, { state: "ready" }>) => {
    const { target } = d;
    close();
    setPhase(target.key, { phase: "busy", target });
    const label = fixLabel(t, target.fixId, target.profile);
    try {
      const r = plain(await health.applyFix({ nodeId: target.nodeId, fixId: target.fixId, params: target.params, dryRun: false, planId: d.planId }));
      if (!r.applied) {
        setPhase(target.key, null);
        toast.error(t("hl.fix.failed", { error: fixErrorText(t, r.error) }));
        return;
      }
      setPhase(target.key, { phase: "done", target });
      timers.current.push(window.setTimeout(() => setPhase(target.key, null), doneFor));
      toast(r.resultParams.noop === "1" ? t("hl.fix.doneNothing", { node: target.nodeName }) : t("hl.fix.done", { node: target.nodeName, label }));
    } catch (e) {
      setPhase(target.key, null);
      toast.error(errorText(e, t));
    } finally {
      refresh();
    }
  };

  const phase = useCallback((key: string) => phases[key]?.phase ?? "idle", [phases]);
  const pending = () => Object.values(phases).map((p) => p.target);
  return { start, phase, pending, modal: <FixDialog dlg={dlg} onClose={close} onConfirm={confirm} /> };
}

function FixDialog({ dlg, onClose, onConfirm }: { dlg: Dialog | null; onClose: () => void; onConfirm: (d: Extract<Dialog, { state: "ready" }>) => void }) {
  const t = useT();
  // the last dialog stays rendered while the modal fades out
  const [last, setLast] = useState<Dialog | null>(dlg);
  if (dlg && dlg !== last) setLast(dlg);
  const d = dlg ?? last;
  if (!d) return null;
  const label = fixLabel(t, d.target.fixId);
  const plan = d.state === "ready" ? d.plan : null;
  const nothing = plan?.params.noop === "1";
  // a restart is asked as a question with the profiles by name and who drops; the node's own words go under "Details"
  const restart = d.target.fixId === "restart_inbound";
  const names = plan?.params.profiles || d.target.profile || "";
  const many = names.includes(",");
  const title =
    restart && names ? t("hl.restart.title", { profiles: quoteNames(t, names), node: d.target.nodeName }) : t("hl.fix.title", { label, node: d.target.nodeName });
  const params = plan ? { ...plan.params, profiles: quoteNames(t, plan.params.profiles || plan.params.inbounds || "") } : {};
  // a fix the SPA has no words for says what the node says, in the open
  const worded = plan ? lookup(t, plan.titleKey, params) : null;
  const sentence = plan && !restart ? (worded ?? plan.detail) : "";
  const consequence = plan && restart ? (restartConsequence(t, many ? "many" : "one", plan.params.online) ?? t("hl.fix.disruptive")) : "";

  return (
    <Modal
      open={!!dlg}
      onOpenChange={(o) => !o && onClose()}
      title={title}
      footer={
        <>
          <Button variant="ghost" size="md" onClick={onClose}>
            {d.state === "ready" && !nothing ? t("common.cancel") : t("common.close")}
          </Button>
          {d.state === "ready" && !nothing && (
            <Button variant="primary" size="md" onClick={() => onConfirm(d)}>
              {restart ? t("hl.restart.do") : t("hl.fix.apply")}
            </Button>
          )}
        </>
      }
    >
      {d.state === "asking" && <p className="text-[13px] text-muted">{t("hl.fix.asking")}</p>}
      {d.state === "error" && (
        <Notice tone="danger" className="items-start">
          {d.error}
        </Notice>
      )}
      {plan && (
        <div className="flex flex-col gap-3">
          {nothing ? (
            <p className="text-sm leading-normal text-pretty">{t("hl.fix.nothing")}</p>
          ) : (
            <>
              {sentence && <p className="text-sm leading-normal text-pretty">{sentence}</p>}
              {restart ? <Notice>{consequence}</Notice> : plan.disruptive ? <Notice>{t("hl.fix.disruptive")}</Notice> : <p className="text-xs text-muted">{t("hl.fix.safe")}</p>}
              {plan.detail && sentence !== plan.detail && (
                <details className="group text-xs">
                  <summary className="flex w-fit cursor-pointer list-none items-center gap-1 font-bold text-muted select-none hover:text-fg [&::-webkit-details-marker]:hidden">
                    {t("hl.fix.details")}
                    <span aria-hidden className="transition-transform duration-200 group-open:rotate-90">
                      ›
                    </span>
                  </summary>
                  <p className="mt-2 rounded-field bg-surface-2 px-3 py-2 font-mono text-[11px] leading-snug break-words text-muted">{plan.detail}</p>
                </details>
              )}
            </>
          )}
        </div>
      )}
    </Modal>
  );
}

/** The fix button of a row or card, or what replaces it while the fix runs and right after. Nothing for a role that cannot fix. */
export function FixControl({ flow, target, variant = "secondary" }: { flow: FixFlow; target: FixTarget; variant?: "primary" | "secondary" }) {
  const t = useT();
  const can = useCan();
  const phase = flow.phase(target.key);
  if (phase === "busy")
    return (
      <span className="flex items-center gap-2 text-xs font-bold text-muted" role="status">
        <StatusDot kind="busy" />
        {t("hl.fix.fixing")}
      </span>
    );
  if (phase === "done") return <span className="text-xs font-bold text-muted">{t("hl.fix.fixed")}</span>;
  if (!can.fix) return null;
  return (
    <Button variant={variant} size="md" onClick={() => flow.start(target)}>
      {fixLabel(t, target.fixId, target.profile)}
    </Button>
  );
}
