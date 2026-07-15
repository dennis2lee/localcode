MODULE      := localcode
BIN_NAME    := localcode
VERSION     ?= 0.1.0
DIST        := dist
LDFLAGS     := -s -w -X main.version=$(VERSION)

.PHONY: build test clean dist dist-mac dist-windows

build:
	go build -o $(BIN_NAME) ./cmd/localcode

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

dist: dist-mac dist-windows
	@echo "Packages written to $(DIST)/"
