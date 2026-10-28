import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, isValidElement, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { StepUpProvider } from "@/components/step-up";
import { setLang } from "@/i18n";
import { meQuery } from "@/lib/session";
import { compactStars, GitHubButton, Shell, VersionLine } from "./shell";

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  setLang("en");
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(me: { version: string; sourceUrl: string; sourceStars?: number }, ui: ReactNode = <VersionLine />) {
  const qc = new QueryClient();
  qc.setQueryData(meQuery.queryKey, me as never);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>{ui}</QueryClientProvider>,
    ),
  );
}

describe("the panel's version line", () => {
  it("links the source code next to the version when the panel names it", async () => {
    await mount({ version: "0.3.0", sourceUrl: "https://github.com/Mistgate/mistgate" });
    expect(host!.textContent).toBe("Panel version 0.3.0 · Source code");
    const a = host!.querySelector("a")!;
    expect(a.getAttribute("href")).toBe("https://github.com/Mistgate/mistgate");
    expect(a.getAttribute("rel")).toContain("noopener");
  });

  it("shows the version alone when the source URL is empty", async () => {
    await mount({ version: "0.3.0", sourceUrl: "" });
    expect(host!.textContent).toBe("Panel version 0.3.0");
    expect(host!.querySelector("a")).toBeNull();
  });
});

describe("the GitHub button of the top bar", () => {
  const repo = "https://github.com/Mistgate/mistgate";
  it("links the source code with the star count the panel read", async () => {
    await mount({ version: "0.3.0", sourceUrl: repo, sourceStars: 823 }, <GitHubButton />);
    const a = host!.querySelector("a")!;
    expect(a.getAttribute("href")).toBe(repo);
    expect(a.getAttribute("rel")).toContain("noopener");
    expect(a.textContent).toBe("823");
    expect(a.getAttribute("aria-label")).toBe("Star on GitHub: 823");
  });

  it("shows the mark alone without a count, and in the phone header", async () => {
    await mount({ version: "0.3.0", sourceUrl: repo }, <GitHubButton />);
    expect(host!.querySelector("a")!.textContent).toBe("");
    expect(host!.querySelector("a")!.getAttribute("aria-label")).toBe("Star on GitHub");
    act(() => root?.unmount());
    host?.remove();
    await mount({ version: "0.3.0", sourceUrl: repo, sourceStars: 823 }, <GitHubButton iconOnly />);
    expect(host!.querySelector("a")!.textContent).toBe("");
  });

  it("is gone when the panel names no source", async () => {
    await mount({ version: "0.3.0", sourceUrl: "", sourceStars: 823 }, <GitHubButton />);
    expect(host!.querySelector("a")).toBeNull();
  });

  it("counts like GitHub: whole up to 999, then k and M rounded down", () => {
    expect([0, 823, 999, 1000, 1250, 1999, 12345, 999999, 1_000_000, 3_456_789].map(compactStars)).toEqual(["0", "823", "999", "1k", "1.2k", "1.9k", "12.3k", "999.9k", "1M", "3.4M"]);
  });
});

describe("the admin shell providers", () => {
  it("keeps the add-node modal inside the step-up provider", () => {
    const shell = Shell();
    expect(shell.type).toBe(StepUpProvider);

    const addNode = (shell.props as { children: ReactNode }).children;
    expect(isValidElement(addNode)).toBe(true);
    if (!isValidElement(addNode)) return;
    expect(addNode.type).toBe(AddNodeProvider);
  });
});
