<#
.SYNOPSIS
Saves the PowerShell modules the tests need, at pinned versions, for CI.

.DESCRIPTION
Pester 5.9.1 and PSScriptAnalyzer 1.25.0 (docs/powershell-guidelines.md),
and Microsoft.PowerShell.SecretManagement 1.1.2 and
Microsoft.PowerShell.SecretStore 1.0.6 for the vault tests. They go into
Path, which the tests put on PSModulePath; nothing is installed for the
user.
#>
[CmdletBinding()]
param(
    # The folder to save them in.
    [Parameter(Mandatory)]
    [string] $Path
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$null = New-Item -ItemType Directory -Path $Path -Force
foreach ($module in @(
        @{ Name = 'Pester'; Version = '5.9.1' }
        @{ Name = 'PSScriptAnalyzer'; Version = '1.25.0' }
        @{ Name = 'Microsoft.PowerShell.SecretManagement'; Version = '1.1.2' }
        @{ Name = 'Microsoft.PowerShell.SecretStore'; Version = '1.0.6' }
    )) {
    Save-PSResource -Name $module.Name -Version $module.Version -Path $Path -Repository PSGallery -TrustRepository
}
Get-ChildItem -Path $Path -Directory | ForEach-Object -Process { "$($_.Name) $((Get-ChildItem -Path $_.FullName -Directory).Name)" }
