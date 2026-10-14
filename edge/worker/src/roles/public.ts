import "../../dist/wasm_exec.js";
import wasm from "../../dist/public.wasm";
import { createRuntime } from "../runtime";
import { publicRole } from "./shared";

// The entry of `<name>-public`: subscriptions, the user page, the decoy. No route and no workers.dev name: FRONT reaches it
// through its PUBLIC service binding.
export default publicRole(createRuntime(wasm));
