---
title: Releases and signing
description: For maintainers and for anyone who builds Mistgate with a key of their own; the release key, reproducible builds, signing and publishing a release, a bundle of your own, and rotating the key.
---

Mistgate trusts one Ed25519 release key: whoever holds its private half decides what node agents and panels may install. The official binaries carry the project's key, and the maintainers sign every GitHub release with it. If you install the official release, you need nothing on this page; [Updates](updates.md) covers running the fleet. This page is for the maintainers, and for anyone who builds the binaries with a key of their own.

## The release key

- The private half stays offline. The public half is stamped into both binaries at build time: `RELEASE_KEY` for `make build`, `--key` for `mistgate release build`, and the repository variable `MISTGATE_RELEASE_PUBLIC_KEY` in the release workflow.
- The panel saves its key in `<data-dir>/release.pub` on its first start. A `release.pub` that differs from the compiled-in key is never accepted silently, in either direction: the panel trusts no bundle until `mistgate release trust-key` (see "Rotate the release key" below).
- A build without a key is unsigned: its node agents never update themselves, and the panel cannot install panel releases or offer a trusted agent to the SSH installation.

### Make the key (once)

```sh
mistgate release keygen --out ~/mistgate-release.key
```

It writes the private key to the file (mode 0600; an existing file is never overwritten) and prints the public key and its fingerprint (the first 16 hex characters of its SHA-256).

> **Warning:** Keep the key file offline and back it up. Without it no node can be updated from the panel: you would make a new key, build new binaries with it, and update every node and the panel by hand once.

### Build with the public key

```sh
git checkout v0.1.4
RELEASE_KEY=<public key> VERSION=v0.1.4 make build
```

This builds `bin/mistgate-linux-{amd64,arm64}` and `bin/mistgate-node-linux-{amd64,arm64}` with `mistgate release build`. The panel and agents receive the same version, build time (the commit time of the checkout) and release public key; `mistgate version` and `mistgate-node version` print the key's fingerprint.

## Publish an official release

For every stable tag `vMAJOR.MINOR.PATCH`, `.github/workflows/release.yml` runs three jobs. `node` builds both Linux node agents with Go only (no Node.js, no npm packages); `panel` builds the admin SPA with pnpm and then the two panel binaries. Both have read-only access to the repository. `publish`, the only job that may write and the only one that runs no build code, creates a **draft** release with the four binaries, `BUILDINFO` and `SHA256SUMS`. Every action is pinned to a commit SHA. A draft is not visible to panels as the latest release. The workflow requires the repository variable `MISTGATE_RELEASE_PUBLIC_KEY` and stamps it into all four binaries; if it is missing or invalid, the workflow fails instead of publishing a release that cannot be signed.

The private key never goes to Actions, and you do not have to trust the CI's binaries: on a machine that holds the key, you build the release from the tag yourself, and `mistgate release sign` signs only binaries that your build reproduces byte for byte. Then you upload the four manifest and signature files and publish the draft. Within 10 minutes panels download and verify the node bundle (nodes stay on their builds until the owner updates or schedules them), and **Check GitHub** offers the panel release.

```sh
VERSION=v0.1.6
git clone https://github.com/Mistgate/mistgate.git && cd mistgate   # or git fetch --tags in your clone
git checkout "$VERSION"
(cd web && pnpm install --frozen-lockfile && pnpm build)           # the panel binary embeds the SPA
go build -o ../mistgate-signer ./cmd/mistgate                       # the signer; any version works
gh release download "$VERSION" --repo Mistgate/mistgate \
  --pattern 'mistgate-linux-*' --pattern 'mistgate-node-linux-*' --dir ../downloaded
../mistgate-signer release sign --key ~/mistgate-release.key --version "$VERSION" --expires 90d \
  ../downloaded/mistgate-node-linux-amd64 ../downloaded/mistgate-node-linux-arm64 \
  ../downloaded/mistgate-linux-amd64 ../downloaded/mistgate-linux-arm64 --out ../signed
gh release upload "$VERSION" ../signed/manifest.json ../signed/manifest.sig \
  ../signed/panel-manifest.json ../signed/panel-manifest.sig --repo Mistgate/mistgate --clobber
gh release edit "$VERSION" --repo Mistgate/mistgate --draft=false
```

`release sign` refuses unless the checkout is exactly the tag without local changes, then rebuilds every binary with `mistgate release build` and the public half of your key and compares it with the downloaded one. The last command publishes the draft: do not publish it before all four files have uploaded.

If signing refuses because a binary does not match its rebuild, do not sign: find out why first. The usual causes are a different key in `MISTGATE_RELEASE_PUBLIC_KEY`, a tag moved after the build, or a toolchain difference (below); an unexplained difference may mean a compromised runner.

### Reproducible builds

`mistgate release build` (used by the workflow, by `make build` and by the check in `release sign`) gives byte-identical binaries for the same tag and key; a Linux build and a Windows cross-build of one commit were checked to match. It holds when:

- the Go toolchain is the one in the `toolchain` line of `go.mod`. `release build` asks for exactly that version, and a different local Go downloads it once from the Go module proxy;
- the source is a clean checkout of the tag. Untracked files (the built SPA, `bin/`) are not stamped into the binary (`-buildvcs=false`); keep the repository's `.gitattributes`, which checks every file out with LF line endings on every system;
- the flags are the fixed ones `release build` sets: `CGO_ENABLED=0`, `-trimpath`, `GOAMD64=v1`, `GOARM64=v8.0`, and `-ldflags "-s -w"` with only `Version` (the tag), `Built` (the commit time of the tag) and `ReleaseKey`;
- for the panel binaries, the SPA is built with `pnpm install --frozen-lockfile && pnpm build` (Node.js 22, the pnpm version pinned in `web/package.json`).

The check proves that the CI built what the tag says. It does not vet the tag's code or its locked dependencies: a malicious package in `go.sum` or `pnpm-lock.yaml` builds the same on your machine.

### Keep the signature short-lived

`--expires` (default `30d`) is how long both manifests can be installed; a captured old manifest stops working when it runs out. Use a short lifetime such as `90d` and renew it before it ends: run the same `release sign` command for the same tag with a new `--expires` and upload the four files again with `--clobber`. Panels refresh the node bundle of the same build without updating nodes again, and they read the panel manifest anew at every check. A panel that finds an expired panel manifest says so and does not install the release; an expired node bundle can no longer be rolled out or used by the SSH installation.

## Use a bundle of your own

A panel built with your own key ignores the official GitHub releases: their signatures do not verify with its key. It gets node updates from the bundle you sign and copy into its data directory, and you replace the panel binary by hand (see "Early builds and manual updates" in [Updates](updates.md)).

### Sign the node binaries

```sh
mistgate release sign --key ~/mistgate-release.key --version v0.1.4 --expires 90d \
  bin/mistgate-node-linux-amd64 bin/mistgate-node-linux-arm64 --out dist/
```

- Run it in the checkout of the tag (or name it with `--source`): it refuses a checkout that is not exactly the tag or has local changes, rebuilds the binaries and refuses one that differs.
- The binaries must be named `<name>-<os>-<arch>`, as `make build` names them. `mistgate-linux-*` binaries given too go into `panel-manifest.json`.
- The build time comes from the tag's commit; a `--built` that differs is refused. A new build whose own build time differs from the manifest rolls itself back after the update (`built_mismatch`).
- `--expires` is a number of days (`90d`) or a Go duration (`2160h`); the default is `30d`. Keep it short and renew it (see above).
- The command writes `dist/manifest.json` and `dist/manifest.sig`, copies the binaries next to them, reads the result back and verifies it, then prints the version, the expiry, every file with its size and the key fingerprint.

### Put the bundle on the panel

```sh
scp dist/* panel.example.com:/var/lib/mistgate/dist/
```

The panel reads `<data-dir>/dist` and notices a change within a minute; **Read the folder again** on the Updates page reads it at once. Only regular files count (a symbolic link is not followed). Replace the whole bundle at once; changing it while a rollout runs pauses the rollout. The **Release bundle** card shows whether the signature verified: see [Updates](updates.md).

## Rotate the release key

1. Make a new key with `mistgate release keygen` and set `MISTGATE_RELEASE_PUBLIC_KEY` (or your own `RELEASE_KEY`) to its public half.
2. Every installed agent trusts only the old key: update each node by hand once with an agent built with the new key, as for an old agent (see "Old agents: update by hand once" in [Updates](updates.md)).
3. A panel installs panel releases only under its compiled-in key, which is still the old one: replace the panel binary by hand with one built with the new key.
4. The new panel logs `release key mismatch` and trusts no bundle. On the panel server run `mistgate release trust-key` (as root or the panel's service user, with `--data-dir` if yours is not `/var/lib/mistgate`); it writes the new key to `release.pub` and prints the old and new fingerprints. Restart the panel.

A key swapped into `release.pub` by someone else never makes the panel trust another signer: only the binary's compiled-in key can become the installation's key, and only through `trust-key`.
