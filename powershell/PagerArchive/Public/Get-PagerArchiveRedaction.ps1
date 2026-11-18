function Get-PagerArchiveRedaction {
    <#
    .SYNOPSIS
    Gets the state of the server's retroactive redaction of stored messages.

    .DESCRIPTION
    When the network profile's redaction rules change, the nightly run checks
    every stored message against them, and writes each changed message's new
    text and its re-parse together (R45a); on a large archive that takes
    hours. This says how that is going, as Administration, Retroactive
    redaction does.

    State is idle (nothing to do; Done and Total are the last run's),
    running, paused (Resume-PagerArchiveRedaction carries it on from where it
    got to, even after a restart; the nightly run leaves it paused) or
    stopped (the next nightly run, or Start-PagerArchiveRedaction, runs it
    again from the beginning). A restart or upgrade carries a running one on
    from where it got to.

    .EXAMPLE
    Get-PagerArchiveRedaction

    State   Done     Total Percent Redacted PerMinute TimeLeft StartedAt
    -----   ----     ----- ------- -------- --------- -------- ---------
    running 1499231 3012877      49     2211     41000 00:36:43 18/11/2026 3:00:12

    .EXAMPLE
    Get-PagerArchiveRedaction | Select-Object -Property State, Reason, Pending

    Why the run started (rules_changed or started_again), and whether the
    rules in use are still to be applied to every stored message.

    .INPUTS
    None.

    .OUTPUTS
    PagerArchive.Redaction. State (idle, running, paused or stopped), Done
    and Total (messages checked and to check; the last run's while idle),
    Percent, Redacted (messages whose text it changed), PerMinute and
    TimeLeft (while it runs: its rate, and the time left at that rate, $null
    until some are done), StartedAt, ChangedAt (its last change of state) and
    FinishedAt, Reason (rules_changed or started_again) and Pending (the
    rules in use aren't yet applied to every stored message). Times are
    [datetimeoffset], or $null when there's none. Counts and codes only:
    never a message, a rule or a capcode.

    .LINK
    Suspend-PagerArchiveRedaction

    .LINK
    Resume-PagerArchiveRedaction

    .LINK
    Stop-PagerArchiveRedaction

    .LINK
    Start-PagerArchiveRedaction

    .LINK
    about_PagerArchive
    #>
    [CmdletBinding()]
    [OutputType('PagerArchive.Redaction')]
    param()
    try {
        Invoke-PaApi -Method GET -Path '/api/v1/admin/redaction' | ConvertTo-PaRedaction
    } catch {
        $PSCmdlet.ThrowTerminatingError((Copy-PaErrorRecord -ErrorRecord $_))
    }
}
