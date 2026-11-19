function Rename-PagerArchiveAlias {
    <#
    .SYNOPSIS
    Renames an alias.

    .DESCRIPTION
    Gives an alias, chosen by capcode or pattern, or by name, a new name.
    Names are shown as they are now, everywhere, at once (R23a). The change
    is recorded in the alias's history as yours (R57). Alias names are
    unique, ignoring letter case (R27b): a name another alias has, in any
    case, is refused with an error naming that alias (ResourceExists,
    duplicate_name). An alias may change its own name's case.

    An alias piped from Get-PagerArchiveAlias, or any object with an Id, is
    chosen by its Id: exactly that alias, even when other aliases have the
    same name from before names were unique. Names typed or piped as text
    are matched as -Name says.
    -WhatIf and the confirmation name the alias with its capcode or pattern,
    name and Id.

    .EXAMPLE
    Rename-PagerArchiveAlias -Pattern 0300001 -NewName 'Brigade North'

    .EXAMPLE
    Rename-PagerArchiveAlias -Name 'Brigade' -NewName 'Brigade North' -PassThru

    .EXAMPLE
    Get-PagerArchiveAlias -Name 'Brigade' | Where-Object -Property Pattern -EQ '0300002' | Rename-PagerArchiveAlias -NewName 'Brigade South' -WhatIf

    What if: Performing the operation "Rename alias to 'Brigade South'" on target "0300002 (Brigade, Id 4)".

    Of the aliases called 'Brigade', renames only the one for 0300002.

    .EXAMPLE
    Get-PagerArchiveAlias -Name 'Old *' | Rename-PagerArchiveAlias -NewName { $_.Name -replace '^Old\s+' } -WhatIf

    What if: Performing the operation "Rename alias to 'Station 1'" on target "0100001 (Old Station 1, Id 1)".
    What if: Performing the operation "Rename alias to 'Station 2'" on target "0100002 (Old Station 2, Id 2)".

    Renames several aliases at once: the script block works out each
    alias's new name from the alias ($_). Set-PagerArchiveAlias doesn't
    rename.

    .INPUTS
    PagerArchive.Alias, or objects with an Id property, chosen by Id;
    objects with a Pattern property and no Id, by Pattern. System.String
    alias names by value.

    .OUTPUTS
    None, or PagerArchive.Alias with -PassThru.

    .LINK
    Set-PagerArchiveAlias

    .LINK
    Get-PagerArchiveAlias
    #>
    [CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium', DefaultParameterSetName = 'Id')]
    [OutputType('PagerArchive.Alias')]
    param(
        # The Id of the alias to rename, exactly. A piped object's Id property binds
        # here, and when it does, the Id chooses the alias and its Pattern and
        # Name are ignored. No wildcards.
        [Parameter(ValueFromPipelineByPropertyName, ParameterSetName = 'Pattern')]
        [Parameter(ValueFromPipelineByPropertyName, ParameterSetName = 'Name')]
        [Parameter(Mandatory, ValueFromPipelineByPropertyName, ParameterSetName = 'Id')]
        [PSDefaultValue(Help = 'none')]
        [long] $Id,

        # The capcode or pattern of the alias to rename, exactly. No
        # wildcards.
        [Parameter(Mandatory, Position = 0, ValueFromPipelineByPropertyName, ParameterSetName = 'Pattern')]
        [Alias('Capcode')]
        [ValidateNotNullOrEmpty()]
        [string] $Pattern,

        # The name of the alias to rename. No wildcards. A name several
        # aliases share from before names were unique matches each of them,
        # and all but the first are refused, as they can't share the new
        # name.
        [Parameter(Mandatory, ValueFromPipeline, ParameterSetName = 'Name')]
        [ValidateNotNullOrEmpty()]
        [string] $Name,

        # The new name, 1 to 100 characters. No wildcards. A script block
        # works out each piped item's new name from the item ($_), e.g.
        # { $_.Name -replace '^Old\s+' }. A piped object's NewName property
        # binds here too.
        [Parameter(Mandatory, Position = 1, ValueFromPipelineByPropertyName)]
        [ValidateNotNullOrEmpty()]
        [string] $NewName,

        # Output the renamed alias. Off by default.
        [switch] $PassThru
    )
    process {
        try {
            $escaped = if ($Name) { [System.Management.Automation.WildcardPattern]::Escape($Name) } else { $null }
            # An Id, when one is bound (a piped record), wins over its Pattern and Name.
            # An unbound [string] -Pattern is '', not $null: pass only the one given.
            $aliases = if ($Id) { @(Find-PaAlias -Id $Id) } elseif ($Pattern) { @(Find-PaAlias -Pattern $Pattern) } else { @(Find-PaAlias -Name $escaped) }
        } catch {
            $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $_.TargetObject))
            return
        }
        foreach ($alias in $aliases) {
            if (-not $PSCmdlet.ShouldProcess((Get-PaAliasTarget -Alias $alias), "Rename alias to '$NewName'")) {
                continue
            }
            try {
                $current = Invoke-PaApi -Method GET -Path "/api/v1/admin/aliases/$($alias.Id)"
                $body = @{ pattern = $current.pattern; name = $NewName; template_id = $current.template_id; ignored = [bool]$current.ignored }
                $null = Invoke-PaApi -Method PUT -Path "/api/v1/admin/aliases/$($alias.Id)" -Body $body
                if ($PassThru) {
                    Get-PaAliasList -Search $alias.Pattern | Where-Object -FilterScript { $_.Id -eq $alias.Id }
                }
            } catch {
                $PSCmdlet.WriteError((Copy-PaErrorRecord -ErrorRecord $_ -TargetObject $alias))
            }
        }
    }
}
