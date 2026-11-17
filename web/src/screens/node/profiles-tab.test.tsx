import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { setLang } from "@/i18n";
import { nodePageData, type NState } from "../dev-kit-node-page";
import { ProfilesTab } from "./profiles";

// The Profiles tab on the data of the dev-kit scene: a card per server with its day of checks, its facts and the day's numbers.

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
beforeEach(() => setLang("en"));

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(state: NState) {
  const { qc, data } = nodePageData(state);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <ProfilesTab data={data as never} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => host!.textContent ?? "";
const labels = () => [...host!.querySelectorAll("button")].map((b) => b.textContent?.trim() || b.getAttribute("aria-label"));

describe("the Profiles tab", () => {
  it("draws a card per server: its name and port, its state, a day of hourly cells, its facts and the numbers of the day", async () => {
    await mount("calm");
    expect(host!.querySelector("h2")!.textContent).toBe("3 profiles on this node");
    expect(text()).toContain("client-side checks every 5 minutes");
    expect(host!.querySelectorAll(".nd-tk")).toHaveLength(3);
    expect(host!.querySelectorAll(".nd-tk")[0]!.children).toHaveLength(24);
    expect(text()).toContain("udp/443");
    expect(text()).toContain("tcp/443");
    expect(text()).toContain("udp/51820");
    expect(text()).toContain("Last 24 hours");
    expect(text()).toContain("checks passed");
    expect(text()).toContain("31 ms"); // the time of the first
    expect(labels()).toEqual(expect.arrayContaining(["Add profile", "Edit", "Restart", "Remove"]));
  });

  it("names the failure of a server that did not start, and offers what fixes it", async () => {
    await mount("many");
    expect(text()).toContain("Port tcp/443 is taken by another program");
    expect(labels()).toContain("Change port");
  });

  it("says there is nothing on a new node, and offers to add", async () => {
    await mount("new");
    expect(text()).toContain("No profiles on de1");
    expect(labels()).toContain("Add profile");
    expect(host!.querySelectorAll(".nd-tk")).toHaveLength(0);
  });

  it("gives a read-only role the rows without the buttons", async () => {
    await mount("viewer");
    expect(labels()).not.toContain("Edit");
    expect(labels()).not.toContain("Add profile");
    expect(text()).toContain("Only the owner can add");
  });
});
