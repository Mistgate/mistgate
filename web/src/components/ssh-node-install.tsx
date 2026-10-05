import { ConnectError } from "@connectrpc/connect";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useMemo, useState, type FormEvent } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { Banner, Notice } from "@/components/ui/notice";
import { Select } from "@/components/ui/select";
import { TextField } from "@/components/ui/text-field";
import type { NodePreflight } from "@/gen/mistgate/admin/v1/provisioning_pb";
import { useT, type T } from "@/i18n";
import { provisioning } from "@/lib/api";
import { countryCodes } from "@/lib/countries";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";

type Props = { onBack: () => void; onClose: () => void };
type Step = 0 | 1 | 2 | 3;
type Busy = "fingerprint" | "check" | "start" | "retry" | null;

const nodeNamePattern = /^[a-z0-9-]{2,24}$/;
const nodeAddressPattern = /^[A-Za-z0-9.:-]{1,253}$/;
const noCountry = "none";
// node_provision_job.state (migration 00031): queued, running, cancel_requested, then completed, failed or cancelled.
const terminalStates = new Set(["completed", "failed", "cancelled"]);

function sshError(error: unknown, t: T): string {
  const raw = ConnectError.from(error).rawMessage.toLowerCase();
  if (raw === "ssh_fingerprint_timeout" || raw === "ssh_connection_timeout" || raw === "ssh_preflight_timeout") {
    return t("node.ssh.error.timeout");
  }
  if (raw === "ssh_fingerprint_unavailable" || raw === "ssh_connection_refused" || raw === "ssh_connection_unavailable") {
    return t("node.ssh.error.unavailable");
  }
  if (raw === "ssh_target_not_public" || raw === "ssh_target_invalid" || raw === "invalid ssh target") {
    return t("node.ssh.error.notPublic");
  }
  if (raw === "ssh_authentication_failed") return t("node.ssh.error.authentication");
  if (raw === "ssh_host_key_changed") return t("node.ssh.error.hostKey");
  if (raw === "unsupported_os" || raw === "unsupported_os_version") return t("node.ssh.error.unsupportedOS");
  if (raw === "unsupported_architecture") return t("node.ssh.error.unsupportedArch");
  if (raw.includes("sudo")) return t("node.ssh.error.sudo");
  if (raw === "panel_address_not_configured") return t("node.ssh.error.panelAddress");
  if (raw === "ssh_preflight_failed") return t("node.ssh.error.preflight");
  if (raw === "name_taken" || raw === "node_name_taken") return t("node.ssh.nameTaken");
  if (raw === "node_retired") return t("node.ssh.error.retired");
  return errorText(error, t);
}

function jobError(code: string, t: T): string {
  if (code === "ssh_authentication_failed") return t("node.ssh.error.authentication");
  if (code === "ssh_connection_timeout" || code === "ssh_preflight_timeout") return t("node.ssh.error.timeout");
  if (code === "ssh_connection_refused" || code === "ssh_connection_unavailable") return t("node.ssh.error.unavailable");
  if (code === "ssh_host_key_changed") return t("node.ssh.error.hostKey");
  if (code === "ssh_target_invalid" || code === "ssh_target_not_public") return t("node.ssh.error.notPublic");
  if (code === "unsupported_os" || code === "unsupported_os_version") return t("node.ssh.error.unsupportedOS");
  if (code === "unsupported_architecture") return t("node.ssh.error.unsupportedArch");
  if (code === "panel_unreachable") return t("node.ssh.error.panelAddress");
  if (code === "node_name_taken" || code === "name_taken") return t("node.ssh.nameTaken");
  if (code === "remote_outcome_unknown") return t("node.ssh.error.remoteOutcome");
  if (code === "host_firewall_configuration_failed") return t("node.ssh.error.firewall");
  if (code === "node_retired") return t("node.ssh.error.retired");
  if (code === "node_not_connected") return t("node.ssh.error.notConnected");
  if (code === "systemd_install_failed" || code === "agent_install_failed") return t("node.ssh.error.systemd");
  return t("node.ssh.error.install");
}

// The worker's phases (provision/worker.go setPhase); an unknown one reads as "Installing", never as a raw code.
function phaseLabel(phase: string, t: T): string {
  switch (phase) {
    case "queued":
      return t("node.ssh.phase.queued");
    case "connecting":
      return t("node.ssh.phase.connecting");
    case "preflight":
      return t("node.ssh.phase.preflight");
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

function stateLabel(state: string, t: T): string {
  switch (state) {
    case "completed":
      return t("node.ssh.done");
    case "failed":
      return t("node.ssh.state.failed");
    case "cancelled":
      return t("node.ssh.state.cancelled");
    case "cancel_requested":
      return t("node.ssh.state.cancelling");
    case "queued":
      return t("node.ssh.state.queued");
    default:
      return t("node.ssh.state.running");
  }
}

function suggestNodeName(host: string) {
  const first = host.trim().toLowerCase().split(".")[0] ?? "";
  const label = first.replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 24);
  return label.length >= 2 ? label : "node-1";
}

function Steps({ current, labels, ariaLabel }: { current: Step; labels: string[]; ariaLabel: string }) {
  return (
    <>
      <ol className="hidden grid-cols-4 gap-2 md:grid" aria-label={ariaLabel}>
        {labels.map((label, index) => {
          const done = index < current;
          const active = index === current;
          return (
            <li key={label} aria-current={active ? "step" : undefined} className="flex min-w-0 flex-col gap-2">
              <span className={done || active ? "h-1 rounded-full bg-accent" : "h-1 rounded-full bg-surface-2"} />
              <span className={active ? "truncate text-xs font-bold text-fg" : done ? "truncate text-xs font-semibold text-accent-text" : "truncate text-xs font-semibold text-muted"}>
                <span className="mr-1.5 font-mono">{index + 1}</span>{label}
              </span>
            </li>
          );
        })}
      </ol>
      <div className="flex flex-col gap-2 md:hidden">
        <div
          role="progressbar"
          aria-label={labels[current]}
          aria-valuemin={1}
          aria-valuemax={labels.length}
          aria-valuenow={current + 1}
          className="h-1 overflow-hidden rounded-full bg-surface-2"
        >
          <span className="block h-full rounded-full bg-accent transition-[width] duration-300" style={{ width: ((current + 1) / labels.length * 100) + "%" }} />
        </div>
        <span aria-live="polite" className="text-xs font-bold text-muted">{current + 1} / {labels.length} · {labels[current]}</span>
      </div>
    </>
  );
}

function Fact({ label, value, tone }: { label: string; value: string; tone?: "good" | "bad" }) {
  return (
    <div className="min-w-0 rounded-field border border-line bg-surface-2 p-3">
      <span className="block text-[11px] font-bold tracking-[0.08em] text-muted uppercase">{label}</span>
      <b className={tone === "good" ? "tone-ok tone-text mt-1 block break-words text-sm" : tone === "bad" ? "tone-bad tone-text mt-1 block break-words text-sm" : "mt-1 block break-words text-sm text-fg"}>
        {value}
      </b>
    </div>
  );
}

export function SSHNodeInstall({ onBack, onClose }: Props) {
  const t = useT();
  const fmt = useFmt();
  const guard = useStepUp();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [step, setStep] = useState<Step>(0);
  const [busy, setBusy] = useState<Busy>(null);
  const [failure, setFailure] = useState("");
  const [host, setHost] = useState("");
  const [port, setPort] = useState("22");
  const [fingerprint, setFingerprint] = useState("");
  const [algorithm, setAlgorithm] = useState("");
  const [confirmedKey, setConfirmedKey] = useState(false);
  const [name, setName] = useState("");
  const [address, setAddress] = useState("");
  const [country, setCountry] = useState(noCountry);
  const [location, setLocation] = useState("");
  const [provider, setProvider] = useState("");
  const [username, setUsername] = useState("root");
  const [password, setPassword] = useState("");
  const [touched, setTouched] = useState(false);
  const [confirmedInstall, setConfirmedInstall] = useState(false);
  const [preflight, setPreflight] = useState<NodePreflight | null>(null);
  const [jobId, setJobId] = useState("");
  const usernamePattern = /^[A-Za-z_][A-Za-z0-9_.-]{0,31}$/;

  const countryOptions = useMemo(
    () => [
      { value: noCountry, label: t("node.add.countryNone") },
      ...countryCodes
        .map((code) => ({ value: code, label: code + " · " + fmt.country(code) }))
        .sort((a, b) => fmt.country(a.value).localeCompare(fmt.country(b.value), fmt.lang)),
    ],
    [fmt, t],
  );

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

  const labels = [t("node.ssh.step.server"), t("node.ssh.step.key"), t("node.ssh.step.check"), t("node.ssh.step.install")];
  const nameError = touched && !nodeNamePattern.test(name.trim()) ? t("node.add.nameError") : "";
  const addressError = touched && !nodeAddressPattern.test(address.trim()) ? t("node.add.addressError") : "";
  const usernameError = touched && !usernamePattern.test(username.trim()) ? t("node.ssh.loginError") : "";
  const portNumber = Number(port);
  const portValid = Number.isInteger(portNumber) && portNumber >= 1 && portNumber <= 65535;
  const jobState = job.data?.state ?? "";
  // the install manager speaks the admin's language: it cannot read the SPA's choice by itself
  const installManagerHref = new URL(`nodes/install?lang=${fmt.lang}&job=${encodeURIComponent(jobId)}`, document.baseURI).href;
  const jobDone = jobState === "completed";
  const jobFailed = jobState === "failed";
  const jobCancelled = jobState === "cancelled";
  // a cancelled job resumes safely (preflight checks the host's enrollment first); the panel refuses a retired node
  const jobCanRetry = (jobFailed || jobCancelled) && job.data?.errorCode !== "node_retired";
  const jobTone = jobDone ? "tone-ok tint tone-text" : jobFailed ? "tone-bad tint tone-text" : jobCancelled ? "tone-off tint tone-text" : "tone-busy tint tone-text";

  useEffect(() => {
    if (!jobDone && !jobFailed) return;
    void qc.invalidateQueries({ queryKey: ["nodes"] });
    void qc.invalidateQueries({ queryKey: ["overview"] });
  }, [jobDone, jobFailed, qc]);

  async function getFingerprint(event: FormEvent) {
    event.preventDefault();
    setFailure("");
    if (!host.trim() || !portValid) {
      setFailure(t("node.ssh.error.notPublic"));
      return;
    }
    setBusy("fingerprint");
    try {
      const response = await provisioning.getSSHFingerprint({ host: host.trim(), port: portNumber });
      setFingerprint(response.fingerprint);
      setAlgorithm(response.algorithm);
      setConfirmedKey(false);
      setName(suggestNodeName(host));
      setAddress(host.trim());
      setStep(1);
    } catch (error) {
      setFailure(sshError(error, t));
    } finally {
      setBusy(null);
    }
  }

  async function checkServer(event: FormEvent) {
    event.preventDefault();
    setTouched(true);
    setFailure("");
    if (!confirmedKey || !nodeNamePattern.test(name.trim()) || !nodeAddressPattern.test(address.trim()) || !usernamePattern.test(username.trim()) || !password) {
      if (!confirmedKey) setFailure(t("node.ssh.confirmKey"));
      return;
    }
    setBusy("check");
    try {
      const response = await guard(() => provisioning.checkSSH({
        host: host.trim(),
        port: portNumber,
        fingerprint,
        password,
        username: username.trim(),
      }));
      if (!response.preflight) throw new Error("preflight_missing");
      setPreflight(response.preflight);
      setPassword("");
      setConfirmedInstall(false);
      setStep(2);
    } catch (error) {
      if (!isStepUpCancelled(error)) setFailure(sshError(error, t));
    } finally {
      setPassword("");
      setBusy(null);
    }
  }

  async function startInstall(event: FormEvent) {
    event.preventDefault();
    setFailure("");
    if (!confirmedInstall || !password) {
      setFailure(t("node.ssh.confirmInstall"));
      return;
    }
    setBusy("start");
    try {
      const response = await guard(() => provisioning.startNodeProvision({
        confirmInstall: true,
        name: name.trim().toLowerCase(),
        address: address.trim(),
        countryCode: country === noCountry ? "" : country,
        location: location.trim(),
        provider: provider.trim(),
        sshHost: host.trim(),
        sshPort: portNumber,
        fingerprint,
        password,
        sshUsername: username.trim(),
      }));
      if (!response.job?.id) throw new Error("provision_job_missing");
      setJobId(response.job.id);
      qc.setQueryData(["node-provision", response.job.id], response.job);
      setStep(3);
    } catch (error) {
      if (!isStepUpCancelled(error)) setFailure(sshError(error, t));
    } finally {
      setPassword("");
      setBusy(null);
    }
  }

  async function retryInstall() {
    if (!jobId || !password) {
      setFailure(t("node.ssh.password"));
      return;
    }
    setFailure("");
    setBusy("retry");
    try {
      const response = await guard(() => provisioning.retryNodeProvision({
        jobId,
        confirmInstall: true,
        password,
        sshUsername: username.trim(),
      }));
      if (response.job) qc.setQueryData(["node-provision", jobId], response.job);
      await qc.invalidateQueries({ queryKey: ["node-provision", jobId] });
    } catch (error) {
      if (!isStepUpCancelled(error)) setFailure(sshError(error, t));
    } finally {
      setPassword("");
      setBusy(null);
    }
  }

  const back = () => {
    setFailure("");
    if (step === 0) onBack();
    else if (step === 1) setStep(0);
    else if (step === 2) {
      setPreflight(null);
      setPassword("");
      setStep(1);
    }
  };

  const openNode = () => {
    const nodeId = job.data?.nodeId;
    if (!nodeId) return;
    onClose();
    void navigate({ to: "/nodes/$id", params: { id: nodeId } });
  };

  return (
    <div className="flex flex-col gap-5">
      <Steps current={step} labels={labels} ariaLabel={t("node.ssh.steps")} />
      {failure && <Notice tone="danger">{failure}</Notice>}

      {step === 0 && (
        <form onSubmit={(event) => void getFingerprint(event)} className="flex flex-col gap-4">
          <div className="flex flex-col gap-1">
            <h3 className="text-base font-extrabold tracking-[-0.02em]">{t("node.ssh.step.server")}</h3>
            <p className="text-sm leading-relaxed text-muted">{t("node.ssh.hostHint")}</p>
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-[minmax(0,1fr)_132px]">
            <TextField
              label={t("node.ssh.host")}
              value={host}
              onChange={(event) => setHost(event.target.value)}
              placeholder="de1.example.com"
              mono
              hint={t("node.ssh.hostHint")}
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              required
              autoFocus
            />
            <TextField
              label={t("node.ssh.port")}
              value={port}
              onChange={(event) => setPort(event.target.value)}
              type="number"
              min={1}
              max={65535}
              step={1}
              inputMode="numeric"
              hint={t("node.ssh.portHint")}
              required
            />
            <p className="text-xs leading-relaxed text-muted sm:col-span-2">{t("node.ssh.providerFirewallHint")}</p>
          </div>
          <div className="flex flex-wrap justify-between gap-2 border-t border-line pt-4">
            <Button type="button" variant="ghost" size="md" onClick={onBack}>{t("common.cancel")}</Button>
            <Button type="submit" variant="primary" size="md" disabled={busy !== null || !host.trim() || !portValid}>
              {busy === "fingerprint" ? t("node.ssh.checkingFingerprint") : t("node.ssh.checkServer")}
            </Button>
          </div>
        </form>
      )}

      {step === 1 && (
        <form onSubmit={(event) => void checkServer(event)} className="flex flex-col gap-4">
          <div className="flex flex-col gap-1">
            <h3 className="text-base font-extrabold tracking-[-0.02em]">{t("node.ssh.fingerprintTitle")}</h3>
            <p className="text-sm leading-relaxed text-muted">{t("node.ssh.fingerprintBody")}</p>
          </div>
          <code className="block overflow-x-auto rounded-field border border-accent-line bg-accent-soft p-3 font-mono text-xs leading-relaxed text-accent-text select-all">{fingerprint}</code>
          {algorithm && <span className="-mt-2 text-xs text-muted">{t("node.ssh.keyType", { algorithm })}</span>}
          <label className="flex cursor-pointer items-start gap-2.5 rounded-field border border-line bg-surface-2 p-3 text-sm leading-relaxed text-fg">
            <input
              type="checkbox"
              checked={confirmedKey}
              onChange={(event) => setConfirmedKey(event.target.checked)}
              className="mt-0.5 size-4 flex-none accent-accent"
            />
            <span>{t("node.ssh.confirmKey")}</span>
          </label>

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <TextField
              label={t("node.add.name")}
              value={name}
              onChange={(event) => setName(event.target.value.toLowerCase())}
              placeholder="de1"
              mono
              maxLength={24}
              autoComplete="off"
              error={nameError}
              hint={t("node.ssh.nameHint")}
              required
            />
            <TextField
              label={t("node.add.address")}
              value={address}
              onChange={(event) => setAddress(event.target.value)}
              placeholder="de1.example.com"
              mono
              maxLength={253}
              autoComplete="off"
              error={addressError}
              hint={t("node.ssh.addressHint")}
              required
            />
            <div className="flex flex-col gap-1.5">
              <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("node.add.country")}</span>
              <Select value={country} onValueChange={setCountry} options={countryOptions} aria-label={t("node.add.country")} />
            </div>
            <TextField label={t("node.ssh.location")} value={location} onChange={(event) => setLocation(event.target.value)} maxLength={100} autoComplete="off" />
            <TextField label={t("node.ssh.provider")} value={provider} onChange={(event) => setProvider(event.target.value)} maxLength={100} autoComplete="off" />
          </div>

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <TextField
              label={t("node.ssh.login")}
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              placeholder="root"
              autoComplete="username"
              maxLength={32}
              pattern={usernamePattern.source}
              hint={t("node.ssh.loginHint")}
              error={usernameError}
              required
            />
            <TextField
              label={t("node.ssh.password")}
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              type="password"
              autoComplete="current-password"
              maxLength={1024}
              required
            />
          </div>
          <p className="text-xs leading-relaxed text-muted">{t("node.ssh.passwordHint")}</p>
          <div className="flex flex-wrap justify-between gap-2 border-t border-line pt-4">
            <Button type="button" variant="ghost" size="md" onClick={back}>{t("node.ssh.back")}</Button>
            <Button type="submit" variant="primary" size="md" disabled={busy !== null || !confirmedKey || !password}>
              {busy === "check" ? t("node.ssh.checking") : t("node.ssh.continue")}
            </Button>
          </div>
        </form>
      )}

      {step === 2 && preflight && (
        <form onSubmit={(event) => void startInstall(event)} className="flex flex-col gap-4">
          <div className="flex flex-col gap-1">
            <h3 className="text-base font-extrabold tracking-[-0.02em]">{t("node.ssh.preflightTitle")}</h3>
            <p className="text-sm leading-relaxed text-muted">{t("node.ssh.preflightBody")}</p>
          </div>
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
            <Fact label={t("node.ssh.system")} value={[preflight.distribution, preflight.version].filter(Boolean).join(" ") || "—"} />
            <Fact label={t("node.ssh.architecture")} value={preflight.architecture || "—"} />
            <Fact label={t("node.ssh.kernel")} value={preflight.kernel || "—"} />
            <Fact label={t("node.ssh.cpus")} value={fmt.num(preflight.cpuCount, 0)} />
            <Fact label={t("node.ssh.memory")} value={fmt.bytes(Number(preflight.memoryBytes), 1024)} />
            <Fact label={t("node.ssh.disk")} value={fmt.bytes(Number(preflight.diskAvailableBytes), 1024)} />
            <Fact label={t("node.ssh.systemd")} value={preflight.systemd ? t("node.ssh.available") : t("node.ssh.unavailable")} tone={preflight.systemd ? "good" : "bad"} />
            <Fact label={t("node.ssh.panelReachable")} value={preflight.panelReachable ? t("node.ssh.available") : t("node.ssh.unavailable")} tone={preflight.panelReachable ? "good" : "bad"} />
          </div>
          <p className="rounded-field border border-line bg-surface-2 p-3 text-sm leading-relaxed text-muted">{t("node.ssh.firewallHint")}</p>
          <TextField
            label={t("node.ssh.password")}
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            type="password"
            autoComplete="current-password"
            maxLength={1024}
            hint={t("node.ssh.passwordHint")}
            required
          />
          <label className="flex cursor-pointer items-start gap-2.5 rounded-field border border-line bg-surface-2 p-3 text-sm leading-relaxed text-fg">
            <input
              type="checkbox"
              checked={confirmedInstall}
              onChange={(event) => setConfirmedInstall(event.target.checked)}
              className="mt-0.5 size-4 flex-none accent-accent"
            />
            <span>{t("node.ssh.confirmInstall")}</span>
          </label>
          <div className="flex flex-wrap justify-between gap-2 border-t border-line pt-4">
            <Button type="button" variant="ghost" size="md" onClick={back}>{t("node.ssh.back")}</Button>
            <Button type="submit" variant="primary" size="md" disabled={busy !== null || !confirmedInstall || !password}>
              {busy === "start" ? t("node.ssh.starting") : t("node.ssh.startInstall")}
            </Button>
          </div>
        </form>
      )}

      {step === 3 && (
        <div className="flex flex-col gap-4">
          {job.isPending && <div className="rounded-card border border-line bg-surface-2 p-4 text-sm text-muted">{t("node.ssh.state.queued")}</div>}
          {job.error && <Notice tone="danger">{sshError(job.error, t)}</Notice>}
          {job.data && (
            <section className="flex flex-col gap-3 rounded-card border border-line bg-surface-2 p-4" aria-live="polite">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="min-w-0">
                  <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("node.add.name")}</span>
                  <h3 className="mt-0.5 break-words text-base font-extrabold">{job.data.name}</h3>
                </div>
                <span className={"inline-flex items-center rounded-full border px-3 py-1 text-xs font-bold " + jobTone}>
                  {stateLabel(jobState, t)}
                </span>
              </div>
              {jobDone ? (
                <Banner kind="ok" title={t("node.ssh.done")} />
              ) : jobFailed ? (
                <Notice tone="danger">{jobError(job.data.errorCode, t)}</Notice>
              ) : jobCancelled ? (
                <Notice tone="danger">{jobError(job.data.errorCode, t)}</Notice>
              ) : (
                <>
                  <p className="text-sm text-muted">{phaseLabel(job.data.phase, t)}</p>
                  <div role="progressbar" aria-label={phaseLabel(job.data.phase, t)} aria-valuemin={0} aria-valuemax={100} className="h-1.5 overflow-hidden rounded-full bg-canvas">
                    <span className="block h-full w-1/3 animate-pulse rounded-full bg-accent" />
                  </div>
                  <p className="text-xs leading-relaxed text-muted">{t("node.ssh.closeBackground")}</p>
                </>
              )}
              {jobCanRetry && (
                <TextField
                  label={t("node.ssh.retryPassword")}
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                  type="password"
                  autoComplete="current-password"
                  maxLength={1024}
                />
              )}
            </section>
          )}
          <div className="flex flex-wrap justify-end gap-2 border-t border-line pt-4">
            {jobDone && job.data?.nodeId ? (
              <Button type="button" variant="primary" size="md" onClick={openNode}>{t("node.ssh.openNode")}</Button>
            ) : jobCanRetry ? (
              <>
                <Button type="button" variant="ghost" size="md" onClick={onClose}>{t("common.close")}</Button>
                <Button type="button" variant="primary" size="md" disabled={busy !== null || !password} onClick={() => void retryInstall()}>
                  {busy === "retry" ? t("node.ssh.retrying") : t("node.ssh.retry")}
                </Button>
              </>
            ) : (
              <Button type="button" variant="secondary" size="md" onClick={onClose}>
                {jobFailed || jobCancelled ? t("common.close") : t("node.ssh.closeBackground")}
              </Button>
            )}
          </div>
          {jobId && <a href={installManagerHref} className="self-end text-xs font-semibold text-accent-text underline-offset-4 hover:underline">{t("node.ssh.manager")}</a>}
        </div>
      )}
    </div>
  );
}
