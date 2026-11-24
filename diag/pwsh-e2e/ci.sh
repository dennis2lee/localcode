#!/bin/bash
# The PowerShell panel end to end on a Windows runner: a daemon with the
# PowerShell setting on, a scripted model that works in a terminal through
# the powershell tool, and a Chrome that shows the panel and types in it.
#
#   ci.sh <source dir> <shell: powershell.exe | pwsh.exe> <expected major version> <expected edition>
set -u
SRC="$1"
SHELL_EXE="$2"
MAJOR="$3"
EDITION="$4"
ROOT="$(pwd)"
OUT="$ROOT/diag/out"
H="$(cygpath -u "$USERPROFILE")"
WORK="$ROOT/work"
mkdir -p "$H/.localcode" "$OUT" "$WORK" "$ROOT/bin"

(cd "$SRC" && go build -o "$ROOT/bin/localcode.exe" ./cmd/localcode) || { echo "build failed"; exit 1; }

cat > "$H/.localcode/config.json" << EOF
{
  "powershell": true,
  "powershell_path": "$SHELL_EXE",
  "skip_permissions": true,
  "auto_update": false,
  "providers": {"fake": {"type": "openai-compat", "base_url": "http://127.0.0.1:9123/v1"}},
  "profiles": {"scripted": {"provider": "fake", "model": "scripted", "vision": false}},
  "default_profile": "scripted"
}
EOF

node "$ROOT/diag/pwsh-e2e/fakemodel.mjs" 9123 "$OUT" > "$OUT/fakemodel.log" 2>&1 &
(cd "$WORK" && "$ROOT/bin/localcode.exe" --headless --listen 127.0.0.1:4196 > "$OUT/daemon.log" 2>&1 &)
for i in $(seq 1 60); do
  curl -s -o /dev/null http://127.0.0.1:4196/api/settings && break
  sleep 0.5
done
curl -s http://127.0.0.1:4196/api/settings | tee "$OUT/settings.json" | head -c 600; echo

SID="$(curl -s -X POST -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:4196/api/sessions | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const j=JSON.parse(s);console.log(j.id||(j.session&&j.session.id)||"")})')"
echo "session: $SID"
[ -n "$SID" ] || { echo "no session"; tail -20 "$OUT/daemon.log"; exit 1; }
curl -s -X POST -H 'Content-Type: application/json' -d '{"text":"work in powershell"}' "http://127.0.0.1:4196/api/sessions/$SID/messages" > "$OUT/send.json"
cat "$OUT/send.json"; echo

# The model's turn: an open and five runs, then its answer.
for i in $(seq 1 240); do
  n="$(wc -l < "$OUT/tool-results.jsonl" 2> /dev/null || echo 0)"
  [ "$n" -ge 7 ] && break
  sleep 0.5
done
echo "tool results:"
cat "$OUT/tool-results.jsonl"

CHROME="/c/Program Files/Google/Chrome/Application/chrome.exe"
"$CHROME" --version 2> /dev/null || true
node "$ROOT/diag/pwsh-e2e/e2e.mjs" --chrome "$CHROME" --url http://127.0.0.1:4196/ --out "$OUT" > "$OUT/e2e.log" 2>&1
echo "e2e exit $?"
cat "$OUT/e2e.log"
cat "$OUT/page.json"
curl -s "http://127.0.0.1:4196/api/sessions/$SID/powershell" > "$OUT/state.json"

taskkill //F //IM localcode.exe > /dev/null 2>&1
sleep 1

MAJOR="$MAJOR" EDITION="$EDITION" WORK="$(cygpath -w "$WORK")" node -e '
const fs = require("fs");
const out = process.argv[1];
const rows = fs.readFileSync(out + "/tool-results.jsonl", "utf8").trim().split("\n").map((l) => JSON.parse(l));
const at = (n) => (rows.find((r) => r.step === n) || {}).content || "";
const page = JSON.parse(fs.readFileSync(out + "/page.json", "utf8"));
const step = (name) => (page.steps || []).find((s) => s.name === name) || {};
const checks = [
  ["the model is offered powershell", ((rows.find((r) => r.step === 0) || {}).offered || []).includes("powershell")],
  ["open starts t1", /Started: terminal t1/.test(at(1))],
  ["run 1: status 0", /Exit status 0/.test(at(2))],
  ["run 1: the version", at(2).includes("pwsh-major-" + process.env.MAJOR)],
  ["run 1: the working directory", at(2).toLowerCase().includes(process.env.WORK.toLowerCase())],
  ["run 1: no echoed command", !at(2).includes("Write-Output")],
  ["run 2: status 7", /Exit status 7/.test(at(3))],
  ["run 3: status 1", /Exit status 1/.test(at(4))],
  ["run 3: the error", /Cannot find path|does not exist/.test(at(4))],
  ["run 4: first and last line", at(5).includes("line 1\n") && at(5).includes("line 120")],
  ["run 5: 300 x on one line", at(6).includes("x".repeat(300))],
  ["the panel is shown", page.panelShown === true],
  ["the header toggle is shown", page.toggleShown === true],
  ["the person typed in t1", (step("person-typed").screen || "").includes("typed-by-person-42")],
  ["+ opened a second terminal", (page.tabs || []).length >= 2],
  ["the second terminal ran", (step("second-terminal").screen || "").includes(process.env.EDITION)],
  ["the settings row is shown and on", !!page.settings && page.settings.rowShown && page.settings.checked],
];
let bad = 0;
for (const [name, ok] of checks) { console.log((ok ? "ok   " : "FAIL ") + name); if (!ok) bad++; }
if (page.error) { console.log("page error: " + page.error); bad++; }
process.exit(bad ? 1 : 0);
' "$OUT"
