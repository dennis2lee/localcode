MODULE      := localcode
BIN_NAME    := localcode
VERSION     ?= 0.1.0
DIST        := dist
LDFLAGS     := -s -w -X main.version=$(VERSION)

.PHONY: build gui-mac test clean dist dist-mac dist-windows dist-msi

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
dist-msi:
	./build/package-msi.sh "$(VERSION)" "$(DIST)"

dist: dist-mac dist-windows dist-msi
	@echo "Packages written to $(DIST)/"
