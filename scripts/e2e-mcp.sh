#!/usr/bin/env bash
# API tokens and MCP against a real panel on loopback: builds the panel, runs scripts/e2e/mcpe2e in both
# admin layouts (own listener, secret path prefix), prints a table of checks. Works on Windows (Git Bash), macOS and Linux:
# plain HTTP on 127.0.0.1, a temp dir that is removed at the end. About a minute (the owner's step-up may wait for the
# next 30 s authenticator step). Extra arguments go to mcpe2e: -mode listener|prefix|both, -keep.
set -euo pipefail
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=${GOTOOLCHAIN:-auto}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
exe=""
case "$(go env GOOS)" in windows) exe=.exe ;; esac
go build -o "$tmp/mistgate$exe" ./cmd/mistgate
go run ./scripts/e2e/mcpe2e -bin "$tmp/mistgate$exe" "$@"
