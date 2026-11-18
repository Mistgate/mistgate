import { afterEach } from "vitest";
import { setEffectKind, setEffectsAllowed, setEffectsEnabled, type FxKind } from "@/lib/effects";

// The motion of the interface is on by default, and a test that wants a still screen asks for it (setEffectsEnabled(false)).
// After each test the choice goes back to the default, whatever the test did with it, so one test never meets another's.
afterEach(() => {
  setEffectsEnabled(true);
  setEffectsAllowed(true);
  for (const kind of ["create", "save", "pages"] satisfies FxKind[]) setEffectKind(kind, true);
});
