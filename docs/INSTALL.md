# Installation

## 1. Build from source

### Requirements

| Tool | Needed for | Install |
|---|---|---|
| Go 1.26.5 or newer | Everything | `brew install go` |
| `lipo` | macOS `.app` bundle | Ships with Xcode Command Line Tools |
| `zip` | Windows zip | Ships with macOS and Linux |
| `msitools` | Windows `.msi` | `brew install msitools` |

`msitools` builds the `.msi` on macOS or Linux, so you do not need a Windows machine. The Linux `.deb` needs nothing at all: it is written by `./build/deb` (see `internal/debpkg`), not by `dpkg-deb`, so a release can be cut from a Mac.

The version in go.mod is the floor. `go build` refuses an older toolchain rather than producing a binary that misbehaves.

### Build

```bash
git clone https://github.com/dennis2lee/localcode.git
cd localcode
go build -o localcode ./cmd/localcode
```

Run it right away:

```bash
./localcode --agent general-purpose
```

## 2. Build distribution packages

```bash
make dist VERSION=x.y.z GUI_EXE=path/to/localcode-gui.exe   # everything
make dist-mac VERSION=x.y.z        # macOS binary and .app
make dist-mac-gui VERSION=x.y.z    # macOS desktop-window .app
make dist-linux VERSION=x.y.z      # Linux tar.gz and .deb
make dist-windows VERSION=x.y.z    # Windows zips only
make dist-msi VERSION=x.y.z GUI_EXE=...   # Windows msi only
```

`VERSION` is stamped into the binary and into every filename; leaving it out builds everything as `0.1.0`. `make dist` also runs `scripts/release-preflight.sh`, which refuses to build when the docs are stale for that version — see [RELEASING.md](../RELEASING.md). `GUI_EXE` must point at a Windows-built `localcode-gui.exe`, which is CGo and comes from CI (`gui-windows.yml`), not from a Mac.

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

The `.app` is not signed or notarized with an Apple Developer ID. If Gatekeeper blocks the first launch:

1. Right click `LocalCode.app` in Finder and choose Open.
2. Click Open again in the warning dialog.

To sign it for distribution, run `codesign --sign "Developer ID Application: ..." LocalCode.app` and then notarize with `xcrun notarytool submit`. Both need an Apple Developer account.

### Install on Linux

**No root: one command, everything under your home directory**

```bash
curl -fsSL https://raw.githubusercontent.com/dennis2lee/localcode/main/scripts/install.sh | sh
```

This is the right one on a machine where you are not an administrator, which on a managed or shared Ubuntu box is most of them. It downloads the release tarball for your architecture, checks it against the SHA-256 GitHub publishes for that file, and puts one static binary in `~/.local/bin/localcode`. Nothing is written outside `$HOME`, no package manager is involved, and you are never asked for a password.

`~/.local/bin` is the directory Ubuntu's own `~/.profile` puts on PATH when it exists, so a new login finds it. The script prints the line to add to `~/.bashrc` if this shell does not have it yet.

Options go after `-s --`:

```bash
curl -fsSL .../install.sh | sh -s -- --version 0.49.0   # a specific release
curl -fsSL .../install.sh | sh -s -- --dir ~/bin        # somewhere else
curl -fsSL .../install.sh | sh -s -- --uninstall        # remove it again
```

Installing again over an existing copy is the upgrade: the new binary is renamed into place, so a localcode running at that moment keeps the file it started from. `--uninstall` removes the binary and leaves `~/.localcode` (your config and sessions) alone.

It works on macOS too, for a command-line install with no `.app`.

**Debian package: Ubuntu, Debian, and anything else with `apt`, if you have root**

```bash
sudo apt install ./localcode-<version>-linux-amd64.deb
```

Tested against Ubuntu 24.04. It installs `/usr/bin/localcode`, so it is on PATH for every user on the machine, and upgrades in place when you install a newer one. `sudo apt remove localcode` takes it off. Use this when localcode is for everyone on the box; use the script above when it is for you.

There is no `Depends:` line and nothing to satisfy: the binary is built with `CGO_ENABLED=0`, so it is static and needs no libc, no runtime, and no Node or Python. On ARM64 machines use the `-linux-arm64.deb`.

The package is unsigned and is not in any apt repository, so `apt` installs it from the file path you give it. `apt update` will never offer an upgrade; localcode checks GitHub itself (see [USAGE.md](USAGE.md#checking-for-updates)).

Two ways this goes wrong, both with an unhelpful message:

| What apt says | What happened |
|---|---|
| `E: Unable to locate package localcode-0.50.0-linux-amd64.deb` | The `./` is missing. Without a `/` in it, apt reads the argument as a package name to look up in a repository, not as a file. Write `./localcode-...deb`, or an absolute path. |
| `dpkg: error processing archive ... (--unpack)`, `package architecture (amd64) does not match system (arm64)` | The wrong file for this machine. `dpkg --print-architecture` says which one it wants; ARM machines (including a Linux VM on an Apple Silicon Mac) need the `-linux-arm64.deb`. |

Both are verified against Ubuntu 24.04, as is everything above: the `.deb` installs on amd64 and arm64, `localcode` lands on PATH, and `localcode version` reports the version installed.

**Tarball, portable, any distribution**

```bash
tar xzf localcode-<version>-linux-amd64.tar.gz
./localcode --agent general-purpose
```

One static binary. This is what the install script downloads; unpack it yourself if you would rather not pipe a script into a shell.

The desktop window is not built for Linux. It links a native webview through CGo, and on Linux that means WebKitGTK and a build per distribution; the daemon, the TUI and the Web UI in a browser are all there and are what a Linux install is.

### Install on Windows

**MSI, recommended, amd64**

Double click `localcode-<version>-windows-amd64.msi` and follow the wizard. It installs to `C:\Program Files\LocalCode\`, adds a Start menu shortcut, and registers PATH so `localcode` works from any directory. Reinstalling upgrades the previous version in place, because the MSI `UpgradeCode` is fixed.

The MSI is unsigned, so SmartScreen may show "Windows protected your PC". Choose More info and then Run, or sign it with a code signing certificate before distributing. Use `signtool sign` on Windows, or `osslsigncode` for a cross platform signing step.

**Zip, portable, amd64 and arm64**

Unzip, put `localcode.exe` wherever you want, and run it. There is no installer and no PATH registration.

ARM64 ships as a zip only. The `wixl` 0.106 build used here rejects `-a arm64`, so there is no ARM64 MSI yet.

## 3. Prepare the config file

localcode needs a config.json before it runs. See [USAGE.md](USAGE.md) for every field.

```bash
mkdir -p ~/.localcode
cp config.example.json ~/.localcode/config.json
```

Then edit `~/.localcode/config.json` and fill in your real Bedrock region and model IDs, or the address of your local LLM.

## 4. AWS credentials, if you use a Bedrock profile

localcode uses the standard AWS credential chain. Set up any one of these:

* `aws configure` for access keys
* `aws sso login` for SSO
* Environment variables `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`
* An EC2 or container instance role

Model access for the Claude models you plan to use must be enabled in the Bedrock console, in that same region.

## 5. MCP servers, optional

A server listed under `mcp_servers` with a `command` is launched over stdio using that executable. For example, `npx -y @modelcontextprotocol/server-github` requires Node.js and npm to be installed. A server with a `url` instead is a remote one (`http` or `sse`) and needs nothing installed locally. See [USAGE.md](USAGE.md#managing-mcp-servers-with-localcode-mcp) for `localcode mcp add`, which writes these entries for you.

See [USAGE.md](USAGE.md#config-file-configjson) for the full configuration.
