(async () => {
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const st = () => ['browser-panel', 'ssh-panel', 'right-panel'].map((id) => { const p = document.getElementById(id); return p.hidden ? 'off' : p.classList.contains('collapsed') ? 'hidden' : 'shown'; }).join(' ');
  const rows = () => [...document.querySelectorAll('#session-list .session-item')];
  const active = () => rows().findIndex((r) => r.classList.contains('active'));
  const out = [];
  const note = (what) => out.push(`${what}: [${st()}] active row ${active()} of ${rows().length}`);
  note('start (conversation A)');
  document.getElementById('toggle-right').click();
  note('A: tasks hidden');
  document.getElementById('new-session-btn').click();
  await sleep(1200);
  note('new conversation B opened');
  document.getElementById('toggle-right').click();
  document.getElementById('toggle-ssh').click();
  note('B: tasks shown, SSH hidden');
  const other = () => rows().find((r) => !r.classList.contains('active'));
  other().click(); await sleep(1000);
  note('back to A');
  other().click(); await sleep(1000);
  note('back to B');
  other().click(); await sleep(1000);
  note('back to A again');
  out.push('stored: ' + localStorage.getItem('localcode.panelViews'));
  return out;
})()
