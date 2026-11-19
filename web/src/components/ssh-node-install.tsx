import { ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useState, type FormEvent } from "react";
import { CheckNote, CloseX, CodeBox, CopyChip, CountryField, FootNote, noCountry, Panel, PanelFoot, PanelHead, Rail, WayCards, type NodeDraft, type RailStep } from "@/components/add-node-frame";
import { markFresh } from "@/components/fx/registry";
import { authorizedKeysFile, usePanelKeyInUse, usePanelSSHKey } from "@/components/panel-ssh-key";
import { Fingerprint, OwnKeyField, useSSHLogin, type LoginMethod } from "@/components/ssh-login";
import { SharePick, useNodeShare } from "@/components/node-share-pick";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Chip, Check } from "@/components/ui/bits";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { TextField } from "@/components/ui/text-field";
import { Glyph, glyphs, Halo, Spinner, WaveField } from "@/components/windows";
import type { NodePreflight } from "@/gen/mistgate/admin/v1/provisioning_pb";
import { SubscriptionHostState } from "@/gen/mistgate/subhost/v1/subhost_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { nodes as nodesApi, provisioning } from "@/lib/api";
import { cx } from "@/lib/cx";
import { prefersReducedMotion } from "@/lib/effects";
import { useEdition } from "@/lib/edition";
import { errorCode, errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { subHostQuery } from "@/lib/queries";
import { rememberShare } from "@/lib/node-share";
import { plain } from "@/lib/plain";

// The SSH install of a node, the board's PnAddNode (.an): four steps (the server, the way in, the check, the install) in the
// window's rail, then the page that says the node is ready. The calls and their order are the panel's own: the pinned host key
// first, the check with the login, the confirmation, the job and its retries.

// What a failure code of the provisioning calls (CheckSSH and the others) or of a job (provision/worker.go failJob) means
// for the owner, and what to do about it. One table for both: a code means the same wherever it comes from.
const codeText: Record<string, MessageKey> = {
  ssh_fingerprint_timeout: "node.ssh.error.timeout",
  ssh_connection_timeout: "node.ssh.error.timeout",
  ssh_preflight_timeout: "node.ssh.error.timeout",
  ssh_fingerprint_unavailable: "node.ssh.error.unavailable",
  ssh_connection_refused: "node.ssh.error.unavailable",
  ssh_connection_unavailable: "node.ssh.error.unavailable",
  ssh_target_not_public: "node.ssh.error.notPublic",
  ssh_target_invalid: "node.ssh.error.notPublic",
  ssh_target_refused: "node.ssh.error.refused",
  "invalid ssh target": "node.ssh.error.notPublic",
  ssh_authentication_failed: "node.ssh.error.authentication",
  ssh_password_auth_unavailable: "node.ssh.error.passwordOff",
  ssh_host_key_changed: "node.ssh.error.hostKey",
  unsupported_os: "node.ssh.error.unsupportedOS",
  unsupported_os_version: "node.ssh.error.unsupportedOS",
  unsupported_architecture: "node.ssh.error.unsupportedArch",
  systemd_required: "node.ssh.error.noSystemd",
  insufficient_resources: "node.ssh.error.resources",
  insufficient_disk_space: "node.ssh.error.disk",
  panel_address_not_configured: "node.ssh.error.panelAddress",
  panel_unreachable: "node.ssh.error.panelUnreachable",
  ssh_preflight_failed: "node.ssh.error.preflight",
  name_taken: "node.ssh.nameTaken",
  node_name_taken: "node.ssh.nameTaken",
  node_retired: "node.ssh.error.retired",
  node_identity_mismatch: "node.ssh.error.identity",
  node_identity_unreadable: "node.ssh.error.identity",
  agent_bundle_unavailable: "node.ssh.error.bundle",
  remote_outcome_unknown: "node.ssh.error.remoteOutcome",
  host_firewall_configuration_failed: "node.ssh.error.firewall",
  nftables_install_failed: "node.ssh.error.nftables",
  node_not_connected: "node.ssh.error.notConnected",
  // the first subscription host (provision/worker.go): it enrols in the session, and the panel dials it once it is installed
  subscription_host_tunnel_down: "node.ssh.error.hostTunnel",
  subscription_host_activation_failed: "node.ssh.error.hostActivation",
  subscription_host_unavailable: "node.ssh.error.hostUnavailable",
  panel_link_not_configured: "node.ssh.error.hostNoLink",
  systemd_install_failed: "node.ssh.error.systemd",
  agent_install_failed: "node.ssh.error.systemd",
  // an own private key, and the panel key the install puts on the server (provision/private_key.go, server_keys.go)
  ssh_private_key_invalid: "node.ssh.error.privateKey",
  ssh_key_passphrase_required: "node.ssh.passphraseHint",
  ssh_key_passphrase_invalid: "node.ssh.error.passphrase",
  authorized_keys_unsafe: "node.sshKeys.error.unsafe",
  authorized_keys_changed: "node.sshKeys.error.changed",
  authorized_keys_too_large: "node.sshKeys.error.tooLarge",
  ssh_authorized_keys_unavailable: "node.sshKeys.error.unavailable",
  node_server_access_exists: "node.ssh.error.accessExists",
  // a login with a node's saved access that broke off or was cut (provision/worker.go publicSSHCode)
  ssh_connection_failed: "node.ssh.error.connection",
  ssh_connection_canceled: "node.ssh.error.connection",
};

// On the Cloudflare edition the panel dials from Cloudflare's network, so a timeout means something else there.
function codeMessage(key: MessageKey, t: T, edge: boolean): string {
  return t(edge && key === "node.ssh.error.timeout" ? "node.ssh.error.timeoutEdge" : key);
}

export function sshError(error: unknown, t: T, edge = false): string {
  const raw = ConnectError.from(error).rawMessage.toLowerCase();
  const key = Object.hasOwn(codeText, raw) ? codeText[raw] : raw.includes("sudo") ? "node.ssh.error.sudo" : undefined;
  return key ? codeMessage(key, t, edge) : errorText(error, t);
}

export function jobError(code: string, t: T, edge = false): string {
  return codeMessage(Object.hasOwn(codeText, code) ? codeText[code]! : "node.ssh.error.install", t, edge);
}

// A refused login in the words of the way in: the panel key says where it must be, an own key that the server refused
// it, and a panel key the install could not put to use (own key, or a password with the box ticked) why.
function loginCode(code: string, method: LoginMethod, user: string, t: T): string | undefined {
  if (method === "key" && (code === "ssh_panel_key_not_accepted" || code === "ssh_authentication_failed")) return t("node.ssh.error.panelKey", { file: authorizedKeysFile, user });
  if (method === "own" && code === "ssh_authentication_failed") return t("node.ssh.error.ownKey", { file: authorizedKeysFile, user });
  if (method !== "key" && code === "ssh_panel_key_not_accepted") return t("node.sshKeys.error.notAccepted");
  return undefined;
}

/** sshError, worded by the login it used (CheckSSH, Start, Retry, Attach). */
export function loginError(error: unknown, method: LoginMethod, user: string, t: T, edge = false): string {
  return loginCode(ConnectError.from(error).rawMessage, method, user, t) ?? sshError(error, t, edge);
}


function suggestNodeName(host: string) {
  const first = host.trim().toLowerCase().split(".")[0] ?? "";
  const label = first.replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 24);
  return label.length >= 2 ? label : "node-1";
}


// The worker's phases (provision/worker.go setPhase) in the order they run; an unknown one reads as "Installing", never as a raw code.
function phaseLabel(phase: string, t: T): string {
  switch (phase) {
    case "queued":
      return t("node.ssh.phase.queued");
    case "connecting":
      return t("node.ssh.phase.connecting");
    case "preflight":
      return t("node.ssh.phase.preflight");
    case "access":
      return t("node.ssh.phase.access");
    case "nftables":
      return t("node.ssh.phase.nftables");
    case "firewall":
      return t("node.ssh.phase.firewall");
    case "transfer":
      return t("node.ssh.phase.transfer");
    case "enrollment":
      return t("node.ssh.phase.enrollment");
    case "install":
      return t("node.ssh.phase.install");
    case "waiting_node":
      return t("node.ssh.phase.waiting_node");
    case "cancelling":
      return t("node.ssh.phase.cancelling");
    default:
      return t("node.ssh.state.running");
  }
}

/** The ssh-keygen command that prints the fingerprint of the key the server offered, for the file of its algorithm. */
function keygenCommand(algorithm: string) {
  const kind = algorithm.startsWith("ecdsa") ? "ecdsa" : algorithm.startsWith("ssh-rsa") ? "rsa" : "ed25519";
  return `ssh-keygen -lf /etc/ssh/ssh_host_${kind}_key.pub`;
}

type Step = 0 | 1 | 2 | 3;
type Busy = "fingerprint" | "check" | "start" | "retry" | null;

const nodeNamePattern = /^[a-z0-9-]{2,24}$/;
const nodeAddressPattern = /^[A-Za-z0-9.:-]{1,253}$/;
const usernamePattern = /^[A-Za-z_][A-Za-z0-9_.-]{0,31}$/;
// node_provision_job.state (migration 00031): queued, running, cancel_requested, then completed, failed or cancelled.
const terminalStates = new Set(["completed", "failed", "cancelled"]);
// What the install does before the first change on the server: a failure there leaves the server as it was.
const beforeChanges = new Set(["", "queued", "connecting", "preflight", "failed"]);

/** The ports the host firewall step opens: the SSH one, the web ones and the QUIC one. */
const firewallPorts = (ssh: number) => [`${ssh}/tcp`, "80/tcp", "443/tcp", "443/udp"];

/**
 * A made-up place to start from: the dev kit opens each state of the window (the board's PnAddNode) with it. The fields are
 * what the person would have typed; `jobId` points at a job whose state the page seeds into its query cache.
 */
export type InstallSeed = Partial<{
  step: Step;
  host: string;
  port: string;
  fingerprint: string;
  algorithm: string;
  confirmedKey: boolean;
  username: string;
  method: LoginMethod;
  preflight: NodePreflight;
  jobId: string;
  location: string;
  provider: string;
  optionsOpen: boolean;
  whereOpen: boolean;
  countryOpen: boolean;
  /** A refusal already on the page. */
  failure: string;
  /** How many rows of the check have answered (the dev kit stops the check at a row). */
  answered: number;
  /** The rows of the check unfolded or folded behind their summary (without it: open for a remark, folded when all is well). */
  checksOpen: boolean;
  /** The words about the firewall unfolded. */
  firewallOpen: boolean;
}>;

type Props = {
  draft: NodeDraft;
  onDraft: (patch: Partial<NodeDraft>) => void;
  onMode: (mode: "ssh" | "cmd") => void;
  onClose: () => void;
  seed?: InstallSeed;
};

type Fact = { key: string; label: string; value: string; bad?: boolean; warn?: boolean; note?: string };

/** The "!" of a remark in the round mark of a check row (the board's WRN). */
const warnMark = "M12 7v6M12 16.5v.01";

export function SSHNodeInstall({ draft, onDraft, onMode, onClose, seed }: Props) {
  const t = useT();
  const { edge } = useEdition();
  const fmt = useFmt();
  const guard = useStepUp();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [step, setStep] = useState<Step>(seed?.step ?? 0);
  const [busy, setBusy] = useState<Busy>(null);
  const [failure, setFailure] = useState(seed?.failure ?? "");
  const [host, setHost] = useState(seed?.host ?? "");
  const [port, setPort] = useState(seed?.port ?? "22");
  const [fingerprint, setFingerprint] = useState(seed?.fingerprint ?? "");
  const [algorithm, setAlgorithm] = useState(seed?.algorithm ?? "");
  const [confirmedKey, setConfirmedKey] = useState(seed?.confirmedKey ?? false);
  const { name, address, country } = draft;
  const share = useNodeShare(draft.share);
  const [location, setLocation] = useState(seed?.location ?? "");
  const [provider, setProvider] = useState(seed?.provider ?? "");
  const [username, setUsername] = useState(seed?.username ?? "root");
  const [touched, setTouched] = useState(false);
  const [confirmedInstall, setConfirmedInstall] = useState(false);
  const [preflight, setPreflight] = useState<NodePreflight | null>(seed?.preflight ?? null);
  const [jobId, setJobId] = useState(seed?.jobId ?? "");
  // the new node may carry the subscription address (a panel behind NAT): offered while no node does, VPS edition only
  const hostable = !useEdition().off("subscription_host");
  const subHostOff = useQuery({ ...subHostQuery, refetchInterval: false, enabled: hostable }).data?.state === SubscriptionHostState.OFF;
  const [asSubHost, setAsSubHost] = useState(false);
  const [subDomain, setSubDomain] = useState("");
  const [subSite, setSubSite] = useState("");
  const [dnsMismatch, setDnsMismatch] = useState(false);
  const hostInstall = hostable && subHostOff && asSubHost;
  const [optionsOpen, setOptionsOpen] = useState(seed?.optionsOpen ?? false);
  const [whereOpen, setWhereOpen] = useState(seed?.whereOpen ?? false);
  // the answers of the server check come in one by one: how many have
  const [answered, setAnswered] = useState(seed?.answered ?? (seed?.preflight ? 99 : 0));
  // the rows of the check hide behind one line once they are all in, unless one of them has a remark; null: the person has not chosen
  const [checksOpen, setChecksOpen] = useState<boolean | null>(seed?.checksOpen ?? null);
  const [firewallOpen, setFirewallOpen] = useState(seed?.firewallOpen ?? false);
  // the panel key is the default once it logs in to some node, else the password; the owner's own key never is
  const keyInUse = usePanelKeyInUse();
  const login = useSSHLogin(seed?.method ?? (keyInUse ? "key" : "password"));
  const viaKey = login.method === "key";
  const { password, setPassword } = login;
  // the how-to under the panel key: open for a key not in use yet, and whenever the server refused it
  const [howTo, setHowTo] = useState<boolean | null>(null);

  const job = useQuery({
    queryKey: ["node-provision", jobId],
    queryFn: async ({ signal }) => {
      const response = await provisioning.getNodeProvision({ jobId }, { signal });
      return response.job;
    },
    enabled: !!jobId,
    refetchInterval: (query) => {
      const state = query.state.data?.state ?? "";
      return terminalStates.has(state) ? false : 2_500;
    },
  });
  // every phase the worker went through (it moves faster than a poll of the job can see)
  const events = useQuery({
    queryKey: ["node-provision-events", jobId],
    queryFn: async ({ signal }) => (await provisioning.listNodeProvisionEvents({ jobId, limit: 100 }, { signal })).events,
    enabled: !!jobId,
    retry: false,
    refetchInterval: () => (terminalStates.has(job.data?.state ?? "") ? false : 2_500),
  });

  const labels = [t("node.ssh.step.server"), t("node.ssh.step.login"), t("node.ssh.step.check"), t("node.ssh.step.install")];
  // a refusal is worded by the way in it used and a refused panel key opens the how-to; a key that needs its passphrase
  // asks for it, with no error (nothing went wrong yet)
  const fail = (error: unknown) => {
    const code = ConnectError.from(error).rawMessage;
    if (code === "ssh_key_passphrase_required") {
      login.askPassphrase();
      return setFailure("");
    }
    if (code === "ssh_key_passphrase_invalid") login.setPassphrase("");
    setFailure(loginError(error, login.method, username.trim(), t, edge));
    if (viaKey && (code === "ssh_panel_key_not_accepted" || code === "ssh_authentication_failed")) setHowTo(true);
  };
  const jobText = (code: string) => loginCode(code, login.method, username.trim(), t) ?? jobError(code, t, edge);
  const nameBad = !!name && !nodeNamePattern.test(name.trim());
  const addressBad = !!address.trim() && !nodeAddressPattern.test(address.trim());
  const usernameError = touched && !usernamePattern.test(username.trim()) ? t("node.ssh.loginError") : "";
  const portNumber = Number(port);
  const portValid = Number.isInteger(portNumber) && portNumber >= 1 && portNumber <= 65535;
  const serverOk = !!host.trim() && portValid && nodeNamePattern.test(name.trim()) && nodeAddressPattern.test(address.trim()) && (!hostInstall || (!!subDomain.trim() && !!subSite.trim()));
  const jobState = job.data?.state ?? "";
  // the install manager speaks the admin's language: it cannot read the SPA's choice by itself
  const installManagerHref = new URL(`nodes/install?lang=${fmt.lang}&job=${encodeURIComponent(jobId)}`, document.baseURI).href;
  const jobDone = jobState === "completed";
  const nodeId = job.data?.nodeId;
  const jobFailed = jobState === "failed";
  const jobCancelled = jobState === "cancelled";
  const jobStopped = jobFailed || jobCancelled;
  // a cancelled job resumes safely (preflight checks the host's enrollment first); the panel refuses a retired node
  const jobCanRetry = jobStopped && job.data?.errorCode !== "node_retired";

  useEffect(() => {
    if (!jobDone && !jobFailed) return;
    if (jobDone && nodeId) markFresh("node", nodeId); // the new node's card is washed once
    void qc.invalidateQueries({ queryKey: ["nodes"] });
    void qc.invalidateQueries({ queryKey: ["overview"] });
  }, [jobDone, jobFailed, nodeId, qc]);

  async function getFingerprint(event?: FormEvent) {
    event?.preventDefault();
    setFailure("");
    if (!host.trim() || !portValid || !serverOk) {
      setFailure(t("node.ssh.error.notPublic"));
      return;
    }
    setBusy("fingerprint");
    try {
      const response = await provisioning.getSSHFingerprint({ host: host.trim(), port: portNumber });
      setFingerprint(response.fingerprint);
      setAlgorithm(response.algorithm);
      setConfirmedKey(false);
      setStep(1);
    } catch (error) {
      setFailure(sshError(error, t, edge));
    } finally {
      setBusy(null);
    }
  }

  async function checkServer(event?: FormEvent) {
    event?.preventDefault();
    setTouched(true);
    setFailure("");
    if (!confirmedKey || !nodeNamePattern.test(name.trim()) || !nodeAddressPattern.test(address.trim()) || !usernamePattern.test(username.trim()) || !login.ready) {
      if (!confirmedKey) setFailure(t("node.ssh.confirmKey"));
      return;
    }
    setBusy("check");
    // the page of the check opens at once: its rows wait for the answer
    setPreflight(null);
    setAnswered(0);
    setChecksOpen(null);
    setFirewallOpen(false);
    setStep(2);
    try {
      const response = await guard(
        () =>
          provisioning.checkSSH({
            host: host.trim(),
            port: portNumber,
            fingerprint,
            ...login.request(),
            username: username.trim(),
            // the first subscription host need not reach the panel: the check looks at its subdomain instead
            ...(hostInstall ? { subscriptionHostDomain: subDomain.trim() } : {}),
          }),
        t("win.node.whatCheck", { host: host.trim() }),
      );
      if (!response.preflight) throw new Error("preflight_missing");
      setPreflight(response.preflight);
      login.hold(); // the way in just proven holds for the install and its retries (an own key stays loaded for Start)
      setPassword("");
      setConfirmedInstall(false);
      // the rows answer one after the other (the board's pace); with reduced motion all at once
      setAnswered(prefersReducedMotion() ? 99 : 0);
    } catch (error) {
      setStep(1);
      if (!isStepUpCancelled(error)) fail(error);
    } finally {
      setPassword("");
      setBusy(null);
    }
  }

  async function startInstall(event?: FormEvent, skipDnsCheck = false) {
    event?.preventDefault();
    setFailure("");
    setDnsMismatch(false);
    if (!confirmedInstall || !login.ready) {
      setFailure(t("node.ssh.confirmInstall"));
      return;
    }
    if (!share.valid) return;
    setBusy("start");
    try {
      const response = await guard(
        () =>
          provisioning.startNodeProvision({
            confirmInstall: true,
            name: name.trim().toLowerCase(),
            address: address.trim(),
            countryCode: country === noCountry ? "" : country,
            location: location.trim(),
            provider: provider.trim(),
            sshHost: host.trim(),
            sshPort: portNumber,
            fingerprint,
            ...login.request(),
            sshUsername: username.trim(),
            ...login.install(),
            ...share.request,
            ...(hostInstall ? { subscriptionHostDomain: subDomain.trim(), subscriptionHostSiteName: subSite.trim(), skipDnsCheck } : {}),
          }),
        t("win.node.whatInstall", { host: host.trim() }),
      );
      if (!response.job?.id) throw new Error("provision_job_missing");
      if (share.ask) rememberShare(share.choice);
      login.clearSecrets(); // the job holds them now, sealed; a retry asks again
      setJobId(response.job.id);
      qc.setQueryData(["node-provision", response.job.id], response.job);
      setStep(3);
    } catch (error) {
      if (!isStepUpCancelled(error)) {
        fail(error);
        if (errorCode(ConnectError.from(error).rawMessage) === "sub_host_dns_mismatch") setDnsMismatch(true);
      }
    } finally {
      setPassword("");
      setBusy(null);
    }
  }

  async function retryInstall() {
    if (!jobId || !login.ready) return;
    setFailure("");
    setBusy("retry");
    try {
      const response = await guard(
        () =>
          provisioning.retryNodeProvision({
            jobId,
            confirmInstall: true,
            ...login.request(),
            sshUsername: username.trim(),
            ...login.install(),
          }),
        t("win.node.whatInstall", { host: host.trim() }),
      );
      login.clearSecrets();
      if (response.job) qc.setQueryData(["node-provision", jobId], response.job);
      await qc.invalidateQueries({ queryKey: ["node-provision", jobId] });
    } catch (error) {
      if (!isStepUpCancelled(error)) fail(error);
    } finally {
      setPassword("");
      setBusy(null);
    }
  }

  const back = (to: Step) => {
    setFailure("");
    if (to < 2) {
      setPreflight(null);
      setPassword("");
    }
    setStep(to);
  };

  const openNode = (tab?: "profiles") => {
    if (!nodeId) return;
    onClose();
    void navigate({ to: "/nodes/$id", params: { id: nodeId }, ...(tab ? { search: { tab } } : {}) });
  };

  // ---- the stages of the install and where the job is
  const stageIds = [
    "connecting",
    "preflight",
    ...(login.install().installPanelKey ? ["access"] : []),
    ...(preflight && !preflight.nftables ? ["nftables"] : []),
    "firewall",
    "transfer",
    ...(preflight?.alreadyEnrolled ? [] : ["enrollment"]),
    "install",
    "waiting_node",
  ];
  const seen = [job.data?.phase ?? "", ...(events.data ?? []).map((e) => e.phase)].map((p) => stageIds.indexOf(p));
  // the last stage the worker reached: a failed job says "failed" as its phase, so the events tell where it stopped
  const reached = Math.max(-1, ...seen);
  const running = !!jobId && !jobStopped && !jobDone;
  const at = jobDone ? stageIds.length : Math.max(0, reached);

  const hostHint = t(edge ? "node.ssh.hostHintEdge" : "node.ssh.hostHint");
  const factList = factRows(preflight, t, fmt, hostInstall);
  // the answers come in one by one; the page of the check is done a moment after the last one
  useEffect(() => {
    if (!preflight || answered > factList.length) return;
    const id = setTimeout(() => setAnswered((n) => n + 1), answered === 0 ? 450 : answered === factList.length ? 350 : 190);
    return () => clearTimeout(id);
  }, [preflight, answered, factList.length]);
  const checked = !!preflight && answered > factList.length;
  // a remark (the image has no nft) or a row that says no keeps the rows open; all well: one line "9 checks passed"
  const remarks = factList.filter((f) => f.warn).length;
  const failed = factList.filter((f) => f.bad).length;
  const rowsOpen = !checked || (checksOpen ?? (remarks + failed > 0));
  const passed = factList.length - remarks - failed;

  const view: "srv" | "auth" | "chk" | "run" | "done" = jobDone ? "done" : step === 0 ? "srv" : step === 1 ? "auth" : step === 2 ? "chk" : "run";
  const railAt = view === "done" ? 4 : step;
  const authName = login.method === "key" ? t("panelKey.title") : login.method === "password" ? t("node.ssh.methodPassword") : t("node.ssh.methodOwn");
  const sums = [
    railAt > 0 ? `${host.trim()}:${portNumber}` : "",
    railAt > 1 ? `${username.trim()} · ${authName}` : "",
    railAt > 2 && preflight ? [[preflight.distribution, preflight.version].filter(Boolean).join(" "), preflight.architecture].filter(Boolean).join(" · ") : "",
    view === "done" ? t("win.node.connected") : jobStopped ? t("win.node.failed") : view === "run" ? t("win.node.stageOf", { n: Math.min(stageIds.length, at + 1), total: stageIds.length }) : "",
  ];
  const locked = view === "run" || view === "done";
  const railSteps: RailStep[] = labels.map((label, i) => ({
    label,
    sum: sums[i] ?? "",
    state: i < railAt ? "done" : i === railAt ? (view === "run" && jobStopped ? "err" : "now") : "",
    onGo: i < railAt && !locked && !busy ? () => back(i as Step) : undefined,
  }));
  const note = jobStopped ? t("win.node.noteFailed") : view === "run" || view === "done" ? t("win.node.noteRun") : t("win.node.noteStart");

  return (
    <>
      <Rail title={t("win.node.title")} mode="ssh" onMode={onMode} modeLocked={locked} steps={railSteps} note={note} />
      <div className="an-m">
        <CloseX onClose={onClose} />

        {view === "srv" && (
          <Panel key="srv" onSubmit={(e) => void getFingerprint(e)}>
            <PanelHead eyebrow={t("auth.step", { n: 1, total: 4 })} title={t("win.node.srvTitle")}>
              {t("win.node.srvBody")}
            </PanelHead>
            <div className="an-b">
              {failure && <FailureNotice failure={failure} onManual={failure === t("node.ssh.error.refused") ? () => onMode("cmd") : undefined} />}
              <div className="an-r an-r2">
                <TextField
                  className="an-f"
                  label={t("node.ssh.host")}
                  value={host}
                  onChange={(e) => setHost(e.target.value)}
                  onBlur={() => {
                    // the name and the address for clients start from the host, when they are still empty
                    if (host.trim() && !name.trim()) onDraft({ name: suggestNodeName(host) });
                    if (host.trim() && !address.trim()) onDraft({ address: host.trim() });
                  }}
                  placeholder={t("win.node.hostPlaceholder")}
                  mono
                  autoComplete="off"
                  autoCapitalize="off"
                  spellCheck={false}
                  autoFocus
                />
                <TextField
                  className="an-f"
                  label={t("node.ssh.port")}
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
                  placeholder="22"
                  mono
                  inputMode="numeric"
                  autoComplete="off"
                />
              </div>
              <p className="-mt-1 text-xs leading-4 text-muted">
                {hostHint} {t(edge ? "node.ssh.providerFirewallHintEdge" : "node.ssh.providerFirewallHint")}
              </p>
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
                  error={nameBad ? t("node.add.nameError") : undefined}
                  hint={t("node.ssh.nameHint")}
                />
                <TextField
                  className={cx("an-f", addressBad && "bad")}
                  label={t("win.node.publicAddress")}
                  value={address}
                  onChange={(e) => onDraft({ address: e.target.value })}
                  placeholder="de1.example.com"
                  mono
                  maxLength={253}
                  autoComplete="off"
                  autoCapitalize="off"
                  spellCheck={false}
                  error={addressBad ? t("node.add.addressError") : undefined}
                  hint={t("node.ssh.addressHint")}
                />
              </div>
              <div className={cx("an-opt", optionsOpen && "open")}>
                <button type="button" className="an-optb" aria-expanded={optionsOpen} onClick={() => setOptionsOpen((v) => !v)}>
                  <span>{t("win.node.optionsTitle")}</span>
                  <em>{t("win.node.optional")}</em>
                  <Glyph d={glyphs.chevron} />
                </button>
                <div className="an-lb">{t("win.node.optional")}</div>
                <div className="an-r an-r3">
                  <CountryField label={t("node.add.country")} value={country} onChange={(c) => onDraft({ country: c })} defaultOpen={seed?.countryOpen} />
                  <TextField className="an-f" label={t("node.ssh.location")} value={location} onChange={(e) => setLocation(e.target.value)} maxLength={100} autoComplete="off" />
                  <TextField className="an-f" label={t("node.ssh.provider")} value={provider} onChange={(e) => setProvider(e.target.value)} maxLength={100} autoComplete="off" />
                </div>
              </div>
              {hostable && subHostOff && (
                <div className="an-r">
                  <Check label={t("set.domains.subHost.installCheck")} checked={asSubHost} onCheckedChange={(v) => setAsSubHost(v)} />
                  {asSubHost && (
                    <>
                      <TextField
                        className="an-f"
                        label={t("set.domains.subHost.domain")}
                        value={subDomain}
                        onChange={(e) => setSubDomain(e.target.value)}
                        placeholder="sub.example.com"
                        mono
                        maxLength={253}
                        autoComplete="off"
                        hint={t("set.domains.subHost.installHint")}
                        required
                      />
                      <TextField
                        className="an-f"
                        label={t("set.domains.subHost.siteName")}
                        value={subSite}
                        onChange={(e) => setSubSite(e.target.value)}
                        placeholder="www.example.com"
                        mono
                        maxLength={253}
                        autoComplete="off"
                        hint={t("set.domains.subHost.installSiteHint")}
                        required
                      />
                    </>
                  )}
                </div>
              )}
            </div>
            <PanelFoot>
              <FootNote glyph={glyphs.lock}>{serverOk ? t("win.node.srvReady") : nameBad ? t("win.node.srvNameBad") : t("win.node.srvFill")}</FootNote>
              <Button type="submit" variant="primary" size="lg" disabled={busy !== null || !serverOk}>
                {busy === "fingerprint" && <Spinner />}
                {busy === "fingerprint" ? t("node.ssh.checkingFingerprint") : t("node.ssh.checkServer")}
              </Button>
            </PanelFoot>
          </Panel>
        )}

        {view === "auth" && (
          <Panel key="auth" onSubmit={(e) => void checkServer(e)}>
            <PanelHead eyebrow={t("auth.step", { n: 2, total: 4 })} title={t("win.node.authTitle")}>
              {t("win.node.authBody")}
            </PanelHead>
            <div className="an-b">
              {failure && <FailureNotice failure={failure} />}
              <section className={cx("an-k", confirmedKey && "ok")}>
                <div className="an-kh">
                  <b>{t("win.node.serverKey")}</b>
                  {algorithm && <Chip mono className="font-medium">{algorithm}</Chip>}
                  <Chip mono className="font-medium">{fingerprint.startsWith("SHA256:") ? "SHA-256" : (fingerprint.split(":")[0] ?? "")}</Chip>
                  <CopyChip value={fingerprint} />
                </div>
                <div className="an-fp" dir="ltr" data-testid="fingerprint">
                  <span className="sr-only">{fingerprint}</span>
                  {(fingerprint.slice(fingerprint.indexOf(":") + 1).match(/.{1,4}/g) ?? []).map((chunk, i) => (
                    <span key={i} aria-hidden>
                      {chunk}
                    </span>
                  ))}
                </div>
                {whereOpen && (
                  <div className="an-kc">
                    <span>{t("win.node.keyWhere")}</span>
                    <CodeBox code={keygenCommand(algorithm)} />
                  </div>
                )}
                <div className="an-kf an-kf2">
                  <Check label={t("node.ssh.confirmKey")} checked={confirmedKey} onCheckedChange={(v) => setConfirmedKey(v)} />
                  <button type="button" className="an-lk" aria-expanded={whereOpen} onClick={() => setWhereOpen((v) => !v)}>
                    {whereOpen ? t("win.node.hide") : t("win.node.keyWhereLink")}
                  </button>
                </div>
              </section>

              <TextField
                className="an-f an-lg"
                label={t("node.ssh.login")}
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="root"
                mono
                autoComplete="off"
                spellCheck={false}
                maxLength={32}
                pattern={usernamePattern.source}
                error={usernameError}
              />

              <div className="an-f flex min-w-0 flex-col gap-1.5">
                <span className="text-[13px] text-sec">{t("node.ssh.method")}</span>
                <WayCards
                  label={t("node.ssh.method")}
                  value={login.method}
                  onValueChange={(m: LoginMethod) => {
                    login.pick(m);
                    setFailure("");
                  }}
                  options={[
                    { value: "key", label: t("panelKey.title"), hint: t("win.node.wayKey"), icon: <Glyph d={glyphs.key} /> },
                    { value: "password", label: t("node.ssh.methodPassword"), hint: t("win.node.wayPassword"), icon: <Glyph d={glyphs.password} /> },
                    { value: "own", label: t("node.ssh.methodOwn"), hint: t("win.node.wayOwn"), icon: <Glyph d={glyphs.terminal} /> },
                  ]}
                />
              </div>

              {viaKey && <PanelKeyBox howTo={howTo ?? !keyInUse} onHowTo={setHowTo} />}
              {login.method === "password" && (
                <div className="an-box">
                  <TextField
                    className="an-f"
                    label={t("node.ssh.password")}
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    type="password"
                    placeholder={t("win.node.passwordPlaceholder")}
                    mono
                    autoComplete="new-password"
                    maxLength={1024}
                  />
                  <CheckNote label={t("node.ssh.installPanelKey")} hint={t("node.ssh.installPanelKeyHint")} checked={login.installPanelKey} onCheckedChange={(v) => login.setInstallPanelKey(v)} />
                  <p className="an-nt">
                    <Glyph d={glyphs.lock} />
                    {t("win.node.passwordNote")}
                  </p>
                </div>
              )}
              {login.method === "own" && (
                <div className="an-box">
                  <OwnKeyField login={login} plain />
                </div>
              )}
            </div>
            <PanelFoot>
              <Button type="button" variant="secondary" size="lg" onClick={() => back(0)}>
                {t("node.ssh.back")}
              </Button>
              <FootNote end>{confirmedKey ? "" : t("win.node.authHint")}</FootNote>
              <Button type="submit" variant="primary" size="lg" disabled={busy !== null || !confirmedKey || !login.ready}>
                {t("node.ssh.continue")}
              </Button>
            </PanelFoot>
          </Panel>
        )}

        {view === "chk" && (
          <Panel key="chk" onSubmit={(e) => void startInstall(e)}>
            <PanelHead eyebrow={t("auth.step", { n: 3, total: 4 })} title={checked ? t(remarks > 0 ? "win.node.chkWarnTitle" : "win.node.chkOk") : t("win.node.chkBusy")}>
              {checked ? t("win.node.chkOkBody") : t("win.node.chkBusyBody")}
            </PanelHead>
            <div className="an-b">
              {failure && <FailureNotice failure={failure} />}
              {dnsMismatch && (
                <Button type="button" variant="outline" size="md" className="self-start" disabled={busy !== null} onClick={() => void startInstall(undefined, true)}>
                  {t("set.domains.subHost.continueAnyway")}
                </Button>
              )}
              {checked && (
                <button type="button" className={cx("an-sum", remarks > 0 && "warn", rowsOpen && "open")} aria-expanded={rowsOpen} onClick={() => setChecksOpen(!rowsOpen)}>
                  <span className="an-ck" aria-hidden>
                    <Glyph d={remarks > 0 ? warnMark : glyphs.check} strokeWidth={2.5} />
                  </span>
                  <b>{t.n(remarks > 0 ? "win.node.chkSumWarn" : "win.node.chkSum", passed)}</b>
                  <span className="an-mono">{factSummary(preflight, fmt)}</span>
                  <em>{rowsOpen ? t("win.node.hide") : t("win.node.chkShow")}</em>
                  <Glyph className="an-cv" d={glyphs.chevron} />
                </button>
              )}
              {rowsOpen && (
                <div className="an-fx" aria-busy={!checked}>
                  {factList.map((f, i) => (
                    <div key={f.key} className={cx("an-fr", i < answered ? "done" : i === answered ? "now" : "wait", f.bad && i < answered && "bad", f.warn && i < answered && "warn")}>
                      <span className="an-ck" aria-hidden>
                        {f.warn && i < answered ? <Glyph d={warnMark} strokeWidth={2.5} /> : <Icon name="check" />}
                      </span>
                      <span>{f.label}</span>
                      <b>{i < answered ? f.value : "—"}</b>
                      {f.note && i < answered && <small>{f.note}</small>}
                    </div>
                  ))}
                </div>
              )}
              {checked && preflight && (
                <>
                  <div className="an-lb">{t("win.node.planTitle")}</div>
                  <div className="an-pl">
                    {!preflight.nftables && (
                      <div>
                        <span className="an-pi" aria-hidden>
                          <Glyph d={glyphs.package} />
                        </span>
                        <div>
                          <b>{t("win.node.planNftables")}</b>
                          <span>{t("win.node.planNftablesBody")}</span>
                        </div>
                      </div>
                    )}
                    <div>
                      <span className="an-pi" aria-hidden>
                        <Glyph d={glyphs.firewall} />
                      </span>
                      <div>
                        <b>{t("win.node.planFirewall", { ports: firewallPorts(portNumber).join(", ") })}</b>
                        <span>{t("win.node.planFirewallBody")}</span>
                      </div>
                    </div>
                    <div>
                      <span className="an-pi" aria-hidden>
                        <Glyph d={glyphs.agent} />
                      </span>
                      <div>
                        <b>{t("win.node.planAgent")}</b>
                        <span className="an-mono">mistgate-node.service</span>
                      </div>
                    </div>
                  </div>
                  <p className="an-fwq">
                    <button type="button" className="an-lk" aria-expanded={firewallOpen} onClick={() => setFirewallOpen((v) => !v)}>
                      {firewallOpen ? t("win.node.fwLess") : t("win.node.fwMore")}
                    </button>
                  </p>
                  {firewallOpen && <p className="an-fwt">{t("node.ssh.firewallHint")}</p>}
                  <SharePick share={share} onChange={(c) => onDraft({ share: c })} />
                  {login.method === "password" && (
                    <TextField
                      className="an-f"
                      label={t("node.ssh.password")}
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                      type="password"
                      placeholder={t("win.node.passwordPlaceholder")}
                      mono
                      autoComplete="new-password"
                      maxLength={1024}
                      hint={t("node.ssh.passwordHint")}
                    />
                  )}
                  {login.method === "own" && <OwnKeyField login={login} warn={false} plain />}
                </>
              )}
            </div>
            {checked && preflight && (
              <div className="an-ag">
                <CheckNote label={t("win.node.agreeTitle")} hint={t("win.node.agreeBody")} checked={confirmedInstall} onCheckedChange={(v) => setConfirmedInstall(v)} />
              </div>
            )}
            <PanelFoot>
              <Button type="button" variant="secondary" size="lg" onClick={() => back(1)} disabled={busy !== null}>
                {t("node.ssh.back")}
              </Button>
              <FootNote end>{checked && !confirmedInstall ? t("win.node.agreeHint") : ""}</FootNote>
              <Button type="submit" variant="primary" size="lg" disabled={busy !== null || !checked || !confirmedInstall || !login.ready || !share.valid}>
                {busy === "start" && <Spinner />}
                {busy === "start" ? t("node.ssh.starting") : t("node.ssh.startInstall")}
              </Button>
            </PanelFoot>
          </Panel>
        )}

        {view === "run" && (
          <Panel key="run">
            <PanelHead
              eyebrow={jobStopped ? `${t("auth.step", { n: 4, total: 4 })} · ${t("win.node.stopped")}` : t("auth.step", { n: 4, total: 4 })}
              title={jobStopped ? t("win.node.runStopped") : t("win.node.runTitle", { name: name.trim() || (job.data?.name ?? "") })}
            >
              {jobStopped ? jobText(job.data?.errorCode ?? "") : t("win.node.runBody")}
            </PanelHead>
            <div className="an-b">
              {job.error && <Notice tone="danger">{sshError(job.error, t, edge)}</Notice>}
              <div
                role="progressbar"
                aria-label={phaseLabel(job.data?.phase ?? "queued", t)}
                aria-valuemin={0}
                aria-valuemax={stageIds.length}
                aria-valuenow={Math.min(at, stageIds.length)}
                className={cx("an-bar", jobStopped && "err")}
              >
                <i style={{ ["--w" as string]: `${jobStopped ? Math.max(4, Math.round(((at + 0.5) / stageIds.length) * 100)) : Math.round(((at + 0.5) / stageIds.length) * 100)}%` }} />
              </div>
              <ol className="an-sg" aria-live="polite">
                {stageIds.map((id, i) => {
                  const err = jobStopped && i === at;
                  const done = !err && i < at;
                  const now = !err && running && i === at;
                  const code = job.data?.errorCode ?? "";
                  return (
                    <li key={id} className={err ? "err" : done ? "done" : now ? "now" : ""}>
                      <span className="an-si" aria-hidden>
                        <Glyph d={err ? glyphs.cross : done ? glyphs.check : glyphs.dot} size={14} strokeWidth={2.5} />
                      </span>
                      <b>{phaseLabel(id, t)}</b>
                      <span>{err ? (code === "ssh_authentication_failed" || code === "ssh_panel_key_not_accepted" ? t("win.node.loginRefused") : t("win.node.failed")) : now ? (id === "waiting_node" ? t("win.node.upTo2min") : t("win.node.inProgress")) : ""}</span>
                    </li>
                  );
                })}
              </ol>
              {failure && <FailureNotice failure={failure} />}
              {jobCanRetry && login.method === "own" && <OwnKeyField login={login} warn={false} note={t("node.ssh.retryOwnKey")} plain />}
              {jobCanRetry && login.method === "password" && (
                <TextField
                  className="an-f"
                  label={t("node.ssh.retryPassword")}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  type="password"
                  mono
                  autoComplete="new-password"
                  maxLength={1024}
                />
              )}
              {job.data?.errorCode === "ssh_target_refused" && jobStopped && (
                <div>
                  <Button type="button" variant="secondary" size="md" onClick={() => onMode("cmd")}>
                    {t("node.ssh.useManual")}
                  </Button>
                </div>
              )}
            </div>
            <PanelFoot>
              {running ? (
                <>
                  <a href={installManagerHref} className={buttonClass("secondary", "lg")}>
                    {t("node.ssh.manager")}
                  </a>
                  <FootNote end />
                  <Button type="button" variant="secondary" size="lg" onClick={onClose}>
                    {t("node.ssh.closeBackground")}
                  </Button>
                </>
              ) : (
                <>
                  <Button type="button" variant="secondary" size="lg" onClick={onClose}>
                    {t("common.close")}
                  </Button>
                  <FootNote end>{beforeChanges.has(job.data?.phase === "failed" ? (stageIds[at] ?? "") : (job.data?.phase ?? "")) && jobStopped ? t("win.node.serverUntouched") : ""}</FootNote>
                  {jobCanRetry && (
                    <Button type="button" variant="primary" size="lg" disabled={busy !== null || !login.ready} onClick={() => void retryInstall()}>
                      {busy === "retry" ? t("node.ssh.retrying") : t("node.ssh.retry")}
                    </Button>
                  )}
                </>
              )}
            </PanelFoot>
          </Panel>
        )}

        {view === "done" && (
          <NodeReady
            nodeId={nodeId ?? ""}
            line={[job.data?.name ?? name, location.trim() || (country !== noCountry ? fmt.country(country) : ""), host.trim()].filter(Boolean).join(" · ")}
            onClose={onClose}
            onOpen={openNode}
          />
        )}
      </div>
    </>
  );
}

/** The refusal of a step, in the window, above what it was about. */
function FailureNotice({ failure, onManual }: { failure: string; onManual?: () => void }) {
  const t = useT();
  return (
    <>
      <Notice tone="danger">{failure}</Notice>
      {onManual && (
        <div>
          <Button type="button" variant="secondary" size="md" onClick={onManual}>
            {t("node.ssh.useManual")}
          </Button>
        </div>
      )}
    </>
  );
}

/** The line of the folded check, in mono: the system, the architecture and the memory. */
function factSummary(p: NodePreflight | null, fmt: ReturnType<typeof useFmt>): string {
  if (!p) return "";
  return [[p.distribution, p.version].filter(Boolean).join(" "), p.architecture, fmt.bytes(Number(p.memoryBytes), 1024)].filter(Boolean).join(" · ");
}

/**
 * The rows of the server check, in the board's order (nftables after systemd); systemd and the panel's way back are the ones that can say "no".
 * A server without nft is a remark, not a refusal (the install adds the package): its row goes first and says so under itself.
 * Without the answer the values are dashes.
 */
function factRows(p: NodePreflight | null, t: T, fmt: ReturnType<typeof useFmt>, subHost: boolean): Fact[] {
  const no = t("node.ssh.unavailable");
  const yes = t("node.ssh.available");
  const rows: Fact[] = [
    { key: "system", label: t("node.ssh.system"), value: (p && [p.distribution, p.version].filter(Boolean).join(" ")) || "—" },
    { key: "arch", label: t("node.ssh.architecture"), value: p?.architecture || "—" },
    { key: "kernel", label: t("node.ssh.kernel"), value: p?.kernel || "—" },
    { key: "cpus", label: t("node.ssh.cpus"), value: p ? fmt.num(p.cpuCount, 0) : "—" },
    { key: "memory", label: t("node.ssh.memory"), value: p ? fmt.bytes(Number(p.memoryBytes), 1024) : "—" },
    { key: "disk", label: t("node.ssh.disk"), value: p ? fmt.bytes(Number(p.diskAvailableBytes), 1024) : "—" },
    { key: "systemd", label: t("node.ssh.systemd"), value: p ? (p.systemd ? yes : no) : "—", bad: !!p && !p.systemd },
    { key: "nftables", label: t("node.ssh.nftables"), value: p ? (p.nftables ? yes : t("node.ssh.nftablesWillInstall")) : "—", warn: !!p && !p.nftables, note: p && !p.nftables ? t("win.node.chkNftNote") : undefined },
    // The first host cannot reach a panel behind NAT: the panel dials it after the install.
    { key: "panel", label: t("node.ssh.panelReachable"), value: p ? (p.panelReachable ? yes : no) : "—", bad: !!p && !p.panelReachable && !subHost },
  ];
  const lack = rows.findIndex((r) => r.warn);
  return lack > 0 ? [rows[lack]!, ...rows.filter((_, i) => i !== lack)] : rows;
}

/** The panel's own key as a way in (.an-box): the key to copy, how to put it on a server, the fingerprint of the key. */
function PanelKeyBox({ howTo, onHowTo }: { howTo: boolean; onHowTo: (open: boolean) => void }) {
  const t = useT();
  const key = usePanelSSHKey();
  const publicKey = key.data?.publicKey ?? "";
  const command = `mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo '${publicKey}' >> ${authorizedKeysFile} && chmod 600 ${authorizedKeysFile}`;
  if (key.isError) {
    return (
      <div className="an-box">
        <p role="alert" className="text-danger-text!">
          {errorText(key.error, t)}
        </p>
        <div>
          <Button type="button" variant="secondary" size="md" onClick={() => void key.refetch()}>
            {t("common.retry")}
          </Button>
        </div>
      </div>
    );
  }
  return (
    <div className="an-box">
      {key.isPending ? (
        <div className="an-code">
          <code>{t("common.loading")}</code>
        </div>
      ) : (
        <CodeBox code={publicKey} label={t("panelKey.copy")} testId="panel-key" />
      )}
      <p>
        {t("panelKey.body")}{" "}
        <button type="button" className="an-lk" aria-expanded={howTo} onClick={() => onHowTo(!howTo)}>
          {howTo ? t("win.node.hideHow") : t("panelKey.howTo")}
        </button>
      </p>
      {key.data?.fingerprint && (
        <p>
          {t("panelKey.fingerprint")} <Fingerprint value={key.data.fingerprint} />
        </p>
      )}
      {howTo && (
        <>
          <ol className="an-ol">
            {[t("panelKey.step1"), t("panelKey.step2", { name: "mistgate" }), t("panelKey.step3")].map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ol>
          {publicKey && (
            <>
              <p>{t("panelKey.command")}</p>
              <CodeBox wrap code={command} label={t("node.add.copyCommand")} />
            </>
          )}
        </>
      )}
    </div>
  );
}

/** The window's last page (.an-dn): the node is installed and connected; the next thing is a profile on it. */
export function NodeReady({ nodeId, line, onClose, onOpen }: { nodeId: string; line: string; onClose: () => void; onOpen: (tab?: "profiles") => void }) {
  const t = useT();
  // a node installed again may have its profiles already: the next step is a profile only when it has none
  const live = useQuery({
    queryKey: ["node", nodeId],
    queryFn: async ({ signal }) => plain(await nodesApi.getNode({ nodeId }, { signal })),
    enabled: !!nodeId,
    staleTime: 30_000,
  });
  const needsProfile = (live.data?.inbounds.length ?? 0) === 0;
  return (
    <Panel key="done" className="an-dn">
      <WaveField at="50% 38%" />
      <Halo className="top-1/2 left-1/2 h-[420px] w-[640px] animate-[an-halo_1s_var(--e)_both]" />
      <div className="an-dn-in">
        <div className="an-ok" aria-hidden>
          <Icon name="check" />
        </div>
        <h2>{t("node.ssh.done")}</h2>
        <p className="an-line an-mono">{line}</p>
        {needsProfile && (
          <div className="an-nx">
            <span className="an-pi" aria-hidden>
              <Glyph d={glyphs.warn} />
            </span>
            <div>
              <b>{t("win.node.noProfiles")}</b>
              <span>{t("win.node.noProfilesBody")}</span>
            </div>
          </div>
        )}
        <div className="an-da">
          {needsProfile && (
            <Button type="button" variant="primary" size="lg" onClick={() => onOpen("profiles")}>
              <Glyph d={glyphs.plus} />
              {t("win.node.addProfile")}
            </Button>
          )}
          <Button type="button" variant={needsProfile ? "secondary" : "primary"} size="lg" onClick={() => (nodeId ? onOpen() : onClose())}>
            {t("node.ssh.openNode")}
          </Button>
        </div>
      </div>
    </Panel>
  );
}
