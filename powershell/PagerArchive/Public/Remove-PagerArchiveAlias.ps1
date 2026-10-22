function Remove-PagerArchiveAlias {
    <#
    .SYNOPSIS
    Deletes aliases.

    .DESCRIPTION
    Deletes aliases chosen by capcode or pattern, or by name. Their pages
    stay, and show the capcode, or whatever other alias covers them.
    Waiting requests for an alias close as "alias deleted", integrations
    that selected a one-capcode alias select its capcode instead, and the
    alias's history is kept (R53, R55, R57). Asks for confirmation unless
    -Confirm:$false is given.

    .EXAMPLE
    Remove-PagerArchiveAlias -Pattern 0100004

    .EXAMPLE
    Get-PagerArchiveAlias -Name 'Old *' | Remove-PagerArchiveAlias -WhatIf

    What if: Performing the operation "Delete alias" on target "0300009 (Old pager)".

    .INPUTS
    PagerArchive.Alias, or objects with a Pattern property. System.String
    alias names by value.

    .OUTPUTS
    None, or PagerArchive.Alias (the deleted alias) with -PassThru.

    .LINK
    Get-PagerArchiveAlias

    .LINK
    New-PagerArchiveAlias
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'High', DefaultParameterSetName = 'Pattern')]
    [OutputType('PagerArchive.Alias')]
    param(
        # Capcodes or patterns of the aliases to delete, exactly. No
        # wildcards.
        [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName, ParameterSetName = 'Pattern')]
        [Alias('Capcode')]
        [ValidateNotNullOrEmpty()]
        [string[]] $Pattern,

        # Names of the aliases to delete. Accepts wildcards, matched
        # case-insensitively.
        [Parameter(Mandatory, ValueFromPipeline, ParameterSetName = 'Name')]
        [SupportsWildcards()]
        [ValidateNotNullOrEmpty()]
        [string[]] $Name,

        # Output each deleted alias. Off by default.
        [switch] $PassThru
    )
    process {
        try {
            $aliases = @(Find-PaAlias -Pattern $Pattern -Name $Name)
        } catch {
            $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $_.TargetObject))
            return
        }
        foreach ($alias in $aliases) {
            if (-not $PSCmdlet.ShouldProcess("$($alias.Pattern) ($($alias.Name))", 'Delete alias')) {
                continue
            }
            try {
                $deleted = Invoke-PaApi -Method DELETE -Path "/api/v1/admin/aliases/$($alias.Id)"
                if ($deleted.integrations -gt 0) {
                    Write-Warning -Message "$($deleted.integrations) integration(s) selected $($alias.Pattern); they now select the capcode, or (for a pattern) no longer select it."
                }
                if ($PassThru) {
                    $alias
                }
            } catch {
                $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $alias))
            }
        }
    }
}
