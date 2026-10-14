import "../../dist/wasm_exec.js";
import wasm from "../../dist/link.wasm";
import { createRuntime } from "../runtime";
import { linkEntrypoint, linkRole } from "./shared";

// The entry of `<name>-link`: the Go side of the agent link. FRONT reaches its fetch through the LINK service binding and
// the NodeLink objects of FRONT reach PanelLink through the PANEL_LINK binding (entrypoint = "PanelLink").
const runtime = createRuntime(wasm);
export class PanelLink extends linkEntrypoint(runtime) {}
export default linkRole(runtime);
