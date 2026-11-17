function Start-PagerArchiveRedaction {
    <#
    .SYNOPSIS
    Runs the server's retroactive redaction of stored messages again from the beginning.

    .DESCRIPTION
    Checks every stored message again against the redaction rules in use,
    from the beginning, as Start again on Administration, Retroactive
    redaction does (R45a), whatever its state: a stopped one, a paused one, a
    running one, one that finished, or none yet.

    Asks for confirmation with -Confirm, and -WhatIf asks for nothing.

    .EXAMPLE
    Start-PagerArchiveRedaction

    .EXAMPLE
    Start-PagerArchiveRedaction -PassThru

    State   Done   Total Percent Redacted PerMinute TimeLeft StartedAt
    -----   ----   ----- ------- -------- --------- -------- ---------
    running    0 3012877       0        0         0          18/11/2026 9:40:02

    .INPUTS
    None.

    .OUTPUTS
    None, or PagerArchive.Redaction with -PassThru: its state after the
    change. Its properties are Get-PagerArchiveRedaction's.

    .LINK
    Stop-PagerArchiveRedaction

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
    if ($PSCmdlet.ShouldProcess('the retroactive redaction of stored messages', 'Start again from the beginning')) {
        Invoke-PaJobChange -Cmdlet $PSCmdlet -Job redaction -Action start -PassThru:$PassThru
    }
}
