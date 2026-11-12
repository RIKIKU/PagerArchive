<#
.SYNOPSIS
Layer 1 of CI's retries (ADR 0025), for the PowerShell scripts: dot-source it.

.DESCRIPTION
Invoke-Retried runs a script block up to 3 times, waiting 15 seconds and
then 45 between tries, for what fails because of the network and never
because of the change: a download, the PowerShell Gallery. Each failed try
is a warning naming the try. A block that passes only on a retry is
reported as flaky (a warning annotation, "flaky: <what>", and a line in the
job's summary, as scripts/ci/flaky.sh does), written to the host so a
caller's captured output stays the block's own. One that fails every try
throws its last error.

.EXAMPLE
. ./scripts/ci/Invoke-Retried.ps1
Invoke-Retried -What 'Saving Pester 5.9.1' -ScriptBlock { Save-PSResource -Name Pester -Version 5.9.1 -Path $path -ErrorAction Stop }
#>
function Invoke-Retried {
    [CmdletBinding()]
    param(
        # What's being done, for the warnings and the flaky report.
        [Parameter(Mandatory)]
        [string] $What,

        # The step to try. It fails by throwing.
        [Parameter(Mandatory)]
        [scriptblock] $ScriptBlock,

        [int] $Tries = 3,

        # Seconds before the second try; each wait after is three times the last.
        [int] $Wait = 15
    )
    for ($try = 1; ; $try++) {
        try {
            $output = & $ScriptBlock
            if ($try -gt 1) {
                $line = "$What (passed on try $try of $Tries)"
                Write-Host -Object "::warning title=Flaky::flaky: $($line -replace '%', '%25' -replace "`r", '%0D' -replace "`n", '%0A')"
                if ($env:GITHUB_STEP_SUMMARY) {
                    $heading = '### Passed only on a retry'
                    $lines = @("- flaky: $line")
                    if (-not ((Test-Path -LiteralPath $env:GITHUB_STEP_SUMMARY) -and (Select-String -LiteralPath $env:GITHUB_STEP_SUMMARY -Pattern "^$([regex]::Escape($heading))$" -Quiet))) {
                        $lines = @('', $heading, '', 'Each is a flake to look into: it failed, then passed when tried again (ADR 0025).', '') + $lines
                    }
                    $lines | Out-File -FilePath $env:GITHUB_STEP_SUMMARY -Append -Encoding utf8
                }
            }
            return $output
        } catch {
            if ($try -ge $Tries) {
                Write-Warning -Message "$What failed on try $try of ${Tries}, the last: $($_.Exception.Message)"
                throw
            }
            Write-Warning -Message "$What failed on try $try of ${Tries}; trying again in $Wait seconds: $($_.Exception.Message)"
            Start-Sleep -Seconds $Wait
            $Wait *= 3
        }
    }
}
