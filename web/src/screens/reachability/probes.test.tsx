import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { dictionaries, langs, setLang, type Lang } from "@/i18n";
import { en } from "@/i18n/en";
import { meQuery } from "@/lib/session";
import { ProbesCard } from "./sources";

const get = vi.fn();
const update = vi.fn();
const create = vi.fn();
const revoke = vi.fn();
const confirmNetwork = vi.fn();
const checkNow = vi.fn();
const updateCommand = vi.fn();
const disconnect = vi.fn();
const getChecks = vi.fn();
let me: Record<string, unknown> = {};
vi.mock("@/lib/api", () => ({
  ownProbe: {
    get: (...a: unknown[]) => get(...a),
    update: (...a: unknown[]) => update(...a),
    create: (...a: unknown[]) => create(...a),
    revoke: (...a: unknown[]) => revoke(...a),
    confirmNetwork: (...a: unknown[]) => confirmNetwork(...a),
    checkNow: (...a: unknown[]) => checkNow(...a),
    updateCommand: (...a: unknown[]) => updateCommand(...a),
    disconnect: (...a: unknown[]) => disconnect(...a),
  },
  health: { getChecks: (...a: unknown[]) => getChecks(...a) },
  auth: { me: () => Promise.resolve(me) },
  isUnauthenticated: () => false,
  webauthnSupported: () => false,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const NOW = Math.floor(Date.now() / 1000);
const min = 60;
const settings = (over: object = {}) => ({ enabled: true, intervalMin: 60, fallbackUrl: "", ...over });
const probe = (over: object = {}) => ({
  id: "prb_0123456789abcdef01234567", name: "kitchen", city: "Springfield", kind: "home", state: "active", asn: 64496, operator: "ExampleNet", pinnedOperator: "ExampleNet",
  version: "0a1b2c3d4e5f", lastSeenUnix: NOW - 4 * min, lastResultUnix: NOW - 12 * min, checksDay: 24, tokenExpiresUnix: 0, reference: "filtered", ...over,
});
const office = probe({ id: "prb_89abcdef0123456789abcdef", name: "office", city: "Shelbyville", kind: "dc", operator: "Demo Telecom", pinnedOperator: "Demo Telecom", asn: 64497 });
const waiting = probe({ id: "prb_aaaaaaaaaaaaaaaaaaaaaaaa", state: "pending", operator: "", pinnedOperator: "", asn: 0, version: "", lastSeenUnix: 0, lastResultUnix: 0, checksDay: 0, tokenExpiresUnix: NOW + 22 * 3600 });
const COMMAND = "opkg update && cd /tmp && sh mgp.sh install 'https://panel.example.com/mgp-q2w3e4r5t6y7u8i9o0p1a2s3d4' 'mgp_q2w3e4r5t6y7u8i9o0p1a2s3d4f5g6h7j8k9l0z1x2c3v4b5'";
const SERVER_COMMAND = "f=$(mktemp) && sudo sh \"$f\" install 'https://panel.example.com/mgp-q2w3e4r5t6y7u8i9o0p1a2s3d4' 'mgp_q2w3e4r5t6y7u8i9o0p1a2s3d4f5g6h7j8k9l0z1x2c3v4b5'";
const UPDATE = "cd /tmp && echo '0a1b2c3d4e5f  mgp.sh' | sha256sum -c - && sh mgp.sh upgrade";
const SERVER_UPDATE = "f=$(mktemp) && echo \"0a1b2c3d4e5f  $f\" | sha256sum -c - && sudo sh \"$f\" upgrade";
const VERSION = "0a1b2c3d4e5f";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let qc: QueryClient;
beforeEach(() => {
  me = { admin: { id: "adm_1", role: 1 }, edition: "vps", unavailable: [] };
  get.mockResolvedValue({ settings: settings(), probes: [], scriptVersion: VERSION });
  getChecks.mockResolvedValue({ nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks: [] });
  update.mockResolvedValue({});
  revoke.mockResolvedValue({});
  disconnect.mockResolvedValue({});
  confirmNetwork.mockResolvedValue({});
  checkNow.mockResolvedValue({});
  create.mockResolvedValue({ probe: waiting, token: "mgp_secret", command: COMMAND, serverCommand: SERVER_COMMAND, scriptSha256: VERSION.padEnd(64, "0") });
  updateCommand.mockResolvedValue({ command: UPDATE, serverCommand: SERVER_UPDATE, scriptVersion: VERSION });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  act(() => setLang("en"));
  for (const m of [get, update, create, revoke, confirmNetwork, checkNow, disconnect, getChecks, updateCommand]) m.mockReset();
});

async function mount(data: { settings?: object; probes?: object[]; scriptVersion?: string } = {}, before: (qc: QueryClient) => void = () => {}) {
  get.mockResolvedValue({ settings: data.settings ?? settings(), probes: data.probes ?? [], scriptVersion: data.scriptVersion ?? VERSION });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  // the session is known by the time a screen of the app draws (the route guard asks for it)
  qc.setQueryData(meQuery.queryKey, me as never);
  before(qc);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <ProbesCard />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
/** The state line of the card as it reads: the source's own word, then its last line. */
const state = () => {
  const el = document.querySelector<HTMLElement>(".rc-last[data-state]");
  return el ? { dataset: { state: el.dataset.state! }, textContent: `${el.dataset.word} · ${el.textContent}` } : undefined;
};
/** What switches the source: "Switch on" while it is off, "Pause" while it is on. */
const ONOFF = "[data-act=enable],[data-act=pause]";
const rows = () => [...document.querySelectorAll<HTMLElement>("[data-testid=probe]")];
const row = (name: string) => rows().find((r) => r.textContent?.startsWith(name))!;
const buttons = (scope: ParentNode = document) => [...scope.querySelectorAll("button")].map((b) => b.textContent?.trim());
const button = (label: string, scope: ParentNode = document) => [...scope.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");
const click = async (b: Element | undefined | null) => {
  await act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 3; i++) await settle();
};
function type(input: Element | null, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  return act(async () => {
    set.call(input, value);
    input?.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("the Own probes card: its state line", () => {
  it("is off by default: the words say nothing is asked, the probes are not listed, nothing to add", async () => {
    await mount({ settings: settings({ enabled: false }), probes: [probe()] });
    expect(state()?.dataset.state).toBe("off");
    expect(state()?.textContent).toBe("Off · nothing is asked of the probes; the ones you installed stay and wake up when you switch it on");
    expect(rows()).toHaveLength(0);
    expect(buttons()).not.toContain("Add a probe");
    expect(document.querySelector<HTMLElement>(ONOFF)?.dataset.act).toBe("enable");
  });

  it("says there is no probe yet when it is on and offers the first one", async () => {
    await mount();
    expect(state()?.dataset.state).toBe("none");
    expect(state()?.textContent).toBe("On · no probes yet: add the first one");
    expect(buttons()).toContain("Add a probe");
  });

  it("waits for the install while every probe waits for its command", async () => {
    await mount({ probes: [waiting] });
    expect(state()?.dataset.state).toBe("pending");
    expect(state()?.textContent).toContain("Waiting for install");
    expect(rows()[0]?.dataset.look).toBe("pending");
    // its only action is to revoke; the command cannot be shown again, and the row says so
    expect(buttons(rows()[0])).toEqual(["Revoke"]);
    expect(rows()[0]?.textContent).toContain("cannot be shown a second time");
  });

  it("counts the working ones, N of M, with the newest check; a revoked one is listed apart and counts for nothing", async () => {
    await mount({ probes: [probe(), office, probe({ id: "prb_dead", name: "old", state: "revoked" })] });
    expect(state()?.dataset.state).toBe("works");
    expect(state()?.textContent).toBe("2 of 2 working · last check 12 min ago");
    expect(rows().map((r) => r.dataset.look)).toEqual(["online", "online", "revoked"]);
    // no actions on a revoked probe
    expect(buttons(row("old"))).toEqual([]);
    expect(row("kitchen").textContent).toContain("ExampleNet · Springfield");
    expect(row("kitchen").textContent).toContain("in touch 4 min ago");
    expect(row("kitchen").textContent).toContain("24 checks in 24 h");
    expect(row("office").textContent).toContain("Data centre");
  });

  it("says no contact when the probes that ought to work are silent, and how long", async () => {
    await mount({ probes: [probe({ lastSeenUnix: NOW - 7 * 3600 })] });
    expect(state()?.dataset.state).toBe("offline");
    expect(state()?.textContent).toBe("No contact · no probe has asked the panel for 7 h");
    expect(row("kitchen").textContent).toContain("Not heard from for 7 h");
  });

  it("holds a probe whose network changed for the owner: its words, its Confirm button, and it does not count as working", async () => {
    await mount({ probes: [probe({ state: "network_changed", operator: "Sample Mobile", asn: 64498 })] });
    expect(state()?.dataset.state).toBe("netChanged");
    const r = row("kitchen");
    expect(r.dataset.look).toBe("netChanged");
    expect(r.textContent).toContain("Network changed");
    expect(r.textContent).toContain("It now asks the panel from Sample Mobile; before that it was ExampleNet.");
    await click(button("Confirm network", r));
    expect(confirmNetwork).toHaveBeenCalledWith({ id: "prb_0123456789abcdef01234567" });
  });

  it("says that a probe sees no filtering on its path from what the probe itself reports, before any point is in the checks, and why that is the right state outside the country", async () => {
    getChecks.mockResolvedValue({ nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks: [] });
    await mount({ probes: [probe({ reference: "open" })] });
    expect(row("kitchen").dataset.look).toBe("noFilter");
    expect(row("kitchen").textContent).toContain("No filtering on its path");
    expect(row("kitchen").textContent).toContain("A probe outside the country does not see blocks");
    expect(state()?.textContent).toBe("1 of 1 working · last check 12 min ago · 1 probe sees no filtering");
    // the card does not read the checks for it any more
    expect(getChecks).not.toHaveBeenCalled();
  });

  it("is an ordinary working probe while its reference is blocked or no round has finished; one that could not try it says its points prove nothing", async () => {
    await mount({ probes: [probe({ reference: "filtered" }), probe({ ...office, reference: "" }), probe({ id: "prb_b", name: "balcony", reference: "unavailable" })] });
    expect(rows().map((r) => r.dataset.look)).toEqual(["online", "online", "online"]);
    expect(row("kitchen").textContent).not.toContain("reference address");
    expect(row("office").textContent).not.toContain("reference address");
    expect(row("balcony").textContent).toContain("The reference address could not be tried");
    expect(row("balcony").textContent).toContain("ref=off");
  });

  it("marks a probe Outdated when the panel's script is newer, and only that probe", async () => {
    await mount({ probes: [probe({ version: "111111111111" }), probe({ ...office, version: VERSION })] });
    expect(row("kitchen").querySelector("[data-testid=probe-outdated]")?.textContent).toBe("Outdated");
    expect(row("office").querySelector("[data-testid=probe-outdated]")).toBeNull();
    expect(document.querySelectorAll("[data-testid=probe-outdated]")).toHaveLength(1);
  });

  it("marks it after a reload as well: the panel's list says the version, no command was made in this session", async () => {
    await mount({ probes: [probe({ version: "111111111111" })] });
    expect(create).not.toHaveBeenCalled();
    expect(document.querySelectorAll("[data-testid=probe-outdated]")).toHaveLength(1);
    // the panel's script changes under the open page: the next list says the new version and the pill follows
    get.mockResolvedValue({ settings: settings(), probes: [probe({ version: VERSION })], scriptVersion: VERSION });
    await act(async () => void (await qc.invalidateQueries({ queryKey: ["ownProbe"] })));
    for (let i = 0; i < 3; i++) await settle();
    expect(document.querySelector("[data-testid=probe-outdated]")).toBeNull();
  });

  it("does not mark a probe that waits for its command or one that is revoked: they have no script yet", async () => {
    await mount({ probes: [waiting, probe({ id: "prb_dead", name: "old", state: "revoked", version: "111111111111" })] });
    expect(document.querySelector("[data-testid=probe-outdated]")).toBeNull();
  });

  it("does not guess: with no script version in the list, nobody is Outdated", async () => {
    await mount({ probes: [probe({ version: "111111111111" })], scriptVersion: "" });
    expect(document.querySelector("[data-testid=probe-outdated]")).toBeNull();
  });
});

describe("the Own probes card: what it asks and says before it acts", () => {
  it("asks 'check now' without a window and says it takes up to 15 minutes", async () => {
    await mount({ probes: [probe()] });
    await click(button("Check now"));
    expect(checkNow).toHaveBeenCalledWith({ id: "prb_0123456789abcdef01234567" });
    expect(row("kitchen").textContent).toContain("The probe picks it up within 15 minutes.");
  });

  it("a probe that has been silent longer than the card calls 'no contact' takes the job when it is back: the note says since when, and where the restart is", async () => {
    await mount({ probes: [probe({ lastSeenUnix: NOW - 8 * 3600 })] });
    expect(row("kitchen").dataset.look).toBe("offline");
    await click(button("Check now"));
    expect(checkNow).toHaveBeenCalledWith({ id: "prb_0123456789abcdef01234567" });
    const note = row("kitchen").querySelector("[data-testid=probe-asked]")!.textContent!;
    expect(note).toMatch(/^The probe has not been in touch since .+; it will take the job when it is back\. The update command, run on the router or server, restarts it\.$/);
    expect(note).not.toContain("within 15 minutes");
  });

  it("a probe that is heard from keeps the old promise, however long its last result is", async () => {
    await mount({ probes: [probe({ lastSeenUnix: NOW - 59 * min, lastResultUnix: NOW - 9 * 3600 })] });
    expect(row("kitchen").dataset.look).toBe("online");
    await click(button("Check now"));
    expect(row("kitchen").querySelector("[data-testid=probe-asked]")?.textContent).toBe("The probe picks it up within 15 minutes.");
  });

  it("the tooltip of the state has the script version, that it is outdated, and the last sync", async () => {
    await mount({ probes: [probe({ version: "111111111111", lastSeenUnix: NOW - 8 * 3600 }), office] });
    const tip = (name: string) => row(name).querySelector("[data-testid=probe-state]")!.getAttribute("title")!;
    expect(tip("kitchen")).toMatch(/^Probe script: version 111111111111, outdated, the panel has a newer one\nLast sync: .+$/);
    expect(tip("office")).toMatch(/^Probe script: version 0a1b2c3d4e5f\nLast sync: .+$/);
    expect(tip("office")).not.toContain("outdated");
  });

  it("no tooltip for a probe that has not synced yet, and none for a revoked one", async () => {
    await mount({ probes: [waiting, probe({ id: "prb_dead", name: "old", state: "revoked" })] });
    for (const r of rows()) expect(r.querySelector("[data-testid=probe-state]")?.hasAttribute("title")).toBe(false);
  });

  it.each(langs)("%s: the note of a silent probe and the tooltip say it in the language, with nothing left to fill", async (lang: Lang) => {
    setLang(lang);
    await mount({ probes: [probe({ lastSeenUnix: NOW - 8 * 3600, version: "111111111111" })] });
    await click(document.querySelector<HTMLButtonElement>("[data-testid=probe] .hl-act button")!);
    const note = row("kitchen").querySelector("[data-testid=probe-asked]")!.textContent!;
    expect(note.length).toBeGreaterThan(30);
    expect(note).not.toMatch(/[{}]/);
    expect(note).not.toBe(dictionaries[lang]["pr.checkNow.note"]);
    expect(row("kitchen").querySelector("[data-testid=probe-state]")!.getAttribute("title")).not.toMatch(/[{}]/);
    setLang("en");
  });

  it("asks before it revokes, says it cannot be undone, and revokes only on the second click", async () => {
    await mount({ probes: [probe()] });
    await click(button("Revoke", row("kitchen")));
    expect(revoke).not.toHaveBeenCalled();
    expect(dialog()?.textContent).toContain("Revoke the probe “kitchen”?");
    expect(dialog()?.textContent).toContain("This cannot be undone");
    // how to take the script off the device is in the window
    expect(dialog()?.textContent).toContain("sh /opt/sbin/mistgate-probe uninstall");
    await click(button("Cancel", dialog()!));
    expect(dialog()).toBeNull();
    expect(revoke).not.toHaveBeenCalled();
    await click(button("Revoke", row("kitchen")));
    await click(button("Revoke", dialog()!));
    expect(revoke).toHaveBeenCalledWith({ id: "prb_0123456789abcdef01234567" });
  });

  it("asks before it disconnects: nothing is sent until the confirmation, and the probes are not deleted", async () => {
    await mount({ probes: [probe()] });
    await click(button("Disconnect"));
    expect(disconnect).not.toHaveBeenCalled();
    expect(dialog()?.textContent).toContain("Disconnect the own probes?");
    expect(dialog()?.textContent).toContain("They are not deleted");
    await click(button("Disconnect", dialog()!));
    expect(disconnect).toHaveBeenCalledTimes(1);
  });

  it("switches with the settings it already has, the interval is 30 to 240 and the spare address is checked", async () => {
    await mount({ settings: settings({ enabled: false, intervalMin: 90, fallbackUrl: "https://spare.example.com" }) });
    await click(document.querySelector(ONOFF));
    expect(update).toHaveBeenCalledWith({ settings: { enabled: true, intervalMin: 90, fallbackUrl: "https://spare.example.com" } });
  });

  it("refuses a spare address that is not https:// before it asks the panel, and saves one that is", async () => {
    await mount();
    const field = document.querySelector<HTMLInputElement>("input[type=url]");
    await type(field, "http://insecure.example.com");
    await click(button("Save"));
    expect(update).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("Enter an address that starts with https://, or leave it empty.");
    await type(field, "https://spare.example.com");
    await click(button("Save"));
    expect(update).toHaveBeenCalledWith({ settings: { enabled: true, intervalMin: 60, fallbackUrl: "https://spare.example.com" } });
  });

  it("is off with its reason, and without a switch, in the Cloudflare edition: nothing is asked of the panel", async () => {
    me = { admin: { id: "adm_1", role: 1 }, edition: "edge", unavailable: ["own_probes"] };
    await mount();
    expect(get).not.toHaveBeenCalled();
    expect(state()?.dataset.state).toBe("unavailable");
    expect(state()?.textContent).toBe("Not available · not in the Cloudflare edition yet; nothing is sent");
    expect(document.querySelector(ONOFF)).toBeNull();
    expect(buttons()).toEqual([]);
  });
});

describe("the Add window and its one-time command", () => {
  async function fillAndCreate() {
    await click(button("Add a probe"));
    const inputs = dialog()!.querySelectorAll("input");
    await type(inputs[0]!, "kitchen");
    await type(inputs[1]!, "Springfield");
    await click(button("Create the command", dialog()!));
  }

  it("asks for the name and the city first, and does not call the panel until both are there", async () => {
    await mount();
    await click(button("Add a probe"));
    await click(button("Create the command", dialog()!));
    expect(create).not.toHaveBeenCalled();
    expect(dialog()?.textContent).toContain("Give it a name, up to 80 characters.");
    expect(dialog()?.textContent).toContain("Give the city, up to 80 characters.");
    expect(dialog()?.textContent).toContain("Home");
    expect(dialog()?.textContent).toContain("Mobile");
    expect(dialog()?.textContent).toContain("Data centre");
  });

  it("shows the command big, one-time for 24 hours, the three steps, what the provider sees and the docs link; and says it cannot be shown again", async () => {
    await mount();
    await fillAndCreate();
    expect(create).toHaveBeenCalledWith({ name: "kitchen", city: "Springfield", kind: "home" });
    const d = dialog()!;
    expect(d.querySelector("[data-testid=probe-command]")?.textContent).toBe(COMMAND);
    expect(d.textContent).toContain("One-time command, valid for 24 hours.");
    expect(d.textContent).toContain("it cannot be shown again");
    expect([...d.querySelectorAll("ol > li")].map((li) => li.querySelector("span.text-sm")?.textContent)).toEqual(["Open SSH on the router", "Paste the command and press Enter", "Wait for “connected” here"]);
    expect(d.textContent).not.toContain("cut everything");
    expect(d.textContent).not.toContain("cd /tmp”");
    expect(buttons(d)).toContain("Copy the command");
    expect(buttons(d)).toContain("Did not copy it? Create a new one");
    expect(d.querySelector("[data-testid=probe-see]")?.textContent).toContain("What this person's provider sees");
    expect(d.querySelector("[data-testid=probe-see] li")?.textContent).toContain("About once an hour");
    expect(d.querySelector("a[href='https://mistgate.app/operations/probes/']")?.getAttribute("target")).toBe("_blank");
    // the token travels inside the command only: the window has no other copy of it
    expect(d.textContent).not.toContain("mgp_secret");
  });

  it("shows the command of the system the probe is for, a router first for a home probe, and the server's one on the other side of the switch", async () => {
    await mount();
    await fillAndCreate();
    const d = () => dialog()!;
    const shown = () => d().querySelector("[data-testid=probe-command]")?.textContent;
    const choice = (label: string) => [...d().querySelectorAll("[role=radio],button")].find((b) => b.textContent?.trim() === label);
    expect(shown()).toBe(COMMAND);
    expect(d().textContent).toContain("Open SSH on the router");
    expect(d().textContent).toContain("For a Keenetic router with Entware");
    await click(choice("Server (VPS)"));
    expect(shown()).toBe(SERVER_COMMAND);
    expect(shown()).not.toContain("opkg");
    expect(d().textContent).toContain("Open SSH on the server");
    expect(d().textContent).toContain("Run it as a user with sudo");
    await click(choice("Keenetic (Entware)"));
    expect(shown()).toBe(COMMAND);
  });

  it("opens on the server's command for a data centre probe", async () => {
    await mount();
    create.mockResolvedValueOnce({ probe: { ...waiting, kind: "dc" }, token: "mgp_secret", command: COMMAND, serverCommand: SERVER_COMMAND, scriptSha256: VERSION.padEnd(64, "0") });
    await click(button("Add a probe"));
    const inputs = dialog()!.querySelectorAll("input");
    await type(inputs[0]!, "office");
    await type(inputs[1]!, "Shelbyville");
    await click([...dialog()!.querySelectorAll("[role=radio],button")].find((b) => b.textContent?.trim() === "Data centre"));
    await click(button("Create the command", dialog()!));
    expect(create).toHaveBeenCalledWith({ name: "office", city: "Shelbyville", kind: "dc" });
    expect(dialog()?.querySelector("[data-testid=probe-command]")?.textContent).toBe(SERVER_COMMAND);
  });

  it("keeps the command in the window and nowhere else: closed, it is gone from the page, the query cache, the mutation cache and both storages", async () => {
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    await mount();
    await fillAndCreate();
    expect(document.body.textContent).toContain("mgp_q2w3e4r5t6y7");
    await click(button("Done", dialog()!));
    expect(dialog()).toBeNull();
    expect(document.body.textContent).not.toContain("mgp_");
    expect(document.body.textContent).not.toContain("sh mgp.sh install");
    const cached = JSON.stringify(qc.getQueryCache().getAll().map((q) => q.state.data));
    expect(cached).not.toContain("mgp_");
    expect(cached).not.toContain("opkg");
    expect(qc.getMutationCache().getAll().map((m) => JSON.stringify(m.state.data ?? null)).join("")).not.toContain("mgp_");
    for (const [key, value] of setItem.mock.calls) expect(`${key}=${value}`).not.toContain("mgp_");
    for (const store of [localStorage, sessionStorage]) for (let i = 0; i < store.length; i++) expect(store.getItem(store.key(i)!)).not.toContain("mgp_");
    setItem.mockRestore();
  });

  it("opens empty the next time: no command of the last one, no old name", async () => {
    await mount();
    await fillAndCreate();
    await click(button("Done", dialog()!));
    await click(button("Add a probe"));
    expect(dialog()?.querySelector("[data-testid=probe-command]")).toBeNull();
    expect(dialog()?.querySelector("input")?.value).toBe("");
  });

  it("makes the same probe again with a new command when it was not copied: the waiting one is revoked first", async () => {
    await mount();
    await fillAndCreate();
    create.mockResolvedValueOnce({ probe: { ...waiting, id: "prb_bbbbbbbbbbbbbbbbbbbbbbbb" }, token: "mgp_second", command: COMMAND.replace("mgp_q2w3", "mgp_n3w2"), serverCommand: SERVER_COMMAND, scriptSha256: VERSION.padEnd(64, "0") });
    await click(button("Did not copy it? Create a new one", dialog()!));
    expect(revoke).toHaveBeenCalledWith({ id: "prb_aaaaaaaaaaaaaaaaaaaaaaaa" });
    expect(create).toHaveBeenCalledTimes(2);
    expect(create).toHaveBeenLastCalledWith({ name: "kitchen", city: "Springfield", kind: "home" });
    expect(dialog()?.querySelector("[data-testid=probe-command]")?.textContent).toContain("mgp_n3w2");
  });

  it("says that the probe is connected in the third step, as soon as the panel lists it as heard from", async () => {
    await mount();
    await fillAndCreate();
    expect(dialog()?.textContent).toContain("Waiting for the probe to reach the panel for the first time.");
    get.mockResolvedValue({ settings: settings(), probes: [probe({ id: "prb_aaaaaaaaaaaaaaaaaaaaaaaa" })], scriptVersion: VERSION });
    await act(async () => void (await qc.invalidateQueries({ queryKey: ["ownProbe"] })));
    for (let i = 0; i < 3; i++) await settle();
    expect(dialog()?.textContent).toContain("ConnectedExampleNet · Springfield. The first check starts on its own.");
  });

  it("names the missing public address in words when the panel cannot build a command", async () => {
    const { ConnectError, Code } = await import("@connectrpc/connect");
    await mount();
    create.mockRejectedValueOnce(new ConnectError("public HTTPS URL is required to build an install command", Code.FailedPrecondition));
    await fillAndCreate();
    expect(document.body.textContent).toContain("The command needs the panel's public HTTPS address. Set it in Settings first.");
    expect(dialog()?.querySelector("[data-testid=probe-command]")).toBeNull();
  });
});

describe("the update command", () => {
  const command = () => dialog()?.querySelector("[data-testid=update-command]")?.textContent ?? "";
  const choice = (label: string) => [...dialog()!.querySelectorAll("[role=radio],button")].find((b) => b.textContent?.trim() === label);

  it("is the panel's: asked of it when the window opens, for the system the probe stands on (a router for a home probe, a server for a data centre one), and made nowhere else", async () => {
    await mount({ probes: [probe(), office] });
    expect(updateCommand).not.toHaveBeenCalled();
    await click(button("Update command", row("kitchen")));
    expect(updateCommand).toHaveBeenCalledTimes(1);
    expect(command()).toBe(UPDATE);
    expect(command()).not.toContain("mgp_");
    // nothing of the old self-made line: no reading of the probe's configuration, no download that skips the hash
    expect(command()).not.toContain("sed -n");
    expect(command()).not.toContain("/conf");
    await click(choice("Server (VPS)"));
    expect(command()).toBe(SERVER_UPDATE);
    await click(button("Close", dialog()!));
    await click(button("Update command", row("office")));
    expect(command()).toBe(SERVER_UPDATE);
  });

  it("says why when the panel cannot build it, and tries again on a click", async () => {
    const { ConnectError, Code } = await import("@connectrpc/connect");
    updateCommand.mockRejectedValueOnce(new ConnectError("public HTTPS URL is required to build an install command", Code.FailedPrecondition));
    await mount({ probes: [probe()] });
    await click(button("Update command", row("kitchen")));
    expect(command()).toBe("");
    expect(dialog()?.textContent).toContain("The command needs the panel's public HTTPS address. Set it in Settings first.");
    await click(button("Try again", dialog()!));
    expect(command()).toBe(UPDATE);
  });
});

describe("in the other languages", () => {
  it("speaks Russian: the state line, the pill and the actions", async () => {
    setLang("ru");
    await mount({ probes: [probe(), office] });
    expect(state()?.textContent).toBe("Работает 2 из 2 · последняя проверка 12 мин назад");
    expect(row("kitchen").textContent).toContain("Дом");
    expect(row("kitchen").textContent).toContain("на связи 4 мин назад");
    expect(buttons(row("kitchen"))).toEqual(["Проверить сейчас", "Команда обновления", "Отозвать"]);
    expect(en["pr.title"]).toBe("Own probes");
  });
});
