import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { AddNodeProvider, useAddNode } from "@/components/add-node";
import { StepUpProvider } from "@/components/step-up";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { ToastProvider } from "@/components/ui/toast";
import { ApprovalState, TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";
import { approvals as approvalsApi, provisioning } from "@/lib/api";
import type { Approvals } from "@/lib/integrations";
import { ApprovalQueue } from "@/screens/integrations/approvals";
import { SSHAccessCard } from "@/screens/node/ssh-access";

// Development only, /dev-kit?only=ssh: a node's SSH access card (Server tab) in each state, the SSH install wizard and a
// node_install approval, on made-up data. The provisioning calls are answered here: by node for the card (a change that
// goes through, one that never ends, one the server does not confirm, one refused at the login, an interrupted change, a
// retired node, nodes installed by the panel key), by login for the wizard (deploy: the server refuses the panel key).

const fingerprint = "SHA256:Vq3n0cX9Lr2bT8kM1pZs6YwE4hJ7uGdA5fNiQoRlKe0";
const panelKey = {
  publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7f2mQ9xW4bN1cR8vT3yL6pZ0sJ5hD2gF9aE7uK4oM mistgate-panel",
  fingerprint: "SHA256:3kQz7mVb1xN8cR2tY5pL0sW9hD4gF6aJ8uE1oK7iM3n",
};
const base = { port: 22, fingerprint, configuredUnix: Math.floor(Date.now() / 1000) - 86400 * 12, passwordSaved: true, passwordLogin: true, panelKeyFingerprint: "" };
const viaKey = { panelKeyFingerprint: panelKey.fingerprint };
const access: Record<string, Record<string, unknown>> = {
  nod_idle: { nodeName: "de1", host: "203.0.113.7", username: "root" },
  nod_generated: { nodeName: "nl1", host: "203.0.113.24", username: "deploy", passwordGenerated: true },
  nod_slow: { nodeName: "de1", host: "203.0.113.7", username: "root" },
  nod_unverified: { nodeName: "nl1", host: "203.0.113.24", username: "root" },
  nod_refused: { nodeName: "de1", host: "203.0.113.7", username: "deploy" },
  nod_pending: { nodeName: "nl1", host: "203.0.113.24", username: "root", rotationPending: true },
  nod_retired: { nodeName: "de1", host: "203.0.113.7", username: "root", nodeRetired: true, passwordGenerated: true },
  nod_key: { nodeName: "nl1", host: "203.0.113.24", username: "root", passwordGenerated: true, ...viaKey },
  nod_keyonly: { nodeName: "de1", host: "203.0.113.7", username: "root", passwordSaved: false, passwordLogin: false, ...viaKey },
  nod_closed: { nodeName: "nl1", host: "203.0.113.24", username: "root", passwordGenerated: true, passwordLogin: false, ...viaKey },
  nod_keyretired: { nodeName: "de1", host: "203.0.113.7", username: "root", nodeRetired: true, passwordSaved: false, passwordLogin: false, ...viaKey },
};

const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));
const preflight = { distribution: "Ubuntu", version: "24.04", kernel: "6.8.0-45-generic", architecture: "amd64", cpuCount: 2, memoryBytes: 2_147_483_648n, diskAvailableBytes: 21_474_836_480n, systemd: true, alreadyEnrolled: false, panelReachable: true };
const job = { id: "prv_7", nodeId: "nod_new", name: "nl1", sshHost: "203.0.113.24", sshPort: 22, createdUnix: 0n, updatedUnix: 0n };

function fakeCalls() {
  const api = provisioning as unknown as Record<string, unknown>;
  api.listNodeServerAccess = async () => ({ access: Object.entries(access).map(([nodeId, a]) => ({ nodeId, ...base, ...a })) });
  api.revealNodeServerPassword = async ({ nodeId }: { nodeId: string }) => {
    await wait(300);
    if (nodeId === "nod_pending") return { password: "kV7#pRw2-Lm9qT4x", pendingPassword: "N4QWZ7KXJ2M5RT3VBY6CDF8GHE", unverified: true };
    return { password: access[nodeId]?.passwordGenerated ? "N4QWZ7KXJ2M5RT3VBY6CDF8GHE" : "kV7#pRw2-Lm9qT4x" };
  };
  api.rotateNodeServerPassword = async ({ nodeId, generate }: { nodeId: string; generate?: boolean }) => {
    if (nodeId === "nod_slow") return new Promise(() => {});
    await wait(900);
    if (nodeId === "nod_unverified") {
      access[nodeId] = { ...access[nodeId], rotationPending: true };
      throw new ConnectError("ssh_password_change_unverified", Code.Unavailable);
    }
    if (nodeId === "nod_refused") throw new ConnectError("ssh_authentication_failed", Code.FailedPrecondition);
    access[nodeId] = { ...access[nodeId], passwordGenerated: !!generate, passwordSaved: true };
    return { rotated: true };
  };
  api.forgetNodeServerAccess = async () => ({});
  api.getPanelSSHKey = async () => {
    await wait(150);
    return panelKey;
  };
  api.getSSHFingerprint = async ({ host, port }: { host: string; port: number }) => {
    await wait(300);
    return { host, port, fingerprint, algorithm: "ssh-ed25519" };
  };
  api.checkSSH = async ({ username }: { username: string }) => {
    await wait(500);
    if (username === "deploy") throw new ConnectError("ssh_panel_key_not_accepted", Code.Unauthenticated);
    return { preflight };
  };
  api.startNodeProvision = async () => ({ job: { ...job, state: "queued", phase: "queued", errorCode: "" } });
  // the agent was installed but never called back: the retry needs no password with the panel key
  api.getNodeProvision = async () => ({ job: { ...job, state: "failed", phase: "waiting_node", errorCode: "node_not_connected" } });
  api.retryNodeProvision = async () => ({ job: { ...job, state: "queued", phase: "queued", errorCode: "" } });
  (approvalsApi as unknown as Record<string, unknown>).approve = async () => {
    await wait(600);
    return {};
  };
}
fakeCalls();

const now = Math.floor(Date.now() / 1000);
const installApproval: Approvals = {
  awaiting: 1,
  nowUnix: now,
  receivedMs: Date.now(),
  approvals: [
    {
      id: "pln_ssh",
      tokenId: "tok_1",
      tokenName: "claude-ops",
      tokenProfile: TokenProfile.ADMIN,
      tool: "node_install",
      facts: [
        { key: "node", value: "nl1", untrusted: true },
        { key: "ssh", value: "203.0.113.24:22", untrusted: true },
        { key: "username", value: "root", untrusted: true },
        { key: "host_key", value: fingerprint, untrusted: false },
        { key: "host_key_algorithm", value: "ssh-ed25519", untrusted: false },
        { key: "panel_ssh_key", value: panelKey.publicKey, untrusted: false },
        { key: "address", value: "nl1.example.com", untrusted: true },
        { key: "installation", value: "signed current agent bundle; waits for the new node to connect", untrusted: false },
      ],
      danger: ["step_up", "fleet"],
      reason: "a second node in the Netherlands for the evening load",
      state: ApprovalState.AWAITING,
      createdUnix: now - 60,
      expiresUnix: now + 540,
      decidedByName: "",
      decidedUnix: 0,
      appliedUnix: 0,
      result: "",
      error: "",
      outcomeCode: "",
      outcomeParams: {},
    },
  ],
} as unknown as Approvals;

function Shot({ id, title, wide, children }: { id: string; title: string; wide?: boolean; children: ReactNode }) {
  const [qc] = useState(() => new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false } } }));
  return (
    <section data-shot={id} className={`flex w-full flex-col gap-3 ${wide ? "max-w-[640px]" : "max-w-[452px]"}`}>
      <SectionLabel>{title}</SectionLabel>
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <div role="tabpanel">{children}</div>
        </ToastProvider>
      </QueryClientProvider>
    </section>
  );
}

function OpenWizard() {
  const addNode = useAddNode();
  return (
    <Button data-open-wizard variant="secondary" onClick={() => addNode()}>
      Open the add-node window
    </Button>
  );
}

export function SSHKit() {
  return (
    <div className="flex flex-wrap items-start gap-8">
      <Shot id="ssh-wizard" title="SSH install wizard (the real window)">
        <StepUpProvider>
          <AddNodeProvider>
            <OpenWizard />
          </AddNodeProvider>
        </StepUpProvider>
      </Shot>
      <Shot id="ssh-approval" title="node_install approval: the owner picks the login" wide>
        <StepUpProvider>
          <ApprovalQueue data={installApproval} />
        </StepUpProvider>
      </Shot>
      <Shot id="ssh-idle" title="SSH access: saved, verified">
        <SSHAccessCard nodeId="nod_idle" />
      </Shot>
      <Shot id="ssh-key" title="SSH access: the panel key and a saved password">
        <SSHAccessCard nodeId="nod_key" />
      </Shot>
      <Shot id="ssh-keyonly" title="SSH access: installed by the panel key, no password">
        <SSHAccessCard nodeId="nod_keyonly" />
      </Shot>
      <Shot id="ssh-closed" title="SSH access: password login closed on the server">
        <SSHAccessCard nodeId="nod_closed" />
      </Shot>
      <Shot id="ssh-keyretired" title="SSH access: a retired node installed by key">
        <SSHAccessCard nodeId="nod_keyretired" />
      </Shot>
      <Shot id="ssh-generated" title="SSH access: a password the panel generated">
        <SSHAccessCard nodeId="nod_generated" />
      </Shot>
      <Shot id="ssh-slow" title="SSH access: a change that is still running">
        <SSHAccessCard nodeId="nod_slow" />
      </Shot>
      <Shot id="ssh-unverified" title="SSH access: the server does not confirm the change">
        <SSHAccessCard nodeId="nod_unverified" />
      </Shot>
      <Shot id="ssh-refused" title="SSH access: the saved password no longer logs in">
        <SSHAccessCard nodeId="nod_refused" />
      </Shot>
      <Shot id="ssh-pending" title="SSH access: an interrupted change">
        <SSHAccessCard nodeId="nod_pending" />
      </Shot>
      <Shot id="ssh-retired" title="SSH access: a retired node">
        <SSHAccessCard nodeId="nod_retired" />
      </Shot>
    </div>
  );
}
