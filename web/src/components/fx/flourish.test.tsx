import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { setEffectsEnabled } from "@/lib/effects";
import { EdgeLoop, Flourish } from "./flourish";

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  setEffectsEnabled(false);
});
const draw = (node: React.ReactNode) => act(async () => root!.render(node));

describe("the end of an action on a card", () => {
  it("is a wave from the point asked for and two glints along the edge, with the reveal animation on", async () => {
    setEffectsEnabled(true);
    await draw(<Flourish x={43} y={39} />);
    const wave = host!.querySelector<HTMLElement>(".fl-wv")!;
    expect(wave.style.getPropertyValue("--fl-x")).toBe("43px");
    expect(wave.style.getPropertyValue("--fl-y")).toBe("39px");
    expect([...host!.querySelectorAll(".fl-eg")].map((e) => e.className)).toEqual(["fl-eg a", "fl-eg b"]);
    expect(host!.querySelectorAll("[aria-hidden]")).toHaveLength(3);
  });

  it("is nothing with the reveal animation off, the loop too", async () => {
    setEffectsEnabled(false);
    await draw(
      <>
        <Flourish x={1} y={1} />
        <EdgeLoop />
      </>,
    );
    expect(host!.innerHTML).toBe("");
  });

  it("the loop goes round the edge with the animation on", async () => {
    setEffectsEnabled(true);
    await draw(<EdgeLoop />);
    expect(host!.querySelector(".fl-loop")).not.toBeNull();
  });
});
