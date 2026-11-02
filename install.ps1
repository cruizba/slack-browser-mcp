# Installs the release binary on Windows and registers it with every agent it finds.
#
# Re-runnable: it replaces the binary in place and re-registers the MCP servers, so an
# upgrade does not leave an agent pointing at a stale path.
#
# The download is verified against the published checksum before anything is installed.
# An installer that runs whatever it fetched is a supply chain with no lock on it.

[CmdletBinding()]
param(
  [string]$Tag = "",
  [string]$BinDir = "$env:LOCALAPPDATA\Programs\slack-browser-mcp",
  [string]$ShareDir = "$env:LOCALAPPDATA\slack-browser-mcp"
)

$ErrorActionPreference = "Stop"

$repo = if ($env:SLACK_BROWSER_MCP_REPO) { $env:SLACK_BROWSER_MCP_REPO } else { "cruizba/slack-browser-mcp" }
$base = if ($env:SLACK_BROWSER_MCP_BASE) { $env:SLACK_BROWSER_MCP_BASE } else { "https://github.com/$repo/releases" }

function Step($message) { Write-Host "`n==> $message" }

Step "Detecting this machine"
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  "AMD64" { "amd64" }
  "ARM64" { "arm64" }
  default { throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}
Write-Host "    windows/$arch"

if (-not $Tag) {
  Step "Finding the latest release"
  # The redirect the releases page leaves behind names the newest tag, which avoids
  # needing a token or the API's rate limit.
  $response = Invoke-WebRequest -Uri "$base/latest" -MaximumRedirection 0 -SkipHttpErrorCheck -ErrorAction SilentlyContinue
  $location = $response.Headers.Location
  if (-not $location) { throw "could not read the latest release tag from $base/latest" }
  $Tag = ($location -split "/")[-1]
  Write-Host "    $Tag"
}

$version = $Tag.TrimStart("v")
$archive = "slack-browser-mcp_${version}_windows_${arch}.zip"
$url = "$base/download/$Tag/$archive"
$checksumsUrl = "$base/download/$Tag/slack-browser-mcp_${version}_checksums.txt"

$work = Join-Path $env:TEMP ("slack-browser-install-" + [guid]::NewGuid().ToString("n"))
New-Item -ItemType Directory -Path $work | Out-Null

try {
  Step "Downloading $Tag"
  Invoke-WebRequest -Uri $url -OutFile (Join-Path $work $archive)
  Invoke-WebRequest -Uri $checksumsUrl -OutFile (Join-Path $work "checksums.txt")

  Step "Verifying the checksum"
  $want = (Get-Content (Join-Path $work "checksums.txt") | Where-Object { $_ -match "\s\*?$([regex]::Escape($archive))$" }) -split "\s+" | Select-Object -First 1
  if (-not $want) { throw "the checksums for $Tag do not mention $archive" }

  $got = (Get-FileHash -Algorithm SHA256 (Join-Path $work $archive)).Hash.ToLower()
  if ($got -ne $want.ToLower()) { throw "the download does not match its published checksum; nothing was installed" }
  Write-Host "    ok"

  Step "Installing"
  New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
  Expand-Archive -Path (Join-Path $work $archive) -DestinationPath $work -Force

  $binary = Get-ChildItem -Path $work -Recurse -Filter "slack-browser.exe" | Select-Object -First 1
  if (-not $binary) { throw "the archive did not carry a slack-browser.exe" }

  $target = Join-Path $BinDir "slack-browser.exe"
  # Windows refuses to replace a file that is running, so the old one is moved aside.
  if (Test-Path $target) { Move-Item -Force $target "$target.old" }
  Copy-Item $binary.FullName $target

  # The forum surface is a second entry point on the same code.
  Copy-Item $target (Join-Path $BinDir "slack-browser-forum.exe") -Force
  Write-Host "    $target"

  $commands = Get-ChildItem -Path $work -Recurse -Filter "*.md" | Where-Object { $_.DirectoryName -like "*commands*" }
  if ($commands) {
    New-Item -ItemType Directory -Force -Path (Join-Path $ShareDir "commands") | Out-Null
    $commands | ForEach-Object { Copy-Item $_.FullName (Join-Path $ShareDir "commands") -Force }
  }

  Step "Registering with the agents in PATH"
  $agents = 0
  if (Get-Command claude -ErrorAction SilentlyContinue) {
    $agents = 1
    claude mcp remove --scope user slack 2>$null | Out-Null
    claude mcp add --scope user slack -- $target serve | Out-Null
    claude mcp remove --scope user forum 2>$null | Out-Null
    claude mcp add --scope user forum -- (Join-Path $BinDir "slack-browser-forum.exe") | Out-Null
    Write-Host "    claude: MCP servers 'slack' and 'forum' registered"
  }
  if (Get-Command pi -ErrorAction SilentlyContinue) {
    $agents = 1
    pi mcp remove slack 2>$null | Out-Null
    pi mcp add slack -- $target serve | Out-Null
    pi mcp remove forum 2>$null | Out-Null
    pi mcp add forum -- (Join-Path $BinDir "slack-browser-forum.exe") | Out-Null
    Write-Host "    pi: MCP servers 'slack' and 'forum' registered"
  }
  if ($agents -eq 0) {
    Write-Host "    no pi or claude in PATH; register the server by hand:"
    Write-Host "      $target serve"
  }

  $path = [Environment]::GetEnvironmentVariable("Path", "User")
  if ($path -notlike "*$BinDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$path;$BinDir", "User")
    Write-Host "`n    added $BinDir to your user PATH; open a new terminal"
  }

  Step "Next"
  Write-Host "    sign in         $target login"
  Write-Host "    check it        $target status"
  Write-Host "    fill history    $target sync"
  Write-Host "    bind a forum    $target agent-forum add <channel>   # then register <name>"
  Write-Host "`n    restart your agent so it loads the new MCP server"
}
finally {
  Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
