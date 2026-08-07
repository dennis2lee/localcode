#!/usr/bin/env python3
"""Build build/icon/localcode.ico from the two SVG sources.

The .ico is committed alongside the sources on purpose: the release
machine has neither cairosvg nor a browser, and an installer build must
not depend on a rasterizer being installed. Re-run this only when the
artwork changes.

Sizes come from three different drawings, not one downscaled: below
about 48px the detailed face turns to mud, and by 16px a trace is a
single pixel and vanishes entirely. 32 uses icon-small.svg and 16 uses
icon-16.svg, each a redraw rather than a resize. Downscaling the big one
for everything is what makes an icon look smeared in a taskbar.
"""

import io
import pathlib
import sys

try:
    import cairosvg
    from PIL import Image
except ImportError as e:
    sys.exit(f"needs cairosvg and pillow: {e}")

HERE = pathlib.Path(__file__).parent
DETAILED = HERE / "icon.svg"
SIMPLE = HERE / "icon-small.svg"
TINY = HERE / "icon-16.svg"
OUT = HERE / "localcode.ico"

# Which drawing each size is rendered from.
SIZES = [
    (16, TINY),
    (32, SIMPLE),
    (48, DETAILED),
    (64, DETAILED),
    (128, DETAILED),
    (256, DETAILED),
]


def render(svg: pathlib.Path, size: int) -> Image.Image:
    png = cairosvg.svg2png(url=str(svg), output_width=size, output_height=size)
    return Image.open(io.BytesIO(png)).convert("RGBA")


def main() -> None:
    frames = [render(svg, size) for size, svg in SIZES]
    # Pillow writes every frame it is given; the largest is passed as the
    # base image and the rest via append_images.
    frames[-1].save(
        OUT,
        format="ICO",
        sizes=[(s, s) for s, _ in SIZES],
        append_images=frames[:-1],
    )
    print(f"wrote {OUT} ({OUT.stat().st_size} bytes, sizes {[s for s, _ in SIZES]})")


if __name__ == "__main__":
    main()
