// What the desktop window remembered, put back before anything is drawn.
//
// The page keeps its own settings in localStorage: the faces and the text
// size, the zoom, the panel widths, which panels a conversation shows,
// which groups are folded. In the desktop window that storage does not
// last: it is kept per address, port included, and the window can come up
// on a different port, where it finds nothing (the cases that were
// measured are listed in internal/daemon/windowstate.go). So in the window
// every change is written to the daemon as well (js/prefs.js), into
// ~/.localcode/window-state.json, and this script reads that file back
// into localStorage at the start of every page. Whatever reads
// localStorage after it, the head script below it in index.html and every
// module, then finds what was kept, on any port.
//
// A classic script, loaded in the head and asked for synchronously, for
// one reason: the values have to be there before the first paint and
// before the first module reads one. Only in the desktop window
// (window.lcGUI, set by the window before any page script runs). A
// browser tab returns at the first line and keeps its own storage.
//
// The file is the record. A name of the page's own that the file does not
// have is removed here, so a setting put back to its default in one
// window is not brought back by another address's older copy.
(function () {
  'use strict';
  if (window.lcGUI !== true) return;
  // The page's own names, the same rule the daemon applies to a write.
  var own = /^(?:localcode\.[A-Za-z0-9][A-Za-z0-9._-]{0,63}|archiveOpen)$/;
  try {
    var ask = new XMLHttpRequest();
    ask.open('GET', '/api/window/state', false);
    ask.send(null);
    if (ask.status !== 200) return;
    var answer = JSON.parse(ask.responseText);
    if (!answer || answer.available !== true) return;
    var kept = answer.kept;
    if (!kept || typeof kept !== 'object') {
      // Nothing has been written yet. What this address already holds is
      // handed over by js/prefs.js rather than replaced by nothing.
      window.localcodeKept = 'nothing yet';
      return;
    }
    var has = function (name) { return Object.prototype.hasOwnProperty.call(kept, name); };
    var gone = [];
    for (var i = 0; i < localStorage.length; i++) {
      var name = localStorage.key(i);
      if (typeof name === 'string' && own.test(name) && !has(name)) gone.push(name);
    }
    for (var j = 0; j < gone.length; j++) localStorage.removeItem(gone[j]);
    for (var key in kept) {
      if (!has(key) || !own.test(key) || typeof kept[key] !== 'string') continue;
      if (localStorage.getItem(key) !== kept[key]) localStorage.setItem(key, kept[key]);
    }
    window.localcodeKept = 'read';
  } catch (e) {
    // The daemon did not answer, or storage refused. The page starts from
    // what this address holds, and writes nothing to the file this time.
  }
})();
