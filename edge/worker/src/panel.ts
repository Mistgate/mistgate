import "../dist/wasm_exec.js";
import panelModule from "../dist/panel.wasm";
import { createRuntime } from "./runtime";

// The single-script edition's panel: dist/panel.wasm, the program with every role. The split edition's scripts build their
// own runtime from their own wasm (src/roles); the code behind both is src/runtime.ts.
export type { PanelApi, ServeHooks } from "./runtime";

const runtime = createRuntime(panelModule);

/** The initialised panel of this isolate. Concurrent first requests share one init; a failed init is retried by the next request. */
export const getPanel = runtime.getPanel;

/** The initialised panel of the Provisioner's isolate (the serve role). Concurrent callers share one init; a failed init is retried. */
export const getServePanel = runtime.getServePanel;
