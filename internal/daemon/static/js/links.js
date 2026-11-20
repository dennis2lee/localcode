// Links open in the person's own browser, from the desktop window too.
//
// Every link in the model's output is drawn with target="_blank"
// (js/markdown.js). In a browser that is a new tab. In the desktop window
// on Windows, WebView2 asks the window for a new window, and the window
// hands the address to the system (internal/gui/newwindow_windows.go).
// WKWebView (macOS) and WebKitGTK (Linux) ask nobody: a click on such a
// link did nothing at all. The window there binds lcOpenExternal, which
// checks the address the same way and hands it to the system, and a click
// on a web link is sent to it instead.

// externalHref is the web address a click on target opens: the href of
// the link it is in, when that is an http or https address. '' for
// anything else (a relative link, a mailto:, no link at all).
export function externalHref(target) {
  const a = target && typeof target.closest === 'function' ? target.closest('a[href]') : null;
  if (!a) return '';
  const href = a.getAttribute('href') || '';
  return /^https?:\/\//i.test(href) ? href : '';
}

// openExternally sends a click on a web link to the window's
// lcOpenExternal, and reports whether it did. Left and middle clicks
// alike: a middle click asks for a new window too, and WebKit drops that
// one the same way.
export function openExternally(e) {
  if (typeof window === 'undefined' || typeof window.lcOpenExternal !== 'function') return false;
  if (e.button !== undefined && e.button !== 0 && e.button !== 1) return false;
  const href = externalHref(e.target);
  if (!href) return false;
  e.preventDefault();
  // Called by its name on window, which internal/gui chrome_test.go reads
  // to check it against what the window binds.
  Promise.resolve()
    .then(() => window.lcOpenExternal(href))
    .catch((err) => console.warn(`could not open ${href}: ${err && err.message ? err.message : err}`));
  return true;
}

// wireExternalLinks listens for link clicks anywhere on the page. Once, at
// startup. Whether the window binds lcOpenExternal is read at each click,
// not here: a binding arrives with the document, and nothing about it is
// worth getting wrong by reading it early.
export function wireExternalLinks() {
  document.addEventListener('click', openExternally);
  document.addEventListener('auxclick', openExternally);
}
