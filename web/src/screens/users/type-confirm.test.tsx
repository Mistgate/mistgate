import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { TypeConfirmModal } from "./ui";

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

function type(input: HTMLInputElement, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    set.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("TypeConfirmModal", () => {
  it("takes the name however its spaces were typed, and offers to copy it exactly", () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    act(() =>
      root!.render(
        <TypeConfirmModal open onOpenChange={() => {}} title="Delete" confirmLabel="Delete" match="Alice  Smith" onConfirm={async () => {}} />,
      ),
    );
    const input = document.querySelector<HTMLInputElement>("input")!;
    const confirm = () => [...document.querySelectorAll("button")].find((b) => b.textContent === "Delete")!;
    expect(confirm().disabled).toBe(true);

    type(input, "Alice Smith");
    expect(confirm().disabled).toBe(false);

    type(input, "Alice Smit");
    expect(confirm().disabled).toBe(true);

    expect([...document.querySelectorAll("button")].some((b) => /copy/i.test(b.textContent ?? ""))).toBe(true);
  });
});
