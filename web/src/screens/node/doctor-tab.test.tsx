import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { setLang } from "@/i18n";
import { nodePageData, type NState } from "../dev-kit-node-page";
import { NodeDoctorTab } from "./doctor";

// The Doctor tab on the data of the dev-kit scene: the summary card with a cell per check, the problems as cards, the rest as lists.

const health = vi.hoisted(() => ({ runDoctor: vi.fn() }));
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), health: { ...(await orig<typeof import("@/lib/api")>()).health, ...health } }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children?: ReactNode; to: string; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
  useNavigate: () => () => {},
  useSearch: () => ({}),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});
beforeEach(() => {
  setLang("en");
  health.runDoctor.mockReset();
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(state: NState) {
  const { qc } = nodePageData(state);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <NodeDoctorTab nodeId="nod_de1" nodeName="de1" />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
}
const text = () => host!.textContent ?? "";
const cells = () => [...host!.querySelectorAll(".nd-dr-strip i")].map((i) => i.className);
const buttons = (label: string) => [...host!.querySelectorAll("button")].filter((b) => b.textContent?.includes(label));

describe("the Doctor tab", () => {
  it("says the node is healthy and draws a cell per check, the accepted one grey", async () => {
    await mount("calm");
    expect(host!.querySelector("h3")!.textContent).toBe("The node is healthy");
    expect(text()).toContain("14 of 14 are fine");
    expect(text()).toContain("Accepted by you"); // the warning accepted as normal says so, with the way back
    expect(buttons("Count it again")).toHaveLength(1);
    expect(cells()).toHaveLength(14);
    expect(cells().filter((c) => c === "mute")).toHaveLength(1); // CPU steal, accepted as normal
    expect(host!.querySelector(".nd-dr-sum")!.className).toContain("ok");
    expect(text()).toContain("The doctor inspects the host every 10 minutes");
    expect(buttons("Run again")).toHaveLength(1);
  });

  it("puts what needs attention in cards, with the reason and what can be done, and counts them", async () => {
    await mount("alerts");
    expect(host!.querySelector("h3")!.textContent).toBe("2 checks need attention");
    expect(text()).toContain("2 of 14");
    expect(host!.querySelectorAll("section h4").length).toBeGreaterThanOrEqual(2);
    expect(cells().filter((c) => c === "warn")).toHaveLength(2);
    expect(buttons("This is normal for this node").length).toBeGreaterThan(0);
  });

  it("is red when a check failed, and names it", async () => {
    await mount("limit");
    expect(host!.querySelector(".nd-dr-sum")!.className).toContain("crit");
    expect(host!.querySelector("h3")!.textContent).toContain("The node is at risk");
    expect(cells().filter((c) => c === "crit")).toHaveLength(1);
  });

  it("invites to the first inspection when there is no report yet", async () => {
    await mount("new");
    expect(host!.querySelector("h3")!.textContent).toBe("Doctor hasn’t inspected this node yet");
    expect(buttons("Inspect")).toHaveLength(1);
    expect(cells().every((c) => c === "wait")).toBe(true);
  });

  it("does not offer to inspect a node that does not answer, and says what it shows is the last report", async () => {
    await mount("down");
    expect(text()).toContain("Doctor can’t examine the node");
    expect(buttons("Run again")).toHaveLength(0);
  });

  it("fills the cells in turn while the node is looked at, and gives them back when it has answered", async () => {
    let answer: (v: unknown) => void = () => {};
    health.runDoctor.mockReturnValue(new Promise((r) => (answer = r)));
    await mount("alerts");
    await act(async () => buttons("Run again")[0]!.click());
    // the mutation turns pending on the render after the click: on a busy machine that is not yet the same tick
    await act(async () => vi.waitFor(() => expect(host!.querySelector("h3")!.textContent).toBe("Inspecting the node"), { timeout: 3000 }));
    expect(cells().filter((c) => c === "now")).toHaveLength(1);
    expect(buttons("Inspecting")[0]!.hasAttribute("disabled") || buttons("Inspecting")[0]!.getAttribute("data-disabled") !== null).toBe(true);
    expect(health.runDoctor).toHaveBeenCalledWith({ nodeId: "nod_de1" });
    await act(async () => answer({ nowUnix: 1, nodes: [] }));
  });
});
