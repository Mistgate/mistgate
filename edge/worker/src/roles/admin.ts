import "../../dist/wasm_exec.js";
import wasm from "../../dist/admin.wasm";
import { createRuntime } from "../runtime";
import { adminRole } from "./shared";

// The entry of `<name>-admin`: the SPA, the Connect API, sign-in and the first-run setup, the only role that creates the
// instance. Reached through FRONT's ADMIN service binding.
export default adminRole(createRuntime(wasm));
