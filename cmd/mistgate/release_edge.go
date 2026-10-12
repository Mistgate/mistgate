package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mistgate/mistgate/internal/release"
)

// edgeRebuild builds the edge bundle; a test seam (goBuildEdge in production), like releaseRebuild.
var edgeRebuild = goBuildEdge

// goBuildEdge is the one definition of how mistgate-edge.tar.gz is built: CI, `release build` and the rebuild check of
// `release sign` all run it. The panel is cmd/mistgate-edge for js/wasm with the same flags and Go toolchain as the other
// release binaries (go.mod's toolchain line); wasm_exec.js is the one of that toolchain; the two Worker scripts are
// bundled by edge/worker/build-bundle.mjs with the esbuild pinned in edge/worker/package.json (run
// `pnpm install --frozen-lockfile` there first); the assets are web/dist (build the SPA first).
func goBuildEdge(src, version string, built int64, key, out string) error {
	worker := filepath.Join(src, "edge", "worker")
	if _, err := os.Stat(filepath.Join(worker, "node_modules", "esbuild", "package.json")); err != nil {
		return fmt.Errorf("esbuild is not installed in %s: run `pnpm install --frozen-lockfile` there first", worker)
	}
	toolchain, err := releaseToolchain(src)
	if err != nil {
		return err
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "mistgate-edge-build-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	run := func(dir string, env []string, name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir, cmd.Env, cmd.Stderr = dir, env, os.Stderr
		b, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return strings.TrimSpace(string(b)), nil
	}

	env := releaseEnv(toolchain, "GOOS=js", "GOARCH=wasm")
	wasm := filepath.Join(tmp, "panel.wasm")
	if _, err := run(src, env, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", releaseLdflags(version, built, key), "-o", wasm, "./cmd/mistgate-edge"); err != nil {
		return err
	}
	goroot, err := run(src, env, "go", "env", "GOROOT")
	if err != nil {
		return err
	}
	scripts := filepath.Join(tmp, "scripts")
	if _, err := run(worker, os.Environ(), "node", "build-bundle.mjs", filepath.Join(goroot, "lib", "wasm", "wasm_exec.js"), scripts); err != nil {
		return err
	}

	// A temporary file in the destination directory, renamed when complete: a half-written tarball is never left at out.
	f, err := os.CreateTemp(filepath.Dir(out), ".edge-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = release.WriteEdgeBundle(f, release.EdgeInputs{
		Version: version, WorkerJS: filepath.Join(scripts, "worker.js"), ProvisionJS: filepath.Join(scripts, "provision.js"),
		Wasm: wasm, AssetsDir: filepath.Join(src, "web", "dist"),
	})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(f.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(f.Name(), out)
}
