#!/usr/bin/env bash
# Build build/icon/localcode.icns, the macOS application icon, from the three
# SVG sources.
#
# The .icns is committed beside the sources on purpose, for the reason
# localcode.ico is (see make-ico.py): a release must not depend on a
# rasterizer being installed. Re-run this only when the artwork changes, then
# commit the result. The macOS packaging scripts copy the committed file into
# each .app; cmd/localcode/macicon_test.go checks that they do and that the
# file is a whole icon made from this artwork.
#
# Sizes come from three drawings, not one downscaled, for the reason
# make-ico.py gives: 16px renders icon-16.svg, 32px renders icon-small.svg,
# and 64px and up render icon.svg. The 16px and 32px images are used as they
# are for the Retina sizes below them as well (16@2x is the 32px image).
#
# Needs macOS, swiftc and iconutil, which come with the Xcode Command Line
# Tools. AppKit draws the SVG because it keeps the rounded corners
# transparent. QuickLook's thumbnailer (qlmanage -t) flattens them onto white,
# which shows as a white square behind the icon.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="$HERE/localcode.icns"

if [ "$(uname)" != "Darwin" ]; then
	echo "make-icns.sh must run on macOS (it draws with AppKit and packs with iconutil)" >&2
	exit 1
fi
for tool in swiftc iconutil; do
	command -v "$tool" >/dev/null 2>&1 || { echo "make-icns.sh needs $tool (Xcode Command Line Tools)" >&2; exit 1; }
done

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# svg -> png at an exact pixel size, with alpha.
cat > "$WORK/render.swift" <<'SWIFT'
import AppKit

let args = CommandLine.arguments
guard args.count == 4, let size = Int(args[3]) else {
	FileHandle.standardError.write("usage: render in.svg out.png size\n".data(using: .utf8)!)
	exit(2)
}
guard let image = NSImage(contentsOf: URL(fileURLWithPath: args[1])) else {
	FileHandle.standardError.write("cannot read \(args[1]) as an image\n".data(using: .utf8)!)
	exit(1)
}
guard let rep = NSBitmapImageRep(
	bitmapDataPlanes: nil, pixelsWide: size, pixelsHigh: size, bitsPerSample: 8, samplesPerPixel: 4,
	hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0
) else {
	FileHandle.standardError.write("cannot allocate a \(size)px bitmap\n".data(using: .utf8)!)
	exit(1)
}
rep.size = NSSize(width: size, height: size)
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
NSGraphicsContext.current?.imageInterpolation = .high
NSColor.clear.set()
NSRect(x: 0, y: 0, width: size, height: size).fill(using: .copy)
image.draw(in: NSRect(x: 0, y: 0, width: size, height: size), from: .zero, operation: .sourceOver, fraction: 1.0)
NSGraphicsContext.restoreGraphicsState()
guard let png = rep.representation(using: .png, properties: [:]) else {
	FileHandle.standardError.write("cannot encode the png\n".data(using: .utf8)!)
	exit(1)
}
try png.write(to: URL(fileURLWithPath: args[2]))
SWIFT
swiftc -O -o "$WORK/render" "$WORK/render.swift"

SET="$WORK/localcode.iconset"
mkdir "$SET"
render() { "$WORK/render" "$HERE/$1" "$SET/$2" "$3"; }

render icon-16.svg    icon_16x16.png        16
render icon-small.svg icon_16x16@2x.png     32
render icon-small.svg icon_32x32.png        32
render icon.svg       icon_32x32@2x.png     64
render icon.svg       icon_128x128.png      128
render icon.svg       icon_128x128@2x.png   256
render icon.svg       icon_256x256.png      256
render icon.svg       icon_256x256@2x.png   512
render icon.svg       icon_512x512.png      512
render icon.svg       icon_512x512@2x.png   1024

iconutil -c icns "$SET" -o "$OUT"
echo "wrote $OUT ($(wc -c < "$OUT" | tr -d ' ') bytes)"
