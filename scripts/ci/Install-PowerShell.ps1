<#
.SYNOPSIS
Installs one PowerShell release from Microsoft's GitHub releases, for CI.

.DESCRIPTION
Downloads the release archive for this OS (Windows zip, or Linux x64
tar.gz), checks it against the release's published SHA-256 hashes, and
unpacks it into Destination. "newest" is the newest stable release, read
from Microsoft's build information (https://aka.ms/pwsh-buildinfo-stable).
Writes the path of the installed pwsh.

.EXAMPLE
pwsh -File scripts/ci/Install-PowerShell.ps1 -Version 7.6.0 -Destination "$env:RUNNER_TEMP/pwsh-7.6.0"
#>
[CmdletBinding()]
param(
    # A version such as 7.6.0, or newest.
    [Parameter(Mandatory)]
    [string] $Version,

    # The folder to unpack into.
    [Parameter(Mandatory)]
    [string] $Destination
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
if ($Version -eq 'newest') {
    $Version = (Invoke-RestMethod -Uri 'https://aka.ms/pwsh-buildinfo-stable').ReleaseTag.TrimStart('v')
}
$file = if ($IsWindows) { "PowerShell-$Version-win-x64.zip" } else { "powershell-$Version-linux-x64.tar.gz" }
$base = "https://github.com/PowerShell/PowerShell/releases/download/v$Version"
$archive = Join-Path -Path ([System.IO.Path]::GetTempPath()) -ChildPath $file
Invoke-WebRequest -Uri "$base/$file" -OutFile $archive
# hashes.sha256 is UTF-16 with a byte-order mark: "<hash> *<file>" per line.
$hashes = (Invoke-WebRequest -Uri "$base/hashes.sha256").Content
if ($hashes -is [byte[]]) {
    $encoding = if ($hashes.Length -ge 2 -and $hashes[0] -eq 0xFF -and $hashes[1] -eq 0xFE) { [System.Text.Encoding]::Unicode } else { [System.Text.Encoding]::UTF8 }
    $hashes = $encoding.GetString($hashes).TrimStart([char]0xFEFF)
}
$expected = ($hashes -split "`n" | Where-Object -FilterScript { $_ -match [regex]::Escape($file) } | Select-Object -First 1) -split '\s+' | Select-Object -First 1
$actual = (Get-FileHash -Path $archive -Algorithm SHA256).Hash
if (-not $expected -or $actual -ne $expected.ToUpperInvariant()) {
    throw "SHA-256 of $file is $actual; the release says $expected."
}
$null = New-Item -ItemType Directory -Path $Destination -Force
if ($IsWindows) {
    Expand-Archive -Path $archive -DestinationPath $Destination -Force
    $pwsh = Join-Path -Path $Destination -ChildPath 'pwsh.exe'
} else {
    tar -xzf $archive -C $Destination
    $pwsh = Join-Path -Path $Destination -ChildPath 'pwsh'
    chmod +x $pwsh
}
Write-Output -InputObject $pwsh
