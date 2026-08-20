// Command deb writes the Debian package for a cross-compiled localcode
// binary. See internal/debpkg for why this is Go rather than dpkg-deb.
//
//	go run ./build/deb -version 0.48.0 -arch amd64 \
//	    -bin dist/linux/localcode -out dist/linux/localcode-0.48.0-linux-amd64.deb
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"localcode/internal/debpkg"
)

// stamp is the modification time written into the package.
//
// Fixed rather than "now" so two builds of one release produce identical
// bytes — the version is what identifies a build, and a timestamp only
// makes two copies of the same thing look different. 2026-01-01 UTC
// because a .deb full of 1970 is the kind of thing that makes someone
// wonder what else is wrong.
var stamp = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func main() {
	version := flag.String("version", "", "package version, e.g. 0.48.0")
	arch := flag.String("arch", "", "Debian architecture: amd64 or arm64")
	bin := flag.String("bin", "", "path to the linux binary to package")
	out := flag.String("out", "", "path of the .deb to write")
	license := flag.String("license", "LICENSE", "path to the licence text")
	flag.Parse()

	if err := run(*version, *arch, *bin, *out, *license); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(version, arch, bin, out, license string) error {
	for name, value := range map[string]string{"-version": version, "-arch": arch, "-bin": bin, "-out": out} {
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if arch != "amd64" && arch != "arm64" {
		// Debian's names happen to match Go's for the two localcode
		// publishes. They do not in general (386/i386, arm/armhf), so a
		// third architecture means a mapping rather than another case.
		return fmt.Errorf("architecture %q is not one this packages (amd64, arm64)", arch)
	}

	program, err := os.ReadFile(bin)
	if err != nil {
		return fmt.Errorf("reading the binary: %w", err)
	}
	copyright, err := os.ReadFile(license)
	if err != nil {
		return fmt.Errorf("reading the licence: %w", err)
	}

	pkg := debpkg.Package{
		Name:         "localcode",
		Version:      version,
		Architecture: arch,
		Maintainer:   "dennis2lee <74128179+dennis2lee@users.noreply.github.com>",
		Homepage:     "https://github.com/dennis2lee/localcode",
		Section:      "devel",
		Priority:     "optional",
		Synopsis:     "Coding agent for Bedrock, the Anthropic API, and local LLMs",
		Description: "localcode is a coding agent that talks to Amazon Bedrock, the Anthropic\n" +
			"API, and any OpenAI-compatible endpoint such as LM Studio or vLLM. The\n" +
			"model calls tools itself for file reads and writes, shell execution, MCP\n" +
			"and Skills.\n" +
			"\n" +
			"The core runs as a headless daemon with an HTTP and SSE API. A terminal\n" +
			"UI and a browser Web UI attach to it as equal clients, so one daemon can\n" +
			"serve both at once.",
		ModTime: stamp,
		Files: []debpkg.File{
			{Path: "/usr/bin/localcode", Mode: 0o755, Data: program},
			{Path: "/usr/share/doc/localcode/copyright", Mode: 0o644, Data: copyright},
		},
	}

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	if err := pkg.Build(f); err != nil {
		f.Close()
		os.Remove(out)
		return err
	}
	return f.Close()
}
