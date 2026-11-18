import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { ToastProvider } from "@/components/ui/toast";
import { setLang } from "@/i18n";
import { demoFor, edgeOfScene, frames, roleOfScene, sceneOf, seeded } from "./dev-kit-nodes";
import { NodesScreen } from "./nodes";

// The scenes of /dev-kit?only=nodes draw the real screen on made-up data: every state a scene names must draw, in every language.

vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) } }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
  useBlocker: () => ({ status: "idle" }),
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
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
  localStorage.clear();
  setLang("en");
});

const states = [...new Set(frames.map((f) => f.state))].filter((s) => !s.startsWith("job-"));

async function draw(state: string) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, roleOfScene(state, null), edgeOfScene(state));
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AddNodeProvider>
            <NodesScreen demo={demoFor(state)} />
          </AddNodeProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

describe("the scenes of the Nodes screen", () => {
  it.each(states)("%s draws", async (state) => {
    await draw(state);
    expect(host!.querySelector("h1")?.textContent).toBeTruthy();
    expect(host!.textContent).not.toContain("undefined");
    if (state === "first") expect(host!.querySelectorAll("ol li")).toHaveLength(4);
    if (state === "twelve" || state === "mixed") expect(host!.querySelectorAll("section[data-select] a.absolute")).toHaveLength(12);
    // the board's note under nl1; the notes scene has two more (a long one, one that is markup as typed): each a plate, as text
    if (state === "list") expect([...host!.querySelectorAll("section[data-select] em.truncate")].map((e) => e.textContent)).toEqual(["1 TB of traffic a month, paid for the year"]);
    if (state === "notes") {
      expect(host!.querySelectorAll("section[data-select] em.truncate")).toHaveLength(3);
      expect(host!.querySelector("section[data-select] em.truncate b")).toBeNull();
      expect(host!.querySelector("section[data-select] em.truncate i")).toBeNull();
    }
    if (state === "select") expect(host!.ownerDocument.querySelector("[data-nodes-bar]")?.textContent).toContain("2 nodes selected");
    if (state === "load") expect(host!.querySelectorAll("button[aria-expanded]").length).toBe(4);
    if (state === "map-card") expect(host!.ownerDocument.querySelector("aside")?.textContent).toContain("nl1");
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the list draws in %s", async (lang) => {
    setLang(lang);
    await draw("mixed");
    expect(host!.querySelectorAll("section[data-select] a.absolute")).toHaveLength(12);
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the Paths view draws in %s, its sheet open", async (lang) => {
    setLang(lang);
    await draw("rw-br-near");
    expect(host!.querySelector("aside")).not.toBeNull();
    expect(host!.querySelectorAll(".rw-row[data-node]")).toHaveLength(9);
  });
});

describe("the scenes of the ways", () => {
  it("name a state each: the list and the Paths view of every toggle of the board", () => {
    for (const r of ["norm", "load", "err", "empty", "noent", "nomeas", "entoff", "enthid", "oldagent", "stale", "cf"]) {
      expect(states).toContain(`ru-${r}`);
      expect(states).toContain(`rw-${r}`);
    }
    expect(states).toContain("ru-helper");
    for (const b of ["near", "norm", "lim", "drop", "udp"]) expect(sceneOf(`rw-br-${b}`)).toMatchObject({ view: "ways", br: b, open: "nod_nl1>nod_msk1" });
    expect(sceneOf("rw-f-part")).toMatchObject({ view: "ways", filter: "part" });
    expect(sceneOf("ru-f-noway")).toMatchObject({ view: "list", status: "noway" });
    expect(sceneOf("rw-sheet-part")).toMatchObject({ count: 12, open: "nod_nl2>nod_msk1" });
  });
  it("write no preference to the browser: the view comes with the demo", async () => {
    expect(demoFor("rw-norm")?.view).toBe("ways");
    expect(demoFor("ru-norm")).toBeUndefined();
    await draw("rw-norm");
    expect(host!.querySelector(".rw-mx")).not.toBeNull();
    expect(localStorage.getItem("nodes.view")).toBeNull();
  });
  it("seat a helper for the helper's toggle and the edge edition for Cloudflare's", () => {
    expect(roleOfScene("ru-helper", null)).not.toBe(roleOfScene("ru-norm", null));
    expect(edgeOfScene("rw-cf")).toBe(true);
    expect(edgeOfScene("rw-norm")).toBe(false);
  });
});
