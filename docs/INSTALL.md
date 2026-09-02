# Installation

Use a published release unless you need a source build or distribution package. Published releases require no Go toolchain.

| Platform and scope | Recommended package |
|---|---|
| Linux or command line macOS, current user | Installation script, no root |
| Ubuntu or Debian, all users | `.deb`, root required |
| Windows AMD64 | `.msi` |
| Windows ARM64 | Portable `.zip` |
| macOS desktop | `.app` archive |
| Other supported systems | Portable archive |

## 0. Install a published release

**Linux and macOS, current user, no root:**

```bash
curl -fsSL https://raw.githubusercontent.com/dennis2lee/localcode/main/scripts/install.sh | sh
```

The script verifies and installs one static binary at `~/.local/bin/localcode`. It writes nothing outside `$HOME` and requests no password. See [Install on Linux](#install-on-linux) for options and upgrades.

Other packages are on the [releases page](https://github.com/dennis2lee/localcode/releases). Platform instructions: [Linux](#install-on-linux), [Windows](#install-on-windows), and [macOS](#install-on-macos).

## 1. Build from source

### Requirements

| Tool | Needed for | Install |
|---|---|---|
| Go 1.26.5 or newer | Everything | `brew install go` |
| `lipo` | macOS `.app` bundle | Ships with Xcode Command Line Tools |
| `zip` | Windows zip | Ships with macOS and Linux |
| `msitools` | Windows `.msi` | `brew install msitools` |

`msitools` builds the `.msi` on macOS or Linux. The Linux `.deb` is written by `./build/deb` through `internal/debpkg`, without `dpkg-deb`. Both packages can be built on macOS.

The version in go.mod is the minimum. `go build` rejects older toolchains.

### Build

```bash
git clone https://github.com/dennis2lee/localcode.git
cd localcode
go build -o localcode ./cmd/localcode
```

Run the source build:

```bash
./localcode --agent general-purpose
```

## 2. Build distribution packages

Run `make check` first. `make dist` requires a successful check stamp that matches the current tree.

```bash
make check
make dist VERSION=x.y.z GUI_EXE=path/to/localcode-gui.exe   # everything
make dist-mac VERSION=x.y.z        # macOS binary and .app
make dist-mac-gui VERSION=x.y.z    # macOS desktop-window .app
make dist-linux VERSION=x.y.z      # Linux tar.gz and .deb
make dist-windows VERSION=x.y.z    # Windows zips only
make dist-msi VERSION=x.y.z GUI_EXE=...   # Windows msi only
```

`VERSION` is included in each binary and filename. The default is `0.1.0`. `make dist` runs `scripts/release-preflight.sh` and rejects stale version documentation. See [RELEASING.md](../RELEASING.md). `GUI_EXE` must reference a Windows build of `localcode-gui.exe`. This CGo artifact comes from CI (`gui-windows.yml`) and cannot be built on macOS.

Output:

| Platform | Path | Form |
|---|---|---|
| macOS | `dist/mac/localcode-<version>-darwin-universal.tar.gz` | Plain binary, universal for Intel and Apple Silicon |
| macOS | `dist/mac/LocalCode-<version>-darwin-universal-app.tar.gz` | `.app` bundle, double click to launch, opens a terminal |
| Linux | `dist/linux/localcode-<version>-linux-amd64.deb` | Debian and Ubuntu package, installs `/usr/bin/localcode` |
| Linux | `dist/linux/localcode-<version>-linux-arm64.deb` | The same for ARM64 |
| Linux | `dist/linux/localcode-<version>-linux-amd64.tar.gz` | Portable static binary |
| Linux | `dist/linux/localcode-<version>-linux-arm64.tar.gz` | Portable static binary, ARM64 |
| Windows | `dist/windows/localcode-<version>-windows-amd64.msi` | Installer for 64 bit Intel and AMD, adds a Start menu shortcut and registers PATH |
| Windows | `dist/windows/localcode-<version>-windows-amd64.zip` | Portable zip, 64 bit Intel and AMD |
| Windows | `dist/windows/localcode-<version>-windows-arm64.zip` | Portable zip for ARM64 devices such as Surface |

### Install on macOS

```bash
tar xzf dist/mac/LocalCode-<version>-darwin-universal-app.tar.gz -C /Applications
```

The `.app` has no Apple Developer ID signature or notarization. If Gatekeeper blocks the first launch:

1. Right click `LocalCode.app` in Finder and choose Open.
2. Click Open again in the warning dialog.

Distribution signing requires an Apple Developer account, `codesign --sign "Developer ID Application: ..." LocalCode.app`, and `xcrun notarytool submit`.

### Install on Linux

**Current user, no root**

```bash
curl -fsSL https://raw.githubusercontent.com/dennis2lee/localcode/main/scripts/install.sh | sh
```

The script downloads the release for the current architecture, verifies the published SHA-256, and installs one static binary at `~/.local/bin/localcode`. It writes nothing outside `$HOME`, uses no package manager, and requests no password.

Ubuntu `~/.profile` adds an existing `~/.local/bin` to PATH after login. The script prints a `~/.bashrc` entry when the current shell does not include it.

Options go after `-s --`:

```bash
curl -fsSL .../install.sh | sh -s -- --version 0.49.0   # a specific release
curl -fsSL .../install.sh | sh -s -- --dir ~/bin        # somewhere else
curl -fsSL .../install.sh | sh -s -- --uninstall        # remove it again
```

Run the installer again to upgrade. The new binary is renamed into place, so an active process continues with its original file. `--uninstall` removes the binary and preserves `~/.localcode`, including configuration and sessions.

The same script provides a command line macOS install without the `.app`.

**Debian package: Ubuntu, Debian, and other systems with `apt`; root required**

```bash
sudo apt install ./localcode-<version>-linux-amd64.deb
```

Tested on Ubuntu 24.04. The package installs `/usr/bin/localcode` for all users. Installing a newer package upgrades in place. Remove it with `sudo apt remove localcode`. Use the script for a current user install.

The `CGO_ENABLED=0` static binary requires no libc, runtime, Node, or Python. The package has no `Depends:` entry. ARM64 systems require the `-linux-arm64.deb` package.

The unsigned package is not in an apt repository. Install it from a local path. `apt update` does not offer upgrades; localcode can check GitHub directly. See [USAGE.md](USAGE.md#checking-for-updates).

Common errors:

| What apt says | What happened |
|---|---|
| `E: Unable to locate package localcode-0.50.0-linux-amd64.deb` | Missing `./`. Without a slash, apt treats the argument as a repository package name. Use `./localcode-...deb` or an absolute path. |
| `dpkg: error processing archive ... (--unpack)`, `package architecture (amd64) does not match system (arm64)` | Wrong architecture. Check with `dpkg --print-architecture`. ARM systems, including Linux virtual machines on Apple Silicon, require `-linux-arm64.deb`. |

Ubuntu 24.04 verification covers both errors, AMD64 and ARM64 package installation, PATH registration, and `localcode version` output.

**Tarball, portable, any distribution**

```bash
tar xzf localcode-<version>-linux-amd64.tar.gz
./localcode --agent general-purpose
```

The archive contains the same static binary used by the installation script. Manual extraction avoids piping a downloaded script into a shell.

Linux has no desktop window. Its native webview would require CGo, WebKitGTK, and distribution specific builds. Linux supports the daemon, TUI, and browser Web UI.

### Install on Windows

**MSI, recommended, amd64**

Open `localcode-<version>-windows-amd64.msi` and follow the installer. It installs to `C:\Program Files\LocalCode\`, adds a Start menu shortcut, and registers PATH. A fixed MSI `UpgradeCode` supports upgrades in place.

The MSI is unsigned. SmartScreen may show "Windows protected your PC". Choose More info and then Run, or sign the package before distribution. Use `signtool sign` on Windows or `osslsigncode` on another platform.

**Zip, portable, amd64 and arm64**

Extract `localcode.exe` to the required directory and run it. The archive provides no installer or PATH registration.

ARM64 is available only as a zip. `wixl` 0.106 rejects `-a arm64`, so the project cannot build an ARM64 MSI.

## 3. Prepare the config file

Create config.json before running localcode. See [USAGE.md](USAGE.md) for field definitions.

```bash
mkdir -p ~/.localcode
cp config.example.json ~/.localcode/config.json
```

Edit `~/.localcode/config.json`. Set the Bedrock region and model IDs, or the local LLM address.

## 4. AWS credentials, if you use a Bedrock profile

localcode uses the standard AWS credential chain. Set up any one of these:

* `aws configure` for access keys
* `aws sso login` for SSO
* Environment variables `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`
* An EC2 or container instance role

Model access for the Claude models you plan to use must be enabled in the Bedrock console, in that same region.

## 5. MCP servers, optional

| MCP configuration | Local requirements |
|---|---|
| `mcp_servers` entry with `command` | Executable and its dependencies for stdio transport. Example: `npx -y @modelcontextprotocol/server-github` requires Node.js and npm. |
| Entry with `url` | No local server installation for remote `http` or `sse` transport. |

Use `localcode mcp add` to write entries. See [USAGE.md](USAGE.md#managing-mcp-servers-with-localcode-mcp).

See [USAGE.md](USAGE.md#config-file-configjson) for the full configuration.
