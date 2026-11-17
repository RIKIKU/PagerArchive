function Get-PagerArchiveReparse {
    <#
    .SYNOPSIS
    Gets the state of the server's re-parse of stored messages.

    .DESCRIPTION
    When an alias, template or profile changes, the server re-parses the
    stored messages it affects in the background (R23a); a profile change
    re-parses the whole archive, which takes hours on a large one. This says
    how that is going, as Administration, Re-parse does (R23c).

    State is idle (nothing to do; Done and Total are the last run's),
    running, paused (Resume-PagerArchiveReparse carries it on from where it
    got to, even after a restart) or stopped (Start-PagerArchiveReparse runs
    it again from the beginning). A restart or upgrade carries a running
    re-parse on from where it got to.

    .EXAMPLE
    Get-PagerArchiveReparse

    State   Done     Total Percent PerMinute TimeLeft StartedAt
    -----   ----     ----- ------- --------- -------- ---------
    running 1499231 3012877      49      2510 09:06:22 18/11/2026 6:12:40

    .EXAMPLE
    Get-PagerArchiveReparse | Select-Object -Property State, Reasons, Waiting

    Why the re-parse is running (its reasons, as codes such as
    profiles_changed), and how many changes wait for the next one.

    .INPUTS
    None.

    .OUTPUTS
    PagerArchive.Reparse. State (idle, running, paused or stopped), Done and
    Total (messages re-parsed and to do; the last run's while idle), Percent,
    PerMinute and TimeLeft (while it runs: its rate, and the time left at
    that rate, $null until some are done), StartedAt, ChangedAt (its last
    change of state) and FinishedAt, Reasons (codes, such as
    profiles_changed), Requests and Patterns (how many changes and capcode
    patterns it covers), and Waiting (changes waiting for the next one).
    Times are [datetimeoffset], or $null when there's none. Counts and
    codes only: never a page or a capcode.

    .LINK
    Suspend-PagerArchiveReparse

    .LINK
    Resume-PagerArchiveReparse

    .LINK
    Stop-PagerArchiveReparse

    .LINK
    Start-PagerArchiveReparse

    .LINK
    about_PagerArchive
    #>
    [CmdletBinding()]
    [OutputType('PagerArchive.Reparse')]
    param()
    try {
        Invoke-PaApi -Method GET -Path '/api/v1/admin/reparse' | ConvertTo-PaReparse
    } catch {
        $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $_))
    }
}
