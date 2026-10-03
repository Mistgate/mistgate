import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, isValidElement, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { StepUpProvider } from "@/components/step-up";
import { setLang } from "@/i18n";
import { meQuery } from "@/lib/session";
import { Shell, VersionLine } from "./shell";

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

async function mount(me: { version: string; sourceUrl: string }) {
  const qc = new QueryClient();
  qc.setQueryData(meQuery.queryKey, me as never);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <VersionLine />
      </QueryClientProvider>,
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
