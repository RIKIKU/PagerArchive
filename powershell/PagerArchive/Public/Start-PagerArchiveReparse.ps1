function Start-PagerArchiveReparse {
    <#
    .SYNOPSIS
    Runs the server's re-parse of stored messages again from the beginning.

    .DESCRIPTION
    Runs the re-parse (the last one, when none is running) again from the
    beginning, as Start again on Administration, Re-parse does (R23c),
    whatever its state: a stopped one, a paused one, a running one or one
    that finished. Changes waiting for the next re-parse are taken in.

    Refused (reparse_none) when there has never been a re-parse. Asks for
    confirmation with -Confirm, and -WhatIf asks for nothing.

    .EXAMPLE
    Start-PagerArchiveReparse

    .EXAMPLE
    Start-PagerArchiveReparse -PassThru

    State   Done   Total Percent PerMinute TimeLeft StartedAt
    -----   ----   ----- ------- --------- -------- ---------
    running    0 3012877       0         0          18/11/2026 9:40:02

    .INPUTS
    None.

    .OUTPUTS
    None, or PagerArchive.Reparse with -PassThru: the re-parse's state after
    the change. Its properties are Get-PagerArchiveReparse's.

    .LINK
    Stop-PagerArchiveReparse

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
    if ($PSCmdlet.ShouldProcess('the re-parse of stored messages', 'Start again from the beginning')) {
        Invoke-PaReparseChange -Cmdlet $PSCmdlet -Action start -PassThru:$PassThru
    }
}
