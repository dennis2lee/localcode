# Installation

## 1. Build from source

### Requirements

| Tool | Needed for | Install |
|---|---|---|
| Go 1.23 or newer | Everything | `brew install go` |
| `lipo` | macOS `.app` bundle | Ships with Xcode Command Line Tools |
| `zip` | Windows zip | Ships with macOS and Linux |
| `msitools` | Windows `.msi` | `brew install msitools` |

`msitools` builds the `.msi` on macOS or Linux, so you do not need a Windows machine.

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
make dist            # everything under dist/mac and dist/windows, msi included
make dist-mac        # macOS only
make dist-windows    # Windows zip only
make dist-msi        # Windows msi only
```

Output:

| Platform | Path | Form |
|---|---|---|
| macOS | `dist/mac/localcode-<version>-darwin-universal.tar.gz` | Plain binary, universal for Intel and Apple Silicon |
| macOS | `dist/mac/LocalCode-<version>-darwin-universal-app.tar.gz` | `.app` bundle, double click to launch, opens a terminal |
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

Every server listed under `mcp_servers` in the config is launched over stdio using the executable named in its `command`. For example, `npx -y @modelcontextprotocol/server-github` requires Node.js and npm to be installed.

See [USAGE.md](USAGE.md#config-file-configjson) for the full configuration.
