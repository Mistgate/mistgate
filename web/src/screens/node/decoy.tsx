import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useId, useRef, useState, type ReactNode } from "react";
import { EditionNotice } from "@/components/edition-notice";
import { Button, buttonClass } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import type { StatusKind } from "@/components/ui/status";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { decoySites, nodes as nodesApi } from "@/lib/api";
import { asDecoyMode, decoyModes, isDecoyReason, maxBundleBytes, type DecoyMode, type DecoyReason } from "@/lib/decoy";
import { assignPreviewQuery, builtinId, decoyErrorText, deliveryHint, deliveryOf, nameFromFile, panelDirId, sitesQuery, uploadError } from "@/lib/decoysites";
import { useEdition } from "@/lib/edition";
import { cx } from "@/lib/cx";
import type { Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { LibraryPicker } from "@/screens/decoys/picker";
import { Msg } from "@/screens/decoys/parts";
import { Glyph } from "@/screens/overview-parts";
import { Spin } from "./parts";
import { SvCard, SvHead, SvOpt } from "./server-parts";

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

/** What a scene of /dev-kit puts the card in at the start (the node's own data says the rest). Never set by the product. */
export const cardDemo: { current?: { mode?: DecoyMode; site?: string; problem?: string } } = {};

/**
 * "Decoy site" of a node (the board's section Server -> Decoy site): the three ways to show a site to strangers. For "Own site" the
 * site is chosen in the library (the picker of the Decoy sites page) and goes to the node through AssignDecoySite, after a dry run
 * that shows what it warns about. An archive dropped on the zone is the quick way: it becomes a site of the library and stands on
 * this node at once. The bar under the choice says what the node has really done (the delivery words of the library). Everyone sees
 * it, only the owner changes it (as the API).
 */
export function DecoyCard({ node }: { node: Node }) {
  const demo = cardDemo.current; // the scenes of /dev-kit start the card in a state a click would bring it to
  const t = useT();
  const qc = useQueryClient();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const edition = useEdition();
  const saved = asDecoyMode(node.decoyMode);
  const savedSite = saved === "own" ? node.decoySiteId : "";
  const [mode, setMode] = useState<DecoyMode>(demo?.mode ?? saved);
  const [site, setSite] = useState(demo?.site ?? savedSite);
  const [problem, setProblem] = useState(demo?.problem ?? "");
  const [over, setOver] = useState(false);
  const picker = useRef<HTMLInputElement>(null);

  // the node changed from elsewhere (another admin, the panel): the choice follows what is saved
  const [seenSaved, setSeenSaved] = useState(`${saved}/${savedSite}`);
  if (seenSaved !== `${saved}/${savedSite}`) {
    setSeenSaved(`${saved}/${savedSite}`);
    setMode(saved);
    setSite(savedSite);
  }

  const ownOff = edition.off("decoy_own") || !node.decoyOwnAvailable;
  const needsSite = mode === "own" && !site;
  const dirty = mode !== saved || (mode === "own" && site !== savedSite);
  // what putting the site on this node would warn about, asked before it is done (the panel decides it, the card only shows it)
  const dry = useQuery({ ...assignPreviewQuery(site, [node.id]), enabled: owner && mode === "own" && !!site && dirty && !ownOff });

  const refresh = () => {
    setProblem("");
    for (const k of ["node", "nodes", "decoy-sites", "decoy-site"]) void qc.invalidateQueries({ queryKey: [k] });
  };
  const apply = useMutation({
    mutationFn: async () => (mode === "own" ? decoySites.assignDecoySite({ siteId: site, nodeIds: [node.id], panel: false, dryRun: false }) : nodesApi.setNodeDecoyMode({ nodeId: node.id, mode })),
    onSuccess: refresh,
    onError: (e) => setProblem(decoyErrorText(e, t)),
  });
  // an archive dropped here: a site of the library first, then the site of this node
  const drop = useMutation({
    mutationFn: async (f: File) => {
      const taken = (await qc.fetchQuery({ ...sitesQuery, staleTime: 0 })).sites.map((s) => s.name);
      const made = await decoySites.createDecoySite({ name: nameFromFile(f.name, taken), bundleArchive: new Uint8Array(await f.arrayBuffer()), state: "ready", source: "upload" });
      return decoySites.assignDecoySite({ siteId: made.site!.id, nodeIds: [node.id], panel: false, dryRun: false });
    },
    onSuccess: refresh,
    onError: (e) => {
      const u = uploadError(e, t);
      setProblem(u.text ? `${u.title}. ${u.text}` : u.title);
    },
  });
  const busy = apply.isPending || drop.isPending;

  function take(f: File | undefined) {
    if (!f) return;
    if (f.size > maxBundleBytes) return setProblem(t("decoy.own.tooBig", { name: f.name, size: Math.ceil((f.size / 1048576) * 10) / 10 }));
    setProblem("");
    drop.mutate(f);
  }

  const live = node.decoyAgentMode && node.decoyReasonCode === "" ? asDecoyMode(node.decoyAgentMode) : undefined;
  const off = (m: DecoyMode) => !owner || busy || (m === "panel" && !node.decoyPanelAvailable && saved !== "panel") || (m === "own" && ownOff);
  const d = deliveryOf({ reasonCode: node.decoyReasonCode, supported: node.decoySupported });
  const settled = !busy && !needsSite && !dirty;
  const bar = busy ? t("np.sv.site.sending") : needsSite ? t("decoy.lib.pick") : dirty ? t("np.sv.site.pending") : d.kind === "ok" ? t("decoy.state.ok") : deliveryHint(t, d) || t(d.word);
  const barKind = settled && d.kind === "ok" ? "ok" : settled && decoyReasonKind(node.decoyReasonCode) === "off" ? "off" : "";
  // the site this choice shows, to be opened on the Decoy sites page
  const open = mode === "builtin" ? builtinId : mode === "panel" ? panelDirId : site;
  const warnings = dry.data?.warnings ?? [];
  const shared = warnings.find((w) => w.code === "shared_domains");

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
        <LibraryPicker value={site} onChange={(id) => (setSite(id), setProblem(""))} disabled={!owner || ownOff || busy}>
          <div
            role="button"
            tabIndex={0}
            aria-label={t("decoy.own.label")}
            aria-busy={drop.isPending}
            className={cx("sv-drop", over && "over", (!owner || ownOff || busy) && "pointer-events-none opacity-60")}
            onClick={() => picker.current?.click()}
            onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && (e.preventDefault(), picker.current?.click())}
            onDragOver={(e) => (e.preventDefault(), setOver(true))}
            onDragLeave={() => setOver(false)}
            onDrop={(e) => {
              e.preventDefault();
              setOver(false);
              if (owner && !ownOff && !busy) take(e.dataTransfer.files[0]);
            }}
          >
            <input
              ref={picker}
              type="file"
              hidden
              aria-label={t("decoy.own.label")}
              accept=".zip,.tar,.tgz,.gz,application/zip,application/x-tar,application/gzip"
              onChange={(e) => {
                take(e.target.files?.[0]);
                e.target.value = ""; // the same file can be picked again after a refusal
              }}
            />
            <Glyph d="M12 16V5M7 9l5-5 5 5M5 19h14" size={24} stroke={1.5} />
            <b>{t("decoy.lib.drop")}</b>
            <span>{t("decoy.lib.dropText")}</span>
          </div>
        </LibraryPicker>
      )}
      <EditionNotice feature="decoy_own" />
      {(problem || warnings.length > 0) && (
        <div className="sv-warns">
          {problem && <Notice tone="danger">{problem}</Notice>}
          {shared && (
            <Msg kind="part" title={t("dz.as.shared")}>
              <span>{t("dz.as.sharedText")}</span>
              {shared.paths.length > 0 && (
                <span className="dz-m" dir="ltr">
                  {shared.paths.join(", ")}
                </span>
              )}
            </Msg>
          )}
          {warnings
            .filter((w) => w !== shared)
            .map((w) => (
              <Msg key={w.code} kind="part" title={t(`dz.ck.${w.code}` as MessageKey)}>
                <span>{t("dz.as.warnText")}</span>
              </Msg>
            ))}
        </div>
      )}
      <div className={cx("sv-save on", barKind)}>
        <span>{bar}</span>
        {open && !edition.off("decoy_own") && (
          <Link to="/decoys/$id" params={{ id: open }} className={cx(buttonClass("secondary", "md"), "dz-open")}>
            {t("dz.pk.open")}
          </Link>
        )}
        {owner ? (
          <Button variant="primary" size="md" disabled={!dirty || needsSite || busy || dry.isFetching || (mode === "own" && ownOff)} onClick={() => apply.mutate()}>
            {busy && <Spin />}
            {t("decoy.apply")}
          </Button>
        ) : (
          <span className="!ms-0 !text-muted">{t("decoy.ownerOnly")}</span>
        )}
      </div>
    </SvCard>
  );
}
