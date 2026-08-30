#!/usr/bin/env python3
"""Fail if any internal markdown link (to a repo file or a #anchor) is
broken. Run by release-preflight and safe to run any time."""
import re, os, sys, subprocess

def anchors(path):
    out = set()
    for line in open(path, encoding="utf-8"):
        m = re.match(r'^#{1,6}\s+(.*?)\s*$', line)
        if m:
            t = re.sub(r'`', '', m.group(1))
            t = re.sub(r'\[([^\]]*)\]\([^)]*\)', r'\1', t)  # link text only
            out.add(re.sub(r'\s+', '-', re.sub(r'[^\w\s-]', '', t.lower())))
    return out

def prose(path):
    """File contents with code removed. Fenced blocks and inline code spans
    are not links even when they look like one — Go generics in particular
    (`mergeMap[K comparable, V any](dst *map[K]V, ...)`) parse as a markdown
    link and were reported as broken files."""
    text = open(path, encoding="utf-8").read()
    # Fences may be indented, e.g. a code block inside a numbered list.
    text = re.sub(r'^[ \t]*```.*?^[ \t]*```', '', text, flags=re.S | re.M)
    return re.sub(r'`[^`\n]*`', '', text)

def markdown_files():
    """Every markdown file the repository actually ships.

    This was `glob("*.md") + glob("docs/*.md")`, which is neither a
    superset nor a subset of the right answer. It missed
    test/webui/README.md and would miss any future docs subdirectory,
    while a plain recursive walk would be worse: this checkout has full
    repository copies under .claude/worktrees, and a walk would descend
    into every one of them and check the same files again.

    Asking git for the tracked and untracked-but-not-ignored files gives
    exactly the set that goes in a release, with ignored trees and
    locally-excluded scratch documents left out on their own.
    """
    out = subprocess.run(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "*.md"],
        capture_output=True, text=True, check=True,
    ).stdout
    return sorted({f for f in out.split("\0") if f and os.path.exists(f)})

files = markdown_files()
if len(files) < 5:
    sys.exit(f"check-doc-links: only found {len(files)} markdown files, which cannot be right")
cache = {f: anchors(f) for f in files}
bad = []
for f in files:
    base = os.path.dirname(f)
    for m in re.finditer(r'\[[^\]]*\]\(([^)]+)\)', prose(f)):
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
