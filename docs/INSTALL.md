# Installation

Install a published release, then write one config file and start localcode in your project. A release needs no Go toolchain.

| Platform and scope | Recommended package |
|---|---|
| Linux or macOS from a terminal, current user | Installation script, no root |
| Ubuntu or Debian, all users | `.deb`, root required |
| Windows AMD64 | `.msi`, which also installs the desktop window |
| Windows ARM64 | Portable `.zip` |
| macOS, launched from Finder | `.app` archive, which opens the terminal client in Terminal |
| Other supported systems | Portable archive |

Sections:

1. [Install a published release](#1-install-a-published-release): [Linux and macOS script](#install-on-linux), [macOS app](#install-on-macos), [Windows](#install-on-windows)
2. [First run](#2-first-run): the config file, credentials, and choosing the workspace
3. [Updating and uninstalling](#3-updating-and-uninstalling)
4. [MCP servers](#4-mcp-servers-optional), optional
5. [Build from source](#5-build-from-source) and [distribution packages](#6-build-distribution-packages)

## 1. Install a published release

Packages are on the [releases page](https://github.com/dennis2lee/localcode/releases/latest). None is signed. The installation script verifies the published SHA-256.

### Install on Linux

**Current user, no root.** This also installs the command line client on macOS.

```bash
curl -fsSL https://raw.githubusercontent.com/dennis2lee/localcode/main/scripts/install.sh | sh
```

The script downloads the release for the current architecture, verifies the published SHA-256, and installs one static binary at `~/.local/bin/localcode`. It leaves nothing outside `$HOME`, uses no package manager, and requests no password.

When `~/.local/bin` is not on PATH, the script prints the line to add. It names `~/.zshrc` when the shell is zsh, the macOS default, and `~/.bashrc` otherwise. Open a new terminal after adding it. Ubuntu's own `~/.profile` adds an existing `~/.local/bin` from the next login.

Options go after `-s --`:

```bash
curl -fsSL https://raw.githubusercontent.com/dennis2lee/localcode/main/scripts/install.sh | sh -s -- --version 0.49.0   # a specific release
curl -fsSL https://raw.githubusercontent.com/dennis2lee/localcode/main/scripts/install.sh | sh -s -- --dir ~/bin        # somewhere else
curl -fsSL https://raw.githubusercontent.com/dennis2lee/localcode/main/scripts/install.sh | sh -s -- --uninstall        # remove it again
```

**Debian package: Ubuntu, Debian, and other systems with `apt`; root required**

```bash
sudo apt install ./localcode-<version>-linux-amd64.deb
```

Tested on Ubuntu 24.04. The package installs `/usr/bin/localcode` for all users. Installing a newer package upgrades in place. Remove it with `sudo apt remove localcode`. Use the script for a current user install.

The `CGO_ENABLED=0` static binary requires no libc, runtime, Node, or Python. The package has no `Depends:` entry. ARM64 systems require the `-linux-arm64.deb` package.

The unsigned package is not in an apt repository. Install it from a local path. `apt update` does not offer upgrades; localcode checks GitHub itself. See [USAGE.md](USAGE.md#checking-for-updates).

Common errors:

| What apt says | What happened |
|---|---|
| `E: Unable to locate package localcode-0.50.0-linux-amd64.deb` | Missing `./`. Without a slash, apt treats the argument as a repository package name. Use `./localcode-...deb` or an absolute path. |
| `dpkg: error processing archive ... (--unpack)`, `package architecture (amd64) does not match system (arm64)` | Wrong architecture. Check with `dpkg --print-architecture`. ARM systems, including Linux virtual machines on Apple Silicon, require `-linux-arm64.deb`. |

Ubuntu 24.04 verification covers both errors, AMD64 and ARM64 package installation, PATH registration, and `localcode version` output.

**Tarball, portable, any distribution**

```bash
tar xzf localcode-<version>-linux-amd64.tar.gz
./localcode
```

The archive contains the same static binary the installation script installs. Manual extraction avoids piping a downloaded script into a shell.

Linux has no desktop window. Its native webview would require CGo, WebKitGTK, and distribution specific builds. Linux supports the daemon, the terminal client, and the browser client.

### Install on macOS

The command line install is the script above. The `.app` is for launching from Finder:

```bash
tar xzf ~/Downloads/LocalCode-<version>-darwin-universal-app.tar.gz -C /Applications
```

The `.app` opens Terminal and runs the terminal client in it. It is not the desktop window: releases ship the window only for Windows. On macOS the window is a source build, `make dist-mac-gui`.

The `.app` has no Apple Developer ID signature or notarization, so Gatekeeper blocks the first launch. On macOS 15 and later:

1. Open `LocalCode.app` once and close the warning.
2. Open System Settings, then Privacy & Security, and click Open Anyway next to LocalCode.

Or remove the quarantine flag: `xattr -dr com.apple.quarantine /Applications/LocalCode.app`.

The `.app` starts in your home directory, so that is the workspace until you choose another. To work on a project, start `localcode` from a terminal in its directory instead.

### Install on Windows

**MSI, recommended, amd64**

Open `localcode-<version>-windows-amd64.msi` and follow the installer. It installs to `C:\Program Files\LocalCode\`:

* `localcode.exe`, the command line program, on PATH
* `localcode-gui.exe`, the desktop window
* Start menu entries for both: **LocalCode** and **LocalCode (Desktop)**
* a **LocalCode** icon on your desktop, which opens the window
* Microsoft's WebView2 runtime, installed when it is missing, since the window needs it

A fixed MSI `UpgradeCode` supports upgrades in place. Uninstall it from Settings, then Apps. Your `%USERPROFILE%\.localcode` folder, with config and conversations, is not removed.

The MSI is unsigned. SmartScreen may show "Windows protected your PC". Choose More info and then Run, or sign the package before distribution. Use `signtool sign` on Windows or `osslsigncode` on another platform.

**Zip, portable, amd64 and arm64**

Extract `localcode.exe` to a directory and run it. The archive provides no installer, no window, and no PATH registration.

ARM64 is available only as a zip. `wixl` 0.106 rejects `-a arm64`, so the project cannot build an ARM64 MSI.

## 2. First run

### The config file

localcode reads `~/.localcode/config.json`, and `.localcode/config.json` in the project. With neither, it stops and says where it looked.

The [README's First run section](../README.md#first-run) has a minimal config for each kind of model: the Anthropic API, Amazon Bedrock, and a local server. Each needs one provider, one profile, and `default_profile`. [config.example.json](../config.example.json) is the full reference, with a note on every key. As a starting point it fits only a machine that can reach all three of its providers.

**On Windows:**

1. Create the folder `%USERPROFILE%\.localcode`.
2. Save `config.json` there. In Notepad, set Save as type to All files, or the file is saved as `config.json.txt`.
3. Leave out `"shell"`. config.example.json names `/bin/bash`, which does not exist on Windows. Unset, localcode uses `sh` on PATH, then Git for Windows' `bash.exe`, then `cmd.exe`.

### Credentials

| Provider | How |
|---|---|
| Anthropic API | `localcode login anthropic` saves the key and prints a starter config. Or put `"api_key": "{env:ANTHROPIC_API_KEY}"` on the provider: localcode does not read `ANTHROPIC_API_KEY` unless the config names it. A provider with no key does not stop localcode; the first request through it says what to do. |
| Amazon Bedrock | The standard AWS credential chain: `aws configure`, `aws sso login`, the `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_SESSION_TOKEN` variables, or an instance role. `localcode login bedrock` signs in through IAM Identity Center without the AWS CLI. Model access must be enabled in the Bedrock console, in the same region. |
| Local server | Its address as `base_url`, and the model name it lists. See [MODELS.md](MODELS.md) for LM Studio, Ollama, vLLM and llama.cpp. |

### The workspace

The directory localcode starts in is the workspace: the model reads and edits files there.

* **Terminal:** `cd` into the project, then run `localcode`.
* **Windows window:** the Start menu entries start in `C:\Program Files\LocalCode`. Click the workspace path at the top of the window and choose your project folder.
* **Browser client:** open `http://127.0.0.1:4096` while localcode runs. The workspace path at the top changes it the same way.

## 3. Updating and uninstalling

localcode installs a newer release when it starts. Set `"auto_update": false` in config.json to stay on the installed version, including one installed with `--version`.

| Installed with | Upgrade | Remove |
|---|---|---|
| Script | Automatic, or run the script again. The new binary is renamed into place, so a running process keeps its file. | `install.sh --uninstall` |
| `.deb` | Install the newer package | `sudo apt remove localcode` |
| MSI | Automatic, or run the newer MSI | Settings, then Apps |

No uninstall removes `~/.localcode` (`%USERPROFILE%\.localcode` on Windows), where config and conversations live.

## 4. MCP servers, optional

| MCP configuration | Local requirements |
|---|---|
| `mcp_servers` entry with `command` | Executable and its dependencies for stdio transport. Example: `npx -y @modelcontextprotocol/server-github` requires Node.js and npm. |
| Entry with `url` | No local server installation for remote `http` or `sse` transport. |

Use `localcode mcp add` to write entries. See [USAGE.md](USAGE.md#managing-mcp-servers-with-localcode-mcp).

## 5. Build from source

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
./localcode
```

A source checkout has `config.example.json` at its root.

## 6. Build distribution packages

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
| macOS | `dist/mac/LocalCode-<version>-darwin-universal-app.tar.gz` | `.app` bundle, double click to launch, opens the terminal client in Terminal |
| macOS | `dist/mac-gui/LocalCode-<version>-darwin-universal-gui-app.tar.gz` | Desktop-window `.app`, from `make dist-mac-gui`, not attached to releases |
| Linux | `dist/linux/localcode-<version>-linux-amd64.deb` | Debian and Ubuntu package, installs `/usr/bin/localcode` |
| Linux | `dist/linux/localcode-<version>-linux-arm64.deb` | The same for ARM64 |
| Linux | `dist/linux/localcode-<version>-linux-amd64.tar.gz` | Portable static binary |
| Linux | `dist/linux/localcode-<version>-linux-arm64.tar.gz` | Portable static binary, ARM64 |
| Windows | `dist/windows/localcode-<version>-windows-amd64.msi` | Installer for 64 bit Intel and AMD, with the desktop window, Start menu entries and PATH |
| Windows | `dist/windows/localcode-<version>-windows-amd64.zip` | Portable zip, 64 bit Intel and AMD |
| Windows | `dist/windows/localcode-<version>-windows-arm64.zip` | Portable zip for ARM64 devices such as Surface |

Distribution signing on macOS requires an Apple Developer account, `codesign --sign "Developer ID Application: ..." LocalCode.app`, and `xcrun notarytool submit`.
