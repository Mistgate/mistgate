import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/en";
import { dictionaries, plainText, setLang } from "@/i18n";
import { DecoysScreen } from "./decoys/list";
import { DecoySiteScreen } from "./decoys/site";
import { frames, sceneOf } from "./dev-kit-decoys";
import { demoBlog } from "./dev-kit-decoys-real";

// The scenes of /dev-kit?only=decoys draw the real screens on made-up data: every state a scene names must draw, in every language.

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
  useNavigate: () => vi.fn(),
  useParams: () => ({}),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});
let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  setLang("en");
});

const states = [...new Set(frames.map((f) => f.state))];

async function draw(state: string) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const scene = sceneOf(state);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={scene.qc}>
        <ToastProvider>{scene.site ? <DecoySiteScreen id={scene.id} demo={scene.siteDemo} /> : <DecoysScreen demo={scene.listDemo} />}</ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => document.body.textContent ?? "";
const buttonOf = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);

describe("the scenes of the Decoy sites screens", () => {
  it.each(states)("%s draws", async (state) => {
    await draw(state);
    expect(text()).not.toContain("undefined");
    expect(document.querySelector("h1")?.textContent || document.querySelector("[role=dialog] h2")?.textContent).toBeTruthy();
    const cards = () => document.querySelectorAll("a.dz-c").length;
    if (state === "list") expect(cards()).toBe(8);
    if (state === "list-full") expect(cards()).toBe(100);
    if (state === "list-many") {
      // twelve nodes and the panel: thirteen cells, filled up to fifteen so the last row has all its lines
      expect(document.querySelectorAll("[data-shot=decoy-now] .dz-nw:not(.dz-nb)").length).toBe(13);
      expect(document.querySelectorAll("[data-shot=decoy-now] .dz-nb").length).toBe(2);
    }
    if (state === "list-draft") expect(cards()).toBe(3);
    if (state === "list-find") expect(cards()).toBe(1);
    if (state === "list-nomatch") expect(text()).toContain(en["dz.none.title"]);
    if (state === "list-empty") expect(document.querySelectorAll(".dz-way")).toHaveLength(4);
    if (state === "dlg-new") expect(document.querySelectorAll(".dz-way")).toHaveLength(4);
    if (state === "list-load") expect(document.querySelectorAll(".dz-sk")).toHaveLength(6);
    if (state === "list-err") expect(text()).toContain(en["dz.err.title"]);
    if (state === "list-deny" || state === "site-deny") expect(text()).toContain(en["dz.deny.title"]);
    if (state === "list-edge") expect(text()).toContain(en["dz.edge.title"]);
    if (state === "dlg-up-ready") expect(text()).toContain(en["dz.res.ready"]);
    if (state === "dlg-up-draft") expect(text()).toContain(en["dz.res.failed"]);
    if (state === "dlg-up-run") expect(text()).toContain("34%");
    if (state === "dlg-up-big") expect(text()).toContain(en["dz.er.decoy_bundle_too_large"]);
    if (state === "dlg-up-unsafe") expect(text()).toContain("img/latest");
    if (state === "dlg-tpl") expect(document.querySelectorAll(".dz-ways.tpl .dz-way")).toHaveLength(10);
    if (state === "dlg-tpl-wait") expect(document.querySelector(".dz-fr")?.textContent).toContain(en["dz.tpl.loading"]);
    if (state === "dlg-tpl-view") expect(document.querySelector(".dz-fr iframe")?.getAttribute("srcdoc")).toContain(demoBlog.name);
    if (state === "dlg-tpl-view") expect(text()).toContain(en["dz.tpl.footView"].replace("{name}", demoBlog.name));
    if (state === "dlg-tpl-run") expect(text()).toContain(en["dz.tpl.footRun"]);
    if (state === "dlg-tpl-err") expect(text()).toContain(en["err.decoy_library_full"]);
    if (state === "dlg-tpl-nokey") {
      expect(text()).toContain(en["dz.ph.off"]);
      expect(text()).toContain(en["dz.ph.st.no_key"].replace("{name}", "Pixabay"));
      expect(buttonOf(en["dz.ph.add"])).toBeDefined();
      expect(buttonOf(en["dz.ph.remove"])).toBeUndefined();
    }
    if (state === "dlg-tpl-keyform") {
      expect(document.querySelector(".dz-ph input[type=password]")).not.toBeNull();
      expect(text()).toContain(en["dz.ph.lead"]);
      expect(document.querySelector(".dz-ph .frm a")?.getAttribute("href")).toBe("https://pixabay.com/api/docs/");
      expect(text()).toContain(en["dz.ph.howPixabay"]);
    }
    if (state === "dlg-tpl-pexels") {
      // Pexels looked at without a key: the calm hint names Pixabay and links to its API page, and no form is open
      expect(document.querySelector(".dz-ph .hint")?.textContent).toContain(en["dz.ph.pexelsHint"]);
      expect(document.querySelector(".dz-ph .hint a")?.getAttribute("href")).toBe("https://pixabay.com/api/docs/");
      expect(document.querySelector(".dz-ph input[type=password]")).toBeNull();
      expect(text()).toContain(en["dz.ph.off"]);
      expect(buttonOf(en["dz.ph.add"])).toBeDefined();
    }
    if (state === "dlg-tpl-both") {
      expect(document.querySelectorAll(".dz-ph .src [role=radio]")).toHaveLength(2);
      expect(document.querySelector(".dz-ph .src [role=radio][data-checked]")?.textContent).toContain("Pexels");
      expect(text()).toContain(en["dz.ph.st.used"]);
    }
    if (state === "dlg-tpl-photos") {
      expect(text()).toContain(en["dz.ph.on"]);
      expect(text()).toContain(en["dz.ph.st.used"]);
      expect(buttonOf(en["dz.ph.remove"])).toBeDefined();
      expect(document.querySelector(".dz-fr iframe")?.getAttribute("srcdoc")).toContain("data:image/jpeg;base64,");
    }
    if (state === "dlg-tpl-limit") expect(text()).toContain(en["dz.ph.st.limit"].replace("{name}", "Pixabay"));
    if (state.startsWith("dlg-tpl")) {
      // the line that says where the photos are from names the source of the preview in view, and links to it; with no photos
      // in view and a source without a key on the screen, it names that source
      const credit = document.querySelector(".dz-ph .ft a");
      const pexels = state === "dlg-tpl-both" || state === "dlg-tpl-pexels";
      expect(credit?.getAttribute("href")).toBe(pexels ? "https://www.pexels.com" : "https://pixabay.com");
      expect(credit?.textContent).toBe(en["dz.ph.credit"].replace("{name}", pexels ? "Pexels" : "Pixabay"));
      expect(credit?.getAttribute("rel")).toContain("noopener");
      expect(document.querySelectorAll(".dz-ph .src [role=radio]")).toHaveLength(2);
    }
    if (state === "dlg-mcp") expect(document.querySelector(".dz-task")?.textContent).toContain("decoy_site_draft_save");
    if (state === "site-used") expect(document.querySelector("[data-shot=decoy-placement]")?.querySelectorAll(".dz-plr")).toHaveLength(2);
    if (state === "site-draft") expect(text()).toContain(en["dz.act.ready"]);
    if (state === "site-bad") expect(text()).toContain(en["dz.ban.bad"]);
    if (state === "site-builtin") expect(document.querySelector(".dz-fr iframe")?.getAttribute("srcdoc")).toContain("Coming soon");
    if (state === "site-fail") expect(text()).toContain(en["dz.dl.fail"]);
    if (state === "site-old") expect(text()).toContain(en["dz.dl.old"]);
    if (state === "site-404") expect(document.querySelector(".dz-fr iframe")?.getAttribute("srcdoc")).toContain("404");
    if (state === "site-load") expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    if (state === "site-nf") expect(text()).toContain(en["dz.nf.title"]);
    if (state === "site-edge") expect(text()).toContain(en["dz.edge.title"]);
    if (state === "site-dir-off") expect(text()).toContain(en["dz.ban.dirOff"]);
    if (state === "site-dir") expect(document.querySelector("[data-shot=decoy-placement]")?.textContent).toContain("de1");
    if (state === "dlg-assign") expect(text()).toContain(en["dz.as.sum"]);
    if (state === "dlg-assign-two") expect(text()).toContain(en["dz.as.shared"]);
    if (state === "dlg-delete-used") expect(text()).toContain(en["dz.del.usedTitle"]);
    if (state === "dlg-revert") expect(text()).toContain(en["dz.ver.text"]);
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the list and a site draw in %s", async (lang) => {
    setLang(lang);
    await draw("list");
    expect(document.querySelectorAll("a.dz-c")).toHaveLength(8);
    act(() => root?.unmount());
    host?.remove();
    await draw("site-used");
    expect(document.querySelector("h1")?.textContent).toBe("Пекарня Хольцофен");
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the window from a template draws in %s: the kinds and a preview", async (lang) => {
    setLang(lang);
    await draw("dlg-tpl");
    expect(document.querySelectorAll(".dz-ways.tpl .dz-way")).toHaveLength(10);
    expect(text()).toContain(dictionaries[lang]["dz.tpl.blog"]);
    expect(text()).not.toContain("dz.tpl.");
    act(() => root?.unmount());
    host?.remove();
    await draw("dlg-tpl-view");
    expect(text()).toContain(dictionaries[lang]["dz.tpl.create"]);
    expect(plainText(text())).toContain(dictionaries[lang]["dz.tpl.footView"].replace("{name}", demoBlog.name));
  });
});
