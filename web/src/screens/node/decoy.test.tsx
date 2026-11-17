import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { editionFeatures } from "@/i18n/edition";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { setLang } from "@/i18n";
import { maxBundleBytes } from "@/lib/decoy";
import { sitesQuery } from "@/lib/decoysites";
import { meQuery } from "@/lib/session";
import { DecoyCard } from "./decoy";

// Node -> Server -> "Decoy site": the three modes, the library's choice for "Own site" (a dry run first, then the assignment), the
// archive dropped on the zone (a site of the library, then this node's), what the node has delivered and why not yet, and the
// Cloudflare edition without the library.

const setNodeDecoyMode = vi.fn();
const assignDecoySite = vi.fn();
const createDecoySite = vi.fn();
const listDecoySites = vi.fn();
vi.mock("@/lib/api", () => ({
  nodes: { setNodeDecoyMode: (...a: unknown[]) => setNodeDecoyMode(...a) },
  decoySites: {
    assignDecoySite: (...a: unknown[]) => assignDecoySite(...a),
    createDecoySite: (...a: unknown[]) => createDecoySite(...a),
    listDecoySites: (...a: unknown[]) => listDecoySites(...a),
    getDecoyPagePreview: () => Promise.resolve({ html: "<p>x</p>", path: "" }),
  },
  auth: { me: () => Promise.resolve({}) },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, className }: { children?: ReactNode; to: string; params?: Record<string, string>; className?: string }) => (
    <a href={params ? to.replace("$id", params.id!) : to} className={className}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let qc: QueryClient;
beforeEach(() => {
  setLang("en");
  listDecoySites.mockImplementation(async () => ({ sites: library, builtin: [], siteLimit: 100, versionLimit: 5 }));
  // the panel's answer to a dry run: what the assignment would warn about
  assignDecoySite.mockImplementation(async (r: { dryRun: boolean }) => (r.dryRun ? { warnings: [], replaced: [], nodes: [], applied: false } : { warnings: [], replaced: [], nodes: [], applied: true }));
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  setNodeDecoyMode.mockReset();
  assignDecoySite.mockReset();
  createDecoySite.mockReset();
});

const check = (code: string, level = "required", ok = true, paths: string[] = []) => ({ code, level, ok, paths });
const site = (id: string, name: string, over: Record<string, unknown> = {}) => ({
  id: `dsi_${id}`, name, note: `theme ${id}`, source: "upload", sourceLabel: "owner", state: "ready", bundleSha256: id.padEnd(64, "0"), createdUnix: 1, createdBy: "owner", updatedUnix: 2, updatedBy: "owner", version: 1,
  sizeBytes: 9000, fileCount: 3, title: name, checks: [check("index_html"), check("size"), check("file_count"), check("safe_paths")], nodeCount: 0, onPanel: false, ...over,
});
const library = [site("bk", "Bakery", { nodeCount: 1 }), site("ph", "Studio"), site("dr", "Draft one", { state: "draft" })];
const node = (over: Record<string, unknown> = {}) => ({
  id: "nod_1",
  name: "de1",
  decoyMode: "builtin",
  decoySiteId: "",
  decoySupported: true,
  decoyReasonCode: "",
  decoyBundleSha256: "",
  decoyPanelAvailable: true,
  decoyOwnAvailable: true,
  decoyAgentMode: "builtin",
  decoyAgentSha256: "",
  ...over,
});
const onBakery = (over: Record<string, unknown> = {}) => node({ decoyMode: "own", decoySiteId: "dsi_bk", decoyAgentMode: "own", ...over });

async function mount(n = node(), me: Record<string, unknown> = { admin: { id: "adm_1", role: Role.OWNER } }, lib: unknown = { sites: library, builtin: [], siteLimit: 100, versionLimit: 5 }) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(meQuery.queryKey, me as never);
  qc.setQueryData(sitesQuery.queryKey, lib as never);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <DecoyCard node={n as never} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
// a mode is a radio button of the first group, in the order of the modes; a library site is one of the second
const modeOrder = ["builtin", "panel", "own"];
const modeRadio = (mode: string) => document.querySelectorAll<HTMLButtonElement>(".sv-opts [role=radio]")[modeOrder.indexOf(mode)]!;
const siteRadio = (name: string) => [...document.querySelectorAll<HTMLButtonElement>(".dz-pkl [role=radio]")].find((r) => r.textContent?.includes(name))!;
const click = async (el: Element | null | undefined) => {
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await settle();
};
async function pickFile(file: File) {
  const input = document.querySelector<HTMLInputElement>("input[type=file]")!;
  Object.defineProperty(input, "files", { value: [file], configurable: true });
  await act(async () => void input.dispatchEvent(new Event("change", { bubbles: true })));
  await settle();
}
const archive = (name = "site.zip", bytes = 3) => new File([new Uint8Array(bytes)], name);
const disabled = (el?: Element) => !!el && (el.hasAttribute("data-disabled") || el.hasAttribute("disabled"));
const bar = () => document.querySelector(".sv-save")!;
const apply = () => button(en["decoy.apply"]);

describe("choosing a mode", () => {
  it("says what each of the three is, one line each", async () => {
    await mount();
    for (const k of ["decoy.mode.builtinHint", "decoy.mode.panelHint", "decoy.mode.ownHint"] as const) expect(text()).toContain(en[k]);
    expect(modeRadio("builtin").getAttribute("aria-checked")).toBe("true");
  });

  it("sends the picked mode for this node, and offers no library while the built-in page or the panel's is picked", async () => {
    setNodeDecoyMode.mockResolvedValue({ node: node({ decoyMode: "panel" }) });
    await mount();
    expect(disabled(apply())).toBe(true); // nothing changed yet
    await click(modeRadio("panel"));
    expect(document.querySelector(".dz-pk")).toBeNull();
    await click(apply());
    expect(setNodeDecoyMode).toHaveBeenCalledWith({ nodeId: "nod_1", mode: "panel" });
    expect(assignDecoySite).not.toHaveBeenCalled();
  });

  it("says in the bar that the choice is not applied yet, and who is on the node now", async () => {
    await mount();
    expect(bar().textContent).toContain(en["decoy.state.ok"]);
    await click(modeRadio("panel"));
    expect(bar().textContent).toContain(en["np.sv.site.pending"]);
    expect(document.querySelector(".sv-opts")!.textContent).toContain(en["np.sv.site.live"]); // the built-in page is what the node reports
  });

  it("offers the panel's site only when the panel has one", async () => {
    await mount(node({ decoyPanelAvailable: false }));
    expect(modeRadio("panel").disabled).toBe(true);
    expect(text()).toContain(en["decoy.panelNone"]);
  });

  it("is read-only for a helper", async () => {
    await mount(node(), { admin: { id: "adm_2", role: Role.HELPER } });
    expect(modeRadio("panel").disabled).toBe(true);
    expect(text()).toContain(en["decoy.ownerOnly"]);
    expect(apply()).toBeUndefined();
  });

  it("opens the entry it shows on the Decoy sites page", async () => {
    await mount();
    const open = () => document.querySelector<HTMLAnchorElement>("a.dz-open")?.getAttribute("href");
    expect(open()).toBe("/decoys/builtin");
    await click(modeRadio("panel"));
    expect(open()).toBe("/decoys/panel_dir");
  });
});

describe("the site from the library", () => {
  it("lists the ready sites only, the saved one chosen, and says what to do while none is", async () => {
    await mount(node());
    await click(modeRadio("own"));
    expect(text()).toContain(en["dz.pk.title"]);
    expect(siteRadio("Bakery")).toBeDefined();
    expect(siteRadio("Studio")).toBeDefined();
    expect(siteRadio("Draft one")).toBeUndefined(); // a draft cannot be put on a node
    expect(bar().textContent).toContain(en["decoy.lib.pick"]);
    expect(disabled(apply())).toBe(true);
    expect(document.querySelector("a.dz-open")).toBeNull(); // no site chosen: nothing to open
  });

  it("starts on the site the node stands on, with nothing to apply", async () => {
    await mount(onBakery());
    expect(modeRadio("own").getAttribute("aria-checked")).toBe("true");
    expect(siteRadio("Bakery").getAttribute("aria-checked")).toBe("true");
    expect(bar().textContent).toContain(en["decoy.state.ok"]);
    expect(disabled(apply())).toBe(true);
    expect(document.querySelector("a.dz-open")!.getAttribute("href")).toBe("/decoys/dsi_bk");
  });

  it("asks the panel what the site would warn about first (a dry run), shows it, and assigns after the click", async () => {
    assignDecoySite.mockImplementation(async (r: { dryRun: boolean }) =>
      r.dryRun
        ? { warnings: [{ code: "shared_domains", level: "warning", ok: false, paths: ["de1", "nl1"] }, { code: "no_404", level: "warning", ok: false, paths: [] }], replaced: [], nodes: [], applied: false }
        : { warnings: [], replaced: [], nodes: [], applied: true },
    );
    await mount(node());
    await click(modeRadio("own"));
    await click(siteRadio("Studio"));
    expect(assignDecoySite).toHaveBeenCalledTimes(1);
    expect(assignDecoySite).toHaveBeenCalledWith({ siteId: "dsi_ph", nodeIds: ["nod_1"], panel: false, dryRun: true }, expect.anything());
    expect(text()).toContain(en["dz.as.shared"]);
    expect(text()).toContain("de1, nl1");
    expect(text()).toContain(en["dz.ck.no_404"]);
    expect(bar().textContent).toContain(en["np.sv.site.pending"]);
    expect(assignDecoySite).toHaveBeenCalledTimes(1); // the warnings do not stop it, and nothing was assigned yet
    await click(apply());
    expect(assignDecoySite).toHaveBeenCalledTimes(2);
    expect(assignDecoySite).toHaveBeenLastCalledWith({ siteId: "dsi_ph", nodeIds: ["nod_1"], panel: false, dryRun: false });
    expect(setNodeDecoyMode).not.toHaveBeenCalled();
  });

  it("changes from one site to another the same way, and leaves the library alone when going back to the built-in page", async () => {
    setNodeDecoyMode.mockResolvedValue({ node: node() });
    await mount(onBakery());
    await click(siteRadio("Studio"));
    expect(document.querySelector("a.dz-open")!.getAttribute("href")).toBe("/decoys/dsi_ph");
    await click(modeRadio("builtin"));
    await click(apply());
    expect(setNodeDecoyMode).toHaveBeenCalledWith({ nodeId: "nod_1", mode: "builtin" });
    expect(assignDecoySite.mock.calls.every(([r]) => (r as { dryRun: boolean }).dryRun)).toBe(true);
  });

  it("says in words why the panel refused the assignment", async () => {
    assignDecoySite.mockImplementation(async (r: { dryRun: boolean }) => {
      if (r.dryRun) return { warnings: [], replaced: [], nodes: [], applied: false };
      throw new ConnectError("decoy_site_draft", Code.FailedPrecondition);
    });
    await mount(node());
    await click(modeRadio("own"));
    await click(siteRadio("Studio"));
    await click(apply());
    expect(document.querySelector("[role=alert]")?.textContent).toBe(en["err.decoy_site_draft"]);
  });

  it("follows the node when it is changed from elsewhere", async () => {
    await mount(onBakery());
    await click(siteRadio("Studio"));
    expect(bar().textContent).toContain(en["np.sv.site.pending"]);
    await act(async () =>
      root!.render(
        <QueryClientProvider client={qc}>
          <ToastProvider>
            <DecoyCard node={onBakery({ decoySiteId: "dsi_ph" }) as never} />
          </ToastProvider>
        </QueryClientProvider>,
      ),
    );
    expect(siteRadio("Studio").getAttribute("aria-checked")).toBe("true");
    expect(bar().textContent).toContain(en["decoy.state.ok"]);
  });
});

describe("an archive dropped on the zone", () => {
  it("becomes a site of the library named after the file, then the site of this node", async () => {
    createDecoySite.mockResolvedValue({ site: site("new", "holzofen") });
    await mount(node());
    await click(modeRadio("own"));
    await pickFile(archive("holzofen.zip", 3));
    expect(createDecoySite).toHaveBeenCalledTimes(1);
    const sent = createDecoySite.mock.calls[0]![0] as { name: string; bundleArchive: Uint8Array; state: string; source: string };
    expect(sent.name).toBe("holzofen");
    expect([...sent.bundleArchive]).toEqual([0, 0, 0]);
    expect([sent.state, sent.source]).toEqual(["ready", "upload"]);
    expect(assignDecoySite).toHaveBeenLastCalledWith({ siteId: "dsi_new", nodeIds: ["nod_1"], panel: false, dryRun: false });
  });

  it("takes a name that is free: the library has a Bakery already", async () => {
    createDecoySite.mockResolvedValue({ site: site("new", "Bakery 2") });
    await mount(node());
    await click(modeRadio("own"));
    await pickFile(archive("bakery.zip"));
    expect((createDecoySite.mock.calls[0]![0] as { name: string }).name).toBe("bakery 2");
  });

  it("is refused here, before it is sent, above 2 MB", async () => {
    await mount(node());
    await click(modeRadio("own"));
    await pickFile(archive("huge.zip", maxBundleBytes + 1));
    expect(text()).toContain("huge.zip is 2.1 MiB: the limit is 2 MiB.");
    expect(createDecoySite).not.toHaveBeenCalled();
    createDecoySite.mockResolvedValue({ site: site("new", "fits") });
    await pickFile(archive("fits.zip", maxBundleBytes)); // exactly the limit goes through to the panel
    expect(createDecoySite).toHaveBeenCalledTimes(1);
  });

  it("is refused by the panel in words, with the path it names, and nothing is assigned", async () => {
    createDecoySite.mockRejectedValue(new ConnectError("decoy_bundle_bad_path: path=assets%2F..%2Fetc%2Fjob", Code.InvalidArgument));
    await mount(node());
    await click(modeRadio("own"));
    await pickFile(archive());
    const alert = document.querySelector("[role=alert]")!.textContent!;
    expect(alert).toContain(en["dz.er.decoy_bundle_bad_path"]);
    expect(alert).toContain("assets/../etc/job");
    expect(assignDecoySite).not.toHaveBeenCalled();
  });

  it("is off for a helper", async () => {
    await mount(node(), { admin: { id: "adm_2", role: Role.HELPER } });
    await click(modeRadio("own")); // a helper cannot even pick it
    expect(document.querySelector(".sv-drop")).toBeNull();
  });
});

describe("what the node has delivered", () => {
  it("says it stands, with the green bar, when the node reports the site", async () => {
    await mount(onBakery());
    expect(bar().className).toContain("ok");
    expect(bar().textContent).toContain(en["decoy.state.ok"]);
  });

  it("names the reason in the library's words: on its way, offline, failed, an old agent", async () => {
    await mount(onBakery({ decoyReasonCode: "pending" }));
    expect(bar().textContent).toContain(en["dz.dl.way"]);
    expect(bar().className).not.toContain("ok");
    act(() => root?.unmount());
    host?.remove();
    for (const [code, key] of [["node_offline", "dz.dl.offHint"], ["apply_failed", "dz.dl.failHint"], ["agent_too_old", "dz.dl.oldHint"]] as const) {
      await mount(onBakery({ decoyReasonCode: code }));
      expect(bar().textContent).toContain(en[key]);
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("dims the bar for an offline node, and says an agent without decoy/1 needs updating even where no reason is given", async () => {
    await mount(onBakery({ decoyReasonCode: "node_offline" }));
    expect(bar().className).toContain("off");
    act(() => root?.unmount());
    host?.remove();
    await mount(node({ decoySupported: false }));
    expect(bar().textContent).toContain(en["dz.dl.oldHint"]);
  });

  it("words the panel's unusable site like its refusal, in Russian too", async () => {
    setLang("ru");
    await mount(node({ decoyMode: "panel", decoyReasonCode: "panel_too_large" }));
    expect(bar().textContent).toContain(ru["err.panel_too_large"]);
  });

  it("sends, with a turning button, while the panel works", async () => {
    let done: (v: unknown) => void = () => {};
    setNodeDecoyMode.mockReturnValue(new Promise((r) => (done = r)));
    await mount();
    await click(modeRadio("panel"));
    await click(apply());
    expect(bar().textContent).toContain(en["np.sv.site.sending"]);
    expect(disabled(apply())).toBe(true);
    await act(async () => done({}));
  });
});

describe("the Cloudflare edition", () => {
  it("cannot take an own site: the option is off, with the edition's notice, and there is no library to open", async () => {
    await mount(node({ decoyOwnAvailable: false }), { admin: { id: "adm_1", role: Role.OWNER }, edition: "edge", unavailable: [...editionFeatures] });
    expect(modeRadio("own").disabled).toBe(true);
    expect(modeRadio("panel").disabled).toBe(false);
    expect(text()).toContain(en["err.unavailable_in_edition"].replace("{feature}", en["edition.feature.decoy_own"]));
    expect(document.querySelector("a.dz-open")).toBeNull();
  });

  it("offers it on the VPS edition", async () => {
    await mount();
    expect(modeRadio("own").disabled).toBe(false);
    expect(text()).not.toContain(en["edition.feature.decoy_own"]);
  });
});
