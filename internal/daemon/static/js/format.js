// Pure text formatting. No DOM, no fetch — safe to unit-test directly and
// safe for every other module to depend on.

export function escapeHtml(s) {
  return s.replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

export function formatTime(iso) {
  try {
    const d = new Date(iso);
    return d.toLocaleString();
  } catch { return iso; }
}

// shortenPath trims a long path from the front, keeping the tail — the
// project directory, which is what identifies the session — and dropping
// the leading directories, which for most people are the same on every
// row. The full path stays in the title attribute.
export function shortenPath(path, max = 32) {
  if (path.length <= max) return path;
  const tail = path.slice(-(max - 1));
  const cut = tail.indexOf('/');
  // Prefer starting at a path separator so the result reads as a path
  // rather than a word chopped in half.
  return '…' + (cut > 0 && cut < 8 ? tail.slice(cut) : tail);
}
