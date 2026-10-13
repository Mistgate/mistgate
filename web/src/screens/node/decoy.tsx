import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useRef, useState, type ReactNode } from "react";
import { EditionNotice } from "@/components/edition-notice";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { kindTextClass, StatusDot, type StatusKind } from "@/components/ui/status";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { nodes as nodesApi } from "@/lib/api";
import { asDecoyMode, decoyModes, isDecoyReason, maxBundleBytes, shortHash, type DecoyMode, type DecoyReason } from "@/lib/decoy";
import { useEdition } from "@/lib/edition";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import type { Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";

type Node = Plain<NodeMsg>;

/** The reason a node has not applied its site, in words; the panel's own refusals read as the refusals do. */
export function reasonText(t: T, code: string): string {
  if (!isDecoyReason(code)) return t("decoy.reason.unknown", { code });
  return code.startsWith("panel_") ? t(`err.${code}` as MessageKey) : t(`decoy.reason.${code}` as MessageKey);
}

/** How alarming a reason is: still on its way (quiet), or something the admin has to do (amber). */
const reasonKind: Record<DecoyReason, StatusKind> = {
  agent_too_old: "warn",
  node_offline: "off",
  pending: "busy",
  apply_failed: "warn",
  own_bundle_missing: "warn",
  panel_unavailable: "warn",
  panel_too_large: "warn",
  panel_no_index: "warn",
  panel_invalid: "warn",
  unavailable_in_edition: "off",
};

/**
 * The three-way choice with one line under each. The caller says which ones are off and why (`panelNote` / `ownNote`) and
 * what goes under "Own site" when it is picked (`ownExtra`: the file picker of a single node).
 */
export function DecoyModeChoice({
  label,
  value,
  onChange,
  disabled,
  panelOff,
  panelNote,
  ownOff,
  ownNote,
  ownExtra,
}: {
  label: string;
  value: DecoyMode;
  onChange: (m: DecoyMode) => void;
  disabled?: boolean;
  panelOff?: boolean;
  panelNote?: ReactNode;
  ownOff?: boolean;
  ownNote?: ReactNode;
  ownExtra?: ReactNode;
}) {
  const t = useT();
  const group = useId();
  return (
    <div role="radiogroup" aria-label={label} className="flex flex-col gap-2">
      {decoyModes.map((m) => {
        const off = disabled || (m === "panel" && panelOff) || (m === "own" && ownOff);
        const note = m === "panel" ? panelNote : m === "own" ? ownNote : null;
        return (
          <div key={m} className="flex flex-col gap-2">
            <label
              className={`flex items-start gap-3 rounded-field border border-line bg-surface px-3.5 py-3 has-checked:border-accent-line has-checked:bg-surface-2 ${off ? "cursor-not-allowed opacity-60" : "cursor-pointer"}`}
            >
              <input type="radio" name={group} value={m} className="mt-0.5 size-4 flex-none accent-accent" checked={value === m} disabled={off} onChange={() => onChange(m)} />
              <span className="flex min-w-0 flex-col gap-1">
                <span className="text-[13px] font-bold">{t(`decoy.mode.${m}`)}</span>
                <span className="text-xs leading-snug text-pretty text-muted">{t(`decoy.mode.${m}Hint`)}</span>
                {note && <span className="text-xs leading-snug text-pretty text-warn-text">{note}</span>}
              </span>
            </label>
            {m === "own" && value === "own" && ownExtra}
          </div>
        );
      })}
    </div>
  );
}

/** "Decoy site" of a node: which site the node shows to strangers, the own archive, and what the node has really applied. Everyone sees it, only the owner changes it (as the API). */
export function DecoyCard({ node }: { node: Node }) {
  const t = useT();
  const fmt = useFmt();
  const qc = useQueryClient();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const edition = useEdition();
  const saved = asDecoyMode(node.decoyMode);
  const [mode, setMode] = useState<DecoyMode>(saved);
  const [file, setFile] = useState<File | null>(null);
  const [problem, setProblem] = useState("");
  const picker = useRef<HTMLInputElement>(null);

  // the node changed from elsewhere (another admin, the panel): the choice follows what is saved
  const [seenSaved, setSeenSaved] = useState(saved);
  if (seenSaved !== saved) {
    setSeenSaved(saved);
    setMode(saved);
  }

  const ownOff = edition.off("decoy_own") || !node.decoyOwnAvailable;
  const hasBundle = node.decoyBundleSha256 !== "";
  const needsFile = mode === "own" && !file && !hasBundle;
  const dirty = mode !== saved || (mode === "own" && file !== null);

  const apply = useMutation({
    mutationFn: async () =>
      mode === "own" && file
        ? nodesApi.uploadNodeDecoy({ nodeId: node.id, bundleArchive: new Uint8Array(await file.arrayBuffer()) })
        : nodesApi.setNodeDecoyMode({ nodeId: node.id, mode }),
    onSuccess: () => {
      setFile(null);
      setProblem("");
      void qc.invalidateQueries({ queryKey: ["node", node.id] });
      void qc.invalidateQueries({ queryKey: ["nodes"] });
    },
    onError: (e) => setProblem(errorText(e, t)),
  });

  function pick(f: File | undefined) {
    if (!f) return;
    if (f.size > maxBundleBytes) {
      // never sent: the panel would refuse it anyway
      setFile(null);
      setProblem(t("decoy.own.tooBig", { name: f.name, size: Math.ceil((f.size / 1048576) * 10) / 10 }));
      return;
    }
    setProblem("");
    setFile(f);
  }

  const ownExtra = (
    <div className="flex flex-col gap-2 rounded-field border border-dashed border-line px-3.5 py-3">
      <SectionLabel as="span">{t("decoy.own.label")}</SectionLabel>
      <div className="flex flex-wrap items-center gap-2">
        <input
          ref={picker}
          type="file"
          hidden
          aria-label={t("decoy.own.label")}
          accept=".zip,.tar,.tgz,.gz,application/zip,application/x-tar,application/gzip"
          onChange={(e) => {
            pick(e.target.files?.[0]);
            e.target.value = ""; // the same file can be picked again after a refusal
          }}
        />
        <Button variant="secondary" size="md" disabled={!owner || ownOff} onClick={() => picker.current?.click()}>
          <span>{file || hasBundle ? t("decoy.own.change") : t("decoy.own.choose")}</span>
        </Button>
        <span className="min-w-0 font-mono text-xs break-all text-muted">{file ? `${file.name} · ${fmt.bytes(file.size, 1024)}` : t("decoy.own.none")}</span>
      </div>
      <span className="text-xs leading-snug text-pretty text-muted">{t("decoy.own.hint")}</span>
      <span className="text-xs leading-snug text-muted">
        {hasBundle ? t("decoy.own.uploaded", { hash: shortHash(node.decoyBundleSha256) }) : t("decoy.own.notUploaded")}
      </span>
    </div>
  );

  return (
    <section className="flex flex-col gap-3 rounded-card-lg border border-line bg-panel p-4">
      <SectionLabel as="h2" icon="mask" tone="lavender">
        {t("decoy.title")}
      </SectionLabel>
      <p className="text-[13px] leading-snug text-pretty text-muted">{t("decoy.intro")}</p>
      <DecoyModeChoice
        label={t("decoy.title")}
        value={mode}
        onChange={(m) => {
          setMode(m);
          setProblem("");
        }}
        disabled={!owner || apply.isPending}
        panelOff={!node.decoyPanelAvailable && saved !== "panel"}
        panelNote={!node.decoyPanelAvailable ? t("decoy.panelNone") : undefined}
        ownOff={ownOff}
        ownExtra={ownExtra}
      />
      <EditionNotice feature="decoy_own" />
      {!owner && <p className="text-xs text-muted">{t("decoy.ownerOnly")}</p>}
      {problem && <Notice tone="danger">{problem}</Notice>}
      {owner && (
        <div className="flex flex-wrap items-center justify-end gap-3">
          {needsFile && <span className="text-xs text-muted">{t("decoy.own.needFile")}</span>}
          <Button variant="primary" size="md" disabled={!dirty || needsFile || apply.isPending || (mode === "own" && ownOff)} onClick={() => apply.mutate()}>
            {mode === "own" && file ? t("decoy.upload") : t("decoy.apply")}
          </Button>
        </div>
      )}
      <AppliedState node={node} />
    </section>
  );
}

/** What is configured against what the node reported (mode and the first digits of the hash), and why they differ. */
function AppliedState({ node }: { node: Node }) {
  const t = useT();
  const mode = asDecoyMode(node.decoyMode);
  const reason = node.decoyReasonCode;
  const kind: StatusKind = reason === "" ? "ok" : isDecoyReason(reason) ? reasonKind[reason] : "warn";
  const agent = node.decoyAgentMode ? t(`decoy.mode.${asDecoyMode(node.decoyAgentMode)}`) : t("decoy.state.noReport");
  const row = (label: string, value: string, hash: string) => (
    <div className="flex min-w-0 flex-col gap-0.5">
      <span className="text-[11px] text-muted">{label}</span>
      <span className="text-[13px] font-bold">{value}</span>
      <span title={hash} className="font-mono text-[11px] break-all text-muted">
        {hash ? shortHash(hash) : "—"}
      </span>
    </div>
  );
  return (
    <div className="flex flex-col gap-2.5 border-t border-line pt-3">
      <SectionLabel as="h3">{t("decoy.state.title")}</SectionLabel>
      <div className="grid grid-cols-2 gap-3">
        {row(t("decoy.state.configured"), t(`decoy.mode.${mode}`), mode === "own" ? node.decoyBundleSha256 : "")}
        {row(t("decoy.state.reported"), agent, node.decoyAgentSha256)}
      </div>
      <p className={`flex items-start gap-2 text-xs leading-snug text-pretty ${kindTextClass[kind]}`}>
        <span className="mt-[3px]">
          <StatusDot kind={kind} />
        </span>
        <span>{reason === "" ? t("decoy.state.ok") : reasonText(t, reason)}</span>
      </p>
      {!node.decoySupported && reason !== "agent_too_old" && <p className="text-xs leading-snug text-pretty text-muted">{t("decoy.unsupported")}</p>}
    </div>
  );
}
