import { connect } from "cloudflare:sockets";
import "../../dist/wasm_exec.js";
import wasm from "../../dist/provision.wasm";
import { createRuntime } from "../runtime";
import { provisionRole } from "./shared";

// The entry of `<name>-provision` for the split edition: the Provisioner object, over the wasm of the provision role. It is
// src/provision-index.ts with a wasm of its own.
export { Provisioner } from "../provisioner";
export default provisionRole(createRuntime(wasm), connect);
