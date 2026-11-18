function Resume-PagerArchiveRedaction {
    <#
    .SYNOPSIS
    Carries on the server's paused retroactive redaction of stored messages.

    .DESCRIPTION
    Carries a paused retroactive redaction on from where it got to, as Resume
    on Administration, Retroactive redaction does (R45a).

    Refused when it isn't paused (redaction_not_paused), and when it was
    stopped (redaction_stopped): a stopped one only starts again from the
    beginning, at the next nightly run or with Start-PagerArchiveRedaction.
    Asks for confirmation with -Confirm, and -WhatIf asks for nothing.

    .EXAMPLE
    Resume-PagerArchiveRedaction

    .EXAMPLE
    Resume-PagerArchiveRedaction -PassThru | Select-Object -Property State, Done, Total

    .INPUTS
    None.

    .OUTPUTS
    None, or PagerArchive.Redaction with -PassThru: its state after the
    change. Its properties are Get-PagerArchiveRedaction's.

    .LINK
    Suspend-PagerArchiveRedaction

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
    if ($PSCmdlet.ShouldProcess('the retroactive redaction of stored messages', 'Resume')) {
        Invoke-PaJobChange -Cmdlet $PSCmdlet -Job redaction -Action resume -PassThru:$PassThru
    }
}
