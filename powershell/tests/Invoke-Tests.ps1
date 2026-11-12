<#
.SYNOPSIS
Runs the module's Pester tests (meta-tests, PSScriptAnalyzer, behaviour and sign-in tests).

.DESCRIPTION
Used by CI and by hand: pwsh -NoProfile -File powershell/tests/Invoke-Tests.ps1
Needs Pester 5.9.1 and PSScriptAnalyzer 1.25.0 on PSModulePath. Exits 1 if
any test, block or file failed.

With -RetryFailed (CI, layer 2 of its retries, ADR 0025), up to 5 tests that
failed are run once more. One that passes then is reported as flaky (a
warning, "flaky: <test>", and a line in the job's summary), so the retry
never hides it; one that fails again fails the run. A block or file that
failed, or more than 5 tests, means something broke: nothing is retried.
#>
[CmdletBinding()]
param(
    # Test files or folders. Default: every test here.
    [string[]] $Path = @($PSScriptRoot),

    # Write test results in NUnit XML to this file. Default: none.
    [string] $ResultPath,

    # Run up to 5 failed tests once more, reporting any that pass as flaky.
    [switch] $RetryFailed
)
Import-Module -Name Pester -RequiredVersion 5.9.1 -ErrorAction Stop
Import-Module -Name PSScriptAnalyzer -RequiredVersion 1.25.0 -ErrorAction Stop
Write-Output -InputObject "PowerShell $($PSVersionTable.PSVersion) on $($PSVersionTable.OS)"
$configuration = New-PesterConfiguration
$configuration.Run.Path = $Path
$configuration.Run.PassThru = $true
# Detailed: each test's name as it finishes, so a hang shows where it is.
$configuration.Output.Verbosity = 'Detailed'
if ($ResultPath) {
    $configuration.TestResult.Enabled = $true
    $configuration.TestResult.OutputPath = $ResultPath
}
$result = Invoke-Pester -Configuration $configuration
Write-Output -InputObject ("Passed {0}, failed {1}, skipped {2}, failed blocks {3}, failed files {4}" -f $result.PassedCount, $result.FailedCount, $result.SkippedCount, $result.FailedBlocksCount, $result.FailedContainersCount)
if ($result.FailedBlocksCount -or $result.FailedContainersCount) {
    exit 1
}
if (-not $result.FailedCount) {
    exit 0
}
if (-not $RetryFailed -or $result.FailedCount -gt 5) {
    exit 1
}

# Pester's FullName filter is the test's path joined with dots, matched with -like.
$failed = @($result.Failed | ForEach-Object -Process { $_.Path -join '.' } | Select-Object -Unique)
Write-Output -InputObject "Trying the failed test(s) once more: $($failed -join '; ')"
$again = New-PesterConfiguration
$again.Run.Path = $Path
$again.Run.PassThru = $true
$again.Output.Verbosity = 'Detailed'
$again.Filter.FullName = @($failed | ForEach-Object -Process { [WildcardPattern]::Escape($_) })
$retry = Invoke-Pester -Configuration $again
Write-Output -InputObject ("Tried again: passed {0}, failed {1}, failed blocks {2}, failed files {3}" -f $retry.PassedCount, $retry.FailedCount, $retry.FailedBlocksCount, $retry.FailedContainersCount)
if ($retry.FailedCount -or $retry.FailedBlocksCount -or $retry.FailedContainersCount -or -not $retry.PassedCount) {
    exit 1
}
$lines = @($failed | ForEach-Object -Process { "Pester: $_ (PowerShell $($PSVersionTable.PSVersion) on $($PSVersionTable.Platform)): failed, then passed when run again" })
foreach ($line in $lines) {
    Write-Output -InputObject "::warning title=Flaky::flaky: $($line -replace '%', '%25' -replace "`r", '%0D' -replace "`n", '%0A')"
}
if ($env:GITHUB_STEP_SUMMARY) {
    @('', '### Passed only on a retry', '', 'Each is a flake to look into: it failed, then passed when tried again (ADR 0025).', '') + @($lines | ForEach-Object -Process { "- flaky: $_" }) |
        Out-File -FilePath $env:GITHUB_STEP_SUMMARY -Append -Encoding utf8
}
exit 0
