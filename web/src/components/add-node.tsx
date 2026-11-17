import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { createContext, use, useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { CloseX, CodeBox, CountryField, FootNote, noCountry, Panel, PanelFoot, PanelHead, Rail, type NodeDraft, type RailStep } from "@/components/add-node-frame";
import { AddNodeShell } from "@/components/add-node-frame";
import { markFresh } from "@/components/fx/registry";
import { NodeReady, SSHNodeInstall, type InstallSeed } from "@/components/ssh-node-install";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { TextField } from "@/components/ui/text-field";
import { Spinner } from "@/components/windows";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { CreateEnrollmentResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { plain, type Plain } from "@/lib/plain";
import { useT } from "@/i18n";
import { nodes as nodesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { useNodeStatus } from "@/lib/node-status";

// New nodes can be installed automatically over SSH or enrolled with the manual one-time command. The window is the board's
// PnAddNode: one window with the two ways in its rail (ssh-node-install.tsx, and the by-command flow below).

export type AddNodeTarget = { id: string; name: string };
export type AddNodeMode = "ssh" | "cmd";
type Open = (reenroll?: AddNodeTarget, mode?: AddNodeMode) => void;

const AddNodeContext = createContext<Open>(() => {});
/** addNode() opens the window (by SSH; addNode(undefined, "cmd") by a command); addNode({ id, name }) issues a fresh install command for an existing node. */
export const useAddNode = () => use(AddNodeContext);

export function AddNodeProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState({ seq: 0, open: false, reenroll: undefined as AddNodeTarget | undefined, mode: "ssh" as AddNodeMode });
  const open = useCallback<Open>((reenroll, mode = "ssh") => setState((s) => ({ seq: s.seq + 1, open: true, reenroll, mode })), []);
  const value = useMemo(() => open, [open]);
  return (
    <AddNodeContext value={value}>
      {children}
      {/* a new key per opening: the form starts empty and the token of the last opening is gone */}
      <AddNodeWindow
        key={state.seq}
        open={state.open}
        reenroll={state.reenroll}
        initialMode={state.mode}
        onClose={() => setState((s) => ({ ...s, open: false }))}
      />
    </AddNodeContext>
  );
}

const namePattern = /^[a-z0-9-]{2,24}$/;
const addressPattern = /^[A-Za-z0-9.:-]{1,253}$/;

/** An IPv4 or IPv6 address rather than a domain: Let's Encrypt does not issue for it. */
export const isIPAddress = (address: string) => /^\d{1,3}(\.\d{1,3}){3}$/.test(address) || address.includes(":");

/** A place to start from for the dev kit: the ways of the window and what they hold. */
export type AddNodeSeed = { draft?: Partial<NodeDraft>; ssh?: InstallSeed; issued?: Plain<CreateEnrollmentResponse> };

/**
 * The window: the rail on the left (the way to install and the steps), the page of the step on the right. The two ways keep what
 * the first page asked for (the name, the address, the country) when the person switches between them. `reenroll` is a node that
 * exists: only a new command for it, nothing to fill in.
 */
export function AddNodeWindow({
  open,
  reenroll,
  initialMode = "ssh",
  onClose,
  seed,
}: {
  open: boolean;
  reenroll?: AddNodeTarget;
  initialMode?: AddNodeMode;
  onClose: () => void;
  seed?: AddNodeSeed;
}) {
  const qc = useQueryClient();
  const [mode, setMode] = useState<AddNodeMode>(reenroll ? "cmd" : initialMode);
  const [draft, setDraft] = useState<NodeDraft>({ name: "", address: "", country: noCountry, ...seed?.draft });
  const [issued, setIssued] = useState<Plain<CreateEnrollmentResponse> | null>(seed?.issued ?? null);

  // The token is single-use and shown once: once the window is closed (after its exit animation) drop it from memory.
  useEffect(() => {
    if (open) return;
    const id = setTimeout(() => setIssued(null), 400);
    return () => clearTimeout(id);
  }, [open]);

  // the new node shows up as PENDING in every list at once
  const issuedNow = useCallback(
    (r: Plain<CreateEnrollmentResponse>) => {
      setIssued(r);
      void qc.invalidateQueries({ queryKey: ["nodes"] });
      void qc.invalidateQueries({ queryKey: ["overview"] });
    },
    [qc],
  );

  return (
    <AddNodeShell open={open} onOpenChange={(o) => !o && onClose()}>
      {mode === "ssh" && !reenroll ? (
        <SSHNodeInstall
          draft={draft}
          onDraft={(p) => setDraft((d) => ({ ...d, ...p }))}
          onMode={(m) => setMode(m)}
          onClose={onClose}
          seed={seed?.ssh}
        />
      ) : (
        <CommandInstall
          draft={draft}
          onDraft={(p) => setDraft((d) => ({ ...d, ...p }))}
          onMode={(m) => setMode(m)}
          onClose={onClose}
          reenroll={reenroll}
          issued={issued}
          onIssued={issuedNow}
        />
      )}
    </AddNodeShell>
  );
}

/** One numbered step of the install. */
export function Step({ n, title, done, children }: { n: number; title: string; done?: boolean; children: ReactNode }) {
  return (
    <li className="flex gap-3">
      <span
        className={cx(
          "box-border grid size-6 flex-none place-items-center rounded-full border font-mono text-xs font-bold",
          done ? "tone-ok border-transparent bg-(--c) text-on-accent" : "border-line text-muted",
        )}
      >
        {done ? <Icon name="check" size={12} /> : n}
      </span>
      <div className="flex min-w-0 flex-1 flex-col gap-2 pt-0.5">
        <span className="text-sm leading-snug font-bold text-pretty">{title}</span>
        {children}
      </div>
    </li>
  );
}

/**
 * The way by a command (the board's "Командой"): the name and the address, then three steps on the server (the scp of the agent,
 * the one-line command, the node that shows up by itself), then the page that says the node is there. The command is shown
 * once; a new one is a new enrollment of the same node.
 */
function CommandInstall({
  draft,
  onDraft,
  onMode,
  onClose,
  reenroll,
  issued,
  onIssued,
}: {
  draft: NodeDraft;
  onDraft: (patch: Partial<NodeDraft>) => void;
  onMode: (mode: AddNodeMode) => void;
  onClose: () => void;
  reenroll?: AddNodeTarget;
  issued: Plain<CreateEnrollmentResponse> | null;
  onIssued: (r: Plain<CreateEnrollmentResponse>) => void;
}) {
  const t = useT();
  const fmt = useFmt();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const st = useNodeStatus();
  const [touched, setTouched] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const { name, address, country } = draft;

  const nameBad = !!name && !namePattern.test(name);
  const nameError = !reenroll && !namePattern.test(name) ? t("node.add.nameError") : undefined;
  const addressError = !reenroll && !addressPattern.test(address.trim()) ? t("node.add.addressError") : undefined;
  // said before the click: a node on an IP gets no Let's Encrypt certificate
  const ip = !reenroll && !addressError && isIPAddress(address.trim());

  const create = useMutation({
    mutationFn: (again?: boolean) =>
      reenroll || (again && issued?.node?.id)
        ? nodesApi.createEnrollment({ nodeId: reenroll?.id ?? issued?.node?.id ?? "" })
        : nodesApi.createEnrollment({ name, address: address.trim(), countryCode: country === noCountry ? "" : country }),
    onSuccess: (r) => onIssued(plain(r)),
  });

  function submit(e?: FormEvent, again?: boolean) {
    e?.preventDefault();
    setTouched(true);
    if (!again && (nameError || addressError)) return;
    create.mutate(again);
  }

  // ---- the agent's coming: the live state of the node, every few seconds
  const node = issued?.node;
  const id = node?.id ?? "";
  const live = useQuery({
    queryKey: ["node", id],
    queryFn: async ({ signal }) => plain(await nodesApi.getNode({ nodeId: id }, { signal })),
    refetchInterval: 3_000,
    enabled: !!id,
  });
  const status = live.data?.node?.status ?? node?.status ?? NodeStatus.PENDING;
  const online = status === NodeStatus.ONLINE;
  const fresh = !reenroll;
  // a node that just came online: refresh the lists once, the window has done its job
  useEffect(() => {
    if (!online) return;
    if (fresh && id) markFresh("node", id); // its card in the Nodes list sweeps in once
    void qc.invalidateQueries({ queryKey: ["nodes"] });
    void qc.invalidateQueries({ queryKey: ["overview"] });
  }, [online, qc, fresh, id]);

  const view: "form" | "run" | "done" = !issued ? "form" : online ? "done" : "run";
  const at = view === "form" ? 0 : view === "run" ? 1 : 3;
  const shownName = node?.name ?? reenroll?.name ?? name;
  const steps: RailStep[] = [
    { label: t("win.node.cmdStep1"), sum: at > 0 ? [shownName, address.trim()].filter(Boolean).join(" · ") : "", state: at > 0 ? "done" : "now" },
    { label: t("win.node.cmdStep2"), sum: at > 1 ? t("win.node.expires", { time: fmt.clock(issued?.expiresUnix ?? 0) }) : at === 1 ? t("win.node.expires", { time: fmt.clock(issued?.expiresUnix ?? 0) }) : "", state: at > 1 ? "done" : at === 1 ? "now" : "" },
    { label: t("win.node.cmdStep3"), sum: view === "done" ? t("win.node.connected") : "", state: at > 2 ? "done" : "" },
  ];

  const goNode = (tab?: "profiles") => {
    onClose();
    void navigate({ to: "/nodes/$id", params: { id }, search: tab ? { tab } : {} });
  };

  const title = reenroll ? t("node.add.reenrollTitle", { name: reenroll.name }) : t("win.node.title");
  return (
    <>
      <Rail title={title} mode={reenroll ? undefined : "cmd"} onMode={reenroll ? undefined : onMode} modeLocked={!!issued} steps={steps} note={t("win.node.noteCommand")} />
      <div className="an-m">
        <CloseX onClose={onClose} />

        {view === "form" && (
          <Panel key="form" onSubmit={(e) => submit(e)}>
            <PanelHead eyebrow={t("auth.step", { n: 1, total: 3 })} title={reenroll ? t("node.add.reenrollTitle", { name: reenroll.name }) : t("win.node.cmdTitle")}>
              {reenroll ? t("node.add.reenrollBody") : t("win.node.cmdBody")}
            </PanelHead>
            <div className="an-b">
              {create.isError && <Notice tone="danger">{errorText(create.error, t)}</Notice>}
              {!reenroll && (
                <>
                  <div className="an-r an-r1">
                    <TextField
                      className={cx("an-f", nameBad && "bad")}
                      label={t("node.add.name")}
                      value={name}
                      onChange={(e) => onDraft({ name: e.target.value.toLowerCase() })}
                      placeholder="de1"
                      mono
                      maxLength={24}
                      autoComplete="off"
                      autoCapitalize="off"
                      spellCheck={false}
                      autoFocus
                      error={touched ? nameError : nameBad ? t("node.add.nameError") : undefined}
                      hint={t("node.add.nameHint")}
                    />
                    <CountryField
                      label={
                        <>
                          {t("node.add.country")} <em className="ms-1 text-muted not-italic">{t("win.node.optional")}</em>
                        </>
                      }
                      value={country}
                      onChange={(c) => onDraft({ country: c })}
                      hint={t("node.add.countryHint")}
                    />
                  </div>
                  <TextField
                    className="an-f"
                    label={t("node.add.address")}
                    value={address}
                    onChange={(e) => onDraft({ address: e.target.value })}
                    placeholder="de1.example.com"
                    mono
                    maxLength={253}
                    autoComplete="off"
                    autoCapitalize="off"
                    spellCheck={false}
                    error={touched ? addressError : undefined}
                    hint={t("node.add.addressHint")}
                  />
                  {ip && <Notice title={t("node.add.ipTitle")}>{t("node.add.ipBody")}</Notice>}
                </>
              )}
            </div>
            <PanelFoot>
              <FootNote />
              <Button type="submit" variant="primary" size="lg" disabled={create.isPending || (!reenroll && (!name || !address.trim() || !!nameError || !!addressError))}>
                {create.isPending && <Spinner />}
                {create.isPending ? t("common.creating") : t("node.add.create")}
              </Button>
            </PanelFoot>
          </Panel>
        )}

        {view === "run" && issued && (
          <Panel key="run">
            <PanelHead eyebrow={t("auth.step", { n: 2, total: 3 })} title={t("win.node.cmdRunTitle")}>
              {t("win.node.cmdRunBody")}
            </PanelHead>
            <div className="an-b">
              {create.isError && <Notice tone="danger">{errorText(create.error, t)}</Notice>}
              <ol className="an-ms">
                <li>
                  <span className="an-g an-mono" aria-hidden>
                    1
                  </span>
                  <div>
                    <b>{t("node.add.step1")}</b>
                    {issued.copyCommand ? (
                      <>
                        <CodeBox code={issued.copyCommand} />
                        <span>{t("node.add.step1.copyHint")}</span>
                      </>
                    ) : (
                      <span>{t("node.add.step1.manual")}</span>
                    )}
                  </div>
                </li>
                <li>
                  <span className="an-g an-mono" aria-hidden>
                    2
                  </span>
                  <div>
                    <b>{t("node.add.step2")}</b>
                    <CodeBox wrap code={issued.installCommand} label={t("node.add.copyCommand")} />
                    <span>{t("node.add.once", { time: fmt.clock(issued.expiresUnix) })}</span>
                  </div>
                </li>
                <li>
                  <span className="an-g an-mono" aria-hidden>
                    3
                  </span>
                  <div>
                    <b>{t("node.add.step3")}</b>
                    <span className="an-wt" aria-live="polite">
                      <Spinner />
                      {st.word({ status })} · {t("node.add.waiting")}
                    </span>
                  </div>
                </li>
              </ol>
              <div className="an-kc">
                <button type="button" className="an-lk self-start text-[13px]" aria-expanded={detailsOpen} onClick={() => setDetailsOpen((v) => !v)}>
                  {t("node.add.details")}
                </button>
                {detailsOpen && (
                  <>
                    <span>{t("node.add.fingerprint")}</span>
                    <div className="an-code">
                      <code dir="ltr">{issued.caFingerprint}</code>
                    </div>
                    <span>{t("node.add.fingerprintHint")}</span>
                  </>
                )}
              </div>
            </div>
            <PanelFoot>
              <Button type="button" variant="secondary" size="lg" disabled={create.isPending} onClick={() => submit(undefined, true)}>
                {create.isPending && <Spinner />}
                {t("win.node.newCommand")}
              </Button>
              <FootNote end />
              <Button type="button" variant="secondary" size="lg" onClick={onClose}>
                {t("common.close")}
              </Button>
            </PanelFoot>
          </Panel>
        )}

        {view === "done" && (
          <NodeReady
            nodeId={id}
            line={[shownName, country !== noCountry ? fmt.country(country) : "", address.trim()].filter(Boolean).join(" · ")}
            onClose={onClose}
            onOpen={goNode}
          />
        )}
      </div>
    </>
  );
}
