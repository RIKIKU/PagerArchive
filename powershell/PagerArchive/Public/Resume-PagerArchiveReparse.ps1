function Resume-PagerArchiveReparse {
    <#
    .SYNOPSIS
    Carries on the server's paused re-parse of stored messages.

    .DESCRIPTION
    Carries a paused re-parse on from where it got to, as Resume on
    Administration, Re-parse does (R23c).

    Refused when it isn't paused (reparse_not_paused), and when it was
    stopped (reparse_stopped): a stopped re-parse only starts again from the
    beginning, with Start-PagerArchiveReparse. Asks for confirmation with
    -Confirm, and -WhatIf asks for nothing.

    .EXAMPLE
    Resume-PagerArchiveReparse

    .EXAMPLE
    Resume-PagerArchiveReparse -PassThru | Select-Object -Property State, Done, Total

    .INPUTS
    None.

    .OUTPUTS
    None, or PagerArchive.Reparse with -PassThru: the re-parse's state after
    the change. Its properties are Get-PagerArchiveReparse's.

    .LINK
    Suspend-PagerArchiveReparse

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
    if ($PSCmdlet.ShouldProcess('the re-parse of stored messages', 'Resume')) {
        Invoke-PaReparseChange -Cmdlet $PSCmdlet -Action resume -PassThru:$PassThru
    }
}
