import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { NodeAddPage } from "./dev-kit-node-add";

// The window "Put a profile on a node" (the board's PnNode, dlg=add) on the made-up data of its scene: the profiles the node lacks stand
// in three sections with their counts, the port is at the end of a row, and a profile that needs a field opens the form.

vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, webauthnSupported: () => false, profiles: {}, nodes: {}, groups: {}, health: {} }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children?: ReactNode }) => <a>{children}</a>,
  useNavigate: () => vi.fn(),
  useSearch: () => ({}),
  useParams: () => ({}),
  useLocation: () => ({ pathname: "/", search: "" }),
  useBlocker: () => ({ status: "idle" }),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: (query: string) => ({ matches: query.includes("prefers-reduced-motion"), media: query, addEventListener() {}, removeEventListener() {} }),
  });
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

async function draw(state: string) {
  history.replaceState(null, "", `/?only=node-add-page&state=${state}`);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<NodeAddPage />));
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const qa = (sel: string) => [...document.querySelectorAll<HTMLElement>(sel)];

describe("put a profile on a node", () => {
  it("sorts the profiles into the three sections, with the port at the end of a row", async () => {
    await draw("list");
    expect(qa(".pt-gh").map((h) => [h.querySelector("b")?.textContent, h.querySelector(".mono")?.textContent])).toEqual([["Can be put", "3"], ["Needs an action", "1"], ["Cannot be put", "1"]]);
    const row = (name: string) => qa(".pt-r").find((r) => r.querySelector("b")?.textContent === name)!;
    expect(row("Primary · WARP").querySelector(".nd-pap")?.textContent).toBe("udp/8443");
    expect(row("VLESS · XHTTP").querySelector(".nd-pap")?.textContent).toBe("tcp/8443"); // its own port is taken: the free one
    expect(row("VLESS · XHTTP").querySelector(".pt-why")?.textContent).toContain("8443 is free");
    expect(row("VLESS · CDN").textContent).toContain("Set up…");
    expect(row("AWG 2.0 (new)").querySelector("[role=checkbox]")?.getAttribute("aria-disabled")).toBe("true");
    expect(row("AWG 2.0 (new)").textContent).toContain("Open updates");
  });

  it("counts what is ticked in its button, and a profile that needs an action cannot be ticked", async () => {
    await draw("list");
    const tick = async (name: string) => {
      const r = qa(".pt-r").find((x) => x.querySelector("b")?.textContent === name)!;
      await act(async () => void r.querySelector("[role=checkbox]")!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    };
    const put = () => qa("footer button").at(-1)!;
    expect(put().textContent).toBe("Put");
    await tick("Primary · WARP");
    await tick("Mihomo · Gecko");
    await tick("VLESS · CDN");
    expect(put().textContent).toBe("Put 2");
  });

  it("is a list of rows still being asked about while the panel checks", async () => {
    await draw("check");
    expect(qa(".pt-gh")).toHaveLength(0);
    expect(qa(".pt-r em").map((e) => e.textContent)).toEqual(Array(5).fill("Checking…"));
  });
});
