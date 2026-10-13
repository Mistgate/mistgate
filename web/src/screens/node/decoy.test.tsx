import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { editionFeatures } from "@/i18n/edition";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { setLang } from "@/i18n";
import { maxBundleBytes } from "@/lib/decoy";
import { meQuery } from "@/lib/session";
import { DecoyCard } from "./decoy";

// Node -> Settings -> "Decoy site": the three modes and their calls, the own archive (checked here before it is sent, refused by the
// panel in words), what the node has applied and why not yet, and the Cloudflare edition without the own site.

const setNodeDecoyMode = vi.fn();
const uploadNodeDecoy = vi.fn();
vi.mock("@/lib/api", () => ({
  nodes: { setNodeDecoyMode: (...a: unknown[]) => setNodeDecoyMode(...a), uploadNodeDecoy: (...a: unknown[]) => uploadNodeDecoy(...a) },
  auth: { me: () => Promise.resolve({}) },
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => setLang("en"));
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  setNodeDecoyMode.mockReset();
  uploadNodeDecoy.mockReset();
});

const sha = (c: string) => c.repeat(64);
const node = (over: Record<string, unknown> = {}) => ({
  id: "nod_1",
  name: "de1",
  decoyMode: "builtin",
  decoySupported: true,
  decoyReasonCode: "",
  decoyBundleSha256: "",
  decoyPanelAvailable: true,
  decoyOwnAvailable: true,
  decoyAgentMode: "builtin",
  decoyAgentSha256: "",
  ...over,
});

async function mount(n = node(), me: Record<string, unknown> = { admin: { id: "adm_1", role: Role.OWNER } }) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQuery.queryKey, me as never);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <DecoyCard node={n as never} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const radio = (mode: string) => document.querySelector<HTMLInputElement>(`input[type=radio][value=${mode}]`)!;
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

describe("choosing a mode", () => {
  it("says what each of the three is, one line each", async () => {
    await mount();
    for (const k of ["decoy.mode.builtinHint", "decoy.mode.panelHint", "decoy.mode.ownHint"] as const) expect(text()).toContain(en[k]);
    expect(radio("builtin").checked).toBe(true);
  });

  it("sends the picked mode for this node", async () => {
    setNodeDecoyMode.mockResolvedValue({ node: node({ decoyMode: "panel" }) });
    await mount();
    expect(button(en["decoy.apply"])!.hasAttribute("data-disabled")).toBe(true); // nothing changed yet
    await click(radio("panel"));
    await click(button(en["decoy.apply"]));
    expect(setNodeDecoyMode).toHaveBeenCalledWith({ nodeId: "nod_1", mode: "panel" });
    expect(uploadNodeDecoy).not.toHaveBeenCalled();
  });

  it("reuses the saved site when own is picked and one is already uploaded", async () => {
    setNodeDecoyMode.mockResolvedValue({ node: node({ decoyMode: "own" }) });
    await mount(node({ decoyBundleSha256: sha("ab") }));
    await click(radio("own"));
    expect(text()).toContain(en["decoy.own.uploaded"].replace("{hash}", "abababababab"));
    await click(button(en["decoy.apply"]));
    expect(setNodeDecoyMode).toHaveBeenCalledWith({ nodeId: "nod_1", mode: "own" });
  });

  it("asks for an archive first when own is picked and none is uploaded", async () => {
    await mount();
    await click(radio("own"));
    expect(text()).toContain(en["decoy.own.needFile"]);
    expect(button(en["decoy.apply"])!.hasAttribute("data-disabled")).toBe(true);
  });

  it("offers the panel's site only when the panel has one", async () => {
    await mount(node({ decoyPanelAvailable: false }));
    expect(radio("panel").disabled).toBe(true);
    expect(text()).toContain(en["decoy.panelNone"]);
  });

  it("is read-only for a helper", async () => {
    await mount(node(), { admin: { id: "adm_2", role: Role.HELPER } });
    expect(radio("panel").disabled).toBe(true);
    expect(text()).toContain(en["decoy.ownerOnly"]);
    expect(button(en["decoy.apply"])).toBeUndefined();
  });
});

describe("the own archive", () => {
  it("is uploaded as the bytes of the picked file, which also picks the own site", async () => {
    uploadNodeDecoy.mockResolvedValue({ node: node({ decoyMode: "own" }) });
    await mount();
    await click(radio("own"));
    await pickFile(archive("site.zip", 3));
    expect(text()).toContain("site.zip");
    await click(button(en["decoy.upload"]));
    expect(uploadNodeDecoy).toHaveBeenCalledTimes(1);
    const sent = uploadNodeDecoy.mock.calls[0]![0] as { nodeId: string; bundleArchive: Uint8Array };
    expect(sent.nodeId).toBe("nod_1");
    expect([...sent.bundleArchive]).toEqual([0, 0, 0]);
  });

  it("is refused here, before it is sent, above 2 MB", async () => {
    await mount();
    await click(radio("own"));
    await pickFile(archive("huge.zip", maxBundleBytes + 1));
    expect(text()).toContain("huge.zip is 2.1 MiB: the limit is 2 MiB.");
    expect(button(en["decoy.upload"])).toBeUndefined();
    expect(uploadNodeDecoy).not.toHaveBeenCalled();
    await pickFile(archive("fits.zip", maxBundleBytes)); // exactly the limit goes through to the panel
    expect(text()).not.toContain(" the limit is 2 MiB.");
    expect(button(en["decoy.upload"])).toBeDefined();
  });

  it("is refused by the panel in words, with the path it names", async () => {
    uploadNodeDecoy.mockRejectedValue(new ConnectError("decoy_bundle_bad_path: path=assets%2F..%2Fetc%2Fjob", Code.InvalidArgument));
    await mount();
    await click(radio("own"));
    await pickFile(archive());
    await click(button(en["decoy.upload"]));
    expect(document.querySelector("[role=alert]")?.textContent).toBe(en["err.decoy_bundle_bad_path"].replace("{path}", "assets/../etc/job"));
  });

  it("says in Russian why the panel's own site cannot be picked", async () => {
    setLang("ru");
    setNodeDecoyMode.mockRejectedValue(new ConnectError("panel_no_index", Code.FailedPrecondition));
    await mount();
    await click(radio("panel"));
    await click(button(ru["decoy.apply"]));
    expect(document.querySelector("[role=alert]")?.textContent).toBe(ru["err.panel_no_index"]);
  });
});

describe("the applied state", () => {
  it("shows the configured and the reported mode with the first digits of their hashes, and no complaint when they agree", async () => {
    await mount(node({ decoyMode: "own", decoyBundleSha256: sha("a3"), decoyAgentMode: "own", decoyAgentSha256: sha("a3") }));
    expect(text()).toContain(en["decoy.state.title"]);
    expect(text().match(/a3a3a3a3a3a3/g)).toHaveLength(3); // the uploaded line, configured, reported: the short form only
    expect(text()).not.toContain("a3a3a3a3a3a3a");
    expect(text()).toContain(en["decoy.state.ok"]);
  });

  it("names the reason a mode is not applied yet", async () => {
    await mount(node({ decoyMode: "panel", decoyReasonCode: "pending" }));
    expect(text()).toContain(en["decoy.reason.pending"]);
    expect(text()).not.toContain(en["decoy.state.ok"]);
  });

  it("says an old agent shows the built-in page, and that it must be updated", async () => {
    await mount(node({ decoyMode: "panel", decoySupported: false, decoyReasonCode: "agent_too_old" }));
    expect(text()).toContain(en["decoy.reason.agent_too_old"]);
    expect(text()).not.toContain(en["decoy.unsupported"]); // said once
  });

  it("explains why a node without decoy/1 uses the built-in page even where no reason is given", async () => {
    await mount(node({ decoySupported: false }));
    expect(text()).toContain(en["decoy.unsupported"]);
  });

  it("words the panel's unusable site like its refusal, and the other reasons in Russian", async () => {
    setLang("ru");
    await mount(node({ decoyMode: "panel", decoyReasonCode: "panel_too_large" }));
    expect(text()).toContain(ru["err.panel_too_large"]);
    for (const code of ["apply_failed", "own_bundle_missing", "node_offline", "unavailable_in_edition"] as const) expect(ru[`decoy.reason.${code}`]).toBeTruthy();
  });
});

describe("the Cloudflare edition", () => {
  it("cannot take an own site: the option is off, with the edition's notice", async () => {
    await mount(node({ decoyOwnAvailable: false }), { admin: { id: "adm_1", role: Role.OWNER }, edition: "edge", unavailable: [...editionFeatures] });
    expect(radio("own").disabled).toBe(true);
    expect(radio("panel").disabled).toBe(false);
    expect(text()).toContain(en["err.unavailable_in_edition"].replace("{feature}", en["edition.feature.decoy_own"]));
  });

  it("offers it on the VPS edition", async () => {
    await mount();
    expect(radio("own").disabled).toBe(false);
    expect(text()).not.toContain(en["edition.feature.decoy_own"]);
  });
});
