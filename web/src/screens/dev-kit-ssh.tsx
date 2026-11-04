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
import { PanelKeyOffer } from "@/screens/nodes-panel-key";

// Development only, /dev-kit?only=ssh: a node's SSH access card (Server tab) in each state, the SSH install wizard and a
// node_install approval, on made-up data. The provisioning calls are answered here: by node for the card (a change that
// goes through, one that never ends, one the server does not confirm, one refused at the login, an interrupted change, a
// retired node, nodes installed by the panel key), by login for the wizard (deploy: the server refuses the panel key; an
// own key always asks for its passphrase first). The keys block reads a made-up authorized_keys per node (the panel key
// missing, unused, a list that never loads, one the server refuses); a node with no access shows Connect SSH access (its
// password logs in, the panel key does not go on); the Nodes page offer on three nodes of its own (it runs in the bulk bar).

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
  nod_keymissing: { nodeName: "de1", host: "203.0.113.7", username: "root", passwordGenerated: true, ...viaKey },
  nod_keyunused: { nodeName: "fi1", host: "198.51.100.12", username: "root" },
  nod_keysloading: { nodeName: "nl1", host: "203.0.113.24", username: "root", ...viaKey },
  nod_keyserror: { nodeName: "fi1", host: "198.51.100.12", username: "root", ...viaKey },
};

// authorized_keys per node, as ListNodeServerKeys reads it
const sshKey = (type: string, bits: number, fp: string, comment: string, over: object = {}) => ({ type, bits, fingerprint: fp, comment, panelKey: false, restricted: false, lines: 1, ...over });
const k = {
  panel: sshKey("ssh-ed25519", 256, panelKey.fingerprint, "mistgate-panel", { panelKey: true }),
  alice: sshKey("ssh-ed25519", 256, "SHA256:Xy7pL2qR9sT4vW8zA1bC3dE5fG6hJ0kM2nP4rS6tU8w", "alice@laptop"),
  ci: sshKey("ssh-rsa", 3072, "SHA256:Ab9cD8eF7gH6iJ5kL4mN3oP2qR1sT0uV9wX8yZ7aB6c", "deploy@ci.example.com", { restricted: true, lines: 2 }),
  yubi: sshKey("sk-ssh-ed25519@openssh.com", 256, "SHA256:Qm4nB7vC1xZ9lK3jH5gF8dS2aP6oI0uY4tR7eW1qE3r", "bob@yubikey"),
  ecdsa: sshKey("ecdsa-sha2-nistp256", 256, "SHA256:Lk8jH2gF5dS9aP1oI4uY7tR3eW6qE0rT2yU5iO8pA1s", ""),
};
const keysOf: Record<string, { keys: ReturnType<typeof sshKey>[]; otherLines: number }> = {
  nod_key: { keys: [k.panel, k.alice, k.ci], otherLines: 2 },
  nod_idle: { keys: [k.alice, k.ci], otherLines: 0 },
  nod_generated: { keys: [k.ecdsa, k.yubi], otherLines: 1 },
  nod_keyonly: { keys: [k.panel], otherLines: 0 },
  nod_closed: { keys: [k.panel, k.alice], otherLines: 0 },
  nod_keymissing: { keys: [k.alice, k.yubi], otherLines: 0 },
  nod_keyunused: { keys: [k.panel, k.alice], otherLines: 0 },
};
// the Nodes page offer's own nodes: three log in with a password only, nl1's ~/.ssh is a link
const bulkAccess = [
  { nodeId: "nod_b_de1", nodeName: "de1", host: "203.0.113.7" },
  { nodeId: "nod_b_nl1", nodeName: "nl1", host: "203.0.113.24" },
  { nodeId: "nod_b_fi1", nodeName: "fi1", host: "198.51.100.12" },
].map((a) => ({ ...base, username: "root", ...a }));

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
  api.listNodeServerKeys = async ({ nodeId }: { nodeId: string }) => {
    if (nodeId === "nod_keysloading") return new Promise(() => {});
    await wait(700);
    if (nodeId === "nod_keyserror") throw new ConnectError("ssh_authentication_failed", Code.FailedPrecondition);
    return keysOf[nodeId] ?? { keys: [k.alice], otherLines: 0 };
  };
  api.addNodeServerKey = async ({ nodeId, publicKey, panelKey: panel }: { nodeId: string; publicKey?: string; panelKey?: boolean }) => {
    await wait(900);
    if (nodeId === "nod_b_nl1") throw new ConnectError("authorized_keys_unsafe", Code.FailedPrecondition);
    const list = (keysOf[nodeId] ??= { keys: [k.alice], otherLines: 0 });
    if (panel) {
      if (!list.keys.some((x) => x.panelKey)) list.keys.unshift(k.panel);
      if (access[nodeId]) access[nodeId] = { ...access[nodeId], ...viaKey };
      return { key: k.panel };
    }
    const [type, , ...comment] = (publicKey ?? "").trim().split(/\s+/);
    if (!type || !/^(ssh-|ecdsa-|sk-)/.test(type)) throw new ConnectError("ssh_public_key_invalid", Code.InvalidArgument);
    const added = sshKey(type, type === "ssh-rsa" ? 4096 : 256, "SHA256:Nw3eR5tY7uI9oP1aS3dF5gH7jK9lZ1xC3vB5nM7qW9e", comment.join(" "));
    list.keys.push(added);
    return { key: added };
  };
  api.removeNodeServerKey = async ({ nodeId, fingerprint: fp }: { nodeId: string; fingerprint: string }) => {
    await wait(900);
    const list = keysOf[nodeId];
    if (list) list.keys = list.keys.filter((x) => x.fingerprint !== fp);
    if (fp === panelKey.fingerprint && access[nodeId]) access[nodeId] = { ...access[nodeId], panelKeyFingerprint: "" };
    return {};
  };
  // a node installed by command: its password logs in, the panel key does not go on (the server's ~/.ssh is a link)
  api.attachNodeServerAccess = async ({ nodeId, sshHost, username, usePanelKey }: { nodeId: string; sshHost: string; username: string; usePanelKey?: boolean }) => {
    await wait(900);
    if (usePanelKey) throw new ConnectError("ssh_panel_key_not_accepted", Code.FailedPrecondition);
    access[nodeId] = { nodeName: "fi1", host: sshHost, username };
    return { access: { nodeId, ...base, ...access[nodeId] }, panelKeyError: "authorized_keys_unsafe" };
  };
  api.getPanelSSHKey = async () => {
    await wait(150);
    return panelKey;
  };
  api.getSSHFingerprint = async ({ host, port }: { host: string; port: number }) => {
    await wait(300);
    return { host, port, fingerprint, algorithm: "ssh-ed25519" };
  };
  api.checkSSH = async ({ username, privateKey, privateKeyPassphrase }: { username: string; privateKey?: string; privateKeyPassphrase?: string }) => {
    await wait(500);
    if (privateKey && !privateKeyPassphrase) throw new ConnectError("ssh_key_passphrase_required", Code.FailedPrecondition);
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

function Shot({ id, title, wide, seed, children }: { id: string; title: string; wide?: boolean; seed?: [readonly unknown[], unknown]; children: ReactNode }) {
  const [qc] = useState(() => {
    const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false } } });
    if (seed) client.setQueryData(seed[0], seed[1]);
    return client;
  });
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
      <Shot id="ssh-idle" title="SSH access: password only, the panel key offered">
        <SSHAccessCard nodeId="nod_idle" />
      </Shot>
      <Shot id="ssh-key" title="SSH access: the panel key, a saved password, three keys">
        <SSHAccessCard nodeId="nod_key" />
      </Shot>
      <Shot id="ssh-keymissing" title="Keys: the panel key is not on the server">
        <SSHAccessCard nodeId="nod_keymissing" />
      </Shot>
      <Shot id="ssh-keyunused" title="Keys: the panel key is there, not used">
        <SSHAccessCard nodeId="nod_keyunused" />
      </Shot>
      <Shot id="ssh-keysloading" title="Keys: reading them over SSH">
        <SSHAccessCard nodeId="nod_keysloading" />
      </Shot>
      <Shot id="ssh-keyserror" title="Keys: the server refused the login">
        <SSHAccessCard nodeId="nod_keyserror" />
      </Shot>
      <Shot id="ssh-bind" title="Keys: bind a key (paste a private one: refused here)">
        <SSHAccessCard nodeId="nod_key" />
      </Shot>
      <Shot id="ssh-unbind" title="Keys: unbind an own key">
        <SSHAccessCard nodeId="nod_key" />
      </Shot>
      <Shot id="ssh-unbindpanel" title="Keys: unbind the panel key (the password is checked first)">
        <SSHAccessCard nodeId="nod_key" />
      </Shot>
      <Shot id="ssh-attach" title="A node installed by command: Connect SSH access">
        <StepUpProvider>
          <SSHAccessCard nodeId="nod_attach" attach={{ name: "fi1", address: "fi1.example.com" }} />
        </StepUpProvider>
      </Shot>
      <Shot id="ssh-bulk" title="Nodes page: the offer of the panel key (its run is in the bulk bar, ?only=bulk)" wide seed={[["node-server-access"], { access: bulkAccess }]}>
        <PanelKeyOffer busy={false} onStart={() => {}} />
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
