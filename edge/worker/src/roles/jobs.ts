import "../../dist/wasm_exec.js";
import wasm from "../../dist/jobs.wasm";
import { createRuntime } from "../runtime";
import { jobsRole } from "./shared";

// The entry of `<name>-jobs`: the cron trigger (`* * * * *`) and nothing else; its fetch answers 404 without starting Go.
export default jobsRole(createRuntime(wasm));
