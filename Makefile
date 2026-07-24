MODULE      := localcode
BIN_NAME    := localcode
VERSION     ?= 0.1.0
DIST        := dist
LDFLAGS     := -s -w -X main.version=$(VERSION)

.PHONY: build gui-mac test clean release-check dist dist-mac dist-mac-gui dist-windows dist-msi

build:
	go build -o $(BIN_NAME) ./cmd/localcode

# --- native desktop window (experimental) ---
# Built with -tags gui, which links a native webview through CGo (WKWebView
# here). CGo means this only builds for the OS you run it on — it cannot be
# cross-compiled the way the pure-Go `build`/`dist` targets are, so there is
# no gui-windows target here; that binary is built on Windows. Run the result
# with `./localcode-gui --gui`.
gui-mac:
	go build -tags gui -ldflags "$(LDFLAGS)" -o $(BIN_NAME)-gui ./cmd/localcode

# --- macOS: universal .app bundle of the native-window build ---
dist-mac-gui:
	./build/package-mac-gui.sh "$(VERSION)" "$(DIST)"

test:
	go test ./...

clean:
	rm -rf $(BIN_NAME) $(DIST)

# --- macOS: universal (arm64+amd64) binary, .app bundle, tar.gz ---
dist-mac:
	./build/package-mac.sh "$(VERSION)" "$(DIST)"

# --- Windows: amd64 + arm64 .exe, zipped ---
dist-windows:
	./build/package-windows.sh "$(VERSION)" "$(DIST)"

# --- Windows: amd64 .msi installer (needs `brew install msitools`) ---
# GUI_EXE must point at a Windows-built localcode-gui.exe (CGo, cannot be
# cross-compiled here): `gh run download <run-id> -n localcode-gui-windows-amd64`
dist-msi:
	./build/package-msi.sh "$(VERSION)" "$(DIST)" "$(GUI_EXE)"

# Refuses to build a release until the docs are updated for VERSION (see
# RELEASING.md). This is deliberately a prerequisite of dist so a release
# tarball cannot be produced with a stale CHANGELOG or broken doc links.
release-check:
	@./scripts/release-preflight.sh "$(VERSION)"

# GUI_EXE (required, see dist-msi above) makes this: make dist VERSION=x.y.z GUI_EXE=...
dist: release-check dist-mac dist-windows dist-msi
	@echo "Packages written to $(DIST)/"
