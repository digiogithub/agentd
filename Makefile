# agentd build helpers. CI and releases call `go build` directly
# (.github/workflows, docs/release.md); this file is for local work.
#
#   make                 build/agentd (the CLI + daemon; never links MyGo)
#   make relay           build/agentd-relay (self-hosted relay server)
#   make test            go test -race ./...
#   make desktop         build/agentd-desktop (MyGo, embeds libghostty-vt)
#   make desktop-run-fake  run agentd-desktop on the fake backend (no host contacted)
#   make deb VERSION=x.y.z dist/agentd_<v>_<arch>.deb: CLI + daemon + desktop app
#   make help            list the targets

GO          ?= go
# MyGo v0.7.1 needs Go >= 1.27.3 (go.mod); the toolchain is pinned here so a
# newer local default does not change the build.
GOTOOLCHAIN ?= go1.27.3
export GOTOOLCHAIN

BUILD_DIR   := build
VERSION     ?= 0.0.0-dev
LDFLAGS     := -X github.com/digiogithub/agentd/internal/version.Version=$(VERSION)

# ------------------------------------------------------------
# Native libraries embedded into agentd-desktop (internal/desktopui/natives)
# ------------------------------------------------------------
# libghostty-vt (the desktop terminal, MyGo plugins/terminal) is fetched per
# target into internal/desktopui/natives/<goos>-<goarch>/ and embedded with
# go:embed, so every distribution is one self-contained binary. On darwin the
# dylib is signed there with the same Developer ID as agentd-desktop
# (MACOS_CODESIGN_WRAPPER, hardened runtime, timestamp) BEFORE go build:
# hardened-runtime library validation then accepts it without
# disable-library-validation.
# NATIVES_SIGN: auto (sign when SIGNING_IDENTITY/MACOS_SIGN_IDENTITY is set),
# required (fail without it; use it for release builds) or off.
NATIVES_DIR            := internal/desktopui/natives
NATIVES_TARGETS        := linux-amd64 linux-arm64 windows-amd64 windows-arm64 darwin-amd64 darwin-arm64
NATIVES_SIGN           ?= auto
MACOS_CODESIGN_WRAPPER ?= $(CURDIR)/scripts/codesign-macos
HOST_NATIVES           := $(shell $(GO) env GOOS 2>/dev/null)-$(shell $(GO) env GOARCH 2>/dev/null)

.DEFAULT_GOAL := build

.PHONY: build relay test vet fmt desktop desktop-run-fake desktop-natives desktop-natives-host desktop-natives-clean deb $(addprefix desktop-natives-,$(NATIVES_TARGETS)) clean help

## Build the agentd CLI + daemon into build/agentd (default target)
build:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BUILD_DIR)/agentd ./cmd/agentd

## Build the self-hosted relay server into build/agentd-relay
relay:
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BUILD_DIR)/agentd-relay ./cmd/agentd-relay

## Run the tests (race detector on, as CI)
test:
	$(GO) test -race -count=1 ./...

## gofmt check + go vet
vet:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...

## Build the native desktop client into build/agentd-desktop (fetches libghostty-vt for the host first)
desktop: desktop-natives-host
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BUILD_DIR)/agentd-desktop ./cmd/agentd-desktop

## Run agentd-desktop on the in-memory fake backend (mockup data, no host contacted)
desktop-run-fake: desktop
	AGENTD_DESKTOP_FAKE=1 $(BUILD_DIR)/agentd-desktop

## Fetch libghostty-vt for one target into the go:embed dir (darwin: signed, see NATIVES_SIGN)
$(addprefix desktop-natives-,$(NATIVES_TARGETS)): desktop-natives-%:
	@set -e; out="$$(GO='$(GO)' scripts/fetch-ghostty-vt $* $(NATIVES_DIR)/$*)"; echo "Embedding $$out"; \
	case "$*" in darwin-*) \
		if [ "$(NATIVES_SIGN)" = off ]; then echo "NATIVES_SIGN=off: $$out left with its upstream signature"; \
		elif [ -n "$${SIGNING_IDENTITY:-$${MACOS_SIGN_IDENTITY:-}}" ] && command -v codesign >/dev/null 2>&1; then \
			echo "Signing $$out with $(MACOS_CODESIGN_WRAPPER)..."; "$(MACOS_CODESIGN_WRAPPER)" "$$out"; \
			codesign --verify --strict --verbose=2 "$$out"; \
		elif [ "$(NATIVES_SIGN)" = required ]; then \
			rm -f "$$out"; echo "Error: $$out must be signed (set SIGNING_IDENTITY and MACOS_SIGN_KEYCHAIN_PATH, on macOS)." >&2; exit 1; \
		else echo "Warning: $$out not signed with a Developer ID (no SIGNING_IDENTITY or codesign); a hardened-runtime agentd-desktop will refuse to load it."; fi ;; \
	esac

## Fetch libghostty-vt for the host platform; offline it warns and builds with what is there
desktop-natives-host:
	@$(MAKE) --no-print-directory desktop-natives-$(HOST_NATIVES) || echo "Warning: could not fetch libghostty-vt for $(HOST_NATIVES); building without an embedded terminal library (MyGo looks it up or downloads it at run time)."

## Fetch libghostty-vt for every target
desktop-natives: $(addprefix desktop-natives-,$(NATIVES_TARGETS))

## Remove fetched native libraries (keeps the .keep placeholders)
desktop-natives-clean:
	find $(NATIVES_DIR) -mindepth 2 -type f ! -name .keep -delete

## Remove build/
clean:
	rm -rf $(BUILD_DIR)

## Build dist/agentd_<VERSION>_<arch>.deb with the CLI, the daemon (systemd user unit) and the
## desktop app (agentd-desktop + .desktop entry + icon); packaging/nfpm-full.yaml, host arch only
DEB_ARCH     := $(shell $(GO) env GOARCH 2>/dev/null)
NFPM_VERSION ?= v2.48.0
NFPM         ?= $(shell $(GO) env GOPATH 2>/dev/null)/bin/nfpm
deb: build desktop
	@test -x "$(NFPM)" || $(GO) install github.com/goreleaser/nfpm/v2/cmd/nfpm@$(NFPM_VERSION)
	@mkdir -p dist
	VERSION=$(VERSION) GOARCH=$(DEB_ARCH) "$(NFPM)" package --packager deb --config packaging/nfpm-full.yaml --target dist/agentd_$(VERSION)_$(DEB_ARCH).deb

## Show available targets
help:
	@awk '/^## /{d=substr($$0,4); next} d && /^[^\t#].*:/{t=$$1; sub(/:.*/,"",t); if (t ~ /^\$$/) t="desktop-natives-<goos>-<goarch>"; printf "  %-34s %s\n", t, d; d=""}' Makefile
