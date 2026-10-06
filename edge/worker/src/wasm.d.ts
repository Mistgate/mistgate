declare module "*.wasm" {
  const module: WebAssembly.Module;
  export default module;
}

// dist/wasm_exec.js (copied from the Go toolchain by scripts/build-edge.ps1) defines this global.
declare class Go {
  importObject: WebAssembly.Imports;
  run(instance: WebAssembly.Instance): Promise<void>;
}