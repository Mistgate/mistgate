import { act, useEffect, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { en } from "@/i18n/en";
import { demoQr } from "@/lib/qr-demo";
import { CodeField, GoButton, OtherWay, PasskeyBlock, TotpEnroll } from "./auth-parts";

// The six boxes of a code: what the person types, pastes and deletes ends in one string of digits, and the phone's
// one-time-code autofill (a whole code into the first box) fills all six.

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

let last = "";
function Probe({ error }: { error?: string }) {
  const [v, setV] = useState("");
  useEffect(() => {
    last = v;
  }, [v]);
  return <CodeField label="Authenticator code" value={v} onChange={setV} error={error} />;
}
async function mount(ui: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(ui));
}
const boxes = () => [...document.querySelectorAll<HTMLInputElement>(".wn-otp input")];
async function type(el: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const key = (el: HTMLInputElement, k: string) => act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));
const values = () => boxes().map((b) => b.value).join("");

describe("CodeField", () => {
  it("has six boxes with a name each, the autofill hint on the first, and a group named by the label", async () => {
    await mount(<Probe />);
    expect(boxes()).toHaveLength(6);
    expect(boxes().map((b) => b.getAttribute("autocomplete"))).toEqual(["one-time-code", "off", "off", "off", "off", "off"]);
    expect(boxes()[2]!.getAttribute("aria-label")).toBe(en["auth.digit"].replace("{n}", "3"));
    const group = document.querySelector("[role=group]")!;
    expect(document.getElementById(group.getAttribute("aria-labelledby")!)?.textContent).toBe("Authenticator code");
  });

  it("takes a digit in a box and moves on to the next one", async () => {
    await mount(<Probe />);
    await type(boxes()[0]!, "4");
    expect(document.activeElement).toBe(boxes()[1]);
    await type(boxes()[1]!, "8");
    expect(last).toBe("48");
    expect(values()).toBe("48");
  });

  it("ignores what is not a digit", async () => {
    await mount(<Probe />);
    await type(boxes()[0]!, "x");
    expect(last).toBe("");
    await type(boxes()[0]!, "7");
    await type(boxes()[1]!, "-");
    expect(last).toBe("7");
  });

  it("spreads a whole code typed or pasted into the first box over all six", async () => {
    await mount(<Probe />);
    await type(boxes()[0]!, "123 456");
    expect(last).toBe("123456");
    expect(values()).toBe("123456");
  });

  it("takes a code pasted from the clipboard, with its spaces", async () => {
    await mount(<Probe />);
    await act(async () => {
      const e = new Event("paste", { bubbles: true, cancelable: true }) as Event & { clipboardData: { getData: () => string } };
      e.clipboardData = { getData: () => "481 209" };
      boxes()[0]!.dispatchEvent(e);
    });
    expect(last).toBe("481209");
  });

  it("goes back a box on Backspace in an empty one and takes the digit before it, and walks with the arrows", async () => {
    await mount(<Probe />);
    await type(boxes()[0]!, "123");
    expect(document.activeElement).toBe(boxes()[3]);
    await key(boxes()[3]!, "Backspace");
    expect(last).toBe("12");
    expect(document.activeElement).toBe(boxes()[2]);
    await key(boxes()[2]!, "ArrowLeft");
    expect(document.activeElement).toBe(boxes()[1]);
    await key(boxes()[1]!, "ArrowRight");
    expect(document.activeElement).toBe(boxes()[2]);
  });

  it("marks the boxes and says what is wrong in a live line when there is an error", async () => {
    await mount(<Probe error="That code didn’t work" />);
    expect(boxes().every((b) => b.getAttribute("aria-invalid") === "true")).toBe(true);
    expect(document.querySelector("[role=alert]")?.textContent).toBe("That code didn’t work");
  });
});

describe("the main button of the page", () => {
  it("turns into a ring while it waits and cannot be pressed again, nothing dimmed", async () => {
    let n = 0;
    await mount(
      <GoButton busy onClick={() => n++}>
        Signing in
      </GoButton>,
    );
    const b = document.querySelector("button")!;
    expect(b.className).toContain("pointer-events-none");
    expect(b.disabled).toBe(false); // waiting is not the same as unavailable
    expect(b.querySelector("svg")).toBeNull();
    expect(b.querySelector("span")).not.toBeNull(); // the ring
  });

  it("says what went wrong under the passkey button, in the failure colour", async () => {
    await mount(<PasskeyBlock label="Try again" hint="That did not work" bad onClick={() => {}} />);
    const note = document.querySelector("p")!;
    expect(note.textContent).toBe("That did not work");
    expect(note.className).toContain("text-danger-text");
    expect(note.getAttribute("aria-live")).toBe("polite");
  });

  it("offers another way as a divider that says whether it is open", async () => {
    await mount(<OtherWay open label="another way" onToggle={() => {}} />);
    expect(document.querySelector("button")?.getAttribute("aria-expanded")).toBe("true");
  });
});

describe("TotpEnroll: the QR code of the secret comes from the server, drawn here", () => {
  it("draws the code the server sent beside the secret typed by hand, in groups of four", async () => {
    await mount(<TotpEnroll qr={demoQr} secret="JBSWY3DPEHPK3PXP" caption="Scan it" />);
    expect(document.querySelector("svg[role=img]")!.getAttribute("aria-label")).toBe("Scan it");
    expect(document.querySelectorAll("svg[role=img] path")).toHaveLength(1);
    expect(host!.textContent).toContain("JBSW Y3DP EHPK 3PXP");
  });

  it("says so, in the same box, when the server sent no code (the secret is still there to type)", async () => {
    await mount(<TotpEnroll qr={undefined} secret="JBSWY3DPEHPK3PXP" caption="Scan it" />);
    expect(document.querySelector("svg[role=img]")).toBeNull();
    expect(host!.textContent).toContain("Could not draw the QR code.");
    expect(host!.textContent).toContain("JBSW Y3DP EHPK 3PXP");
  });
});
