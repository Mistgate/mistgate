import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { createContext, use, useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { CopyButton } from "@/components/copy-button";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Select } from "@/components/ui/select";
import { StatusPill } from "@/components/ui/status";
import { TextField } from "@/components/ui/text-field";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { CreateEnrollmentResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { plain, type Plain } from "@/lib/plain";
import { useT } from "@/i18n";
import { basepath, nodes as nodesApi } from "@/lib/api";
import { countryCodes } from "@/lib/countries";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { nodeKind, useNodeStatus } from "@/lib/node-status";

// New nodes can be installed automatically over SSH or enrolled with the manual one-time command below.

export type AddNodeTarget = { id: string; name: string };
type Open = (reenroll?: AddNodeTarget) => void;

const AddNodeContext = createContext<Open>(() => {});
/** addNode() opens the modal; addNode({ id, name }) issues a fresh install command for an existing node. */
export const useAddNode = () => use(AddNodeContext);

export function AddNodeProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState({ seq: 0, open: false, reenroll: undefined as AddNodeTarget | undefined });
  const open = useCallback<Open>((reenroll) => setState((s) => ({ seq: s.seq + 1, open: true, reenroll })), []);
  const value = useMemo(() => open, [open]);
  return (
    <AddNodeContext value={value}>
      {children}
      {/* a new key per opening: the form starts empty and the token of the last opening is gone */}
      <AddNodeModal
        key={state.seq}
        open={state.open}
        reenroll={state.reenroll}
        onClose={() => setState((s) => ({ ...s, open: false }))}
      />
    </AddNodeContext>
  );
}

const namePattern = /^[a-z0-9-]{2,24}$/;
const addressPattern = /^[A-Za-z0-9.:-]{1,253}$/;
const none = "none";

/** An IPv4 or IPv6 address rather than a domain: Let's Encrypt does not issue for it. */
export const isIPAddress = (address: string) => /^\d{1,3}(\.\d{1,3}){3}$/.test(address) || address.includes(":");

function AddNodeModal({ open, reenroll, onClose }: { open: boolean; reenroll?: AddNodeTarget; onClose: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [address, setAddress] = useState("");
  const [country, setCountry] = useState(none);
  const [touched, setTouched] = useState(false);
  const [method, setMethod] = useState<"choose" | "manual">("choose");
  const [issued, setIssued] = useState<Plain<CreateEnrollmentResponse> | null>(null);

  // The token is single-use and shown once: once the modal is closed (after its exit animation) drop it from memory.
  useEffect(() => {
    if (open) return;
    const id = setTimeout(() => setIssued(null), 400);
    return () => clearTimeout(id);
  }, [open]);

  const nameError = !reenroll && !namePattern.test(name) ? t("node.add.nameError") : undefined;
  const addressError = !reenroll && !addressPattern.test(address.trim()) ? t("node.add.addressError") : undefined;
  // said before the click: a node on an IP gets no Let's Encrypt certificate
  const ip = !reenroll && !addressError && isIPAddress(address.trim());
  const installHref = `${basepath.replace(/\/+$/, "")}/nodes/install`;

  const countries = useMemo(
    () => [
      { value: none, label: t("node.add.countryNone") },
      ...countryCodes
        .map((c) => ({ value: c, label: `${c} · ${fmt.country(c)}` }))
        .sort((a, b) => fmt.country(a.value).localeCompare(fmt.country(b.value), fmt.lang)),
    ],
    [fmt, t],
  );

  const create = useMutation({
    mutationFn: () =>
      reenroll
        ? nodesApi.createEnrollment({ nodeId: reenroll.id })
        : nodesApi.createEnrollment({
            name,
            address: address.trim(),
            countryCode: country === none ? "" : country,
          }),
    onSuccess: (r) => {
      setIssued(plain(r));
      // the new node shows up as PENDING in every list at once
      void qc.invalidateQueries({ queryKey: ["nodes"] });
      void qc.invalidateQueries({ queryKey: ["overview"] });
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (nameError || addressError) return;
    create.mutate();
  }

  return (
    <Modal
      open={open}
      onOpenChange={(o) => !o && onClose()}
      title={issued ? t("node.add.commandTitle", { name: issued.node?.name ?? "" }) : reenroll ? t("node.add.reenrollTitle", { name: reenroll.name }) : t("node.add.title")}
      description={issued ? t("node.add.commandBody") : reenroll ? t("node.add.reenrollBody") : method === "manual" ? t("node.add.manualBody") : t("node.add.body")}
    >
      {issued ? (
        <InstallSteps issued={issued} onClose={onClose} />
      ) : !reenroll && method === "choose" ? (
        <div className="flex flex-col gap-4">
          <section className="rounded-card border border-accent/40 bg-accent/5 p-4">
            <h3 className="text-base font-bold text-ink">{t("node.add.sshInstallTitle")}</h3>
            <p className="mt-1.5 text-sm leading-relaxed text-muted">{t("node.add.sshInstallHint")}</p>
            <a
              href={installHref}
              className="mt-4 flex min-h-11 w-full items-center justify-center gap-2 rounded-field bg-accent px-4 py-2 text-sm font-bold text-on-accent shadow-sm transition hover:brightness-105 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
            >
              {t("node.add.sshInstall")}
              <span aria-hidden="true">→</span>
            </a>
          </section>
          <div className="flex flex-col gap-2 border-t border-line pt-3">
            <p className="text-center text-xs text-muted">{t("node.add.manualDivider")}</p>
            <Button type="button" variant="outline" size="md" onClick={() => setMethod("manual")}>
              {t("node.add.manualOption")}
            </Button>
          </div>
        </div>
      ) : (
        <form onSubmit={submit} className="flex flex-col gap-3.5">
          {!reenroll && (
            <div className="flex items-center justify-between gap-3">
              <h3 className="text-sm font-bold text-ink">{t("node.add.manualTitle")}</h3>
              <Button type="button" variant="ghost" size="md" onClick={() => { create.reset(); setTouched(false); setMethod("choose"); }}>
                {t("node.add.manualBack")}
              </Button>
            </div>
          )}
          {!reenroll && (
            <>
              <TextField
                label={t("node.add.name")}
                value={name}
                onChange={(e) => setName(e.target.value.toLowerCase())}
                placeholder="de1"
                mono
                maxLength={24}
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
                autoFocus
                error={touched ? nameError : undefined}
                hint={t("node.add.nameHint")}
              />
              <div className="flex flex-col gap-1.5">
                <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("node.add.country")}</span>
                <Select value={country} onValueChange={setCountry} options={countries} aria-label={t("node.add.country")} />
                <p className="text-xs leading-snug text-muted">{t("node.add.countryHint")}</p>
              </div>
              <TextField
                label={t("node.add.address")}
                value={address}
                onChange={(e) => setAddress(e.target.value)}
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
          {/* a refusal stays in the window, above its buttons */}
          {create.isError && <Notice tone="danger">{errorText(create.error, t)}</Notice>}
          <div className="flex justify-end gap-2 pt-1">
            <Button variant="ghost" size="md" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" variant="primary" size="md" disabled={create.isPending}>
              {create.isPending ? t("common.creating") : t("node.add.create")}
            </Button>
          </div>
        </form>
      )}
    </Modal>
  );
}

/** One numbered step of the install. */
function Step({ n, title, done, children }: { n: number; title: string; done?: boolean; children: ReactNode }) {
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

const codeCls = "rounded-field border border-line bg-canvas p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap text-fg select-all";

/**
 * Three steps: put the binary on the server (a ready scp from the panel's own trusted bundle when it has one), paste the
 * one-line command as root, wait. The CA fingerprint is under "Details": it is inside the command already.
 */
export function InstallSteps({ issued, onClose }: { issued: Plain<CreateEnrollmentResponse>; onClose: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const node = issued.node;
  const id = node?.id ?? "";
  const name = node?.name ?? "";

  const live = useQuery({
    queryKey: ["node", id],
    queryFn: async ({ signal }) => plain(await nodesApi.getNode({ nodeId: id }, { signal })),
    refetchInterval: 3_000,
    enabled: !!id,
  });
  const status = live.data?.node?.status ?? node?.status ?? NodeStatus.PENDING;
  const online = status === NodeStatus.ONLINE;
  const st = useNodeStatus();
  // the next step after the agent: a profile, unless the node already has some (a new command for a reinstall)
  const needsProfile = (live.data?.inbounds.length ?? 0) === 0;

  // a node that just came online: refresh the lists once, the modal has done its job
  useEffect(() => {
    if (!online) return;
    void qc.invalidateQueries({ queryKey: ["nodes"] });
    void qc.invalidateQueries({ queryKey: ["overview"] });
  }, [online, qc]);

  const go = (tab?: "profiles") => {
    onClose();
    void navigate({ to: "/nodes/$id", params: { id }, search: tab ? { tab } : {} });
  };

  return (
    <div className="flex flex-col gap-4">
      <ol className="flex flex-col gap-4">
        <Step n={1} title={t("node.add.step1")}>
          {issued.copyCommand ? (
            <>
              <pre tabIndex={0} className={codeCls}>
                {issued.copyCommand}
              </pre>
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                <CopyButton value={issued.copyCommand} />
                <span className="min-w-0 flex-1 text-xs leading-snug text-pretty text-muted">{t("node.add.step1.copyHint")}</span>
              </div>
            </>
          ) : (
            <p className="text-xs leading-snug text-pretty text-muted">{t("node.add.step1.manual")}</p>
          )}
        </Step>
        <Step n={2} title={t("node.add.step2")}>
          <pre tabIndex={0} className={codeCls}>
            {issued.installCommand}
          </pre>
          <CopyButton value={issued.installCommand} label={t("node.add.copyCommand")} variant="primary" size="md" full />
          <span className="text-xs text-muted">{t("node.add.once", { time: fmt.clock(issued.expiresUnix) })}</span>
        </Step>
        <Step n={3} title={t("node.add.step3")} done={online}>
          <div className="flex items-center gap-2.5 rounded-field border border-line bg-surface-2 px-3 py-2.5" aria-live="polite">
            <StatusPill kind={online ? "ok" : nodeKind({ status })} label={online ? t("node.add.connected") : st.word({ status })} sm />
            <span className="text-xs leading-snug text-pretty text-muted">{online ? t("node.add.connectedBody") : t("node.add.waiting")}</span>
          </div>
        </Step>
      </ol>

      <details className="group rounded-field border border-line px-3 py-2">
        <summary className="cursor-pointer list-none text-xs font-bold text-muted select-none hover:text-fg [&::-webkit-details-marker]:hidden">
          <span className="inline-flex items-center gap-1.5">
            <Icon name="chevronRight" size={12} className="transition-transform duration-200 group-open:rotate-90" />
            {t("node.add.details")}
          </span>
        </summary>
        <div className="flex flex-col gap-1.5 pt-2.5 pb-1">
          <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("node.add.fingerprint")}</span>
          <code className="font-mono text-xs leading-relaxed break-all text-muted select-all">{issued.caFingerprint}</code>
          <p className="text-xs leading-snug text-muted">{t("node.add.fingerprintHint")}</p>
        </div>
      </details>

      <div className="flex flex-wrap justify-end gap-2">
        {online && id ? (
          <>
            <Button variant="secondary" size="md" onClick={onClose}>
              {t("common.done")}
            </Button>
            <Button variant="primary" size="md" onClick={() => go(needsProfile ? "profiles" : undefined)}>
              {needsProfile ? t("node.add.addProfile", { name }) : t("node.add.open")}
            </Button>
          </>
        ) : (
          <Button variant="ghost" size="md" onClick={onClose}>
            {t("common.close")}
          </Button>
        )}
      </div>
    </div>
  );
}
