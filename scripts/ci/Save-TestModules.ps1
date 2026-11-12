<#
.SYNOPSIS
Saves the PowerShell modules the tests need, at pinned versions, for CI.

.DESCRIPTION
Pester 5.9.1 and PSScriptAnalyzer 1.25.0 (docs/powershell-guidelines.md),
and Microsoft.PowerShell.SecretManagement 1.1.2 and
Microsoft.PowerShell.SecretStore 1.0.6 for the vault tests. They go into
Path, which the tests put on PSModulePath; nothing is installed for the
user.

The Gallery sometimes answers a request with a 404 or a 403 that works when
tried again, so each module gets up to 3 tries, 15 seconds and then 45 apart
(Invoke-Retried.ps1, ADR 0025). Each failed try is a warning naming the
module and the try, a module saved only on a retry is reported as flaky,
and a module that fails all 3 stops the script with the last error.
#>
[CmdletBinding()]
param(
    # The folder to save them in.
    [Parameter(Mandatory)]
    [string] $Path
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
. (Join-Path -Path $PSScriptRoot -ChildPath 'Invoke-Retried.ps1')
$null = New-Item -ItemType Directory -Path $Path -Force
foreach ($module in @(
        @{ Name = 'Pester'; Version = '5.9.1' }
        @{ Name = 'PSScriptAnalyzer'; Version = '1.25.0' }
        @{ Name = 'Microsoft.PowerShell.SecretManagement'; Version = '1.1.2' }
        @{ Name = 'Microsoft.PowerShell.SecretStore'; Version = '1.0.6' }
    )) {
    Invoke-Retried -What "Saving $($module.Name) $($module.Version)" -ScriptBlock {
        Save-PSResource -Name $module.Name -Version $module.Version -Path $Path -Repository PSGallery -TrustRepository -ErrorAction Stop
        if (-not (Test-Path -LiteralPath (Join-Path -Path $Path -ChildPath $module.Name -AdditionalChildPath $module.Version) -PathType Container)) {
            throw "Save-PSResource reported no error, but $($module.Name) $($module.Version) isn't in $Path."
        }
    }
}
Get-ChildItem -Path $Path -Directory | ForEach-Object -Process { "$($_.Name) $((Get-ChildItem -Path $_.FullName -Directory).Name)" }
