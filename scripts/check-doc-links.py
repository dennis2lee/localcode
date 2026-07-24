#!/usr/bin/env python3
"""Fail if any internal markdown link (to a repo file or a #anchor) is
broken. Run by release-preflight and safe to run any time."""
import re, os, sys, glob

def anchors(path):
    out = set()
    for line in open(path, encoding="utf-8"):
        m = re.match(r'^#{1,6}\s+(.*?)\s*$', line)
        if m:
            t = re.sub(r'`', '', m.group(1))
            t = re.sub(r'\[([^\]]*)\]\([^)]*\)', r'\1', t)  # link text only
            out.add(re.sub(r'\s+', '-', re.sub(r'[^\w\s-]', '', t.lower())))
    return out

files = sorted(glob.glob("*.md") + glob.glob("docs/*.md"))
cache = {f: anchors(f) for f in files}
bad = []
for f in files:
    base = os.path.dirname(f)
    for m in re.finditer(r'\[[^\]]*\]\(([^)]+)\)', open(f, encoding="utf-8").read()):
        link = m.group(1)
        if link.startswith(("http://", "https://", "mailto:")):
            continue
        path, _, frag = link.partition("#")
        target = f if path == "" else os.path.normpath(os.path.join(base, path))
        if path and not os.path.exists(target):
            bad.append((f, link, "missing file"))
            continue
        if frag and target.endswith(".md") and frag not in cache.get(target, anchors(target)):
            bad.append((f, link, "missing anchor"))

if bad:
    for f, link, why in bad:
        print(f"broken link in {f}: {link}  ({why})", file=sys.stderr)
    sys.exit(1)
print(f"doc links OK ({len(files)} files)")
