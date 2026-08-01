# Run your own Yarr.It, on this machine.
#
#     powershell -ExecutionPolicy Bypass -File .\Install-Local-Server.ps1
#
# This is the Windows counterpart of deploy/selfhost.sh and makes the same
# decisions: the search API, the peer bridge and the torrent gateway all run as
# you, on your own box, serving the same web app. Your apps point at your
# machine and never touch anyone else's.
#
# What you still need is an indexer. Yarr.It does not scrape sites itself -- it
# asks Prowlarr, which is the piece that knows how to talk to each tracker and
# holds the credentials. If you already run Prowlarr (most *arr users do), give
# this its address and API key.
#
# It does NOT need Administrator. Everything is installed under %LOCALAPPDATA%,
# nothing is registered with Windows, and uninstalling is deleting one folder.
# The single step that would need elevation is the firewall rule that lets the
# TVs in the house reach this box; if it cannot be added, the exact command is
# printed rather than the install failing or quietly skipping it.
#
# Nothing is installed on your behalf. Missing tools are named, with where to
# get them, and the script stops.

[CmdletBinding()]
param(
  [string] $InstallDir  = (Join-Path $env:LOCALAPPDATA 'Yarrit'),
  [string] $Repo        = 'https://github.com/BlizzHacker/yarr-it.git',
  [string] $Branch      = 'feat/universal-source-layer',

  # The web app and the search API are bound to every interface, not to
  # loopback. A Roku or an Xbox is a separate device on the LAN, and a service
  # bound to 127.0.0.1 is unreachable from every machine except this one --
  # which is the entire point of running it here.
  [string] $Bind        = '0.0.0.0',

  [int]    $WebPort     = 8800,
  [int]    $Port        = 8802,
  [int]    $BridgePort  = 8801,
  [int]    $GatewayPort = 8900,

  # The rebuild is the slow part and is usually not what you came back for.
  [switch] $SkipBuild
)

$ErrorActionPreference = 'Stop'

$Src  = Join-Path $InstallDir 'src'
$Bin  = Join-Path $InstallDir 'bin'
$Www  = Join-Path $InstallDir 'www'
$Data = Join-Path $InstallDir 'data'
$Conf = Join-Path $InstallDir 'config.env'

function Say($message)  { Write-Host ''; Write-Host "==> $message" -ForegroundColor Cyan }
function Note($message) { Write-Host "    $message" }

function Have($name) {
  $found = Get-Command $name -ErrorAction SilentlyContinue
  return ($null -ne $found)
}

function Write-TextFile($path, $text) {
  # UTF-8 with no BOM. Set-Content and Out-File both write one in Windows
  # PowerShell, and a BOM at the top of config.env becomes part of the first
  # key's name when the file is read back -- so PROWLARR_URL silently stops
  # being PROWLARR_URL.
  [System.IO.File]::WriteAllText($path, $text, (New-Object System.Text.UTF8Encoding($false)))
}

function Copy-IfPresent($from, $to) {
  if (Test-Path $from) {
    Copy-Item -Path $from -Destination $to -Force
    return $true
  }
  return $false
}

# --- what is already here -----------------------------------------------------

Say 'checking what is already here'

$missing = @()
if (-not (Have 'git'))  { $missing += 'git' }
if (-not (Have 'go'))   { $missing += 'go' }
if (-not (Have 'node')) { $missing += 'node' }

if ($missing.Count -gt 0) {
  Note "missing: $($missing -join ', ')"
  Note ''
  Note 'Install them yourself and run this again:'
  Note '  Go 1.24 or newer   https://go.dev/dl/                  winget install GoLang.Go'
  Note '  Node.js LTS        https://nodejs.org/                 winget install OpenJS.NodeJS.LTS'
  Note '  Git for Windows    https://git-scm.com/download/win    winget install Git.Git'
  Note ''
  Note 'Open a new terminal afterwards. An installer updates PATH for new'
  Note 'processes only, so this window will keep reporting them missing.'
  exit 1
}

$goVersionText = (& go version)
$goMatch = [regex]::Match([string]$goVersionText, 'go(\d+)\.(\d+)')
if (-not $goMatch.Success) {
  Note "could not read a version out of: $goVersionText"
  exit 1
}
$goMajor = [int]$goMatch.Groups[1].Value
$goMinor = [int]$goMatch.Groups[2].Value
if ($goMajor -lt 1 -or ($goMajor -eq 1 -and $goMinor -lt 24)) {
  # Every module here declares `go 1.24`. An older toolchain does not fail with
  # anything as clear as "too old" -- it reports a module version complaint that
  # reads like a broken checkout, which is a bad afternoon.
  Note "Go $goMajor.$goMinor is too old; 1.24 or newer is required."
  Note 'Take it from https://go.dev/dl/ -- side-by-side installs are fine.'
  exit 1
}

$nodeVersionText = (& node -v)
Note "go $goMajor.$goMinor, node $nodeVersionText"

# --- source -------------------------------------------------------------------

Say "fetching the source into $Src"

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path $Bin        | Out-Null
New-Item -ItemType Directory -Force -Path $Www        | Out-Null
New-Item -ItemType Directory -Force -Path $Data       | Out-Null

if (Test-Path (Join-Path $Src '.git')) {
  & git -C $Src fetch --quiet --depth 1 origin $Branch
  if ($LASTEXITCODE -ne 0) { throw "git fetch failed ($LASTEXITCODE)" }
  & git -C $Src reset --hard --quiet FETCH_HEAD
  if ($LASTEXITCODE -ne 0) { throw "git reset failed ($LASTEXITCODE)" }
  Note 'updated'
} else {
  & git clone --quiet --depth 1 --branch $Branch $Repo $Src
  if ($LASTEXITCODE -ne 0) { throw "git clone failed ($LASTEXITCODE)" }
  Note 'cloned'
}

$StreamDir = Join-Path $Src 'stream'
$WebSrc    = Join-Path $StreamDir 'web'

# --- services -----------------------------------------------------------------

if ($SkipBuild) {
  Say 'skipping the build (-SkipBuild)'
} else {
  Say 'building the services'
  Note 'the first gateway build pulls a few hundred modules; give it a few minutes'

  # Static binaries, matching selfhost.sh. Nothing here needs cgo, and without
  # this a machine with a half-configured gcc on PATH builds differently from
  # one without.
  $env:CGO_ENABLED = '0'

  foreach ($svc in @('search', 'bridge', 'gateway')) {
    $svcDir = Join-Path $StreamDir $svc
    if (-not (Test-Path $svcDir)) {
      Note "no $svc directory in this checkout; skipped"
      continue
    }
    $out = Join-Path $Bin "mw-$svc.exe"
    Push-Location $svcDir
    try {
      & go build -trimpath -o $out .
      if ($LASTEXITCODE -ne 0) { throw "go build failed for $svc ($LASTEXITCODE)" }
    } finally {
      Pop-Location
    }
    Note "mw-$svc.exe"
  }

  # --- web app ----------------------------------------------------------------

  Say 'building the web app'

  Push-Location $WebSrc
  try {
    & npm install --silent --no-audit --no-fund
    if ($LASTEXITCODE -ne 0) { throw "npm install failed ($LASTEXITCODE)" }

    # The platform binary rather than node_modules\.bin\esbuild.cmd, which is a
    # batch shim -- the same choice web/deploy.sh makes on Windows.
    $esbuild = Join-Path $WebSrc 'node_modules\@esbuild\win32-x64\esbuild.exe'
    if (-not (Test-Path $esbuild)) {
      $esbuild = Join-Path $WebSrc 'node_modules\.bin\esbuild.cmd'
    }
    if (-not (Test-Path $esbuild)) { throw "esbuild not found under $WebSrc\node_modules" }

    & $esbuild 'src/main.js' '--bundle' '--format=esm' '--outfile=dist/app.js' `
      '--define:global=globalThis' '--external:./webtorrent.min.js' '--minify'
    if ($LASTEXITCODE -ne 0) { throw "esbuild failed ($LASTEXITCODE)" }
  } finally {
    Pop-Location
  }

  Get-ChildItem -Path (Join-Path $WebSrc 'dist') -Filter '*.js' |
    ForEach-Object { Copy-Item -Path $_.FullName -Destination $Www -Force }

  # Both are ES modules copied unmodified out of the webtorrent package; they
  # are external to the bundle and must not be bundled.
  Copy-IfPresent (Join-Path $WebSrc 'node_modules\webtorrent\dist\webtorrent.min.js') $Www | Out-Null
  Copy-IfPresent (Join-Path $WebSrc 'node_modules\webtorrent\dist\sw.min.js')         $Www | Out-Null

  # index.html and the web manifest carry cache-busting placeholders that the
  # real deploy fills with content hashes. A local install rebuilds in place and
  # serves no-store, so a fixed stamp is honest and keeps the icon URLs valid.
  $stamp = 'local'
  $indexHtml = (Get-Content -Raw (Join-Path $WebSrc 'index.html')).Replace('__BUILD__', $stamp).Replace('__ICON__', $stamp)
  Write-TextFile (Join-Path $Www 'index.html') $indexHtml

  if (Test-Path (Join-Path $WebSrc 'manifest.webmanifest')) {
    $manifest = (Get-Content -Raw (Join-Path $WebSrc 'manifest.webmanifest')).Replace('__ICON__', $stamp)
    Write-TextFile (Join-Path $Www 'manifest.webmanifest') $manifest
  }

  # The page asks for icon-192.local.png because of the substitution above, and
  # installed PWAs ask for the bare name. Both are the same file.
  foreach ($size in @('192', '512')) {
    $icon = Join-Path $WebSrc "dist\icon-$size.png"
    if (Test-Path $icon) {
      Copy-Item $icon (Join-Path $Www "icon-$size.$stamp.png") -Force
      Copy-Item $icon (Join-Path $Www "icon-$size.png") -Force
    }
  }

  # Raw modules, served so the console can be used for diagnostics against the
  # same code the bundle was built from.
  foreach ($module in @('engine.js', 'bridge-peer.js', 'tracker-udp.js', 'dht.js', 'bencode.js')) {
    Copy-IfPresent (Join-Path $WebSrc "src\$module") $Www | Out-Null
  }

  # Linked from the footer. Without them those links 404 on a local copy.
  foreach ($page in @('privacy.html', 'sam.html')) {
    Copy-IfPresent (Join-Path $WebSrc $page) $Www | Out-Null
  }

  Note "web app in $Www"
}

# The front door is preferred from the checkout, so a re-run picks up changes
# to it. Running from a working tree that has not been pushed yet, the clone
# will not have it -- so fall back to the copy sitting beside this script.
$serveSrc = Join-Path $StreamDir 'installers\serve-local.mjs'
if (-not (Test-Path $serveSrc)) {
  $serveSrc = Join-Path $PSScriptRoot 'serve-local.mjs'
}
if (-not (Test-Path $serveSrc)) {
  throw "serve-local.mjs not found in the checkout or next to this script"
}
Copy-Item $serveSrc (Join-Path $InstallDir 'serve-local.mjs') -Force

# --- configuration ------------------------------------------------------------

Say 'configuration'

if (-not (Test-Path $Conf)) {
  $confTemplate = @'
# Where your Prowlarr lives, and its API key (Settings -> General in Prowlarr).
# mw-search REFUSES TO START without the key -- it is the one setting that is
# not optional, because with no indexer there is nothing to search.
PROWLARR_URL=http://127.0.0.1:9696
PROWLARR_API_KEY=

# Optional artwork. Without them the catalogue still works, just plainer.
TMDB_API_KEY=
IGDB_CLIENT_ID=
IGDB_CLIENT_SECRET=

# Sign-in is off unless you fill these in. Leave blank to run open, which is
# the sensible default for a server only you can reach. Note that the watchlist
# and resume points need a signed-in user to belong to, so with these blank the
# server answers "accounts are not configured" and the app simply draws no
# shelf.
SSO_CLIENT_ID=
SSO_CLIENT_SECRET=
SESSION_SECRET=
AUTH_SCOPE=off

# Where your watchlist and resume points are kept.
LIBRARY_PATH={{LIBRARY}}
'@
  Write-TextFile $Conf $confTemplate.Replace('{{LIBRARY}}', (Join-Path $InstallDir 'library.json'))
  Note "wrote $Conf"
} else {
  Note "keeping the $Conf you already have"
}

# --- start script -------------------------------------------------------------

# Ports and paths are baked in at install time, exactly as selfhost.sh bakes
# them into start.sh, so the runtime file has no configuration of its own to
# disagree with.
$startTemplate = @'
# Start Yarr.It on this machine.
#
# Written by Install-Local-Server.ps1 -- re-running the installer overwrites it.
# Settings live in config.env; ports and paths are fixed here.
#
#     powershell -ExecutionPolicy Bypass -File .\Start-Yarrit.ps1
#
# Ctrl+C stops everything. Nothing is installed as a service: if you want this
# on boot, point a Task Scheduler task at this file with "run whether the user
# is logged on or not".

$ErrorActionPreference = 'Stop'

$Root        = '{{ROOT}}'
$Bind        = '{{BIND}}'
$WebPort     = {{WEBPORT}}
$Port        = {{PORT}}
$BridgePort  = {{BRIDGEPORT}}
$GatewayPort = {{GATEWAYPORT}}

$Bin   = Join-Path $Root 'bin'
$Www   = Join-Path $Root 'www'
$Data  = Join-Path $Root 'data'
$Conf  = Join-Path $Root 'config.env'
$Serve = Join-Path $Root 'serve-local.mjs'

if (-not (Test-Path $Conf)) { throw "no config.env in $Root; re-run Install-Local-Server.ps1" }

# config.env is the same KEY=VALUE file selfhost.sh writes, so a config can be
# carried between a Linux box and this one unchanged.
#
# SetEnvironmentVariable rather than Set-Item: a blank value is legal and common
# here, and Set-Item refuses an empty string outright.
foreach ($line in Get-Content $Conf) {
  $trimmed = $line.Trim()
  if ($trimmed -eq '') { continue }
  if ($trimmed.StartsWith('#')) { continue }
  $split = $trimmed.IndexOf('=')
  if ($split -lt 1) { continue }
  $name  = $trimmed.Substring(0, $split).Trim()
  $value = $trimmed.Substring($split + 1).Trim()
  [Environment]::SetEnvironmentVariable($name, $value, 'Process')
}

if (-not $env:PROWLARR_API_KEY) {
  # mw-search calls log.Fatal on a missing key and is gone a second later. Left
  # to itself that looks like the app half-working: the page loads, the shelves
  # are empty, and the only evidence scrolled past during startup.
  Write-Host ''
  Write-Host 'PROWLARR_API_KEY is empty and mw-search will not start without it.' -ForegroundColor Yellow
  Write-Host "  Put your key in $Conf"
  Write-Host '  Prowlarr: Settings -> General -> API Key'
  exit 1
}

if (-not $env:PROWLARR_URL) {
  # The compiled-in default points at the address the public instance uses over
  # its tunnel, which is somebody else's LAN. A local guess is the honest one.
  $env:PROWLARR_URL = 'http://127.0.0.1:9696'
  Write-Host "PROWLARR_URL was blank; assuming $env:PROWLARR_URL"
}

$env:YARRIT_WWW           = $Www
$env:YARRIT_WEB_ADDR      = ('{0}:{1}' -f $Bind, $WebPort)
$env:YARRIT_SEARCH_ADDR   = ('127.0.0.1:{0}' -f $Port)
$env:YARRIT_BRIDGE_ADDR   = ('127.0.0.1:{0}' -f $BridgePort)

$started = @()

function Launch($label, $exe, $argv) {
  # $exe is either a built binary given by full path or a command that has to be
  # found on PATH, and Test-Path cannot answer the second question -- it reports
  # "node" as missing on a machine where node is perfectly well installed.
  if ([System.IO.Path]::IsPathRooted($exe)) {
    if (-not (Test-Path $exe)) { throw "$label is missing: $exe -- re-run Install-Local-Server.ps1" }
  } elseif (-not (Get-Command $exe -ErrorAction SilentlyContinue)) {
    throw "$label needs $exe on PATH"
  }
  $p = Start-Process -FilePath $exe -ArgumentList $argv -NoNewWindow -PassThru
  # Touching .Handle caches the process handle now, while the process is
  # certainly alive. Without it .ExitCode reads back empty once the process is
  # gone -- so the one line that explains why everything stopped says
  # "exited with code " and nothing else.
  $null = $p.Handle
  $script:started += [pscustomobject]@{ Name = $label; Process = $p }
  Write-Host "    $label (pid $($p.Id))"
}

Write-Host ''
Write-Host '==> starting' -ForegroundColor Cyan

# Everything that starts a process lives inside this try. Outside it, a service
# that fails to launch leaves the ones already running orphaned -- they keep
# holding their ports, and the next attempt fails on "address already in use"
# for a reason that has scrolled off the screen.
try {
  # The relay only ever answers the front door, so it stays on loopback.
  Launch 'mw-bridge' (Join-Path $Bin 'mw-bridge.exe') @(
    '-addr', ('127.0.0.1:{0}' -f $BridgePort),
    '-state', ('"{0}"' -f (Join-Path $Root 'bridge-budget.json'))
  )

  # The search API is reached directly by TV clients, which have no front door
  # to go through, so it binds the LAN like the gateway does.
  Launch 'mw-search' (Join-Path $Bin 'mw-search.exe') @(
    '-addr', ('{0}:{1}' -f $Bind, $Port),
    '-prowlarr', ('"{0}"' -f $env:PROWLARR_URL)
  )

  # The gateway exists to be reached by the televisions on this network. Bound
  # to loopback it would be useless to every device except this PC, which is
  # the one device that does not need it.
  Launch 'mw-gateway' (Join-Path $Bin 'mw-gateway.exe') @(
    '-addr', ('0.0.0.0:{0}' -f $GatewayPort),
    '-data', ('"{0}"' -f $Data)
  )

  Launch 'front door' 'node' @(('"{0}"' -f $Serve))

  Write-Host ''
  Write-Host "  Open  http://localhost:$WebPort/" -ForegroundColor Green
  Write-Host ''
  Write-Host '  On a TV, enter this one address:'
  Write-Host ("    http://{0}:{1}" -f [System.Net.Dns]::GetHostName(), $WebPort)
  Write-Host ("    (the raw API is on :{0}; the gateway on :{1})" -f $Port, $GatewayPort) -ForegroundColor DarkGray
  Write-Host ''
  Write-Host '  Ctrl+C stops all four.' -ForegroundColor DarkGray

  while ($true) {
    Start-Sleep -Seconds 1
    foreach ($entry in $started) {
      if ($entry.Process.HasExited) {
        # One service exiting is the whole thing being broken, and the useful
        # information is which one -- so say it rather than leaving three
        # survivors running against a missing fourth.
        Write-Host ''
        Write-Host ("$($entry.Name) exited with code $($entry.Process.ExitCode)") -ForegroundColor Yellow
        return
      }
    }
  }
} finally {
  foreach ($entry in $started) {
    if (-not $entry.Process.HasExited) {
      Stop-Process -Id $entry.Process.Id -Force -ErrorAction SilentlyContinue
    }
  }
}
'@

$startPath = Join-Path $InstallDir 'Start-Yarrit.ps1'
$startText = $startTemplate.Replace('{{ROOT}}', $InstallDir).
                            Replace('{{BIND}}', $Bind).
                            Replace('{{WEBPORT}}', [string]$WebPort).
                            Replace('{{PORT}}', [string]$Port).
                            Replace('{{BRIDGEPORT}}', [string]$BridgePort).
                            Replace('{{GATEWAYPORT}}', [string]$GatewayPort)
Write-TextFile $startPath $startText
Note "wrote $startPath"

# --- firewall -----------------------------------------------------------------

# Windows blocks inbound connections from the LAN by default, and the failure
# mode is genuinely nasty: this PC works perfectly, and every TV in the house
# times out with no message that names a cause. Adding the rule needs elevation,
# which this script deliberately does not demand -- so it tries, and prints the
# command if it cannot.

Say 'letting the rest of the network reach this box'

$ruleName  = 'Yarr.It (self-hosted)'
$rulePorts = "$WebPort,$Port,$GatewayPort"
$ruleCommand = "New-NetFirewallRule -DisplayName '$ruleName' -Direction Inbound -Action Allow -Protocol TCP -LocalPort $rulePorts -Profile Private"

$existingRule = $null
if (Have 'Get-NetFirewallRule') {
  $existingRule = Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue
}

if ($existingRule) {
  Note "the rule '$ruleName' is already there"
} elseif (-not (Have 'New-NetFirewallRule')) {
  Note 'New-NetFirewallRule is not available on this system. In an elevated prompt:'
  Note "  netsh advfirewall firewall add rule name=`"$ruleName`" dir=in action=allow protocol=TCP localport=$rulePorts"
} else {
  $added = $false
  try {
    New-NetFirewallRule -DisplayName $ruleName -Direction Inbound -Action Allow `
      -Protocol TCP -LocalPort $WebPort, $Port, $GatewayPort -Profile Private | Out-Null
    $added = $true
  } catch {
    Note "could not add it: $($_.Exception.Message)"
  }
  if ($added) {
    Note "opened TCP $rulePorts for private networks"
    # A rule scoped to Private does nothing on a network Windows has classified
    # as Public, and Windows classifies unknown networks that way by default.
    Note 'if your network is set to Public in Windows, change it to Private or'
    Note 'the rule will not apply'
  } else {
    Note 'run this once in an ELEVATED PowerShell (it is the only step that'
    Note 'needs Administrator, and everything except other devices reaching'
    Note 'this box works without it):'
    Note ''
    Note "  $ruleCommand"
  }
}

# --- done ---------------------------------------------------------------------

Say 'done'

Write-Host ''
Write-Host "  1. Put your Prowlarr address and API key in:"
Write-Host "       $Conf"
Write-Host '     Prowlarr shows the key under Settings -> General -> API Key.'
Write-Host ''
Write-Host '  2. Start it:'
Write-Host "       powershell -ExecutionPolicy Bypass -File `"$startPath`""
Write-Host ''
Write-Host '  3. Open:'
Write-Host "       http://localhost:$WebPort/" -ForegroundColor Green
Write-Host ''
Write-Host '     Leave the Server box in Settings blank. Blank means "whichever'
Write-Host '     server delivered this page", which is now this machine -- so a'
Write-Host '     self-hosted copy needs no configuration at all.'
Write-Host ''
Write-Host '  4. Point the TVs in the house at this machine. One address does it:'
Write-Host ("       http://{0}:{1}" -f [System.Net.Dns]::GetHostName(), $WebPort)
Write-Host ''
Write-Host '     A Roku, an Xbox or a TV cannot run any of this itself, so one'
Write-Host '     always-on machine on the network serves all of them. On Roku the'
Write-Host '     address goes in the channel Settings screen, and the playback'
Write-Host '     gateway is derived from it automatically.'

if ($GatewayPort -ne 8900) {
  Write-Host ''
  Write-Host "     Note: you chose gateway port $GatewayPort. The Roku channel derives" -ForegroundColor Yellow
  Write-Host '     the gateway from the server address on port 8900 and cannot be told' -ForegroundColor Yellow
  Write-Host '     otherwise, so Roku playback will not find it.' -ForegroundColor Yellow
}
Write-Host ''
Write-Host '  Nothing here phones home. yarrit.com is only the default for clients'
Write-Host '  that have not been told otherwise.'
Write-Host ''
Write-Host '  Use a VPN. The gateway joins swarms from this machine, on your own'
Write-Host '  connection and your own IP, exactly as any torrent client does.' -ForegroundColor DarkGray
Write-Host ''
