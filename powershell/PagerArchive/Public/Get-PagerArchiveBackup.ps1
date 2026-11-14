function Get-PagerArchiveBackup {
    <#
    .SYNOPSIS
    Gets the state of the server's backups.

    .DESCRIPTION
    Each night the server's backup service dumps PagerArchive's and
    Authentik's databases, then restores the dumps into a throwaway database
    and compares them (R86, R86a). This says how that is going, as
    Administration, Backups does: State is ok, waiting (no backup yet on a
    new install), failed (the last one failed, and Error says why), overdue
    (no good backup for 36 hours) or off (the server has no backup service).

    Run says what the backup service is doing now: idle, queued (a backup was
    asked for with Start-PagerArchiveBackup or in the browser, and starts
    within a minute), running, or off (backups can't be asked for on this
    server).

    .EXAMPLE
    Get-PagerArchiveBackup

    State Run  LastGoodAt          LastRunAt           Nights Error
    ----- ---  ----------          ---------           ------ -----
    ok    idle 14/11/2026 4:03:12  14/11/2026 4:03:12      14

    .EXAMPLE
    Get-PagerArchiveBackup | Select-Object -Property LastGoodNight, LastGoodBytes, LastGoodDumpSeconds, LastGoodCheckSeconds

    The last good backup's night, size in bytes, and how long its dumps and
    check took.

    .INPUTS
    None.

    .OUTPUTS
    PagerArchive.Backup. State, Run, RequestedAt (while a backup asked for
    waits to start), RunningSince (while one runs), LastRunAt and LastGoodAt
    (the last backup, and the last good one), LastGoodNight, LastGoodBytes,
    LastGoodDumpSeconds, LastGoodCheckSeconds, Nights (how many are kept) and
    Error (why the last one failed). Times are [datetimeoffset], or $null
    when there's none.

    .LINK
    Start-PagerArchiveBackup

    .LINK
    about_PagerArchive
    #>
    [CmdletBinding()]
    [OutputType('PagerArchive.Backup')]
    param()
    try {
        Invoke-PaApi -Method GET -Path '/api/v1/admin/backup' | ConvertTo-PaBackup
    } catch {
        $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $_))
    }
}
