import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { ToastProvider, useToast } from "./toast";

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

function Buttons() {
  const toast = useToast();
  return (
    <>
      <button onClick={() => toast.error("The node is not connected.")}>e1</button>
      <button onClick={() => toast.error("This name is taken.")}>e2</button>
      <button onClick={() => toast("Saved")}>n1</button>
      <button onClick={() => toast("Copied")}>n2</button>
    </>
  );
}

const press = (label: string) => act(() => [...document.querySelectorAll("button")].find((b) => b.textContent === label)!.click());
const shown = () => [...document.querySelectorAll("[role=dialog], [role=alertdialog]")].map((r) => `${r.getAttribute("role")}: ${r.textContent}`);

describe("toast", () => {
  it("keeps an error, read out at once and closable, above the note that came after it", () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    act(() =>
      root!.render(
        <ToastProvider>
          <Buttons />
        </ToastProvider>,
      ),
    );
    press("e1");
    press("n1");
    expect(shown()).toEqual(["alertdialog: !The node is not connected.", "dialog: Saved"]);
    expect(document.querySelector("[role=alert]")?.textContent).toBe("The node is not connected.");
    expect(document.querySelector("[role=alertdialog] button[aria-label=Close]")).not.toBeNull();

    // one of each: a new note replaces the note, a new error the error
    press("n2");
    press("e2");
    expect(shown()).toEqual(["alertdialog: !This name is taken.", "dialog: Copied"]);
  });
});
