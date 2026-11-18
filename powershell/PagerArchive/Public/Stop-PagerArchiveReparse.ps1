function Stop-PagerArchiveReparse {
    <#
    .SYNOPSIS
    Stops the server's re-parse of stored messages.

    .DESCRIPTION
    Cancels the running or paused re-parse, as Stop on Administration,
    Re-parse does (R23c). The messages it has re-parsed stay so; the rest
    stay as they were parsed before. Every message is re-parsed or not,
    never half done, and the large events around the ones it did are rebuilt.
    It shows as stopped, even after a restart, until
    Start-PagerArchiveReparse runs it again from the beginning; changes made
    meanwhile wait for that.

    Refused (reparse_not_running) when no re-parse is running or paused.
    Asks for confirmation with -Confirm, and -WhatIf asks for nothing.

    .EXAMPLE
    Stop-PagerArchiveReparse

    .EXAMPLE
    Stop-PagerArchiveReparse -PassThru | Select-Object -Property State, Done, Total

    .INPUTS
    None.

    .OUTPUTS
    None, or PagerArchive.Reparse with -PassThru: the re-parse's state after
    the change. Its properties are Get-PagerArchiveReparse's.

    .LINK
    Start-PagerArchiveReparse

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
    if ($PSCmdlet.ShouldProcess('the re-parse of stored messages', 'Stop')) {
        Invoke-PaJobChange -Cmdlet $PSCmdlet -Job reparse -Action stop -PassThru:$PassThru
    }
}
