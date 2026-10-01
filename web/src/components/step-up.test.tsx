import { Code, ConnectError } from "@connectrpc/connect";
import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { StepUpRequiredSchema } from "@/gen/mistgate/admin/v1/auth_pb";
import { StepUpProvider, isStepUpCancelled, stepUpNeeded, useStepUp } from "./step-up";

const finishStepUp = vi.fn();
vi.mock("@/lib/api", () => ({
  auth: { finishStepUp: (...a: unknown[]) => finishStepUp(...a), beginStepUp: vi.fn() },
  webauthnSupported: () => false,
}));

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
