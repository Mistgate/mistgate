import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setEffectKind, setEffectsEnabled } from "@/lib/effects";
import { halo, ringColors, stopHalo } from "./halo";

const realRect = HTMLElement.prototype.getBoundingClientRect;
const realContext = HTMLCanvasElement.prototype.getContext;

beforeAll(() => {
  // every block 100x40 at (300, 300), about 475 px from the origin: the ring reaches it ~50 ms in. No WebGL in jsdom, so
  // only the push is tested
  HTMLElement.prototype.getBoundingClientRect = () => ({ x: 300, y: 300, left: 300, top: 300, right: 400, bottom: 340, width: 100, height: 40, toJSON: () => ({}) }) as DOMRect;
  HTMLCanvasElement.prototype.getContext = (() => null) as never;
});
afterAll(() => {
  HTMLElement.prototype.getBoundingClientRect = realRect;
  HTMLCanvasElement.prototype.getContext = realContext;
});

let main: HTMLElement;
let blocks: HTMLElement[];
beforeEach(() => {
  vi.useFakeTimers({ toFake: ["requestAnimationFrame", "cancelAnimationFrame", "performance", "setTimeout"] });
  setEffectsEnabled(true);
  setEffectKind("save", true);
  main = document.createElement("main");
  main.id = "main";
  blocks = [document.createElement("div"), document.createElement("div")];
  blocks[1]!.style.transform = "rotate(1deg)";
  main.append(...blocks);
  document.body.append(main);
});
afterEach(() => {
  stopHalo();
  main.remove();
  vi.useRealTimers();
});

const origin = { x: 0, y: 0 };

describe("halo", () => {
  it("pushes the blocks as the ring passes and puts them back as they were", async () => {
    halo("save", { origin });
    await vi.advanceTimersByTimeAsync(48);
    expect(blocks[0]!.style.transform).toContain("translate3d");
    expect(blocks[1]!.style.transform).toMatch(/^rotate\(1deg\) translate3d/);
    await vi.advanceTimersByTimeAsync(1000);
    expect(blocks[0]!.style.transform).toBe("");
    expect(blocks[1]!.style.transform).toBe("rotate(1deg)");
  });

  it("a new halo puts back what the one playing moved before it starts", async () => {
    halo("save", { origin });
    await vi.advanceTimersByTimeAsync(48);
    expect(blocks[1]!.style.transform).not.toBe("rotate(1deg)");
    halo("save", { origin: { x: 1000, y: 0 } });
    expect(blocks[1]!.style.transform).toBe("rotate(1deg)");
    await vi.advanceTimersByTimeAsync(1100);
    expect(blocks[1]!.style.transform).toBe("rotate(1deg)");
  });

  it("stays out with the effects or its moment off, and waits for an open window", async () => {
    setEffectKind("save", false);
    halo("save", { origin });
    await vi.advanceTimersByTimeAsync(250);
    expect(blocks[0]!.style.transform).toBe("");

    setEffectKind("save", true);
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    document.body.append(dialog);
    halo("save", { origin });
    await vi.advanceTimersByTimeAsync(250);
    expect(blocks[0]!.style.transform).toBe(""); // held while the window is open
    dialog.remove();
    await vi.advanceTimersByTimeAsync(50 + 48);
    expect(blocks[0]!.style.transform).toContain("translate3d");
  });
});

describe("ringColors", () => {
  it("keeps the first ring for lavender, has one for each accent, and turns the nearest one for any other colour", () => {
    expect(ringColors("#b8acf2")[1]).toEqual([0.63, 0.43, 1]);
    expect(ringColors("#7dd3a0")).not.toEqual(ringColors("#b8acf2"));
    expect(ringColors("nonsense")).toEqual(ringColors("#b8acf2"));
    const purple = ringColors("#a080ff"); // nearest is lavender: the same ring, turned a little
    expect(purple).not.toEqual(ringColors("#b8acf2"));
    purple.flat().forEach((v) => expect(v).toBeGreaterThanOrEqual(0));
  });
});
