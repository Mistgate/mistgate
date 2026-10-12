// wasm_exec_node.js hands the whole process environment to the Go program, and wasm_exec.js refuses to start when
// arguments plus environment exceed its fixed limit (a Windows developer environment easily does). The tests need none of
// it, so keep only what a Go test can use. MG_FAKESSHD_* tell the edge SSH tests where the fake SSH server listens
// (cmd/mistgate-edge/testdata); the Node-side socket shim reads them from here too.
const keep = /^(GO[A-Z0-9_]*|MG_FAKESSHD_[A-Z]+|TMP|TEMP|TMPDIR|HOME|USERPROFILE)$/;
for (const name of Object.keys(process.env)) {
  if (!keep.test(name)) delete process.env[name];
}
