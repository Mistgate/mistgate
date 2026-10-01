# Run from a POSIX shell (Git Bash or WSL on Windows).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
# BUILT orders releases (Unix time of the commit); RELEASE_KEY is the owner's release public key (base64, printed by
# `mistgate release keygen`). Without RELEASE_KEY the binaries refuse self-update ("unsigned build").
BUILT   ?= $(shell git log -1 --format=%ct 2>/dev/null || echo 0)
RELEASE_KEY ?=
BI      := github.com/mistgate/mistgate/internal/buildinfo
LDFLAGS := -s -w -X $(BI).Version=$(VERSION) -X $(BI).Built=$(BUILT) -X $(BI).ReleaseKey=$(RELEASE_KEY)
TARGETS := linux/amd64 linux/arm64
CMDS    := mistgate mistgate-node

.PHONY: gen web build test dev

gen:
	go tool buf lint && go tool buf generate

web:
	cd web && pnpm install --frozen-lockfile && pnpm build

# Static linux binaries into bin/<cmd>-<os>-<arch>; the SPA is embedded, so it builds first.
build: web
	@mkdir -p bin
	@for t in $(TARGETS); do for c in $(CMDS); do \
		echo "bin/$$c-$${t%/*}-$${t#*/}"; \
		CGO_ENABLED=0 GOOS=$${t%/*} GOARCH=$${t#*/} go build -trimpath -ldflags "$(LDFLAGS)" \
			-o bin/$$c-$${t%/*}-$${t#*/} ./cmd/$$c || exit 1; \
	done; done

test:
	go vet ./... && go test ./...
	cd web && pnpm typecheck && pnpm lint && pnpm test

# Panel in dev mode (decoy :8080, admin :8081). Run `cd web && pnpm dev` beside it for hot reload on :5173.
dev:
	go run ./cmd/mistgate serve --dev
