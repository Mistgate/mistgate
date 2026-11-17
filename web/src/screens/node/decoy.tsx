import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useRef, useState, type ReactNode } from "react";
import { EditionNotice } from "@/components/edition-notice";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import type { StatusKind } from "@/components/ui/status";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { nodes as nodesApi } from "@/lib/api";
import { asDecoyMode, decoyModes, isDecoyReason, maxBundleBytes, shortHash, type DecoyMode, type DecoyReason } from "@/lib/decoy";
import { useEdition } from "@/lib/edition";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { cx } from "@/lib/cx";
import type { Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { Glyph } from "@/screens/overview-parts";
import { Facts, SvCard, SvHead, SvOpt } from "./server-parts";

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

/** The tone of a reason code (the Nodes page's bulk rows read it too); one the page does not know is amber. */
export const decoyReasonKind = (code: string): StatusKind => (isDecoyReason(code) ? reasonKind[code] : "warn");

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

  const [over, setOver] = useState(false);
  const live = node.decoyAgentMode && node.decoyReasonCode === "" ? asDecoyMode(node.decoyAgentMode) : undefined;
  const off = (m: DecoyMode) => !owner || apply.isPending || (m === "panel" && !node.decoyPanelAvailable && saved !== "panel") || (m === "own" && ownOff);
  const bar = apply.isPending ? t("np.sv.site.sending") : needsFile ? t("decoy.own.needFile") : dirty ? t("np.sv.site.pending") : node.decoyReasonCode === "" ? t("decoy.state.ok") : reasonText(t, node.decoyReasonCode);
  const barKind = !apply.isPending && !needsFile && !dirty && node.decoyReasonCode === "" ? "ok" : !apply.isPending && !needsFile && !dirty && decoyReasonKind(node.decoyReasonCode) === "off" ? "off" : "";

  return (
    <SvCard aria-label={t("decoy.title")}>
      <SvHead title={t("decoy.title")} hint={t("decoy.intro")} />
      <div role="radiogroup" aria-label={t("decoy.title")} className="sv-opts">
        {decoyModes.map((m) => (
          <SvOpt key={m} on={mode === m} disabled={off(m)} title={t(`decoy.mode.${m}`)} mark={live === m ? t("np.sv.site.live") : undefined} onPick={() => (setMode(m), setProblem(""))}>
            <span>{t(`decoy.mode.${m}Hint`)}</span>
            {m === "panel" && !node.decoyPanelAvailable && <span className="w">{t("decoy.panelNone")}</span>}
          </SvOpt>
        ))}
      </div>
      {mode === "own" && (
        <div
          role="button"
          tabIndex={0}
          aria-label={t("decoy.own.label")}
          className={cx("sv-drop", over && "over", (!owner || ownOff) && "pointer-events-none opacity-60")}
          onClick={() => picker.current?.click()}
          onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && (e.preventDefault(), picker.current?.click())}
          onDragOver={(e) => (e.preventDefault(), setOver(true))}
          onDragLeave={() => setOver(false)}
          onDrop={(e) => {
            e.preventDefault();
            setOver(false);
            if (owner && !ownOff) pick(e.dataTransfer.files[0]);
          }}
        >
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
          <Glyph d="M12 16V5M7 9l5-5 5 5M5 19h14" size={24} stroke={1.5} />
          <b>{file || hasBundle ? t("decoy.own.change") : t("decoy.own.choose")}</b>
          <span className={file ? "file" : undefined}>{file ? `${file.name} · ${fmt.bytes(file.size, 1024)}` : t("decoy.own.none")}</span>
          <span>{t("decoy.own.hint")}</span>
          <span>{hasBundle ? t("decoy.own.uploaded", { hash: shortHash(node.decoyBundleSha256) }) : t("decoy.own.notUploaded")}</span>
        </div>
      )}
      <EditionNotice feature="decoy_own" />
      {problem && (
        <div className="px-6 pb-4 max-[720px]:px-4">
          <Notice tone="danger">{problem}</Notice>
        </div>
      )}
      <div className={cx("sv-save on", barKind)}>
        <span>{bar}</span>
        {owner ? (
          <Button variant="primary" size="md" disabled={!dirty || needsFile || apply.isPending || (mode === "own" && ownOff)} onClick={() => apply.mutate()}>
            {mode === "own" && file ? t("decoy.upload") : t("decoy.apply")}
          </Button>
        ) : (
          <span className="!ms-0 !text-muted">{t("decoy.ownerOnly")}</span>
        )}
      </div>
      <AppliedState node={node} />
    </SvCard>
  );
}


/** What is configured against what the node reported (mode and the first digits of the hash), and why they differ. */
function AppliedState({ node }: { node: Node }) {
  const t = useT();
  const mode = asDecoyMode(node.decoyMode);
  const agent = node.decoyAgentMode ? t(`decoy.mode.${asDecoyMode(node.decoyAgentMode)}`) : t("decoy.state.noReport");
  const hash = (h: string) => (
    <span title={h} className="block font-mono text-[11px] font-normal break-all text-muted">
      {h ? shortHash(h) : "—"}
    </span>
  );
  return (
    <>
      <Facts
        className="sv-facts"
        items={[
          { l: t("decoy.state.configured"), v: <>{t(`decoy.mode.${mode}`)}{hash(mode === "own" ? node.decoyBundleSha256 : "")}</> },
          { l: t("decoy.state.reported"), v: <>{agent}{hash(node.decoyAgentSha256)}</> },
        ]}
      />
      {!node.decoySupported && node.decoyReasonCode !== "agent_too_old" && <p className="sv-note !pt-3.5">{t("decoy.unsupported")}</p>}
    </>
  );
}
