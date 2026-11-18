function Suspend-PagerArchiveRedaction {
    <#
    .SYNOPSIS
    Pauses the server's retroactive redaction of stored messages.

    .DESCRIPTION
    Pauses the running retroactive redaction, as Pause on Administration,
    Retroactive redaction does (R45a). It stops after the message in hand;
    every message is done or not, its text and what's parsed from it
    together. It stays paused, even after a restart, an upgrade or a nightly
    run, until Resume-PagerArchiveRedaction carries it on from where it got
    to. Until then, the messages it hasn't reached keep what the rules remove.

    Refused (redaction_not_running) when none is running. Asks for
    confirmation with -Confirm, and -WhatIf asks for nothing.

    .EXAMPLE
    Suspend-PagerArchiveRedaction

    Pauses it, for instance while the server is busy.

    .EXAMPLE
    Suspend-PagerArchiveRedaction -PassThru

    State  Done     Total Percent Redacted PerMinute TimeLeft StartedAt
    -----  ----     ----- ------- -------- --------- -------- ---------
    paused 1499500 3012877      49     2211         0          18/11/2026 3:00:12

    .INPUTS
    None.

    .OUTPUTS
    None, or PagerArchive.Redaction with -PassThru: its state after the
    change. Its properties are Get-PagerArchiveRedaction's.

    .LINK
    Resume-PagerArchiveRedaction

    .LINK
    Get-PagerArchiveRedaction

    .LINK
    about_PagerArchive
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
    [OutputType('PagerArchive.Redaction')]
    param(
        # Output the state. Off by default.
        [switch] $PassThru
    )
    if ($PSCmdlet.ShouldProcess('the retroactive redaction of stored messages', 'Pause')) {
        Invoke-PaJobChange -Cmdlet $PSCmdlet -Job redaction -Action pause -PassThru:$PassThru
    }
}
