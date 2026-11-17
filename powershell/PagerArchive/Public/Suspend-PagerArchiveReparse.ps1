function Suspend-PagerArchiveReparse {
    <#
    .SYNOPSIS
    Pauses the server's re-parse of stored messages.

    .DESCRIPTION
    Pauses the running re-parse, as Pause on Administration, Re-parse does
    (R23c). It stops after the message in hand; every message is re-parsed
    or not, never half done. It stays paused, even after a restart or an
    upgrade, until Resume-PagerArchiveReparse carries it on from where it
    got to. Changes made meanwhile wait for the next re-parse.

    Refused (reparse_not_running) when no re-parse is running. Asks for
    confirmation with -Confirm, and -WhatIf asks for nothing.

    .EXAMPLE
    Suspend-PagerArchiveReparse

    Pauses the re-parse, for instance while the server is busy.

    .EXAMPLE
    Suspend-PagerArchiveReparse -PassThru

    State  Done     Total Percent PerMinute TimeLeft StartedAt
    -----  ----     ----- ------- --------- -------- ---------
    paused 1499500 3012877      49         0          18/11/2026 6:12:40

    .INPUTS
    None.

    .OUTPUTS
    None, or PagerArchive.Reparse with -PassThru: the re-parse's state after
    the change. Its properties are Get-PagerArchiveReparse's.

    .LINK
    Resume-PagerArchiveReparse

    .LINK
    Get-PagerArchiveReparse

    .LINK
    about_PagerArchive
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
    [OutputType('PagerArchive.Reparse')]
    param(
        # Output the re-parse's state. Off by default.
        [switch] $PassThru
    )
    if ($PSCmdlet.ShouldProcess('the re-parse of stored messages', 'Pause')) {
        Invoke-PaJobChange -Cmdlet $PSCmdlet -Job reparse -Action pause -PassThru:$PassThru
    }
}
