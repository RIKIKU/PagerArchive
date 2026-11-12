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

    An alias piped from Get-PagerArchiveAlias, or any object with an Id, is
    chosen by its Id: exactly that alias, even when other aliases have the
    same name. Names typed or piped as text are matched as -Name says.
    -WhatIf and the confirmation name each alias with its capcode or pattern,
    name and Id.

    .EXAMPLE
    Remove-PagerArchiveAlias -Pattern 0100004

    .EXAMPLE
    Get-PagerArchiveAlias -Name 'Old *' | Remove-PagerArchiveAlias -WhatIf

    What if: Performing the operation "Delete alias" on target "0300009 (Old pager, Id 9)".

    .EXAMPLE
    Get-PagerArchiveAlias -Name 'Brigade' | Where-Object -Property Pattern -EQ '0300002' | Remove-PagerArchiveAlias

    Of the aliases called 'Brigade', deletes only the one for 0300002.

    .EXAMPLE
    Remove-PagerArchiveAlias -Id 12

    .INPUTS
    PagerArchive.Alias, or objects with an Id property, chosen by Id;
    objects with a Pattern property and no Id, by Pattern. System.String
    alias names by value.

    .OUTPUTS
    None, or PagerArchive.Alias (the deleted alias) with -PassThru.

    .LINK
    Get-PagerArchiveAlias

    .LINK
    New-PagerArchiveAlias
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'High', DefaultParameterSetName = 'Id')]
    [OutputType('PagerArchive.Alias')]
    param(
        # The Ids of the aliases to delete, exactly. A piped object's Id
        # property binds here, and when it does, the Id chooses the alias and
        # its Pattern and Name are ignored. No wildcards.
        [Parameter(ValueFromPipelineByPropertyName, ParameterSetName = 'Pattern')]
        [Parameter(ValueFromPipelineByPropertyName, ParameterSetName = 'Name')]
        [Parameter(Mandatory, ValueFromPipelineByPropertyName, ParameterSetName = 'Id')]
        [long[]] $Id,

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
            # An Id, when one is bound (a piped record), wins over its Pattern and Name.
            $aliases = if ($Id) { @(Find-PaAlias -Id $Id) } else { @(Find-PaAlias -Pattern $Pattern -Name $Name) }
        } catch {
            $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $_.TargetObject))
            return
        }
        foreach ($alias in $aliases) {
            if (-not $PSCmdlet.ShouldProcess((Get-PaAliasTarget -Alias $alias), 'Delete alias')) {
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
