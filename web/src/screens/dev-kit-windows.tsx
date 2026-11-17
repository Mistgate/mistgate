import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { lazy, Suspense, useState, type ReactNode } from "react";
import { AddNodeProvider, AddNodeWindow, type AddNodeSeed } from "@/components/add-node";
import { ConfirmWindow, type ConfirmView } from "@/components/step-up";
import { TurnstileRow } from "@/components/turnstile";
import { PageTitle } from "@/components/ui/bits";
import { Switch } from "@/components/ui/switch";
import { ToastProvider } from "@/components/ui/toast";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { NodePreflightSchema } from "@/gen/mistgate/admin/v1/provisioning_pb";
import { useLang, useT, type T } from "@/i18n";
import { setEffectsEnabled } from "@/lib/effects";
import { nodesQuery, userSearchQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { useTheme } from "@/lib/theme";
import { Shot } from "./dev-kit-redesign";
import { LoginPage, type LoginPageProps } from "./login";

const Palette = lazy(() => import("@/components/palette"));

// Development only. /dev-kit?only=windows: the four windows of the redesign (sign-in, the confirmation, the search, the new node)
// in each of their states, as the board (PnLogin, PnConfirm, PnSearch, PnAddNode) has them, and the ones the board does not
// draw (the password form with a wrong code, a refused install, a node that needs nftables, a command that is issued again).
// Each scene is a frame of its own width (an iframe, so the window's breakpoints answer to the frame) and a page of its own:
//   /dev-kit?only=windows-page&scene=<id>            fx=1 turns the reveal animation on (the halo and the wave), fx=0 off
// the ids are listed under each frame. The frames follow the theme and the language chosen at the top (they reload with them).
// The app's own dev cases of the sign-in (?case=nopasskey|badtotp|locked) work on the real page, /login?case=...
// Made-up data: the nodes de1, fi1, nl1 and msk1, the people alice and dave, the addresses of the documentation ranges.

const NOW = Math.floor(Date.now() / 1000);

const node = (id: string, name: string, cc: string, location: string, status: NodeStatus, reason?: string) => ({
  id: `nod_${id}`,
  name,
  countryCode: cc,
  location,
  provider: "",
  address: `${name}.example.com`,
  status,
  reason: reason ? { code: reason, params: {} } : undefined,
  online: [],
  sparkBytes: [],
});
const fleet = {
  nodes: [
    node("de1", "de1", "DE", "Frankfurt", NodeStatus.ONLINE, "no_profiles"),
    node("fi1", "fi1", "FI", "Helsinki", NodeStatus.ONLINE, "no_profiles"),
    node("nl1", "nl1", "NL", "Amsterdam", NodeStatus.ONLINE, "no_profiles"),
    node("msk1", "msk1", "RU", "Moscow", NodeStatus.ONLINE),
  ],
};
const person = (id: string, name: string, groupName: string, online: boolean, currentNodeName = "") => ({ id: `usr_${id}`, name, groupName, online, currentNodeName });
const peopleFor: Record<string, ReturnType<typeof person>[]> = {
  de: [person("dave", "dave", "friends", true, "de1")],
  al: [person("alice", "alice", "family", true, "de1")],
  a: [person("alice", "alice", "family", true, "de1"), person("carol", "carol", "friends", true, "fi1")],
};

const panelKey = {
  publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7f2mQ9xW4bN1cR8vT3yL6pZ0sJ5hD2gF9aE7uK4oM mistgate-panel",
  fingerprint: "SHA256:3kQz7mVb1xN8cR2tY5pL0sW9hD4gF6aJ8uE1oK7iM3n",
};
const fingerprint = "SHA256:Vq3n0cX9Lr2bT8kM1pZs6YwE4hJ7uGdA5fNiQoRlKe0";
const pf = (over: { nftables?: boolean } = {}) => create(NodePreflightSchema, {
  distribution: "Ubuntu",
  version: "24.04",
  kernel: "6.8.0",
  architecture: "amd64",
  cpuCount: 2,
  memoryBytes: 2_040_109_466n,
  diskAvailableBytes: 18_683_971_379n,
  systemd: true,
  alreadyEnrolled: false,
  panelReachable: true,
  nftables: true,
  ...over,
});
const preflight = pf();
const job = (state: string, phase: string, errorCode = "") => ({ id: "prv_demo", nodeId: "nod_se1", name: "se1", sshHost: "203.0.113.24", sshPort: 22, state, phase, errorCode, createdUnix: BigInt(NOW), updatedUnix: BigInt(NOW) });
const events = (...phases: string[]) => phases.map((phase, i) => ({ id: i + 1, phase, code: phase, createdUnix: NOW }));
const issued = (over: Record<string, unknown> = {}) =>
  ({
    node: { id: "nod_se1", name: "se1", status: NodeStatus.PENDING, online: [], protocols: [] },
    installCommand:
      "chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni q3m8x2kd7w.invalid --ca-sha256 9f2c1a6d0b7e4c3a8f5d2e1b0c9a8f7e6d5c4b3a2f1e0d9c8b7a6f5e4d3c2b1a --token 2qgx7mbd3jc5u4yvtwnfhx6kqjz4pm2s7ylbq3e5c6a7nd8p9r0s && /root/mistgate-node install",
    caFingerprint: "sha256:9f2c1a6d0b7e4c3a8f5d2e1b0c9a8f7e6d5c4b3a2f1e0d9c8b7a6f5e4d3c2b1a",
    expiresUnix: BigInt(NOW + 3600),
    copyCommand: "scp /var/lib/mistgate/dist/mistgate-node-linux-amd64 root@se1.example.com:/root/mistgate-node",
    ...over,
  }) as unknown as NonNullable<AddNodeSeed["issued"]>;

type Seed = [readonly unknown[], unknown][];
const baseSeed: Seed = [
  [nodesQuery.queryKey, fleet],
  [["panel-ssh-key"], panelKey],
  [["node-server-access"], { access: [] }],
];

type Scene = { id: string; group: string; title: string; w: number; h: number; seed?: Seed; render: (t: T) => ReactNode };
const phone = { w: 400, h: 780 };
const wide = { w: 1440, h: 900 };

// ---- sign-in (PnLogin: idle, check, wait, err, pw, ok), 1440 x 900
const loginProps = (t: T, over: Partial<LoginPageProps> = {}): LoginPageProps => ({
  view: "key",
  host: "panel.example.com",
  supported: true,
  passwordLogin: true,
  captcha: <TurnstileRow status="ok" label={t("win.cf.passed")} />,
  captchaOn: true,
  captchaReady: true,
  busy: false,
  login: "",
  password: "",
  code: "",
  onLogin: () => {},
  onPassword: () => {},
  onCode: () => {},
  onPasskey: () => {},
  onSubmit: (e) => e.preventDefault(),
  onToggle: () => {},
  ...over,
});
/** The password form keeps what is typed in it: the boxes of the code and the fields work on the scene. */
function PasswordScene({ over }: { over?: (t: T) => Partial<LoginPageProps> }) {
  const t = useT();
  const [login, setLogin] = useState("owner");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  return <LoginPage {...loginProps(t, { view: "password", login, password, code, onLogin: setLogin, onPassword: setPassword, onCode: setCode, ...over?.(t) })} />;
}

const scenes: Scene[] = [
  { id: "login-idle", group: "Sign-in", title: "Sign in: the passkey button, the check passed", ...wide, render: (t) => <LoginPage {...loginProps(t)} /> },
  { id: "login-check", group: "Sign-in", title: "Sign in: the check runs, the button waits for it", ...wide, render: (t) => <LoginPage {...loginProps(t, { captcha: <TurnstileRow status="busy" label={t("win.cf.checking")} />, captchaReady: false })} /> },
  { id: "login-wait", group: "Sign-in", title: "Sign in: waiting for the passkey", ...wide, render: (t) => <LoginPage {...loginProps(t, { busy: true })} /> },
  { id: "login-err", group: "Sign-in", title: "Sign in: the passkey did not fit", ...wide, render: (t) => <LoginPage {...loginProps(t, { error: t("err.cancelled") })} /> },
  { id: "login-pw", group: "Sign-in", title: "Sign in: password and code (type in the boxes)", ...wide, render: () => <PasswordScene /> },
  { id: "login-pw-bad", group: "Sign-in", title: "Sign in: a wrong code, attempts left", ...wide, render: () => <PasswordScene over={(t) => ({ password: "hunter2hunter2", code: "", codeError: `${t("auth.badLogin")} ${t.n("auth.attemptsLeft", 2)}` })} /> },
  { id: "login-pw-wait", group: "Sign-in", title: "Sign in: the password is being checked", ...wide, render: () => <PasswordScene over={() => ({ busy: true, password: "hunter2hunter2", code: "481209" })} /> },
  { id: "login-ok", group: "Sign-in", title: "Sign in: through, the panel is loading", ...wide, render: (t) => <LoginPage {...loginProps(t, { view: "done" })} /> },
  { id: "login-nocaptcha", group: "Sign-in", title: "Sign in: no check on this panel", ...wide, render: (t) => <LoginPage {...loginProps(t, { captcha: null, captchaOn: false })} /> },
  { id: "login-nopasskey", group: "Sign-in", title: "Sign in: a browser without passkeys (the password form only)", ...wide, render: () => <PasswordScene over={() => ({ supported: false })} /> },
  { id: "login-offline", group: "Sign-in", title: "Sign in: the panel did not answer for the login info", ...wide, render: (t) => <LoginPage {...loginProps(t, { onRetryInfo: () => {} })} /> },
  { id: "login-phone", group: "Sign-in", title: "Sign in on a phone, 400 wide", ...phone, render: (t) => <LoginPage {...loginProps(t)} /> },
  { id: "login-phone-pw", group: "Sign-in", title: "The password form on a phone", ...phone, render: () => <PasswordScene /> },
];

// ---- the confirmation (PnConfirm: idle, wait, err, code, ok), the board's frame is 520 x 640
function ConfirmScene({ view, error, canKey = true, canCode = true }: { view: ConfirmView; error?: string; canKey?: boolean; canCode?: boolean }) {
  const t = useT();
  const [code, setCode] = useState("");
  return (
    <ConfirmWindow
      view={view}
      what={t("win.node.whatInstall", { host: "192.0.2.10" })}
      error={error}
      code={code}
      onCode={setCode}
      canKey={canKey}
      canCode={canCode}
      onKey={() => {}}
      onSubmitCode={() => {}}
      onSwap={() => {}}
      onCancel={() => {}}
    />
  );
}
const confirm = (id: string, title: string, body: ReactNode, size = { w: 520, h: 640 }): Scene => ({ id, group: "Confirmation", title, ...size, render: () => body });
scenes.push(
  confirm("confirm-idle", "Confirmation: the request", <ConfirmScene view="idle" />),
  confirm("confirm-wait", "Confirmation: waiting for the passkey (the rings go out)", <ConfirmScene view="wait" />),
  { id: "confirm-err", group: "Confirmation", title: "Confirmation: the passkey did not fit", w: 520, h: 640, render: (t) => <ConfirmScene view="err" error={t("err.cancelled")} /> },
  confirm("confirm-code", "Confirmation: the code of the app (six boxes)", <ConfirmScene view="code" />),
  { id: "confirm-code-bad", group: "Confirmation", title: "Confirmation: a wrong code", w: 520, h: 640, render: (t) => <ConfirmScene view="code" error={t("stepup.badCode")} /> },
  confirm("confirm-codewait", "Confirmation: the code is being checked", <ConfirmScene view="codewait" />),
  confirm("confirm-ok", "Confirmation: confirmed (the ring fills, the tick draws itself)", <ConfirmScene view="ok" />),
  confirm("confirm-key-only", "Confirmation: an admin with a passkey only (no code to switch to)", <ConfirmScene view="idle" canCode={false} />),
  confirm("confirm-code-only", "Confirmation: an admin with a password and a code only", <ConfirmScene view="code" canKey={false} />),
  confirm("confirm-phone", "Confirmation on a phone, 400 wide", <ConfirmScene view="idle" />, phone),
);

// ---- the search (PnSearch: empty, "de", "na", nothing), the board's frame is 800 x 640
const search = (id: string, title: string, q: string, size = { w: 800, h: 640 }): Scene => ({
  id,
  group: "Search",
  title,
  ...size,
  seed: [
    ...baseSeed,
    [userSearchQuery("de").queryKey, { users: peopleFor.de }],
    [userSearchQuery("al").queryKey, { users: peopleFor.al }],
    [userSearchQuery("a").queryKey, { users: peopleFor.a }],
    [userSearchQuery("qwe").queryKey, { users: [] }],
  ],
  render: () => (
    <Suspense fallback={null}>
      <Palette open onOpenChange={() => {}} initialQuery={q} />
    </Suspense>
  ),
});
scenes.push(
  search("search-empty", "Search: the first look (the actions and the nodes)", ""),
  search("search-de", "Search: «de» (a node, a person, an action)", "de"),
  search("search-al", "Search: «al» (a person)", "al"),
  search("search-a", "Search: «a» (a few of each: five rows at most, the count at the foot)", "a"),
  search("search-none", "Search: nothing found", "qwe"),
  search("search-phone", "Search on a phone, 400 wide (no foot, no Enter keys)", "de", phone),
);

// ---- the new node (PnAddNode: srv, auth, chk, run, done, fail, cmd), the board's frame is 960 x 760
const filled = { name: "se1", address: "se1.example.com", country: "SE" };
const ssh = { host: "203.0.113.24", port: "22", fingerprint, algorithm: "ssh-ed25519" };
const authSeed = { ...ssh, step: 1 as const };
const checkSeed = { ...ssh, step: 2 as const, confirmedKey: true, preflight, method: "key" as const };
const jobSeeds = (state: string, phase: string, ev: string[], code = ""): Seed => [
  [["node-provision", "prv_demo"], job(state, phase, code)],
  [["node-provision-events", "prv_demo"], events(...ev)],
];
const doneSeed: Seed = [[["node", "nod_se1"], { node: { id: "nod_se1", name: "se1", status: NodeStatus.ONLINE }, inbounds: [] }]];
const node4 = { w: 960, h: 760 };
const win = (id: string, title: string, seed: AddNodeSeed | ((t: T) => AddNodeSeed), q: Seed = [], mode: "ssh" | "cmd" = "ssh", size = node4, reenroll?: { id: string; name: string }): Scene => ({
  id,
  group: "New node",
  title,
  ...size,
  seed: [...baseSeed, ...q],
  render: (t) => <AddNodeWindow open reenroll={reenroll} initialMode={mode} onClose={() => {}} seed={typeof seed === "function" ? seed(t) : seed} />,
});
const runEvents = ["connecting", "preflight", "firewall", "transfer"];
scenes.push(
  win("node-srv", "New node, 1 Server: empty", {}),
  win("node-srv-filled", "New node, 1 Server: filled in", { draft: filled, ssh: { host: "203.0.113.24", optionsOpen: true, location: "Stockholm" } }),
  win("node-srv-country", "New node, 1 Server: the list of countries", { draft: { ...filled, country: "none" }, ssh: { host: "203.0.113.24", optionsOpen: true, countryOpen: true } }),
  win("node-srv-bad", "New node, 1 Server: a name that does not fit", { draft: { name: "SE_1", address: "se1.example.com", country: "none" }, ssh: { host: "203.0.113.24", failure: "" } }),
  win("node-srv-refused", "New node, 1 Server: the target refused (the way out is the command)", (t) => ({ draft: filled, ssh: { host: "203.0.113.24", failure: t("node.ssh.error.refused") } })),
  win("node-auth", "New node, 2 Login: the key not confirmed yet", { draft: filled, ssh: authSeed }),
  win("node-auth-ok", "New node, 2 Login: confirmed, where to look open, the panel key with its how-to", { draft: filled, ssh: { ...authSeed, confirmedKey: true, whereOpen: true, method: "key" } }),
  win("node-auth-pw", "New node, 2 Login: a password", { draft: filled, ssh: { ...authSeed, confirmedKey: true, method: "password" } }),
  win("node-auth-own", "New node, 2 Login: an own key", { draft: filled, ssh: { ...authSeed, confirmedKey: true, method: "own" } }),
  win("node-chk-busy", "New node, 3 Check: the server is being read", { draft: filled, ssh: { ...ssh, step: 2, confirmedKey: true, method: "key" } }),
  win("node-chk-mid", "New node, 3 Check: the rows answer one by one", { draft: filled, ssh: { ...checkSeed, answered: 3 } }),
  win("node-chk", "New node, 3 Check: the server fits, what the install does", { draft: filled, ssh: { ...checkSeed, answered: 99 } }),
  win("node-chk-nft", "New node, 3 Check: no nftables on the server (its own step)", { draft: filled, ssh: { ...checkSeed, preflight: pf({ nftables: false }) } }),
  win("node-run", "New node, 4 Install: stage 4 of 7", { draft: filled, ssh: { ...checkSeed, step: 3, jobId: "prv_demo", method: "key" } }, jobSeeds("running", "transfer", runEvents)),
  win("node-run-wait", "New node, 4 Install: waiting for the node (up to 2 minutes)", { draft: filled, ssh: { ...checkSeed, step: 3, jobId: "prv_demo", method: "key" } }, jobSeeds("running", "waiting_node", ["connecting", "preflight", "firewall", "transfer", "enrollment", "install", "waiting_node"])),
  win("node-fail", "New node, 4 Install: the server did not take the panel key", { draft: filled, ssh: { ...checkSeed, step: 3, jobId: "prv_demo", method: "key" } }, jobSeeds("failed", "failed", ["connecting"], "ssh_authentication_failed")),
  win("node-fail-late", "New node, 4 Install: failed on the firewall, the server was changed", { draft: filled, ssh: { ...checkSeed, step: 3, jobId: "prv_demo", method: "password" } }, jobSeeds("failed", "failed", runEvents.slice(0, 3), "host_firewall_configuration_failed")),
  win("node-done", "New node: installed and connected", { draft: filled, ssh: { ...checkSeed, step: 3, jobId: "prv_demo", method: "key" } }, [...jobSeeds("completed", "completed", [...runEvents, "enrollment", "install", "waiting_node"]), ...doneSeed]),
  win("node-cmd", "New node by a command, 1 Data", { draft: { name: "", address: "", country: "none" } }, [], "cmd"),
  win("node-cmd-ip", "New node by a command, 1 Data: an address that is an IP", { draft: { name: "se1", address: "203.0.113.24", country: "SE" } }, [], "cmd"),
  win("node-cmd-run", "New node by a command, 2 Command", { draft: filled, issued: issued() }, [], "cmd"),
  win("node-cmd-nobundle", "New node by a command, 2 Command: no trusted bundle (no scp)", { draft: filled, issued: issued({ copyCommand: "" }) }, [], "cmd"),
  win("node-cmd-done", "New node by a command: the node came", { draft: filled, issued: issued() }, doneSeed, "cmd"),
  win("node-reenroll", "A new command for a node that exists", { draft: filled }, [], "cmd", node4, { id: "nod_de1", name: "de1" }),
  win("node-phone-srv", "New node on a phone: a sheet, the optional fields folded", { draft: filled }, [], "ssh", phone),
  win("node-phone-auth", "New node on a phone: the login", { draft: filled, ssh: { ...authSeed, confirmedKey: true, method: "key" } }, [], "ssh", phone),
  win("node-phone-chk", "New node on a phone: the check", { draft: filled, ssh: { ...checkSeed, answered: 99 } }, [], "ssh", phone),
  win("node-phone-run", "New node on a phone: the install", { draft: filled, ssh: { ...checkSeed, step: 3, jobId: "prv_demo", method: "key" } }, jobSeeds("running", "transfer", runEvents), "ssh", phone),
  win("node-phone-done", "New node on a phone: ready", { draft: filled, ssh: { ...checkSeed, step: 3, jobId: "prv_demo", method: "key" } }, [...jobSeeds("completed", "completed", runEvents), ...doneSeed], "ssh", phone),
  win("node-phone-cmd", "New node on a phone: the command", { draft: filled, issued: issued() }, [], "cmd", phone),
);

// ---- a page of one scene, and the gallery of the frames

function seeded(extra: Seed = []) {
  const qc = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role: Role.OWNER }, version: "0.1.71", stepUpUntilUnix: 0n } as never);
  for (const [key, data] of extra) qc.setQueryData(key, data);
  return qc;
}

function SceneBody({ scene }: { scene: Scene }) {
  const t = useT();
  return <>{scene.render(t)}</>;
}

/** One whole scene on a page of its own, at the width of the window it is opened in. */
export function WindowsPage() {
  const params = new URLSearchParams(location.search);
  const scene = scenes.find((s) => s.id === params.get("scene")) ?? scenes[0]!;
  const [qc] = useState(() => {
    if (params.get("fx") === "1") setEffectsEnabled(true);
    else if (params.get("fx") === "0") setEffectsEnabled(false);
    return seeded(scene.seed);
  });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AddNodeProvider>
          <SceneBody scene={scene} />
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

export function WindowsKit() {
  // the frames read the theme, the language and the animation when they load: a change at the top reloads them
  const [fx, setFx] = useState(true);
  const key = `${useTheme()}-${useLang()}-${fx}`;
  const path = location.pathname;
  const groups = [...new Set(scenes.map((s) => s.group))];
  return (
    <div className="flex flex-col gap-8">
      <div className="flex flex-wrap items-center gap-3">
        <PageTitle>Windows</PageTitle>
        <div className="flex-1" />
        <label className="flex items-center gap-2 text-[13px] text-sec">
          <Switch checked={fx} onCheckedChange={setFx} aria-label="Reveal animation" />
          Reveal animation (halo, wave)
        </label>
        <LangToggle />
        <ThemeToggle />
      </div>
      {groups.map((g) => (
        <div key={g} className="flex flex-col gap-8">
          {scenes
            .filter((s) => s.group === g)
            .map((s) => (
              <Shot key={s.id} id={`windows-${s.id}`} title={`${s.title} · ${s.id} · ${s.w}`}>
                <div className="max-w-full overflow-x-auto rounded-card border border-line">
                  {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
                  {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
                  <iframe key={key} title={s.title} src={`${path}?only=windows-page&scene=${s.id}&fx=${fx ? 1 : 0}`} width={s.w} height={s.h} className="block max-w-none border-0" />
                </div>
              </Shot>
            ))}
        </div>
      ))}
    </div>
  );
}
