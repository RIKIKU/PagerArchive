function Rename-PagerArchiveAlias {
    <#
    .SYNOPSIS
    Renames an alias.

    .DESCRIPTION
    Gives an alias, chosen by capcode or pattern, or by name, a new name.
    Names are shown as they are now, everywhere, at once (R23a). The change
    is recorded in the alias's history as yours (R57). The same name may be
    used for several capcodes (a brigade can have several pagers).

    An alias piped from Get-PagerArchiveAlias, or any object with an Id, is
    chosen by its Id: exactly that alias, even when other aliases have the
    same name. Names typed or piped as text are matched as -Name says.
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

        # The name of the alias to rename. No wildcards: a name used by
        # several aliases renames each of them.
        [Parameter(Mandatory, ValueFromPipeline, ParameterSetName = 'Name')]
        [ValidateNotNullOrEmpty()]
        [string] $Name,

        # The new name, 1 to 100 characters. No wildcards.
        [Parameter(Mandatory, Position = 1)]
        [ValidateNotNullOrEmpty()]
        [string] $NewName,

        # Output the renamed alias. Off by default.
        [switch] $PassThru
    )
    process {
        try {
            $escaped = if ($Name) { [System.Management.Automation.WildcardPattern]::Escape($Name) } else { $null }
            # An Id, when one is bound (a piped record), wins over its Pattern and Name.
            $aliases = if ($Id) { @(Find-PaAlias -Id $Id) } else { @(Find-PaAlias -Pattern $Pattern -Name $escaped) }
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
