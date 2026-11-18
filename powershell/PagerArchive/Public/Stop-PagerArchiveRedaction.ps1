function Stop-PagerArchiveRedaction {
    <#
    .SYNOPSIS
    Stops the server's retroactive redaction of stored messages.

    .DESCRIPTION
    Cancels the running or paused retroactive redaction, as Stop on
    Administration, Retroactive redaction does (R45a). The messages it did
    stay done; every message is done or not, its text and what's parsed from
    it together, and the large events around the ones it changed are
    rebuilt. It shows as stopped, even after a restart, until the next
    nightly run starts it again from the beginning (the messages it didn't
    reach still keep what the rules remove), or Start-PagerArchiveRedaction
    does sooner.

    Refused (redaction_not_running) when none is running or paused. Asks for
    confirmation with -Confirm, and -WhatIf asks for nothing.

    .EXAMPLE
    Stop-PagerArchiveRedaction

    .EXAMPLE
    Stop-PagerArchiveRedaction -PassThru | Select-Object -Property State, Done, Total

    .INPUTS
    None.

    .OUTPUTS
    None, or PagerArchive.Redaction with -PassThru: its state after the
    change. Its properties are Get-PagerArchiveRedaction's.

    .LINK
    Start-PagerArchiveRedaction

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
    if ($PSCmdlet.ShouldProcess('the retroactive redaction of stored messages', 'Stop')) {
        Invoke-PaJobChange -Cmdlet $PSCmdlet -Job redaction -Action stop -PassThru:$PassThru
    }
}
