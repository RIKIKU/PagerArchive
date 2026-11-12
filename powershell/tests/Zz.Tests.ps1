# Throwaway (never merged): fails the first time it runs in a job, to show
# Invoke-Tests.ps1 -RetryFailed and the flaky report (ADR 0025).
Describe 'zz throwaway' {
    It 'fails the first time only' {
        $marker = Join-Path -Path ([System.IO.Path]::GetTempPath()) -ChildPath "zz-flaky-$env:GITHUB_RUN_ID-$env:GITHUB_RUN_ATTEMPT"
        if (-not (Test-Path -LiteralPath $marker)) {
            $null = New-Item -ItemType File -Path $marker
            throw 'The first try fails on purpose'
        }
    }
}
