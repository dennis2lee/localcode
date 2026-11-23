#!/bin/bash
# Runs the flicker harness on a CI runner: a daemon and a fake SSH server
# of its own, and each browser the runner has. Results go to diag/out.
set -u
OS="$(uname -s)"
EXT=""
case "$OS" in MINGW* | MSYS*) EXT=".exe" ;; esac
if [ -n "$EXT" ]; then H="$(cygpath -u "$USERPROFILE")"; else H="$HOME"; fi
ROOT="$(pwd)"
OUT="$ROOT/diag/out"
mkdir -p "$H/.localcode" "$H/.ssh" "$OUT" "$ROOT/work"
cat > "$H/.localcode/config.json" << 'EOF'
{
  "ssh": true,
  "auto_update": false,
  "providers": {"fake": {"type": "openai-compat", "base_url": "http://127.0.0.1:9/v1"}},
  "profiles": {"scripted": {"provider": "fake", "model": "scripted"}},
  "default_profile": "scripted"
}
EOF
printf 'Host devbox\n  HostName 127.0.0.1\n  Port 22998\n  User me\n\nHost realbox\n  HostName 127.0.0.1\n  Port 22997\n' > "$H/.ssh/config"

stop() {
  if [ -n "$EXT" ]; then
    taskkill //F //IM sshfake.exe > /dev/null 2>&1
    taskkill //F //IM localcode.exe > /dev/null 2>&1
  else
    pkill -f bin/sshfake > /dev/null 2>&1
    pkill -f "bin/localcode --headless" > /dev/null 2>&1
  fi
  sleep 1
}

# start <echo mode> [gap]: a fresh fake server and a fresh daemon.
start() {
  stop
  rm -rf "$H/.localcode/sessions" "$H/.localcode/ssh"
  "$ROOT/bin/sshfake$EXT" -addr 127.0.0.1:22998 -known "$H/.ssh/known_hosts_fake" -echo "$1" -gap "${2:-20ms}" >> "$OUT/sshfake.log" 2>&1 &
  sleep 1
  cat "$H/.ssh/known_hosts_fake" "$H/.ssh/known_hosts_real" > "$H/.ssh/known_hosts" 2> /dev/null
  (cd "$ROOT/work" && "$ROOT/bin/localcode$EXT" --headless --listen 127.0.0.1:4196 >> "$OUT/daemon.log" 2>&1 &)
  for i in $(seq 1 40); do
    curl -s -o /dev/null http://127.0.0.1:4196/api/settings && return 0
    sleep 0.5
  done
  echo "daemon did not start"
  tail -5 "$OUT/daemon.log"
  return 1
}

# run <name> <browser> [harness args]
run() {
  local name="$1" browser="$2"
  shift 2
  if [ ! -e "$browser" ]; then
    echo "SKIP $name: no $browser"
    return
  fi
  node diag/flicker/flicker.mjs --chrome "$browser" --url http://127.0.0.1:4196/ --out "$OUT" --name "$name" "$@" > "$OUT/$name.log" 2>&1 || {
    echo "FAIL $name"
    tail -5 "$OUT/$name.log"
  }
}

if [ -n "$EXT" ]; then
  CHROME="/c/Program Files/Google/Chrome/Application/chrome.exe"
  EDGE="/c/Program Files (x86)/Microsoft/Edge/Application/msedge.exe"
  HEAD="--headful --size 1000x700"
  EXTRA=""
else
  CHROME="$(command -v google-chrome || command -v chromium || echo /none)"
  EDGE="$(command -v microsoft-edge || echo /none)"
  HEAD="--size 1400x900"
  EXTRA="--extra --no-sandbox"
fi
"$CHROME" --version 2> /dev/null || true

LONG="echo the quick brown fox jumps over the lazy dog 0123456789"
BIG="--size 1400x900"

start plain || exit 1
# The method: a flicker made on purpose must be seen, and a quiet run must be quiet.
run m-inject-hide "$CHROME" $BIG $EXTRA --fresh --pre "seq 1 80" --inject hide
run m-inject-scroll "$CHROME" $BIG $EXTRA --fresh --pre "seq 1 80" --inject scroll
# The page's own zoom, the prompt on the bottom row.
run z100 "$CHROME" $BIG $EXTRA --fresh --pre "seq 1 80"
run z080 "$CHROME" $BIG $EXTRA --fresh --zoom 0.8 --pre "seq 1 80"
run z110 "$CHROME" $BIG $EXTRA --fresh --zoom 1.1 --pre "seq 1 80"
run z125 "$CHROME" $BIG $EXTRA --fresh --zoom 1.25 --pre "seq 1 80"
run z150 "$CHROME" $BIG $EXTRA --fresh --zoom 1.5 --pre "seq 1 80"
# The prediction: at the top of the screen the box is still inside.
run z125-top "$CHROME" $BIG $EXTRA --fresh --zoom 1.25
# The intervention: the typing box kept at the corner.
run z125-pin "$CHROME" $BIG $EXTRA --fresh --zoom 1.25 --pre "seq 1 80" --pin
# The observer: the screencast alone.
run z125-norec "$CHROME" $BIG $EXTRA --fresh --zoom 1.25 --pre "seq 1 80" --norec
run z100-norec "$CHROME" $BIG $EXTRA --fresh --pre "seq 1 80" --norec
# The right margin at 100%: a line typed past the last column.
run z100-margin "$CHROME" $BIG $EXTRA --fresh --pre "seq 1 80" --text "$LONG $LONG"
# Display scaling is not the page's zoom.
run z100-dsf125 "$CHROME" $BIG $EXTRA --fresh --dsf 1.25 --pre "seq 1 80"
run z100-dsf150 "$CHROME" $BIG $EXTRA --fresh --dsf 1.5 --pre "seq 1 80"
run z125-dsf125 "$CHROME" $BIG $EXTRA --fresh --dsf 1.25 --zoom 1.25 --pre "seq 1 80"
if [ -n "$EXT" ]; then
  # A window on the runner's desktop.
  run h-z100 "$CHROME" $HEAD --fresh --pre "seq 1 80"
  run h-z125 "$CHROME" $HEAD --fresh --zoom 1.25 --pre "seq 1 80"
fi
# A narrow window: the terminal's forty columns are wider than its panel,
# and at a display scale of 150% the panel is partly outside the window.
SMALL="--size 1000x700"
run n-z100 "$CHROME" $SMALL $EXTRA --fresh --pre "seq 1 80" --text "$LONG"
run n-z100-fast "$CHROME" $SMALL $EXTRA --fresh --pre "seq 1 80" --interval 25 --text "$LONG"
run n-z100-dsf150 "$CHROME" $SMALL $EXTRA --fresh --dsf 1.5 --pre "seq 1 80" --text "$LONG"
run n-z125 "$CHROME" $SMALL $EXTRA --fresh --zoom 1.25 --pre "seq 1 80" --text "$LONG"
if [ -n "$EXT" ]; then
  run h-n-z100 "$CHROME" $HEAD --fresh --pre "seq 1 80" --text "$LONG"
  run h-n-dsf150 "$CHROME" $HEAD --fresh --dsf 1.5 --pre "seq 1 80" --text "$LONG"
fi
run e-z100 "$EDGE" $BIG $EXTRA --fresh --pre "seq 1 80"
run e-z125 "$EDGE" $BIG $EXTRA --fresh --zoom 1.25 --pre "seq 1 80"
run e-z125-pin "$EDGE" $BIG $EXTRA --fresh --zoom 1.25 --pre "seq 1 80" --pin

# A Chrome from before 128, where a zoomed page's rectangles are in
# layout pixels: the other way an engine reports them.
if [ -z "$EXT" ]; then
  OLDURL="$(curl -s https://googlechromelabs.github.io/chrome-for-testing/known-good-versions-with-downloads.json | python3 -c "
import json, sys
d = json.load(sys.stdin)
v = [x for x in d['versions'] if x['version'].startswith('120.') and 'chrome' in x['downloads']]
print([u['url'] for u in v[-1]['downloads']['chrome'] if u['platform'] == 'linux64'][0])
")"
  echo "old chrome: $OLDURL"
  mkdir -p "$ROOT/work/oldchrome"
  if curl -s -L -o "$ROOT/work/oldchrome/chrome.zip" "$OLDURL" && (cd "$ROOT/work/oldchrome" && unzip -q chrome.zip); then
    OLD="$ROOT/work/oldchrome/chrome-linux64/chrome"
    "$OLD" --version || true
    run o-z100 "$OLD" $BIG $EXTRA --fresh --pre "seq 1 80"
    run o-z125 "$OLD" $BIG $EXTRA --fresh --zoom 1.25 --pre "seq 1 80"
    run o-z150 "$OLD" $BIG $EXTRA --fresh --zoom 1.5 --pre "seq 1 80"
    run o-z080 "$OLD" $BIG $EXTRA --fresh --zoom 0.8 --pre "seq 1 80"
    run o-n-z125 "$OLD" $SMALL $EXTRA --fresh --zoom 1.25 --pre "seq 1 80" --text "$LONG"
  fi
fi

# A real sshd with the runner's own bash, where there is one.
if [ -z "$EXT" ] && [ -x /usr/sbin/sshd ]; then
  D="$ROOT/work/sshd"
  mkdir -p "$D"
  ssh-keygen -q -t ed25519 -N '' -f "$D/hostkey"
  [ -f "$H/.ssh/id_ed25519" ] || ssh-keygen -q -t ed25519 -N '' -f "$H/.ssh/id_ed25519"
  cp "$H/.ssh/id_ed25519.pub" "$D/authorized_keys"
  chmod 600 "$D/authorized_keys"
  cat > "$D/sshd_config" << EOF
Port 22997
ListenAddress 127.0.0.1
HostKey $D/hostkey
AuthorizedKeysFile $D/authorized_keys
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
StrictModes no
PidFile $D/sshd.pid
AcceptEnv LANG LC_*
EOF
  /usr/sbin/sshd -f "$D/sshd_config" -E "$D/sshd.log"
  sleep 1
  echo "[127.0.0.1]:22997 $(cut -d' ' -f1,2 "$D/hostkey.pub")" > "$H/.ssh/known_hosts_real"
  start plain || exit 1
  run r-bash-z100 "$CHROME" $BIG $EXTRA --fresh --target realbox --pre "seq 1 80"
  run r-bash-z125 "$CHROME" $BIG $EXTRA --fresh --target realbox --zoom 1.25 --pre "seq 1 80"
  tail -3 "$D/sshd.log"
fi
stop

echo "==== summary"
python3 diag/flicker/sum.py "$OUT"/*.json || python diag/flicker/sum.py "$OUT"/*.json
echo "==== timelines of runs with something to see"
for f in "$OUT"/*.json; do
  case "$f" in *inject*) continue ;; esac
  if node diag/flicker/has.mjs "$f"; then
    echo "---- $f"
    node diag/flicker/timeline.mjs "$f" 0 1000000000 | grep -v ' MUT ' | cut -c1-240 | head -150
  fi
done
exit 0
