# Diagnostic, not part of the release.
#
# Does a setting made in the desktop window's Settings survive closing the
# window and opening it again? Each scenario starts from nothing, changes
# three settings in the real window (two the daemon keeps in config.json,
# one the page keeps for itself), ends the window one way, starts it again
# and reads all three back, from the window and from underneath it.
#
#   run.ps1 -Exe <localcode-gui.exe> [-Ref <label>] [-Only "a b c"]
param(
  [Parameter(Mandatory = $true)][string]$Exe,
  [string]$Ref = 'main',
  [string]$Only = ''
)

$ErrorActionPreference = 'Continue'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
Add-Type -AssemblyName Microsoft.VisualBasic
Add-Type @"
using System;
using System.Runtime.InteropServices;
public static class W {
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint f, uint dx, uint dy, uint d, UIntPtr e);
  [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr h, uint m, IntPtr w, IntPtr l);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool MoveWindow(IntPtr h, int x, int y, int w, int hgt, bool repaint);
  // What Hyper-V, WSL and Docker call at boot to take ranges of the
  // dynamic ports for themselves. The port is in network byte order.
  [DllImport("iphlpapi.dll")] public static extern uint CreatePersistentTcpPortReservation(ushort startPort, ushort count, out ulong token);
  [DllImport("iphlpapi.dll")] public static extern uint DeletePersistentTcpPortReservation(ushort startPort, ushort count);
  public static ushort Net(int port) { return (ushort)(((port & 0xff) << 8) | ((port >> 8) & 0xff)); }
  public static void Click(int x, int y) {
    SetCursorPos(x, y);
    System.Threading.Thread.Sleep(150);
    mouse_event(0x0002, 0, 0, 0, UIntPtr.Zero);
    System.Threading.Thread.Sleep(60);
    mouse_event(0x0004, 0, 0, 0, UIntPtr.Zero);
  }
}
"@

$Exe = (Resolve-Path $Exe).Path
$LC = Join-Path $env:USERPROFILE '.localcode'
$Shots = Join-Path $PWD 'shots'
New-Item -ItemType Directory $LC -Force | Out-Null
New-Item -ItemType Directory $Shots -Force | Out-Null
$AE = [System.Windows.Automation.AutomationElement]
$Root = $AE::RootElement

function Shot($name) {
  try {
    $b = [System.Windows.Forms.SystemInformation]::VirtualScreen
    $bmp = New-Object System.Drawing.Bitmap $b.Width, $b.Height
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.CopyFromScreen($b.Left, $b.Top, 0, 0, $bmp.Size)
    $bmp.Save((Join-Path $Shots "$name.png"))
    $g.Dispose(); $bmp.Dispose()
  } catch { Write-Host "shot $name failed: $_" }
}

# The config the window starts from. With -Comments it is the same file
# with a note in it, which is a thing people do to a config.
function Write-Config([switch]$Comments) {
  $note = if ($Comments) { "`n  // my notes about this machine" } else { '' }
  @"
{$note
  "auto_update": false,
  "browser": false,
  "providers": { "local": { "type": "openai-compat", "base_url": "http://127.0.0.1:9/v1" } },
  "profiles": { "main": { "provider": "local", "model": "test-model" } },
  "agents": { "general-purpose": { "profile": "main" } },
  "default_profile": "main"
}
"@ | Set-Content -Encoding utf8 (Join-Path $LC 'config.json')
}

function Stop-All {
  Get-Process | Where-Object { $_.Path -and ($_.Path -like "*localcode-gui*" -or $_.Path -like "*lc-renamed*") } | ForEach-Object {
    try { Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue } catch {}
  }
  for ($i = 0; $i -lt 40; $i++) {
    if (-not (Get-Process msedgewebview2 -ErrorAction SilentlyContinue)) { break }
    Start-Sleep -Milliseconds 250
  }
}

function DataDir($exe) { Join-Path $env:APPDATA (Split-Path $exe -Leaf) }

function Reset-State([switch]$Comments) {
  Stop-All
  foreach ($d in (Get-ChildItem $env:APPDATA -Directory | Where-Object { $_.Name -like 'localcode-gui*' -or $_.Name -like 'lc-renamed*' })) {
    Remove-Item -Recurse -Force $d.FullName -ErrorAction SilentlyContinue
  }
  foreach ($f in 'window-port', 'window-state.json') {
    Remove-Item -Force (Join-Path $LC $f) -ErrorAction SilentlyContinue
  }
  Remove-Item -Recurse -Force (Join-Path $LC 'sessions') -ErrorAction SilentlyContinue
  Write-Config -Comments:$Comments
}

function Start-App($exe = $Exe) {
  $p = Start-Process $exe -PassThru
  $port = $null
  for ($i = 0; $i -lt 120 -and -not $port; $i++) {
    $ports = Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue | Where-Object { $_.OwningProcess -eq $p.Id } | ForEach-Object { $_.LocalPort }
    foreach ($c in $ports) {
      try { Invoke-RestMethod "http://127.0.0.1:$c/api/version" -TimeoutSec 2 | Out-Null; $port = $c; break } catch {}
    }
    if (-not $port) { Start-Sleep -Milliseconds 500 }
  }
  $app = [pscustomobject]@{ Proc = $p; Port = $port; Base = "http://127.0.0.1:$port"; Exe = $exe }
  if (-not $port) { Write-Host "window pid $($p.Id) has no daemon port"; return $app }
  # The page is up when its settings button is in the accessibility tree.
  $btn = El $app 'settings-btn' 80
  if (-not $btn) { Write-Host "window pid $($p.Id): the page never showed its settings button" }
  $win = Win $app
  if ($win) {
    $h = [IntPtr]$win.Current.NativeWindowHandle
    $scr = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
    $ww = [Math]::Min(1100, $scr.Width - 20); $wh = [Math]::Min(800, $scr.Height - 70)
    [W]::MoveWindow($h, 10, 10, $ww, $wh, $true) | Out-Null
    [W]::SetForegroundWindow($h) | Out-Null
  }
  Start-Sleep -Milliseconds 800
  return $app
}

function Win($app) {
  $cond = New-Object System.Windows.Automation.PropertyCondition($AE::ProcessIdProperty, $app.Proc.Id)
  return $Root.FindFirst('Children', $cond)
}

function El($app, $id, $tries = 30) {
  $cond = New-Object System.Windows.Automation.PropertyCondition($AE::AutomationIdProperty, $id)
  for ($i = 0; $i -lt $tries; $i++) {
    try {
      $win = Win $app
      if ($win) {
        $el = $win.FindFirst('Descendants', $cond)
        if ($el) { return $el }
      }
    } catch {}
    Start-Sleep -Milliseconds 300
  }
  return $null
}

function Front($app) {
  try {
    $win = Win $app
    if ($win) { [W]::SetForegroundWindow([IntPtr]$win.Current.NativeWindowHandle) | Out-Null }
  } catch {}
  Start-Sleep -Milliseconds 250
}

# A real click in the middle of the element, as a person would.
function Click-El($app, $el) {
  Front $app
  $r = $el.Current.BoundingRectangle
  if ($r.Width -le 0 -or $r.Height -le 0) { return $false }
  [W]::Click([int]($r.X + $r.Width / 2), [int]($r.Y + $r.Height / 2))
  Start-Sleep -Milliseconds 500
  return $true
}

function Toggle-State($el) {
  try { return [string]$el.GetCurrentPattern([System.Windows.Automation.TogglePattern]::Pattern).Current.ToggleState } catch { return '?' }
}

# Turns a checkbox on with a click, and falls back to the accessibility
# toggle if the click did not land.
function Check($app, $id) {
  $el = El $app $id
  if (-not $el) { return "missing" }
  if ((Toggle-State $el) -eq 'On') { return 'On' }
  Click-El $app $el | Out-Null
  Start-Sleep -Milliseconds 700
  $el = El $app $id
  if ((Toggle-State $el) -ne 'On') {
    try { $el.GetCurrentPattern([System.Windows.Automation.TogglePattern]::Pattern).Toggle() } catch {}
    Start-Sleep -Milliseconds 700
    $el = El $app $id
  }
  return (Toggle-State $el)
}

function Select-Value($el) {
  try { return [string]$el.GetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern).Current.Value } catch {}
  try {
    $sel = $el.GetCurrentPattern([System.Windows.Automation.SelectionPattern]::Pattern).Current.GetSelection()
    if ($sel.Length -gt 0) { return [string]$sel[0].Current.Name }
  } catch {}
  return '?'
}

function Text-Of($app, $id) {
  $el = El $app $id 3
  if (-not $el) { return '' }
  $n = [string]$el.Current.Name
  if ($n) { return $n }
  try {
    $kids = $el.FindAll('Descendants', [System.Windows.Automation.Condition]::TrueCondition)
    return (($kids | ForEach-Object { $_.Current.Name }) -join ' ').Trim()
  } catch { return '' }
}

function Open-Settings($app) {
  if (El $app 'settings-close' 1) { return $true }
  $btn = El $app 'settings-btn'
  if (-not $btn) { return $false }
  Click-El $app $btn | Out-Null
  if (El $app 'settings-close' 10) { return $true }
  try { $btn.GetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern).Invoke() } catch {}
  return [bool](El $app 'settings-close' 10)
}

function Open-Typography($app) {
  if (-not (Open-Settings $app)) { return $false }
  $tab = El $app 'settings-tab-typography'
  if (-not $tab) { return $false }
  Click-El $app $tab | Out-Null
  if (El $app 'typo-size-select' 10) { return $true }
  try { $tab.GetCurrentPattern([System.Windows.Automation.SelectionItemPattern]::Pattern).Select() } catch {}
  try { $tab.GetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern).Invoke() } catch {}
  return [bool](El $app 'typo-size-select' 10)
}

function Close-Settings($app) {
  $c = El $app 'settings-close' 2
  if ($c) { Click-El $app $c | Out-Null }
}

# Changes the three settings and says what the window shows afterwards.
function Set-All($app) {
  if (-not (Open-Settings $app)) { return 'settings did not open' }
  $smart = Check $app 'smart-agent-checkbox'
  $ssh = Check $app 'ssh-checkbox'
  Start-Sleep -Milliseconds 1200
  $warn = ((Text-Of $app 'smart-agent-warn') + ' ' + (Text-Of $app 'ssh-warn')).Trim()
  if ($script:ShotAgents) { Shot $script:ShotAgents }
  $text = '?'
  if (Open-Typography $app) {
    $sel = El $app 'typo-size-select'
    $before = Select-Value $sel
    Front $app
    try { $sel.SetFocus() } catch {}
    Start-Sleep -Milliseconds 400
    [System.Windows.Forms.SendKeys]::SendWait('{DOWN}')
    Start-Sleep -Milliseconds 700
    $sel = El $app 'typo-size-select'
    $text = Select-Value $sel
    if ($text -eq $before) {
      # The list did not move: open it with a click and choose from it.
      Click-El $app $sel | Out-Null
      [System.Windows.Forms.SendKeys]::SendWait('{DOWN}{ENTER}')
      Start-Sleep -Milliseconds 700
      $sel = El $app 'typo-size-select'
      $text = Select-Value $sel
    }
  }
  $out = "text=$text smart=$smart ssh=$ssh"
  if ($warn) { $out += " warn=[$warn]" }
  return $out
}

# Reads the three back: what the window shows, and what the daemon says.
function Read-All($app) {
  $api = '?'
  try {
    $s = Invoke-RestMethod "$($app.Base)/api/settings" -TimeoutSec 5
    $api = "smart=$($s.smart_agent) ssh=$($s.ssh)"
  } catch { $api = "unreachable" }
  $smart = '?'; $ssh = '?'; $text = '?'
  if (Open-Settings $app) {
    $smart = Toggle-State (El $app 'smart-agent-checkbox')
    $ssh = Toggle-State (El $app 'ssh-checkbox')
    if (Open-Typography $app) { $text = Select-Value (El $app 'typo-size-select') }
  }
  return "text=$text smart=$smart ssh=$ssh (daemon: $api)"
}

function Close-App($app, $how = 'close') {
  if ($how -eq 'kill') {
    Stop-Process -Id $app.Proc.Id -Force -ErrorAction SilentlyContinue
  } else {
    # WM_CLOSE to the window: what the page's own close button posts, and
    # what Alt+F4 and the taskbar's Close send.
    $win = Win $app
    if ($win) { [W]::PostMessage([IntPtr]$win.Current.NativeWindowHandle, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero) | Out-Null }
  }
  if (-not $app.Proc.WaitForExit(20000)) {
    Write-Host "pid $($app.Proc.Id) did not exit after $how; killing it"
    Stop-Process -Id $app.Proc.Id -Force -ErrorAction SilentlyContinue
  }
  for ($i = 0; $i -lt 60; $i++) {
    if (-not (Get-Process msedgewebview2 -ErrorAction SilentlyContinue)) { break }
    Start-Sleep -Milliseconds 250
  }
}

function Port-File {
  $f = Join-Path $LC 'window-port'
  if (Test-Path $f) { return (Get-Content $f -Raw).Trim() }
  return 'none'
}

function Config-Keys {
  try {
    $raw = Get-Content (Join-Path $LC 'config.json') -Raw
    $smart = if ($raw -match '"smart_agent"\s*:\s*(true|false)') { $Matches[1] } else { 'absent' }
    $ssh = if ($raw -match '"ssh"\s*:\s*(true|false)') { $Matches[1] } else { 'absent' }
    return "smart_agent=$smart ssh=$ssh"
  } catch { return 'unreadable' }
}

# What the window's own storage holds, read from its files: which
# addresses have a text size written under them, and what it is.
function Storage($exe = $Exe) {
  $dir = Join-Path (DataDir $exe) 'EBWebView\Default\Local Storage\leveldb'
  if (-not (Test-Path $dir)) { return "no storage at $dir" }
  $found = @{}
  $origins = @{}
  foreach ($f in (Get-ChildItem $dir -File | Where-Object { $_.Extension -in '.log', '.ldb' })) {
    try {
      $fs = [System.IO.File]::Open($f.FullName, 'Open', 'Read', 'ReadWrite,Delete')
      $buf = New-Object byte[] $fs.Length
      [void]$fs.Read($buf, 0, $buf.Length)
      $fs.Close()
      $s = [System.Text.Encoding]::Latin1.GetString($buf)
      foreach ($m in [regex]::Matches($s, '_http://127\.0\.0\.1:(\d+)\x00\x01(localcode\.textScale)[\x00-\x1f]\x01([0-9.]+)')) {
        $found["$($m.Groups[1].Value)"] = $m.Groups[3].Value
      }
      foreach ($m in [regex]::Matches($s, 'http://127\.0\.0\.1:(\d+)')) { $origins["$($m.Groups[1].Value)"] = 1 }
    } catch { Write-Host "storage read $($f.Name): $_" }
  }
  $seen = ($origins.Keys | Sort-Object) -join ' '
  if ($found.Count -eq 0) { return "no text size under any address; addresses with anything stored: $seen" }
  return (($found.GetEnumerator() | Sort-Object Name | ForEach-Object { "port $($_.Name): textScale=$($_.Value)" }) -join ', ') + "; addresses with anything stored: $seen"
}

function Prefs-File {
  $p = Join-Path $LC 'window-state.json'
  if (Test-Path $p) { return "window-state.json: " + ((Get-Content $p -Raw) -replace '\s+', ' ').Trim() }
  return 'no window-state.json'
}

function Wanted($name) {
  if (-not $Only) { return $true }
  return (" $Only " -like "* $name *")
}

function Result($name, $line) { "RESULT $name ($Ref) :: $line" }

$script:ShotAgents = $null

# ---- what this machine is

"exe: $Exe"
"screen: $([System.Windows.Forms.Screen]::PrimaryScreen.Bounds)"
try {
  $wv = Get-ItemProperty 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}' -ErrorAction Stop
  "WebView2 runtime: $($wv.pv)"
} catch { "WebView2 runtime: not read" }
"--- dynamic port range"
netsh int ipv4 show dynamicport tcp
"--- excluded port ranges"
netsh int ipv4 show excludedportrange protocol=tcp

# ---- scenarios

function Scenario-Close($name, $waitMs, $how) {
  if (-not (Wanted $name)) { return }
  "--- $name"
  Reset-State
  $a = Start-App
  $set = Set-All $a
  Shot "$name-set"
  Start-Sleep -Milliseconds $waitMs
  Close-App $a $how
  $stored = Storage
  $b = Start-App
  $got = Read-All $b
  Shot "$name-after"
  Close-App $b
  Result $name "ended by $how after ${waitMs}ms :: port $($a.Port) -> $($b.Port) :: set $set :: after restart $got :: config $(Config-Keys) :: storage before restart [$stored] :: $(Prefs-File)"
}

# The control: a setting is given time, and the window is closed normally.
Scenario-Close 'close-after-15s' 15000 'close'
# Closed at once, as somebody does who came to change one thing.
Scenario-Close 'close-at-once' 300 'close'
# The process ended without being asked: a crash, a forced shutdown, a
# sign-out, an installer that stops it.
Scenario-Close 'killed-after-2s' 2000 'kill'
Scenario-Close 'killed-after-20s' 20000 'kill'

# The address the window was on is not free the next time.
if (Wanted 'port-taken') {
  "--- port-taken"
  Reset-State
  $a = Start-App
  $set = Set-All $a
  Start-Sleep 15
  Close-App $a
  $l = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, [int]$a.Port)
  $taken = $true
  try { $l.Start() } catch { $taken = $false; Write-Host "could not take port $($a.Port): $_" }
  $b = Start-App
  $got = Read-All $b
  Shot 'port-taken-after'
  Close-App $b
  if ($taken) { $l.Stop() }
  $c = Start-App
  $again = Read-All $c
  Close-App $c
  Result 'port-taken' "another program on port $($a.Port) :: port $($a.Port) -> $($b.Port) -> $($c.Port) (window-port $(Port-File)) :: set $set :: after restart $got :: once the port is free again $again :: config $(Config-Keys) :: storage [$(Storage)] :: $(Prefs-File)"
}

# The same, the way Windows itself does it. Hyper-V, WSL and Docker take
# ranges of the dynamic ports at boot, a different set each time, and a
# port in one of them cannot be bound by anybody else. The reservation
# here is made with the call they use. (An exclusion added with netsh is a
# different thing: it only keeps the port out of the ones handed out, and
# the first run of this found the window binding straight through it.)
if (Wanted 'port-reserved') {
  "--- port-reserved"
  Reset-State
  $a = Start-App
  $set = Set-All $a
  Start-Sleep 15
  Close-App $a
  $token = [uint64]0
  $made = [W]::CreatePersistentTcpPortReservation([W]::Net([int]$a.Port), 1, [ref]$token)
  $bind = 'bound'
  try { $t = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, [int]$a.Port); $t.Start(); $t.Stop() } catch { $bind = "refused: $($_.Exception.InnerException.Message)" }
  $b = Start-App
  $got = Read-All $b
  Shot 'port-reserved-after'
  Close-App $b
  $gone = [W]::DeletePersistentTcpPortReservation([W]::Net([int]$a.Port), 1)
  Result 'port-reserved' "port $($a.Port) reserved by the system (create=$made, delete=$gone; another program binding it: $bind) :: port $($a.Port) -> $($b.Port) (window-port $(Port-File)) :: set $set :: after restart $got :: config $(Config-Keys) :: storage [$(Storage)] :: $(Prefs-File)"
}

# Opened again while the last one is still going away.
if (Wanted 'reopen-while-closing') {
  "--- reopen-while-closing"
  Reset-State
  $a = Start-App
  $set = Set-All $a
  Start-Sleep 8
  $win = Win $a
  $sw = [System.Diagnostics.Stopwatch]::StartNew()
  [W]::PostMessage([IntPtr]$win.Current.NativeWindowHandle, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero) | Out-Null
  $p2 = Start-Process $Exe -PassThru
  $aliveAtStart = -not $a.Proc.HasExited
  [void]$a.Proc.WaitForExit(20000)
  $exitMs = $sw.ElapsedMilliseconds
  $port = $null
  for ($i = 0; $i -lt 120 -and -not $port; $i++) {
    $ports = Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue | Where-Object { $_.OwningProcess -eq $p2.Id } | ForEach-Object { $_.LocalPort }
    foreach ($c in $ports) { try { Invoke-RestMethod "http://127.0.0.1:$c/api/version" -TimeoutSec 2 | Out-Null; $port = $c; break } catch {} }
    if (-not $port) { Start-Sleep -Milliseconds 500 }
  }
  $bindMs = $sw.ElapsedMilliseconds
  $b = [pscustomobject]@{ Proc = $p2; Port = $port; Base = "http://127.0.0.1:$port"; Exe = $Exe }
  [void](El $b 'settings-btn' 80)
  $got = Read-All $b
  Close-App $b
  Result 'reopen-while-closing' "started again as the first was asked to close (first still running then: $aliveAtStart; it was gone after ${exitMs}ms; the second was serving within ${bindMs}ms) :: port $($a.Port) -> $($b.Port) (window-port $(Port-File)) :: set $set :: after restart $got :: config $(Config-Keys) :: storage [$(Storage)] :: $(Prefs-File)"
}

# Two windows at once, then one.
if (Wanted 'second-window') {
  "--- second-window"
  Reset-State
  $a = Start-App
  $set = Set-All $a
  Start-Sleep 8
  $b = Start-App
  $inB = Read-All $b
  Close-App $b
  Close-App $a
  $c = Start-App
  $got = Read-All $c
  Shot 'second-window-after'
  Close-App $c
  Result 'second-window' "port first $($a.Port), second $($b.Port), then alone $($c.Port) (window-port $(Port-File)) :: set in the first $set :: the second shows $inB :: after both closed $got :: config $(Config-Keys) :: storage [$(Storage)] :: $(Prefs-File)"
}

# A config with a note in it.
if (Wanted 'config-with-comment') {
  "--- config-with-comment"
  Reset-State -Comments
  $a = Start-App
  $script:ShotAgents = 'config-with-comment-agents'
  $set = Set-All $a
  $script:ShotAgents = $null
  Shot 'config-with-comment-set'
  Start-Sleep 15
  Close-App $a
  $b = Start-App
  $got = Read-All $b
  Close-App $b
  $kept = if ((Get-Content (Join-Path $LC 'config.json') -Raw) -match 'my notes about this machine') { 'still there' } else { 'GONE' }
  Result 'config-with-comment' "port $($a.Port) -> $($b.Port) :: set $set :: after restart $got :: config $(Config-Keys), the note in it is $kept :: $(Prefs-File)"
}

# The program under another file name, as a copy kept beside an older one.
if (Wanted 'renamed-exe') {
  "--- renamed-exe"
  Reset-State
  $a = Start-App
  $set = Set-All $a
  Start-Sleep 15
  Close-App $a
  $other = Join-Path (Split-Path $Exe) 'lc-renamed.exe'
  Copy-Item $Exe $other -Force
  $b = Start-App $other
  $got = Read-All $b
  Close-App $b
  Result 'renamed-exe' "port $($a.Port) -> $($b.Port) :: set $set :: after restart under another name $got :: config $(Config-Keys) :: storage of the first name [$(Storage)] :: $(Prefs-File)"
}

Stop-All
"done"
