import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { setLang } from "@/i18n";
import { noteServerVersion } from "@/lib/stale";
import { StaleToast } from "./stale-toast";

// the page was served by v0.1.35 (lib/stale.ts reads the meta once, when it loads, like on a page load)
vi.hoisted(() => {
  document.head.innerHTML = `<meta name="mistgate-version" content="v0.1.35">`;
});
// location.reload cannot be replaced in jsdom; reloadPage is the one call to it
const reload = vi.hoisted(() => vi.fn());
vi.mock("@/lib/stale", async (orig) => ({ ...(await orig<object>()), reloadPage: () => reload() }));

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  setLang("en");
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});
/** A page load: the toast mounts afresh (the server's version stays known, like after a reload that is still stale). */
function mount() {
  act(() => root?.unmount());
  host?.remove();
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
  act(() => root!.render(<StaleToast />));
}
const button = (label: string) => [...host!.querySelectorAll("button")].find((b) => b.textContent === label || b.getAttribute("aria-label")?.startsWith(label)) ?? null;
const click = (el: Element | null) => act(() => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));

describe("the reload notice", () => {
  it("waits in an empty live region while the tab is current, then shows the new version and a reload", () => {
    mount();
    const live = host!.querySelector("[role=status]")!;
    expect(live.getAttribute("aria-live")).toBe("polite");
    expect(host!.textContent).toBe("");
    act(() => noteServerVersion(new Headers({ "X-Mistgate-Version": "v0.1.35" })));
    expect(host!.textContent).toBe("");

    act(() => noteServerVersion(new Headers({ "X-Mistgate-Version": "v0.1.36" })));
    expect(live.querySelector("h2")?.textContent).toBe("The panel was updated to v0.1.36");
    expect(live.textContent).toContain("Reload the page so the admin works with the new version.");
    expect(button("Reload")?.tagName).toBe("BUTTON");
  });

  it("reloads the page", () => {
    mount();
    click(button("Reload"));
    expect(reload).toHaveBeenCalledOnce();
  });

  it("collapses to a pill with the button or Esc, keeps the focus with it, opens again, and is back after a reload", () => {
    mount();
    button("Collapse")!.focus();
    click(button("Collapse"));
    expect(host!.textContent).toBe("v0.1.36");
    expect(document.activeElement).toBe(button("Show the reload notice"));

    click(button("Show the reload notice"));
    expect(host!.querySelector("h2")?.textContent).toBe("The panel was updated to v0.1.36");
    const card = host!.querySelector("section")!;
    expect(document.activeElement).toBe(card);
    act(() => void card.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(host!.textContent).toBe("v0.1.36");

    mount();
    expect(host!.querySelector("h2")?.textContent).toBe("The panel was updated to v0.1.36");
  });
});
