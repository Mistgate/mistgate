import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactElement, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { en } from "@/i18n/en";
import { setAccent } from "@/lib/accent";
import { setEffectsEnabled } from "@/lib/effects";
import { readPref } from "@/lib/storage";
import { SettingsLayout } from "../settings";
import { SxGate } from "./sx-bits";
import { AdminsPage } from "./admins";
import { BackupsPage } from "./backups";
import { cloudflareAllowed } from "./cloudflare";
import { DomainsPage } from "./domains";
import { InterfacePage } from "./interface";
import { SecurityPage } from "./security";
import { SessionsPage } from "./sessions";

// Settings, page by page: what each one says, and the calls behind its buttons.

const auth = vi.hoisted(() => ({
  me: vi.fn(),
  listPasskeys: vi.fn(),
  getPasswordLogin: vi.fn(),
  changePassword: vi.fn(),
  beginTotpEnrollment: vi.fn(),
  finishTotpEnrollment: vi.fn(),
  getSecuritySettings: vi.fn(),
  updateSecuritySettings: vi.fn(),
  listSessions: vi.fn(),
  endSession: vi.fn(),
  endOtherSessions: vi.fn(),
}));
const getInstance = vi.hoisted(() => vi.fn());
const updateInstance = vi.hoisted(() => vi.fn());
const backupApi = vi.hoisted(() => ({
  getBackupSettings: vi.fn(),
  listBackups: vi.fn(),
  updateBackupSettings: vi.fn(),
  testBackupStorage: vi.fn(),
  createBackup: vi.fn(),
}));
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  auth,
  instance: { getInstance: (...a: unknown[]) => getInstance(...a), updateInstance: (...a: unknown[]) => updateInstance(...a) },
  backups: backupApi,
  webauthnSupported: () => true,
}));
let section = "interface";
const navigate = vi.hoisted(() => vi.fn());
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className, to, params, ...rest }: { children?: ReactNode; className?: string; to: string; params?: { section?: string } }) => (
    <a href={params?.section ? `/settings/${params.section}` : to} data-status={params?.section === section ? "active" : undefined} className={className} {...rest}>
      {children}
    </a>
  ),
  useParams: () => ({ section }),
  useSearch: () => ({}),
  useNavigate: () => navigate,
  Outlet: () => <p>the page of the tab</p>,
  Navigate: () => null,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

type Options = Record<string, (...a: unknown[]) => unknown>;
let widgets: { options: Options }[] = [];
let role = Role.OWNER;
let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  role = Role.OWNER;
  section = "interface";
  navigate.mockReset();
  widgets = [];
  window.turnstile = {
    render: (_el, options) => {
      widgets.push({ options: options as Options });
      return `w${widgets.length}`;
    },
    reset: () => {},
    remove: () => {},
  };
  auth.me.mockImplementation(async () => ({ admin: { id: "adm_1", displayName: "Alice", role } }));
  auth.listPasskeys.mockResolvedValue({ passkeys: [] });
  auth.getPasswordLogin.mockResolvedValue({ enabled: true, login: "alice", available: true });
  auth.getSecuritySettings.mockResolvedValue({ turnstileEnabled: false, turnstileSiteKey: "", turnstileSecretSet: false });
  backupApi.getBackupSettings.mockResolvedValue({ settings: {
    accountId: "", jurisdiction: "default", bucket: "", accessKeyId: "", hasSecret: false, ageRecipient: "",
    enabled: false, intervalHours: 24, retentionDays: 0, lastSuccessUnix: 0, lastErrorCode: "",
  } });
  backupApi.listBackups.mockResolvedValue({ backups: [] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  delete window.turnstile;
  Object.values(auth).forEach((m) => m.mockReset());
  Object.values(backupApi).forEach((m) => m.mockReset());
  getInstance.mockReset();
  updateInstance.mockReset();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function mount(ui: ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>{ui}</ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
// a toast is a dialog too, a modeless one
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]:not([aria-modal=false])");
const inDialog = (label: string) => [...(dialog()?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === label);
const click = async (b: Element | undefined | null) => {
  await act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await settle();
};
const input = (selector: string) => document.querySelector<HTMLInputElement>(selector)!;
async function type(el: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function submit(label: string) {
  const b = (dialog() ? inDialog(label) : button(label)) as HTMLButtonElement | undefined;
  // a window's buttons stand in its footer, outside the form, tied to it by the form attribute
  const form = b?.getAttribute("form") ? document.getElementById(b.getAttribute("form")!) : b?.closest("form");
  await act(async () => void (form as HTMLFormElement | null)?.requestSubmit(b));
  for (let i = 0; i < 3; i++) await settle();
}
/** location as a page loaded from `path` would have it (the CSP of a document follows the path it was served at). */
function servedAt(path: string, search = "") {
  const href = `http://localhost:3000${path}${search}`;
  const assign = vi.fn();
  vi.stubGlobal("location", { href, pathname: path, search, hash: "", host: "localhost:3000", assign });
  return assign;
}
const coded = (code: string, c = Code.InvalidArgument) => new ConnectError(code, c);

describe("Settings: the frame", () => {
  const tabs = () => [...document.querySelectorAll<HTMLElement>("[role=tab]")];
  it("says what the page is for and lists the eight tabs in the board's order, the open one chosen", async () => {
    await mount(<SettingsLayout />);
    expect(document.querySelector("h1")?.textContent).toBe("Settings");
    expect(document.querySelector(".sb-h p")?.textContent).toBe(en["sx.sum"]);
    expect(tabs().map((a) => a.textContent)).toEqual(["Interface", "System", "Security", "Sessions", "Audit", "Admins", "Domains", "Backups"]);
    expect(tabs().find((a) => a.getAttribute("aria-selected") === "true")?.textContent).toBe("Interface");
    expect(document.querySelector("[role=tabpanel]")?.textContent).toContain("the page of the tab");
  });

  it("opens the tab of the address and takes the route to another one on a click", async () => {
    section = "sessions";
    await mount(<SettingsLayout />);
    expect(tabs().find((a) => a.getAttribute("aria-selected") === "true")?.textContent).toBe("Sessions");
    await click(tabs().find((a) => a.textContent === "Domains"));
    expect(navigate).toHaveBeenCalledWith({ to: "/settings/$section", params: { section: "domains" } });
  });

  it("draws the load of a tab and its failure, with a way to try again", async () => {
    const retry = vi.fn();
    await mount(<SxGate pending error={false} onRetry={retry}><p>ready</p></SxGate>);
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    expect(text()).not.toContain("ready");
    await act(async () => root!.render(<SxGate pending={false} error onRetry={retry}><p>ready</p></SxGate>));
    expect(document.querySelector("[role=alert]")?.textContent).toContain(en["sx.err.title"]);
    await click(button(en["sx.err.retry"]));
    expect(retry).toHaveBeenCalled();
    await act(async () => root!.render(<SxGate pending={false} error={false} onRetry={retry}><p>ready</p></SxGate>));
    expect(text()).toContain("ready");
  });
});

describe("Settings → Security → Password and code", () => {
  it("shows the login, the two changes and the way back from the server for an admin with a password", async () => {
    await mount(<SecurityPage />);
    expect(text()).toContain(en["sx.sec.pw.on"]);
    expect(text()).toContain("alice");
    expect(button(en["pw.change"])).toBeDefined();
    expect(button(en["pw.rebind"])).toBeDefined();
    expect(button(en["pw.add"])).toBeUndefined();
    expect(text()).toContain(en["pw.lost"]);
    expect(text()).toContain("mistgate auth reset-login alice");
  });

  it("offers a passkey admin to add one, and says when the server cannot keep it", async () => {
    auth.getPasswordLogin.mockResolvedValue({ enabled: false, login: "", available: true });
    await mount(<SecurityPage />);
    expect(text()).toContain(en["pw.off"]);
    expect(button(en["pw.add"])).toBeDefined();
    expect(text()).toContain("mistgate auth reset-login");
    act(() => root?.unmount());
    host?.remove();

    auth.getPasswordLogin.mockResolvedValue({ enabled: false, login: "", available: false });
    await mount(<SecurityPage />);
    expect(text()).toContain(en["pw.noKey"]);
    expect(button(en["pw.add"])).toBeUndefined();
    expect(text()).not.toContain("reset-login");
  });

  it("changes the password with the current one, says so when it is wrong, and counts the ended sessions", async () => {
    auth.changePassword.mockRejectedValueOnce(coded("wrong_password")).mockResolvedValueOnce({ endedSessions: 2 });
    await mount(<SecurityPage />);
    await click(button(en["pw.change"]));
    expect(dialog()?.textContent).toContain(en["pw.changeBody"]);
    await type(input("[role=dialog] input[autocomplete=current-password]"), "old password 1");
    // the board asks on the press: a short new password is refused there, and nothing is sent
    await type(input("[role=dialog] input[autocomplete=new-password]"), "short");
    await submit(en["pw.change"]);
    expect(dialog()?.querySelector(".an-f.err small")?.textContent).toBe(en["auth.err.passwordShort"]);
    expect(auth.changePassword).not.toHaveBeenCalled();
    await type(input("[role=dialog] input[autocomplete=new-password]"), "a much longer new password");
    await submit(en["pw.change"]);
    expect(auth.changePassword).toHaveBeenCalledWith({ currentPassword: "old password 1", newPassword: "a much longer new password" });
    expect(dialog()?.textContent).toContain(en["pw.wrong"]);

    await type(input("[role=dialog] input[autocomplete=current-password]"), "old password 2");
    await submit(en["pw.change"]);
    expect(auth.changePassword).toHaveBeenLastCalledWith({ currentPassword: "old password 2", newPassword: "a much longer new password" });
    expect(dialog()).toBeNull();
    expect(text()).toContain("Password changed. Other sessions ended: 2");
  });

  it("re-binds the app: the QR first, then a code of the new app; a wrong code keeps the QR", async () => {
    auth.beginTotpEnrollment.mockResolvedValue({ ceremonyId: "cer_1", totpUri: "otpauth://totp/Mistgate:alice?secret=JBSWY3DPEHPK3PXP", totpSecret: "JBSWY3DPEHPK3PXP" });
    auth.finishTotpEnrollment.mockRejectedValueOnce(coded("invalid_code")).mockResolvedValueOnce({ login: "alice", endedSessions: 1 });
    await mount(<SecurityPage />);
    await click(button(en["pw.rebind"]));
    expect(auth.beginTotpEnrollment).toHaveBeenCalledWith({});
    expect(dialog()?.textContent).toContain(en["pw.rebindTitle"]);
    expect(dialog()?.textContent).toContain("JBSW Y3DP EHPK 3PXP");
    await type(input("[role=dialog] input[inputmode=numeric]"), "111111");
    await submit(en["stepup.confirm"]);
    expect(auth.finishTotpEnrollment).toHaveBeenCalledWith({ ceremonyId: "cer_1", totpCode: "111111" });
    expect(dialog()?.textContent).toContain(en["auth.badCode"]);

    await type(input("[role=dialog] input[inputmode=numeric]"), "222222");
    await submit(en["stepup.confirm"]);
    expect(dialog()).toBeNull();
    expect(text()).toContain("The new app is bound. Other sessions ended: 1");
  });

  it("closes a re-bind whose time ran out and says to start again", async () => {
    auth.beginTotpEnrollment.mockResolvedValue({ ceremonyId: "cer_1", totpUri: "otpauth://totp/x?secret=AAAA", totpSecret: "AAAA" });
    auth.finishTotpEnrollment.mockRejectedValue(coded("enrollment_expired"));
    await mount(<SecurityPage />);
    await click(button(en["pw.rebind"]));
    await type(input("[role=dialog] input[inputmode=numeric]"), "123456");
    await submit(en["stepup.confirm"]);
    expect(dialog()).toBeNull();
    expect(text()).toContain(en["pw.expired"]);
  });

  it("adds a password login to a passkey admin: login and password, then the QR and a code", async () => {
    auth.getPasswordLogin.mockResolvedValue({ enabled: false, login: "", available: true });
    auth.beginTotpEnrollment.mockRejectedValueOnce(coded("login_taken", Code.AlreadyExists)).mockResolvedValueOnce({ ceremonyId: "cer_2", totpUri: "otpauth://totp/x?secret=BBBB", totpSecret: "BBBB" });
    auth.finishTotpEnrollment.mockResolvedValue({ login: "alice", endedSessions: 0 });
    await mount(<SecurityPage />);
    await click(button(en["pw.add"]));
    await type(input("[role=dialog] input[autocomplete=username]"), "bad login!");
    expect(dialog()?.textContent).toContain(en["auth.err.login"]);
    expect((inDialog(en["auth.next"]) as HTMLButtonElement).disabled).toBe(true);
    await type(input("[role=dialog] input[autocomplete=username]"), "alice");
    await type(input("[role=dialog] input[autocomplete=new-password]"), "correct horse battery");
    await submit(en["auth.next"]);
    expect(auth.beginTotpEnrollment).toHaveBeenCalledWith({ login: "alice", password: "correct horse battery" });
    expect(dialog()?.textContent).toContain(en["pw.loginTaken"]);

    await submit(en["auth.next"]);
    expect(dialog()?.textContent).toContain("BBBB");
    await type(input("[role=dialog] input[inputmode=numeric]"), "654321");
    await submit(en["stepup.confirm"]);
    expect(auth.finishTotpEnrollment).toHaveBeenCalledWith({ ceremonyId: "cer_2", totpCode: "654321" });
    expect(text()).toContain(en["pw.added"]);
  });
});

describe("Settings → Security → Sign-in (Cloudflare)", () => {
  it("knows which documents may load Cloudflare: the Security page itself, or any page while the check is on", () => {
    expect(cloudflareAllowed(false, "/settings/security")).toBe(true);
    expect(cloudflareAllowed(false, "/settings/sessions")).toBe(false);
    expect(cloudflareAllowed(false, "/")).toBe(false);
    expect(cloudflareAllowed(true, "/")).toBe(true);
  });

  it("opens the Security page afresh, switched on, when this page cannot load the check", async () => {
    const assign = servedAt("/");
    await mount(<SecurityPage />);
    expect(text()).toContain(en["cf.card"]);
    await click(document.querySelector("[role=switch]"));
    expect(String(assign.mock.calls[0]![0])).toBe("http://localhost:3000/settings/security?turnstile=on");
    expect(auth.updateSecuritySettings).not.toHaveBeenCalled();
  });

  it("switches on only with a token the new keys made, and says plainly when the secret does not fit", async () => {
    servedAt("/settings/security", "?turnstile=on");
    const replace = vi.spyOn(history, "replaceState");
    auth.getSecuritySettings.mockResolvedValue({ turnstileEnabled: false, turnstileSiteKey: "0x4AAA", turnstileSecretSet: true });
    auth.updateSecuritySettings
      .mockRejectedValueOnce(coded("turnstile_secret_rejected", Code.FailedPrecondition))
      .mockResolvedValueOnce({ turnstileEnabled: true, turnstileSiteKey: "0x4AAA", turnstileSecretSet: true });
    await mount(<SecurityPage />);
    expect(replace).toHaveBeenCalled(); // ?turnstile=on leaves the address
    expect(document.querySelector("[role=switch]")?.getAttribute("aria-checked")).toBe("true");
    expect(button(en["common.save"])).toBeUndefined(); // nothing is saved unchecked
    await click(button(en["cf.prove"]));
    expect(text()).toContain(en["cf.proveHint"]);
    expect(widgets.at(-1)!.options.sitekey).toBe("0x4AAA");
    await act(async () => void widgets.at(-1)!.options.callback!("tok-1"));
    await settle();
    expect(auth.updateSecuritySettings).toHaveBeenCalledWith(expect.objectContaining({ turnstileEnabled: true, turnstileTestToken: "tok-1" }));
    expect(text()).toContain(en["cf.err.secret"]);
    expect(button(en["cf.prove"])).toBeDefined(); // the used token is gone: the next try is a fresh check

    await click(button(en["cf.prove"]));
    await act(async () => void widgets.at(-1)!.options.callback!("tok-2"));
    await settle();
    expect(auth.updateSecuritySettings).toHaveBeenLastCalledWith(expect.objectContaining({ turnstileTestToken: "tok-2" }));
    expect(text()).toContain(en["cf.saved"]);
  });

  it("says the site key does not fit this address when Cloudflare refuses the domain", async () => {
    servedAt("/settings/security");
    auth.getSecuritySettings.mockResolvedValue({ turnstileEnabled: true, turnstileSiteKey: "0x4AAA", turnstileSecretSet: true });
    await mount(<SecurityPage />);
    await type(input("input[autocomplete=off]"), "0x4BBB");
    await click(button(en["cf.proveSave"]));
    const fail = widgets.at(-1)!.options["error-callback"]!;
    await act(async () => {
      fail("110200");
      fail("110200");
      fail("110200");
    });
    expect(text()).toContain(en["cf.err.sitekey"]);
    expect(auth.updateSecuritySettings).not.toHaveBeenCalled();
  });

  it("asks before removing the secret key, which switches the check off", async () => {
    servedAt("/settings/security");
    auth.getSecuritySettings.mockResolvedValue({ turnstileEnabled: true, turnstileSiteKey: "0x4AAA", turnstileSecretSet: true });
    auth.updateSecuritySettings.mockResolvedValue({ turnstileEnabled: false, turnstileSiteKey: "0x4AAA", turnstileSecretSet: false });
    await mount(<SecurityPage />);
    await click(button(en["cf.secretRemove"]));
    expect(dialog()?.textContent).toContain(en["cf.removeBody"]);
    expect(auth.updateSecuritySettings).not.toHaveBeenCalled();
    await click(inDialog(en["cf.secretRemove"]));
    expect(auth.updateSecuritySettings).toHaveBeenCalledWith({ turnstileSecretKey: "" });
    expect(text()).toContain(en["cf.removed"]);
  });

  it("is not there for a helper", async () => {
    role = Role.HELPER;
    await mount(<SecurityPage />);
    expect(text()).not.toContain(en["cf.title"]);
    expect(auth.getSecuritySettings).not.toHaveBeenCalled();
  });
});

describe("Settings → Sessions", () => {
  it("marks this device and asks before ending the other sessions", async () => {
    const now = Math.floor(Date.now() / 1000);
    const s = (id: string, current = false) => ({ id, current, userAgent: "", ip: "203.0.113.7", createdAtUnix: now - 3600, lastSeenAtUnix: now - 60 });
    auth.listSessions.mockResolvedValue({ sessions: [s("s1", true), s("s2"), s("s3")] });
    auth.endOtherSessions.mockResolvedValue({});
    await mount(<SessionsPage />);
    expect(text()).toContain("this device");
    await click(button(en["sess.endOthers"]));
    expect(dialog()?.textContent).toContain("End 2 other sessions?");
    expect(dialog()?.textContent).toContain(en["sess.endOthersBody"]);
    expect(auth.endOtherSessions).not.toHaveBeenCalled();
    await click(inDialog(en["sess.endOthers"]));
    expect(auth.endOtherSessions).toHaveBeenCalledWith({});
    expect(dialog()).toBeNull();
  });
});

describe("Settings → Admins, Domains, Backups", () => {
  it("shows who is signed in and how, and points scripts to API tokens", async () => {
    auth.listPasskeys.mockResolvedValue({ passkeys: [{ id: "pk1" }, { id: "pk2" }] });
    await mount(<AdminsPage />);
    expect(text()).toContain("Alice");
    expect(text()).toContain("Owner");
    expect(text()).toContain("Signs in with: 2 passkeys, password and code (login “alice”)");
    expect(text()).toContain("A helper cannot be added yet.");
    expect(document.querySelector("a[href='/ai']")?.textContent).toBe(en["sx.adm.tokens"]);
    expect(document.querySelector("a[href='/settings/security']")?.textContent).toBe(en["settings.security"]);
  });

  it("shows the owner both addresses to copy and that setup fixed them", async () => {
    getInstance.mockResolvedValue({ instance: { adminUrl: "https://panel.example/s3cret/", subscriptionBase: "", brandHead: "", brandTail: "", accent: "", language: "en", hasLogo: false, logoVersion: "" } });
    await mount(<DomainsPage />);
    expect(text()).toContain("https://panel.example/s3cret/");
    expect(text()).toContain(en["set.domains.subNone"]);
    expect(text()).toContain("mistgate setup sets them once, when the panel is installed; they cannot be changed yet");
    expect(text()).toContain(en["sx.dom.warn"]);
    expect(button(en["common.copy"])).toBeDefined();
  });

  it("keeps the addresses from anyone but the owner", async () => {
    role = Role.HELPER;
    await mount(<DomainsPage />);
    expect(text()).toContain(en["set.domains.ownerOnly"]);
    expect(getInstance).not.toHaveBeenCalled();
  });

  it("shows encrypted restore commands before R2 is configured", async () => {
    await mount(<BackupsPage />);
    expect(text()).toContain(en["set.backups.title"]);
    expect(text()).toContain("mistgate backup keygen --identity-file ./mistgate-recovery.txt");
    expect(text()).toContain("mistgate backup restore --identity-file ./mistgate-recovery.txt --file ./backup.tar.gz.age --data-dir /var/lib/mistgate-restored");
    expect(text()).not.toContain("tar czf");
    expect(text()).toContain(en["set.backups.configureFirst"]);
    expect(backupApi.listBackups).not.toHaveBeenCalled();
    expect(button(en["common.copy"])).toBeDefined();
  });

  describe("Create backup now", () => {
    const configured = {
      accountId: "cf-account", jurisdiction: "default", bucket: "mistgate-backups", accessKeyId: "access-id", hasSecret: true,
      ageRecipient: "age1example", enabled: true, intervalHours: 24, retentionDays: 0, lastSuccessUnix: 1_800_000_000, lastErrorCode: "", running: false,
    };
    async function create(after: Partial<typeof configured>) {
      backupApi.getBackupSettings.mockResolvedValueOnce({ settings: configured }).mockResolvedValue({ settings: { ...configured, ...after } });
      backupApi.createBackup.mockResolvedValue({});
      await mount(<BackupsPage />);
      await click(button(en["set.backups.create"]));
      for (let i = 0; i < 4; i++) await settle();
      expect(backupApi.createBackup).toHaveBeenCalledOnce();
    }

    it("says the backup started and keeps the button busy while the panel makes it", async () => {
      await create({ running: true });
      expect(text()).toContain(en["set.backups.started"]);
      expect(button(en["sx.bk.running"])?.getAttribute("aria-busy")).toBe("true");
      expect(text()).not.toContain(en["set.backups.created"]);
    });

    it("says it worked once the panel reports a new success", async () => {
      await create({ lastSuccessUnix: 1_800_000_100 });
      expect(text()).toContain(en["set.backups.created"]);
    });

    it("says why it failed when the panel reports the error instead", async () => {
      await create({ lastErrorCode: "backup_storage_failed" });
      expect(text()).toContain(en["set.backups.error.storage"]);
      expect(text()).not.toContain(en["set.backups.created"]);
    });
  });

  it("lists encrypted backups without returning the saved R2 secret", async () => {
    backupApi.getBackupSettings.mockResolvedValue({ settings: {
      accountId: "cf-account", jurisdiction: "eu", bucket: "mistgate-backups", accessKeyId: "access-id",
      hasSecret: true, ageRecipient: "age1example", enabled: true, intervalHours: 12, retentionDays: 30,
      lastSuccessUnix: 1_800_000_000, lastErrorCode: "",
    } });
    backupApi.listBackups.mockResolvedValue({ backups: [{ key: "mistgate/2026-10-03T00-00-00Z.tar.gz.age", sizeBytes: 4096, createdUnix: 1_800_000_000 }] });
    await mount(<BackupsPage />);
    expect([...document.querySelectorAll<HTMLInputElement>("input")].some((field) => field.value === "mistgate-backups")).toBe(true);
    expect(text()).toContain("mistgate/2026-10-03T00-00-00Z.tar.gz.age");
    expect(text()).toContain("mistgate-recovery.txt");
    expect(document.querySelector<HTMLInputElement>('input[type="password"]')?.value).toBe("");
    expect(backupApi.listBackups).toHaveBeenCalledOnce();
  });

  it("pages a long list of backups, ten to a page, and shows no pager for a short one", async () => {
    backupApi.getBackupSettings.mockResolvedValue({ settings: {
      accountId: "cf-account", jurisdiction: "eu", bucket: "mistgate-backups", accessKeyId: "access-id",
      hasSecret: true, ageRecipient: "age1example", enabled: true, intervalHours: 24, retentionDays: 0,
      lastSuccessUnix: 1_800_000_000, lastErrorCode: "",
    } });
    const day = (n: number) => ({ key: `mistgate/2026-09-${String(n).padStart(2, "0")}.tar.gz.age`, sizeBytes: 4096, createdUnix: 1_800_000_000 - n * 86400 });
    backupApi.listBackups.mockResolvedValue({ backups: Array.from({ length: 25 }, (_, i) => day(25 - i)) });
    await mount(<BackupsPage />);
    expect(text()).toContain("mistgate/2026-09-25.tar.gz.age");
    expect(text()).toContain("mistgate/2026-09-16.tar.gz.age");
    expect(text()).not.toContain("mistgate/2026-09-15.tar.gz.age");
    expect(text()).toContain("Showing 1–10 of 25");
    expect(document.querySelector("nav[aria-label='Recent backups']")).not.toBeNull();
  });
});

describe("Settings → Interface", () => {
  const stored = { brandHead: "mist", brandTail: "gate", accent: "#7dd3a0", language: "en", hasLogo: false, logoVersion: "", adminUrl: "", subscriptionBase: "" };
  beforeEach(() => {
    localStorage.clear();
    setAccent("#7dd3a0");
    getInstance.mockResolvedValue({ instance: stored });
    updateInstance.mockImplementation(async (r: Record<string, unknown>) => ({ instance: { ...stored, ...Object.fromEntries(Object.entries(r).filter(([, v]) => v !== undefined)) } }));
  });
  afterEach(() => {
    setEffectsEnabled(false);
    localStorage.clear();
  });
  const group = (label: string, n = 0) => document.querySelectorAll<HTMLElement>(`[role=radiogroup][aria-label="${label}"]`)[n]!;
  const pill = (g: HTMLElement, label: string) => [...g.querySelectorAll<HTMLElement>("[role=radio]")].find((b) => b.textContent === label)!;
  const field = (label: string) => document.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!;
  const save = () => button(en["common.save"]) as HTMLButtonElement | undefined;

  it("draws what the three screens will look like, then what is only this device's and what is for everyone", async () => {
    await mount(<InterfacePage />);
    for (const k of ["sx.if.pv.menu", "sx.if.pv.login", "sx.if.pv.friends", "settings.device", "sx.if.all", "sx.if.local"] as const) expect(text()).toContain(en[k]);
    expect(document.querySelectorAll(".sx-pv .sx-mock")).toHaveLength(3);
    // the preview wears the name that is typed, before it is saved
    await type(field(en["sx.if.name.a2"]), "gates");
    expect(document.querySelector(".sx-pv b")?.textContent).toBe("mistgates");
  });

  it("follows the panel's colour until a colour is picked, and Like the panel goes back to it", async () => {
    await mount(<InterfacePage />);
    const mine = group(en["settings.accent"]);
    expect(pill(mine, en["settings.accent.mint"]).getAttribute("aria-checked")).toBe("true");
    expect(pill(mine, en["settings.accent.follow"]).getAttribute("aria-checked")).toBe("false");
    await click(pill(mine, en["settings.accent.sky"]));
    expect(readPref("accent")).toBe("#8ec8ea");
    await click(pill(mine, en["settings.accent.follow"]));
    expect(readPref("accent")).toBeNull();
    expect(pill(mine, en["settings.accent.follow"]).getAttribute("aria-checked")).toBe("true");
  });

  it("takes a colour of its own as six hex digits, and says when they are not", async () => {
    await mount(<InterfacePage />);
    const hex = document.querySelector<HTMLInputElement>(`input[aria-label="${en["sx.if.acc.hex"]}"]`)!;
    await type(hex, "b8ac");
    expect(readPref("accent")).toBe("#7dd3a0");
    expect(hex.getAttribute("aria-invalid")).toBe("true");
    await type(hex, "b8acf2");
    expect(readPref("accent")).toBe("#b8acf2");
  });

  it("saves only what the owner changed, and says so", async () => {
    await mount(<InterfacePage />);
    expect(save()?.getAttribute("aria-disabled")).toBe("true");
    await type(field(en["sx.if.name.a2"]), "gates");
    expect(text()).toContain(en["sx.if.dirty"]);
    await submit(en["common.save"]);
    expect(updateInstance).toHaveBeenCalledTimes(1);
    expect(updateInstance.mock.calls[0]?.[0]).toEqual({ brandHead: undefined, brandTail: "gates", accent: undefined, language: undefined, logoSvg: undefined });
    expect(text()).toContain(en["sx.if.toast"]);
  });

  it("keeps the first part of the name from being empty or odd, and the Save button off", async () => {
    await mount(<InterfacePage />);
    await type(field(en["sx.if.name.a1"]), "");
    expect(text()).toContain(en["sx.if.name.empty"]);
    expect(save()?.getAttribute("aria-disabled")).toBe("true");
    await type(field(en["sx.if.name.a1"]), "mist<>");
    expect(text()).toContain(en["brand.nameError"]);
    await submit(en["common.save"]);
    expect(updateInstance).not.toHaveBeenCalled();
  });

  it("draws the card for everyone to a helper, read-only, with no Save and the reason", async () => {
    role = Role.HELPER;
    await mount(<InterfacePage />);
    expect(text()).toContain(en["sx.if.all"]);
    expect(document.querySelector<HTMLFieldSetElement>("fieldset")?.disabled).toBe(true);
    expect(save()).toBeUndefined();
    expect(text()).toContain(en["sx.if.ro"]);
  });

  it("refuses a logo over 64 KB with the reason, and one that is not an SVG", async () => {
    await mount(<InterfacePage />);
    const file = document.querySelector<HTMLInputElement>("input[type=file]")!;
    const choose = async (f: File) => {
      Object.defineProperty(file, "files", { configurable: true, value: [f] });
      await act(async () => void file.dispatchEvent(new Event("change", { bubbles: true })));
      await settle();
    };
    await choose(new File([new Uint8Array(70 * 1024)], "big.svg"));
    expect(document.querySelector("[role=alert]")?.textContent).toContain(en["sx.if.logo.big"]);
    expect(document.querySelector("[role=alert]")?.textContent).toContain(en["sx.if.logo.big.body"]);
    await choose(Object.assign(new File(["x"], "bad.svg"), { text: async () => "not svg" }));
    expect(document.querySelector("[role=alert]")?.textContent).toContain(en["brand.logoInvalid"]);
    expect(save()?.getAttribute("aria-disabled")).toBe("true");
  });

  it("takes a logo, shows it in the preview at once and sends it when saved", async () => {
    await mount(<InterfacePage />);
    const file = document.querySelector<HTMLInputElement>("input[type=file]")!;
    const svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><circle cx="4" cy="4" r="3" fill="#ffffff"/></svg>';
    Object.defineProperty(file, "files", { configurable: true, value: [Object.assign(new File([svg], "logo.svg"), { text: async () => svg })] });
    await act(async () => void file.dispatchEvent(new Event("change", { bubbles: true })));
    await settle();
    expect(text()).toContain(en["sx.if.logo.new"]);
    expect(document.querySelector(".sx-pv .sx-mk svg circle")).not.toBeNull();
    await submit(en["common.save"]);
    expect(updateInstance.mock.calls[0]?.[0]).toMatchObject({ logoSvg: svg });
  });

  it("puts the moments of the reveal animation under its switch once it is on, and draws none on the Cloudflare edition", async () => {
    await mount(<InterfacePage />);
    expect(text()).not.toContain(en["settings.fx.create"]);
    await click(document.querySelector<HTMLElement>(`[role=switch][aria-label="${en["settings.fx"]}"]`));
    for (const k of ["settings.fx.create", "settings.fx.save", "settings.fx.pages"] as const) expect(text()).toContain(en[k]);
    await click(document.querySelector<HTMLElement>(`[role=switch][aria-label="${en["settings.fx.save"]}"]`));
    expect(readPref("fx-save")).toBe("off");
    await act(async () => root!.unmount());
    host!.remove();
    auth.me.mockImplementation(async () => ({ admin: { id: "adm_1", displayName: "Alice", role }, edition: "edge", unavailable: [] }));
    await mount(<InterfacePage />);
    expect(text()).not.toContain(en["settings.fx"]);
  });
});
