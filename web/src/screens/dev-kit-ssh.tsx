import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { provisioning } from "@/lib/api";
import { SSHAccessCard } from "@/screens/node/ssh-access";

// Development only, /dev-kit?only=ssh: a node's SSH access card (Server tab) in each state, on made-up data. The
// provisioning calls are answered here by node: a change that goes through, one that never ends (the progress), one the
// server does not confirm, one refused at the login, an interrupted change, a retired node.

const fingerprint = "SHA256:Vq3n0cX9Lr2bT8kM1pZs6YwE4hJ7uGdA5fNiQoRlKe0";
const base = { port: 22, fingerprint, configuredUnix: Math.floor(Date.now() / 1000) - 86400 * 12 };
const access: Record<string, Record<string, unknown>> = {
  nod_idle: { nodeName: "de1", host: "203.0.113.7", username: "root" },
  nod_generated: { nodeName: "nl1", host: "203.0.113.24", username: "deploy", passwordGenerated: true },
  nod_slow: { nodeName: "de1", host: "203.0.113.7", username: "root" },
  nod_unverified: { nodeName: "nl1", host: "203.0.113.24", username: "root" },
  nod_refused: { nodeName: "de1", host: "203.0.113.7", username: "deploy" },
  nod_pending: { nodeName: "nl1", host: "203.0.113.24", username: "root", rotationPending: true },
  nod_retired: { nodeName: "de1", host: "203.0.113.7", username: "root", nodeRetired: true, passwordGenerated: true },
};

const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

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
    access[nodeId] = { ...access[nodeId], passwordGenerated: !!generate };
    return { rotated: true };
  };
  api.forgetNodeServerAccess = async () => ({});
}
fakeCalls();

function Shot({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  const [qc] = useState(() => new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false } } }));
  return (
    <section data-shot={id} className="flex w-full max-w-[452px] flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <div role="tabpanel">{children}</div>
        </ToastProvider>
      </QueryClientProvider>
    </section>
  );
}

export function SSHKit() {
  return (
    <div className="flex flex-wrap items-start gap-8">
      <Shot id="ssh-idle" title="SSH access: saved, verified">
        <SSHAccessCard nodeId="nod_idle" />
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
