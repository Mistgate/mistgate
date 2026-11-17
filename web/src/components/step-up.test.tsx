import { Code, ConnectError } from "@connectrpc/connect";
import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { StepUpRequiredSchema } from "@/gen/mistgate/admin/v1/auth_pb";
import { en } from "@/i18n/en";
import { ConfirmWindow, StepUpProvider, isStepUpCancelled, stepUpNeeded, useStepUp, type ConfirmView } from "./step-up";

const finishStepUp = vi.fn();
const beginStepUp = vi.fn();
const passkeys = vi.hoisted(() => ({ on: false }));
vi.mock("@/lib/api", () => ({
  auth: { finishStepUp: (...a: unknown[]) => finishStepUp(...a), beginStepUp: (...a: unknown[]) => beginStepUp(...a) },
  webauthnSupported: () => passkeys.on,
}));
const getPasskey = vi.fn();
vi.mock("@/lib/passkey", () => ({ getPasskey: (...a: unknown[]) => getPasskey(...a) }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  finishStepUp.mockReset();
  beginStepUp.mockReset();
  getPasskey.mockReset();
  passkeys.on = false;
});

const needTotp = () =>
  new ConnectError("step-up required", Code.PermissionDenied, undefined, [{ desc: StepUpRequiredSchema, value: { passkey: false, totp: true } }]);

let guarded: ReturnType<typeof useStepUp>;
function Probe() {
  const guard = useStepUp();
  useEffect(() => {
    guarded = guard;
  });
  return null;
}
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <StepUpProvider>
        <Probe />
      </StepUpProvider>,
    ),
  );
}
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");

describe("stepUpNeeded", () => {
  it("recognises the StepUpRequired detail on PERMISSION_DENIED and nothing else", () => {
    expect(stepUpNeeded(needTotp())).toMatchObject({ totp: true, passkey: false });
    expect(stepUpNeeded(new ConnectError("role", Code.PermissionDenied))).toBeNull();
    expect(stepUpNeeded(new ConnectError("x", Code.Internal))).toBeNull();
    expect(stepUpNeeded(new Error("boom"))).toBeNull();
  });
});

describe("useStepUp", () => {
  it("lets every other failure through untouched", async () => {
    await mount();
    const boom = new ConnectError("nope", Code.FailedPrecondition);
    await expect(guarded(() => Promise.reject(boom))).rejects.toBe(boom);
    expect(dialog()).toBeNull();
  });

  it("asks for an authenticator code, then runs the call again", async () => {
    await mount();
    finishStepUp.mockResolvedValue({ stepUpUntilUnix: 1 });
    const call = vi.fn().mockRejectedValueOnce(needTotp()).mockResolvedValue("done");
    let result: Promise<string> = Promise.resolve("");
    await act(async () => void (result = guarded(call)));
    expect(dialog()).not.toBeNull();
    expect(call).toHaveBeenCalledTimes(1);

    const input = dialog()!.querySelector<HTMLInputElement>("input[autocomplete=one-time-code]")!;
    await act(async () => {
      // React tracks the value through the native setter
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "123456");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => void dialog()!.querySelector<HTMLButtonElement>("button[type=submit]")!.click());
    await expect(result).resolves.toBe("done");
    expect(finishStepUp).toHaveBeenCalledWith({ totpCode: "123456" });
    expect(call).toHaveBeenCalledTimes(2);
  });

  it("rejects with a recognisable cancellation when the dialog is dismissed", async () => {
    await mount();
    const call = vi.fn().mockRejectedValue(needTotp());
    let settled: Promise<unknown> = Promise.resolve();
    await act(async () => void (settled = guarded(call).catch((e: unknown) => e)));
    const cancel = [...dialog()!.querySelectorAll("button")].find((b) => b.type === "button")!;
    await act(async () => void cancel.click());
    const err = await settled;
    expect(isStepUpCancelled(err)).toBe(true);
    expect(call).toHaveBeenCalledTimes(1); // never retried
    expect(finishStepUp).not.toHaveBeenCalled();
  });
});

const needBoth = () =>
  new ConnectError("step-up required", Code.PermissionDenied, undefined, [{ desc: StepUpRequiredSchema, value: { passkey: true, totp: true } }]);
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (el: Element | null | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const wait = (ms: number) => act(async () => void (await new Promise((r) => setTimeout(r, ms))));

// The window as the board draws it (PnConfirm): a ring, what is confirmed in a card, the main button, two quiet ones.
function show(view: ConfirmView, over: Partial<React.ComponentProps<typeof ConfirmWindow>> = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  return act(async () =>
    root!.render(
      <ConfirmWindow view={view} what="Install a node on 192.0.2.10" code="" onCode={() => {}} canKey canCode onKey={() => {}} onSubmitCode={() => {}} onSwap={() => {}} onCancel={() => {}} {...over} />,
    ),
  );
}
const popup = () => document.querySelector<HTMLElement>("[role=dialog]")!;
const ring = () => popup().querySelector(".cfm-o")!;

describe("the confirmation window", () => {
  it("asks first: the card says what is confirmed, the passkey is the main button, the code and cancel are quiet and equal", async () => {
    await show("idle");
    expect(popup().textContent).toContain(en["stepup.title"]);
    expect(popup().textContent).toContain("Install a node on 192.0.2.10");
    expect(popup().textContent).toContain(en["win.confirm.onBehalf"]);
    expect(button(en["stepup.passkey"])?.className).toContain("bg-accent");
    expect(button(en["stepup.code"])).toBeDefined();
    expect(button(en["common.cancel"])).toBeDefined();
    expect(ring().className).not.toContain("on");
  });

  it("without a card line says that the change needs a fresh confirmation", async () => {
    await show("idle", { what: undefined });
    expect(popup().textContent).toContain(en["win.confirm.whatDefault"]);
  });

  it("an admin with a passkey only has nothing to switch to: cancel takes the row", async () => {
    await show("idle", { canCode: false });
    expect(button(en["stepup.code"])).toBeUndefined();
    expect(button(en["common.cancel"])?.parentElement?.className).toContain("grid-cols-1");
  });

  it("an admin with a code only starts on the six boxes, with no passkey to go to", async () => {
    await show("code", { canKey: false });
    expect(popup().querySelectorAll(".wn-otp input")).toHaveLength(6);
    expect(button(en["win.confirm.byPasskey"])).toBeUndefined();
    expect(button(en["stepup.confirm"])?.disabled).toBe(true); // until the six digits are in
  });

  it("pulses the ring while the passkey is awaited and says what to do in the browser", async () => {
    await show("wait");
    expect(ring().className).toContain("on");
    expect(popup().textContent).toContain(en["win.confirm.waitBody"]);
    expect(button(en["auth.waitingPasskey"])?.className).toContain("pointer-events-none");
  });

  it("turns the ring red and the line with it when it failed, and the button asks again", async () => {
    await show("err", { error: "The passkey did not fit" });
    expect(popup().className).toContain("err");
    expect(popup().textContent).toContain(en["win.confirm.errTitle"]);
    const line = [...popup().querySelectorAll("p")].find((p) => p.textContent === "The passkey did not fit");
    expect(line?.className).toContain("text-danger-text");
    expect(button(en["win.retry"])).toBeDefined();
  });

  it("a wrong code keeps the boxes and shows the line", async () => {
    await show("code", { error: en["stepup.badCode"] });
    expect(popup().querySelectorAll(".wn-otp input")).toHaveLength(6);
    expect(popup().textContent).toContain(en["stepup.badCode"]);
    expect(popup().className).toContain("err");
  });

  it("when it is confirmed the ring fills and the buttons are gone", async () => {
    await show("ok");
    expect(ring().className).toContain("ok");
    expect(popup().textContent).toContain(en["win.confirm.okTitle"]);
    expect(popup().textContent).toContain(en["win.confirm.okBody"]);
    expect(button(en["stepup.passkey"])).toBeUndefined();
    expect(button(en["common.cancel"])).toBeUndefined();
  });

  it("is closed by its cross, and the cross is named", async () => {
    let closed = 0;
    await show("idle", { onCancel: () => closed++ });
    const cross = popup().querySelector<HTMLButtonElement>("button[aria-label]")!;
    expect(cross.getAttribute("aria-label")).toBe(en["common.cancel"]);
    await click(cross);
    expect(closed).toBe(1);
  });
});

describe("a confirmation by passkey", () => {
  async function ask() {
    passkeys.on = true;
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () =>
      root!.render(
        <StepUpProvider>
          <Probe />
        </StepUpProvider>,
      ),
    );
    const call = vi.fn().mockRejectedValueOnce(needBoth()).mockResolvedValue("done");
    let outcome: Promise<unknown> = Promise.resolve();
    await act(async () => void (outcome = guarded(call, "Install a node on 192.0.2.10").catch(() => {})));
    return { call, outcome: () => outcome };
  }

  it("shows the ring while the browser asks, then Confirmed for a moment, then goes on with the call", async () => {
    beginStepUp.mockResolvedValue({ optionsJson: "{}", ceremonyId: "c1" });
    getPasskey.mockResolvedValue("{}");
    finishStepUp.mockResolvedValue({ stepUpUntilUnix: 1 });
    const { call, outcome } = await ask();
    expect(popup().textContent).toContain("Install a node on 192.0.2.10");
    await click(button(en["stepup.passkey"]));
    await wait(0);
    expect(finishStepUp).toHaveBeenCalledWith({ ceremonyId: "c1", credentialJson: "{}" });
    expect(ring().className).toContain("ok"); // the tick is drawn before the call goes on
    expect(call).toHaveBeenCalledTimes(1);
    await wait(1_000);
    await outcome();
    expect(call).toHaveBeenCalledTimes(2);
    expect(document.querySelector("[role=dialog]")).toBeNull();
  });

  it("says why when the passkey is refused, and tries again from the same window", async () => {
    beginStepUp.mockResolvedValue({ optionsJson: "{}", ceremonyId: "c1" });
    getPasskey.mockRejectedValueOnce(new DOMException("cancelled", "NotAllowedError")).mockResolvedValue("{}");
    finishStepUp.mockResolvedValue({ stepUpUntilUnix: 1 });
    await ask();
    await click(button(en["stepup.passkey"]));
    await wait(0);
    expect(popup().className).toContain("err");
    expect(button(en["win.retry"])).toBeDefined();
    await click(button(en["win.retry"]));
    await wait(0);
    expect(ring().className).toContain("ok");
    await wait(1_000);
  });

  it("switches to the code and back, keeping the window", async () => {
    await ask();
    await click(button(en["stepup.code"]));
    expect(popup().querySelectorAll(".wn-otp input")).toHaveLength(6);
    await click(button(en["win.confirm.byPasskey"]));
    expect(button(en["stepup.passkey"])).toBeDefined();
  });
});
