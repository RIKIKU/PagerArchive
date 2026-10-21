<#
.SYNOPSIS
Runs the module's Pester tests (meta-tests, PSScriptAnalyzer, behaviour and sign-in tests).

.DESCRIPTION
Used by CI and by hand: pwsh -NoProfile -File powershell/tests/Invoke-Tests.ps1
Needs Pester 5.9.1 and PSScriptAnalyzer 1.25.0 on PSModulePath. Exits 1 if
any test, block or file failed.
#>
[CmdletBinding()]
param(
    # Test files or folders. Default: every test here.
    [string[]] $Path = @($PSScriptRoot),

    # Write test results in NUnit XML to this file. Default: none.
    [string] $ResultPath
)
Import-Module -Name Pester -RequiredVersion 5.9.1 -ErrorAction Stop
Import-Module -Name PSScriptAnalyzer -RequiredVersion 1.25.0 -ErrorAction Stop
Write-Output -InputObject "PowerShell $($PSVersionTable.PSVersion) on $($PSVersionTable.OS)"
$configuration = New-PesterConfiguration
$configuration.Run.Path = $Path
$configuration.Run.PassThru = $true
$configuration.Output.Verbosity = 'Normal'
if ($ResultPath) {
    $configuration.TestResult.Enabled = $true
    $configuration.TestResult.OutputPath = $ResultPath
}
$result = Invoke-Pester -Configuration $configuration
Write-Output -InputObject ("Passed {0}, failed {1}, skipped {2}, failed blocks {3}, failed files {4}" -f $result.PassedCount, $result.FailedCount, $result.SkippedCount, $result.FailedBlocksCount, $result.FailedContainersCount)
if ($result.FailedCount -or $result.FailedBlocksCount -or $result.FailedContainersCount) {
    exit 1
}
